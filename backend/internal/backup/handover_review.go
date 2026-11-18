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

var errHandoverReview = errors.New("backup: invalid, stale or conflicting handover review")
var errHandoverUnconfirmed = errors.New("backup: handover review persistence unconfirmed; retain artifacts")

// Metadata bindings only: never hash secret bytes into these fields. Actual
// identity/isolation collectors are not implemented by this report model.
type handoverTargetIdentity struct {
	Database string `json:"database_identity_sha256"`
	Runtime  string `json:"runtime_config_revision_sha256"`
	Redis    string `json:"redis_identity_sha256"`
}

func (i handoverTargetIdentity) valid() bool {
	return validSHA(i.Database) && validSHA(i.Runtime) && validSHA(i.Redis)
}

type handoverReviewCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func handoverCheckIDs() []string {
	return []string{"schema_ledger", "database_identity", "database_role", "published_objects", "header_keyring", "jwt_rotation", "redis_isolation", "sessions_revoked", "tasks_disabled", "runtime_behavior"}
}

// This review is NOT an execution credential, including when all checks pass.
// No restore transition consumes this type or its stored JSON.
type handoverReview struct {
	Format               string                 `json:"report_format"`
	Version              int                    `json:"report_version"`
	Scope                string                 `json:"scope"`
	OperationID          string                 `json:"operation_id"`
	BackupID             string                 `json:"backup_id"`
	PlanDigest           string                 `json:"plan_digest"`
	PackageSHA           string                 `json:"package_sha256"`
	ManifestSHA          string                 `json:"manifest_sha256"`
	TargetID             string                 `json:"target_instance_id"`
	StorageFingerprint   string                 `json:"target_storage_fingerprint"`
	TreeFingerprint      string                 `json:"publication_tree_fingerprint"`
	Generation           int64                  `json:"observed_generation"`
	OperationSHA         string                 `json:"operation_record_sha256"`
	AttemptSHA           string                 `json:"attempt_record_sha256"`
	PublicationSHA       string                 `json:"publication_record_sha256"`
	TargetIdentity       handoverTargetIdentity `json:"target_identity"`
	Started              time.Time              `json:"started_at"`
	Finished             time.Time              `json:"finished_at"`
	Expires              time.Time              `json:"expires_at"`
	Checks               []handoverReviewCheck  `json:"checks"`
	DeploymentAuthorized bool                   `json:"deployment_authorized"`
	ReadyForCutover      bool                   `json:"ready_for_cutover"`
}

func (r handoverReview) validate() error {
	if r.Format != "ylreader-handover-review" || r.Version != 1 || r.Scope != "isolated-localfs-handover-review" ||
		!validUUID(r.OperationID) || !validUUID(r.BackupID) || !validUUID(r.TargetID) || r.Generation < 1 || r.Generation > 9007199254740991 ||
		!r.TargetIdentity.valid() || r.DeploymentAuthorized || r.ReadyForCutover {
		return errHandoverReview
	}
	for _, digest := range []string{r.PlanDigest, r.PackageSHA, r.ManifestSHA, r.StorageFingerprint, r.TreeFingerprint, r.OperationSHA, r.AttemptSHA, r.PublicationSHA} {
		if !validSHA(digest) {
			return errHandoverReview
		}
	}
	if !validStamp(r.Started) || !validStamp(r.Finished) || !validStamp(r.Expires) || r.Finished.Before(r.Started) ||
		r.Finished.Sub(r.Started) > 5*time.Minute || !r.Expires.After(r.Finished) || r.Expires.Sub(r.Finished) > 5*time.Minute {
		return errHandoverReview
	}
	ids := handoverCheckIDs()
	if len(r.Checks) != len(ids) {
		return errHandoverReview
	}
	for n, c := range r.Checks {
		if c.ID != ids[n] || (c.Status != "pass" && c.Status != "fail" && c.Status != "unavailable") {
			return errHandoverReview
		}
	}
	return nil
}

// validateFor compares a report with freshly supplied, independently collected
// inputs. It does not authenticate those inputs or establish a durable CAS.
func (r handoverReview) validateFor(p Plan, s diagnosticSnapshot, live handoverTargetIdentity, now time.Time) error {
	if r.validateBindingsFor(p, s, live, now) != nil {
		return errHandoverReview
	}
	for _, c := range r.Checks {
		if c.Status != "pass" {
			return errHandoverReview
		}
	}
	return nil
}

// Shared binding validation permits partial reviews, but does not grant the
// all-checks-passed property required by validateFor.
func (r handoverReview) validateBindingsFor(p Plan, s diagnosticSnapshot, live handoverTargetIdentity, now time.Time) error {
	if r.validate() != nil || checkHandoverSnapshot(p, s) != nil ||
		!live.valid() || !validStamp(now) || live != r.TargetIdentity {
		return errHandoverReview
	}
	o, pub := s.op, s.pub
	if r.OperationID != o.ID || r.BackupID != o.BackupID || r.Generation != o.Generation || r.PlanDigest != p.Digest ||
		r.PackageSHA != p.PackageSHA || r.ManifestSHA != p.ManifestSHA || r.TargetID != p.TargetID ||
		r.StorageFingerprint != p.TargetFingerprint || r.TreeFingerprint != pub.TreeFingerprint ||
		r.OperationSHA != s.tokens[0] || r.AttemptSHA != s.tokens[1] || r.PublicationSHA != s.tokens[2] {
		return errHandoverReview
	}
	created, _ := time.Parse(time.RFC3339Nano, p.Created)
	expires, _ := time.Parse(time.RFC3339Nano, p.Expires)
	if r.Started.Before(created) || r.Started.Before(o.UpdatedAt) || r.Expires.After(expires) || now.Before(r.Finished) || !now.Before(r.Expires) {
		return errHandoverReview
	}
	return nil
}

func checkHandoverSnapshot(p Plan, s diagnosticSnapshot) error {
	if p.Validate() != nil || s.op.Validate() != nil || s.attempt.validate() != nil || s.pub.validate() != nil {
		return errHandoverReview
	}
	for _, token := range s.tokens {
		if !validSHA(token) {
			return errHandoverReview
		}
	}
	o, a, pub := s.op, s.attempt, s.pub
	if o.Phase != "validating" || o.CancelRequested || a.DatabaseOutcome != "committed" || pub.State != "verified" ||
		o.ID != p.OperationID || a.OperationID != o.ID || pub.OperationID != o.ID ||
		o.BackupID != p.BackupID || a.BackupID != o.BackupID || pub.BackupID != o.BackupID ||
		o.PlanDigest != p.Digest || a.PlanDigest != o.PlanDigest || pub.PlanDigest != o.PlanDigest ||
		a.OwnerID != o.OwnerID || pub.OwnerID != o.OwnerID ||
		a.TargetFingerprint != p.TargetFingerprint || pub.TargetFingerprint != p.TargetFingerprint ||
		a.Generation == nil || *a.Generation != pub.Staging || pub.PackageSHA != p.PackageSHA || pub.ManifestSHA != p.ManifestSHA {
		return errHandoverReview
	}
	return nil
}

func loadHandoverReview(reader io.Reader) (handoverReview, error) {
	var r handoverReview
	if reader == nil {
		return r, errHandoverReview
	}
	data, e := io.ReadAll(io.LimitReader(reader, 32769))
	if e != nil || len(data) > 32768 || exactJSON(data, &r) != nil || r.validate() != nil {
		return handoverReview{}, errHandoverReview
	}
	return r, nil
}

type handoverReviewStore struct{ Files storage.ConditionalFiles }

func handoverReviewKey(id string) (string, error) {
	if !validUUID(id) {
		return "", errHandoverReview
	}
	return "operations/" + id + "/handover-review.json", nil
}

func (s handoverReviewStore) Load(ctx context.Context, id string) (handoverReview, string, error) {
	key, e := handoverReviewKey(id)
	if e != nil || s.Files == nil {
		return handoverReview{}, "", errHandoverReview
	}
	reader, e := s.Files.Get(ctx, key)
	if e != nil {
		return handoverReview{}, "", errHandoverReview
	}
	data, e := io.ReadAll(io.LimitReader(reader, 32769))
	closed := reader.Close()
	if e != nil || closed != nil {
		return handoverReview{}, "", errHandoverReview
	}
	r, e := loadHandoverReview(bytes.NewReader(data))
	if e != nil || r.OperationID != id {
		return handoverReview{}, "", errHandoverReview
	}
	sum := sha256.Sum256(data)
	return r, hex.EncodeToString(sum[:]), nil
}

// Create preserves even failed reviews for inspection. Persistence confirms
// bytes only; it is neither a check attestation nor an operation transition.
func (s handoverReviewStore) Create(ctx context.Context, r handoverReview) error {
	if s.Files == nil || r.validate() != nil {
		return errHandoverReview
	}
	if ctx.Err() != nil {
		return errHandoverUnconfirmed
	}
	key, _ := handoverReviewKey(r.OperationID)
	data, e := json.Marshal(r)
	if e != nil || len(data) > 32768 {
		return errHandoverReview
	}
	if e = s.Files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data))); e != nil {
		if errors.Is(e, storage.ErrConflict) && !errors.Is(e, storage.ErrCommitUnknown) {
			return storage.ErrConflict
		}
		return errHandoverUnconfirmed
	}
	_, token, e := s.Load(ctx, r.OperationID)
	sum := sha256.Sum256(data)
	if e != nil || ctx.Err() != nil || token != hex.EncodeToString(sum[:]) {
		return errHandoverUnconfirmed
	}
	return nil
}
