package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func collectionFixture(t *testing.T) (*memoryFiles, Plan, handoverProbes, func() time.Time) {
	t.Helper()
	_, p, s, now := handoverFixture(t)
	f := &diagnosticReader{data: map[string][]byte{}}
	writeDiagnosticFixture(t, f, s.op, s.attempt, s.pub)
	files := &memoryFiles{data: f.data}
	return files, p, handoverProbes{
		Database: func(context.Context) (string, error) { return strings.Repeat("5", 64), nil },
		Runtime:  func(context.Context) (string, error) { return strings.Repeat("6", 64), nil },
		Redis:    func(context.Context) (string, error) { return strings.Repeat("7", 64), nil },
	}, func() time.Time { return now }
}

func TestHandoverCollectionPartialReviewNeverPassesFullGate(t *testing.T) {
	f, p, probes, clock := collectionFixture(t)
	before := map[string][]byte{}
	for k, v := range f.data {
		before[k] = append([]byte(nil), v...)
	}
	r, e := collectHandoverReview(context.Background(), f, p, probes, clock)
	if e != nil {
		t.Fatal(e)
	}
	pass, unavailable := 0, 0
	for _, c := range r.Checks {
		if c.Status == "pass" {
			pass++
		}
		if c.Status == "unavailable" {
			unavailable++
		}
	}
	if pass != 4 || unavailable != 6 || r.ReadyForCutover || r.DeploymentAuthorized {
		t.Fatal("partial report overstated")
	}
	s, e := stableHandoverSnapshot(context.Background(), f, p.OperationID)
	if e != nil || r.validateBindingsFor(p, s, r.TargetIdentity, clock()) != nil || r.validateFor(p, s, r.TargetIdentity, clock()) == nil {
		t.Fatal("partial report gate changed")
	}
	if !reflect.DeepEqual(before, f.data) {
		t.Fatal("collection wrote records")
	}
	if e = persistCollectedHandoverReview(context.Background(), f, p, r, clock); e != nil {
		t.Fatal(e)
	}
	for k, v := range before {
		if string(f.data[k]) != string(v) {
			t.Fatal("persistence changed a control record")
		}
	}
	if len(f.data) != 4 {
		t.Fatal("unexpected record created")
	}
	if e = persistCollectedHandoverReview(context.Background(), f, p, r, clock); !errors.Is(e, storage.ErrConflict) {
		t.Fatal("report overwritten", e)
	}
}

func TestHandoverCollectionProbeFailuresAndChangedRecords(t *testing.T) {
	for _, name := range []string{"database_error", "runtime_error", "redis_error", "invalid_digest", "cancel", "record_changed"} {
		t.Run(name, func(t *testing.T) {
			f, p, probes, clock := collectionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bad := func(context.Context) (string, error) { return "SECRET", errors.New("private DSN password") }
			switch name {
			case "database_error":
				probes.Database = bad
			case "runtime_error":
				probes.Runtime = bad
			case "redis_error":
				probes.Redis = bad
			case "invalid_digest":
				probes.Runtime = func(context.Context) (string, error) { return "SECRET", nil }
			case "cancel":
				probes.Runtime = func(context.Context) (string, error) { cancel(); return strings.Repeat("6", 64), nil }
			case "record_changed":
				probes.Redis = func(context.Context) (string, error) {
					k := "operations/" + p.OperationID + "/state.json"
					f.data[k] = append(f.data[k], ' ')
					return strings.Repeat("7", 64), nil
				}
			}
			r, e := collectHandoverReview(ctx, f, p, probes, clock)
			if e != errHandoverReview || r.Format != "" || len(f.data) != 3 {
				t.Fatal("failed collection leaked or persisted")
			}
		})
	}
}

func TestHandoverCollectionPreflightAndTimeLimits(t *testing.T) {
	for _, name := range []string{"expired", "before_operation", "clock_reversed", "plan_tampered", "canceled_operation", "missing_probe"} {
		t.Run(name, func(t *testing.T) {
			f, p, probes, clock := collectionFixture(t)
			called := false
			probes.Database = func(context.Context) (string, error) { called = true; return strings.Repeat("5", 64), nil }
			switch name {
			case "expired":
				stamp, _ := time.Parse(time.RFC3339Nano, p.Expires)
				clock = func() time.Time { return stamp }
			case "before_operation":
				stamp, _ := time.Parse(time.RFC3339Nano, p.Created)
				clock = func() time.Time { return stamp }
			case "clock_reversed":
				first := clock()
				calls := 0
				clock = func() time.Time {
					calls++
					if calls == 1 {
						return first
					}
					return first.Add(-time.Second)
				}
			case "plan_tampered":
				p.Database.Port++
			case "canceled_operation":
				key := "operations/" + p.OperationID + "/state.json"
				var op Operation
				json.Unmarshal(f.data[key], &op)
				op.CancelRequested = true
				f.data[key], _ = json.Marshal(op)
			case "missing_probe":
				probes.Redis = nil
			}
			if _, e := collectHandoverReview(context.Background(), f, p, probes, clock); e != errHandoverReview {
				t.Fatal("invalid preflight/time accepted")
			}
			if name != "clock_reversed" && called {
				t.Fatal("probe called before preflight")
			}
		})
	}
	// TTL is capped to the plan's existing expiration, never extended.
	f, p, probes, _ := collectionFixture(t)
	end, _ := time.Parse(time.RFC3339Nano, p.Expires)
	clock := func() time.Time { return end.Add(-time.Second) }
	r, e := collectHandoverReview(context.Background(), f, p, probes, clock)
	if e != nil || !r.Expires.Equal(end) {
		t.Fatal("plan expiry not preserved", e)
	}
}

type collectionWriteFiles struct {
	*memoryFiles
	afterWrite func()
	lostAck    bool
}

func (f *collectionWriteFiles) CreateOnly(ctx context.Context, key string, r io.Reader, n int64) error {
	if e := f.memoryFiles.CreateOnly(ctx, key, r, n); e != nil {
		return e
	}
	if f.afterWrite != nil {
		f.afterWrite()
	}
	if f.lostAck {
		return storage.ErrCommitUnknown
	}
	return nil
}

func TestHandoverCollectionPersistenceFailureWindows(t *testing.T) {
	for _, name := range []string{"changed_before", "changed_after", "expired_after", "lost_ack"} {
		t.Run(name, func(t *testing.T) {
			f, p, probes, clock := collectionFixture(t)
			r, e := collectHandoverReview(context.Background(), f, p, probes, clock)
			if e != nil {
				t.Fatal(e)
			}
			wrapped := &collectionWriteFiles{memoryFiles: f}
			key := "operations/" + p.OperationID + "/state.json"
			mutate := func() { f.data[key] = append(f.data[key], ' ') }
			switch name {
			case "changed_before":
				mutate()
			case "changed_after":
				wrapped.afterWrite = mutate
			case "expired_after":
				old := clock
				calls := 0
				clock = func() time.Time {
					calls++
					if calls == 1 {
						return old()
					}
					return r.Expires
				}
			case "lost_ack":
				wrapped.lostAck = true
			}
			e = persistCollectedHandoverReview(context.Background(), wrapped, p, r, clock)
			if e == nil {
				t.Fatal("failure window reported success")
			}
			want := 4
			if name == "changed_before" {
				want = 3
			}
			if len(f.data) != want {
				t.Fatal("artifact lost or unexpected write")
			}
			if name == "lost_ack" && e != errHandoverUnconfirmed {
				t.Fatal("acknowledgement loss hidden")
			}
		})
	}
}

func TestHandoverProbeFactoryRejectsOtherRuntimeTarget(t *testing.T) {
	cleanPG(t)
	_, p, _, _ := collectionFixture(t)
	i := handoverProbeInputs{ExpectedRuntime: runtimeFixture(), ActualRuntime: runtimeFixture()}
	i.ExpectedRuntime.TargetID, i.ActualRuntime.TargetID = p.TargetID, p.TargetID
	i.DatabaseURL = fmt.Sprintf("postgres://reader:@%s:%d/%s?sslmode=disable", p.Database.Host, p.Database.Port, p.Database.Database)
	if _, e := i.probes(p); e != nil {
		t.Fatal("bound inputs rejected", e)
	}
	for _, name := range []string{"runtime_target", "database_target"} {
		t.Run(name, func(t *testing.T) {
			changed := i
			if name == "runtime_target" {
				changed.ExpectedRuntime.TargetID = "00000000-0000-4000-8000-000000000099"
			} else {
				changed.DatabaseURL = strings.Replace(changed.DatabaseURL, "/"+p.Database.Database+"?", "/yreader_a1_iso_other?", 1)
			}
			if _, e := changed.probes(p); e != errHandoverReview {
				t.Fatal("target drift accepted")
			}
		})
	}
}
