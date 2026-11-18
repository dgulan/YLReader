//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func assertBoundBehaviorReview(t *testing.T, g *restoreGuard, inputs handoverProbeInputs, clock func() time.Time, mode string) {
	t.Helper()
	ctx := context.Background()
	if mode != "good" {
		t.Run("bound_review_reject_unconfirmed", func(t *testing.T) {
			if r, e := g.collectBoundBehaviorReview(ctx, inputs, clock); e != errBehaviorReview || r.Format != "" || g.behaviorRun != nil {
				t.Fatal("unconfirmed HTTP/disk state became live proof")
			}
		})
		return
	}
	before, e := stableHandoverSnapshot(ctx, g.locations.files[3], g.plan.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	var report boundBehaviorReview
	originalTargetJWT := append([]byte(nil), inputs.TargetJWT...)
	defer clear(originalTargetJWT)
	t.Run("bound_review_wrong_jwt_material", func(t *testing.T) {
		bad := inputs
		bad.TargetJWT = make([]byte, 32)
		if r, e := g.collectBoundBehaviorReview(ctx, bad, clock); e != errBehaviorReview || r.Format != "" {
			t.Fatal("JWT material drift kept the same runtime identity")
		}
		badSource := inputs
		badSource.SourceJWT = make([]byte, 32)
		if r, e := g.collectBoundBehaviorReview(ctx, badSource, clock); e != errBehaviorReview || r.Format != "" {
			t.Fatal("source JWT material drift accepted")
		}
	})
	t.Run("bound_review_wrong_runtime", func(t *testing.T) {
		bad := inputs
		bad.ActualRuntime.ConfigRevision = uuid.NewString()
		if r, e := g.collectBoundBehaviorReview(ctx, bad, clock); e != errBehaviorReview || r.Format != "" {
			t.Fatal("wrong runtime became reviewed")
		}
	})
	t.Run("bound_review_collect_complete", func(t *testing.T) {
		var e error
		report, e = g.collectBoundBehaviorReview(ctx, inputs, clock)
		if e != nil || report.validate() != nil || report.Status != "complete" || report.Profile != "bound-https-account-verification-v1" || report.AccountCount != 2 || report.SessionCount != 2 || report.Execution || report.Ready || report.Deployment {
			t.Fatal("guarded complete review failed", e)
		}
		if report.Checks[10].ID != "old_token_http_rejection" || report.Checks[10].Status != "pass" || report.Checks[11].ID != "reader_roundtrip" || report.Checks[11].Status != "pass" {
			t.Fatal("unexpected old-token or reader check status", report.Checks[10], report.Checks[11])
		}
		if !bytes.Equal(inputs.TargetJWT, originalTargetJWT) || !bytes.Equal(g.behaviorRun.sourceJWT, make([]byte, len(g.behaviorRun.sourceJWT))) || !bytes.Equal(g.behaviorRun.targetJWT, make([]byte, len(g.behaviorRun.targetJWT))) {
			t.Fatal("witness secret ownership/erasure mismatch")
		}
	})
	if report.Format == "" {
		return
	}
	t.Run("bound_review_persist", func(t *testing.T) {
		if e := g.persistBoundBehaviorReview(ctx, report, clock); e != nil {
			t.Fatal(e)
		}
		key, _ := boundBehaviorReviewKey(g.plan.OperationID)
		raw, present, e := optionalStatusBytes(ctx, g.locations.files[3], key, 32768)
		encoded, _ := json.Marshal(report)
		if e != nil || !present || !bytes.Equal(raw, encoded) {
			t.Fatal("immutable report readback")
		}
		if _, e = loadBoundBehaviorReview(bytes.NewReader(raw)); e != nil {
			t.Fatal(e)
		}
		if _, e = loadHandoverReview(bytes.NewReader(raw)); e == nil {
			t.Fatal("new partial review became old full profile")
		}
	})
	t.Run("bound_review_no_replay", func(t *testing.T) {
		if g.persistBoundBehaviorReview(ctx, report, clock) != errBehaviorReview {
			t.Fatal("report write replayed")
		}
		if _, e := g.collectBoundBehaviorReview(ctx, inputs, clock); e != errBehaviorReview {
			t.Fatal("live review proof replaced")
		}
		after, e := stableHandoverSnapshot(ctx, g.locations.files[3], g.plan.OperationID)
		if e != nil || after.tokens != before.tokens || after.op.Phase != "validating" {
			t.Fatal("review changed operation/control records")
		}
	})
	encoded, _ := json.Marshal(report)
	t.Logf("BOUND_BEHAVIOR_REVIEW %s", encoded)
	if os.Getenv("BACKUP_HANDOVER_REGISTER") == "1" {
		var reg handoverRegistration
		t.Run("bound_review_registration", func(t *testing.T) {
			var e error
			reg, e = g.registerBoundReview(ctx, inputs, clock)
			if e != nil {
				t.Fatal("bound review registration failed", e)
			}
			if reg.Scope != "bound-behavior-review-registration" || reg.State != "registered" || reg.Ready || reg.Deployment || reg.BaseGeneration != report.Generation || reg.NextGeneration != report.Generation+1 {
				t.Fatalf("invalid bound registration chain: %+v", reg)
			}
			st := DiagnoseRestoreStatus(ctx, g.locations.files[3], g.plan.OperationID, clock())
			if st.Status != "observed_registered" || !st.BoundReview.Present || st.BoundReview.Status != "complete_registered" || st.BoundReview.CurrentGen != reg.NextGeneration || st.Ready || st.Deployment {
				t.Fatalf("unexpected status after bound registration: %+v", st)
			}
			bd := DiagnoseBehaviorLedger(ctx, g.locations.files[3], g.plan.OperationID, clock())
			if bd.Status != "stale" || bd.Reason != "stale_records_outcome_not_inferred" || !bd.BoundReview.Present || bd.BoundReview.Status != "complete_registered" || bd.BoundReview.ObservedGen != report.Generation || bd.BoundReview.CurrentGen != reg.NextGeneration || bd.BoundReview.Registration != "registered" || bd.BoundReview.RegistrationBind != "bound_next_observed" || bd.BoundReview.Persistence != "not_inferred" || bd.BoundReview.Execution || bd.BoundReview.Ready || bd.BoundReview.Deployment || bd.Dispatch || bd.Retry || bd.Deployment {
				t.Fatalf("unexpected behavior-status after bound registration: %+v", bd)
			}
			regBytes, _ := json.Marshal(reg)
			t.Logf("BOUND_REVIEW_REGISTRATION %s", regBytes)
		})
		t.Run("bound_review_registration_no_replay", func(t *testing.T) {
			if _, e := g.registerBoundReview(ctx, inputs, clock); e != errRegistrationUnknown {
				t.Fatal("bound registration replay accepted", e)
			}
			op, sha, e := (Control{g.locations.files[3]}).Load(ctx, g.plan.OperationID)
			if e != nil || op.Generation != reg.NextGeneration || op.Phase != "validating" || sha != reg.NextSHA {
				t.Fatal("bound registration replay mutated operation state")
			}
		})
	}
}
