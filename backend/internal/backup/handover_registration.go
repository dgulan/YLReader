package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"yreader/backend/internal/pkg/storage"
)

var errRegistration = errors.New("backup: invalid or stale isolated review registration")
var errRegistrationUnknown = errors.New("backup: review registration unconfirmed; retain all records")

type handoverRegistration struct {
	Format         string    `json:"registration_format"`
	Version        int       `json:"registration_version"`
	Scope          string    `json:"scope"`
	OperationID    string    `json:"operation_id"`
	BackupID       string    `json:"backup_id"`
	OwnerID        string    `json:"owner_id"`
	PlanDigest     string    `json:"plan_digest"`
	ReportSHA      string    `json:"report_sha256"`
	BaseSHA        string    `json:"base_state_sha256"`
	BaseGeneration int64     `json:"base_generation"`
	NextSHA        string    `json:"next_state_sha256"`
	NextGeneration int64     `json:"next_generation"`
	Recorded       time.Time `json:"recorded_at"`
	State          string    `json:"state"`
	Ready          bool      `json:"ready_for_cutover"`
	Deployment     bool      `json:"deployment_authorized"`
}

func (r handoverRegistration) validate() error {
	if r.Format != "ylreader-handover-registration" || r.Version != 1 || r.Scope != "isolated-review-registration" || !validUUID(r.OperationID) || !validUUID(r.BackupID) || !validUUID(r.OwnerID) ||
		!validSHA(r.PlanDigest) || !validSHA(r.ReportSHA) || !validSHA(r.BaseSHA) || !validSHA(r.NextSHA) || r.BaseGeneration < 1 || r.BaseGeneration >= 9007199254740991 || r.NextGeneration != r.BaseGeneration+1 ||
		!validStamp(r.Recorded) || (r.State != "prepared" && r.State != "registered") || r.Ready || r.Deployment {
		return errRegistration
	}
	return nil
}

type handoverRegistrationStore struct{ Files storage.ConditionalFiles }

func registrationKey(id string) (string, error) {
	if !validUUID(id) {
		return "", errRegistration
	}
	return "operations/" + id + "/handover-registration.json", nil
}
func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (s handoverRegistrationStore) Load(ctx context.Context, id string) (handoverRegistration, string, error) {
	key, e := registrationKey(id)
	if e != nil || s.Files == nil {
		return handoverRegistration{}, "", errRegistration
	}
	r, e := s.Files.Get(ctx, key)
	if e != nil {
		return handoverRegistration{}, "", errRegistration
	}
	data, e := io.ReadAll(io.LimitReader(r, 8193))
	closed := r.Close()
	var value handoverRegistration
	if e != nil || closed != nil || len(data) > 8192 || exactJSON(data, &value) != nil || value.validate() != nil || value.OperationID != id {
		return handoverRegistration{}, "", errRegistration
	}
	return value, digestBytes(data), nil
}

func registrationWriteError(e error) error {
	if !errors.Is(e, storage.ErrCommitUnknown) && (errors.Is(e, storage.ErrConflict) || errors.Is(e, storage.ErrBusy)) {
		return storage.ErrConflict
	}
	return errRegistrationUnknown
}

// The caller must hold the live guard/confirmation capability. This internal
// core is separately tested with synthetic records; it is never a JSON API.
func registerIsolatedReview(ctx context.Context, files storage.ConditionalFiles, p Plan, report handoverReview, clock func() time.Time) (handoverRegistration, error) {
	if ctx == nil || files == nil || clock == nil {
		return handoverRegistration{}, errRegistration
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	old, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	now := clock()
	if e != nil || report.validateFor(p, old, report.TargetIdentity, now) != nil || !now.After(old.op.UpdatedAt) {
		return handoverRegistration{}, errRegistration
	}
	stored, reportSHA, e := (handoverReviewStore{files}).Load(ctx, p.OperationID)
	data, _ := json.Marshal(report)
	if e != nil || reportSHA != digestBytes(data) || stored.validateFor(p, old, report.TargetIdentity, now) != nil {
		return handoverRegistration{}, errRegistration
	}
	next := old.op
	next.Generation++
	next.UpdatedAt = now
	next.HeartbeatAt = now
	if ValidateTransition(old.op, next, old.op.Generation, false) != nil {
		return handoverRegistration{}, errRegistration
	}
	nextBytes, _ := json.Marshal(next)
	prepared := handoverRegistration{Format: "ylreader-handover-registration", Version: 1, Scope: "isolated-review-registration", OperationID: p.OperationID, BackupID: p.BackupID, OwnerID: old.op.OwnerID,
		PlanDigest: p.Digest, ReportSHA: reportSHA, BaseSHA: old.tokens[0], BaseGeneration: old.op.Generation, NextSHA: digestBytes(nextBytes), NextGeneration: next.Generation, Recorded: now, State: "prepared"}
	if prepared.validate() != nil {
		return handoverRegistration{}, errRegistration
	}
	key, _ := registrationKey(p.OperationID)
	preparedBytes, _ := json.Marshal(prepared)
	if e = files.CreateOnly(ctx, key, bytes.NewReader(preparedBytes), int64(len(preparedBytes))); e != nil {
		return handoverRegistration{}, registrationWriteError(e)
	}
	_, preparedSHA, readErr := (handoverRegistrationStore{files}).Load(ctx, p.OperationID)
	_, currentReportSHA, reportErr := (handoverReviewStore{files}).Load(ctx, p.OperationID)
	if readErr != nil || preparedSHA != digestBytes(preparedBytes) || reportErr != nil || currentReportSHA != reportSHA {
		return handoverRegistration{}, errRegistrationUnknown
	}
	// Recheck before the sole state CAS. A late failure retains prepared.
	before, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || before.tokens != old.tokens || report.validateFor(p, before, report.TargetIdentity, clock()) != nil {
		return handoverRegistration{}, errRegistrationUnknown
	}
	stateKey, _ := operationKey(p.OperationID)
	if e = files.CompareAndSwap(ctx, stateKey, old.tokens[0], nextBytes); e != nil {
		return handoverRegistration{}, registrationWriteError(e)
	}
	confirm := func() bool {
		s, e := stableHandoverSnapshot(ctx, files, p.OperationID)
		if e != nil || s.tokens[0] != prepared.NextSHA || s.op.Generation != prepared.NextGeneration || s.tokens[1] != old.tokens[1] || s.tokens[2] != old.tokens[2] || report.validateFor(p, old, report.TargetIdentity, clock()) != nil {
			return false
		}
		_, sha, e := (handoverReviewStore{files}).Load(ctx, p.OperationID)
		return e == nil && sha == reportSHA && ctx.Err() == nil
	}
	if !confirm() {
		return handoverRegistration{}, errRegistrationUnknown
	}
	registered := prepared
	registered.State = "registered"
	registeredBytes, _ := json.Marshal(registered)
	if e = files.CompareAndSwap(ctx, key, digestBytes(preparedBytes), registeredBytes); e != nil {
		return handoverRegistration{}, errRegistrationUnknown
	}
	readBack, sha, e := (handoverRegistrationStore{files}).Load(ctx, p.OperationID)
	if e != nil || sha != digestBytes(registeredBytes) || readBack.State != "registered" || !confirm() {
		return handoverRegistration{}, errRegistrationUnknown
	}
	return readBack, nil
}
