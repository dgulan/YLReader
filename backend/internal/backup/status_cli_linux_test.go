//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func currentStatusFixture(t *testing.T, files storage.ConditionalFiles, when time.Time) (Plan, handoverReview) {
	t.Helper()
	r, p, s, oldNow := handoverFixture(t)
	delta := when.Sub(oldNow)
	shift := func(text string) string {
		stamp, e := time.Parse(time.RFC3339Nano, text)
		if e != nil {
			t.Fatal(e)
		}
		return stamp.Add(delta).Format(time.RFC3339Nano)
	}
	p.Created, p.Expires = shift(p.Created), shift(p.Expires)
	var e error
	p.Digest, e = p.digest()
	if e != nil {
		t.Fatal(e)
	}
	s.op.CreatedAt = s.op.CreatedAt.Add(delta)
	s.op.UpdatedAt = s.op.UpdatedAt.Add(delta)
	s.op.HeartbeatAt = s.op.HeartbeatAt.Add(delta)
	s.op.PlanDigest, s.attempt.PlanDigest, s.pub.PlanDigest = p.Digest, p.Digest, p.Digest
	f := &diagnosticReader{data: map[string][]byte{}}
	writeDiagnosticFixture(t, f, s.op, s.attempt, s.pub)
	for key, data := range f.data {
		if files.CreateOnly(context.Background(), key, bytes.NewReader(data), int64(len(data))) != nil {
			t.Fatal("status fixture files")
		}
	}
	s, e = stableHandoverSnapshot(context.Background(), files, p.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	r.PlanDigest = p.Digest
	r.Started = r.Started.Add(delta)
	r.Finished = r.Finished.Add(delta)
	r.Expires = r.Expires.Add(delta)
	r.OperationSHA, r.AttemptSHA, r.PublicationSHA = s.tokens[0], s.tokens[1], s.tokens[2]
	if (handoverReviewStore{files}).Create(context.Background(), r) != nil {
		t.Fatal("status fixture report")
	}
	if _, e := registerIsolatedReview(context.Background(), files, p, r, func() time.Time { return when }); e != nil {
		t.Fatal(e)
	}
	return p, r
}

func statusFixtureBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	e := filepath.WalkDir(root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			value += ":" + digestBytes(data)
		}
		out[relative] = value
		return nil
	})
	if e != nil {
		t.Fatal("fixture snapshot")
	}
	return out
}

func TestBackupStatusCLIIsolated(t *testing.T) {
	if os.Getenv("BACKUP_STATUS_CLI") == "" {
		t.Skip("explicit status CLI harness required")
	}
	if os.Getenv("BACKUP_STATUS_CLI") != "/verify/backup-cli" {
		t.Fatal("dedicated CLI binary required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("fixture permissions")
	}
	files, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer files.Close()
	p, _ := currentStatusFixture(t, files, time.Now().UTC().Truncate(time.Microsecond))
	before := statusFixtureBytes(t, root)
	invoke := func(t *testing.T, target, format string, want int) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, "/verify/backup-cli", "status", "--control-root", target, "--operation", p.OperationID, "--format", format)
		command.Env = []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
		var out, errs bytes.Buffer
		command.Stdout = &out
		command.Stderr = &errs
		e := command.Run()
		code := 0
		if e != nil {
			exit, ok := e.(*exec.ExitError)
			if !ok {
				t.Fatal("CLI process unavailable")
			}
			code = exit.ExitCode()
		}
		if code != want || strings.Contains(out.String(), target) || strings.Contains(errs.String(), target) {
			t.Fatalf("unexpected CLI code %d, expected %d", code, want)
		}
		return out.Bytes()
	}
	t.Run("json_registered", func(t *testing.T) {
		var result RestoreStatus
		if json.Unmarshal(invoke(t, root, "json", 0), &result) != nil || result.Status != "observed_registered" || result.Persistence != "not_inferred" || result.Ready || result.Deployment {
			t.Fatal("unsafe JSON observation")
		}
	})
	t.Run("text_registered", func(t *testing.T) {
		if !bytes.Contains(invoke(t, root, "text", 0), []byte("无法从磁盘推断")) {
			t.Fatal("text omitted warning")
		}
	})
	t.Run("prepared_next_attention", func(t *testing.T) {
		reg, token, e := (handoverRegistrationStore{files}).Load(ctx, p.OperationID)
		if e != nil {
			t.Fatal(e)
		}
		key, _ := registrationKey(p.OperationID)
		original, _ := json.Marshal(reg)
		reg.State = "prepared"
		changed, _ := json.Marshal(reg)
		if files.CompareAndSwap(ctx, key, token, changed) != nil {
			t.Fatal("fixture fault")
		}
		defer func() {
			if files.CompareAndSwap(ctx, key, digestBytes(changed), original) != nil {
				t.Error("fixture reset")
			}
		}()
		var result RestoreStatus
		if json.Unmarshal(invoke(t, root, "json", 4), &result) != nil || result.Status != "registration_pending" || result.Chain != "next_observed" {
			t.Fatal("pending state misreported")
		}
	})
	t.Run("expired_report_attention", func(t *testing.T) {
		other := t.TempDir()
		if os.Chmod(other, 0700) != nil {
			t.Fatal("fixture permissions")
		}
		old, e := storage.OpenBackupFiles(other)
		if e != nil {
			t.Fatal(e)
		}
		defer old.Close()
		currentStatusFixture(t, old, time.Now().UTC().Truncate(time.Microsecond).Add(-10*time.Minute))
		var result RestoreStatus
		if json.Unmarshal(invoke(t, other, "json", 4), &result) != nil || result.ReportFreshness != "expired" || result.Registration != "registered" {
			t.Fatal("expired history misreported")
		}
	})
	t.Run("symlink_root_denied", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if os.Symlink(root, link) != nil {
			t.Fatal("fixture symlink")
		}
		if len(invoke(t, link, "json", 3)) != 0 {
			t.Fatal("unsafe directory produced status")
		}
	})
	t.Run("unsafe_permissions_denied", func(t *testing.T) {
		if os.Chmod(root, 0755) != nil {
			t.Fatal("fixture mode")
		}
		defer os.Chmod(root, 0700)
		if len(invoke(t, root, "json", 3)) != 0 {
			t.Fatal("unsafe directory read")
		}
	})
	t.Run("no_content_or_mode_writes", func(t *testing.T) {
		if !reflect.DeepEqual(before, statusFixtureBytes(t, root)) {
			t.Fatal("status command changed fixture")
		}
	})
	if !t.Failed() {
		t.Log(`BACKUP_STATUS_EVIDENCE {"scope":"isolated-status-cli-fixture","checks":7,"content_and_modes_unchanged":true,"ready_for_cutover":false,"deployment_authorized":false}`)
	}
}
