//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"
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
	t.Run("bound_review_collect_partial", func(t *testing.T) {
		var e error
		report, e = g.collectBoundBehaviorReview(ctx, inputs, clock)
		if e != nil || report.validate() != nil || report.Status != "partial" || report.Profile != "bound-https-account-verification-v1" || report.AccountCount != 2 || report.SessionCount != 2 || report.Execution || report.Ready || report.Deployment {
			t.Fatal("guarded partial review failed", e)
		}
		if report.Checks[10].ID != "old_token_http_rejection" || report.Checks[10].Status != "pass" || report.Checks[11].ID != "reader_roundtrip" || report.Checks[11].Status != "unavailable" {
			t.Fatal("unexpected old-token or read check status", report.Checks[10], report.Checks[11])
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
}
