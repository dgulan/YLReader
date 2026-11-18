//go:build integration && linux

package backup

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRecordedDatabaseIntegration(t *testing.T) {
	raw := os.Getenv("BACKUP_RESTORE_DSN")
	if raw == "" {
		t.Skip("fresh independent disposable cluster required")
	}
	cleanPG(t)
	ctx := context.Background()
	tool := PostgresArchiveTool{Executable: os.Getenv("BACKUP_PG_RESTORE")}
	for _, spec := range []struct {
		name         string
		mode         int
		fault        string
		after, state bool
		outcome      string
	}{{"recordedgood", 1, "", false, false, "committed"}, {"recordedrollback", 2, "", false, false, "rolled_back"}, {"recordedlost", 1, "committed", true, false, "committed"}, {"recordedstate", 1, "", false, true, "committed"}, {"recordedintent", 1, "unknown", false, false, "not_started"}} {
		t.Run(spec.name, func(t *testing.T) {
			c := localConfig(t)
			u, e := url.Parse(raw)
			if e != nil {
				t.Fatal("fixture DSN")
			}
			u.Path = "/yreader_a1_iso_" + spec.name
			if e = os.WriteFile(c.Target.DSNFile, []byte(u.String()), 0600); e != nil {
				t.Fatal(e)
			}
			pkg := restoreFixturePackageAt(t, os.Getenv("BACKUP_SYNTHETIC_DUMP"), spec.mode, c.Temporary.Root)
			c.Source.InstanceID = pkg.Binding().SourceID
			c.Limits = pkg.verificationLimits
			now := time.Now().UTC().Truncate(time.Microsecond)
			inspector := PostgresTargetInspector{}
			p, e := PreparePlan(ctx, c, pkg.Binding(), "", now, inspector)
			if e != nil {
				t.Fatal(e)
			}
			g, e := acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now, inspector)
			if e != nil {
				t.Fatal(e)
			}
			defer g.Close()
			owner := "50000000-0000-4000-8000-000000000001"
			stage, _, e := g.StageRecorded(ctx, pkg, owner, c.Target.InstanceID, now, inspector)
			if e != nil {
				t.Fatal(e)
			}
			defer stage.Close()
			if spec.fault != "" || spec.state {
				g.locations.files[3] = &recordedFaultFiles{BackupFiles: g.locations.files[3], outcome: spec.fault, after: spec.after, state: spec.state}
			}
			result, runErr := g.RunDatabaseRecorded(ctx, stage, owner, c.Target.InstanceID, now, tool, 1<<20)
			if spec.name == "recordedgood" {
				if runErr != nil || result.Database == nil || !result.Executed {
					t.Fatal("committed step", runErr)
				}
			} else if runErr == nil {
				t.Fatal("fault reported success")
			}
			if spec.fault != "" || spec.state {
				if !errors.Is(runErr, ErrOutcomeRecord) {
					t.Fatal("record failure not distinguished", runErr)
				}
			}
			stored, _, e := (attemptStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
			if e != nil || stored.DatabaseOutcome != spec.outcome {
				t.Fatal("stored database result", stored.DatabaseOutcome, e)
			}
			if spec.outcome == "committed" && stage.Reference() == "" {
				t.Fatal("committed data lost objects")
			}
			if spec.outcome == "rolled_back" && stage.Reference() != "" {
				t.Fatal("confirmed rollback retained generation")
			}
			_, beforeToken, e := (attemptStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
			if e != nil {
				t.Fatal(e)
			}
			observation, e := observeRecordedDatabase(ctx, u.String(), pkg.manifest.Counts)
			if e != nil {
				t.Fatal("read-only outcome observation", e)
			}
			if spec.outcome == "committed" {
				if observation.State != "populated" || !observation.CountsMatch {
					t.Fatal("commit evidence", observation)
				}
			} else if observation.State != "pristine" {
				t.Fatal("rollback/no-write evidence", observation)
			}
			_, afterToken, e := (attemptStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
			if e != nil || beforeToken != afterToken {
				t.Fatal("observation rewrote outcome", e)
			}
			cfg, e := ParseTarget(u.String())
			if e != nil {
				t.Fatal(e)
			}
			connection, e := pgx.ConnectConfig(ctx, cfg)
			if e != nil {
				t.Fatal("fixture connection")
			}
			defer connection.Close(ctx)
			if observed, e := observeRecordedDatabase(ctx, u.String(), pkg.manifest.Counts); e != nil || observed.State != "busy" {
				t.Fatal("competing connection not reported", observed, e)
			}
			if spec.outcome == "committed" {
				var valid bool
				e = connection.QueryRow(ctx, `SELECT (SELECT count(*)=2 AND bool_and(token_version=4) FROM users) AND (SELECT count(*)=2 AND bool_and(char_offset=17 AND version=7) FROM reading_progress)`).Scan(&valid)
				if e != nil || !valid {
					t.Fatal("actual commit not preserved", e)
				}
			} else {
				tx, e := connection.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				if e = checkPristineTransaction(ctx, tx); e != nil {
					t.Fatal("uncommitted target modified", e)
				}
				tx.Rollback(ctx)
			}
			// Keep this competing connection open: a replay must return recorded
			// state without even re-inspecting or writing the database.
			replayed, replayErr := g.RunDatabaseRecorded(ctx, stage, owner, c.Target.InstanceID, now.Add(time.Hour), tool, 1<<20)
			if replayed.Executed || replayed.Database != nil {
				t.Fatal("database replayed")
			}
			if replayed.Attempt.DatabaseOutcome != spec.outcome {
				t.Fatal("repeat lost persisted state", replayed.Attempt.DatabaseOutcome)
			}
			if spec.outcome == "committed" || spec.outcome == "rolled_back" {
				if replayErr != nil {
					t.Fatal("status-only repeat inspected expired/busy target", replayErr)
				}
			}
			if spec.name == "recordedintent" && !errors.Is(replayErr, ErrOutcomeRecord) {
				t.Fatal("intent failure blindly retried", replayErr)
			}
		})
	}
}
