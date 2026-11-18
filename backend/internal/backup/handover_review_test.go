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

func handoverFixture(t *testing.T) (handoverReview, Plan, diagnosticSnapshot, time.Time) {
	t.Helper()
	f, o, a, _ := diagnosticFixture(t)
	p := planExample(t)
	created, _ := time.Parse(time.RFC3339Nano, p.Created)
	o.ID, o.BackupID, o.PlanDigest = p.OperationID, p.BackupID, p.Digest
	o.CreatedAt, o.UpdatedAt, o.HeartbeatAt = created, created.Add(2*time.Second), created.Add(2*time.Second)
	pub := publicationFor(p, o.OwnerID, ".scratch-"+strings.Repeat("a", 32))
	pub.State, pub.TreeFingerprint = "verified", strings.Repeat("4", 64)
	a.OperationID, a.BackupID, a.PlanDigest, a.TargetFingerprint = o.ID, o.BackupID, o.PlanDigest, p.TargetFingerprint
	g := pub.Staging
	a.Generation = &g
	f.data = map[string][]byte{}
	writeDiagnosticFixture(t, f, o, a, pub)
	s, e := readDiagnosticSnapshot(context.Background(), diagnosticFiles{f}, o.ID)
	if e != nil {
		t.Fatal(e)
	}
	r := handoverReview{Format: "ylreader-handover-review", Version: 1, Scope: "isolated-localfs-handover-review",
		OperationID: o.ID, BackupID: o.BackupID, PlanDigest: p.Digest, PackageSHA: p.PackageSHA, ManifestSHA: p.ManifestSHA,
		TargetID: p.TargetID, StorageFingerprint: p.TargetFingerprint, TreeFingerprint: pub.TreeFingerprint, Generation: o.Generation,
		OperationSHA: s.tokens[0], AttemptSHA: s.tokens[1], PublicationSHA: s.tokens[2],
		TargetIdentity: handoverTargetIdentity{strings.Repeat("5", 64), strings.Repeat("6", 64), strings.Repeat("7", 64)},
		Started:        created.Add(3 * time.Second), Finished: created.Add(10 * time.Second), Expires: created.Add(310 * time.Second)}
	for _, id := range handoverCheckIDs() {
		r.Checks = append(r.Checks, handoverReviewCheck{id, "pass"})
	}
	return r, p, s, created.Add(11 * time.Second)
}

func TestHandoverReviewStrictParsing(t *testing.T) {
	r, _, _, _ := handoverFixture(t)
	data, _ := json.Marshal(r)
	if _, e := loadHandoverReview(bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	for name, raw := range map[string][]byte{
		"duplicate":        bytes.Replace(data, []byte(`"report_version":1`), []byte(`"report_version":1,"report_version":1`), 1),
		"unknown":          bytes.Replace(data, []byte(`"report_version":1`), []byte(`"report_version":1,"password":"SECRET"`), 1),
		"missing":          bytes.Replace(data, []byte(`"ready_for_cutover":false`), []byte(`"unused":false`), 1),
		"null":             bytes.Replace(data, []byte(`"deployment_authorized":false`), []byte(`"deployment_authorized":null`), 1),
		"nested_duplicate": bytes.Replace(data, []byte(`"id":"schema_ledger"`), []byte(`"id":"schema_ledger","id":"schema_ledger"`), 1),
		"offset":           bytes.Replace(data, []byte(`08:00:03Z`), []byte(`08:00:03+00:00`), 1),
		"trailing":         append(append([]byte(nil), data...), []byte("{}")...),
		"oversize":         append(append([]byte(nil), data...), bytes.Repeat([]byte(" "), 32769)...),
		"utf8":             append(append([]byte(nil), data...), 0xff),
	} {
		t.Run(name, func(t *testing.T) {
			_, e := loadHandoverReview(bytes.NewReader(raw))
			if !errors.Is(e, errHandoverReview) || strings.Contains(e.Error(), "SECRET") {
				t.Fatal("malformed review accepted or error leaked", e)
			}
		})
	}
	if _, e := loadHandoverReview(nil); e == nil {
		t.Fatal("nil reader accepted")
	}
}

func TestHandoverReviewShapeAndCheckInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*handoverReview)
	}{
		{"version", func(r *handoverReview) { r.Version = 2 }},
		{"scope", func(r *handoverReview) { r.Scope = "production" }},
		{"authorized", func(r *handoverReview) { r.DeploymentAuthorized = true }},
		{"ready", func(r *handoverReview) { r.ReadyForCutover = true }},
		{"hash", func(r *handoverReview) { r.PackageSHA = "secret" }},
		{"identity", func(r *handoverReview) { r.TargetIdentity.Redis = "" }},
		{"generation", func(r *handoverReview) { r.Generation = 0 }},
		{"missing_check", func(r *handoverReview) { r.Checks = r.Checks[1:] }},
		{"duplicate_check", func(r *handoverReview) { r.Checks[1] = r.Checks[0] }},
		{"extra_check", func(r *handoverReview) { r.Checks = append(r.Checks, r.Checks[0]) }},
		{"check_status", func(r *handoverReview) { r.Checks[0].Status = "true" }},
		{"time_reversed", func(r *handoverReview) { r.Finished = r.Started.Add(-time.Second) }},
		{"window", func(r *handoverReview) { r.Started = r.Finished.Add(-5*time.Minute - time.Microsecond) }},
		{"ttl", func(r *handoverReview) { r.Expires = r.Finished.Add(5*time.Minute + time.Microsecond) }},
		{"empty_ttl", func(r *handoverReview) { r.Expires = r.Finished }},
		{"nanoseconds", func(r *handoverReview) { r.Started = r.Started.Add(time.Nanosecond) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _, _ := handoverFixture(t)
			tc.change(&r)
			if r.validate() == nil {
				t.Fatal("invalid review accepted")
			}
		})
	}
}

func TestHandoverReviewBoundInputsAndFreshness(t *testing.T) {
	r, p, s, now := handoverFixture(t)
	if e := r.validateFor(p, s, r.TargetIdentity, now); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		change func(*handoverReview, *Plan, *diagnosticSnapshot, *handoverTargetIdentity, *time.Time)
	}{
		{"package", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.PackageSHA = strings.Repeat("9", 64)
		}},
		{"manifest", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.ManifestSHA = strings.Repeat("9", 64)
		}},
		{"tree", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.TreeFingerprint = strings.Repeat("9", 64)
		}},
		{"target", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.TargetID = "00000000-0000-4000-8000-000000000099"
		}},
		{"generation", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.Generation++
		}},
		{"record_token", func(_ *handoverReview, _ *Plan, s *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			s.tokens[1] = strings.Repeat("9", 64)
		}},
		{"owner", func(_ *handoverReview, _ *Plan, s *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			s.pub.OwnerID = "00000000-0000-4000-8000-000000000099"
		}},
		{"database_unknown", func(_ *handoverReview, _ *Plan, s *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			s.attempt.DatabaseOutcome = "unknown"
		}},
		{"publication", func(_ *handoverReview, _ *Plan, s *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			s.pub.State = "publishing"
		}},
		{"cancel", func(_ *handoverReview, _ *Plan, s *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			s.op.CancelRequested = true
		}},
		{"phase", func(_ *handoverReview, _ *Plan, s *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			s.op.Phase = "ready_for_cutover"
		}},
		{"database_identity", func(_ *handoverReview, _ *Plan, _ *diagnosticSnapshot, i *handoverTargetIdentity, _ *time.Time) {
			i.Database = strings.Repeat("9", 64)
		}},
		{"runtime_identity", func(_ *handoverReview, _ *Plan, _ *diagnosticSnapshot, i *handoverTargetIdentity, _ *time.Time) {
			i.Runtime = strings.Repeat("9", 64)
		}},
		{"redis_identity", func(_ *handoverReview, _ *Plan, _ *diagnosticSnapshot, i *handoverTargetIdentity, _ *time.Time) {
			i.Redis = strings.Repeat("9", 64)
		}},
		{"expired", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, n *time.Time) {
			*n = r.Expires
		}},
		{"future", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, n *time.Time) {
			*n = r.Finished.Add(-time.Microsecond)
		}},
		{"before_plan", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.Started = r.Started.Add(-4 * time.Second)
		}},
		{"before_update", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.Started = r.Started.Add(-2 * time.Second)
		}},
		{"extends_plan", func(r *handoverReview, p *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, n *time.Time) {
			end, _ := time.Parse(time.RFC3339Nano, p.Expires)
			r.Started = end.Add(-time.Second)
			r.Finished = end
			r.Expires = end.Add(time.Second)
			*n = end
		}},
		{"plan_digest", func(_ *handoverReview, p *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			p.Database.Port++
		}},
		{"fail", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.Checks[0].Status = "fail"
		}},
		{"unavailable", func(r *handoverReview, _ *Plan, _ *diagnosticSnapshot, _ *handoverTargetIdentity, _ *time.Time) {
			r.Checks[0].Status = "unavailable"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, p, s, n := handoverFixture(t)
			live := r.TargetIdentity
			tc.change(&r, &p, &s, &live, &n)
			if r.validateFor(p, s, live, n) == nil {
				t.Fatal("changed/unconfirmed inputs accepted")
			}
		})
	}
	// Inclusive finished bound and exclusive expiration bound.
	if r.validateFor(p, s, r.TargetIdentity, r.Finished) != nil || r.validateFor(p, s, r.TargetIdentity, r.Expires.Add(-time.Microsecond)) != nil {
		t.Fatal("valid boundary rejected")
	}
}

type handoverFaultFiles struct {
	*memoryFiles
	writeErr error
	readErr  error
	corrupt  bool
}

func (f *handoverFaultFiles) CreateOnly(ctx context.Context, k string, r io.Reader, n int64) error {
	if e := f.memoryFiles.CreateOnly(ctx, k, r, n); e != nil {
		return e
	}
	if f.corrupt {
		f.mu.Lock()
		f.data[k] = append(f.data[k], ' ')
		f.mu.Unlock()
	}
	return f.writeErr
}
func (f *handoverFaultFiles) Get(ctx context.Context, k string) (io.ReadCloser, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.memoryFiles.Get(ctx, k)
}

func TestHandoverReviewImmutablePersistenceAndConcurrentCreation(t *testing.T) {
	r, _, _, _ := handoverFixture(t)
	files := &memoryFiles{data: map[string][]byte{}}
	store := handoverReviewStore{files}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- store.Create(context.Background(), r) }()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, storage.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 || len(files.data) != 1 {
		t.Fatal("exclusive create failed")
	}
	loaded, token, e := store.Load(context.Background(), r.OperationID)
	if e != nil || !validSHA(token) || loaded.OperationID != r.OperationID {
		t.Fatal("readback failed", e)
	}
	r.Checks[0].Status = "fail"
	if !errors.Is(store.Create(context.Background(), r), storage.ErrConflict) {
		t.Fatal("overwrote existing review")
	}
	// Failed review can be preserved in a fresh store but grants no transition.
	if e = (handoverReviewStore{&memoryFiles{data: map[string][]byte{}}}).Create(context.Background(), r); e != nil {
		t.Fatal(e)
	}
}

func TestHandoverReviewUncertainWritesRetainArtifacts(t *testing.T) {
	for _, name := range []string{"lost_ack", "conflict_and_unknown", "read_failure", "byte_change"} {
		t.Run(name, func(t *testing.T) {
			r, _, _, _ := handoverFixture(t)
			f := &handoverFaultFiles{memoryFiles: &memoryFiles{data: map[string][]byte{}}}
			switch name {
			case "lost_ack":
				f.writeErr = storage.ErrCommitUnknown
			case "conflict_and_unknown":
				f.writeErr = errors.Join(storage.ErrConflict, storage.ErrCommitUnknown)
			case "read_failure":
				f.readErr = errors.New("SECRET DSN private path")
			case "byte_change":
				f.corrupt = true
			}
			e := (handoverReviewStore{f}).Create(context.Background(), r)
			if !errors.Is(e, errHandoverUnconfirmed) || strings.Contains(e.Error(), "SECRET") || len(f.data) != 1 {
				t.Fatal("unknown confirmation lost retention or leaked", e)
			}
		})
	}
}

func TestHandoverReviewStoreInvalidInputs(t *testing.T) {
	r, _, _, _ := handoverFixture(t)
	f := &memoryFiles{data: map[string][]byte{}}
	store := handoverReviewStore{f}
	if _, _, e := store.Load(context.Background(), "../secret"); e == nil {
		t.Fatal("invalid key accepted")
	}
	r.ReadyForCutover = true
	if store.Create(context.Background(), r) == nil || len(f.data) != 0 {
		t.Fatal("invalid review persisted")
	}
	r.ReadyForCutover = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(store.Create(ctx, r), errHandoverUnconfirmed) || len(f.data) != 0 {
		t.Fatal("canceled create wrote")
	}
	if (handoverReviewStore{}).Create(context.Background(), r) == nil {
		t.Fatal("nil storage accepted")
	}
}
