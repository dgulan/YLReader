package backup

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"sync"
	"time"

	"yreader/backend/internal/pkg/storage"
)

// Preparation only: retains directory handles/leases and revalidates the frozen
// plan. It is deliberately not an exported apply or background operation.
type restoreGuard struct {
	mu                   sync.Mutex
	locations            *Locations
	leases               []storage.RestoreLease
	pin                  io.ReadCloser
	plan                 Plan
	config               Config
	packageBinding       PackageBinding
	scratch              locatedFiles
	verified             *VerifiedPackage
	executionBlocked     bool
	headerProof          *headerPublicationProof
	publicationStarted   bool
	publicationConfirmed bool
	finalReviewProof     *finalizedReviewProof
	registrationStarted  bool
	behaviorStarted      bool
	behaviorRun          *confirmedBehaviorRun
	accountStage         string
}

func (g *restoreGuard) setAccountStage(stage string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.accountStage = stage
	g.mu.Unlock()
}

func (g *restoreGuard) accountStageValue() string {
	if g == nil {
		return "guard_nil"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.accountStage == "" {
		return "unknown"
	}
	return g.accountStage
}

func acquireRestoreGuard(ctx context.Context, p Plan, c Config, pkg *VerifiedPackage, confirmed string, now time.Time, inspector TargetInspector) (guard *restoreGuard, err error) {
	if pkg == nil {
		return nil, ErrPlan
	}
	if e := checkPlanFields(p, c, pkg.Binding(), confirmed, now, inspector); e != nil {
		return nil, e
	}
	pkg.mu.Lock()
	policy := pkg.verificationLimits
	scratch, ok := pkg.temp.(locatedFiles)
	pkg.mu.Unlock()
	if policy != c.Limits {
		return nil, ErrPlan
	}
	if !ok {
		return nil, storage.ErrCapability
	}
	pin, e := pkg.OpenDatabase(ctx)
	if e != nil {
		return nil, e
	}
	g := &restoreGuard{pin: pin, plan: p, config: c, packageBinding: pkg.Binding(), scratch: scratch, verified: pkg}
	defer func() {
		if err != nil {
			err = errors.Join(err, g.Close())
		}
	}()
	g.locations, e = OpenLocations(ctx, c)
	if e != nil {
		return nil, e
	}
	for _, index := range []int{3, 1} {
		locker, ok := g.locations.files[index].(storage.RestoreLocker)
		if !ok {
			return nil, storage.ErrCapability
		}
		lease, e := locker.TryRestoreLock(ctx)
		if e != nil {
			return nil, e
		}
		g.leases = append(g.leases, lease)
	}
	if e = g.Recheck(ctx, confirmed, now, inspector); e != nil {
		return nil, e
	}
	return g, nil
}

// Recheck is for BEFORE object staging, while the target is still empty.
// Reopen paths to detect replacement, but keep the original handles and locks.
func (g *restoreGuard) Recheck(ctx context.Context, confirmed string, now time.Time, inspector TargetInspector) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, e := g.recheckLocked(ctx, confirmed, now, inspector, true)
	return e
}

func (g *restoreGuard) recheckLocked(ctx context.Context, confirmed string, now time.Time, inspector TargetInspector, requireEmpty bool) (*pgx.ConnConfig, error) {
	cfg, e := g.recheckBindingsLocked(ctx, confirmed, now, inspector, requireEmpty)
	if e != nil {
		return nil, e
	}
	if e := inspector.CheckEmpty(ctx, cfg); e != nil {
		return nil, preservePlanError(e, "target_check")
	}
	return cfg, nil
}

func (g *restoreGuard) recheckBindingsLocked(ctx context.Context, confirmed string, now time.Time, inspector TargetInspector, requireEmpty bool) (*pgx.ConnConfig, error) {
	if g.locations == nil || len(g.leases) != 2 {
		return nil, planStageError("guard_closed")
	}
	if e := checkPlanFields(g.plan, g.config, g.packageBinding, confirmed, now, inspector); e != nil {
		return nil, e
	}
	for _, lease := range g.leases {
		if e := lease.Validate(ctx); e != nil {
			return nil, e
		}
	}
	scratch, e := g.scratch.Location(ctx)
	if e != nil {
		return nil, e
	}
	temporary, e := g.locations.files[4].Location(ctx)
	if e != nil {
		return nil, e
	}
	contained := false
	for _, ancestor := range scratch.Ancestors {
		if ancestor == temporary.Identity {
			contained = true
		}
	}
	if !contained {
		return nil, ErrLocations
	}
	live, e := openLocations(ctx, g.config, requireEmpty)
	if e != nil {
		return nil, e
	}
	defer live.Close()
	for i, pinned := range g.locations.files {
		a, e := pinned.Location(ctx)
		if e != nil {
			return nil, e
		}
		b, e := live.files[i].Location(ctx)
		if e != nil || a.Identity != b.Identity {
			return nil, ErrLocations
		}
	}
	snapshot, e := g.locations.snapshot(ctx, requireEmpty)
	if e != nil {
		return nil, e
	}
	if snapshot.Target != g.plan.TargetFingerprint || snapshot.Control != g.plan.ControlFingerprint || snapshot.Temporary != g.plan.TempFingerprint {
		return nil, planStageError("location_fingerprint")
	}
	cfg, e := readTarget(ctx, g.config)
	if e != nil {
		return nil, e
	}
	if (DatabaseTarget{cfg.Host, cfg.Port, cfg.Database}) != g.plan.Database {
		return nil, planStageError("target_database_mismatch")
	}
	return cfg, nil
}

func (g *restoreGuard) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var result error
	for i := len(g.leases) - 1; i >= 0; i-- {
		result = errors.Join(result, g.leases[i].Close())
	}
	g.leases = nil
	g.headerProof = nil
	g.publicationConfirmed = false
	g.finalReviewProof = nil
	if g.behaviorRun != nil {
		clear(g.behaviorRun.sourceJWT)
		clear(g.behaviorRun.targetJWT)
	}
	g.behaviorRun = nil
	if g.locations != nil {
		result = errors.Join(result, g.locations.Close())
		g.locations = nil
	}
	if g.pin != nil {
		result = errors.Join(result, g.pin.Close())
		g.pin = nil
	}
	return result
}
