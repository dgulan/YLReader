//go:build integration

package backup

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/pkg/storage"
)

func TestObjectDatabasePhase(t *testing.T) {
	raw := os.Getenv("BACKUP_RESTORE_DSN")
	if raw == "" {
		t.Skip("fresh independent disposable cluster required")
	}
	cleanPG(t)
	tool := PostgresArchiveTool{Executable: os.Getenv("BACKUP_PG_RESTORE")}
	ctx := context.Background()
	for _, spec := range []struct {
		name, env string
		mode      int
		success   bool
	}{{"objects", "BACKUP_SYNTHETIC_DUMP", 1, true}, {"missing", "BACKUP_SYNTHETIC_DUMP", 2, false}, {"orphan", "BACKUP_SYNTHETIC_DUMP", 3, false}, {"badref", "BACKUP_BAD_REF_DUMP", 1, false}, {"crossuser", "BACKUP_CROSS_USER_DUMP", 1, false}} {
		t.Run(spec.name, func(t *testing.T) {
			pkg := restoreFixturePackageObjects(t, os.Getenv(spec.env), spec.mode)
			root := t.TempDir()
			if e := os.Chmod(root, 0700); e != nil {
				t.Fatal(e)
			}
			target, e := storage.OpenBackupFiles(root)
			if e != nil {
				t.Fatal(e)
			}
			defer target.Close()
			u, e := url.Parse(raw)
			if e != nil {
				t.Fatal("fixture DSN")
			}
			u.Path = "/yreader_a1_iso_" + spec.name
			result, stage, e := restoreObjectDatabasePhase(ctx, u.String(), pkg, tool, 1<<20, target, "40000000-0000-4000-8000-000000000001")
			if spec.success {
				if e != nil || stage == nil || result.Counts != pkg.manifest.Counts {
					t.Fatal("coordinated substep", e)
				}
				defer stage.Close()
				r, e := stage.Open(ctx, "local/20000000-0000-4000-8000-000000000002/0.txt")
				if e != nil {
					t.Fatal(e)
				}
				body, e := io.ReadAll(r)
				r.Close()
				if e != nil || !strings.Contains(string(body), "恢复正文 2 中文 😀") {
					t.Fatal("restored text unreadable", e)
				}
				if empty, e := target.Empty(ctx); e != nil || empty {
					t.Fatal("committed database lost staged objects", e)
				}
				return
			}
			if !errors.Is(e, ErrObjectReferences) || stage != nil || result != (databaseRestoreResult{}) {
				t.Fatal("bad references accepted", result, e)
			}
			if empty, e := target.Empty(ctx); e != nil || !empty {
				t.Fatal("confirmed rollback left objects", e)
			}
			cfg, e := ParseTarget(u.String())
			if e != nil {
				t.Fatal(e)
			}
			c, e := pgx.ConnectConfig(ctx, cfg)
			if e != nil {
				t.Fatal("fixture connection")
			}
			defer c.Close(ctx)
			tx, e := c.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(ctx)
			if e = checkPristineTransaction(ctx, tx); e != nil {
				t.Fatal("reference failure committed database", e)
			}
		})
	}
}
