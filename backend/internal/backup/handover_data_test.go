package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHandoverDataProofCollection(t *testing.T) {
	f, p, probes, clock := collectionFixture(t)
	probes.Data = func(_ context.Context, s diagnosticSnapshot) (handoverDataProof, error) {
		return handoverDataProof{p.Digest, s.tokens, s.pub.TreeFingerprint}, nil
	}
	r, e := collectHandoverReview(context.Background(), f, p, probes, clock)
	if e != nil {
		t.Fatal(e)
	}
	pass, missing := 0, 0
	for _, check := range r.Checks {
		if check.Status == "pass" {
			pass++
		}
		if check.Status == "unavailable" {
			missing++
			if check.ID != "jwt_rotation" && check.ID != "runtime_behavior" {
				t.Fatal("wrong missing check")
			}
		}
	}
	s, _ := stableHandoverSnapshot(context.Background(), f, p.OperationID)
	if pass != 8 || missing != 2 || r.validateFor(p, s, r.TargetIdentity, clock()) == nil || r.ReadyForCutover {
		t.Fatal("data proof overstated readiness")
	}
}

func TestHandoverDataProofFailures(t *testing.T) {
	for _, name := range []string{"error", "plan", "records", "tree", "changed_after"} {
		t.Run(name, func(t *testing.T) {
			f, p, probes, clock := collectionFixture(t)
			probes.Data = func(_ context.Context, s diagnosticSnapshot) (handoverDataProof, error) {
				proof := handoverDataProof{p.Digest, s.tokens, s.pub.TreeFingerprint}
				switch name {
				case "error":
					return handoverDataProof{}, errors.New("SECRET path")
				case "plan":
					proof.plan = strings.Repeat("9", 64)
				case "records":
					proof.records[0] = strings.Repeat("9", 64)
				case "tree":
					proof.tree = strings.Repeat("9", 64)
				case "changed_after":
					key := "operations/" + p.OperationID + "/state.json"
					f.data[key] = append(f.data[key], ' ')
				}
				return proof, nil
			}
			if r, e := collectHandoverReview(context.Background(), f, p, probes, clock); e != errHandoverReview || r.Format != "" {
				t.Fatal("invalid proof returned report")
			}
		})
	}
}

func TestHandoverDataRejectsAbsentLiveGuard(t *testing.T) {
	for _, g := range []*restoreGuard{nil, {}} {
		if proof, e := g.probeHandoverData(context.Background(), nil, "", "", time.Now().UTC(), nil, nil, diagnosticSnapshot{}); e != errHandoverReview || proof.plan != "" {
			t.Fatal("absent live proof accepted")
		}
	}
}
