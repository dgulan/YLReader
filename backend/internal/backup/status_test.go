package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func TestRestoreStatusRegistrationObservations(t *testing.T) {
	for _, tc := range []struct{ mode, status, reason, chain string }{{"none", "review_only", "report_visible_unregistered", "not_applicable"}, {"prepared_ack", "registration_pending", "prepared_base_visible", "base_observed"}, {"state_ack", "registration_pending", "prepared_next_visible", "next_observed"}, {"receipt_ack", "observed_registered", "registered_record_visible", "next_observed"}, {"success", "observed_registered", "registered_record_visible", "next_observed"}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := &memoryFiles{data: map[string][]byte{}}
			p, r, clock := registrationFixture(t, f)
			if tc.mode != "none" {
				var files storage.ConditionalFiles = f
				if tc.mode != "success" {
					files = &registrationFaultFiles{ConditionalFiles: f, mode: tc.mode}
				}
				_, e := registerIsolatedReview(context.Background(), files, p, r, clock)
				if (tc.mode == "success") != (e == nil) {
					t.Fatal("invalid fixture", e)
				}
			}
			before := map[string][]byte{}
			for k, v := range f.data {
				before[k] = append([]byte(nil), v...)
			}
			// This reader has Get only; the observer cannot request writes.
			seen := DiagnoseRestoreStatus(context.Background(), &diagnosticReader{data: f.data}, p.OperationID, clock())
			if seen.Status != tc.status || seen.Reason != tc.reason || seen.Chain != tc.chain || seen.Persistence != "not_inferred" || seen.Ready || seen.Deployment {
				t.Fatalf("wrong observation: %+v", seen)
			}
			if !reflect.DeepEqual(before, f.data) {
				t.Fatal("status wrote files")
			}
		})
	}
}

func TestRestoreStatusBlocksStalePartialAndDrift(t *testing.T) {
	for _, name := range []string{"expired", "partial", "drift", "registration_without_report", "report_binding", "cancel", "future"} {
		t.Run(name, func(t *testing.T) {
			f := &memoryFiles{data: map[string][]byte{}}
			p, r, clock := registrationFixture(t, f)
			now := clock()
			want := "blocked"
			if name == "expired" || name == "drift" || name == "registration_without_report" {
				if _, e := registerIsolatedReview(context.Background(), f, p, r, clock); e != nil {
					t.Fatal(e)
				}
			}
			switch name {
			case "expired":
				now = r.Expires
			case "future":
				now = r.Finished.Add(-time.Microsecond)
			case "partial":
				r.Checks[0].Status = "unavailable"
				k, _ := handoverReviewKey(p.OperationID)
				f.data[k], _ = json.Marshal(r)
			case "drift":
				k, _ := operationKey(p.OperationID)
				var op Operation
				json.Unmarshal(f.data[k], &op)
				op = advance(op, "validating")
				f.data[k], _ = json.Marshal(op)
			case "registration_without_report":
				k, _ := handoverReviewKey(p.OperationID)
				delete(f.data, k)
				want = "inconsistent"
			case "report_binding":
				k, _ := handoverReviewKey(p.OperationID)
				r.PackageSHA = strings.Repeat("9", 64)
				f.data[k], _ = json.Marshal(r)
				want = "inconsistent"
			case "cancel":
				k, _ := operationKey(p.OperationID)
				var op Operation
				json.Unmarshal(f.data[k], &op)
				op.CancelRequested = true
				f.data[k], _ = json.Marshal(op)
				k, _ = handoverReviewKey(p.OperationID)
				delete(f.data, k)
			}
			out := DiagnoseRestoreStatus(context.Background(), f, p.OperationID, now)
			if out.Status != want || out.Ready || out.Deployment {
				t.Fatalf("unsafe status: %+v", out)
			}
		})
	}
}

func TestRestoreStatusMalformedAndChangingRecords(t *testing.T) {
	for _, name := range []string{"duplicate", "missing_state", "changed", "optional_disappears"} {
		t.Run(name, func(t *testing.T) {
			f := &memoryFiles{data: map[string][]byte{}}
			p, _, clock := registrationFixture(t, f)
			reader := &diagnosticReader{data: f.data}
			want := "unavailable"
			switch name {
			case "duplicate":
				key, _ := handoverReviewKey(p.OperationID)
				f.data[key] = []byte(`{"report_version":1,"report_version":1}`)
			case "missing_state":
				key, _ := operationKey(p.OperationID)
				delete(f.data, key)
			case "changed":
				want = "changed"
				reader.hook = func(d *diagnosticReader) {
					if d.reads == 6 {
						key, _ := operationKey(p.OperationID)
						d.data[key] = append(d.data[key], ' ')
					}
				}
			case "optional_disappears":
				want = "changed"
				reader.hook = func(d *diagnosticReader) {
					if d.reads == 6 {
						key, _ := handoverReviewKey(p.OperationID)
						delete(d.data, key)
					}
				}
			}
			out := DiagnoseRestoreStatus(context.Background(), reader, p.OperationID, clock())
			if out.Status != want || out.Generation != 0 {
				t.Fatalf("bad rejection: %+v", out)
			}
		})
	}
}

type optionalDeniedReader struct{ RestoreRecordReader }

func (r optionalDeniedReader) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if strings.HasSuffix(key, "handover-review.json") {
		return nil, errors.New("SECRET D:/private")
	}
	return r.RestoreRecordReader.Get(ctx, key)
}

func TestRestoreStatusDoesNotInferMissingOrLeakInputs(t *testing.T) {
	f := &memoryFiles{data: map[string][]byte{}}
	p, _, clock := registrationFixture(t, f)
	out := DiagnoseRestoreStatus(context.Background(), optionalDeniedReader{f}, p.OperationID, clock())
	if out.Status != "unavailable" || out.ReportClaims != "not_observed" {
		t.Fatal("read error treated as absent")
	}
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "private") {
		t.Fatal("raw error leaked")
	}
	out = DiagnoseRestoreStatus(context.Background(), f, "../SECRET", clock())
	if out.OperationID != "" || out.Reason != "invalid_input" {
		t.Fatal("invalid operation reflected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if DiagnoseRestoreStatus(ctx, f, p.OperationID, clock()).Status != "unavailable" {
		t.Fatal("canceled status succeeded")
	}
	key, _ := handoverReviewKey(p.OperationID)
	delete(f.data, key)
	out = DiagnoseRestoreStatus(context.Background(), f, p.OperationID, clock())
	if out.Status != "records_only" || out.ReportClaims != "absent" || out.Registration != "absent" {
		t.Fatal(out)
	}
}
