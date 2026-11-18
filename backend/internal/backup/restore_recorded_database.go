package backup

import (
	"context"
	"errors"
	"io"
	"time"

	"yreader/backend/internal/pkg/storage"
)

var ErrOutcomeRecord = errors.New("backup: restore result publication unconfirmed; preserve objects and reconcile before retry")

type recordedDatabaseResult struct {
	Attempt        restoreAttempt
	Operation      Operation
	Database       *databaseRestoreResult
	Executed       bool
	HeadersChecked bool
}

type databaseStep func(context.Context, string, *VerifiedPackage, PostgresArchiveTool, int64, *stagedObjects) (databaseRestoreResult, error)

func matchingRecordedOperation(op Operation, a restoreAttempt) bool {
	return op.ID == a.OperationID && op.OwnerID == a.OwnerID && op.PlanDigest == a.PlanDigest && op.BackupID == a.BackupID
}

// Internal isolated step only. Success remains validating until key/object
// publication and application verification gates have independently passed.
func (g *restoreGuard) RunDatabaseRecorded(ctx context.Context, stage *stagedObjects, ownerID, confirmed string, now time.Time, tool PostgresArchiveTool, budget int64) (recordedDatabaseResult, error) {
	return g.runDatabaseRecorded(ctx, stage, ownerID, confirmed, now, tool, budget, PostgresTargetInspector{}, restoreDatabaseValidated)
}

func (g *restoreGuard) runDatabaseRecorded(ctx context.Context, stage *stagedObjects, ownerID, confirmed string, now time.Time, tool PostgresArchiveTool, budget int64, inspector TargetInspector, execute databaseStep) (result recordedDatabaseResult, err error) {
	if budget < 1 || budget > 1<<40 || !validUUID(ownerID) || confirmed != g.plan.TargetID || execute == nil {
		return result, ErrPlan
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil {
		return result, ErrPlan
	}
	control := Control{Files: g.locations.files[3]}
	store := attemptStore{Files: g.locations.files[3]}
	op, _, e := control.Load(ctx, g.plan.OperationID)
	if e != nil {
		return result, e
	}
	a, token, e := store.Load(ctx, g.plan.OperationID)
	if e != nil {
		return result, e
	}
	result.Attempt = a
	result.Operation = op
	if op.PlanDigest != g.plan.Digest || op.BackupID != g.plan.BackupID || op.OwnerID != ownerID || a.PlanDigest != op.PlanDigest || a.BackupID != op.BackupID || a.OwnerID != op.OwnerID || a.TargetFingerprint != g.plan.TargetFingerprint {
		return result, ErrAttempt
	}
	// Read-only replay may outlive the plan, but grants no new authority.
	if a.DatabaseOutcome != "not_started" {
		if a.DatabaseOutcome == "unknown" {
			return result, errors.Join(ErrOutcomeRecord, ErrRestoreCommitUnknown)
		}
		return result, nil
	}
	if g.executionBlocked {
		return result, ErrOutcomeRecord
	}
	if op.Phase != "validating" || op.CancelRequested {
		return result, ErrAttempt
	}
	if stage == nil || stage.binding != g.packageBinding || a.Generation == nil || *a.Generation != stage.Reference() {
		return result, ErrAttempt
	}
	pin, e := stage.open(ctx, "restore-owner.json")
	if e != nil {
		return result, e
	}
	defer pin.Close()
	ownerData, e := io.ReadAll(io.LimitReader(pin, 4097))
	if e != nil || len(ownerData) > 4096 {
		return result, ErrAttempt
	}
	var owner struct {
		OperationID string `json:"operation_id"`
		BackupID    string `json:"backup_id"`
		PackageSHA  string `json:"package_sha256"`
	}
	if exactJSON(ownerData, &owner) != nil || owner.OperationID != op.ID || owner.BackupID != op.BackupID || owner.PackageSHA != g.packageBinding.PackageSHA {
		return result, ErrAttempt
	}
	cfg, e := g.recheckLocked(ctx, confirmed, now, inspector, false)
	if e != nil {
		return result, e
	}
	stage.mu.Lock()
	located, ok := stage.temp.(locatedFiles)
	stage.mu.Unlock()
	if !ok {
		return result, storage.ErrCapability
	}
	info, e := located.Location(ctx)
	if e != nil {
		return result, e
	}
	directory, ok := g.locations.files[1].(storage.RestoreDirectory)
	if !ok {
		return result, storage.ErrCapability
	}
	if e = directory.OnlyDirectory(ctx, *a.Generation, info.Identity); e != nil {
		return result, e
	}
	op, _, e = control.Load(ctx, op.ID)
	if e != nil {
		return result, e
	}
	if op.OwnerID != ownerID || op.PlanDigest != a.PlanDigest || op.Phase != "validating" || op.CancelRequested {
		return result, ErrAttempt
	}
	result.Operation = op
	// Latch before attempting the intent CAS: even an uncertain/failed CAS must
	// not lead this process to blindly execute on a later not_started read.
	g.executionBlocked = true
	intent := a
	intent.DatabaseOutcome = "unknown"
	if e = store.Update(ctx, a.OperationID, token, intent); e != nil {
		return result, errors.Join(ErrOutcomeRecord, e)
	}
	result.Attempt = intent
	current, intentToken, e := store.Load(ctx, a.OperationID)
	if e != nil {
		return result, errors.Join(ErrOutcomeRecord, e)
	}
	if current.DatabaseOutcome != "unknown" || attemptTransition(intent, current) != nil {
		return result, ErrOutcomeRecord
	}
	// A cancel arriving during intent publication can still avoid DB execution.
	latest, _, stateErr := control.Load(ctx, op.ID)
	var db databaseRestoreResult
	var databaseErr error
	if stateErr != nil {
		databaseErr = errors.Join(ErrOutcomeRecord, stateErr)
	} else if !matchingRecordedOperation(latest, intent) {
		databaseErr = ErrOutcomeRecord
	} else if latest.Phase != "validating" || latest.CancelRequested {
		databaseErr = context.Canceled
	} else {
		result.Executed = true
		db, databaseErr = execute(ctx, cfg.ConnString(), g.verified, tool, budget, stage)
	}
	if databaseErr == nil {
		result.Database = &db
	}
	if errors.Is(databaseErr, ErrRestoreCommitUnknown) || errors.Is(databaseErr, ErrRestoreRollback) || errors.Is(databaseErr, ErrOutcomeRecord) {
		return result, databaseErr
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	final := intent
	final.DatabaseOutcome = "committed"
	if databaseErr != nil {
		final.DatabaseOutcome = "rolled_back"
	}
	if e = store.Update(cleanupCtx, a.OperationID, intentToken, final); e != nil {
		return result, errors.Join(databaseErr, ErrOutcomeRecord, e)
	}
	result.Attempt = final
	latest, _, e = control.Load(cleanupCtx, op.ID)
	if e != nil {
		return result, errors.Join(databaseErr, ErrOutcomeRecord, e)
	}
	if !matchingRecordedOperation(latest, final) {
		return result, errors.Join(databaseErr, ErrOutcomeRecord)
	}
	phase := "validating"
	var failure *string
	if databaseErr != nil {
		code := "database_failed"
		if errors.Is(databaseErr, ErrObjectReferences) || errors.Is(databaseErr, ErrHeaderEnvelope) || errors.Is(databaseErr, ErrHeaderDecrypt) || errors.Is(databaseErr, ErrHeaderInventory) {
			code = "validation_failed"
		}
		failure = &code
		phase = "failed"
		if errors.Is(databaseErr, context.Canceled) {
			phase = "canceled"
			failure = nil
		}
	}
	result.Operation, e = advanceRecordedPhase(cleanupCtx, control, latest, phase, failure)
	if e != nil {
		return result, errors.Join(databaseErr, ErrOutcomeRecord, e)
	}
	if databaseErr != nil {
		pin.Close()
		if e = stage.Close(); e != nil {
			return result, errors.Join(databaseErr, e)
		}
	}
	return result, databaseErr
}
