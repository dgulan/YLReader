package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/storage"
)

func boundReviewFixture(t *testing.T) (boundBehaviorReview, Plan, *confirmedBehaviorRun, behaviorLedgerSnapshot, behaviorLedgerAssessment, time.Time) {
	t.Helper()
	files, p, clock := ledgerFixture(t)
	s, a, e := stableBehaviorLedger(context.Background(), files, p.OperationID, clock())
	if e != nil {
		t.Fatal(e)
	}
	run := &confirmedBehaviorRun{plan: p.Digest, consent: a.intent.ConsentID, ledger: behaviorLedgerDigest(s), certificate: strings.Repeat("8", 64), identity: handoverTargetIdentity{strings.Repeat("5", 64), a.intent.RuntimeSHA, strings.Repeat("7", 64)}, records: s.base.control.tokens, sessions: append([]behaviorSessionResponse(nil), a.known...), started: a.intent.Recorded, finished: clock(), expires: a.intent.Expires}
	now := clock().Add(time.Second)
	r := boundBehaviorReview{Format: "ylreader-bound-behavior-review", Version: 1, Profile: "bound-https-account-verification-v1", Scope: "localfs-bound-https-account-review", Status: "partial", OperationID: p.OperationID, BackupID: p.BackupID, ConsentID: run.consent, PlanDigest: p.Digest, PackageSHA: p.PackageSHA, ManifestSHA: p.ManifestSHA, TargetID: p.TargetID, Storage: p.TargetFingerprint, Tree: s.base.control.pub.TreeFingerprint, Generation: s.base.control.op.Generation, OperationSHA: run.records[0], AttemptSHA: run.records[1], PublicationSHA: run.records[2], Identity: run.identity, Certificate: run.certificate, Ledger: run.ledger, AccountCount: 1, SessionCount: 1, Started: run.started, Finished: now, Expires: run.expires}
	for i, id := range boundBehaviorCheckIDs() {
		status := "pass"
		if i >= 10 {
			status = "unavailable"
		}
		r.Checks = append(r.Checks, handoverReviewCheck{id, status})
	}
	return r, p, run, s, a, now
}
func TestBoundBehaviorReviewStrictPartialProfile(t *testing.T) {
	r, _, run, _, _, _ := boundReviewFixture(t)
	data, _ := json.Marshal(r)
	if _, e := loadBoundBehaviorReview(bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	if _, e := loadHandoverReview(bytes.NewReader(data)); e == nil {
		t.Fatal("new partial profile upgraded old handover")
	}
	if _, e := json.Marshal(run); e == nil {
		t.Fatal("live witness serialized")
	}
	run.targetJWT = bytes.Repeat([]byte("K"), 32)
	if strings.Contains(fmt.Sprintf("%#v", *run), string(run.targetJWT)) {
		t.Fatal("live witness key formatting leaked")
	}
	clear(run.targetJWT)
	for _, name := range []string{"profile", "complete", "missing_checks", "reader_pass", "jwt_pass", "execution", "ready", "deployment", "counts", "expiry", "unknown", "duplicate", "null", "oversize"} {
		t.Run(name, func(t *testing.T) {
			bad := r
			bad.Checks = append([]handoverReviewCheck(nil), r.Checks...)
			raw := data
			switch name {
			case "profile":
				bad.Profile = "isolated-http-fixture-v1"
			case "complete":
				bad.Status = "complete"
			case "missing_checks":
				bad.Checks = bad.Checks[:11]
			case "reader_pass":
				bad.Checks[11].Status = "pass"
			case "jwt_pass":
				bad.Checks[10].Status = "pass"
			case "execution":
				bad.Execution = true
			case "ready":
				bad.Ready = true
			case "deployment":
				bad.Deployment = true
			case "counts":
				bad.SessionCount = 2
			case "expiry":
				bad.Expires = bad.Finished
			case "unknown":
				raw = append([]byte(`{"SECRET":1,`), raw[1:]...)
			case "duplicate":
				raw = append([]byte(`{"report_version":1,`), raw[1:]...)
			case "null":
				raw = []byte("null")
			case "oversize":
				raw = append(raw, bytes.Repeat([]byte(" "), 32769)...)
			}
			if name != "unknown" && name != "duplicate" && name != "null" && name != "oversize" {
				raw, _ = json.Marshal(bad)
			}
			if _, e := loadBoundBehaviorReview(bytes.NewReader(raw)); e != errBehaviorReview {
				t.Fatal("invalid/full-pass report accepted")
			}
		})
	}
}
func TestConfirmedBehaviorRunBindings(t *testing.T) {
	for _, name := range []string{"valid", "absent", "plan", "consent", "ledger", "records", "runtime", "certificate", "session", "user", "expired", "early", "partial"} {
		t.Run(name, func(t *testing.T) {
			_, p, run, s, a, now := boundReviewFixture(t)
			switch name {
			case "absent":
				run = nil
			case "plan":
				run.plan = strings.Repeat("f", 64)
			case "consent":
				run.consent = uuid.NewString()
			case "ledger":
				run.ledger = strings.Repeat("f", 64)
			case "records":
				run.records[0] = strings.Repeat("f", 64)
			case "runtime":
				run.identity.Runtime = strings.Repeat("f", 64)
			case "certificate":
				run.certificate = "SECRET"
			case "session":
				run.sessions[0].SessionID = uuid.New()
			case "user":
				run.sessions[0].UserID = uuid.New()
			case "expired":
				now = run.expires
			case "early":
				now = run.finished.Add(-time.Second)
			case "partial":
				a.diagnostic.Status = "outcome_unknown"
			}
			e := checkConfirmedBehaviorRun(p, run, s, a, now)
			if (e == nil) != (name == "valid") {
				t.Fatal("run binding mismatch accepted")
			}
		})
	}
	r, _, _, _, _, now := boundReviewFixture(t)
	var g *restoreGuard
	if g.persistBoundBehaviorReview(context.Background(), r, func() time.Time { return now }) != errBehaviorReview {
		t.Fatal("nil guard persisted review")
	}
}

type boundReviewFaultFiles struct {
	storage.ConditionalFiles
	mode    string
	written bool
	cancel  context.CancelFunc
}

func (f *boundReviewFaultFiles) CreateOnly(ctx context.Context, key string, r io.Reader, n int64) error {
	if f.mode == "write_fail" {
		return errors.New("SECRET write failure")
	}
	if e := f.ConditionalFiles.CreateOnly(ctx, key, r, n); e != nil {
		return e
	}
	f.written = true
	if f.mode == "ack_lost" {
		return storage.ErrCommitUnknown
	}
	if f.mode == "cancel" {
		f.cancel()
	}
	return nil
}
func (f *boundReviewFaultFiles) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if f.written && f.mode == "readback" {
		return nil, errors.New("SECRET readback")
	}
	return f.ConditionalFiles.Get(ctx, key)
}
func TestBoundBehaviorReviewImmutableSaveFaults(t *testing.T) {
	for _, mode := range []string{"success", "write_fail", "ack_lost", "readback", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _, _, _, _ := boundReviewFixture(t)
			base := &memoryFiles{data: map[string][]byte{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			files := &boundReviewFaultFiles{ConditionalFiles: base, mode: mode, cancel: cancel}
			e := saveBoundBehaviorReview(ctx, files, r)
			if (e == nil) != (mode == "success") {
				t.Fatal("uncertain save reported success")
			}
			key, _ := boundBehaviorReviewKey(r.OperationID)
			if mode != "write_fail" {
				stored, ok := base.data[key]
				if !ok {
					t.Fatal("unconfirmed report removed")
				}
				if _, e := loadBoundBehaviorReview(bytes.NewReader(stored)); e != nil {
					t.Fatal(e)
				}
				before := digestBytes(stored)
				if saveBoundBehaviorReview(context.Background(), base, r) != errBehaviorReview || digestBytes(base.data[key]) != before {
					t.Fatal("report overwritten or replayed")
				}
			}
		})
	}
}
