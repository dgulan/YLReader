package backup

import (
	"context"
	"encoding/json"
	"time"

	headercrypto "yreader/backend/internal/pkg/crypto"
)

type finalizedReviewProof struct {
	sha                       string
	sessions                  handoverSessionAllowance
	writeAttempted, persisted bool
}

func (g *restoreGuard) persistFinalHandoverReview(ctx context.Context, r handoverReview, clock func() time.Time) error {
	if g == nil || ctx == nil || clock == nil {
		return errHandoverUnconfirmed
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	data, _ := json.Marshal(r)
	if g.locations == nil || g.finalReviewProof == nil || g.finalReviewProof.sha != digestBytes(data) || !g.publicationConfirmed {
		return errHandoverUnconfirmed
	}
	if g.finalReviewProof.writeAttempted {
		return errHandoverUnconfirmed
	}
	g.finalReviewProof.writeAttempted = true
	if e := persistCollectedHandoverReview(ctx, g.locations.files[3], g.plan, r, clock); e != nil {
		return e
	}
	g.finalReviewProof.persisted = true
	return nil
}

// Registers one confirmed isolated report, not a cutover. The state phase
// remains validating. Every attempt is latched before revalidation or writes.
func (g *restoreGuard) registerHandoverReview(ctx context.Context, stage *stagedObjects, owner string, ring *headercrypto.Keyring, kids []string, probes handoverProbes, clock func() time.Time) (handoverRegistration, error) {
	if g == nil || ctx == nil || clock == nil || probes.Database == nil || probes.Runtime == nil || probes.Redis == nil {
		return handoverRegistration{}, errRegistration
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	g.mu.Lock()
	if g.locations == nil || g.finalReviewProof == nil || !g.finalReviewProof.persisted || g.registrationStarted {
		g.mu.Unlock()
		return handoverRegistration{}, errRegistrationUnknown
	}
	g.registrationStarted = true
	files, p, proof := g.locations.files[3], g.plan, *g.finalReviewProof
	g.mu.Unlock()
	r, sha, e := (handoverReviewStore{files}).Load(ctx, p.OperationID)
	s, readErr := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || readErr != nil || sha != proof.sha || r.validateFor(p, s, r.TargetIdentity, clock()) != nil {
		return handoverRegistration{}, errRegistration
	}
	var live handoverTargetIdentity
	for _, check := range []struct {
		run func(context.Context) (string, error)
		out *string
	}{{probes.Database, &live.Database}, {probes.Runtime, &live.Runtime}, {probes.Redis, &live.Redis}} {
		value, e := check.run(ctx)
		if e != nil || !validSHA(value) || ctx.Err() != nil {
			return handoverRegistration{}, errRegistration
		}
		*check.out = value
	}
	if live != r.TargetIdentity {
		return handoverRegistration{}, errRegistration
	}
	checked, e := g.probeHandoverDataSessions(ctx, stage, owner, p.TargetID, time.Now().UTC(), ring, kids, s, &proof.sessions)
	if e != nil || !checked.matches(p, s) {
		return handoverRegistration{}, errRegistration
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil || g.finalReviewProof == nil || !g.finalReviewProof.persisted || g.finalReviewProof.sha != sha {
		return handoverRegistration{}, errRegistrationUnknown
	}
	if _, e = g.recheckBindingsLocked(ctx, p.TargetID, time.Now().UTC(), PostgresTargetInspector{}, false); e != nil {
		return handoverRegistration{}, errRegistration
	}
	return registerIsolatedReview(ctx, files, p, r, clock)
}
