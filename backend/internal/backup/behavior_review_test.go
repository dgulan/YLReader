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

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"yreader/backend/internal/api"
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
	withReaderOnly := r
	withReaderOnly.Checks = append([]handoverReviewCheck(nil), r.Checks...)
	withReaderOnly.Checks[11].Status = "pass"
	withReaderOnlyData, _ := json.Marshal(withReaderOnly)
	if _, e := loadBoundBehaviorReview(bytes.NewReader(withReaderOnlyData)); e != nil {
		t.Fatal("valid 11/1 reader partial review rejected", e)
	}
	complete := r
	complete.Status = "complete"
	complete.Checks = append([]handoverReviewCheck(nil), r.Checks...)
	complete.Checks[10].Status = "pass"
	complete.Checks[11].Status = "pass"
	completeData, _ := json.Marshal(complete)
	if loaded, e := loadBoundBehaviorReview(bytes.NewReader(completeData)); e != nil || loaded.Status != "complete" || loaded.Execution || loaded.Ready || loaded.Deployment {
		t.Fatal("valid 12/0 complete review rejected", e)
	}
	if _, e := loadHandoverReview(bytes.NewReader(completeData)); e == nil {
		t.Fatal("12/0 bound profile upgraded old handover")
	}
	if _, e := json.Marshal(run); e == nil {
		t.Fatal("live witness serialized")
	}
	run.targetJWT = bytes.Repeat([]byte("K"), 32)
	if strings.Contains(fmt.Sprintf("%#v", *run), string(run.targetJWT)) {
		t.Fatal("live witness key formatting leaked")
	}
	clear(run.targetJWT)
	for _, name := range []string{"profile", "complete", "partial_all_pass", "missing_checks", "reader_fail", "jwt_fail", "execution", "ready", "deployment", "complete_ready", "counts", "expiry", "unknown", "duplicate", "null", "oversize"} {
		t.Run(name, func(t *testing.T) {
			bad := r
			bad.Checks = append([]handoverReviewCheck(nil), r.Checks...)
			raw := data
			switch name {
			case "profile":
				bad.Profile = "isolated-http-fixture-v1"
			case "complete":
				bad.Status = "complete"
			case "partial_all_pass":
				bad.Status = "partial"
				bad.Checks[10].Status = "pass"
				bad.Checks[11].Status = "pass"
			case "missing_checks":
				bad.Checks = bad.Checks[:11]
			case "reader_fail":
				bad.Checks[11].Status = "fail"
			case "jwt_fail":
				bad.Checks[10].Status = "fail"
			case "execution":
				bad.Execution = true
			case "ready":
				bad.Ready = true
			case "deployment":
				bad.Deployment = true
			case "complete_ready":
				bad = complete
				bad.Ready = true
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
				t.Fatal("invalid/unauthorized report accepted")
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

func TestReaderRoundtripConsentAndDispatchBoundary(t *testing.T) {
	t.Run("consent_reader_scope", func(t *testing.T) {
		a, input, control, p, now := consentFixture(t)
		a.ReaderScope = behaviorReaderScopeReadOnly
		input.data["consent.json"], _ = json.Marshal(a)
		prepared, e := prepareBehaviorInput(context.Background(), input, control, "consent.json", p, a.RuntimeSHA, func() time.Time { return now })
		if e != nil || !prepared.consent.allowsReaderRoundtrip() {
			t.Fatal("explicit reader consent rejected", e)
		}
		prepared.Close()

		for _, rawScope := range []string{`""`, `"write"`, `null`} {
			bad := strings.Replace(string(input.data["consent.json"]), `"`+behaviorReaderScopeReadOnly+`"`, rawScope, 1)
			input.data["consent.json"] = []byte(bad)
			if p2, e := prepareBehaviorInput(context.Background(), input, control, "consent.json", p, a.RuntimeSHA, func() time.Time { return now }); e != errBehaviorConsent || p2 != nil {
				t.Fatalf("invalid reader_scope %s accepted", rawScope)
			}
		}
	})

	for _, mode := range []string{"supported_ok", "reader_fail_still_logouts", "unsupported_transport_still_logouts"} {
		t.Run(mode, func(t *testing.T) {
			prepared, files, g, proof, baseTransport, p, clock := dispatchFixture(t)
			prepared.consent.ReaderScope = behaviorReaderScopeReadOnly

			var tr behaviorLoginTransport = baseTransport
			readerCalls := 0
			if mode != "unsupported_transport_still_logouts" {
				tr = &dispatchReaderFakeTransport{
					dispatchFakeTransport: baseTransport,
					onReader: func(ctx context.Context, consent behaviorConsent, gotProof *verifiedBehaviorAccounts, s behaviorSessionResponse) error {
						readerCalls++
						if consent.ReaderScope != behaviorReaderScopeReadOnly || gotProof != proof || s.UserID.String() != prepared.accounts[0].UserID {
							return errors.New("unexpected reader args")
						}
						if mode == "reader_fail_still_logouts" {
							return errors.New("SECRET reader failure")
						}
						return nil
					},
				}
			}
			obs, e := dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, tr, clock, func(context.Context) error { return nil })
			if mode == "supported_ok" {
				if e != nil || !obs.ResponsesObserved || obs.Stage != "completed" || readerCalls != 1 || baseTransport.logins != 1 || baseTransport.logouts != 1 {
					t.Fatalf("authorized reader dispatch failed: obs=%+v err=%v readerCalls=%d", obs, e, readerCalls)
				}
			} else {
				if e != errBehaviorDispatch || obs.ResponsesObserved || obs.Stage != "reader_roundtrip" || baseTransport.logins != 1 || baseTransport.logouts != 1 {
					t.Fatalf("mode %s did not logout cleanly or leaked error: obs=%+v err=%v logouts=%d", mode, obs, e, baseTransport.logouts)
				}
				d := DiagnoseBehaviorLedger(context.Background(), files, p.OperationID, clock())
				if d.LogoutClaims != 1 {
					t.Fatalf("logout action not persisted after reader failure: %+v", d)
				}
			}
		})
	}
}

type dispatchReaderFakeTransport struct {
	*dispatchFakeTransport
	onReader func(context.Context, behaviorConsent, *verifiedBehaviorAccounts, behaviorSessionResponse) error
}

func (f *dispatchReaderFakeTransport) readerRoundtrip(ctx context.Context, consent behaviorConsent, proof *verifiedBehaviorAccounts, s behaviorSessionResponse) error {
	return f.onReader(ctx, consent, proof, s)
}

func TestReaderRoundtripWitnessValidation(t *testing.T) {
	modes := []string{
		"confirmed", "not_authorized", "drifted", "missing_user", "partial_users",
		"unconfirmed", "session_mismatch", "same_book_and_other", "nil_book", "nil_other_book",
		"digest_mismatch", "empty_digest", "plan", "consent", "records",
		"database", "runtime", "certificate", "before_issued", "before_window",
		"after_now", "at_run_expires", "stale_observation",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			_, p, run, s, a, now := boundReviewFixture(t)
			uid := uuid.MustParse(a.intent.Users[0])
			sid := run.sessions[0].SessionID
			run.windowStart = a.intent.Issued
			base := readerRoundtripWitness{
				plan:         run.plan,
				consent:      run.consent,
				records:      run.records,
				database:     run.identity.Database,
				runtime:      run.identity.Runtime,
				certificate:  run.certificate,
				sessionID:    sid,
				bookID:       uuid.New(),
				otherBookID:  uuid.New(),
				observedAt:   a.intent.Issued,
				beforeDigest: strings.Repeat("a", 64),
				afterDigest:  strings.Repeat("a", 64),
				confirmed:    true,
			}
			run.reader = confirmedReaderSet{
				authorized: true,
				users:      map[uuid.UUID]readerRoundtripWitness{uid: base},
			}
			w := run.reader.users[uid]
			switch mode {
			case "not_authorized":
				run.reader.authorized = false
			case "drifted":
				run.reader.drifted = true
			case "missing_user":
				delete(run.reader.users, uid)
			case "partial_users":
				u2 := uuid.New()
				a.intent.Users = append(a.intent.Users, u2.String())
				run.sessions = append(run.sessions, behaviorSessionResponse{UserID: u2, SessionID: uuid.New()})
			case "unconfirmed":
				w.confirmed = false
				run.reader.users[uid] = w
			case "session_mismatch":
				w.sessionID = uuid.New()
				run.reader.users[uid] = w
			case "same_book_and_other":
				w.otherBookID = w.bookID
				run.reader.users[uid] = w
			case "nil_book":
				w.bookID = uuid.Nil
				run.reader.users[uid] = w
			case "nil_other_book":
				w.otherBookID = uuid.Nil
				run.reader.users[uid] = w
			case "digest_mismatch":
				w.afterDigest = strings.Repeat("b", 64)
				run.reader.users[uid] = w
			case "empty_digest":
				w.beforeDigest = ""
				w.afterDigest = ""
				run.reader.users[uid] = w
			case "plan":
				w.plan = strings.Repeat("f", 64)
				run.reader.users[uid] = w
			case "consent":
				w.consent = uuid.NewString()
				run.reader.users[uid] = w
			case "records":
				w.records[2] = strings.Repeat("f", 64)
				run.reader.users[uid] = w
			case "database":
				w.database = strings.Repeat("f", 64)
				run.reader.users[uid] = w
			case "runtime":
				w.runtime = strings.Repeat("f", 64)
				run.reader.users[uid] = w
			case "certificate":
				w.certificate = strings.Repeat("f", 64)
				run.reader.users[uid] = w
			case "before_issued":
				w.observedAt = a.intent.Issued.Add(-time.Second)
				run.reader.users[uid] = w
			case "before_window":
				run.windowStart = a.intent.Issued.Add(time.Millisecond)
			case "after_now":
				w.observedAt = now.Add(time.Second)
				run.reader.users[uid] = w
			case "at_run_expires":
				w.observedAt = run.expires
				run.reader.users[uid] = w
			case "stale_observation":
				now = a.intent.Issued.Add(6 * time.Minute)
				run.expires = now.Add(time.Minute)
			}
			got := readerRoundtripConfirmed(p, run, s, a, run.identity, now)
			if got != (mode == "confirmed") {
				t.Fatalf("mode %s got=%v want=%v", mode, got, mode == "confirmed")
			}
		})
	}
}

func TestReaderRoundtripTransportBounded(t *testing.T) {
	modes := []string{
		"good", "unauthorized_consent", "consent_mismatch", "consent_expired", "missing_session",
		"logged_out_session", "expired_proof", "closed_transport", "cancel",
		"pre_db_error", "pre_active_job", "pre_no_book", "pre_no_foreign_book",
		"books_500", "books_dup_key", "books_wrong_type", "books_missing",
		"chapters_404", "chapters_empty", "content_cached", "content_trace", "content_empty",
		"progress_mismatch", "bookmarks_mismatch", "cross_200", "cross_wrong_code",
		"compressed", "bad_close", "network", "redirect",
		"post_db_error", "post_digest_mutated", "post_active_job", "post_session_revoked",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			c := behaviorHTTPFixture(t)
			now := c.Clock()
			c.Clock = func() time.Time { return now }
			tr, e := newBehaviorHTTPTransport(c)
			if e != nil {
				t.Fatal(e)
			}
			defer tr.Close()
			u1 := c.Users[0]
			sid := uuid.New()
			bookID := uuid.New()
			otherBookID := uuid.New()
			bmID := uuid.New()
			note := "note 中文 😀"
			accessClaims := behaviorClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: u1.String(), IssuedAt: jwt.NewNumericDate(now.Add(-time.Second)), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}, TokenVersion: 1}
			access, e := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(c.JWT)
			if e != nil {
				t.Fatal(e)
			}
			g := &restoreGuard{locations: &Locations{}, plan: Plan{OperationID: uuid.NewString(), TargetID: c.Binding.TargetID, Digest: strings.Repeat("a", 64)}}
			consent := behaviorConsent{
				Format: "ylreader-behavior-consent", Version: 1, Scope: "isolated-account-verification-only",
				ID: uuid.NewString(), OperationID: g.plan.OperationID, OwnerID: uuid.NewString(),
				PlanDigest: g.plan.Digest, TargetID: c.Binding.TargetID, Generation: 1,
				OperationSHA: strings.Repeat("1", 64), AttemptSHA: strings.Repeat("2", 64), PublicationSHA: strings.Repeat("3", 64),
				RuntimeSHA: c.Binding.Runtime, ReaderScope: behaviorReaderScopeReadOnly,
				Issued: now.Add(-time.Second), Expires: now.Add(time.Minute),
				Accounts: []behaviorConsentAccount{{u1.String(), "account.json"}},
			}
			proof := &verifiedBehaviorAccounts{
				guard: g, plan: g.plan.Digest, consent: consent.ID, runtime: c.Binding.Runtime, database: c.Binding.Database,
				redis: strings.Repeat("r", 64), records: [3]string{consent.OperationSHA, consent.AttemptSHA, consent.PublicationSHA},
				users: []uuid.UUID{u1}, checked: now, expires: now.Add(time.Minute),
			}
			tr.secrets.known[sid] = behaviorHTTPKnown{user: u1, access: access, refresh: strings.Repeat("a", 64), version: 1, started: now}
			ctx := context.Background()
			switch mode {
			case "unauthorized_consent":
				consent.ReaderScope = ""
			case "consent_mismatch":
				consent.ID = uuid.NewString()
			case "consent_expired":
				consent.Expires = now
			case "missing_session":
				delete(tr.secrets.known, sid)
			case "logged_out_session":
				known := tr.secrets.known[sid]
				known.attempted = true
				tr.secrets.known[sid] = known
			case "expired_proof":
				proof.expires = now
			case "closed_transport":
				tr.Close()
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			dbCalls := 0
			tr.readerObserver = func(_ context.Context, gotUser, gotSess uuid.UUID) (readerDBSnapshot, error) {
				dbCalls++
				if gotUser != u1 || gotSess != sid {
					t.Fatal("observer args mismatch")
				}
				base := readerDBSnapshot{
					sessionActive: true, activeJobs: 0, bookID: bookID, chapterIndex: 0,
					otherBookID: otherBookID, otherChapterIndex: 0,
					progressChapter: 0, progressCharOffset: 17, progressVersion: 7,
					bookmarks: []readerExpectedBookmark{{id: bmID, chapterIndex: 0, charOffset: 3, note: &note}},
					digest:    strings.Repeat("d", 64),
				}
				if dbCalls == 1 {
					switch mode {
					case "pre_db_error":
						return readerDBSnapshot{}, errors.New("SECRET pre db")
					case "pre_active_job":
						base.activeJobs = 1
					case "pre_no_book":
						base.bookID = uuid.Nil
					case "pre_no_foreign_book":
						base.otherBookID = uuid.Nil
					}
					return base, nil
				}
				switch mode {
				case "post_db_error":
					return readerDBSnapshot{}, errors.New("SECRET post db")
				case "post_digest_mutated":
					base.digest = strings.Repeat("e", 64)
				case "post_active_job":
					base.activeJobs = 1
				case "post_session_revoked":
					base.sessionActive = false
				}
				return base, nil
			}
			httpCalls := 0
			tr.client.Transport = behaviorRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				httpCalls++
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+access || r.Header.Get("Cookie") != "" {
					t.Fatal("unsafe reader HTTP request")
				}
				if mode == "network" {
					return nil, errors.New("SECRET network")
				}
				status := http.StatusOK
				header := http.Header{"Content-Type": []string{"application/json"}}
				var body string
				switch r.URL.Path {
				case "/api/v1/books":
					if mode == "redirect" {
						status = http.StatusTemporaryRedirect
						header.Set("Location", "https://127.0.0.1:1")
					} else if mode == "books_500" {
						status = http.StatusInternalServerError
						body = `{}`
					} else if mode == "books_dup_key" {
						body = `{"page":1,"page":1,"page_size":20,"total":1,"items":[]}`
					} else {
						bt := api.BookBookType("local")
						bid := bookID
						if mode == "books_wrong_type" {
							bt = api.BookBookType("online")
						}
						if mode == "books_missing" {
							bid = uuid.New()
						}
						raw, _ := json.Marshal(api.BookPage{Page: 1, PageSize: 20, Total: 1, Items: []api.Book{{Id: bid, Name: "b", Author: "a", BookType: bt, Status: "unknown", CreatedAt: now, UpdatedAt: now, Version: 1}}})
						body = string(raw)
					}
					if mode == "compressed" {
						header.Set("Content-Encoding", "gzip")
					}
				case "/api/v1/books/" + bookID.String() + "/chapters":
					if mode == "chapters_404" {
						status = http.StatusNotFound
						body = `{"error":{"code":"not_found","message":"missing"}}`
					} else {
						idx := 0
						items := []api.Chapter{{Id: 1, ChapterIndex: &idx, Title: "c0", NormalizedTitle: "c0", SortOrder: 0, Url: "local://0"}}
						if mode == "chapters_empty" {
							items = nil
						}
						raw, _ := json.Marshal(api.ChapterList{BookId: bookID, Total: len(items), Items: items})
						body = string(raw)
					}
				case "/api/v1/books/" + bookID.String() + "/content":
					idx := 0
					cached := mode == "content_cached"
					var trace *string
					if mode == "content_trace" {
						trID := "tr_1"
						trace = &trID
					}
					text := "正文 中文 😀"
					wc := 7
					if mode == "content_empty" {
						text = "   "
						wc = 0
					}
					raw, _ := json.Marshal(api.ChapterContent{Chapter: api.Chapter{Id: 1, ChapterIndex: &idx, Title: "c0", NormalizedTitle: "c0", SortOrder: 0, Url: "local://0"}, Content: text, Cached: cached, TraceId: trace, WordCount: wc})
					body = string(raw)
				case "/api/v1/books/" + bookID.String() + "/progress":
					ver := 7
					if mode == "progress_mismatch" {
						ver = 8
					}
					raw, _ := json.Marshal(api.Progress{BookId: bookID, ChapterIndex: 0, CharOffset: 17, Version: ver})
					body = string(raw)
				case "/api/v1/books/" + bookID.String() + "/bookmarks":
					off := 3
					if mode == "bookmarks_mismatch" {
						off = 9
					}
					raw, _ := json.Marshal([]api.Bookmark{{Id: bmID, BookId: bookID, ChapterIndex: 0, CharOffset: off, Note: &note, CreatedAt: now}})
					body = string(raw)
				case "/api/v1/books/" + otherBookID.String() + "/content":
					status = http.StatusNotFound
					body = `{"error":{"code":"not_found","message":"Not found"}}`
					if mode == "cross_200" {
						status = http.StatusOK
						body = `{}`
					} else if mode == "cross_wrong_code" {
						body = `{"error":{"code":"forbidden","message":"Forbidden"}}`
					}
				default:
					t.Fatalf("unexpected GET path %s", r.URL.Path)
				}
				var rc io.ReadCloser = io.NopCloser(strings.NewReader(body))
				if mode == "bad_close" {
					rc = behaviorHTTPBadClose{strings.NewReader(body)}
				}
				return &http.Response{StatusCode: status, Header: header, Body: rc, Request: r}, nil
			})

			err := tr.readerRoundtrip(ctx, consent, proof, behaviorSessionResponse{UserID: u1, SessionID: sid})
			wantOK := mode == "good"
			if (err == nil) != wantOK {
				t.Fatalf("mode %s err=%v wantOK=%v", mode, err, wantOK)
			}
			if err != nil && err != errBehaviorHTTP {
				t.Fatalf("mode %s exposed non-sentinel error: %v", mode, err)
			}
			// Must never replay on same user after success or failure.
			callsAfter := httpCalls
			if tr.readerRoundtrip(ctx, consent, proof, behaviorSessionResponse{UserID: u1, SessionID: sid}) != errBehaviorHTTP || httpCalls != callsAfter {
				t.Fatalf("mode %s allowed readerRoundtrip replay", mode)
			}
			snap := tr.readerWitnessSnapshot(consent, c.JWT)
			if wantOK {
				if httpCalls != 6 || dbCalls != 2 || snap.drifted || len(snap.users) != 1 || !snap.users[u1].confirmed {
					t.Fatalf("good reader snapshot invalid: http=%d db=%d snap=%+v", httpCalls, dbCalls, snap)
				}
				badSnap := tr.readerWitnessSnapshot(consent, bytes.Repeat([]byte("z"), 32))
				if !badSnap.drifted {
					t.Fatal("target JWT drift not detected in readerWitnessSnapshot")
				}
				tr.Close()
				if len(tr.secrets.readerTried) != 0 || len(tr.secrets.readerWitness) != 0 {
					t.Fatal("Close retained reader secrets")
				}
			} else if len(snap.users) != 0 {
				t.Fatalf("mode %s recorded failed reader witness", mode)
			}
		})
	}
}
