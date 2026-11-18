package backup

import (
	"context"
	"errors"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func advanceRecordedPhase(ctx context.Context, control Control, old Operation, phase string, failure *string) (Operation, error) {
	next := old
	next.Generation++
	now := time.Now().UTC().Truncate(time.Microsecond)
	if !now.After(old.UpdatedAt) {
		now = old.UpdatedAt.Add(time.Microsecond)
	}
	next.UpdatedAt = now
	next.HeartbeatAt = now
	next.Phase = phase
	if terminal(phase) {
		next.FinishedAt = &now
	}
	next.FailureCode = failure
	if phase == "canceled" {
		next.CancelRequested = true
	}
	if e := control.Update(ctx, old.ID, old.Generation, next, false); e != nil {
		return old, e
	}
	return next, nil
}

// Staging preparation only. Repeated calls on this guard return recorded state
// without restaging or taking ownership. A new process must query Control first,
// never use stale heartbeat as proof it may resume an existing operation.
func (g *restoreGuard) StageRecorded(ctx context.Context, pkg *VerifiedPackage, ownerID, confirmed string, now time.Time, inspector TargetInspector) (*stagedObjects, Operation, error) {
	if !validUUID(ownerID) || confirmed != g.plan.TargetID {
		return nil, Operation{}, planStageError("stage_confirmation")
	}
	if pkg == nil || pkg.Binding() != g.packageBinding {
		return nil, Operation{}, planStageError("stage_package_binding")
	}
	g.mu.Lock()
	if g.locations == nil {
		g.mu.Unlock()
		return nil, Operation{}, planStageError("guard_closed")
	}
	control := Control{Files: g.locations.files[3]}
	existing, _, e := control.Load(ctx, g.plan.OperationID)
	g.mu.Unlock()
	if e == nil {
		if existing.PlanDigest != g.plan.Digest || existing.BackupID != g.plan.BackupID {
			return nil, Operation{}, ErrAttempt
		}
		return nil, existing, nil
	}
	if !errors.Is(e, storage.ErrNotFound) {
		return nil, Operation{}, e
	}
	if pkg == nil || pkg != g.verified || pkg.Binding() != g.packageBinding {
		return nil, Operation{}, planStageError("stage_package_binding")
	}
	if e = g.Recheck(ctx, confirmed, now, inspector); e != nil {
		return nil, Operation{}, e
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil {
		return nil, Operation{}, planStageError("guard_closed")
	}
	stamp := now.UTC().Truncate(time.Microsecond)
	op := Operation{Format: "ylreader-restore-operation", Version: 1, ID: g.plan.OperationID, BackupID: g.plan.BackupID, PlanDigest: g.plan.Digest, Generation: 1, Phase: "planned", CreatedAt: stamp, UpdatedAt: stamp, HeartbeatAt: stamp, OwnerID: ownerID}
	if e = control.Create(ctx, op); e != nil {
		return nil, Operation{}, e
	}
	store := attemptStore{Files: g.locations.files[3]}
	attempt := restoreAttempt{Format: "ylreader-restore-attempt", Version: 1, OperationID: op.ID, BackupID: op.BackupID, PlanDigest: op.PlanDigest, OwnerID: ownerID, TargetFingerprint: g.plan.TargetFingerprint, DatabaseOutcome: "not_started"}
	if e = store.Create(ctx, attempt); e != nil {
		return nil, op, e
	}
	op, e = advanceRecordedPhase(ctx, control, op, "staging", nil)
	if e != nil {
		return nil, op, e
	}
	stage, e := stageRegisteredObjects(ctx, pkg, g.locations.files[1], op.ID, func(ctx context.Context, reference string) error {
		old, token, e := store.Load(ctx, op.ID)
		if e != nil {
			return e
		}
		next := old
		next.Generation = &reference
		return store.Update(ctx, op.ID, token, next)
	})
	if e != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		phase, code := "failed", "storage_failed"
		var failure *string = &code
		if errors.Is(e, context.Canceled) {
			phase = "canceled"
			failure = nil
		}
		op, stateErr := advanceRecordedPhase(cleanupCtx, control, op, phase, failure)
		return nil, op, errors.Join(e, stateErr)
	}
	op, e = advanceRecordedPhase(ctx, control, op, "validating", nil)
	// If state publication is uncertain, keep the generation and its durable
	// reference. This method never starts a database transaction.
	return stage, op, e
}
