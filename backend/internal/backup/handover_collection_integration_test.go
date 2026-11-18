//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
	"yreader/backend/internal/pkg/cache"
	"yreader/backend/internal/pkg/storage"
)

func TestHandoverCollectionIsolated(t *testing.T) {
	if os.Getenv("BACKUP_COLLECTION_ISOLATED") != "1" {
		t.Skip("dedicated PostgreSQL and Redis collection harness required")
	}
	cleanPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const raw = "postgres://reader:@127.0.0.1:5432/yreader_a1_iso_collection?sslmode=disable"
	const adminRaw = "postgres://postgres:@127.0.0.1:5432/yreader_a1_iso_collection?sslmode=disable"
	connect := func(raw string) *pgx.Conn {
		t.Helper()
		cfg, e := ParseTarget(raw)
		if e != nil {
			t.Fatal("fixture target rejected")
		}
		c, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal("fixture connection")
		}
		return c
	}
	admin := connect(adminRaw)
	var source, target databaseIdentityPin
	e := admin.QueryRow(ctx, `SELECT (SELECT system_identifier::text FROM pg_catalog.pg_control_system()),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname='yreader_a1_iso_collection'),
 (SELECT oid FROM pg_catalog.pg_roles WHERE rolname='reader'),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname='postgres')`).Scan(&target.SystemID, &target.DatabaseOID, &target.RoleOID, &source.DatabaseOID)
	if e != nil {
		t.Fatal("fixture identity")
	}
	if _, e = admin.Exec(ctx, `REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC; GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO reader`); e != nil {
		t.Fatal("fixture function permission")
	}
	admin.Close(ctx)
	source.SystemID, source.RoleOID = target.SystemID, target.RoleOID
	c := connect(raw)
	if migration.Up(ctx, c) != nil {
		t.Fatal("fixture baseline")
	}
	c.Close(ctx)
	_, p, s, _ := handoverFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	p.Created = now.Format(time.RFC3339Nano)
	p.Expires = now.Add(15 * time.Minute).Format(time.RFC3339Nano)
	s.op.CreatedAt, s.op.UpdatedAt, s.op.HeartbeatAt = now, now, now
	p.Database = DatabaseTarget{"127.0.0.1", 5432, "yreader_a1_iso_collection"}
	p.Digest, e = p.digest()
	if e != nil {
		t.Fatal("fixture plan")
	}
	s.op.PlanDigest, s.attempt.PlanDigest, s.pub.PlanDigest = p.Digest, p.Digest, p.Digest
	f := &diagnosticReader{data: map[string][]byte{}}
	writeDiagnosticFixture(t, f, s.op, s.attempt, s.pub)
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("fixture directory")
	}
	files, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal("safe fixture files", e)
	}
	defer func() {
		if files != nil {
			_ = files.Close()
		}
	}()
	for key, data := range f.data {
		if files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data))) != nil {
			t.Fatal("fixture record")
		}
	}
	initial, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	runtime := runtimeFixture()
	runtime.TargetID = p.TargetID
	input := handoverProbeInputs{DatabaseURL: raw, SourceDatabase: source, TargetDatabase: target, ExpectedRuntime: runtime, ActualRuntime: runtime,
		SourceJWT: bytes.Repeat([]byte("a"), 32), TargetJWT: bytes.Repeat([]byte("b"), 32), Ring: headerRing(t, "primary", 1), RequiredKIDs: []string{"primary"},
		SourceRedisURL: "redis://127.0.0.1:16379/0", TargetRedisURL: "redis://127.0.0.1:16380/0",
		SourceRedis: redisIdentityPin{os.Getenv("IDENTITY_SOURCE_RUN_ID"), 0}, TargetRedis: redisIdentityPin{os.Getenv("IDENTITY_TARGET_RUN_ID"), 0}, SourceWitness: make([]byte, 32), TargetWitness: make([]byte, 32)}
	defer clear(input.SourceJWT)
	defer clear(input.TargetJWT)
	defer clear(input.SourceWitness)
	defer clear(input.TargetWitness)
	if _, e = rand.Read(input.SourceWitness); e != nil {
		t.Fatal("fixture entropy")
	}
	if _, e = rand.Read(input.TargetWitness); e != nil {
		t.Fatal("fixture entropy")
	}
	for _, pair := range []struct {
		url string
		w   []byte
	}{{input.SourceRedisURL, input.SourceWitness}, {input.TargetRedisURL, input.TargetWitness}} {
		client, e := cache.ConfiguredRedis(pair.url, "", time.Second)
		if e != nil || client.SetEX(ctx, "ylreader:handover:"+p.OperationID, pair.w, time.Minute) != nil {
			t.Fatal("fixture witness")
		}
	}
	probes, e := input.probes(p)
	if e != nil {
		t.Fatal(e)
	}
	clock := func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
	var report handoverReview
	t.Run("actual_probe_collection", func(t *testing.T) {
		var e error
		report, e = collectHandoverReview(ctx, files, p, probes, clock)
		if e != nil {
			t.Fatal("actual collection failed", e)
		}
		if report.TargetIdentity.Database == "" || report.ReadyForCutover || report.DeploymentAuthorized {
			t.Fatal("bad report")
		}
	})
	if t.Failed() {
		return
	}
	t.Run("partial_not_execution_credential", func(t *testing.T) {
		passes, missing := 0, 0
		for _, v := range report.Checks {
			if v.Status == "pass" {
				passes++
			}
			if v.Status == "unavailable" {
				missing++
			}
		}
		if passes != 4 || missing != 6 || report.validateBindingsFor(p, initial, report.TargetIdentity, clock()) != nil || report.validateFor(p, initial, report.TargetIdentity, clock()) == nil {
			t.Fatal("partial review passed full gate")
		}
	})
	t.Run("safe_file_save_reopen", func(t *testing.T) {
		if persistCollectedHandoverReview(ctx, files, p, report, clock) != nil {
			t.Fatal("safe report save")
		}
		if files.Close() != nil {
			t.Fatal("fixture close")
		}
		var e error
		files, e = storage.OpenBackupFiles(root)
		if e != nil {
			t.Fatal("fixture reopen")
		}
		loaded, token, e := (handoverReviewStore{files}).Load(ctx, p.OperationID)
		if e != nil || !validSHA(token) || loaded.Generation != initial.op.Generation {
			t.Fatal("report reopen failed")
		}
		after, e := stableHandoverSnapshot(ctx, files, p.OperationID)
		if e != nil || after.tokens != initial.tokens {
			t.Fatal("state/attempt/publication changed")
		}
	})
	if t.Failed() {
		return
	}
	data, _ := json.Marshal(report)
	t.Log("HANDOVER_COLLECTION_REPORT " + string(data))
}
