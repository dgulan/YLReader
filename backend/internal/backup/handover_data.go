package backup

import (
	"context"
	"slices"
	"time"

	headercrypto "yreader/backend/internal/pkg/crypto"
)

// Not deserializable evidence. Only the live guard's read-only verification
// creates this value on the production path; it never authorizes cutover.
type handoverDataProof struct {
	plan    string
	records [3]string
	tree    string
}

func (p handoverDataProof) matches(plan Plan, s diagnosticSnapshot) bool {
	return p.plan == plan.Digest && p.records == s.tokens && validSHA(p.tree) && p.tree == s.pub.TreeFingerprint
}

func (g *restoreGuard) probeHandoverData(ctx context.Context, stage *stagedObjects, owner, confirmed string, now time.Time, ring *headercrypto.Keyring, kids []string, s diagnosticSnapshot) (handoverDataProof, error) {
	return g.probeHandoverDataSessions(ctx, stage, owner, confirmed, now, ring, kids, s, nil)
}

func (g *restoreGuard) probeHandoverDataSessions(ctx context.Context, stage *stagedObjects, owner, confirmed string, now time.Time, ring *headercrypto.Keyring, kids []string, s diagnosticSnapshot, allowance *handoverSessionAllowance) (handoverDataProof, error) {
	if g == nil || ctx == nil {
		return handoverDataProof{}, errHandoverReview
	}
	if allowance != nil && !allowance.valid() {
		return handoverDataProof{}, errHandoverReview
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	g.mu.Lock()
	defer g.mu.Unlock()
	proof := g.headerProof
	if g.locations == nil || !g.publicationConfirmed || g.verified == nil || stage == nil || proof == nil || proof.stage != stage || proof.owner != owner ||
		proof.binding != g.packageBinding || stage.binding != g.packageBinding || proof.ring != ring || ring == nil ||
		!slices.Equal(kids, g.verified.manifest.HeaderKIDs) || checkHandoverSnapshot(g.plan, s) != nil || s.op.OwnerID != owner ||
		stage.Reference() != s.pub.Staging || confirmed != g.plan.TargetID {
		return handoverDataProof{}, errHandoverReview
	}
	before, e := stableHandoverSnapshot(ctx, g.locations.files[3], g.plan.OperationID)
	if e != nil || before.tokens != s.tokens {
		return handoverDataProof{}, errHandoverReview
	}
	cfg, e := g.recheckBindingsLocked(ctx, confirmed, now, PostgresTargetInspector{}, false)
	if e != nil {
		return handoverDataProof{}, errHandoverReview
	}
	root, e := g.locations.files[1].OpenDirectory(ctx, s.pub.Output, false)
	if e != nil {
		return handoverDataProof{}, errHandoverReview
	}
	defer root.Close()
	location, e := root.Location(ctx)
	if e != nil || fingerprint(location, "publication") != s.pub.TreeFingerprint {
		return handoverDataProof{}, errHandoverReview
	}
	objects, e := publicationObjects(stage)
	if e != nil || len(objects) != len(g.verified.manifest.Objects) {
		return handoverDataProof{}, errHandoverReview
	}
	inventory := map[string]ManifestObject{}
	for _, object := range g.verified.manifest.Objects {
		inventory[object.ID] = object
	}
	for _, object := range objects {
		if expected, ok := inventory[object.ID]; !ok || expected != object {
			return handoverDataProof{}, errHandoverReview
		}
		if verifyPublishedObject(ctx, root, object) != nil {
			return handoverDataProof{}, errHandoverReview
		}
	}
	if validatePublicationDatabaseSessions(ctx, cfg, g.verified, stage, ring, allowance) != nil {
		return handoverDataProof{}, errHandoverReview
	}
	location, e = root.Location(ctx)
	if e != nil || fingerprint(location, "publication") != s.pub.TreeFingerprint {
		return handoverDataProof{}, errHandoverReview
	}
	if _, e = g.recheckBindingsLocked(ctx, confirmed, time.Now().UTC(), PostgresTargetInspector{}, false); e != nil {
		return handoverDataProof{}, errHandoverReview
	}
	after, e := stableHandoverSnapshot(ctx, g.locations.files[3], g.plan.OperationID)
	if e != nil || after.tokens != s.tokens || ctx.Err() != nil {
		return handoverDataProof{}, errHandoverReview
	}
	if root.Close() != nil {
		return handoverDataProof{}, errHandoverReview
	}
	return handoverDataProof{g.plan.Digest, s.tokens, s.pub.TreeFingerprint}, nil
}
