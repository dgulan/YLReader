package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func consentFixture(t *testing.T) (behaviorConsent, *diagnosticReader, *diagnosticReader, Plan, time.Time) {
	t.Helper()
	_, p, s, now := handoverFixture(t)
	control := &diagnosticReader{data: map[string][]byte{}}
	writeDiagnosticFixture(t, control, s.op, s.attempt, s.pub)
	a := behaviorConsent{Format: "ylreader-behavior-consent", Version: 1, Scope: "isolated-account-verification-only", ID: "80000000-0000-4000-8000-000000000001", OperationID: p.OperationID, OwnerID: s.op.OwnerID, PlanDigest: p.Digest, TargetID: p.TargetID, Generation: s.op.Generation, OperationSHA: s.tokens[0], AttemptSHA: s.tokens[1], PublicationSHA: s.tokens[2], RuntimeSHA: strings.Repeat("6", 64), Issued: now.Add(-time.Second), Expires: now.Add(time.Minute), Accounts: []behaviorConsentAccount{{"90000000-0000-4000-8000-000000000001", "account.json"}}}
	encode := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	secret := map[string]string{"user_id": a.Accounts[0].UserID, "username": "原账号", "password": " SECRET-password 中文 ", "old_access_token": "SECRET-old-access", "old_refresh_token": strings.Repeat("a", 64)}
	input := &diagnosticReader{data: map[string][]byte{"consent.json": encode(a), "account.json": encode(secret)}}
	return a, input, control, p, now
}

func TestBehaviorConsentReadOnlyPrivateInput(t *testing.T) {
	a, input, control, p, now := consentFixture(t)
	beforeInput, _ := json.Marshal(input.data)
	beforeControl, _ := json.Marshal(control.data)
	prepared, e := prepareBehaviorInput(context.Background(), input, control, "consent.json", p, a.RuntimeSHA, func() time.Time { return now })
	if e != nil {
		t.Fatal(e)
	}
	if len(prepared.accounts) != 1 || prepared.accounts[0].Password != " SECRET-password 中文 " || prepared.consent.ID != a.ID {
		t.Fatal("credential changed")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		for _, v := range []any{prepared, *prepared, prepared.accounts[0]} {
			if text := fmt.Sprintf(format, v); strings.Contains(text, "SECRET") || strings.Contains(text, "原账号") || strings.Contains(text, strings.Repeat("a", 64)) {
				t.Fatal("credential formatting leaked")
			}
		}
	}
	for _, v := range []any{prepared, *prepared, prepared.accounts[0]} {
		if raw, e := json.Marshal(v); e == nil || bytes.Contains(raw, []byte("SECRET")) {
			t.Fatal("credential serialization allowed")
		}
	}
	afterInput, _ := json.Marshal(input.data)
	afterControl, _ := json.Marshal(control.data)
	if !bytes.Equal(beforeInput, afterInput) || !bytes.Equal(beforeControl, afterControl) {
		t.Fatal("read-only preflight wrote records")
	}
	prepared.Close()
	prepared.Close()
	if len(prepared.accounts) != 0 || !reflect.DeepEqual(prepared.consent, behaviorConsent{}) {
		t.Fatal("references retained after close")
	}
}

func TestBehaviorConsentBindingsAndWindow(t *testing.T) {
	for _, name := range []string{"scope", "operation", "owner", "target", "generation", "plan", "state", "attempt", "publication", "runtime", "expired", "future", "too_long", "before_plan", "duplicate_user", "duplicate_key", "traversal", "self_reference", "missing_accounts", "cancelled_operation"} {
		t.Run(name, func(t *testing.T) {
			a, input, control, p, now := consentFixture(t)
			switch name {
			case "scope":
				a.Scope = "production"
			case "operation":
				a.OperationID = a.ID
			case "owner":
				a.OwnerID = a.ID
			case "target":
				a.TargetID = a.ID
			case "generation":
				a.Generation++
			case "plan":
				a.PlanDigest = strings.Repeat("b", 64)
			case "state":
				a.OperationSHA = strings.Repeat("b", 64)
			case "attempt":
				a.AttemptSHA = strings.Repeat("b", 64)
			case "publication":
				a.PublicationSHA = strings.Repeat("b", 64)
			case "runtime":
				a.RuntimeSHA = strings.Repeat("b", 64)
			case "expired":
				a.Expires = now
			case "future":
				a.Issued = now.Add(time.Second)
			case "too_long":
				a.Expires = a.Issued.Add(5*time.Minute + time.Microsecond)
			case "before_plan":
				a.Issued = now.Add(-time.Hour)
			case "duplicate_user":
				a.Accounts = append(a.Accounts, behaviorConsentAccount{a.Accounts[0].UserID, "other.json"})
			case "duplicate_key":
				a.Accounts = append(a.Accounts, behaviorConsentAccount{a.ID, a.Accounts[0].CredentialKey})
			case "traversal":
				a.Accounts[0].CredentialKey = "../SECRET"
			case "self_reference":
				a.Accounts[0].CredentialKey = "consent.json"
			case "missing_accounts":
				a.Accounts = nil
			case "cancelled_operation":
				key := "operations/" + p.OperationID + "/state.json"
				control.data[key] = bytes.Replace(control.data[key], []byte(`"cancel_requested":false`), []byte(`"cancel_requested":true`), 1)
			}
			input.data["consent.json"], _ = json.Marshal(a)
			result, e := prepareBehaviorInput(context.Background(), input, control, "consent.json", p, strings.Repeat("6", 64), func() time.Time { return now })
			if e != errBehaviorConsent || result != nil {
				t.Fatal("unsafe consent accepted")
			}
		})
	}
}

func TestBehaviorConsentStrictSecretsAndReadFailures(t *testing.T) {
	for _, name := range []string{"unknown_consent", "duplicate_consent", "null_consent", "oversized_consent", "unknown_secret", "duplicate_secret", "null_secret", "oversized_secret", "user_mismatch", "bad_refresh", "empty_password", "oversized_password", "credential_drift", "consent_drift", "record_drift", "expiry_during_read", "cancel", "close_failure", "storage_error"} {
		t.Run(name, func(t *testing.T) {
			a, input, control, p, now := consentFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var reader RestoreRecordReader = input
			calls := 0
			clock := func() time.Time {
				calls++
				if name == "expiry_during_read" && calls > 1 {
					return a.Expires
				}
				return now
			}
			switch name {
			case "unknown_consent":
				input.data["consent.json"] = append([]byte(`{"SECRET":true,`), input.data["consent.json"][1:]...)
			case "duplicate_consent":
				input.data["consent.json"] = append([]byte(`{"consent_version":1,`), input.data["consent.json"][1:]...)
			case "null_consent":
				input.data["consent.json"] = []byte("null")
			case "oversized_consent":
				input.data["consent.json"] = bytes.Repeat([]byte(" "), 16385)
			case "unknown_secret":
				input.data["account.json"] = append([]byte(`{"SECRET":true,`), input.data["account.json"][1:]...)
			case "duplicate_secret":
				input.data["account.json"] = append([]byte(`{"password":"SECRET",`), input.data["account.json"][1:]...)
			case "null_secret":
				input.data["account.json"] = []byte("null")
			case "oversized_secret":
				input.data["account.json"] = bytes.Repeat([]byte("x"), 32769)
			case "user_mismatch", "bad_refresh", "empty_password", "oversized_password":
				var secret map[string]string
				json.Unmarshal(input.data["account.json"], &secret)
				if name == "user_mismatch" {
					secret["user_id"] = a.ID
				}
				if name == "bad_refresh" {
					secret["old_refresh_token"] = strings.Repeat("Z", 64)
				}
				if name == "empty_password" {
					secret["password"] = ""
				}
				if name == "oversized_password" {
					secret["password"] = strings.Repeat("x", 4097)
				}
				input.data["account.json"], _ = json.Marshal(secret)
			case "credential_drift":
				input.hook = func(f *diagnosticReader) {
					if f.reads == 3 {
						f.data["account.json"] = append(f.data["account.json"], ' ')
					}
				}
			case "consent_drift":
				input.hook = func(f *diagnosticReader) {
					if f.reads == 4 {
						f.data["consent.json"] = append(f.data["consent.json"], ' ')
					}
				}
			case "record_drift":
				control.hook = func(f *diagnosticReader) {
					if f.reads == 7 {
						key := "operations/" + p.OperationID + "/state.json"
						f.data[key] = append(f.data[key], ' ')
					}
				}
			case "cancel":
				input.hook = func(f *diagnosticReader) {
					if f.reads == 3 {
						cancel()
					}
				}
			case "close_failure":
				reader = consentCloseFailure{input}
			case "storage_error":
				input.err = errors.New("SECRET storage failure")
			}
			prepared, e := prepareBehaviorInput(ctx, reader, control, "consent.json", p, strings.Repeat("6", 64), clock)
			if prepared != nil || e != errBehaviorConsent || strings.Contains(e.Error(), "SECRET") {
				t.Fatal("invalid or uncertain input accepted")
			}
		})
	}
}

type consentCloseFailure struct{ RestoreRecordReader }
type consentBadClose struct{ io.ReadCloser }

func (consentBadClose) Close() error { return errors.New("SECRET close failure") }
func (r consentCloseFailure) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	f, e := r.RestoreRecordReader.Get(ctx, key)
	if e != nil {
		return nil, e
	}
	return consentBadClose{f}, nil
}
