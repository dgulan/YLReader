//go:build linux

package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

func TestControlledRestoreLinuxScratchRetention(t *testing.T) {
	ctx := context.Background()
	m, entries := packageFixture(t)
	id := identityFixture(t)
	cipher, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)

	setupStage := func(t *testing.T) (targetRoot, ref string, stage *stagedObjects) {
		t.Helper()
		sourceRoot := t.TempDir()
		if err := os.Chmod(sourceRoot, 0700); err != nil {
			t.Fatal(err)
		}
		source, err := storage.OpenBackupFiles(sourceRoot)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = source.Close() })

		targetRoot = t.TempDir()
		if err := os.Chmod(targetRoot, 0700); err != nil {
			t.Fatal(err)
		}
		target, err := storage.OpenBackupFiles(targetRoot)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = target.Close() })

		pkg, err := VerifyPackage(ctx, bytes.NewReader(cipher), id, options, source)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pkg.Close() })

		st, err := stageRestoreObjects(ctx, pkg, target, "40000000-0000-4000-8000-000000000001")
		if err != nil {
			t.Fatal(err)
		}
		return targetRoot, st.Reference(), st
	}

	assertScratchIntact := func(t *testing.T, targetRoot, ref string) {
		t.Helper()
		if !strings.HasPrefix(ref, ".scratch-") {
			t.Fatalf("unexpected scratch reference %q", ref)
		}
		scratchDir := filepath.Join(targetRoot, ref)
		if info, err := os.Stat(scratchDir); err != nil || !info.IsDir() {
			t.Fatalf("expected %s directory on disk: err=%v", scratchDir, err)
		}
		if _, err := os.Stat(filepath.Join(scratchDir, "restore-owner.json")); err != nil {
			t.Fatalf("expected restore-owner.json on disk in %s: err=%v", scratchDir, err)
		}
		for _, obj := range m.Objects {
			p := filepath.Join(scratchDir, "object-"+obj.ID)
			if fi, err := os.Stat(p); err != nil || fi.Size() != obj.Bytes {
				t.Fatalf("expected object %s intact in %s: err=%v info=%v", obj.ID, scratchDir, err, fi)
			}
		}
	}

	for _, tc := range []struct {
		name     string
		dbRes    recordedDatabaseResult
		dbErr    error
		pubErr   error
		retain   bool
		wantCode string
	}{
		{
			name:     "commit_unknown",
			dbRes:    recordedDatabaseResult{Operation: Operation{Phase: "validating"}, Attempt: restoreAttempt{DatabaseOutcome: "unknown"}},
			dbErr:    ErrRestoreCommitUnknown,
			retain:   true,
			wantCode: "RESTORE_COMMIT_UNKNOWN",
		},
		{
			name:     "publication_failed",
			dbRes:    recordedDatabaseResult{Operation: Operation{Phase: "validating"}, Attempt: restoreAttempt{DatabaseOutcome: "committed"}, HeadersChecked: true},
			pubErr:   ErrPublication,
			retain:   true,
			wantCode: "RESTORE_PUBLICATION_FAILED",
		},
		{
			name:     "confirmed_rollback",
			dbRes:    recordedDatabaseResult{Operation: Operation{Phase: "failed"}, Attempt: restoreAttempt{DatabaseOutcome: "rolled_back"}},
			dbErr:    ErrRestoreDatabase,
			retain:   false,
			wantCode: "RESTORE_REJECTED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetRoot, ref, stage := setupStage(t)
			res := ControlledRestoreResult{ArtifactsRetained: true}
			ops := controlledRestoreOps{
				runDB: func(context.Context, *stagedObjects) (recordedDatabaseResult, error) {
					return tc.dbRes, tc.dbErr
				},
				publish: func(context.Context, *stagedObjects) (publicationResult, error) {
					return publicationResult{}, tc.pubErr
				},
			}
			_ = executeControlledRestoreStages(ctx, stage, ops, &res)
			if res.StatusCode != tc.wantCode {
				t.Fatalf("unexpected status code: got %q want %q", res.StatusCode, tc.wantCode)
			}
			if tc.retain {
				assertScratchIntact(t, targetRoot, ref)
			} else {
				if _, err := os.Stat(filepath.Join(targetRoot, ref)); !os.IsNotExist(err) {
					t.Fatalf("expected %s removed on confirmed rollback, got err=%v", ref, err)
				}
			}
		})
	}
}
