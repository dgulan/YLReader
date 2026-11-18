//go:build linux

package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	headercrypto "yreader/backend/internal/pkg/crypto"
)

func TestRestorePrivateHeaderKeyring(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, "header-keys.json")
	data := []byte(`{"keyring_format":"ylreader-header-keyring","keyring_version":1,"current_kid":"primary","keys":{"primary":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) + `"}}`)
	if e := os.WriteFile(file, data, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("YREADER_HEADER_KEY_PRIMARY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	old := headercrypto.DefaultKeyring()
	headercrypto.SetDefaultKeyring(headerRing(t, "primary", 2))
	t.Cleanup(func() { headercrypto.SetDefaultKeyring(old) })
	ring, e := LoadRestoreHeaderKeyring(context.Background(), file)
	if e != nil {
		t.Fatal(e)
	}
	user, source := uuid.New(), uuid.New()
	v, e := newHeaderVerifier([]string{"primary"}, ring)
	if e != nil {
		t.Fatal(e)
	}
	if e = v.check(user, source, headerEnvelope(t, headerRing(t, "primary", 1), user, source, []byte(`{"Cookie":"synthetic-only"}`))); e != nil {
		t.Fatal("used global or env key", e)
	}
	if e = os.Chmod(file, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadRestoreHeaderKeyring(context.Background(), file); !errors.Is(e, ErrHeaderKeyring) {
		t.Fatal("public secret file accepted", e)
	}
}
