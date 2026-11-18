//go:build linux

package backup

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

func TestPackageNativePrivateStaging(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	scratch, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer scratch.Close()
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	verified, e := VerifyPackage(context.Background(), bytes.NewReader(data), id, options, scratch)
	if e != nil {
		t.Fatal(e)
	}
	reader, e := verified.OpenObject(context.Background(), m.Objects[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(reader)
	reader.Close()
	if e != nil || !bytes.Equal(body, entries[1].body) {
		t.Fatal("staged bytes differ", e)
	}
	if e = verified.Close(); e != nil {
		t.Fatal(e)
	}
	if empty, e := scratch.Empty(context.Background()); e != nil || !empty {
		t.Fatal("native plaintext residue", e)
	}
	options.Descriptor.SHA256 = sha([]byte("synthetic mismatch"))
	if _, e = VerifyPackage(context.Background(), bytes.NewReader(data), id, options, scratch); e == nil {
		t.Fatal("mismatch accepted")
	}
	if empty, e := scratch.Empty(context.Background()); e != nil || !empty {
		t.Fatal("failed verification residue", e)
	}
}
