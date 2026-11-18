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

	"github.com/jackc/pgx/v5"
)

func TestBackupPreviewCLIIsolated(t *testing.T) {
	if os.Getenv("BACKUP_PREVIEW_CLI") == "" {
		t.Skip("explicit preview CLI harness required")
	}
	if os.Getenv("BACKUP_PREVIEW_CLI") != "/verify/backup-cli" || os.Getenv("BACKUP_PREVIEW_DSN") != "postgres://reader:@127.0.0.1:5432/yreader_a1_iso_preview?sslmode=disable" {
		t.Fatal("dedicated fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("permissions")
	}
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, o := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	r := InspectionRequest{Format: "ylreader-package-inspection-request", Version: 1, PackageKey: "package.age", IdentityKey: "identity.txt", BackupID: o.Descriptor.BackupID, Bytes: o.Descriptor.Bytes, SHA: o.Descriptor.SHA256, HeaderKIDs: o.HeaderKIDs, Limits: o.Limits}
	write := func(key string, data []byte) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(root, key), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	encode := func(v any) []byte {
		data, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	write("request.json", encode(r))
	write("package.age", data)
	write("identity.txt", []byte(id.String()))
	c := localConfig(t)
	c.Source.InstanceID = m.SourceID
	c.Encryption.Identity = filepath.Join(root, "identity.txt")
	c.Limits = o.Limits
	if e := os.WriteFile(c.Target.DSNFile, []byte(os.Getenv("BACKUP_PREVIEW_DSN")), 0600); e != nil {
		t.Fatal(e)
	}
	write("config.json", encode(c))
	before := statusFixtureBytes(t, root)
	locationsBefore := statusFixtureBytes(t, c.Temporary.Root+"/..")
	run := func(command string, extra ...string) ([]byte, int) {
		t.Helper()
		args := []string{command, "--input-root", root, "--request-key", "request.json"}
		args = append(args, extra...)
		cmd := exec.CommandContext(ctx, "/verify/backup-cli", args...)
		var out, errout bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errout
		e := cmd.Run()
		code := 0
		if e != nil {
			exit, ok := e.(*exec.ExitError)
			if !ok {
				t.Fatal("subprocess unavailable")
			}
			code = exit.ExitCode()
		}
		for _, secret := range []string{id.String(), string(entries[0].body), "postgres://", root} {
			if strings.Contains(out.String()+errout.String(), secret) {
				t.Fatal("private input exposed")
			}
		}
		return out.Bytes(), code
	}
	t.Run("authenticated-inspect", func(t *testing.T) {
		out, code := run("inspect")
		var summary PackageInspection
		if code != 0 || json.Unmarshal(out, &summary) != nil || !summary.Authenticated || summary.Execution || summary.Deployment || summary.DatabaseVerified {
			t.Fatal("inspection output")
		}
	})
	t.Run("text-inspect", func(t *testing.T) {
		out, code := run("inspect", "--format", "text")
		if code != 0 || !bytes.Contains(out, []byte("执行/部署未获授权")) {
			t.Fatal("text boundary")
		}
	})
	t.Run("readonly-candidate-plan", func(t *testing.T) {
		out, code := run("plan", "--config-key", "config.json")
		var result struct {
			Plan       Plan `json:"plan"`
			Checked    bool `json:"target_readonly_checked"`
			Execution  bool `json:"execution_authorized"`
			Deployment bool `json:"deployment_authorized"`
		}
		if code != 0 || json.Unmarshal(out, &result) != nil || result.Plan.Validate() != nil || !result.Checked || result.Execution || result.Deployment || result.Plan.BackupID != m.BackupID || result.Plan.SourceID != m.SourceID {
			t.Fatal("candidate plan")
		}
		if e := (PostgresTargetInspector{}).CheckEmpty(ctx, mustPreviewTarget(t, os.Getenv("BACKUP_PREVIEW_DSN"))); e != nil {
			t.Fatal("CLI changed target")
		}
	})
	t.Run("wrong-identity", func(t *testing.T) {
		write("identity.txt", []byte(identityFixture(t).String()))
		defer write("identity.txt", []byte(id.String()))
		out, code := run("inspect")
		if code != 4 || len(out) != 0 {
			t.Fatal("wrong identity accepted")
		}
	})
	t.Run("damaged-package", func(t *testing.T) {
		bad := append([]byte(nil), data...)
		bad[len(bad)/2] ^= 1
		write("package.age", bad)
		defer write("package.age", data)
		out, code := run("inspect")
		if code != 4 || len(out) != 0 {
			t.Fatal("damaged package accepted")
		}
	})
	t.Run("plan-source-mismatch", func(t *testing.T) {
		bad := c
		bad.Source.InstanceID = "10000000-0000-4000-8000-000000000099"
		write("config.json", encode(bad))
		defer write("config.json", encode(c))
		out, code := run("plan", "--config-key", "config.json")
		if code != 4 || len(out) != 0 {
			t.Fatal("source mismatch accepted")
		}
	})
	t.Run("occupied-target", func(t *testing.T) {
		conn, e := pgx.ConnectConfig(ctx, mustPreviewTarget(t, os.Getenv("BACKUP_PREVIEW_DSN")))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = conn.Exec(ctx, "CREATE TABLE preview_fixture(id integer)"); e != nil {
			conn.Close(ctx)
			t.Fatal(e)
		}
		conn.Close(ctx)
		out, code := run("plan", "--config-key", "config.json")
		conn, e = pgx.ConnectConfig(ctx, mustPreviewTarget(t, os.Getenv("BACKUP_PREVIEW_DSN")))
		if e != nil {
			t.Fatal(e)
		}
		_, cleanupErr := conn.Exec(ctx, "DROP TABLE preview_fixture")
		conn.Close(ctx)
		if cleanupErr != nil {
			t.Fatal(cleanupErr)
		}
		if code != 4 || len(out) != 0 {
			t.Fatal("nonempty target accepted")
		}
	})
	t.Run("symlink-key", func(t *testing.T) {
		if e := os.Symlink(filepath.Join(root, "identity.txt"), filepath.Join(root, "link")); e != nil {
			t.Fatal(e)
		}
		defer os.Remove(filepath.Join(root, "link"))
		bad := r
		bad.IdentityKey = "link"
		write("request.json", encode(bad))
		defer write("request.json", encode(r))
		out, code := run("inspect")
		if code != 4 || len(out) != 0 {
			t.Fatal("symlink accepted")
		}
	})
	t.Run("insecure-key-mode", func(t *testing.T) {
		file := filepath.Join(root, "identity.txt")
		if os.Chmod(file, 0644) != nil {
			t.Fatal("chmod")
		}
		defer os.Chmod(file, 0600)
		out, code := run("inspect")
		if code != 4 || len(out) != 0 {
			t.Fatal("public secret accepted")
		}
	})
	if !reflect.DeepEqual(before, statusFixtureBytes(t, root)) || !reflect.DeepEqual(locationsBefore, statusFixtureBytes(t, c.Temporary.Root+"/..")) {
		t.Fatal("preview changed contents or modes")
	}
	t.Log(`BACKUP_PREVIEW_EVIDENCE {"scope":"isolated-preview-cli-fixture","checks":9,"content_and_modes_unchanged":true,"target_empty_after_plan":true,"execution_authorized":false,"deployment_authorized":false}`)
}

func mustPreviewTarget(t *testing.T, dsn string) *pgx.ConnConfig {
	t.Helper()
	c, e := ParseTarget(dsn)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
