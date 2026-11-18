package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/backup"
	"yreader/backend/internal/pkg/storage"
)

func behaviorCommandFixture() (*commandReader, string, func() time.Time) {
	r, id, clock := commandFixture()
	root := "operations/" + id + "/"
	encode := func(v any) []byte { b, _ := json.Marshal(v); return b }
	hash := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	var state map[string]any
	json.Unmarshal(r.data[root+"state.json"], &state)
	user, session, consent := "00000000-0000-4000-8000-000000000011", "00000000-0000-4000-8000-000000000012", "00000000-0000-4000-8000-000000000013"
	stamp := func(t time.Time) string { return t.Format(time.RFC3339Nano) }
	intent := map[string]any{"intent_format": "ylreader-behavior-intent", "intent_version": 1, "scope": "isolated-login-intent-only", "state": "dispatch_reserved", "consent_id": consent, "operation_id": id, "owner_id": state["owner_id"], "plan_digest": state["plan_digest"], "target_instance_id": user, "observed_generation": state["generation"], "operation_record_sha256": hash(r.data[root+"state.json"]), "attempt_record_sha256": hash(r.data[root+"attempt.json"]), "publication_record_sha256": hash(r.data[root+"publication.json"]), "runtime_config_revision_sha256": strings.Repeat("6", 64), "user_ids": []string{user}, "issued_at": stamp(clock().Add(-time.Second)), "expires_at": stamp(clock().Add(time.Minute)), "recorded_at": stamp(clock())}
	r.data[root+"behavior-intent.json"] = encode(intent)
	intentSHA := hash(r.data[root+"behavior-intent.json"])
	a := map[string]any{"action_format": "ylreader-behavior-action", "action_version": 1, "operation_id": id, "consent_id": consent, "intent_sha256": intentSHA, "index": 0, "user_id": user, "kind": "dispatch_reserved", "session_id": "", "parent_sha256": intentSHA, "observed_at": stamp(clock())}
	base := root + "behavior-actions/0/"
	r.data[base+"dispatch.json"] = encode(a)
	a["kind"] = "session_observed"
	a["session_id"] = session
	a["parent_sha256"] = hash(r.data[base+"dispatch.json"])
	r.data[base+"response.json"] = encode(a)
	a["kind"] = "logout_response_observed"
	a["parent_sha256"] = hash(r.data[base+"response.json"])
	r.data[base+"logout.json"] = encode(a)
	return r, id, clock
}

func TestBehaviorStatusCommandJSONTextAttention(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			r, id, clock := behaviorCommandFixture()
			var out, errs bytes.Buffer
			code := run(context.Background(), []string{"behavior-status", "--control-root", t.TempDir(), "--operation", id, "--format", format}, &out, &errs, func(string) (controlReader, error) { return r, nil }, clock)
			if code != 0 || !r.closed || errs.Len() != 0 {
				t.Fatal("behavior status failed")
			}
			if format == "json" {
				var d backup.BehaviorLedgerDiagnostic
				if json.Unmarshal(out.Bytes(), &d) != nil || d.Status != "responses_observed" || d.Persistence != "not_inferred" || d.Dispatch || d.Retry || d.Deployment {
					t.Fatal("unsafe ledger output")
				}
			} else if !strings.Contains(out.String(), "数据库撤销: 未核验") || !strings.Contains(out.String(), "允许派发/重试/部署: 否") {
				t.Fatal("text implies execution or revocation")
			}
		})
	}
	r, id, clock := behaviorCommandFixture()
	delete(r.data, "operations/"+id+"/behavior-actions/0/response.json")
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"behavior-status", "--control-root", t.TempDir(), "--operation", id}, &out, &errs, func(string) (controlReader, error) { return r, nil }, clock); code != 4 || !bytes.Contains(out.Bytes(), []byte("response_parent_unconfirmed")) {
		t.Fatal("unknown parent granted success")
	}
}

func TestBehaviorStatusRejectsArgumentsAndPrivateErrors(t *testing.T) {
	r, id, clock := behaviorCommandFixture()
	root := t.TempDir()
	for _, args := range [][]string{{"behavior-status"}, {"behavior-status", "--control-root", "relative", "--operation", id}, {"behavior-status", "--control-root", root, "--operation", "../SECRET"}, {"behavior-status", "--control-root", root, "--operation", id, "--dsn", "SECRET"}, {"behavior-status", "--control-root", root, "--control-root", root, "--operation", id}, {"behavior-status", "--control-root", root, "--operation", id, "--format", "SECRET"}} {
		called := false
		var out, errs bytes.Buffer
		if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { called = true; return r, nil }, clock); code != 2 || called || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
			t.Fatal("invalid arguments opened storage or leaked")
		}
	}
	args := []string{"behavior-status", "--control-root", root, "--operation", id}
	var out, errs bytes.Buffer
	if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return nil, errors.New("SECRET") }, clock); code != 3 || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("open error exposed")
	}
	r.closeErr = errors.New("SECRET")
	errs.Reset()
	if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return r, nil }, clock); code != 5 || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("close failure emitted result")
	}
	r.closeErr = nil
	errs.Reset()
	if code := run(context.Background(), args, failedOutput{}, &errs, func(string) (controlReader, error) { return r, nil }, clock); code != 5 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("output failure ignored or exposed")
	}
}

func TestBehaviorStatusNativeUnsupported(t *testing.T) {
	if runtime.GOOS == "linux" {
		return
	}
	_, id, clock := behaviorCommandFixture()
	var out, errs bytes.Buffer
	root := filepath.Clean(t.TempDir())
	if code := run(context.Background(), []string{"behavior-status", "--control-root", root, "--operation", id}, &out, &errs, func(s string) (controlReader, error) { return storage.OpenBackupFiles(s) }, clock); code != 3 || out.Len() != 0 {
		t.Fatal("unsupported native driver silently downgraded")
	}
}
