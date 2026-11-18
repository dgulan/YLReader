package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

type commandReader struct {
	data     map[string][]byte
	closed   bool
	closeErr error
}

func (r *commandReader) Get(_ context.Context, k string) (io.ReadCloser, error) {
	data, ok := r.data[k]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (r *commandReader) Close() error { r.closed = true; return r.closeErr }

func commandFixture() (*commandReader, string, func() time.Time) {
	id, backupID, owner := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003"
	h := func(c string) string { return strings.Repeat(c, 64) }
	encode := func(v any) []byte { data, _ := json.Marshal(v); return data }
	sha := func(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
	stamp := func(n int) string { return time.Date(2026, 11, 16, 0, 0, n, 0, time.UTC).Format(time.RFC3339) }
	base := map[string]any{"operation_format": "ylreader-restore-operation", "operation_version": 1, "operation_id": id, "backup_id": backupID, "owner_id": owner, "plan_digest": h("a"), "generation": 3, "phase": "validating", "created_at": stamp(0), "updated_at": stamp(1), "heartbeat_at": stamp(1), "finished_at": nil, "failure_code": nil, "cancel_requested": false}
	baseSHA := sha(encode(base))
	base["generation"] = 4
	base["updated_at"] = stamp(4)
	base["heartbeat_at"] = stamp(4)
	state := encode(base)
	attempt := encode(map[string]any{"attempt_format": "ylreader-restore-attempt", "attempt_version": 1, "operation_id": id, "backup_id": backupID, "owner_id": owner, "plan_digest": h("a"), "target_storage_fingerprint": h("d"), "generation_reference": ".scratch-" + strings.Repeat("a", 32), "database_outcome": "committed"})
	pub := encode(map[string]any{"publication_format": "ylreader-object-publication", "publication_version": 1, "operation_id": id, "backup_id": backupID, "owner_id": owner, "plan_digest": h("a"), "package_sha256": h("b"), "manifest_sha256": h("c"), "target_storage_fingerprint": h("d"), "staging_reference": ".scratch-" + strings.Repeat("a", 32), "pending_reference": ".publish-" + strings.ReplaceAll(id, "-", ""), "output_reference": "objects-" + id, "tree_fingerprint": h("e"), "state": "verified"})
	checks := []map[string]string{}
	for _, name := range strings.Fields("schema_ledger database_identity database_role published_objects header_keyring jwt_rotation redis_isolation sessions_revoked tasks_disabled runtime_behavior") {
		checks = append(checks, map[string]string{"id": name, "status": "pass"})
	}
	report := encode(map[string]any{"report_format": "ylreader-handover-review", "report_version": 1, "scope": "isolated-localfs-handover-review", "operation_id": id, "backup_id": backupID, "plan_digest": h("a"), "package_sha256": h("b"), "manifest_sha256": h("c"), "target_instance_id": owner, "target_storage_fingerprint": h("d"), "publication_tree_fingerprint": h("e"), "observed_generation": 3, "operation_record_sha256": baseSHA, "attempt_record_sha256": sha(attempt), "publication_record_sha256": sha(pub), "target_identity": map[string]string{"database_identity_sha256": h("1"), "runtime_config_revision_sha256": h("2"), "redis_identity_sha256": h("3")}, "started_at": stamp(2), "finished_at": stamp(3), "expires_at": "2026-11-16T00:01:00Z", "checks": checks, "ready_for_cutover": false, "deployment_authorized": false})
	registration := encode(map[string]any{"registration_format": "ylreader-handover-registration", "registration_version": 1, "scope": "isolated-review-registration", "operation_id": id, "backup_id": backupID, "owner_id": owner, "plan_digest": h("a"), "report_sha256": sha(report), "base_state_sha256": baseSHA, "base_generation": 3, "next_state_sha256": sha(state), "next_generation": 4, "recorded_at": stamp(4), "state": "registered", "ready_for_cutover": false, "deployment_authorized": false})
	root := "operations/" + id + "/"
	return &commandReader{data: map[string][]byte{root + "state.json": state, root + "attempt.json": attempt, root + "publication.json": pub, root + "handover-review.json": report, root + "handover-registration.json": registration}}, id, func() time.Time { return time.Date(2026, 11, 16, 0, 0, 5, 0, time.UTC) }
}

func TestStatusCommandJSONTextAndAttention(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			reader, id, clock := commandFixture()
			root := filepath.Join(t.TempDir(), "SECRET")
			var out, errs bytes.Buffer
			code := run(context.Background(), []string{"status", "--control-root", root, "--operation", id, "--format", format}, &out, &errs, func(string) (controlReader, error) { return reader, nil }, clock)
			if code != 0 || !reader.closed || errs.Len() != 0 || strings.Contains(out.String(), "SECRET") {
				t.Fatalf("command failed: %d %s", code, errs.String())
			}
			if format == "json" {
				var result map[string]any
				if json.Unmarshal(out.Bytes(), &result) != nil || result["status"] != "observed_registered" || result["persistence_confirmation"] != "not_inferred" || result["ready_for_cutover"] != false {
					t.Fatal("wrong JSON output")
				}
			} else if !strings.Contains(out.String(), "无法从磁盘推断") {
				t.Fatal("text omitted confirmation boundary")
			}
		})
	}
	reader, id, clock := commandFixture()
	delete(reader.data, "operations/"+id+"/handover-registration.json")
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"status", "--control-root", t.TempDir(), "--operation", id}, &out, &errs, func(string) (controlReader, error) { return reader, nil }, clock); code != 4 || out.Len() == 0 {
		t.Fatal("attention status not emitted")
	}
}

func TestStatusCommandBoundBehaviorReviewDisplay(t *testing.T) {
	reader, id, clock := commandFixture()
	root := "operations/" + id + "/"
	delete(reader.data, root+"handover-review.json")
	delete(reader.data, root+"handover-registration.json")
	sha := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	encode := func(v any) []byte { b, _ := json.Marshal(v); return b }
	var state map[string]any
	json.Unmarshal(reader.data[root+"state.json"], &state)
	h := func(c string) string { return strings.Repeat(c, 64) }
	stamp := func(n int) string { return time.Date(2026, 11, 16, 0, 0, n, 0, time.UTC).Format(time.RFC3339) }
	checks := []map[string]string{}
	for _, name := range strings.Fields("schema_ledger database_identity database_role published_objects header_keyring redis_isolation sessions_revoked tasks_disabled credential_preflight https_login_logout old_token_http_rejection reader_roundtrip") {
		status := "pass"
		if name == "reader_roundtrip" {
			status = "unavailable"
		}
		checks = append(checks, map[string]string{"id": name, "status": status})
	}
	bound := map[string]any{
		"report_format": "ylreader-bound-behavior-review", "report_version": 1,
		"profile": "bound-https-account-verification-v1", "scope": "localfs-bound-https-account-review",
		"status": "partial", "operation_id": id, "backup_id": state["backup_id"],
		"consent_id": "00000000-0000-4000-8000-000000000099", "plan_digest": state["plan_digest"],
		"package_sha256": h("b"), "manifest_sha256": h("c"), "target_instance_id": state["owner_id"],
		"target_storage_fingerprint": h("d"), "publication_tree_fingerprint": h("e"),
		"observed_generation": state["generation"], "operation_record_sha256": sha(reader.data[root+"state.json"]),
		"attempt_record_sha256": sha(reader.data[root+"attempt.json"]), "publication_record_sha256": sha(reader.data[root+"publication.json"]),
		"target_identity":        map[string]string{"database_identity_sha256": h("1"), "runtime_config_revision_sha256": h("2"), "redis_identity_sha256": h("3")},
		"tls_certificate_sha256": h("4"), "behavior_ledger_sha256": h("5"),
		"account_count": 1, "session_count": 1,
		"started_at": stamp(4), "finished_at": stamp(4), "expires_at": "2026-11-16T00:01:00Z",
		"checks": checks, "execution_authorized": false, "ready_for_cutover": false, "deployment_authorized": false,
	}
	reader.data[root+"behavior-account-review.json"] = encode(bound)

	var out, errs bytes.Buffer
	code := run(context.Background(), []string{"status", "--control-root", t.TempDir(), "--operation", id, "--format", "text"}, &out, &errs, func(string) (controlReader, error) { return reader, nil }, clock)
	if code != 4 || errs.Len() != 0 || !strings.Contains(out.String(), "待验项清单: reader_roundtrip") || !strings.Contains(out.String(), "允许切换/部署: 否") {
		t.Fatalf("partial bound review text output wrong: code=%d out=%s", code, out.String())
	}

	// Complete & registered bound review -> exit 0, still ready_for_cutover=false
	checks[11]["status"] = "pass"
	bound["status"] = "complete"
	bound["checks"] = checks
	baseSHA := sha(reader.data[root+"state.json"])
	reader.data[root+"behavior-account-review.json"] = encode(bound)
	state["generation"] = 5
	state["updated_at"] = stamp(5)
	state["heartbeat_at"] = stamp(5)
	reader.data[root+"state.json"] = encode(state)
	reg := map[string]any{
		"registration_format": "ylreader-handover-registration", "registration_version": 1,
		"scope": "bound-behavior-review-registration", "operation_id": id,
		"backup_id": state["backup_id"], "owner_id": state["owner_id"], "plan_digest": state["plan_digest"],
		"report_sha256":     sha(reader.data[root+"behavior-account-review.json"]),
		"base_state_sha256": baseSHA, "base_generation": 4,
		"next_state_sha256": sha(reader.data[root+"state.json"]), "next_generation": 5,
		"recorded_at": stamp(5), "state": "registered", "ready_for_cutover": false, "deployment_authorized": false,
	}
	reader.data[root+"handover-registration.json"] = encode(reg)
	out.Reset()
	code = run(context.Background(), []string{"status", "--control-root", t.TempDir(), "--operation", id, "--format", "json"}, &out, &errs, func(string) (controlReader, error) { return reader, nil }, func() time.Time { return time.Date(2026, 11, 16, 0, 0, 6, 0, time.UTC) })
	if code != 0 || errs.Len() != 0 || !strings.Contains(out.String(), `"complete_registered"`) || !strings.Contains(out.String(), `"ready_for_cutover":false`) {
		t.Fatalf("registered bound review status wrong: code=%d out=%s", code, out.String())
	}
}

func TestStatusCommandRejectsArgumentsBeforeOpen(t *testing.T) {
	_, id, clock := commandFixture()
	root := t.TempDir()
	for _, args := range [][]string{nil, {"apply", "--control-root", root, "--operation", id}, {"status", "--control-root", "relative", "--operation", id}, {"status", "--control-root", root, "--operation", "../SECRET"}, {"status", "--control-root", root, "--operation", id, "--format", "SECRET"}, {"status", "--control-root", root, "--control-root", root, "--operation", id}, {"status", "--control-root", root, "--operation", id, "--resume", "SECRET"}} {
		var out, errs bytes.Buffer
		called := false
		code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { called = true; return nil, nil }, clock)
		if code != 2 || called || strings.Contains(errs.String(), "SECRET") || out.Len() != 0 {
			t.Fatal("bad arguments reached opener or leaked")
		}
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, errors.New("SECRET") }

func TestStatusCommandSanitizesStorageAndOutputErrors(t *testing.T) {
	reader, id, clock := commandFixture()
	args := []string{"status", "--control-root", t.TempDir(), "--operation", id}
	var out, errs bytes.Buffer
	if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return nil, errors.New("SECRET path") }, clock); code != 3 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("unsafe storage error exposed")
	}
	errs.Reset()
	if code := run(context.Background(), args, failedOutput{}, &errs, func(string) (controlReader, error) { return reader, nil }, clock); code != 5 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("output error exposed")
	}
	errs.Reset()
	reader.closeErr = errors.New("SECRET")
	if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return reader, nil }, clock); code != 5 || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("close error exposed or partial output")
	}
}

func TestStatusCommandUnsupportedPlatformFailsClosed(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux capability check")
	}
	_, id, clock := commandFixture()
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"status", "--control-root", t.TempDir(), "--operation", id}, &out, &errs, func(root string) (controlReader, error) { return storage.OpenBackupFiles(root) }, clock); code != 3 || out.Len() != 0 {
		t.Fatal("unsupported platform fell back to unsafe file reads")
	}
}
