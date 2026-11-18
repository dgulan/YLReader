package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func ledgerFixture(t *testing.T) (*memoryFiles, Plan, func() time.Time) {
	t.Helper()
	prepared, files, g, proof, transport, p, clock := dispatchFixture(t)
	if _, e := dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	return files, p, clock
}
func TestBehaviorLedgerClaimsAreNotRevocationProof(t *testing.T) {
	files, p, clock := ledgerFixture(t)
	before, _ := json.Marshal(files.data)
	d := DiagnoseBehaviorLedger(context.Background(), files, p.OperationID, clock())
	if d.Status != "responses_observed" || d.Known != 1 || d.LogoutClaims != 1 || d.Dispatches != 1 || d.Persistence != "not_inferred" || d.Dispatch || d.Retry || d.Deployment {
		t.Fatal("incorrect response claims")
	}
	after, _ := json.Marshal(files.data)
	if !bytes.Equal(before, after) {
		t.Fatal("diagnostic wrote records")
	}
	raw, _ := json.Marshal(d)
	if bytes.Contains(raw, []byte("session_id")) || bytes.Contains(raw, []byte("user_id")) {
		t.Fatal("session inventory exposed")
	}
}
func TestBehaviorLedgerRejectsBrokenChainsAndUncertainty(t *testing.T) {
	for _, name := range []string{"absent", "missing_intent", "partial", "orphan_logout", "unknown_response", "wrong_parent", "wrong_user", "wrong_index", "wrong_kind", "future", "backwards", "extra_slot", "duplicate_json", "unknown_json", "oversized", "expired", "state_drift", "read_drift", "denied", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			files, p, clock := ledgerFixture(t)
			base := fmt.Sprintf("operations/%s/behavior-actions/0/", p.OperationID)
			intentKey, _ := behaviorIntentKey(p.OperationID)
			expected := "inconsistent"
			when := clock()
			var reader RestoreRecordReader = files
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			change := func(file string, mutate func(*behaviorAction)) {
				var a behaviorAction
				json.Unmarshal(files.data[base+file], &a)
				mutate(&a)
				files.data[base+file], _ = json.Marshal(a)
			}
			switch name {
			case "absent":
				delete(files.data, intentKey)
				for _, f := range []string{"dispatch.json", "response.json", "logout.json"} {
					delete(files.data, base+f)
				}
				expected = "intent_not_observed"
			case "missing_intent":
				delete(files.data, intentKey)
			case "partial":
				delete(files.data, base+"logout.json")
				expected = "outcome_unknown"
			case "orphan_logout":
				delete(files.data, base+"response.json")
				expected = "outcome_unknown"
			case "unknown_response":
				change("response.json", func(a *behaviorAction) { a.Kind = "login_response_unknown"; a.SessionID = "" })
				delete(files.data, base+"logout.json")
				expected = "outcome_unknown"
			case "wrong_parent":
				change("logout.json", func(a *behaviorAction) { a.ParentSHA = strings.Repeat("f", 64) })
			case "wrong_user":
				change("response.json", func(a *behaviorAction) { a.UserID = uuid.NewString() })
			case "wrong_index":
				change("response.json", func(a *behaviorAction) { a.Index = 1 })
			case "wrong_kind":
				change("response.json", func(a *behaviorAction) { a.Kind = "logout_response_observed" })
			case "future":
				change("logout.json", func(a *behaviorAction) { a.Observed = when.Add(time.Second) })
			case "backwards":
				change("response.json", func(a *behaviorAction) { a.Observed = when.Add(-time.Second) })
			case "extra_slot":
				files.data[fmt.Sprintf("operations/%s/behavior-actions/7/dispatch.json", p.OperationID)] = files.data[base+"dispatch.json"]
				expected = "unavailable"
			case "duplicate_json":
				files.data[base+"dispatch.json"] = append([]byte(`{"index":0,`), files.data[base+"dispatch.json"][1:]...)
				expected = "unavailable"
			case "unknown_json":
				files.data[base+"dispatch.json"] = append([]byte(`{"SECRET":true,`), files.data[base+"dispatch.json"][1:]...)
				expected = "unavailable"
			case "oversized":
				files.data[base+"dispatch.json"] = bytes.Repeat([]byte("x"), 8193)
				expected = "unavailable"
			case "expired":
				when = when.Add(time.Hour)
				expected = "stale"
			case "state_drift":
				state := "operations/" + p.OperationID + "/state.json"
				files.data[state] = append(files.data[state], ' ')
				expected = "stale"
			case "read_drift":
				reader = &diagnosticReader{data: files.data, hook: func(f *diagnosticReader) {
					if f.reads == 35 {
						f.data[base+"logout.json"] = append(f.data[base+"logout.json"], ' ')
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
			d := DiagnoseBehaviorLedger(ctx, reader, p.OperationID, when)
			if d.Status != expected || d.Dispatch || d.Retry || d.Deployment || d.Persistence != "not_inferred" {
				t.Fatalf("got %s want %s", d.Status, expected)
			}
			if name == "orphan_logout" && (d.Known != 0 || d.Reason != "response_parent_unconfirmed") {
				t.Fatal("orphan logout became trusted session")
			}
		})
	}
}

func TestBehaviorSessionObservationRejectsBeforeDatabase(t *testing.T) {
	files, p, clock := ledgerFixture(t)
	for _, raw := range []string{"SECRET", "postgres://reader:@10.0.0.1:5432/yreader_a1_iso_denied?sslmode=disable"} {
		o := observeBehaviorSessions(context.Background(), files, p, raw, databaseIdentityPin{SystemID: "1", DatabaseOID: 1, RoleOID: 1}, databaseIdentityPin{SystemID: "1", DatabaseOID: 2, RoleOID: 1}, clock())
		if o.Database != "unavailable" || o.Known != 0 || o.Dispatch || o.Retry || o.Deployment {
			t.Fatal("invalid target granted observation")
		}
	}
}

func TestBehaviorLedgerRejectsDuplicateAndSkippedAccounts(t *testing.T) {
	for _, mode := range []string{"duplicate_session", "skipped_account"} {
		t.Run(mode, func(t *testing.T) {
			prepared, files, g, proof, transport, p, clock := dispatchFixture(t)
			id := uuid.New()
			prepared.consent.Accounts = append(append([]behaviorConsentAccount(nil), prepared.consent.Accounts...), behaviorConsentAccount{id.String(), "second.json"})
			credential := prepared.accounts[0]
			credential.UserID = id.String()
			prepared.accounts = append(prepared.accounts, credential)
			proof.users = append(proof.users, id)
			if _, e := dispatchBehaviorCore(context.Background(), files, g, p, prepared, proof, transport, clock, func(context.Context) error { return nil }); e != nil {
				t.Fatal(e)
			}
			prefix := fmt.Sprintf("operations/%s/behavior-actions/", p.OperationID)
			if mode == "skipped_account" {
				delete(files.data, prefix+"0/logout.json")
			} else {
				var first, second, logout behaviorAction
				json.Unmarshal(files.data[prefix+"0/response.json"], &first)
				json.Unmarshal(files.data[prefix+"1/response.json"], &second)
				second.SessionID = first.SessionID
				files.data[prefix+"1/response.json"], _ = json.Marshal(second)
				json.Unmarshal(files.data[prefix+"1/logout.json"], &logout)
				logout.SessionID = first.SessionID
				logout.ParentSHA = digestBytes(files.data[prefix+"1/response.json"])
				files.data[prefix+"1/logout.json"], _ = json.Marshal(logout)
			}
			if d := DiagnoseBehaviorLedger(context.Background(), files, p.OperationID, clock()); d.Status != "inconsistent" || d.Known != 0 || d.Dispatch || d.Retry {
				t.Fatal("invalid multi-account chain trusted")
			}
		})
	}
}
