package backup

import (
	"context"
	"time"
)

type boundBehaviorStatusRead struct {
	control diagnosticSnapshot
	bound   optionalBoundReviewRead
	reg     handoverRegistration
	hasReg  bool
	tokens  [3]string
}

func readBoundBehaviorStatusSnapshot(ctx context.Context, r RestoreRecordReader, id string) (boundBehaviorStatusRead, error) {
	var out boundBehaviorStatusRead
	ctrl, e := readDiagnosticSnapshot(ctx, diagnosticFiles{r}, id)
	if e != nil {
		return out, e
	}
	out.control = ctrl
	bound, e := readOptionalBoundReview(ctx, r, id)
	if e != nil {
		return out, e
	}
	out.bound = bound
	out.tokens[0] = bound.sha
	regKey, _ := registrationKey(id)
	regData, regExists, e := optionalStatusBytes(ctx, r, regKey, 8192)
	if e != nil {
		return out, e
	}
	out.hasReg = regExists
	if regExists {
		if exactJSON(regData, &out.reg) != nil || out.reg.validate() != nil || out.reg.OperationID != id {
			return out, ErrState
		}
		out.tokens[1] = digestBytes(regData)
	}
	return out, ctx.Err()
}

func attachBoundReviewToBehaviorDiagnostic(
	ctx context.Context,
	r RestoreRecordReader,
	id string,
	now time.Time,
	snap behaviorLedgerSnapshot,
	assessed behaviorLedgerAssessment,
	ledgerErr error,
	out *BehaviorLedgerDiagnostic,
) {
	if out == nil {
		return
	}
	if out.Status == "changed" {
		out.BoundReview = defaultBoundReviewDiagnostic("changed", "records_changed")
		return
	}
	if r == nil || ctx == nil || ctx.Err() != nil || !validUUID(id) || !validStamp(now) {
		out.BoundReview = defaultBoundReviewDiagnostic("unavailable", "records_unavailable")
		return
	}
	if ledgerErr != nil && ledgerErr != errBehaviorLedgerBinding {
		out.BoundReview = defaultBoundReviewDiagnostic("unavailable", "records_unavailable")
		return
	}
	first, e := readBoundBehaviorStatusSnapshot(ctx, r, id)
	if e != nil {
		out.BoundReview = defaultBoundReviewDiagnostic("unavailable", "bound_review_unavailable")
		return
	}
	second, e := readBoundBehaviorStatusSnapshot(ctx, r, id)
	if e != nil {
		out.BoundReview = defaultBoundReviewDiagnostic("unavailable", "bound_review_unavailable")
		return
	}
	if first.control.tokens != second.control.tokens || first.tokens != second.tokens || first.bound.present != second.bound.present || first.hasReg != second.hasReg || (ledgerErr == nil && second.control.tokens != snap.base.control.tokens) {
		out.Status = "changed"
		out.Reason = "records_changed"
		out.BoundReview = defaultBoundReviewDiagnostic("changed", "records_changed")
		return
	}
	if !coherentBehaviorControl(second.control, id) {
		out.BoundReview = defaultBoundReviewDiagnostic("inconsistent", "control_binding_mismatch")
		if second.bound.present {
			out.BoundReview.Present = true
		}
		return
	}
	ledgerCheck := func(b boundBehaviorReview) (bool, string) {
		if ledgerErr != nil || !snap.base.present {
			return false, "ledger_binding_mismatch"
		}
		if b.ConsentID != assessed.intent.ConsentID || b.AccountCount != len(assessed.intent.Users) || b.Identity.Runtime != assessed.intent.RuntimeSHA {
			return false, "ledger_binding_mismatch"
		}
		ledgerSnap := snap
		c := second.control
		registeredNext := second.hasReg && second.reg.Scope == "bound-behavior-review-registration" &&
			c.tokens[0] == second.reg.NextSHA && c.op.Generation == second.reg.NextGeneration
		if registeredNext {
			if assessed.intent.Generation != second.reg.BaseGeneration ||
				assessed.intent.OperationSHA != second.reg.BaseSHA ||
				assessed.intent.AttemptSHA != c.tokens[1] ||
				assessed.intent.PublicationSHA != c.tokens[2] ||
				assessed.intent.OwnerID != c.op.OwnerID ||
				assessed.intent.PlanDigest != c.op.PlanDigest {
				return false, "ledger_binding_mismatch"
			}
			ledgerSnap.base.control.tokens[0] = second.reg.BaseSHA
		}
		if b.Ledger != behaviorLedgerDigest(ledgerSnap) {
			return false, "ledger_binding_mismatch"
		}
		completeClaims := assessed.diagnostic.Known == len(assessed.intent.Users) &&
			assessed.diagnostic.LogoutClaims == len(assessed.intent.Users)
		if !now.Before(b.Expires) && (registeredNext || assessed.diagnostic.Status == "stale") {
			if !completeClaims {
				return false, "ledger_not_responses_observed"
			}
			return true, ""
		}
		if registeredNext {
			if c.op.CancelRequested || c.op.Phase != "validating" ||
				c.attempt.DatabaseOutcome != "committed" || c.pub.State != "verified" ||
				!now.Before(assessed.intent.Expires) || !completeClaims {
				return false, "ledger_not_responses_observed"
			}
			return true, ""
		}
		if assessed.diagnostic.Status != "responses_observed" {
			return false, "ledger_not_responses_observed"
		}
		return true, ""
	}
	out.BoundReview = evaluateBoundReview(second.control, second.bound, second.hasReg, second.reg, now, ledgerCheck)
}
