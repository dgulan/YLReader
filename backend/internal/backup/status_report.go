package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"yreader/backend/internal/pkg/storage"
)

// BoundReviewDiagnostic reports read-only observations of behavior-account-review.json.
// Disk visibility never confirms historical persistence or grants execution/cutover authority.
type BoundReviewDiagnostic struct {
	Present          bool     `json:"present"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason"`
	Profile          string   `json:"profile,omitempty"`
	ReportStatus     string   `json:"report_status,omitempty"`
	Freshness        string   `json:"freshness"`
	ControlBinding   string   `json:"control_binding"`
	ObservedGen      int64    `json:"observed_generation,omitempty"`
	CurrentGen       int64    `json:"current_generation,omitempty"`
	PendingChecks    []string `json:"pending_checks,omitempty"`
	FailedChecks     []string `json:"failed_checks,omitempty"`
	Registration     string   `json:"registration_state,omitempty"`
	RegistrationBind string   `json:"registration_binding,omitempty"`
	Persistence      string   `json:"persistence_confirmation"`
	Execution        bool     `json:"execution_authorized"`
	Ready            bool     `json:"ready_for_cutover"`
	Deployment       bool     `json:"deployment_authorized"`
}

func defaultBoundReviewDiagnostic(status, reason string) BoundReviewDiagnostic {
	return BoundReviewDiagnostic{
		Status:         status,
		Reason:         reason,
		Freshness:      "not_observed",
		ControlBinding: "not_observed",
		Persistence:    "not_inferred",
	}
}

type optionalBoundReviewRead struct {
	present bool
	invalid bool
	sha     string
	review  boundBehaviorReview
}

func readOptionalBoundReview(ctx context.Context, r RestoreRecordReader, id string) (optionalBoundReviewRead, error) {
	key, e := boundBehaviorReviewKey(id)
	if e != nil {
		return optionalBoundReviewRead{}, ErrState
	}
	reader, e := r.Get(ctx, key)
	if errors.Is(e, storage.ErrNotFound) {
		return optionalBoundReviewRead{}, nil
	}
	if e != nil || reader == nil {
		return optionalBoundReviewRead{}, ErrState
	}
	data, e := io.ReadAll(io.LimitReader(reader, 32769))
	closed := reader.Close()
	if e != nil || closed != nil || ctx.Err() != nil {
		return optionalBoundReviewRead{}, ErrState
	}
	out := optionalBoundReviewRead{present: true, sha: digestBytes(data)}
	if len(data) > 32768 {
		out.invalid = true
		return out, nil
	}
	parsed, e := loadBoundBehaviorReview(bytes.NewReader(data))
	if e != nil || parsed.OperationID != id {
		out.invalid = true
		return out, nil
	}
	out.review = parsed
	return out, nil
}

func evaluateBoundReview(
	c diagnosticSnapshot,
	boundRead optionalBoundReviewRead,
	hasReg bool,
	reg handoverRegistration,
	now time.Time,
	ledgerCheck func(boundBehaviorReview) (bool, string),
) BoundReviewDiagnostic {
	out := defaultBoundReviewDiagnostic("absent", "bound_review_absent")
	out.CurrentGen = c.op.Generation
	out.Registration = "absent"
	if hasReg {
		out.Registration = reg.State
	}
	if !boundRead.present {
		if hasReg && reg.Scope == "bound-behavior-review-registration" {
			out.Status = "inconsistent"
			out.Reason = "bound_registration_without_report"
			out.RegistrationBind = "missing_report"
		}
		return out
	}
	out.Present = true
	if boundRead.invalid {
		out.Status = "invalid"
		out.Reason = "bound_review_malformed"
		return out
	}
	b := boundRead.review
	out.Profile = b.Profile
	out.ReportStatus = b.Status
	out.ObservedGen = b.Generation
	for _, chk := range b.Checks {
		if chk.Status == "unavailable" {
			out.PendingChecks = append(out.PendingChecks, chk.ID)
		} else if chk.Status == "fail" {
			out.FailedChecks = append(out.FailedChecks, chk.ID)
		}
	}
	out.Freshness = "fresh"
	if now.Before(b.Finished) {
		out.Freshness = "not_yet_valid"
	} else if !now.Before(b.Expires) {
		out.Freshness = "expired"
	}

	o, p := c.op, c.pub
	if b.BackupID != o.BackupID || b.PlanDigest != o.PlanDigest || b.PackageSHA != p.PackageSHA || b.ManifestSHA != p.ManifestSHA || b.Storage != p.TargetFingerprint || b.Tree != p.TreeFingerprint || b.AttemptSHA != c.tokens[1] || b.PublicationSHA != c.tokens[2] {
		out.ControlBinding = "control_mismatch"
		out.Status = "inconsistent"
		out.Reason = "bound_review_binding_mismatch"
		return out
	}
	if ledgerCheck != nil {
		if ok, reason := ledgerCheck(b); !ok {
			out.ControlBinding = "ledger_mismatch"
			out.Status = "inconsistent"
			out.Reason = reason
			return out
		}
	}

	switch {
	case !hasReg:
		out.RegistrationBind = "absent"
		if b.Generation != o.Generation || b.OperationSHA != c.tokens[0] {
			out.ControlBinding = "stale_generation_or_state"
			out.Status = "drifted"
			out.Reason = "operation_changed_since_report"
			return out
		}
		if b.Started.Before(o.UpdatedAt) {
			out.ControlBinding = "control_mismatch"
			out.Status = "inconsistent"
			out.Reason = "bound_review_state_time_mismatch"
			return out
		}
		out.ControlBinding = "matches_current_control"
	case reg.Scope != "bound-behavior-review-registration":
		out.RegistrationBind = "other_review_registered"
		if b.Generation != o.Generation || b.OperationSHA != c.tokens[0] {
			out.ControlBinding = "stale_generation_or_state"
			out.Status = "drifted"
			out.Reason = "operation_changed_since_report"
			return out
		}
		if b.Started.Before(o.UpdatedAt) {
			out.ControlBinding = "control_mismatch"
			out.Status = "inconsistent"
			out.Reason = "bound_review_state_time_mismatch"
			return out
		}
		out.ControlBinding = "matches_current_control"
	default:
		if reg.BackupID != o.BackupID || reg.OwnerID != o.OwnerID || reg.PlanDigest != o.PlanDigest || reg.ReportSHA != boundRead.sha || reg.BaseGeneration != b.Generation || reg.BaseSHA != b.OperationSHA || reg.Recorded.Before(b.Finished) || !reg.Recorded.Before(b.Expires) {
			out.ControlBinding = "control_mismatch"
			out.RegistrationBind = "binding_mismatch"
			out.Status = "inconsistent"
			out.Reason = "registration_binding_mismatch"
			return out
		}
		out.ControlBinding = "matches_registration_base"
		if b.Status != "complete" || len(out.PendingChecks) > 0 || len(out.FailedChecks) > 0 {
			out.RegistrationBind = "partial_review_forbidden"
			out.Status = "inconsistent"
			out.Reason = "partial_bound_review_registered"
			return out
		}
		if now.Before(reg.Recorded) {
			out.RegistrationBind = "time_not_yet_observed"
			out.Status = "inconsistent"
			out.Reason = "registration_time_not_yet_observed"
			return out
		}
		switch {
		case c.tokens[0] == reg.BaseSHA && o.Generation == reg.BaseGeneration:
			out.RegistrationBind = "bound_base_observed"
			if b.Started.Before(o.UpdatedAt) {
				out.ControlBinding = "control_mismatch"
				out.RegistrationBind = "binding_mismatch"
				out.Status = "inconsistent"
				out.Reason = "bound_review_state_time_mismatch"
				return out
			}
			if reg.State != "prepared" {
				out.RegistrationBind = "registered_without_next_state"
				out.Status = "inconsistent"
				out.Reason = "registered_without_next_state"
				return out
			}
		case c.tokens[0] == reg.NextSHA && o.Generation == reg.NextGeneration:
			out.RegistrationBind = "bound_next_observed"
			if !o.UpdatedAt.Equal(reg.Recorded) || !o.HeartbeatAt.Equal(reg.Recorded) {
				out.ControlBinding = "control_mismatch"
				out.RegistrationBind = "binding_mismatch"
				out.Status = "inconsistent"
				out.Reason = "registration_time_mismatch"
				return out
			}
		default:
			out.ControlBinding = "stale_generation_or_state"
			out.RegistrationBind = "drifted"
			out.Status = "drifted"
			out.Reason = "operation_changed_since_registration"
			return out
		}
	}

	if out.Freshness == "not_yet_valid" {
		out.Status = "inconsistent"
		out.Reason = "bound_review_not_yet_valid"
		return out
	}
	if out.Freshness == "expired" {
		out.Status = "expired"
		out.Reason = "bound_review_expired"
		return out
	}
	if b.Status == "partial" || len(out.PendingChecks) > 0 || len(out.FailedChecks) > 0 {
		out.Status = "partial"
		out.Reason = "bound_review_checks_incomplete"
		return out
	}
	if hasReg && reg.Scope == "bound-behavior-review-registration" {
		if reg.State == "prepared" {
			out.Status = "registration_pending"
			out.Reason = "prepared_base_visible"
			if out.RegistrationBind == "bound_next_observed" {
				out.Reason = "prepared_next_visible"
			}
			return out
		}
		out.Status = "complete_registered"
		out.Reason = "bound_registered_record_visible"
		return out
	}
	out.Status = "complete_unregistered"
	out.Reason = "bound_review_visible_unregistered"
	return out
}

func applyBoundStatusToRestore(out *RestoreStatus, s statusSnapshot) {
	b := out.BoundReview
	if !b.Present {
		if s.hasRegistration && s.registration.Scope == "bound-behavior-review-registration" {
			out.Status, out.Reason = "inconsistent", "registration_without_report"
		}
		return
	}
	if b.Status == "invalid" {
		out.ReportClaims, out.ReportFreshness = "invalid", "not_observed"
		out.Status, out.Reason = "unavailable", b.Reason
		return
	}
	out.ReportFreshness = b.Freshness
	if b.ReportStatus == "complete" && len(b.PendingChecks) == 0 && len(b.FailedChecks) == 0 {
		out.ReportClaims = "all_checks_pass"
	} else {
		out.ReportClaims = "partial"
	}
	switch b.RegistrationBind {
	case "bound_base_observed":
		out.Chain = "base_observed"
	case "bound_next_observed":
		out.Chain = "next_observed"
	case "drifted":
		out.Chain = "drifted"
	}
	switch b.Status {
	case "inconsistent":
		out.Status, out.Reason = "inconsistent", b.Reason
	case "drifted":
		out.Status, out.Reason = "blocked", b.Reason
	case "expired":
		out.Status, out.Reason = "blocked", "report_expired"
	case "partial":
		out.Status, out.Reason = "blocked", "bound_review_checks_incomplete"
	case "registration_pending":
		out.Status, out.Reason = "registration_pending", b.Reason
	case "complete_unregistered":
		out.Status, out.Reason = "review_only", b.Reason
	case "complete_registered":
		out.Status, out.Reason = "observed_registered", b.Reason
	}
}
