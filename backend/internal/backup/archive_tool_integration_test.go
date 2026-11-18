//go:build integration

package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

// Read-only external-tool test. The caller supplies ONLY a synthetic fixture;
// no DB process, connection environment, port, or target is required.
func TestArchiveToolSynthetic(t *testing.T) {
	binary, fixture := os.Getenv("BACKUP_PG_RESTORE"), os.Getenv("BACKUP_SYNTHETIC_DUMP")
	if binary == "" || fixture == "" {
		t.Skip("dedicated synthetic archive and trusted pg_restore required")
	}
	f, e := os.Open(fixture)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	tool := PostgresArchiveTool{Executable: binary}
	t.Setenv("PGHOST", "DO_NOT_CONNECT")
	t.Setenv("PGDATABASE", "DO_NOT_CONNECT")
	t.Setenv("PGOPTIONS", "DO_NOT_CONNECT")
	result, e := tool.Inspect(context.Background(), f)
	if e != nil || len(strings.Split(strings.TrimSpace(result.DataUseList()), "\n")) != 16 {
		t.Fatal("real archive rejected", e)
	}
	for _, bad := range []string{"PGDMP", "not an archive"} {
		if _, e = tool.Inspect(context.Background(), strings.NewReader(bad)); !errors.Is(e, ErrArchiveInventory) {
			t.Fatal("invalid archive accepted", e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = tool.Inspect(ctx, strings.NewReader("PGDMP")); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestArchiveDataToolSynthetic(t *testing.T) {
	binary, fixture := os.Getenv("BACKUP_PG_RESTORE"), os.Getenv("BACKUP_SYNTHETIC_DUMP")
	if binary == "" || fixture == "" {
		t.Skip("dedicated synthetic archive and trusted pg_restore required")
	}
	f, e := os.Open(fixture)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	tool := PostgresArchiveTool{Executable: binary}
	// These counts are independently recorded by verify-backup-db.py from
	// source_snapshot in the known synthetic fixture, not inferred from output.
	counts := ManifestCounts{Migrations: 1, Users: 2, Sessions: 2, Settings: 2, Sources: 2, Books: 2, Mappings: 2, Chapters: 2, Progress: 2, Bookmarks: 2, Jobs: 2}
	result, e := tool.inspectData(context.Background(), f, counts, 1<<20)
	if e != nil || result.Counts != counts || result.ChaptersSequence != (SequenceState{2, true}) || result.FailuresSequence != (SequenceState{1, false}) {
		t.Fatal("real data rejected", result, e)
	}
	for name, budget := range map[string]int64{"expanded-budget": 10, "count-mismatch": 1 << 20} {
		t.Run(name, func(t *testing.T) {
			if _, e := f.Seek(0, io.SeekStart); e != nil {
				t.Fatal(e)
			}
			wrong := counts
			if name == "count-mismatch" {
				wrong.Users++
			}
			if _, e := tool.inspectData(context.Background(), f, wrong, budget); !errors.Is(e, ErrArchiveData) {
				t.Fatal(e)
			}
		})
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		t.Fatal(e)
	}
	info, e := f.Stat()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tool.inspectData(context.Background(), io.LimitReader(f, info.Size()/2), counts, 1<<20); !errors.Is(e, ErrArchiveData) {
		t.Fatal("truncated archive", e)
	}
}

func TestArchiveDataAuthenticatedPackage(t *testing.T) {
	binary, fixture := os.Getenv("BACKUP_PG_RESTORE"), os.Getenv("BACKUP_SYNTHETIC_DUMP")
	if binary == "" || fixture == "" {
		t.Skip("dedicated synthetic archive and trusted pg_restore required")
	}
	dump, e := os.ReadFile(fixture)
	if e != nil {
		t.Fatal(e)
	}
	m, entries := packageFixture(t)
	m.Database.Bytes = int64(len(dump))
	m.Database.SHA = sha(dump)
	m.TotalBytes = m.Database.Bytes + m.Objects[0].Bytes
	m.Counts = ManifestCounts{Migrations: 1, Users: 2, Sessions: 2, Settings: 2, Sources: 2, Books: 2, Mappings: 2, Chapters: 2, Progress: 2, Bookmarks: 2, Jobs: 2}
	entries[0].body = dump
	identity := identityFixture(t)
	ciphertext, options := encryptFixture(t, tarFixture(t, m, entries, nil), identity)
	root := t.TempDir()
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	scratch, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer scratch.Close()
	verified, e := VerifyPackage(context.Background(), bytes.NewReader(ciphertext), identity, options, scratch)
	if e != nil {
		t.Fatal(e)
	}
	defer verified.Close()
	result, e := verified.VerifyDatabaseData(context.Background(), PostgresArchiveTool{Executable: binary}, 1<<20)
	if e != nil || result.Counts != m.Counts {
		t.Fatal("authenticated database verification", result, e)
	}
	if e = verified.Close(); e != nil {
		t.Fatal(e)
	}
	if empty, e := scratch.Empty(context.Background()); e != nil || !empty {
		t.Fatal("scratch cleanup", e)
	}
}
