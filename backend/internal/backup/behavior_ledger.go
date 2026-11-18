package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"time"
)

var errBehaviorLedger = errors.New("backup: behavior ledger invalid or unavailable")
var errBehaviorLedgerBinding = errors.New("backup: behavior ledger binding mismatch")

type behaviorLedgerSnapshot struct {
	base    behaviorIntentSnapshot
	actions [8][3][]byte
	present [8][3]bool
}

func readBehaviorLedger(ctx context.Context, r RestoreRecordReader, id string) (s behaviorLedgerSnapshot, e error) {
	s.base, e = readBehaviorIntentSnapshot(ctx, r, id)
	if e != nil {
		return s, errBehaviorLedger
	}
	for i := 0; i < 8; i++ {
		for j, name := range []string{"dispatch.json", "response.json", "logout.json"} {
			key := fmt.Sprintf("operations/%s/behavior-actions/%d/%s", id, i, name)
			s.actions[i][j], s.present[i][j], e = optionalStatusBytes(ctx, r, key, 8192)
			if e != nil {
				return s, errBehaviorLedger
			}
		}
	}
	return s, nil
}
func (a behaviorLedgerSnapshot) equal(b behaviorLedgerSnapshot) bool {
	if a.base.control.tokens != b.base.control.tokens || a.base.present != b.base.present || !bytes.Equal(a.base.raw, b.base.raw) || a.present != b.present {
		return false
	}
	for i := range a.actions {
		for j := range a.actions[i] {
			if !bytes.Equal(a.actions[i][j], b.actions[i][j]) {
				return false
			}
		}
	}
	return true
}

type BehaviorLedgerDiagnostic struct {
	Format       string                `json:"diagnostic_format"`
	Version      int                   `json:"diagnostic_version"`
	Scope        string                `json:"scope"`
	OperationID  string                `json:"operation_id"`
	Status       string                `json:"status"`
	Reason       string                `json:"reason"`
	Dispatches   int                   `json:"dispatch_records"`
	Known        int                   `json:"known_session_records"`
	LogoutClaims int                   `json:"logout_response_claims"`
	Persistence  string                `json:"persistence_confirmation"`
	BoundReview  BoundReviewDiagnostic `json:"bound_behavior_review"`
	Dispatch     bool                  `json:"dispatch_authorized"`
	Retry        bool                  `json:"retry_authorized"`
	Deployment   bool                  `json:"deployment_authorized"`
}
type behaviorLedgerAssessment struct {
	intent     behaviorIntent
	known      []behaviorSessionResponse
	diagnostic BehaviorLedgerDiagnostic
}

func ledgerDiagnostic(id string) BehaviorLedgerDiagnostic {
	return BehaviorLedgerDiagnostic{Format: "ylreader-behavior-ledger-diagnostic", Version: 1, Scope: "control-records-only", OperationID: id, Status: "unavailable", Reason: "records_unavailable", Persistence: "not_inferred", BoundReview: defaultBoundReviewDiagnostic("unavailable", "records_unavailable")}
}

func inspectBehaviorLedger(s behaviorLedgerSnapshot, id string, now time.Time) (result behaviorLedgerAssessment, err error) {
	d := ledgerDiagnostic(id)
	result.diagnostic = d
	if !coherentBehaviorControl(s.base.control, id) {
		return result, errBehaviorLedgerBinding
	}
	if !s.base.present {
		for _, row := range s.present {
			for _, present := range row {
				if present {
					return result, errBehaviorLedgerBinding
				}
			}
		}
		d.Status = "intent_not_observed"
		d.Reason = "no_intent_observed_no_authority"
		result.diagnostic = d
		return result, nil
	}
	intent, e := loadBehaviorIntent(s.base.raw, id)
	if e != nil {
		return result, errBehaviorLedger
	}
	if intent.OwnerID != s.base.control.op.OwnerID || intent.PlanDigest != s.base.control.op.PlanDigest || intent.Recorded.After(now) {
		return result, errBehaviorLedgerBinding
	}
	result.intent = intent
	intentSHA := digestBytes(s.base.raw)
	seen := map[string]bool{}
	previousComplete := true
	parentUnconfirmed := false
	for i := 0; i < 8; i++ {
		var records [3]behaviorAction
		for j, present := range s.present[i] {
			if present {
				if i >= len(intent.Users) || exactJSON(s.actions[i][j], &records[j]) != nil || records[j].validate() != nil {
					return result, errBehaviorLedger
				}
				a := records[j]
				if a.OperationID != id || a.ConsentID != intent.ConsentID || a.IntentSHA != intentSHA || a.Index != i || a.UserID != intent.Users[i] || a.Observed.After(now) {
					return result, errBehaviorLedgerBinding
				}
			}
		}
		has := s.present[i]
		a, r, l := records[0], records[1], records[2]
		if !has[0] {
			if has[1] || has[2] {
				return result, errBehaviorLedgerBinding
			}
			previousComplete = false
			continue
		}
		if !previousComplete || a.Kind != "dispatch_reserved" || a.ParentSHA != intentSHA || a.Observed.Before(intent.Recorded) || !a.Observed.Before(intent.Expires) {
			return result, errBehaviorLedgerBinding
		}
		d.Dispatches++
		previousComplete = false
		if !has[1] {
			if has[2] {
				if (l.Kind != "logout_response_observed" && l.Kind != "logout_response_unknown") || l.Observed.Before(a.Observed) {
					return result, errBehaviorLedgerBinding
				}
				parentUnconfirmed = true
			}
			continue
		}
		if (r.Kind != "session_observed" && r.Kind != "login_response_unknown") || r.ParentSHA != digestBytes(s.actions[i][0]) || r.Observed.Before(a.Observed) {
			return result, errBehaviorLedgerBinding
		}
		if r.Kind == "login_response_unknown" {
			if has[2] {
				return result, errBehaviorLedgerBinding
			}
			continue
		}
		if seen[r.SessionID] {
			return result, errBehaviorLedgerBinding
		}
		seen[r.SessionID] = true
		result.known = append(result.known, behaviorSessionResponse{uuid.MustParse(r.UserID), uuid.MustParse(r.SessionID)})
		d.Known++
		if !has[2] {
			continue
		}
		if (l.Kind != "logout_response_observed" && l.Kind != "logout_response_unknown") || l.ParentSHA != digestBytes(s.actions[i][1]) || l.SessionID != r.SessionID || l.Observed.Before(r.Observed) {
			return result, errBehaviorLedgerBinding
		}
		if l.Kind == "logout_response_observed" {
			d.LogoutClaims++
			previousComplete = true
		}
	}
	d.Status = "outcome_unknown"
	d.Reason = "partial_or_unknown_responses"
	if parentUnconfirmed {
		d.Reason = "response_parent_unconfirmed"
	} else if d.Known == len(intent.Users) && d.LogoutClaims == len(intent.Users) {
		d.Status = "responses_observed"
		d.Reason = "response_claims_not_database_proof"
	}
	c := s.base.control
	if intent.Generation != c.op.Generation || [3]string{intent.OperationSHA, intent.AttemptSHA, intent.PublicationSHA} != c.tokens || c.op.CancelRequested || c.op.Phase != "validating" || c.attempt.DatabaseOutcome != "committed" || c.pub.State != "verified" || !now.Before(intent.Expires) {
		d.Status = "stale"
		d.Reason = "stale_records_outcome_not_inferred"
	}
	result.diagnostic = d
	return result, nil
}

// Two bounded reads; neither an atomic snapshot nor historical confirmation.
func stableBehaviorLedger(ctx context.Context, r RestoreRecordReader, id string, now time.Time) (behaviorLedgerSnapshot, behaviorLedgerAssessment, error) {
	var empty behaviorLedgerSnapshot
	a := behaviorLedgerAssessment{diagnostic: ledgerDiagnostic(id)}
	if ctx == nil || r == nil || !validUUID(id) || !validStamp(now) {
		return empty, a, errBehaviorLedger
	}
	x, e := readBehaviorLedger(ctx, r, id)
	if e != nil {
		return empty, a, e
	}
	y, e := readBehaviorLedger(ctx, r, id)
	if e != nil || ctx.Err() != nil {
		return empty, a, errBehaviorLedger
	}
	if !x.equal(y) {
		a.diagnostic.Status = "changed"
		a.diagnostic.Reason = "records_changed"
		return empty, a, errBehaviorLedger
	}
	a, e = inspectBehaviorLedger(y, id, now)
	if e != nil {
		a = behaviorLedgerAssessment{diagnostic: ledgerDiagnostic(id)}
		if errors.Is(e, errBehaviorLedgerBinding) {
			a.diagnostic.Status = "inconsistent"
			a.diagnostic.Reason = "chain_binding_mismatch"
		}
	}
	return y, a, e
}

func DiagnoseBehaviorLedger(ctx context.Context, r RestoreRecordReader, id string, now time.Time) BehaviorLedgerDiagnostic {
	if !validUUID(id) {
		d := ledgerDiagnostic("")
		d.Reason = "invalid_input"
		d.BoundReview = defaultBoundReviewDiagnostic("unavailable", "invalid_input")
		return d
	}
	if ctx == nil {
		return ledgerDiagnostic(id)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	snap, a, e := stableBehaviorLedger(ctx, r, id, now)
	attachBoundReviewToBehaviorDiagnostic(ctx, r, id, now, snap, a, e, &a.diagnostic)
	return a.diagnostic
}
