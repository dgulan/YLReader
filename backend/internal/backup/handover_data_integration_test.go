//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/pkg/cache"
	headercrypto "yreader/backend/internal/pkg/crypto"
)

func assertHandoverDataReview(t *testing.T, g *restoreGuard, stage *stagedObjects, c Config, p Plan, owner, raw string, ring *headercrypto.Keyring, source *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	s, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	check := func(ring *headercrypto.Keyring) (handoverDataProof, error) {
		return g.probeHandoverData(ctx, stage, owner, c.Target.InstanceID, time.Now().UTC(), ring, g.verified.manifest.HeaderKIDs, s)
	}
	denied := func(t *testing.T) {
		t.Helper()
		proof, e := check(ring)
		if e != errHandoverReview || proof.plan != "" {
			t.Fatal("corrupt data accepted or error leaked")
		}
	}
	connect := func() *pgx.Conn {
		t.Helper()
		cfg, e := ParseTarget(raw)
		if e != nil {
			t.Fatal("fixture config")
		}
		db, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal("fixture connection")
		}
		return db
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		db := connect()
		defer db.Close(ctx)
		if _, e := db.Exec(ctx, sql, args...); e != nil {
			t.Fatal("fixture data mutation")
		}
	}
	t.Run("handover_live_data", func(t *testing.T) {
		proof, e := check(ring)
		if e != nil || !proof.matches(p, s) {
			t.Fatal("valid data rejected", e)
		}
	})
	t.Run("handover_wrong_ring", func(t *testing.T) {
		if proof, e := check(headerRing(t, "primary", 99)); e != errHandoverReview || proof.plan != "" {
			t.Fatal("other runtime keyring accepted")
		}
	})
	t.Run("handover_corrupt_object", func(t *testing.T) {
		path := filepath.Join(c.Target.Storage.Root, c.Target.Storage.Namespace, s.pub.Output, g.verified.manifest.Objects[0].Key)
		original, e := os.ReadFile(path)
		if e != nil {
			t.Fatal("fixture object read")
		}
		if os.WriteFile(path, []byte("bad object"), 0600) != nil {
			t.Fatal("fixture object fault")
		}
		defer func() {
			if os.WriteFile(path, original, 0600) != nil {
				t.Error("fixture reset")
			}
		}()
		denied(t)
		got, e := os.ReadFile(path)
		if e != nil || string(got) != "bad object" {
			t.Fatal("probe repaired or deleted corrupt object")
		}
	})
	t.Run("handover_corrupt_header", func(t *testing.T) {
		db := connect()
		var id, original string
		e := db.QueryRow(ctx, `SELECT id::text,header::text FROM public.book_sources ORDER BY id LIMIT 1`).Scan(&id, &original)
		db.Close(ctx)
		if e != nil {
			t.Fatal("fixture header")
		}
		exec(`UPDATE public.book_sources SET header=$1::jsonb WHERE id=$2`, `{"__enc":"v1:primary:AAAA"}`, id)
		defer exec(`UPDATE public.book_sources SET header=$1::jsonb WHERE id=$2`, original, id)
		denied(t)
		db = connect()
		var got string
		e = db.QueryRow(ctx, `SELECT header::text FROM public.book_sources WHERE id=$1`, id).Scan(&got)
		db.Close(ctx)
		if e != nil || !bytes.Contains([]byte(got), []byte("AAAA")) {
			t.Fatal("probe repaired header")
		}
	})
	t.Run("handover_active_session", func(t *testing.T) {
		db := connect()
		var id string
		var revoked time.Time
		e := db.QueryRow(ctx, `SELECT id::text,revoked_at FROM public.auth_sessions ORDER BY id LIMIT 1`).Scan(&id, &revoked)
		db.Close(ctx)
		if e != nil {
			t.Fatal("fixture session")
		}
		exec(`UPDATE public.auth_sessions SET revoked_at=NULL WHERE id=$1`, id)
		defer exec(`UPDATE public.auth_sessions SET revoked_at=$1 WHERE id=$2`, revoked, id)
		denied(t)
		db = connect()
		var active bool
		e = db.QueryRow(ctx, `SELECT revoked_at IS NULL FROM public.auth_sessions WHERE id=$1`, id).Scan(&active)
		db.Close(ctx)
		if e != nil || !active {
			t.Fatal("probe revoked session")
		}
	})
	t.Run("handover_queued_task", func(t *testing.T) {
		db := connect()
		var id, status string
		e := db.QueryRow(ctx, `SELECT id::text,status::text FROM public.jobs ORDER BY id LIMIT 1`).Scan(&id, &status)
		db.Close(ctx)
		if e != nil {
			t.Fatal("fixture task")
		}
		exec(`UPDATE public.jobs SET status='queued' WHERE id=$1`, id)
		defer exec(`UPDATE public.jobs SET status=$1::job_status WHERE id=$2`, status, id)
		denied(t)
		db = connect()
		var got string
		e = db.QueryRow(ctx, `SELECT status::text FROM public.jobs WHERE id=$1`, id).Scan(&got)
		db.Close(ctx)
		if e != nil || got != "queued" {
			t.Fatal("probe canceled task")
		}
	})
	t.Run("handover_eight_check_report", func(t *testing.T) {
		var sourceDB, targetDB databaseIdentityPin
		e := source.QueryRow(ctx, `SELECT (SELECT system_identifier::text FROM pg_catalog.pg_control_system()),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()),
 (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname=$1),
 (SELECT oid FROM pg_catalog.pg_roles WHERE rolname='reader')`, p.Database.Database).Scan(&sourceDB.SystemID, &sourceDB.DatabaseOID, &sourceDB.RoleOID, &targetDB.DatabaseOID, &targetDB.RoleOID)
		if e != nil {
			t.Fatal("fixture identity pins")
		}
		targetDB.SystemID = sourceDB.SystemID
		sourceURL, targetURL := os.Getenv("BACKUP_HTTP_SOURCE_REDIS_URL"), os.Getenv("BACKUP_HTTP_TARGET_REDIS_URL")
		if sourceURL != "redis://127.0.0.1:16379/0" || targetURL != "redis://127.0.0.1:16380/0" {
			t.Fatal("dedicated cache pair required")
		}
		sw, tw := make([]byte, 32), make([]byte, 32)
		defer clear(sw)
		defer clear(tw)
		if _, e = rand.Read(sw); e != nil {
			t.Fatal("fixture entropy")
		}
		if _, e = rand.Read(tw); e != nil {
			t.Fatal("fixture entropy")
		}
		pins := []redisIdentityPin{}
		for _, pair := range []struct {
			url string
			w   []byte
		}{{sourceURL, sw}, {targetURL, tw}} {
			client, e := cache.ConfiguredRedis(pair.url, "", time.Second)
			if e != nil || client.SetEX(ctx, "ylreader:handover:"+p.OperationID, pair.w, time.Minute) != nil {
				t.Fatal("fixture cache witness")
			}
			seen, e := client.ObserveServerWitness(ctx, "ylreader:handover:"+p.OperationID)
			if e != nil {
				t.Fatal("fixture cache pin")
			}
			pins = append(pins, redisIdentityPin{seen.RunID, seen.Database})
			clear(seen.Witness)
		}
		runtime := runtimeFixture()
		runtime.TargetID = p.TargetID
		input := handoverProbeInputs{DatabaseURL: raw, SourceDatabase: sourceDB, TargetDatabase: targetDB, ExpectedRuntime: runtime, ActualRuntime: runtime,
			SourceJWT: bytes.Repeat([]byte("a"), 32), TargetJWT: bytes.Repeat([]byte("b"), 32), Ring: ring, RequiredKIDs: g.verified.manifest.HeaderKIDs,
			SourceRedisURL: sourceURL, TargetRedisURL: targetURL, SourceRedis: pins[0], TargetRedis: pins[1], SourceWitness: sw, TargetWitness: tw, Guard: g, Stage: stage, OwnerID: owner}
		defer clear(input.SourceJWT)
		defer clear(input.TargetJWT)
		probes, e := input.probes(p)
		if e != nil {
			t.Fatal(e)
		}
		clock := func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
		report, e := collectHandoverReview(ctx, g.locations.files[3], p, probes, clock)
		if e != nil {
			t.Fatal("restored data report failed", e)
		}
		pass, missing := 0, 0
		for _, v := range report.Checks {
			if v.Status == "pass" {
				pass++
			}
			if v.Status == "unavailable" {
				missing++
			}
		}
		if pass != 8 || missing != 2 || report.validateFor(p, s, report.TargetIdentity, clock()) == nil {
			t.Fatal("report overstated readiness")
		}
		if persistCollectedHandoverReview(ctx, g.locations.files[3], p, report, clock) != nil {
			t.Fatal("restored report save")
		}
		after, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
		if e != nil || after.tokens != s.tokens {
			t.Fatal("data review changed records")
		}
		encoded, _ := json.Marshal(report)
		fmt.Printf("RESTORE_DATA_REVIEW %s\n", encoded)
	})
}
