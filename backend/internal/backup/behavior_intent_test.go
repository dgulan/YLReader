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

func intentFixture(t *testing.T) (*preparedBehaviorInput, *memoryFiles, Plan, func() time.Time) {
	t.Helper()
	a, input, control, p, now := consentFixture(t)
	prepared, e := prepareBehaviorInput(context.Background(), input, control, "consent.json", p, a.RuntimeSHA, func() time.Time { return now })
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(prepared.Close)
	return prepared, &memoryFiles{data: control.data}, p, func() time.Time { return now }
}

func TestBehaviorIntentDurableSlotAndNoAuthority(t *testing.T) {
	prepared, files, p, clock := intentFixture(t)
	before := map[string][]byte{}
	for k, b := range files.data {
		before[k] = append([]byte(nil), b...)
	}
	r, e := recordBehaviorIntent(context.Background(), files, prepared, p, prepared.consent.RuntimeSHA, clock)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := behaviorIntentKey(p.OperationID)
	body := files.data[key]
	if r.validate() != nil || len(files.data) != len(before)+1 || bytes.Contains(body, []byte("SECRET")) || bytes.Contains(body, []byte("account.json")) || bytes.Contains(body, []byte("原账号")) {
		t.Fatal("invalid or secret-bearing intent")
	}
	for k, b := range before {
		if !bytes.Equal(files.data[k], b) {
			t.Fatal("control records changed")
		}
	}
	for _, changeConsent := range []bool{false, true} {
		if changeConsent {
			prepared.consent.ID = "80000000-0000-4000-8000-000000000099"
		}
		if _, e = recordBehaviorIntent(context.Background(), files, prepared, p, prepared.consent.RuntimeSHA, clock); e != storage.ErrConflict {
			t.Fatal("same-operation replay accepted")
		}
	}
	d := DiagnoseBehaviorIntent(context.Background(), files, p.OperationID, clock())
	if d.Status != "outcome_unknown" || d.Persistence != "not_inferred" || d.Dispatch || d.Retry || d.Deployment {
		t.Fatal("disk records granted authority")
	}
}

func TestBehaviorIntentConcurrentSingleWinner(t *testing.T) {
	prepared, files, p, clock := intentFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := recordBehaviorIntent(context.Background(), files, prepared, p, prepared.consent.RuntimeSHA, clock)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		} else if e != storage.ErrConflict {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("create-only must have one winner")
	}
}

type intentFaultFiles struct {
	storage.ConditionalFiles
	mode    string
	written bool
	cancel  context.CancelFunc
}

func (f *intentFaultFiles) CreateOnly(ctx context.Context, key string, r io.Reader, n int64) error {
	if f.mode == "write_failure" {
		return errors.New("SECRET write")
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
	if f.mode == "control_drift" {
		m := f.ConditionalFiles.(*memoryFiles)
		state := strings.TrimSuffix(key, "behavior-intent.json") + "state.json"
		m.data[state] = append(m.data[state], ' ')
	}
	return nil
}
func (f *intentFaultFiles) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if f.mode == "readback" && f.written && strings.HasSuffix(key, "behavior-intent.json") {
		return nil, errors.New("SECRET read")
	}
	return f.ConditionalFiles.Get(ctx, key)
}

func TestBehaviorIntentUncertainWritesNeverReplay(t *testing.T) {
	for _, mode := range []string{"write_failure", "ack_lost", "readback", "cancel", "control_drift", "expired_after_write"} {
		t.Run(mode, func(t *testing.T) {
			prepared, base, p, clock := intentFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			files := &intentFaultFiles{ConditionalFiles: base, mode: mode, cancel: cancel}
			when := func() time.Time {
				if mode == "expired_after_write" && files.written {
					return prepared.consent.Expires
				}
				return clock()
			}
			if _, e := recordBehaviorIntent(ctx, files, prepared, p, prepared.consent.RuntimeSHA, when); e != errBehaviorIntentUnknown || strings.Contains(e.Error(), "SECRET") {
				t.Fatal("uncertain result misclassified")
			}
			key, _ := behaviorIntentKey(p.OperationID)
			if mode != "write_failure" {
				if _, ok := base.data[key]; !ok {
					t.Fatal("lost retained marker")
				}
				if _, e := recordBehaviorIntent(context.Background(), base, prepared, p, prepared.consent.RuntimeSHA, clock); e == nil {
					t.Fatal("uncertain intent replayed")
				}
			}
			d := DiagnoseBehaviorIntent(context.Background(), base, p.OperationID, clock())
			if d.Dispatch || d.Retry || d.Deployment {
				t.Fatal("uncertain diagnosis granted retry")
			}
		})
	}
}

func TestBehaviorIntentPreconditionsBeforeWrite(t *testing.T) {
	for _, name := range []string{"closed_input", "wrong_runtime", "expired", "cancelled", "wrong_plan", "wrong_account"} {
		t.Run(name, func(t *testing.T) {
			prepared, files, p, clock := intentFixture(t)
			runtime := prepared.consent.RuntimeSHA
			when := clock
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "closed_input":
				prepared.Close()
			case "wrong_runtime":
				runtime = strings.Repeat("f", 64)
			case "expired":
				when = func() time.Time { return prepared.consent.Expires }
			case "cancelled":
				cancel()
			case "wrong_plan":
				p.Digest = strings.Repeat("f", 64)
			case "wrong_account":
				prepared.accounts[0].UserID = prepared.consent.ID
			}
			before := len(files.data)
			if _, e := recordBehaviorIntent(ctx, files, prepared, p, runtime, when); e != errBehaviorIntent || len(files.data) != before {
				t.Fatal("invalid precondition wrote an intent")
			}
		})
	}
}

func TestBehaviorIntentDiagnosticsStrictAndReadOnly(t *testing.T) {
	for _, name := range []string{"missing", "normal", "expired", "owner", "generation", "control_binding", "duplicate", "unknown", "null", "oversized", "changed", "denied", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			prepared, files, p, clock := intentFixture(t)
			_, e := recordBehaviorIntent(context.Background(), files, prepared, p, prepared.consent.RuntimeSHA, clock)
			if e != nil {
				t.Fatal(e)
			}
			key, _ := behaviorIntentKey(p.OperationID)
			when := clock()
			expected := "outcome_unknown"
			var reader RestoreRecordReader = files
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "missing":
				delete(files.data, key)
				expected = "intent_not_observed"
			case "expired":
				when = prepared.consent.Expires
				expected = "stale"
			case "owner", "generation":
				var r behaviorIntent
				json.Unmarshal(files.data[key], &r)
				if name == "owner" {
					r.OwnerID = prepared.consent.ID
					expected = "inconsistent"
				} else {
					r.Generation++
					expected = "stale"
				}
				files.data[key], _ = json.Marshal(r)
			case "control_binding":
				k := "operations/" + p.OperationID + "/attempt.json"
				files.data[k] = bytes.Replace(files.data[k], []byte(prepared.consent.OwnerID), []byte(prepared.consent.ID), 1)
				expected = "inconsistent"
			case "duplicate":
				files.data[key] = append([]byte(`{"intent_version":1,`), files.data[key][1:]...)
				expected = "unavailable"
			case "unknown":
				files.data[key] = append([]byte(`{"SECRET":true,`), files.data[key][1:]...)
				expected = "unavailable"
			case "null":
				files.data[key] = []byte("null")
				expected = "unavailable"
			case "oversized":
				files.data[key] = bytes.Repeat([]byte("x"), 16385)
				expected = "unavailable"
			case "changed":
				reader = &diagnosticReader{data: files.data, hook: func(f *diagnosticReader) {
					if f.reads == 8 {
						f.data[key] = append(f.data[key], ' ')
					}
				}}
				expected = "changed"
			case "denied":
				reader = &diagnosticReader{err: errors.New("SECRET permission")}
				expected = "unavailable"
			case "cancelled":
				cancel()
				expected = "unavailable"
			}
			before, _ := json.Marshal(files.data)
			d := DiagnoseBehaviorIntent(ctx, reader, p.OperationID, when)
			after, _ := json.Marshal(files.data)
			if d.Status != expected || d.Dispatch || d.Retry || d.Deployment || d.Persistence != "not_inferred" {
				t.Fatalf("wrong diagnostic: %s want %s", d.Status, expected)
			}
			if name != "changed" && !bytes.Equal(before, after) {
				t.Fatal("diagnostic wrote records")
			}
			data, _ := json.Marshal(d)
			if bytes.Contains(data, []byte("SECRET")) {
				t.Fatal("error input leaked")
			}
		})
	}
}
