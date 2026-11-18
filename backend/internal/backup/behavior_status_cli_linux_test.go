//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/storage"
)

func currentBehaviorCLIFixture(t *testing.T, files storage.BackupFiles, when time.Time) Plan {
	t.Helper()
	p, _ := currentStatusFixture(t, files, when)
	ctx := context.Background()
	s, e := stableHandoverSnapshot(ctx, files, p.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	id := uuid.New()
	a := behaviorConsent{Format: "ylreader-behavior-consent", Version: 1, Scope: "isolated-account-verification-only", ID: uuid.NewString(), OperationID: p.OperationID, OwnerID: s.op.OwnerID, PlanDigest: p.Digest, TargetID: p.TargetID, Generation: s.op.Generation, OperationSHA: s.tokens[0], AttemptSHA: s.tokens[1], PublicationSHA: s.tokens[2], RuntimeSHA: strings.Repeat("6", 64), Issued: when, Expires: when.Add(time.Minute), Accounts: []behaviorConsentAccount{{id.String(), "synthetic.json"}}}
	prepared := &preparedBehaviorInput{consent: a, accounts: []behaviorCredential{{UserID: id.String(), Username: "synthetic", Password: "synthetic", OldAccess: "unused", OldRefresh: strings.Repeat("a", 64)}}}
	defer prepared.Close()
	g := &restoreGuard{plan: p}
	proof := &verifiedBehaviorAccounts{guard: g, plan: p.Digest, consent: a.ID, runtime: a.RuntimeSHA, database: strings.Repeat("d", 64), records: s.tokens, users: []uuid.UUID{id}, checked: when, expires: a.Expires}
	transport := &dispatchFakeTransport{binding: behaviorTransportBinding{p.TargetID, proof.database, proof.runtime}}
	if _, e := dispatchBehaviorCore(ctx, files, g, p, prepared, proof, transport, func() time.Time { return when }, func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestBackupBehaviorStatusCLIIsolated(t *testing.T) {
	if os.Getenv("BACKUP_BEHAVIOR_STATUS_CLI") == "" {
		t.Skip("explicit CLI fixture required")
	}
	if os.Getenv("BACKUP_BEHAVIOR_STATUS_CLI") != "/verify/backup-cli" || os.Geteuid() != 10001 {
		t.Fatal("dedicated non-root fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("permissions")
	}
	files, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	p := currentBehaviorCLIFixture(t, files, time.Now().UTC().Truncate(time.Microsecond))
	before := statusFixtureBytes(t, root)
	invoke := func(t *testing.T, target, format string, want int) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "/verify/backup-cli", "behavior-status", "--control-root", target, "--operation", p.OperationID, "--format", format)
		cmd.Env = []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "DATABASE_URL=SECRET-unused", "REDIS_URL=SECRET-unused"}
		var out, errs bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errs
		e := cmd.Run()
		code := 0
		if e != nil {
			exit, ok := e.(*exec.ExitError)
			if !ok {
				t.Fatal("CLI process unavailable")
			}
			code = exit.ExitCode()
		}
		if code != want || strings.Contains(out.String()+errs.String(), target) || strings.Contains(out.String()+errs.String(), "SECRET") {
			t.Fatalf("unexpected code %d want %d", code, want)
		}
		return out.Bytes()
	}
	t.Run("json_response_claims", func(t *testing.T) {
		var d BehaviorLedgerDiagnostic
		if json.Unmarshal(invoke(t, root, "json", 0), &d) != nil || d.Status != "responses_observed" || d.Persistence != "not_inferred" || d.Dispatch || d.Retry || d.Deployment {
			t.Fatal("unsafe response claims")
		}
	})
	t.Run("text_no_database_claim", func(t *testing.T) {
		out := invoke(t, root, "text", 0)
		if !bytes.Contains(out, []byte("数据库撤销: 未核验")) || !bytes.Contains(out, []byte("允许派发/重试/部署: 否")) {
			t.Fatal("unsafe text")
		}
	})
	base := filepath.Join(root, "operations", p.OperationID)
	remove := func(t *testing.T, path string) {
		t.Helper()
		data, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if os.Remove(path) != nil {
			t.Fatal("fixture remove")
		}
		t.Cleanup(func() {
			if os.WriteFile(path, data, 0600) != nil {
				t.Error("fixture restore")
			}
		})
	}
	t.Run("missing_intent_attention", func(t *testing.T) {
		remove(t, filepath.Join(base, "behavior-intent.json"))
		var d BehaviorLedgerDiagnostic
		if json.Unmarshal(invoke(t, root, "json", 4), &d) != nil || d.Status != "inconsistent" {
			t.Fatal("orphan actions accepted")
		}
	})
	t.Run("orphan_logout_attention", func(t *testing.T) {
		remove(t, filepath.Join(base, "behavior-actions", "0", "response.json"))
		var d BehaviorLedgerDiagnostic
		if json.Unmarshal(invoke(t, root, "json", 4), &d) != nil || d.Reason != "response_parent_unconfirmed" || d.Known != 0 {
			t.Fatal("orphan logout became known session")
		}
	})
	t.Run("wrong_parent_attention", func(t *testing.T) {
		path := filepath.Join(base, "behavior-actions", "0", "logout.json")
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var a behaviorAction
		json.Unmarshal(raw, &a)
		a.ParentSHA = strings.Repeat("f", 64)
		changed, _ := json.Marshal(a)
		if os.WriteFile(path, changed, 0600) != nil {
			t.Fatal("fixture parent")
		}
		defer os.WriteFile(path, raw, 0600)
		var d BehaviorLedgerDiagnostic
		if json.Unmarshal(invoke(t, root, "json", 4), &d) != nil || d.Status != "inconsistent" {
			t.Fatal("parent mismatch accepted")
		}
	})
	t.Run("expired_attention", func(t *testing.T) {
		other := t.TempDir()
		os.Chmod(other, 0700)
		old, e := storage.OpenBackupFiles(other)
		if e != nil {
			t.Fatal(e)
		}
		defer old.Close()
		currentBehaviorCLIFixture(t, old, time.Now().UTC().Truncate(time.Microsecond).Add(-10*time.Minute))
		var d BehaviorLedgerDiagnostic
		if json.Unmarshal(invoke(t, other, "json", 4), &d) != nil || d.Status != "stale" {
			t.Fatal("expired records accepted")
		}
	})
	t.Run("symlink_root_denied", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if os.Symlink(root, link) != nil {
			t.Fatal("fixture link")
		}
		if len(invoke(t, link, "json", 3)) != 0 {
			t.Fatal("unsafe root emitted result")
		}
	})
	t.Run("unsafe_permissions_denied", func(t *testing.T) {
		if os.Chmod(root, 0755) != nil {
			t.Fatal("chmod")
		}
		defer os.Chmod(root, 0700)
		if len(invoke(t, root, "json", 3)) != 0 {
			t.Fatal("unsafe permissions emitted result")
		}
	})
	if !reflect.DeepEqual(before, statusFixtureBytes(t, root)) {
		t.Fatal("CLI changed bytes or permissions")
	}
	t.Log(`BACKUP_BEHAVIOR_STATUS_EVIDENCE {"scope":"isolated-behavior-status-cli-fixture","checks":8,"content_and_modes_unchanged":true,"dispatch_authorized":false,"retry_authorized":false,"deployment_authorized":false}`)
}
