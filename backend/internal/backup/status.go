package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"yreader/backend/internal/pkg/storage"
)

// RestoreStatus reports file observations, never historical durability or
// authority to resume/cut over. No target connection or write capability.
type RestoreStatus struct {
	Format          string `json:"diagnostic_format"`
	Version         int    `json:"diagnostic_version"`
	Scope           string `json:"scope"`
	OperationID     string `json:"operation_id"`
	Status          string `json:"status"`
	Reason          string `json:"reason"`
	Generation      int64  `json:"observed_generation,omitempty"`
	Phase           string `json:"operation_phase,omitempty"`
	Database        string `json:"database_outcome,omitempty"`
	Publication     string `json:"publication_state,omitempty"`
	ReportClaims    string `json:"report_claims"`
	ReportFreshness string `json:"report_freshness"`
	Registration    string `json:"registration_state"`
	Chain           string `json:"registration_chain"`
	Persistence     string `json:"persistence_confirmation"`
	Ready           bool   `json:"ready_for_cutover"`
	Deployment      bool   `json:"deployment_authorized"`
}

type statusSnapshot struct {
	control                    diagnosticSnapshot
	report                     handoverReview
	registration               handoverRegistration
	hasReport, hasRegistration bool
	tokens                     [5]string
}

func optionalStatusBytes(ctx context.Context, r RestoreRecordReader, key string, limit int64) ([]byte, bool, error) {
	reader, e := r.Get(ctx, key)
	if errors.Is(e, storage.ErrNotFound) {
		return nil, false, nil
	}
	if e != nil || reader == nil {
		return nil, false, ErrState
	}
	data, e := io.ReadAll(io.LimitReader(reader, limit+1))
	closed := reader.Close()
	if e != nil || closed != nil || int64(len(data)) > limit {
		return nil, false, ErrState
	}
	return data, true, nil
}

func readStatusSnapshot(ctx context.Context, r RestoreRecordReader, id string) (s statusSnapshot, err error) {
	s.control, err = readDiagnosticSnapshot(ctx, diagnosticFiles{r}, id)
	if err != nil {
		return
	}
	copy(s.tokens[:3], s.control.tokens[:])
	key, _ := handoverReviewKey(id)
	data, exists, e := optionalStatusBytes(ctx, r, key, 32768)
	if e != nil {
		return s, e
	}
	s.hasReport = exists
	if exists {
		s.report, e = loadHandoverReview(bytes.NewReader(data))
		if e != nil || s.report.OperationID != id {
			return s, ErrState
		}
		s.tokens[3] = digestBytes(data)
	}
	key, _ = registrationKey(id)
	data, exists, e = optionalStatusBytes(ctx, r, key, 8192)
	if e != nil {
		return s, e
	}
	s.hasRegistration = exists
	if exists {
		if exactJSON(data, &s.registration) != nil || s.registration.validate() != nil || s.registration.OperationID != id {
			return s, ErrState
		}
		s.tokens[4] = digestBytes(data)
	}
	return s, ctx.Err()
}

func statusControlBound(s diagnosticSnapshot) bool {
	o, a, p := s.op, s.attempt, s.pub
	return o.ID == a.OperationID && o.ID == p.OperationID && o.BackupID == a.BackupID && o.BackupID == p.BackupID && o.PlanDigest == a.PlanDigest && o.PlanDigest == p.PlanDigest &&
		o.OwnerID == a.OwnerID && o.OwnerID == p.OwnerID && a.TargetFingerprint == p.TargetFingerprint && a.Generation != nil && *a.Generation == p.Staging
}

// DiagnoseRestoreStatus uses only bounded Get reads. Registered visibility is
// deliberately indistinguishable from a lost-ack write with the same bytes.
func DiagnoseRestoreStatus(ctx context.Context, r RestoreRecordReader, id string, now time.Time) RestoreStatus {
	out := RestoreStatus{Format: "ylreader-restore-status", Version: 1, Scope: "control-records-only", Status: "unavailable", Reason: "invalid_input", ReportClaims: "not_observed", ReportFreshness: "not_observed", Registration: "not_observed", Chain: "not_applicable", Persistence: "not_inferred"}
	if !validUUID(id) {
		return out
	}
	out.OperationID = id
	if ctx == nil || r == nil || !validStamp(now) {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, e := readStatusSnapshot(ctx, r, id)
	if e != nil {
		out.Reason = "record_invalid_or_unavailable"
		if errors.Is(e, storage.ErrNotFound) {
			out.Reason = "record_missing"
		}
		return out
	}
	second, e := readStatusSnapshot(ctx, r, id)
	if e != nil || s.tokens != second.tokens {
		out.Status, out.Reason = "changed", "snapshot_changed"
		return out
	}
	c := s.control
	o, a, p := c.op, c.attempt, c.pub
	if !statusControlBound(c) {
		out.Status, out.Reason = "inconsistent", "control_binding_mismatch"
		return out
	}
	out.Generation, out.Phase, out.Database, out.Publication = o.Generation, o.Phase, a.DatabaseOutcome, p.State
	out.ReportClaims, out.Registration = "absent", "absent"
	if s.hasRegistration {
		out.Registration = s.registration.State
	}
	out.Status, out.Reason = "records_only", "report_absent"
	if s.hasRegistration && !s.hasReport {
		out.Status, out.Reason = "inconsistent", "registration_without_report"
		return out
	}
	if s.hasReport {
		report := s.report
		if report.BackupID != o.BackupID || report.PlanDigest != o.PlanDigest || report.PackageSHA != p.PackageSHA || report.ManifestSHA != p.ManifestSHA || report.StorageFingerprint != p.TargetFingerprint || report.TreeFingerprint != p.TreeFingerprint || report.AttemptSHA != c.tokens[1] || report.PublicationSHA != c.tokens[2] {
			out.Status, out.Reason = "inconsistent", "report_binding_mismatch"
			return out
		}
		out.ReportClaims = "all_checks_pass"
		for _, check := range report.Checks {
			if check.Status != "pass" {
				out.ReportClaims = "partial"
			}
		}
		out.ReportFreshness = "fresh"
		if now.Before(report.Finished) {
			out.ReportFreshness = "not_yet_valid"
		} else if !now.Before(report.Expires) {
			out.ReportFreshness = "expired"
		}
		if !s.hasRegistration {
			if report.Generation != o.Generation || report.OperationSHA != c.tokens[0] || report.Started.Before(o.UpdatedAt) {
				out.Status, out.Reason = "inconsistent", "report_state_mismatch"
				return out
			}
			out.Status, out.Reason = "review_only", "report_visible_unregistered"
		} else {
			reg := s.registration
			out.Registration = reg.State
			if reg.BackupID != o.BackupID || reg.OwnerID != o.OwnerID || reg.PlanDigest != o.PlanDigest || reg.ReportSHA != s.tokens[3] || reg.BaseGeneration != report.Generation || reg.BaseSHA != report.OperationSHA || reg.Recorded.Before(report.Finished) || !reg.Recorded.Before(report.Expires) {
				out.Status, out.Reason = "inconsistent", "registration_binding_mismatch"
				return out
			}
			if now.Before(reg.Recorded) {
				out.Status, out.Reason = "blocked", "registration_time_not_yet_observed"
				return out
			}
			switch {
			case c.tokens[0] == reg.BaseSHA && o.Generation == reg.BaseGeneration:
				out.Chain = "base_observed"
				if report.Started.Before(o.UpdatedAt) {
					out.Status, out.Reason = "inconsistent", "report_state_time_mismatch"
					return out
				}
				if reg.State != "prepared" {
					out.Status, out.Reason = "inconsistent", "registered_without_next_state"
					return out
				}
				out.Status, out.Reason = "registration_pending", "prepared_base_visible"
			case c.tokens[0] == reg.NextSHA && o.Generation == reg.NextGeneration:
				out.Chain = "next_observed"
				if !o.UpdatedAt.Equal(reg.Recorded) || !o.HeartbeatAt.Equal(reg.Recorded) {
					out.Status, out.Reason = "inconsistent", "registration_time_mismatch"
					return out
				}
				out.Status, out.Reason = "registration_pending", "prepared_next_visible"
				if reg.State == "registered" {
					out.Status, out.Reason = "observed_registered", "registered_record_visible"
				}
			default:
				out.Chain = "drifted"
				out.Status, out.Reason = "blocked", "operation_changed_since_registration"
				return out
			}
		}
		if out.ReportClaims != "all_checks_pass" {
			out.Status, out.Reason = "blocked", "report_checks_incomplete"
			return out
		}
		if out.ReportFreshness != "fresh" {
			out.Status, out.Reason = "blocked", "report_"+out.ReportFreshness
			return out
		}
	}
	switch {
	case o.CancelRequested:
		out.Status, out.Reason = "blocked", "cancellation_requested"
	case o.Phase != "validating":
		out.Status, out.Reason = "blocked", "operation_not_validating"
	case a.DatabaseOutcome != "committed":
		out.Status, out.Reason = "blocked", "database_unconfirmed"
	case p.State != "verified":
		out.Status, out.Reason = "blocked", "publication_unconfirmed"
	}
	return out
}
