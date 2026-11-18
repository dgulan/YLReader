//go:build integration && linux

package backup

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/pkg/cache"
	headercrypto "yreader/backend/internal/pkg/crypto"
)

func fixtureBehaviorDraft(t *testing.T, g *restoreGuard, stage *stagedObjects, p Plan, owner, raw string, ring *headercrypto.Keyring, source *pgx.Conn, sourceKey, targetKey []byte) (handoverReview, handoverProbes, func(), handoverProbeInputs) {
	t.Helper()
	ctx := context.Background()
	var sourceDB, targetDB databaseIdentityPin
	e := source.QueryRow(ctx, `SELECT (SELECT system_identifier::text FROM pg_catalog.pg_control_system()),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()),
 (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname=$1),(SELECT oid FROM pg_catalog.pg_roles WHERE rolname='reader')`, p.Database.Database).Scan(&sourceDB.SystemID, &sourceDB.DatabaseOID, &sourceDB.RoleOID, &targetDB.DatabaseOID, &targetDB.RoleOID)
	if e != nil {
		t.Fatal("fixture pins")
	}
	targetDB.SystemID = sourceDB.SystemID
	const sourceURL = "redis://127.0.0.1:16379/0"
	const targetURL = "redis://127.0.0.1:16380/0"
	witnesses := [][]byte{make([]byte, 32), make([]byte, 32)}
	release := func() { clear(witnesses[0]); clear(witnesses[1]) }
	pins := []redisIdentityPin{}
	for i, url := range []string{sourceURL, targetURL} {
		if _, e := rand.Read(witnesses[i]); e != nil {
			t.Fatal("fixture entropy")
		}
		client, e := cache.ConfiguredRedis(url, "", time.Second)
		if e != nil || client.SetEX(ctx, "ylreader:handover:"+p.OperationID, witnesses[i], time.Minute) != nil {
			t.Fatal("fixture witness")
		}
		seen, e := client.ObserveServerWitness(ctx, "ylreader:handover:"+p.OperationID)
		if e != nil {
			t.Fatal("fixture Redis pin")
		}
		pins = append(pins, redisIdentityPin{seen.RunID, seen.Database})
		clear(seen.Witness)
	}
	runtime := runtimeFixture()
	runtime.TargetID = p.TargetID
	i := handoverProbeInputs{DatabaseURL: raw, SourceDatabase: sourceDB, TargetDatabase: targetDB, ExpectedRuntime: runtime, ActualRuntime: runtime,
		SourceJWT: sourceKey, TargetJWT: targetKey, Ring: ring, RequiredKIDs: g.verified.manifest.HeaderKIDs, SourceRedisURL: sourceURL, TargetRedisURL: targetURL,
		SourceRedis: pins[0], TargetRedis: pins[1], SourceWitness: witnesses[0], TargetWitness: witnesses[1], Guard: g, Stage: stage, OwnerID: owner}
	probes, e := i.probes(p)
	if e != nil {
		t.Fatal(e)
	}
	r, e := collectHandoverReview(ctx, g.locations.files[3], p, probes, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) })
	if e != nil {
		t.Fatal("behavior preflight draft", e)
	}
	return r, probes, release, i
}
