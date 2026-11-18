package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

// This fake has no write methods. Its read hook simulates concurrent changes.
type diagnosticReader struct {
	data  map[string][]byte
	reads int
	hook  func(*diagnosticReader)
	err   error
}

func (f *diagnosticReader) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	f.reads++
	if f.hook != nil {
		f.hook(f)
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func diagnosticFixture(t *testing.T) (*diagnosticReader, Operation, restoreAttempt, publicationReceipt) {
	t.Helper()
	o := advance(advance(example(t), "staging"), "validating")
	p := Plan{OperationID: o.ID, BackupID: o.BackupID, Digest: o.PlanDigest,
		PackageSHA: strings.Repeat("1", 64), ManifestSHA: strings.Repeat("2", 64), TargetFingerprint: strings.Repeat("3", 64)}
	pub := publicationFor(p, o.OwnerID, ".scratch-"+strings.Repeat("a", 32))
	pub.State, pub.TreeFingerprint = "verified", strings.Repeat("4", 64)
	gen := pub.Staging
	a := restoreAttempt{Format: "ylreader-restore-attempt", Version: 1, OperationID: o.ID, BackupID: o.BackupID,
		PlanDigest: o.PlanDigest, OwnerID: o.OwnerID, TargetFingerprint: p.TargetFingerprint, Generation: &gen, DatabaseOutcome: "committed"}
	f := &diagnosticReader{data: map[string][]byte{}}
	writeDiagnosticFixture(t, f, o, a, pub)
	return f, o, a, pub
}

func writeDiagnosticFixture(t *testing.T, f *diagnosticReader, o Operation, a restoreAttempt, p publicationReceipt) {
	t.Helper()
	if o.Validate() != nil || a.validate() != nil || p.validate() != nil {
		t.Fatal("invalid fixture")
	}
	keys := []string{"operations/" + o.ID + "/state.json", "operations/" + o.ID + "/attempt.json", "operations/" + o.ID + "/publication.json"}
	for i, v := range []any{o, a, p} {
		data, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		f.data[keys[i]] = data
	}
}

func TestDiagnosticConsistentRecordsAreReadOnlyAndNotAuthorization(t *testing.T) {
	f, o, _, _ := diagnosticFixture(t)
	before := map[string][]byte{}
	for k, v := range f.data {
		before[k] = append([]byte(nil), v...)
	}
	r := DiagnoseRestoreRecords(context.Background(), f, o.ID)
	if r.Status != "consistent_records" || r.Reason != "records_match" || r.ObservedGeneration != o.Generation || r.DatabaseOutcome != "committed" || r.PublicationState != "verified" || r.ReadyForCutover || r.DeploymentAuthorized || f.reads != 6 {
		t.Fatalf("unexpected diagnostic: %+v", r)
	}
	if !reflect.DeepEqual(before, f.data) {
		t.Fatal("diagnosis modified records")
	}
	data, _ := json.Marshal(r)
	for _, forbidden := range []string{"scratch", "fingerprint", "password", "dsn", "plan_digest", "owner_id"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("sensitive/internal field leaked")
		}
	}
}

func TestDiagnosticBlockedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*Operation, *restoreAttempt, *publicationReceipt)
	}{
		{"unknown", "database_unconfirmed", func(_ *Operation, a *restoreAttempt, _ *publicationReceipt) { a.DatabaseOutcome = "unknown" }},
		{"rollback", "database_unconfirmed", func(_ *Operation, a *restoreAttempt, _ *publicationReceipt) { a.DatabaseOutcome = "rolled_back" }},
		{"not_started", "database_unconfirmed", func(_ *Operation, a *restoreAttempt, _ *publicationReceipt) { a.DatabaseOutcome = "not_started" }},
		{"publishing", "publication_unconfirmed", func(_ *Operation, _ *restoreAttempt, p *publicationReceipt) { p.State = "publishing" }},
		{"cancel", "cancellation_requested", func(o *Operation, _ *restoreAttempt, _ *publicationReceipt) { o.CancelRequested = true }},
		{"ready", "operation_not_validating", func(o *Operation, _ *restoreAttempt, _ *publicationReceipt) { *o = advance(*o, "ready_for_cutover") }},
		{"completed", "operation_not_validating", func(o *Operation, _ *restoreAttempt, _ *publicationReceipt) { *o = advance(*o, "completed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, o, a, p := diagnosticFixture(t)
			tc.change(&o, &a, &p)
			writeDiagnosticFixture(t, f, o, a, p)
			r := DiagnoseRestoreRecords(context.Background(), f, o.ID)
			if r.Status != "blocked" || r.Reason != tc.reason || r.ReadyForCutover || r.DeploymentAuthorized {
				t.Fatalf("unsafe result: %+v", r)
			}
		})
	}
}

func TestDiagnosticBindingDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*restoreAttempt, *publicationReceipt)
	}{
		{"backup", func(a *restoreAttempt, _ *publicationReceipt) { a.BackupID = "00000000-0000-4000-8000-000000000099" }},
		{"plan", func(a *restoreAttempt, _ *publicationReceipt) { a.PlanDigest = strings.Repeat("9", 64) }},
		{"owner", func(_ *restoreAttempt, p *publicationReceipt) { p.OwnerID = "00000000-0000-4000-8000-000000000099" }},
		{"target", func(a *restoreAttempt, _ *publicationReceipt) { a.TargetFingerprint = strings.Repeat("9", 64) }},
		{"generation", func(a *restoreAttempt, _ *publicationReceipt) {
			g := ".scratch-" + strings.Repeat("b", 32)
			a.Generation = &g
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, o, a, p := diagnosticFixture(t)
			tc.change(&a, &p)
			writeDiagnosticFixture(t, f, o, a, p)
			r := DiagnoseRestoreRecords(context.Background(), f, o.ID)
			if r.Status != "inconsistent" || r.Reason != "binding_mismatch" || r.ObservedGeneration != 0 {
				t.Fatalf("binding drift accepted: %+v", r)
			}
		})
	}
}

func TestDiagnosticMissingMalformedAndSensitiveErrors(t *testing.T) {
	for _, name := range []string{"missing_state", "missing_attempt", "missing_publication", "duplicate", "wrong_id", "secret_error"} {
		t.Run(name, func(t *testing.T) {
			f, o, _, _ := diagnosticFixture(t)
			base := "operations/" + o.ID + "/"
			want := "record_invalid_or_unavailable"
			switch name {
			case "missing_state":
				delete(f.data, base+"state.json")
				want = "record_missing"
			case "missing_attempt":
				delete(f.data, base+"attempt.json")
				want = "record_missing"
			case "missing_publication":
				delete(f.data, base+"publication.json")
				want = "record_missing"
			case "duplicate":
				f.data[base+"publication.json"] = bytes.Replace(f.data[base+"publication.json"], []byte(`"publication_version":1`), []byte(`"publication_version":1,"publication_version":1`), 1)
			case "wrong_id":
				f.data[base+"attempt.json"] = bytes.Replace(f.data[base+"attempt.json"], []byte(`"operation_id":"`+o.ID+`"`), []byte(`"operation_id":"00000000-0000-4000-8000-000000000099"`), 1)
			case "secret_error":
				f.err = errors.New("password=TOP_SECRET path=D:/private")
			}
			r := DiagnoseRestoreRecords(context.Background(), f, o.ID)
			if r.Status != "unavailable" || r.Reason != want || r.ObservedGeneration != 0 {
				t.Fatalf("bad rejection: %+v", r)
			}
			data, _ := json.Marshal(r)
			if bytes.Contains(data, []byte("TOP_SECRET")) || bytes.Contains(data, []byte("private")) {
				t.Fatal("raw error exposed")
			}
		})
	}
}

func TestDiagnosticChangesDuringObservation(t *testing.T) {
	for _, name := range []string{"state", "attempt", "publication", "removed", "canceled_context"} {
		t.Run(name, func(t *testing.T) {
			f, o, _, _ := diagnosticFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.hook = func(f *diagnosticReader) {
				if f.reads != 4 {
					return
				}
				base := "operations/" + o.ID + "/"
				switch name {
				case "state":
					f.data[base+"state.json"] = append(f.data[base+"state.json"], ' ')
				case "attempt":
					f.data[base+"attempt.json"] = append(f.data[base+"attempt.json"], ' ')
				case "publication":
					f.data[base+"publication.json"] = append(f.data[base+"publication.json"], ' ')
				case "removed":
					delete(f.data, base+"publication.json")
				case "canceled_context":
					cancel()
				}
			}
			r := DiagnoseRestoreRecords(ctx, f, o.ID)
			if r.Status != "changed" || r.Reason != "snapshot_changed" || r.ObservedGeneration != 0 {
				t.Fatalf("change accepted: %+v", r)
			}
		})
	}
}

func TestDiagnosticInvalidInputsAndWriteAdapter(t *testing.T) {
	f, o, _, _ := diagnosticFixture(t)
	for _, id := range []string{"../secret", "", strings.ToUpper(o.ID) + "/x"} {
		r := DiagnoseRestoreRecords(context.Background(), f, id)
		if r.OperationID != "" || r.Reason != "invalid_input" || f.reads != 0 {
			t.Fatal("invalid ID read or reflected")
		}
	}
	if r := DiagnoseRestoreRecords(context.Background(), nil, o.ID); r.Reason != "invalid_input" {
		t.Fatal(r)
	}
	if r := DiagnoseRestoreRecords(nil, f, o.ID); r.Reason != "invalid_input" {
		t.Fatal(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := DiagnoseRestoreRecords(ctx, f, o.ID)
	if r.Status != "unavailable" || f.reads != 0 {
		t.Fatal(r)
	}
	adapter := diagnosticFiles{f}
	if !errors.Is(adapter.CreateOnly(context.Background(), "x", nil, 0), storage.ErrCapability) || !errors.Is(adapter.CompareAndSwap(context.Background(), "x", "", nil), storage.ErrCapability) {
		t.Fatal("write adapter did not reject")
	}
}
