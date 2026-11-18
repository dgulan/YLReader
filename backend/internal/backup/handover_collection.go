package backup

import (
	"context"
	"time"

	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
)

// Internal seams for deterministic tests. No external result/JSON adapter.
type handoverProbes struct {
	Database func(context.Context) (string, error)
	Runtime  func(context.Context) (string, error)
	Redis    func(context.Context) (string, error)
	Data     func(context.Context, diagnosticSnapshot) (handoverDataProof, error)
}

type handoverProbeInputs struct {
	DatabaseURL                    string
	SourceDatabase, TargetDatabase databaseIdentityPin
	ExpectedRuntime, ActualRuntime runtimeConfigBinding
	SourceJWT, TargetJWT           []byte
	Ring                           *headercrypto.Keyring
	RequiredKIDs                   []string
	SourceRedisURL, TargetRedisURL string
	SourceRedis, TargetRedis       redisIdentityPin
	SourceWitness, TargetWitness   []byte
	Guard                          *restoreGuard
	Stage                          *stagedObjects
	OwnerID                        string
}

// Inputs/secrets must remain caller-owned and immutable during collection.
func (i handoverProbeInputs) probes(p Plan) (handoverProbes, error) {
	if p.Validate() != nil || i.ExpectedRuntime.TargetID != p.TargetID || i.ActualRuntime.TargetID != p.TargetID {
		return handoverProbes{}, errHandoverReview
	}
	cfg, e := ParseTarget(i.DatabaseURL)
	if e != nil || (DatabaseTarget{cfg.Host, cfg.Port, cfg.Database}) != p.Database {
		return handoverProbes{}, errHandoverReview
	}
	probes := handoverProbes{
		Database: func(ctx context.Context) (string, error) {
			return probeHandoverDatabase(ctx, i.DatabaseURL, i.SourceDatabase, i.TargetDatabase)
		},
		Runtime: func(ctx context.Context) (string, error) {
			if ctx.Err() != nil {
				return "", errRuntimeIdentity
			}
			return probeHandoverRuntime(i.ExpectedRuntime, i.ActualRuntime, i.SourceJWT, i.TargetJWT, i.Ring, i.RequiredKIDs)
		},
		Redis: func(ctx context.Context) (string, error) {
			return probeHandoverRedisPair(ctx, i.SourceRedisURL, i.TargetRedisURL, p.OperationID, i.SourceRedis, i.TargetRedis, i.SourceWitness, i.TargetWitness)
		},
	}
	if i.Guard != nil || i.Stage != nil || i.OwnerID != "" {
		if i.Guard == nil || i.Stage == nil || !validUUID(i.OwnerID) || i.Guard.plan.Digest != p.Digest {
			return handoverProbes{}, errHandoverReview
		}
		probes.Data = func(ctx context.Context, s diagnosticSnapshot) (handoverDataProof, error) {
			return i.Guard.probeHandoverData(ctx, i.Stage, i.OwnerID, p.TargetID, time.Now().UTC(), i.Ring, i.RequiredKIDs, s)
		}
	}
	return probes, nil
}

func partialHandoverReview(p Plan, s diagnosticSnapshot, id handoverTargetIdentity, start, finish time.Time) handoverReview {
	expires, _ := time.Parse(time.RFC3339Nano, p.Expires)
	if limit := finish.Add(5 * time.Minute); limit.Before(expires) {
		expires = limit
	}
	r := handoverReview{Format: "ylreader-handover-review", Version: 1, Scope: "isolated-localfs-handover-review",
		OperationID: p.OperationID, BackupID: p.BackupID, PlanDigest: p.Digest, PackageSHA: p.PackageSHA, ManifestSHA: p.ManifestSHA,
		TargetID: p.TargetID, StorageFingerprint: p.TargetFingerprint, TreeFingerprint: s.pub.TreeFingerprint, Generation: s.op.Generation,
		OperationSHA: s.tokens[0], AttemptSHA: s.tokens[1], PublicationSHA: s.tokens[2], TargetIdentity: id, Started: start, Finished: finish, Expires: expires}
	for _, name := range handoverCheckIDs() {
		status := "unavailable"
		if name == "schema_ledger" || name == "database_identity" || name == "database_role" || name == "redis_isolation" {
			status = "pass"
		}
		r.Checks = append(r.Checks, handoverReviewCheck{name, status})
	}
	return r
}

func stableHandoverSnapshot(ctx context.Context, files storage.ConditionalFiles, id string) (diagnosticSnapshot, error) {
	a, e := readDiagnosticSnapshot(ctx, diagnosticFiles{files}, id)
	if e != nil {
		return diagnosticSnapshot{}, errHandoverReview
	}
	b, e := readDiagnosticSnapshot(ctx, diagnosticFiles{files}, id)
	if e != nil || a.tokens != b.tokens {
		return diagnosticSnapshot{}, errHandoverReview
	}
	return b, nil
}

func collectHandoverReview(ctx context.Context, files storage.ConditionalFiles, p Plan, probes handoverProbes, clock func() time.Time) (handoverReview, error) {
	if ctx == nil || files == nil || clock == nil || probes.Database == nil || probes.Runtime == nil || probes.Redis == nil || p.Validate() != nil {
		return handoverReview{}, errHandoverReview
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || checkHandoverSnapshot(p, s) != nil {
		return handoverReview{}, errHandoverReview
	}
	start := clock()
	created, _ := time.Parse(time.RFC3339Nano, p.Created)
	expires, _ := time.Parse(time.RFC3339Nano, p.Expires)
	if !validStamp(start) || start.Before(created) || start.Before(s.op.UpdatedAt) || !start.Before(expires) {
		return handoverReview{}, errHandoverReview
	}
	var id handoverTargetIdentity
	for _, probe := range []struct {
		run func(context.Context) (string, error)
		out *string
	}{{probes.Database, &id.Database}, {probes.Runtime, &id.Runtime}, {probes.Redis, &id.Redis}} {
		if ctx.Err() != nil {
			return handoverReview{}, errHandoverReview
		}
		value, e := probe.run(ctx)
		if e != nil || !validSHA(value) || ctx.Err() != nil {
			return handoverReview{}, errHandoverReview
		}
		*probe.out = value
	}
	var dataProof handoverDataProof
	if probes.Data != nil {
		dataProof, e = probes.Data(ctx, s)
		if e != nil || !dataProof.matches(p, s) || ctx.Err() != nil {
			return handoverReview{}, errHandoverReview
		}
	}
	after, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || s.tokens != after.tokens {
		return handoverReview{}, errHandoverReview
	}
	finish := clock()
	r := partialHandoverReview(p, after, id, start, finish)
	if probes.Data != nil {
		if !dataProof.matches(p, after) {
			return handoverReview{}, errHandoverReview
		}
		for index, check := range r.Checks {
			if check.ID == "published_objects" || check.ID == "header_keyring" || check.ID == "sessions_revoked" || check.ID == "tasks_disabled" {
				r.Checks[index].Status = "pass"
			}
		}
	}
	if r.validateBindingsFor(p, after, id, finish) != nil {
		return handoverReview{}, errHandoverReview
	}
	return r, nil
}

// Saves a partial review only. It never writes state.json or registers an
// execution credential. A late failure may leave the immutable report intact.
func persistCollectedHandoverReview(ctx context.Context, files storage.ConditionalFiles, p Plan, r handoverReview, clock func() time.Time) error {
	if ctx == nil || files == nil || clock == nil {
		return errHandoverReview
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || r.validateBindingsFor(p, s, r.TargetIdentity, clock()) != nil {
		return errHandoverReview
	}
	if e = (handoverReviewStore{files}).Create(ctx, r); e != nil {
		return e
	}
	after, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || ctx.Err() != nil || r.validateBindingsFor(p, after, r.TargetIdentity, clock()) != nil {
		return errHandoverReview
	}
	return nil
}
