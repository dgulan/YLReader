package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"yreader/backend/internal/pkg/storage"
)

var errBehaviorIntent = errors.New("backup: behavior intent invalid or stale")
var errBehaviorIntentUnknown = errors.New("backup: behavior intent unconfirmed; retain records and do not retry login")

type behaviorIntent struct {
	Format         string    `json:"intent_format"`
	Version        int       `json:"intent_version"`
	Scope          string    `json:"scope"`
	State          string    `json:"state"`
	ConsentID      string    `json:"consent_id"`
	OperationID    string    `json:"operation_id"`
	OwnerID        string    `json:"owner_id"`
	PlanDigest     string    `json:"plan_digest"`
	TargetID       string    `json:"target_instance_id"`
	Generation     int64     `json:"observed_generation"`
	OperationSHA   string    `json:"operation_record_sha256"`
	AttemptSHA     string    `json:"attempt_record_sha256"`
	PublicationSHA string    `json:"publication_record_sha256"`
	RuntimeSHA     string    `json:"runtime_config_revision_sha256"`
	Users          []string  `json:"user_ids"`
	Issued         time.Time `json:"issued_at"`
	Expires        time.Time `json:"expires_at"`
	Recorded       time.Time `json:"recorded_at"`
}

func (r behaviorIntent) validate() error {
	if r.Format != "ylreader-behavior-intent" || r.Version != 1 || r.Scope != "isolated-login-intent-only" || r.State != "dispatch_reserved" || !validUUID(r.ConsentID) || !validUUID(r.OperationID) || !validUUID(r.OwnerID) || !validUUID(r.TargetID) || !validSHA(r.PlanDigest) || !validSHA(r.OperationSHA) || !validSHA(r.AttemptSHA) || !validSHA(r.PublicationSHA) || !validSHA(r.RuntimeSHA) || r.Generation < 1 || r.Generation > 9007199254740991 || !validStamp(r.Issued) || !validStamp(r.Expires) || !validStamp(r.Recorded) || !r.Expires.After(r.Issued) || r.Expires.Sub(r.Issued) > 5*time.Minute || r.Recorded.Before(r.Issued) || !r.Recorded.Before(r.Expires) || len(r.Users) < 1 || len(r.Users) > 8 {
		return errBehaviorIntent
	}
	seen := map[string]bool{}
	for _, id := range r.Users {
		if !validUUID(id) || seen[id] {
			return errBehaviorIntent
		}
		seen[id] = true
	}
	return nil
}
func behaviorIntentKey(operation string) (string, error) {
	if !validUUID(operation) {
		return "", errBehaviorIntent
	}
	return "operations/" + operation + "/behavior-intent.json", nil
}

func loadBehaviorIntent(data []byte, operation string) (behaviorIntent, error) {
	var r behaviorIntent
	if len(data) > 16<<10 || exactJSON(data, &r) != nil || r.validate() != nil || r.OperationID != operation {
		return behaviorIntent{}, errBehaviorIntent
	}
	return r, nil
}

// Internal durable marker only. No dispatch capability is returned or recovered.
func recordBehaviorIntent(ctx context.Context, files storage.ConditionalFiles, prepared *preparedBehaviorInput, p Plan, runtimeSHA string, clock func() time.Time) (behaviorIntent, error) {
	if ctx == nil || files == nil || prepared == nil || clock == nil || ctx.Err() != nil {
		return behaviorIntent{}, errBehaviorIntent
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a := prepared.consent
	if a.validate() != nil || len(prepared.accounts) != len(a.Accounts) {
		return behaviorIntent{}, errBehaviorIntent
	}
	for i, secret := range prepared.accounts {
		if !secret.valid(a.Accounts[i].UserID) {
			return behaviorIntent{}, errBehaviorIntent
		}
	}
	s, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	now := clock()
	if e != nil || a.matches(p, s, runtimeSHA, now) != nil {
		return behaviorIntent{}, errBehaviorIntent
	}
	r := behaviorIntent{Format: "ylreader-behavior-intent", Version: 1, Scope: "isolated-login-intent-only", State: "dispatch_reserved", ConsentID: a.ID, OperationID: a.OperationID, OwnerID: a.OwnerID, PlanDigest: a.PlanDigest, TargetID: a.TargetID, Generation: a.Generation, OperationSHA: a.OperationSHA, AttemptSHA: a.AttemptSHA, PublicationSHA: a.PublicationSHA, RuntimeSHA: a.RuntimeSHA, Issued: a.Issued, Expires: a.Expires, Recorded: now}
	for _, account := range a.Accounts {
		r.Users = append(r.Users, account.UserID)
	}
	if r.validate() != nil {
		return behaviorIntent{}, errBehaviorIntent
	}
	key, _ := behaviorIntentKey(p.OperationID)
	data, _ := json.Marshal(r)
	if e = files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data))); e != nil {
		if !errors.Is(e, storage.ErrCommitUnknown) && (errors.Is(e, storage.ErrConflict) || errors.Is(e, storage.ErrBusy)) {
			return behaviorIntent{}, storage.ErrConflict
		}
		return behaviorIntent{}, errBehaviorIntentUnknown
	}
	body, present, e := optionalStatusBytes(ctx, files, key, 16<<10)
	if e != nil || !present || !bytes.Equal(body, data) {
		return behaviorIntent{}, errBehaviorIntentUnknown
	}
	if _, e = loadBehaviorIntent(body, p.OperationID); e != nil {
		return behaviorIntent{}, errBehaviorIntentUnknown
	}
	after, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil || s.tokens != after.tokens || a.matches(p, after, runtimeSHA, clock()) != nil || ctx.Err() != nil {
		return behaviorIntent{}, errBehaviorIntentUnknown
	}
	return r, nil
}

type BehaviorIntentDiagnostic struct {
	Format      string `json:"diagnostic_format"`
	Version     int    `json:"diagnostic_version"`
	Scope       string `json:"scope"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	Persistence string `json:"persistence_confirmation"`
	Dispatch    bool   `json:"dispatch_authorized"`
	Retry       bool   `json:"retry_authorized"`
	Deployment  bool   `json:"deployment_authorized"`
}

type behaviorIntentSnapshot struct {
	control diagnosticSnapshot
	raw     []byte
	present bool
}

func readBehaviorIntentSnapshot(ctx context.Context, reader RestoreRecordReader, id string) (s behaviorIntentSnapshot, e error) {
	s.control, e = readDiagnosticSnapshot(ctx, diagnosticFiles{reader}, id)
	if e != nil {
		return
	}
	key, _ := behaviorIntentKey(id)
	s.raw, s.present, e = optionalStatusBytes(ctx, reader, key, 16<<10)
	return
}

// DiagnoseBehaviorIntent observes records, never whether a request was sent.
func DiagnoseBehaviorIntent(ctx context.Context, reader RestoreRecordReader, id string, now time.Time) BehaviorIntentDiagnostic {
	d := BehaviorIntentDiagnostic{Format: "ylreader-behavior-intent-diagnostic", Version: 1, Scope: "control-records-only", Status: "unavailable", Reason: "invalid_input", Persistence: "not_inferred"}
	if !validUUID(id) {
		return d
	}
	d.OperationID = id
	if ctx == nil || reader == nil || !validStamp(now) {
		return d
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a, e := readBehaviorIntentSnapshot(ctx, reader, id)
	if e != nil {
		d.Reason = "records_unavailable"
		return d
	}
	b, e := readBehaviorIntentSnapshot(ctx, reader, id)
	if e != nil || ctx.Err() != nil {
		d.Reason = "records_unavailable"
		return d
	}
	if a.control.tokens != b.control.tokens || a.present != b.present || !bytes.Equal(a.raw, b.raw) {
		d.Status = "changed"
		d.Reason = "records_changed"
		return d
	}
	if !coherentBehaviorControl(b.control, id) {
		d.Status = "inconsistent"
		d.Reason = "control_binding_mismatch"
		return d
	}
	if !b.present {
		d.Status = "intent_not_observed"
		d.Reason = "no_intent_observed_no_authority"
		return d
	}
	r, e := loadBehaviorIntent(b.raw, id)
	if e != nil {
		d.Reason = "intent_invalid"
		return d
	}
	s := b.control
	if r.OwnerID != s.op.OwnerID || r.PlanDigest != s.op.PlanDigest {
		d.Status = "inconsistent"
		d.Reason = "intent_binding_mismatch"
		return d
	}
	if r.Generation != s.op.Generation || [3]string{r.OperationSHA, r.AttemptSHA, r.PublicationSHA} != s.tokens || s.op.CancelRequested || s.op.Phase != "validating" || s.attempt.DatabaseOutcome != "committed" || s.pub.State != "verified" || now.Before(r.Recorded) || !now.Before(r.Expires) {
		d.Status = "stale"
		d.Reason = "intent_stale_outcome_unknown"
		return d
	}
	d.Status = "outcome_unknown"
	d.Reason = "reserved_does_not_prove_dispatch_or_response"
	return d
}

func coherentBehaviorControl(s diagnosticSnapshot, id string) bool {
	o, a, p := s.op, s.attempt, s.pub
	return o.ID == id && a.OperationID == id && p.OperationID == id && o.BackupID == a.BackupID && o.BackupID == p.BackupID && o.PlanDigest == a.PlanDigest && o.PlanDigest == p.PlanDigest && o.OwnerID == a.OwnerID && o.OwnerID == p.OwnerID && a.TargetFingerprint == p.TargetFingerprint && a.Generation != nil && *a.Generation == p.Staging
}
