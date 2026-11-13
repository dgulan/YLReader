// Package main 是旧版 Reader 数据导入与 DB 迁移 CLI 的入口（Stage 3 脚手架占位）。
// 真正的迁移逻辑在 Stage 6 实现，此处仅保证目录拓扑与可编译。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"yreader/backend/internal/migration"
	"yreader/backend/internal/repository"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func allowed(cfg *pgxpool.Config) bool {
	for _, key := range []string{"PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGSERVICE", "PGSERVICEFILE"} {
		if _, ok := os.LookupEnv(key); ok {
			return false
		}
	}
	c := cfg.ConnConfig
	if c.Host != "127.0.0.1" && c.Host != "::1" && c.Host != "localhost" {
		return false
	}
	if len(c.Fallbacks) > 0 {
		for _, f := range c.Fallbacks {
			if f.Host != c.Host || f.Port != c.Port {
				return false
			}
		}
	}
	return c.Database == "yreader_test" || regexp.MustCompile(`^yreader_a1_iso_[a-z0-9_]{1,24}$`).MatchString(c.Database)
}

func explicitTarget(raw string) bool {
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, e := url.Parse(raw)
		return e == nil && u.Hostname() != "" && u.Port() != "" && u.Path != ""
	}
	token := regexp.MustCompile(`(?s)^\s*([a-z_]+)\s*=\s*('(?:[^'\\]|\\.)*'|(?:[^\s\\]|\\.)*)`)
	seen := map[string]bool{}
	allowedKeys := map[string]bool{"host": true, "port": true, "dbname": true, "user": true, "password": true, "sslmode": true, "connect_timeout": true, "application_name": true}
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
func run(args []string) error {
	if len(args) != 2 || args[0] != "schema" || (args[1] != "status" && args[1] != "check" && args[1] != "up") {
		return fmt.Errorf("usage: migrator schema status|check|up (DATABASE_URL required)")
	}
	raw := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if raw == "" {
		return fmt.Errorf("DATABASE_URL required")
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("invalid DATABASE_URL")
	}
	if args[1] == "up" && !allowed(cfg) {
		return fmt.Errorf("database write target denied; only explicit local isolated test targets allowed")
	}
	if args[1] == "up" && !explicitTarget(raw) {
		return fmt.Errorf("explicit unambiguous host, port and database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("cannot initialize database pool")
	}
	defer pool.Close()
	switch args[1] {
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
	fmt.Println("schema " + args[1] + ": ok")
	return nil
}
