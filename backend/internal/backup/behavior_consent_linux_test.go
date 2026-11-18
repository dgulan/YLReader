//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func TestBehaviorConsentSecureFiles(t *testing.T) {
	if os.Getenv("BACKUP_CONSENT_FIXTURE") != "isolated-private-files" {
		t.Skip("explicit isolated consent harness required")
	}
	if os.Geteuid() != 10001 {
		t.Fatal("non-root fixture user required")
	}
	a, input, control, p, now := consentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	open := func(data map[string][]byte) (storage.BackupFiles, string) {
		t.Helper()
		root := t.TempDir()
		if os.Chmod(root, 0700) != nil {
			t.Fatal("permissions")
		}
		f, e := storage.OpenBackupFiles(root)
		if e != nil {
			t.Fatal(e)
		}
		for key, b := range data {
			if e = f.CreateOnly(ctx, key, bytes.NewReader(b), int64(len(b))); e != nil {
				f.Close()
				t.Fatal(e)
			}
		}
		t.Cleanup(func() { f.Close() })
		return f, root
	}
	in, inRoot := open(input.data)
	ctl, ctlRoot := open(control.data)
	beforeIn, beforeControl := statusFixtureBytes(t, inRoot), statusFixtureBytes(t, ctlRoot)
	preflight := func() error {
		prepared, e := prepareBehaviorInput(ctx, in, ctl, "consent.json", p, a.RuntimeSHA, func() time.Time { return now })
		if prepared != nil {
			prepared.Close()
		}
		return e
	}
	t.Run("private-files", func(t *testing.T) {
		if e := preflight(); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("insecure-credential-mode", func(t *testing.T) {
		path := filepath.Join(inRoot, "account.json")
		if os.Chmod(path, 0644) != nil {
			t.Fatal("chmod")
		}
		defer os.Chmod(path, 0600)
		if e := preflight(); e != errBehaviorConsent {
			t.Fatal("public credential accepted")
		}
	})
	t.Run("symlink-credential", func(t *testing.T) {
		if e := os.Symlink("account.json", filepath.Join(inRoot, "link")); e != nil {
			t.Fatal(e)
		}
		defer os.Remove(filepath.Join(inRoot, "link"))
		bad := a
		bad.Accounts = append([]behaviorConsentAccount(nil), a.Accounts...)
		bad.Accounts[0].CredentialKey = "link"
		body, _ := json.Marshal(bad)
		path := filepath.Join(inRoot, "consent.json")
		if os.WriteFile(path, body, 0600) != nil {
			t.Fatal("fixture consent")
		}
		defer os.WriteFile(path, input.data["consent.json"], 0600)
		if e := preflight(); e != errBehaviorConsent {
			t.Fatal("linked credential accepted")
		}
	})
	if !reflect.DeepEqual(beforeIn, statusFixtureBytes(t, inRoot)) || !reflect.DeepEqual(beforeControl, statusFixtureBytes(t, ctlRoot)) {
		t.Fatal("preflight changed files or modes")
	}
	t.Log(`BACKUP_CONSENT_EVIDENCE {"scope":"isolated-consent-input-only","checks":3,"content_and_modes_unchanged":true,"network_used":false,"execution_authorized":false}`)
}
