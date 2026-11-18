package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	withOldTokens := r
	withOldTokens.Checks = append([]handoverReviewCheck(nil), r.Checks...)
	withOldTokens.Checks[10].Status = "pass"
	withOldTokensData, _ := json.Marshal(withOldTokens)
	if _, e := loadBoundBehaviorReview(bytes.NewReader(withOldTokensData)); e != nil {
		t.Fatal("valid 11/1 partial review rejected", e)
	}
	if _, e := loadHandoverReview(bytes.NewReader(withOldTokensData)); e == nil {
		t.Fatal("11/1 partial profile upgraded old handover")
	}
	if _, e := json.Marshal(run); e == nil {
		t.Fatal("live witness serialized")
	}
	run.targetJWT = bytes.Repeat([]byte("K"), 32)
	if strings.Contains(fmt.Sprintf("%#v", *run), string(run.targetJWT)) {
		t.Fatal("live witness key formatting leaked")
	}
	clear(run.targetJWT)
	for _, name := range []string{"profile", "complete", "missing_checks", "reader_pass", "jwt_fail", "execution", "ready", "deployment", "counts", "expiry", "unknown", "duplicate", "null", "oversize"} {
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
			case "jwt_fail":
				bad.Checks[10].Status = "fail"
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

func TestOldTokenRejectionWitnessValidation(t *testing.T) {
	modes := []string{
		"confirmed", "drifted", "missing_access", "missing_refresh", "partial_users",
		"unconfirmed_access", "unconfirmed_refresh", "plan", "consent", "records",
		"database", "runtime", "certificate", "before_issued", "after_now",
		"at_run_expires", "stale_observation", "token_expired",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			_, p, run, s, a, now := boundReviewFixture(t)
			uid := uuid.MustParse(a.intent.Users[0])
			baseWitness := oldTokenProbeWitness{
				plan:         run.plan,
				consent:      run.consent,
				records:      run.records,
				database:     run.identity.Database,
				runtime:      run.identity.Runtime,
				certificate:  run.certificate,
				observedAt:   a.intent.Issued,
				tokenExpires: now.Add(time.Minute),
				confirmed:    true,
			}
			run.windowStart = a.intent.Issued
			run.oldTokens = confirmedOldTokenSet{
				access:  map[uuid.UUID]oldTokenProbeWitness{uid: baseWitness},
				refresh: map[uuid.UUID]oldTokenProbeWitness{uid: baseWitness},
			}
			acc := run.oldTokens.access[uid]
			ref := run.oldTokens.refresh[uid]
			switch mode {
			case "drifted":
				run.oldTokens.drifted = true
			case "missing_access":
				delete(run.oldTokens.access, uid)
			case "missing_refresh":
				delete(run.oldTokens.refresh, uid)
			case "partial_users":
				u2 := uuid.New()
				a.intent.Users = append(a.intent.Users, u2.String())
				run.sessions = append(run.sessions, behaviorSessionResponse{UserID: u2, SessionID: uuid.New()})
			case "unconfirmed_access":
				acc.confirmed = false
				run.oldTokens.access[uid] = acc
			case "unconfirmed_refresh":
				ref.confirmed = false
				run.oldTokens.refresh[uid] = ref
			case "plan":
				acc.plan = strings.Repeat("f", 64)
				run.oldTokens.access[uid] = acc
			case "consent":
				ref.consent = uuid.NewString()
				run.oldTokens.refresh[uid] = ref
			case "records":
				acc.records[1] = strings.Repeat("f", 64)
				run.oldTokens.access[uid] = acc
			case "database":
				ref.database = strings.Repeat("f", 64)
				run.oldTokens.refresh[uid] = ref
			case "runtime":
				acc.runtime = strings.Repeat("f", 64)
				run.oldTokens.access[uid] = acc
			case "certificate":
				ref.certificate = strings.Repeat("f", 64)
				run.oldTokens.refresh[uid] = ref
			case "before_issued":
				acc.observedAt = a.intent.Issued.Add(-time.Second)
				run.oldTokens.access[uid] = acc
			case "after_now":
				ref.observedAt = now.Add(time.Second)
				run.oldTokens.refresh[uid] = ref
			case "at_run_expires":
				acc.observedAt = run.expires
				run.oldTokens.access[uid] = acc
			case "stale_observation":
				now = a.intent.Issued.Add(6 * time.Minute)
				run.expires = now.Add(time.Minute)
				acc.tokenExpires = now.Add(time.Minute)
				ref.tokenExpires = now.Add(time.Minute)
				run.oldTokens.access[uid] = acc
				run.oldTokens.refresh[uid] = ref
			case "token_expired":
				acc.tokenExpires = now
				run.oldTokens.access[uid] = acc
			}
			got := oldTokenRejectionConfirmed(p, run, s, a, run.identity, now)
			if got != (mode == "confirmed") {
				t.Fatalf("mode %s got=%v, want=%v", mode, got, mode == "confirmed")
			}
		})
	}
}

func TestOldTokenTransportWitnessCaptureAndAttach(t *testing.T) {
	c := behaviorHTTPFixture(t)
	now := c.Clock()
	c.Clock = func() time.Time { return now }
	u1 := c.Users[0]
	u2 := uuid.New()
	c.Users = []uuid.UUID{u1, u2}
	tr, e := newBehaviorHTTPTransport(c)
	if e != nil {
		t.Fatal(e)
	}
	defer tr.Close()
	files, p, _ := ledgerFixture(t)
	p.TargetID = c.Binding.TargetID
	s, a, e := stableBehaviorLedger(context.Background(), files, p.OperationID, now)
	if e != nil {
		t.Fatal(e)
	}
	a.intent.Users = []string{u1.String(), u2.String()}
	a.intent.Issued = now.Add(-time.Second)
	a.intent.Expires = now.Add(2 * time.Minute)
	g := &restoreGuard{locations: &Locations{}, plan: p}
	proof := &verifiedBehaviorAccounts{guard: g, plan: p.Digest, consent: a.intent.ConsentID, runtime: c.Binding.Runtime, database: c.Binding.Database, redis: strings.Repeat("r", 64), records: s.base.control.tokens, users: []uuid.UUID{u1, u2}, checked: now, expires: now.Add(2 * time.Minute)}
	sourceKey := bytes.Repeat([]byte("s"), 32)
	otherSourceKey := bytes.Repeat([]byte("o"), 32)
	acc1 := oldAccessFixture(t, c, sourceKey, u1, now.Add(time.Minute))
	acc2 := oldAccessFixture(t, c, sourceKey, u2, now.Add(time.Minute))
	ref1 := strings.Repeat("b", 64)
	ref2 := strings.Repeat("c", 64)
	statusCode := http.StatusUnauthorized
	tr.client.Transport = behaviorRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"error":{"code":"unauthorized","message":"Unauthorized"}}`
		return &http.Response{StatusCode: statusCode, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	tr.oldRefreshObserver = func(_ context.Context, _ uuid.UUID, _ string) (oldRefreshSessionSnapshot, error) {
		return oldRefreshSessionSnapshot{total: 2, revoked: 2, activeForUser: 0, targetFound: true, targetRevoked: true, targetExpires: now.Add(time.Minute), digest: strings.Repeat("1", 64)}, nil
	}

	// Reject witness recording when sourceKey drifts across users or probe returns non-401.
	statusCode = http.StatusForbidden
	if tr.confirmOldAccess(context.Background(), proof, u1, 2, acc1, sourceKey) != errBehaviorHTTP {
		t.Fatal("403 accepted by confirmOldAccess")
	}
	snapFail := tr.oldTokenWitnessSnapshot(sourceKey, c.JWT)
	if len(snapFail.access) != 0 {
		t.Fatal("failed access probe recorded witness")
	}

	// Successful pre-capture for u2 + pre-login refresh for u1.
	statusCode = http.StatusUnauthorized
	if tr.confirmOldAccess(context.Background(), proof, u2, 2, acc2, sourceKey) != nil {
		t.Fatal("valid confirmOldAccess failed")
	}
	// Drifted sourceKey must be rejected before probing.
	if tr.confirmOldAccess(context.Background(), proof, u1, 2, oldAccessFixture(t, c, otherSourceKey, u1, now.Add(time.Minute)), otherSourceKey) != errBehaviorHTTP {
		t.Fatal("drifted source key accepted")
	}
	if tr.rejectOldRefresh(context.Background(), proof, u1, ref1) != nil {
		t.Fatal("valid pre-login rejectOldRefresh failed")
	}

	// Capture into confirmedBehaviorRun (with sourceKey drift check vs matching sourceKey).
	driftedSnap := tr.oldTokenWitnessSnapshot(otherSourceKey, c.JWT)
	if !driftedSnap.drifted {
		t.Fatal("source JWT drift not detected on witness snapshot")
	}
	run := &confirmedBehaviorRun{
		plan: p.Digest, consent: a.intent.ConsentID, ledger: behaviorLedgerDigest(s),
		certificate: tr.certificateSHA, identity: handoverTargetIdentity{c.Binding.Database, c.Binding.Runtime, strings.Repeat("7", 64)},
		records: s.base.control.tokens, sessions: []behaviorSessionResponse{{UserID: u1, SessionID: uuid.New()}, {UserID: u2, SessionID: uuid.New()}},
		windowStart: a.intent.Issued, started: now, finished: now, expires: now.Add(2 * time.Minute),
		sourceJWT: append([]byte(nil), sourceKey...), targetJWT: append([]byte(nil), c.JWT...),
		oldTokens: tr.oldTokenWitnessSnapshot(sourceKey, c.JWT),
	}
	g.behaviorRun = run
	if oldTokenRejectionConfirmed(p, run, s, a, run.identity, now) {
		t.Fatal("incomplete dual witness promoted")
	}

	// Post-capture attachment for remaining refresh (u2); simulate missing access for u1 via direct witness attachment.
	if tr.rejectOldRefresh(context.Background(), proof, u2, ref2) != nil {
		t.Fatal("valid post-capture rejectOldRefresh failed")
	}
	if oldTokenRejectionConfirmed(p, run, s, a, run.identity, now) {
		t.Fatal("missing u1 access promoted")
	}
	w1 := oldTokenProbeWitness{plan: p.Digest, consent: a.intent.ConsentID, records: s.base.control.tokens, database: c.Binding.Database, runtime: c.Binding.Runtime, certificate: tr.certificateSHA, observedAt: now, tokenExpires: now.Add(time.Minute), confirmed: true}
	g.attachOldTokenWitness(u1, true, w1, c.JWT, sourceKey)
	if !oldTokenRejectionConfirmed(p, run, s, a, run.identity, now) {
		t.Fatal("complete dual witness not promoted")
	}

	// Drift during late attachment permanently blocks promotion; Close erases transport secrets.
	g.attachOldTokenWitness(u1, true, w1, c.JWT, otherSourceKey)
	if oldTokenRejectionConfirmed(p, run, s, a, run.identity, now) {
		t.Fatal("source drift after attachment did not block promotion")
	}
	tr.Close()
	if len(tr.secrets.sourceKey) != 0 || len(tr.secrets.oldAccessWitness) != 0 || len(tr.secrets.oldRefreshWitness) != 0 {
		t.Fatal("Close retained old-token witness secrets")
	}
}
