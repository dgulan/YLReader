//go:build linux

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
)

type inspectorFunc func(context.Context, *pgx.ConnConfig) error

func (f inspectorFunc) CheckEmpty(ctx context.Context, c *pgx.ConnConfig) error { return f(ctx, c) }
func localConfig(t *testing.T) Config {
	t.Helper()
	c := configExample(t)
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"source", "target", "backups", "control", "temporary"} {
		if e := os.Mkdir(filepath.Join(root, name), 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, store := range []*LocalStorage{&c.Source.Storage, &c.Target.Storage, &c.Backup, &c.Control} {
		store.Root = root
	}
	c.Temporary.Root = filepath.Join(root, "temporary")
	c.Source.DSNFile = filepath.Join(root, "source.dsn")
	c.Target.DSNFile = filepath.Join(root, "target.dsn")
	c.Encryption.Identity = filepath.Join(root, "identity.txt")
	c.Encryption.Recipient = ""
	c.QuiescenceFile = filepath.Join(root, "quiescence.json")
	if e := os.WriteFile(c.Target.DSNFile, []byte("postgres://reader:synthetic@127.0.0.1:55439/yreader_a1_iso_plan?sslmode=disable"), 0600); e != nil {
		t.Fatal(e)
	}
	return c
}
func bindingExample() PackageBinding {
	return PackageBinding{BackupID: "30000000-0000-4000-8000-000000000001", SourceID: "10000000-0000-4000-8000-000000000001", PackageSHA: strings.Repeat("0", 64), ManifestSHA: strings.Repeat("1", 64), SchemaVersion: 1, MigrationChecksum: migration.Expected().Checksum}
}

func TestPlanPreparationAndRevalidation(t *testing.T) {
	cleanPG(t)
	ctx := context.Background()
	c := localConfig(t)
	clock := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	calls := 0
	inspector := inspectorFunc(func(_ context.Context, cfg *pgx.ConnConfig) error {
		calls++
		if cfg.Database != "yreader_a1_iso_plan" {
			t.Fatal("wrong inspected target")
		}
		return nil
	})
	p, e := PreparePlan(ctx, c, bindingExample(), "", clock, inspector)
	if e != nil {
		t.Fatal(e)
	}
	if e = CheckPlan(ctx, p, c, bindingExample(), c.Target.InstanceID, clock.Add(899*time.Second), inspector); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("database not re-inspected")
	}
	for _, now := range []time.Time{clock.Add(-time.Second), clock.Add(900 * time.Second)} {
		if e = CheckPlan(ctx, p, c, bindingExample(), c.Target.InstanceID, now, inspector); !errors.Is(e, ErrPlan) {
			t.Fatal("invalid clock accepted", e)
		}
	}
	if e = CheckPlan(ctx, p, c, bindingExample(), c.Source.InstanceID, clock, inspector); !errors.Is(e, ErrPlan) {
		t.Fatal("wrong confirmation accepted")
	}
	changed := bindingExample()
	changed.PackageSHA = strings.Repeat("f", 64)
	if e = CheckPlan(ctx, p, c, changed, c.Target.InstanceID, clock, inspector); !errors.Is(e, ErrPlan) {
		t.Fatal("package drift accepted")
	}
	if e = os.WriteFile(filepath.Join(c.Target.Storage.Root, "target", "unexpected"), []byte("synthetic"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = CheckPlan(ctx, p, c, bindingExample(), c.Target.InstanceID, clock, inspector); e == nil {
		t.Fatal("nonempty target accepted")
	}
}

func TestPlanDirectoryReplacementAndSecretPermissions(t *testing.T) {
	cleanPG(t)
	ctx := context.Background()
	c := localConfig(t)
	clock := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	inspect := inspectorFunc(func(context.Context, *pgx.ConnConfig) error { return nil })
	p, e := PreparePlan(ctx, c, bindingExample(), "", clock, inspect)
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(c.Target.Storage.Root, c.Target.Storage.Namespace)
	moved := root + "-old"
	if e = os.Rename(root, moved); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e = CheckPlan(ctx, p, c, bindingExample(), c.Target.InstanceID, clock, inspect); !errors.Is(e, ErrPlan) {
		t.Fatal("directory identity drift accepted", e)
	}
	if e = os.Chmod(c.Target.DSNFile, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = PreparePlan(ctx, c, bindingExample(), "", clock, inspect); e == nil {
		t.Fatal("public DSN accepted")
	}
}

func TestSecureConfigAndInspectorFailure(t *testing.T) {
	cleanPG(t)
	ctx := context.Background()
	c := localConfig(t)
	data, _ := json.Marshal(c)
	file := filepath.Join(c.Target.Storage.Root, "config.json")
	if e := os.WriteFile(file, data, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfigFile(ctx, file); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(file, file+"-alias"); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfigFile(ctx, file+"-alias"); e == nil {
		t.Fatal("linked config accepted")
	}
	failing := inspectorFunc(func(context.Context, *pgx.ConnConfig) error { return errors.New("synthetic DSN must not leak") })
	if _, e := PreparePlan(ctx, c, bindingExample(), "", time.Now(), failing); !errors.Is(e, ErrPlan) || strings.Contains(e.Error(), "synthetic") {
		t.Fatal("inspector error leaked", e)
	}
	if _, e := PreparePlan(ctx, c, bindingExample(), "", time.Now(), nil); !errors.Is(e, ErrPlan) {
		t.Fatal("missing inspection allowed")
	}
	unsafe := &pgx.ConnConfig{}
	if e := (PostgresTargetInspector{}).CheckEmpty(ctx, unsafe); !errors.Is(e, ErrTargetDenied) {
		t.Fatal("invalid target reached connection")
	}
}

func TestPlanRejectsAliasedStorageRoot(t *testing.T) {
	cleanPG(t)
	c := localConfig(t)
	alias := filepath.Join(c.Backup.Root, "alias")
	if e := os.Symlink(c.Backup.Root, alias); e != nil {
		t.Fatal(e)
	}
	c.Backup.Root = alias
	if c.Validate() != nil {
		t.Fatal("fixture must pass lexical checks")
	}
	if locations, e := OpenLocations(context.Background(), c); e == nil {
		locations.Close()
		t.Fatal("physical alias passed lexical boundary")
	}
}
