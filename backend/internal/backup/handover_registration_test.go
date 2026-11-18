package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func registrationFixture(t *testing.T, files storage.ConditionalFiles) (Plan, handoverReview, func() time.Time) {
	t.Helper()
	r, p, s, now := handoverFixture(t)
	f := &diagnosticReader{data: map[string][]byte{}}
	writeDiagnosticFixture(t, f, s.op, s.attempt, s.pub)
	for k, data := range f.data {
		if e := files.CreateOnly(context.Background(), k, bytes.NewReader(data), int64(len(data))); e != nil {
			t.Fatal(e)
		}
	}
	if e := (handoverReviewStore{files}).Create(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	return p, r, func() time.Time { return now }
}

type registrationFaultFiles struct {
	storage.ConditionalFiles
	mode         string
	stateWritten bool
	stateCAS     int
}

func (f *registrationFaultFiles) CreateOnly(ctx context.Context, key string, r io.Reader, n int64) error {
	if e := f.ConditionalFiles.CreateOnly(ctx, key, r, n); e != nil {
		return e
	}
	if f.mode == "prepared_ack" && strings.HasSuffix(key, "handover-registration.json") {
		return storage.ErrCommitUnknown
	}
	return nil
}
func (f *registrationFaultFiles) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if f.mode == "readback" && f.stateWritten && strings.HasSuffix(key, "handover-review.json") {
		return nil, errors.New("SECRET private path")
	}
	return f.ConditionalFiles.Get(ctx, key)
}
func (f *registrationFaultFiles) CompareAndSwap(ctx context.Context, key, token string, data []byte) error {
	state := strings.HasSuffix(key, "/state.json")
	if state {
		f.stateCAS++
	}
	if !state && f.mode == "receipt_conflict" {
		return storage.ErrConflict
	}
	if state && f.mode == "conflict" {
		var competing Operation
		if json.Unmarshal(data, &competing) != nil {
			return ErrState
		}
		competing.UpdatedAt = competing.UpdatedAt.Add(time.Microsecond)
		competing.HeartbeatAt = competing.UpdatedAt
		changed, _ := json.Marshal(competing)
		if e := f.ConditionalFiles.CompareAndSwap(ctx, key, token, changed); e != nil {
			return e
		}
	}
	if e := f.ConditionalFiles.CompareAndSwap(ctx, key, token, data); e != nil {
		return e
	}
	if state {
		f.stateWritten = true
	}
	if (state && f.mode == "state_ack") || (!state && f.mode == "receipt_ack") {
		return storage.ErrCommitUnknown
	}
	return nil
}

func TestRegistrationVersionChainAndNoPhaseAdvance(t *testing.T) {
	f := &memoryFiles{data: map[string][]byte{}}
	p, r, clock := registrationFixture(t, f)
	before, _ := stableHandoverSnapshot(context.Background(), f, p.OperationID)
	reg, e := registerIsolatedReview(context.Background(), f, p, r, clock)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := stableHandoverSnapshot(context.Background(), f, p.OperationID)
	if reg.State != "registered" || reg.Ready || reg.Deployment || reg.BaseSHA != before.tokens[0] || reg.NextSHA != after.tokens[0] || after.op.Generation != before.op.Generation+1 || after.op.Phase != "validating" || after.tokens[1] != before.tokens[1] || after.tokens[2] != before.tokens[2] {
		t.Fatal("registration chain incorrect")
	}
	if r.validateFor(p, after, r.TargetIdentity, clock()) == nil {
		t.Fatal("old report pretended to bind next generation")
	}
	_, sha, e := (handoverReviewStore{f}).Load(context.Background(), p.OperationID)
	if e != nil || sha != reg.ReportSHA {
		t.Fatal("report altered")
	}
	if _, e = registerIsolatedReview(context.Background(), f, p, r, clock); e == nil {
		t.Fatal("registration replayed")
	}
}

func TestRegistrationFaultRetention(t *testing.T) {
	for _, mode := range []string{"prepared_ack", "state_ack", "receipt_ack", "receipt_conflict", "readback", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := &memoryFiles{data: map[string][]byte{}}
			p, r, clock := registrationFixture(t, f)
			fault := &registrationFaultFiles{ConditionalFiles: f, mode: mode}
			if _, e := registerIsolatedReview(context.Background(), fault, p, r, clock); (mode == "conflict" && !errors.Is(e, storage.ErrConflict)) || (mode != "conflict" && e != errRegistrationUnknown) {
				t.Fatal("confirmation loss misclassified", e)
			}
			if len(f.data) != 5 {
				t.Fatal("uncertain artifacts deleted")
			}
			reg, _, e := (handoverRegistrationStore{f}).Load(context.Background(), p.OperationID)
			if e != nil {
				t.Fatal(e)
			}
			want := "prepared"
			if mode == "receipt_ack" {
				want = "registered"
			}
			if reg.State != want {
				t.Fatal("wrong retained registration state")
			}
			op, _, e := (Control{f}).Load(context.Background(), p.OperationID)
			if e != nil {
				t.Fatal(e)
			}
			generation := r.Generation + 1
			if mode == "prepared_ack" {
				generation = r.Generation
			}
			if op.Generation != generation || op.Phase != "validating" {
				t.Fatal("wrong retained operation")
			}
			writes := fault.stateCAS
			if _, e = registerIsolatedReview(context.Background(), fault, p, r, clock); e == nil || fault.stateCAS != writes {
				t.Fatal("retry resumed uncertain state")
			}
		})
	}
}

func TestRegistrationProtocolStrict(t *testing.T) {
	f := &memoryFiles{data: map[string][]byte{}}
	p, r, clock := registrationFixture(t, f)
	if _, e := registerIsolatedReview(context.Background(), f, p, r, clock); e != nil {
		t.Fatal(e)
	}
	key, _ := registrationKey(p.OperationID)
	original := append([]byte(nil), f.data[key]...)
	for _, bad := range [][]byte{
		bytes.Replace(original, []byte(`"registration_version":1`), []byte(`"registration_version":1,"registration_version":1`), 1),
		bytes.Replace(original, []byte(`"registration_version":1`), []byte(`"registration_version":null`), 1),
		bytes.Replace(original, []byte(`"registration_version":1`), []byte(`"registration_version":1,"password":"SECRET"`), 1),
		bytes.Replace(original, []byte(`"state":"registered"`), []byte(`"state":"completed"`), 1),
		bytes.Replace(original, []byte(`"ready_for_cutover":false`), []byte(`"ready_for_cutover":true`), 1),
		append(original, []byte("{}")...),
	} {
		f.data[key] = bad
		if _, _, e := (handoverRegistrationStore{f}).Load(context.Background(), p.OperationID); e != errRegistration {
			t.Fatal("invalid receipt accepted or error leaked")
		}
	}
}

func TestRegistrationRejectsStaleOrPartialBeforeWrite(t *testing.T) {
	for _, name := range []string{"expired", "partial", "report_changed"} {
		t.Run(name, func(t *testing.T) {
			f := &memoryFiles{data: map[string][]byte{}}
			p, r, clock := registrationFixture(t, f)
			switch name {
			case "expired":
				clock = func() time.Time { return r.Expires }
			case "partial":
				r.Checks[0].Status = "unavailable"
			case "report_changed":
				key, _ := handoverReviewKey(p.OperationID)
				f.data[key] = append(f.data[key], ' ')
			}
			if _, e := registerIsolatedReview(context.Background(), f, p, r, clock); e != errRegistration || len(f.data) != 4 {
				t.Fatal("stale report wrote registration")
			}
		})
	}
}

func TestRegistrationConcurrentSingleWinner(t *testing.T) {
	f := &memoryFiles{data: map[string][]byte{}}
	p, r, clock := registrationFixture(t, f)
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := registerIsolatedReview(context.Background(), f, p, r, clock)
			out <- e
		}()
	}
	wg.Wait()
	close(out)
	winners := 0
	for e := range out {
		if e == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("expected exactly one registration")
	}
}

// Only the three file operations are usable; no lease/target capability is
// available. Registration must reject before invoking any target probe.
type registrationAckBackup struct {
	storage.BackupFiles
	fault *registrationFaultFiles
}

func (f registrationAckBackup) Get(ctx context.Context, k string) (io.ReadCloser, error) {
	return f.fault.Get(ctx, k)
}
func (f registrationAckBackup) CreateOnly(ctx context.Context, k string, r io.Reader, n int64) error {
	if e := f.fault.ConditionalFiles.CreateOnly(ctx, k, r, n); e != nil {
		return e
	}
	return storage.ErrCommitUnknown
}
func (f registrationAckBackup) CompareAndSwap(ctx context.Context, k, token string, data []byte) error {
	return f.fault.CompareAndSwap(ctx, k, token, data)
}

func TestRegistrationCannotRecoverReportAcknowledgementFromDisk(t *testing.T) {
	f := &memoryFiles{data: map[string][]byte{}}
	p, r, clock := registrationFixture(t, f)
	key, _ := handoverReviewKey(p.OperationID)
	delete(f.data, key)
	encoded, _ := json.Marshal(r)
	g := &restoreGuard{plan: p, locations: &Locations{}, publicationConfirmed: true, finalReviewProof: &finalizedReviewProof{sha: digestBytes(encoded)}}
	g.locations.files[3] = registrationAckBackup{fault: &registrationFaultFiles{ConditionalFiles: f}}
	if e := g.persistFinalHandoverReview(context.Background(), r, clock); e != errHandoverUnconfirmed {
		t.Fatal("lost report acknowledgement hidden")
	}
	if _, _, e := (handoverReviewStore{f}).Load(context.Background(), p.OperationID); e != nil {
		t.Fatal("test did not leave visible report")
	}
	called := false
	probe := func(context.Context) (string, error) { called = true; return "", nil }
	if _, e := g.registerHandoverReview(context.Background(), nil, "", nil, nil, handoverProbes{Database: probe, Runtime: probe, Redis: probe}, clock); e != errRegistrationUnknown || called {
		t.Fatal("visible report restored write authority")
	}
	if e := g.persistFinalHandoverReview(context.Background(), r, clock); e != errHandoverUnconfirmed {
		t.Fatal("report persistence automatically retried")
	}
}

func boundRegistrationFixture(t *testing.T, files storage.ConditionalFiles, complete bool) (Plan, boundBehaviorReview, func() time.Time) {
	t.Helper()
	_, p, s, now := handoverFixture(t)
	f := &diagnosticReader{data: map[string][]byte{}}
	writeDiagnosticFixture(t, f, s.op, s.attempt, s.pub)
	for k, data := range f.data {
		if e := files.CreateOnly(context.Background(), k, bytes.NewReader(data), int64(len(data))); e != nil {
			t.Fatal(e)
		}
	}
	snap, e := stableHandoverSnapshot(context.Background(), files, p.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	r := boundBehaviorReview{
		Format:         "ylreader-bound-behavior-review",
		Version:        1,
		Profile:        "bound-https-account-verification-v1",
		Scope:          "localfs-bound-https-account-review",
		Status:         "complete",
		OperationID:    p.OperationID,
		BackupID:       p.BackupID,
		ConsentID:      "80000000-0000-4000-8000-000000000001",
		PlanDigest:     p.Digest,
		PackageSHA:     p.PackageSHA,
		ManifestSHA:    p.ManifestSHA,
		TargetID:       p.TargetID,
		Storage:        p.TargetFingerprint,
		Tree:           snap.pub.TreeFingerprint,
		Generation:     snap.op.Generation,
		OperationSHA:   snap.tokens[0],
		AttemptSHA:     snap.tokens[1],
		PublicationSHA: snap.tokens[2],
		Identity:       handoverTargetIdentity{strings.Repeat("5", 64), strings.Repeat("6", 64), strings.Repeat("7", 64)},
		Certificate:    strings.Repeat("8", 64),
		Ledger:         strings.Repeat("9", 64),
		AccountCount:   1,
		SessionCount:   1,
		Started:        now.Add(-2 * time.Second),
		Finished:       now.Add(-time.Second),
		Expires:        now.Add(time.Minute),
	}
	for _, id := range boundBehaviorCheckIDs() {
		status := "pass"
		if !complete && (id == "old_token_http_rejection" || id == "reader_roundtrip") {
			status = "unavailable"
		}
		r.Checks = append(r.Checks, handoverReviewCheck{id, status})
	}
	if !complete {
		r.Status = "partial"
	}
	if e := saveBoundBehaviorReview(context.Background(), files, r); e != nil {
		t.Fatal(e)
	}
	return p, r, func() time.Time { return now }
}

func TestBoundReviewRegistrationChainAndRejections(t *testing.T) {
	t.Run("complete_registers_and_bumps_generation", func(t *testing.T) {
		f := &memoryFiles{data: map[string][]byte{}}
		p, r, clock := boundRegistrationFixture(t, f, true)
		before, _ := stableHandoverSnapshot(context.Background(), f, p.OperationID)
		reg, e := registerBoundBehaviorReview(context.Background(), f, p, r, clock)
		if e != nil {
			t.Fatal(e)
		}
		after, _ := stableHandoverSnapshot(context.Background(), f, p.OperationID)
		if reg.Scope != "bound-behavior-review-registration" || reg.State != "registered" || reg.Ready || reg.Deployment || reg.BaseSHA != before.tokens[0] || reg.NextSHA != after.tokens[0] || after.op.Generation != before.op.Generation+1 || after.op.Phase != "validating" {
			t.Fatalf("bound registration chain invalid: %+v", reg)
		}
		if _, e := registerBoundBehaviorReview(context.Background(), f, p, r, clock); e == nil {
			t.Fatal("bound registration replayed")
		}
	})

	for _, name := range []string{"partial_10_2", "partial_11_1", "expired", "future", "tampered_disk", "identity_mismatch"} {
		t.Run("reject_"+name, func(t *testing.T) {
			f := &memoryFiles{data: map[string][]byte{}}
			p, r, clock := boundRegistrationFixture(t, f, name != "partial_10_2")
			switch name {
			case "partial_11_1":
				r.Status = "partial"
				r.Checks[11].Status = "unavailable"
				key, _ := boundBehaviorReviewKey(p.OperationID)
				f.data[key], _ = json.Marshal(r)
			case "expired":
				clock = func() time.Time { return r.Expires }
			case "future":
				clock = func() time.Time { return r.Finished.Add(-time.Microsecond) }
			case "tampered_disk":
				key, _ := boundBehaviorReviewKey(p.OperationID)
				f.data[key] = append(f.data[key], ' ')
			case "identity_mismatch":
				r.Identity.Runtime = strings.Repeat("a", 64)
			}
			beforeCount := len(f.data)
			if _, e := registerBoundBehaviorReview(context.Background(), f, p, r, clock); e != errRegistration || len(f.data) != beforeCount {
				t.Fatalf("mode %s wrote or accepted invalid bound review: err=%v", name, e)
			}
		})
	}
}

func TestBoundReviewRegistrationFaultRetentionAndGuardAck(t *testing.T) {
	for _, mode := range []string{"prepared_ack", "state_ack", "receipt_ack", "receipt_conflict", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := &memoryFiles{data: map[string][]byte{}}
			p, r, clock := boundRegistrationFixture(t, f, true)
			fault := &registrationFaultFiles{ConditionalFiles: f, mode: mode}
			if _, e := registerBoundBehaviorReview(context.Background(), fault, p, r, clock); (mode == "conflict" && !errors.Is(e, storage.ErrConflict)) || (mode != "conflict" && e != errRegistrationUnknown) {
				t.Fatalf("mode %s misclassified: %v", mode, e)
			}
			reg, _, e := (handoverRegistrationStore{f}).Load(context.Background(), p.OperationID)
			if e != nil {
				t.Fatal(e)
			}
			want := "prepared"
			if mode == "receipt_ack" {
				want = "registered"
			}
			if reg.State != want || reg.Scope != "bound-behavior-review-registration" {
				t.Fatalf("wrong retained bound registration: %+v", reg)
			}
			writes := fault.stateCAS
			if _, e := registerBoundBehaviorReview(context.Background(), fault, p, r, clock); e == nil || fault.stateCAS != writes {
				t.Fatal("uncertain bound registration replayed")
			}
		})
	}

	t.Run("lost_persistence_ack_cannot_register_from_disk", func(t *testing.T) {
		f := &memoryFiles{data: map[string][]byte{}}
		p, r, clock := boundRegistrationFixture(t, f, true)
		encoded, _ := json.Marshal(r)
		g := &restoreGuard{
			plan: p, locations: &Locations{}, publicationConfirmed: true,
			behaviorRun: &confirmedBehaviorRun{reviewSHA: digestBytes(encoded), reviewPersisted: false},
		}
		g.locations.files[3] = registrationAckBackup{fault: &registrationFaultFiles{ConditionalFiles: f}}
		inputs := handoverProbeInputs{
			Guard: g, Stage: &stagedObjects{}, OwnerID: "50000000-0000-4000-8000-000000000001",
			DatabaseURL: "postgres://reader:@127.0.0.1:5432/yreader_a1_iso_test?sslmode=disable",
		}
		if _, e := g.registerBoundReview(context.Background(), inputs, clock); e != errRegistrationUnknown {
			t.Fatalf("unconfirmed bound persistence registered: %v", e)
		}
	})
}

func TestBoundReviewStatusAndBehaviorStatusDiagnostics(t *testing.T) {
	t.Run("status_absent_partial_complete_registered_expired_drifted_invalid", func(t *testing.T) {
		f := &memoryFiles{data: map[string][]byte{}}
		p, r, clock := boundRegistrationFixture(t, f, true)
		key, _ := boundBehaviorReviewKey(p.OperationID)

		// 1. Complete unregistered -> status: review_only, bound: complete_unregistered
		st := DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
		if st.Status != "review_only" || !st.BoundReview.Present || st.BoundReview.Status != "complete_unregistered" || len(st.BoundReview.PendingChecks) != 0 || st.BoundReview.ControlBinding != "matches_current_control" || st.BoundReview.Persistence != "not_inferred" || st.Ready || st.Deployment {
			t.Fatalf("complete unregistered status wrong: %+v", st)
		}

		// 2. Partial (11/1) -> status: blocked, bound: partial, pending_checks listed
		partial := r
		partial.Status = "partial"
		partial.Checks = append([]handoverReviewCheck(nil), r.Checks...)
		partial.Checks[11].Status = "unavailable"
		f.data[key], _ = json.Marshal(partial)
		st = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
		if st.Status != "blocked" || st.BoundReview.Status != "partial" || len(st.BoundReview.PendingChecks) != 1 || st.BoundReview.PendingChecks[0] != "reader_roundtrip" {
			t.Fatalf("partial bound status wrong: %+v", st)
		}

		// 3. Partial forcibly registered -> inconsistent
		f.data[key], _ = json.Marshal(r)
		reg, e := registerBoundBehaviorReview(context.Background(), f, p, r, clock)
		if e != nil {
			t.Fatal(e)
		}
		st = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
		if st.Status != "observed_registered" || st.Chain != "next_observed" || st.BoundReview.Status != "complete_registered" || st.BoundReview.RegistrationBind != "bound_next_observed" || st.BoundReview.ObservedGen != reg.BaseGeneration || st.BoundReview.CurrentGen != reg.NextGeneration {
			t.Fatalf("complete registered status wrong: %+v", st)
		}

		// 4. Prepared base/next -> registration_pending
		regKey, _ := registrationKey(p.OperationID)
		prep := reg
		prep.State = "prepared"
		f.data[regKey], _ = json.Marshal(prep)
		st = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
		if st.Status != "registration_pending" || st.BoundReview.Status != "registration_pending" {
			t.Fatalf("prepared bound status wrong: %+v", st)
		}
		f.data[regKey], _ = json.Marshal(reg)

		// 5. Expired -> blocked + bound expired
		st = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, r.Expires)
		if st.Status != "blocked" || st.Reason != "report_expired" || st.BoundReview.Status != "expired" || st.BoundReview.Freshness != "expired" {
			t.Fatalf("expired bound status wrong: %+v", st)
		}

		// 6. Drifted -> blocked + bound drifted
		opKey, _ := operationKey(p.OperationID)
		var op Operation
		json.Unmarshal(f.data[opKey], &op)
		op = advance(op, "validating")
		f.data[opKey], _ = json.Marshal(op)
		st = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
		if st.Status != "blocked" || st.Chain != "drifted" || st.BoundReview.Status != "drifted" || st.BoundReview.RegistrationBind != "drifted" {
			t.Fatalf("drifted bound status wrong: %+v", st)
		}

		// 7. Missing report with bound registration -> inconsistent
		delete(f.data, key)
		st = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
		if st.Status != "inconsistent" || st.BoundReview.Present || st.BoundReview.Status != "inconsistent" {
			t.Fatalf("missing bound report status wrong: %+v", st)
		}

		// 8. Malformed bound report -> invalid
		delete(f.data, regKey)
		f.data[key] = []byte(`{"report_version":1,"report_version":1}`)
		st = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
		if st.Status != "unavailable" || !st.BoundReview.Present || st.BoundReview.Status != "invalid" {
			t.Fatalf("invalid bound report status wrong: %+v", st)
		}
	})

	t.Run("behavior_status_ledger_binding_and_errors", func(t *testing.T) {
		files, p, clock := ledgerFixture(t)
		snap, _, e := stableBehaviorLedger(context.Background(), files, p.OperationID, clock())
		if e != nil {
			t.Fatal(e)
		}
		// Absent bound review
		d := DiagnoseBehaviorLedger(context.Background(), files, p.OperationID, clock())
		if d.Status != "responses_observed" || d.BoundReview.Present || d.BoundReview.Status != "absent" {
			t.Fatalf("absent bound review in behavior-status wrong: %+v", d)
		}
		// Add complete bound review matching ledger
		_, a, _ := stableBehaviorLedger(context.Background(), files, p.OperationID, clock())
		r := boundBehaviorReview{
			Format: "ylreader-bound-behavior-review", Version: 1,
			Profile: "bound-https-account-verification-v1", Scope: "localfs-bound-https-account-review", Status: "complete",
			OperationID: p.OperationID, BackupID: p.BackupID, ConsentID: a.intent.ConsentID,
			PlanDigest: p.Digest, PackageSHA: p.PackageSHA, ManifestSHA: p.ManifestSHA, TargetID: p.TargetID,
			Storage: p.TargetFingerprint, Tree: snap.base.control.pub.TreeFingerprint,
			Generation:   snap.base.control.op.Generation,
			OperationSHA: snap.base.control.tokens[0], AttemptSHA: snap.base.control.tokens[1], PublicationSHA: snap.base.control.tokens[2],
			Identity:    handoverTargetIdentity{strings.Repeat("5", 64), a.intent.RuntimeSHA, strings.Repeat("7", 64)},
			Certificate: strings.Repeat("8", 64), Ledger: behaviorLedgerDigest(snap),
			AccountCount: 1, SessionCount: 1,
			Started: a.intent.Recorded, Finished: clock(), Expires: a.intent.Expires,
		}
		for _, id := range boundBehaviorCheckIDs() {
			r.Checks = append(r.Checks, handoverReviewCheck{id, "pass"})
		}
		if e := saveBoundBehaviorReview(context.Background(), files, r); e != nil {
			t.Fatal(e)
		}
		d = DiagnoseBehaviorLedger(context.Background(), files, p.OperationID, clock())
		if d.Status != "responses_observed" || !d.BoundReview.Present || d.BoundReview.Status != "complete_unregistered" || d.BoundReview.ControlBinding != "matches_current_control" {
			t.Fatalf("complete bound review in behavior-status wrong: %+v", d)
		}
		// Tamper ledger digest in bound review -> inconsistent ledger_mismatch
		bad := r
		bad.Ledger = strings.Repeat("f", 64)
		key, _ := boundBehaviorReviewKey(p.OperationID)
		files.data[key], _ = json.Marshal(bad)
		d = DiagnoseBehaviorLedger(context.Background(), files, p.OperationID, clock())
		if d.BoundReview.Status != "inconsistent" || d.BoundReview.ControlBinding != "ledger_mismatch" {
			t.Fatalf("ledger mismatch not detected: %+v", d.BoundReview)
		}
	})
}
