//go:build integration

package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/pkg/storage"
)

func restoreFixturePackage(t *testing.T, path string) *VerifiedPackage {
	return restoreFixturePackageObjects(t, path, 0)
}

func restoreFixturePackageObjects(t *testing.T, path string, mode int) *VerifiedPackage {
	return restoreFixturePackageAt(t, path, mode, "")
}

func restoreFixturePackageAt(t *testing.T, path string, mode int, root string) *VerifiedPackage {
	return restoreFixturePackageHeaderAt(t, path, mode, root, nil)
}

func restoreFixturePackageHeaderAt(t *testing.T, path string, mode int, root string, kids []string) *VerifiedPackage {
	t.Helper()
	dump, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("synthetic fixture missing")
	}
	m, entries := packageFixture(t)
	m.Database.Bytes = int64(len(dump))
	m.Database.SHA = sha(dump)
	m.TotalBytes = m.Database.Bytes + m.Objects[0].Bytes
	m.Counts = ManifestCounts{Migrations: 1, Users: 2, Sessions: 2, Settings: 2, Sources: 2, Books: 2, Mappings: 2, Chapters: 2, Progress: 2, Bookmarks: 2, Jobs: 2}
	entries[0].body = dump
	if mode != 0 {
		m.Objects = nil
		entries = entries[:1]
		m.TotalBytes = m.Database.Bytes
		count := 2
		if mode == 3 {
			count = 3
		}
		if mode == 2 {
			count = 1
		}
		for i := 1; i <= count; i++ {
			body := []byte(strings.Repeat(fmt.Sprintf("恢复正文 %d 中文 😀\n", i), 16))
			id := fmt.Sprintf("%032x", i)
			m.Objects = append(m.Objects, ManifestObject{ID: id, Key: fmt.Sprintf("local/20000000-0000-4000-8000-%012d/0.txt", i), Bytes: int64(len(body)), SHA: sha(body)})
			entries = append(entries, archiveEntry{name: "objects/" + id, body: body, kind: entries[0].kind})
			m.TotalBytes += int64(len(body))
		}
		m.ObjectCount = int64(len(m.Objects))
	}
	identity := identityFixture(t)
	if kids != nil {
		m.HeaderKIDs = kids
	}
	encrypted, options := encryptFixture(t, tarFixture(t, m, entries, nil), identity)
	if kids != nil {
		options.HeaderKIDs = kids
	}
	if root == "" {
		root = t.TempDir()
	}
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	scratch, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { scratch.Close() })
	pkg, e := VerifyPackage(context.Background(), bytes.NewReader(encrypted), identity, options, scratch)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := pkg.Close(); e != nil {
			t.Error(e)
		}
	})
	return pkg
}

func TestIsolatedDatabaseRestore(t *testing.T) {
	raw := os.Getenv("BACKUP_RESTORE_DSN")
	if raw == "" {
		t.Skip("fresh independent disposable cluster required")
	}
	cleanPG(t)
	if _, e := ParseTarget(raw); e != nil {
		t.Fatal(e)
	}
	tool := PostgresArchiveTool{Executable: os.Getenv("BACKUP_PG_RESTORE")}
	if tool.Executable == "" {
		t.Fatal("trusted tool required")
	}
	ctx := context.Background()
	target := func(name string) string {
		u, e := url.Parse(raw)
		if e != nil {
			t.Fatal("fixture DSN")
		}
		u.Path = "/yreader_a1_iso_" + name
		return u.String()
	}
	connect := func(raw string) *pgx.Conn {
		cfg, e := ParseTarget(raw)
		if e != nil {
			t.Fatal(e)
		}
		c, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal("fixture connection")
		}
		return c
	}
	assertPristine := func(raw string) {
		c := connect(raw)
		defer c.Close(ctx)
		tx, e := c.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if e = checkPristineTransaction(ctx, tx); e != nil {
			t.Fatal("transaction did not leave pristine database", e)
		}
	}
	pkg := restoreFixturePackage(t, os.Getenv("BACKUP_SYNTHETIC_DUMP"))
	t.Run("success", func(t *testing.T) {
		result, e := restoreIsolatedDatabase(ctx, target("restore"), pkg, tool, 1<<20)
		if e != nil {
			t.Fatal(e)
		}
		if result.Counts != pkg.manifest.Counts || result.RevokedSessions != 2 || result.AdvancedUsers != 2 || result.CanceledJobs != 2 {
			t.Fatal("incorrect restore result", result)
		}
		c := connect(target("restore"))
		defer c.Close(ctx)
		var preserved, secured, sequences bool
		e = c.QueryRow(ctx, `SELECT
 (SELECT count(*)=2 AND bool_and(char_offset=17 AND version=7 AND server_updated_at='2026-01-02T03:04:05Z') FROM reading_progress)
 AND (SELECT count(*)=2 AND bool_and(note='unicode: 中文 😀') FROM bookmarks),
 (SELECT bool_and(token_version=4) FROM users)
 AND (SELECT bool_and(revoked_at IS NOT NULL) FROM auth_sessions)
 AND (SELECT bool_and(status='canceled' AND worker_id IS NULL AND finished_at IS NOT NULL) FROM jobs),
 (SELECT last_value=2 AND is_called FROM chapters_id_seq)
 AND (SELECT last_value=1 AND NOT is_called FROM source_failures_id_seq)`).Scan(&preserved, &secured, &sequences)
		if e != nil || !preserved || !secured || !sequences {
			t.Fatal("restore invariants", e, preserved, secured, sequences)
		}
	})
	t.Run("existing-target-denied", func(t *testing.T) {
		if _, e := restoreIsolatedDatabase(ctx, target("restore"), pkg, tool, 1<<20); !errors.Is(e, ErrPlan) {
			t.Fatal("existing target allowed", e)
		}
		c := connect(target("restore"))
		defer c.Close(ctx)
		var good bool
		if e := c.QueryRow(ctx, `SELECT bool_and(token_version=4) FROM users`).Scan(&good); e != nil || !good {
			t.Fatal("existing data changed", e)
		}
	})
	for _, spec := range []struct{ name, env string }{{"badfk", "BACKUP_BAD_FK_DUMP"}, {"overflow", "BACKUP_OVERFLOW_DUMP"}} {
		t.Run(spec.name+"-rollback", func(t *testing.T) {
			bad := restoreFixturePackage(t, os.Getenv(spec.env))
			// Both samples pass full read-only data validation. Their late failure
			// is a real PostgreSQL constraint/overflow, not a preflight rejection.
			if _, e := bad.VerifyDatabaseData(ctx, tool, 1<<20); e != nil {
				t.Fatal("fixture must reach transaction", e)
			}
			if result, e := restoreIsolatedDatabase(ctx, target(spec.name), bad, tool, 1<<20); !errors.Is(e, ErrRestoreDatabase) || result != (databaseRestoreResult{}) {
				t.Fatal("bad data committed", result, e)
			}
			assertPristine(target(spec.name))
			// A fresh valid attempt succeeds after rollback, including sequences
			// created in the aborted transaction and the removed ledger seed.
			if _, e := restoreIsolatedDatabase(ctx, target(spec.name), pkg, tool, 1<<20); e != nil {
				t.Fatal("clean retry after confirmed rollback", e)
			}
		})
	}
	t.Run("canceled-before-write", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, e := restoreIsolatedDatabase(canceled, target("cancel"), pkg, tool, 1<<20); e == nil {
			t.Fatal("canceled operation succeeded")
		}
		assertPristine(target("cancel"))
	})
	t.Run("occupied-target-preserved", func(t *testing.T) {
		c := connect(target("occupied"))
		if _, e := c.Exec(ctx, `CREATE TABLE marker(value text);INSERT INTO marker VALUES('keep')`); e != nil {
			t.Fatal(e)
		}
		c.Close(ctx)
		if _, e := restoreIsolatedDatabase(ctx, target("occupied"), pkg, tool, 1<<20); !errors.Is(e, ErrPlan) {
			t.Fatal(e)
		}
		if observed, e := observeRecordedDatabase(ctx, target("occupied"), pkg.manifest.Counts); e != nil || observed.State != "incompatible" {
			t.Fatal("unrelated schema misclassified", observed, e)
		}
		c = connect(target("occupied"))
		defer c.Close(ctx)
		var value string
		if e := c.QueryRow(ctx, `SELECT value FROM marker`).Scan(&value); e != nil || value != "keep" {
			t.Fatal("marker changed", e)
		}
	})
}
