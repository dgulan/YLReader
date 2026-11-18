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
