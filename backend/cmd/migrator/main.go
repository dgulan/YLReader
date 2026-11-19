// Package main 是 YLReader 数据库迁移与旧版 Reader / Legado 书源导入 CLI 入口。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/migration"
	"yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

const maxImportFileBytes = 32 << 20

var targetOverrideEnvVars = []string{
	"PGHOST",
	"PGHOSTADDR",
	"PGPORT",
	"PGDATABASE",
	"PGSERVICE",
	"PGSERVICEFILE",
}

var isolatedTestDBPattern = regexp.MustCompile(`^yreader_a1_iso_[a-z0-9_]{1,24}$`)

type importCommandOptions struct {
	User      string
	File      string
	Overwrite string
	DryRun    bool
	ConfirmDB string
}

type importCLIReport struct {
	DryRun bool `json:"dry_run"`
	*repository.ImportReport
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func hasTargetOverrideEnv(lookup func(string) (string, bool)) bool {
	for _, key := range targetOverrideEnvVars {
		if _, ok := lookup(key); ok {
			return true
		}
	}
	return false
}

func sameTargetEndpoints(cfg *pgxpool.Config) bool {
	c := cfg.ConnConfig
	for _, f := range c.Fallbacks {
		if f.Host != c.Host || f.Port != c.Port {
			return false
		}
	}
	return true
}

func isLocalTestTarget(cfg *pgxpool.Config) bool {
	c := cfg.ConnConfig
	if c.Host != "127.0.0.1" && c.Host != "::1" && c.Host != "localhost" {
		return false
	}
	if !sameTargetEndpoints(cfg) {
		return false
	}
	return c.Database == "yreader_test" || isolatedTestDBPattern.MatchString(c.Database)
}

func explicitTarget(raw string) bool {
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || strings.Contains(u.Host, ",") || u.Hostname() == "" || u.Port() == "" {
			return false
		}
		portNum, perr := strconv.Atoi(u.Port())
		if perr != nil || portNum < 1 || portNum > 65535 {
			return false
		}
		dbname := strings.TrimPrefix(u.Path, "/")
		if dbname == "" || strings.Contains(dbname, "/") || strings.Contains(raw, "%2f") || strings.Contains(raw, "%2F") {
			return false
		}
		for key := range u.Query() {
			switch strings.ToLower(key) {
			case "host", "hostaddr", "port", "dbname", "service", "options":
				return false
			}
		}
		return true
	}
	token := regexp.MustCompile(`(?s)^\s*([a-z_]+)\s*=\s*('(?:[^'\\]|\\.)*'|(?:[^\s\\]|\\.)*)`)
	seen := map[string]bool{}
	allowedKeys := map[string]bool{
		"host": true, "port": true, "dbname": true, "user": true, "password": true,
		"sslmode": true, "connect_timeout": true, "application_name": true,
	}
	for strings.TrimSpace(raw) != "" {
		m := token.FindStringSubmatch(raw)
		if m == nil || len(m[0]) == 0 || seen[m[1]] || !allowedKeys[m[1]] {
			return false
		}
		seen[m[1]] = true
		raw = raw[len(m[0]):]
		if raw != "" && !strings.ContainsAny(raw[:1], " \t\r\n\v\f") {
			return false
		}
	}
	return seen["host"] && seen["port"] && seen["dbname"]
}

func authorizeSchemaUpTarget(raw, confirmDB string, lookup func(string) (string, bool)) (*pgxpool.Config, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("DATABASE_URL required")
	}
	if hasTargetOverrideEnv(lookup) {
		return nil, fmt.Errorf("conflicting PG* environment variables are forbidden for explicit database writes")
	}
	if !explicitTarget(raw) {
		return nil, fmt.Errorf("explicit unambiguous host, port and database required")
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL")
	}
	if !sameTargetEndpoints(cfg) || cfg.ConnConfig.Host == "" || cfg.ConnConfig.Port == 0 || cfg.ConnConfig.Database == "" {
		return nil, fmt.Errorf("explicit unambiguous host, port and database required")
	}
	confirmDB = strings.TrimSpace(confirmDB)
	if confirmDB == "" {
		if !isLocalTestTarget(cfg) {
			return nil, fmt.Errorf("database write target denied; pass --production-confirm-db=<dbname> for non-test targets")
		}
		return cfg, nil
	}
	if cfg.ConnConfig.Database != confirmDB {
		return nil, fmt.Errorf("production confirmation mismatch: --production-confirm-db does not match DATABASE_URL database")
	}
	return cfg, nil
}

func validateConfirmDBFlag(args []string, confirm string) (string, error) {
	trimmed := strings.TrimSpace(confirm)
	for _, a := range args {
		if a == "--production-confirm-db" || strings.HasPrefix(a, "--production-confirm-db=") || a == "-production-confirm-db" || strings.HasPrefix(a, "-production-confirm-db=") {
			if trimmed == "" {
				return "", fmt.Errorf("--production-confirm-db must be a non-empty database name")
			}
		}
	}
	return trimmed, nil
}

func parseSchemaUpArgs(args []string) (confirmDB string, err error) {
	fs := flag.NewFlagSet("schema up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	confirm := fs.String("production-confirm-db", "", "exact database name confirmation for production upgrades")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("usage: migrator schema up [--production-confirm-db=<dbname>]")
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("usage: migrator schema up [--production-confirm-db=<dbname>]")
	}
	return validateConfirmDBFlag(args, *confirm)
}

func parseImportSourcesArgs(args []string) (importCommandOptions, error) {
	fs := flag.NewFlagSet("import-legado-sources", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	user := fs.String("user", "", "target username")
	file := fs.String("file", "", "path to Legado source JSON file")
	overwrite := fs.String("overwrite", "skip", "duplicate handling: skip or update")
	dryRun := fs.Bool("dry-run", false, "validate and simulate import inside a rolled-back transaction")
	confirm := fs.String("production-confirm-db", "", "exact database name confirmation for non-test targets")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return importCommandOptions{}, fmt.Errorf("usage: migrator import-legado-sources --user=<username> --file=<path> [--overwrite=skip|update] [--dry-run] [--production-confirm-db=<dbname>]")
	}
	confirmDB, err := validateConfirmDBFlag(args, *confirm)
	if err != nil {
		return importCommandOptions{}, err
	}
	opts := importCommandOptions{
		User:      strings.TrimSpace(*user),
		File:      strings.TrimSpace(*file),
		Overwrite: strings.TrimSpace(*overwrite),
		DryRun:    *dryRun,
		ConfirmDB: confirmDB,
	}
	if opts.User == "" || opts.File == "" {
		return importCommandOptions{}, fmt.Errorf("usage: migrator import-legado-sources --user=<username> --file=<path> [--overwrite=skip|update] [--dry-run] [--production-confirm-db=<dbname>]")
	}
	if opts.Overwrite != "skip" && opts.Overwrite != "update" {
		return importCommandOptions{}, fmt.Errorf("invalid --overwrite %q: must be skip or update", opts.Overwrite)
	}
	return opts, nil
}

func parseLegadoImportData(data []byte) ([]json.RawMessage, error) {
	if len(data) > maxImportFileBytes {
		return nil, fmt.Errorf("import file exceeds %d bytes", maxImportFileBytes)
	}
	trimmed := bytes.TrimSpace(data)
	// Strip UTF-8 BOM if exported on Windows editors.
	trimmed = bytes.TrimPrefix(trimmed, []byte("\xef\xbb\xbf"))
	trimmed = bytes.TrimSpace(trimmed)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("import file is empty")
	}
	var sources []json.RawMessage
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &sources); err != nil {
			return nil, fmt.Errorf("invalid Legado JSON array: %w", err)
		}
	case '{':
		if !json.Valid(trimmed) {
			return nil, fmt.Errorf("invalid Legado JSON object")
		}
		sources = []json.RawMessage{json.RawMessage(trimmed)}
	default:
		return nil, fmt.Errorf("Legado import payload must be a JSON array or object")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("import file contains no sources")
	}
	return sources, nil
}

func readLegadoImportFile(path string) ([]json.RawMessage, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat import file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("import path %q is a directory", path)
	}
	if info.Size() > maxImportFileBytes {
		return nil, fmt.Errorf("import file exceeds %d bytes", maxImportFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open import file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImportFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read import file: %w", err)
	}
	return parseLegadoImportData(data)
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: migrator <schema status|check|up [--production-confirm-db=<dbname>]> | <import-legado-sources --user=<username> --file=<path> [--dry-run] [--production-confirm-db=<dbname>]>")
	}
	switch args[0] {
	case "schema":
		return runSchema(args[1:])
	case "import-legado-sources":
		return runImportLegadoSources(args[1:])
	default:
		return fmt.Errorf("usage: migrator <schema status|check|up [--production-confirm-db=<dbname>]> | <import-legado-sources --user=<username> --file=<path> [--dry-run] [--production-confirm-db=<dbname>]>")
	}
}

func runSchema(args []string) error {
	if len(args) == 0 || (args[0] != "status" && args[0] != "check" && args[0] != "up") {
		return fmt.Errorf("usage: migrator schema status|check|up [--production-confirm-db=<dbname>]")
	}
	sub := args[0]
	if sub != "up" && len(args) != 1 {
		return fmt.Errorf("usage: migrator schema %s", sub)
	}
	var confirmDB string
	if sub == "up" {
		var err error
		confirmDB, err = parseSchemaUpArgs(args[1:])
		if err != nil {
			return err
		}
	}

	raw := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if raw == "" {
		return fmt.Errorf("DATABASE_URL required")
	}
	var cfg *pgxpool.Config
	var err error
	if sub == "up" {
		cfg, err = authorizeSchemaUpTarget(raw, confirmDB, os.LookupEnv)
		if err != nil {
			return err
		}
	} else {
		cfg, err = pgxpool.ParseConfig(raw)
		if err != nil {
			return fmt.Errorf("invalid DATABASE_URL")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("cannot initialize database pool")
	}
	defer pool.Close()

	switch sub {
	case "status":
		entries, err := migration.Status(ctx, pool)
		if err != nil {
			return fmt.Errorf("cannot read migration status")
		}
		return json.NewEncoder(os.Stdout).Encode(entries)
	case "check":
		if err = repository.CheckSchema(ctx, pool); err != nil {
			return fmt.Errorf("schema check failed: %w", err)
		}
	case "up":
		if err = migration.Up(ctx, pool); err != nil {
			return fmt.Errorf("schema migration failed: %w", err)
		}
		if err = repository.CheckSchema(ctx, pool); err != nil {
			return fmt.Errorf("post-migration check failed: %w", err)
		}
	}
	fmt.Println("schema " + sub + ": ok")
	return nil
}

func runImportLegadoSources(args []string) error {
	opts, err := parseImportSourcesArgs(args)
	if err != nil {
		return err
	}
	sources, err := readLegadoImportFile(opts.File)
	if err != nil {
		return err
	}

	cfg, err := authorizeSchemaUpTarget(os.Getenv("DATABASE_URL"), opts.ConfirmDB, os.LookupEnv)
	if err != nil {
		return err
	}

	ring, err := crypto.LoadKeyringFromEnv()
	if err != nil {
		return fmt.Errorf("load header keyring (set YREADER_HEADER_KEY_<KID> to encrypt imported headers): %w", err)
	}
	crypto.SetDefaultKeyring(ring)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("cannot initialize database pool")
	}
	defer pool.Close()

	if err := repository.CheckSchema(ctx, pool); err != nil {
		return fmt.Errorf("schema check failed: %w", err)
	}

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE lower(username) = lower($1)`, opts.User).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("user %q not found", opts.User)
		}
		return fmt.Errorf("lookup user %q: %w", opts.User, err)
	}

	svc := service.NewSourceService(pool)
	rep, err := svc.Import(ctx, userID, sources, repository.ImportOptions{
		Overwrite: opts.Overwrite,
		DryRun:    opts.DryRun,
	})
	if err != nil {
		return fmt.Errorf("import legado sources: %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(importCLIReport{
		DryRun:       opts.DryRun,
		ImportReport: rep,
	})
}
