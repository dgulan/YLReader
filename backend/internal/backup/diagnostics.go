package backup

import (
	"context"
	"errors"
	"io"

	"yreader/backend/internal/pkg/storage"
)

// RestoreRecordReader deliberately excludes writes and ownership capabilities.
// Supply a safely opened control directory, never an ordinary arbitrary path.
type RestoreRecordReader interface {
	Get(context.Context, string) (io.ReadCloser, error)
}

// RestoreRecordDiagnostic describes records only. Even consistent_records is
// neither a target observation, a commit proof, nor a cutover authorization.
type RestoreRecordDiagnostic struct {
	Format               string `json:"diagnostic_format"`
	Version              int    `json:"diagnostic_version"`
	Scope                string `json:"scope"`
	OperationID          string `json:"operation_id"`
	Status               string `json:"status"`
	Reason               string `json:"reason"`
	ObservedGeneration   int64  `json:"observed_generation,omitempty"`
	DatabaseOutcome      string `json:"database_outcome,omitempty"`
	PublicationState     string `json:"publication_state,omitempty"`
	DeploymentAuthorized bool   `json:"deployment_authorized"`
	ReadyForCutover      bool   `json:"ready_for_cutover"`
}

// Adapt existing strict loaders without exposing the caller's write methods.
type diagnosticFiles struct{ RestoreRecordReader }

func (diagnosticFiles) CreateOnly(context.Context, string, io.Reader, int64) error {
	return storage.ErrCapability
}
func (diagnosticFiles) CompareAndSwap(context.Context, string, string, []byte) error {
	return storage.ErrCapability
}

type diagnosticSnapshot struct {
	op      Operation
	attempt restoreAttempt
	pub     publicationReceipt
	tokens  [3]string
}

func readDiagnosticSnapshot(ctx context.Context, files diagnosticFiles, id string) (s diagnosticSnapshot, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	s.op, s.tokens[0], err = (Control{files}).Load(ctx, id)
	if err != nil {
		return
	}
	s.attempt, s.tokens[1], err = (attemptStore{files}).Load(ctx, id)
	if err != nil {
		return
	}
	s.pub, s.tokens[2], err = (publicationStore{files}).Load(ctx, id)
	if err == nil {
		err = ctx.Err()
	}
	return
}

// DiagnoseRestoreRecords performs two bounded rounds of strict record reads.
// It never resumes, changes, or cleans up a restore. The repeated read is not
// atomic, detects no ABA, and grants no freshness beyond this observation.
func DiagnoseRestoreRecords(ctx context.Context, reader RestoreRecordReader, id string) RestoreRecordDiagnostic {
	r := RestoreRecordDiagnostic{Format: "ylreader-restore-record-diagnostic", Version: 1,
		Scope: "control-records-only", Status: "unavailable", Reason: "invalid_input"}
	if !validUUID(id) {
		return r
	}
	r.OperationID = id
	if reader == nil || ctx == nil {
		return r
	}
	files := diagnosticFiles{reader}
	s, err := readDiagnosticSnapshot(ctx, files, id)
	if err != nil {
		r.Reason = "record_invalid_or_unavailable"
		if errors.Is(err, storage.ErrNotFound) {
			r.Reason = "record_missing"
		}
		return r
	}
	second, err := readDiagnosticSnapshot(ctx, files, id)
	if err != nil || s.tokens != second.tokens {
		r.Status, r.Reason = "changed", "snapshot_changed"
		return r
	}
	o, a, p := s.op, s.attempt, s.pub
	if o.ID != a.OperationID || o.ID != p.OperationID || o.BackupID != a.BackupID || o.BackupID != p.BackupID ||
		o.PlanDigest != a.PlanDigest || o.PlanDigest != p.PlanDigest || o.OwnerID != a.OwnerID || o.OwnerID != p.OwnerID ||
		a.TargetFingerprint != p.TargetFingerprint || a.Generation == nil || *a.Generation != p.Staging {
		r.Status, r.Reason = "inconsistent", "binding_mismatch"
		return r
	}
	r.ObservedGeneration, r.DatabaseOutcome, r.PublicationState = o.Generation, a.DatabaseOutcome, p.State
	r.Status = "blocked"
	switch {
	case o.CancelRequested:
		r.Reason = "cancellation_requested"
	case o.Phase != "validating":
		r.Reason = "operation_not_validating"
	case a.DatabaseOutcome != "committed":
		r.Reason = "database_unconfirmed"
	case p.State != "verified":
		r.Reason = "publication_unconfirmed"
	default:
		r.Status, r.Reason = "consistent_records", "records_match"
	}
	return r
}
