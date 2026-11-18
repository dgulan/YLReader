//go:build linux

package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

// Native CAS failure/ack-loss injection. All underlying bytes/handles remain
// the real secure LocalFS driver; no real COMMIT fault is implied by this type.
type recordedFaultFiles struct {
	storage.BackupFiles
	outcome string
	after   bool
	state   bool
}

func (f *recordedFaultFiles) CompareAndSwap(ctx context.Context, key, token string, data []byte) error {
	match := f.state && strings.HasSuffix(key, "/state.json")
	if strings.HasSuffix(key, "/attempt.json") && f.outcome != "" {
		a, e := decodeAttempt(data)
		match = e == nil && a.DatabaseOutcome == f.outcome
	}
	if !match {
		return f.BackupFiles.CompareAndSwap(ctx, key, token, data)
	}
	if f.after {
		if e := f.BackupFiles.CompareAndSwap(ctx, key, token, data); e != nil {
			return e
		}
	}
	return storage.ErrCommitUnknown
}

func TestRecordedDatabaseResultsAndNoReplay(t *testing.T) {
	for _, spec := range []struct {
		name                 string
		dbErr                error
		fault                string
		after, state, cancel bool
		outcome              string
		calls                int
		retained             bool
	}{
		{"commit", nil, "", false, false, false, "committed", 1, true},
		{"rollback", ErrRestoreDatabase, "", false, false, false, "rolled_back", 1, false},
		{"commit-unknown", ErrRestoreCommitUnknown, "", false, false, false, "unknown", 1, true},
		{"rollback-unknown", ErrRestoreRollback, "", false, false, false, "unknown", 1, true},
		{"intent-before-publication", nil, "unknown", false, false, false, "not_started", 0, true},
		{"intent-ack-lost", nil, "unknown", true, false, false, "unknown", 0, true},
		{"result-before-publication", nil, "committed", false, false, false, "unknown", 1, true},
		{"result-ack-lost", nil, "committed", true, false, false, "committed", 1, true},
		{"state-publication", nil, "", false, true, false, "committed", 1, true},
		{"rollback-result-publication", ErrRestoreDatabase, "rolled_back", false, false, false, "unknown", 1, true},
		{"rollback-state-publication", ErrRestoreDatabase, "", false, true, false, "rolled_back", 1, true},
		{"canceled-execution", nil, "", false, false, true, "rolled_back", 1, false},
	} {
		t.Run(spec.name, func(t *testing.T) {
			c, pkg, p, inspect, now := guardFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g, e := acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now, inspect)
			if e != nil {
				t.Fatal(e)
			}
			defer g.Close()
			owner := "50000000-0000-4000-8000-000000000001"
			stage, _, e := g.StageRecorded(ctx, pkg, owner, c.Target.InstanceID, now, inspect)
			if e != nil {
				t.Fatal(e)
			}
			defer stage.Close()
			if spec.fault != "" || spec.state {
				g.locations.files[3] = &recordedFaultFiles{BackupFiles: g.locations.files[3], outcome: spec.fault, after: spec.after, state: spec.state}
			}
			calls := 0
			execute := databaseStep(func(_ context.Context, _ string, _ *VerifiedPackage, _ PostgresArchiveTool, _ int64, _ *stagedObjects) (databaseRestoreResult, error) {
				calls++
				a, _, e := (attemptStore{Files: g.locations.files[3]}).Load(context.Background(), p.OperationID)
				if e != nil || a.DatabaseOutcome != "unknown" {
					t.Fatal("database ran before durable intent", e)
				}
				if e = stage.Close(); !errors.Is(e, storage.ErrBusy) {
					t.Fatal("database lost staged objects", e)
				}
				if spec.cancel {
					cancel()
					return databaseRestoreResult{}, context.Canceled
				}
				return databaseRestoreResult{Counts: pkg.manifest.Counts}, spec.dbErr
			})
			result, runErr := g.runDatabaseRecorded(ctx, stage, owner, c.Target.InstanceID, now, PostgresArchiveTool{}, 1<<20, inspect, execute)
			if calls != spec.calls || result.Executed != (spec.calls == 1) {
				t.Fatal("wrong execution count", calls, result.Executed)
			}
			if spec.name == "commit" && (runErr != nil || result.Database == nil || result.Operation.Phase != "validating") {
				t.Fatal("commit status", runErr)
			}
			if spec.name == "commit" {
				if _, e := g.PublishObjects(context.Background(), stage, owner, c.Target.InstanceID, now); !errors.Is(e, ErrPublicationState) {
					t.Fatal("unchecked database granted publication", e)
				}
				if _, _, e := (publicationStore{Files: g.locations.files[3]}).Load(context.Background(), p.OperationID); !errors.Is(e, storage.ErrNotFound) {
					t.Fatal("unchecked publication wrote intent", e)
				}
			}
			if spec.name != "commit" && runErr == nil {
				t.Fatal("failure reported success")
			}
			stored, _, e := (attemptStore{Files: g.locations.files[3]}).Load(context.Background(), p.OperationID)
			if e != nil || stored.DatabaseOutcome != spec.outcome {
				t.Fatal("wrong persisted outcome", stored.DatabaseOutcome, e)
			}
			if (stage.Reference() != "") != spec.retained {
				t.Fatal("unsafe generation cleanup", stage.Reference())
			}
			// Even if the plan expired or the stage was cleaned, this is status only.
			replayed, replayErr := g.runDatabaseRecorded(context.Background(), stage, owner, c.Target.InstanceID, now.Add(time.Hour), PostgresArchiveTool{}, 1<<20, inspect, execute)
			if replayed.Attempt.DatabaseOutcome != spec.outcome {
				t.Fatal("repeat lost recorded state", replayed.Attempt.DatabaseOutcome)
			}
			if spec.outcome == "committed" || spec.outcome == "rolled_back" {
				if replayErr != nil {
					t.Fatal("terminal status lookup failed", replayErr)
				}
			} else if !errors.Is(replayErr, ErrOutcomeRecord) {
				t.Fatal("uncertain repeat granted execution authority", replayErr)
			}
			if calls != spec.calls {
				t.Fatal("database replayed after recorded/latching outcome")
			}
		})
	}
}

func TestRecordedDatabaseRejectsExtraTargetObject(t *testing.T) {
	c, pkg, p, inspect, now := guardFixture(t)
	ctx := context.Background()
	g, e := acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now, inspect)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	owner := "50000000-0000-4000-8000-000000000001"
	stage, _, e := g.StageRecorded(ctx, pkg, owner, c.Target.InstanceID, now, inspect)
	if e != nil {
		t.Fatal(e)
	}
	defer stage.Close()
	if e = g.locations.files[1].CreateOnly(ctx, "foreign", strings.NewReader("keep"), 4); e != nil {
		t.Fatal(e)
	}
	calls := 0
	_, e = g.runDatabaseRecorded(ctx, stage, owner, c.Target.InstanceID, now, PostgresArchiveTool{}, 1<<20, inspect, func(context.Context, string, *VerifiedPackage, PostgresArchiveTool, int64, *stagedObjects) (databaseRestoreResult, error) {
		calls++
		return databaseRestoreResult{}, nil
	})
	if !errors.Is(e, storage.ErrUnsafeStorage) || calls != 0 {
		t.Fatal("extra target data accepted", e, calls)
	}
	a, _, e := (attemptStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
	if e != nil || a.DatabaseOutcome != "not_started" {
		t.Fatal(e)
	}
	if stage.Reference() == "" {
		t.Fatal("preflight failure deleted generation")
	}
}
