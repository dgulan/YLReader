//go:build linux

package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

func TestStageObjectsWithPrivateLocalFS(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	if e := os.Chmod(sourceRoot, 0700); e != nil {
		t.Fatal(e)
	}
	source, e := storage.OpenBackupFiles(sourceRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	targetRoot := t.TempDir()
	if e := os.Chmod(targetRoot, 0700); e != nil {
		t.Fatal(e)
	}
	target, e := storage.OpenBackupFiles(targetRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	m, entries := packageFixture(t)
	id := identityFixture(t)
	cipher, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	pkg, e := VerifyPackage(ctx, bytes.NewReader(cipher), id, options, source)
	if e != nil {
		t.Fatal(e)
	}
	defer pkg.Close()
	operationID := "40000000-0000-4000-8000-000000000001"
	if _, e = stageRestoreObjects(ctx, pkg, source, operationID); !errors.Is(e, ErrLocations) {
		t.Fatal("source ancestor accepted", e)
	}
	stage, e := stageRestoreObjects(ctx, pkg, target, operationID)
	if e != nil {
		t.Fatal(e)
	}
	if stage.Reference() == "" {
		t.Fatal("missing owned generation reference")
	}
	r, e := stage.Open(ctx, m.Objects[0].Key)
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(r)
	r.Close()
	if e != nil || !bytes.Equal(got, entries[1].body) {
		t.Fatal("bytes not preserved", e)
	}
	// A second operation cannot reuse the now nonempty target.
	if _, e = stageRestoreObjects(ctx, pkg, target, operationID); !errors.Is(e, ErrLocations) {
		t.Fatal("nonempty target accepted", e)
	}
	if e = stage.Close(); e != nil {
		t.Fatal(e)
	}
	if empty, e := target.Empty(ctx); e != nil || !empty {
		t.Fatal("generation cleanup", e)
	}
	if empty, e := source.Empty(ctx); e != nil || empty {
		t.Fatal("source was removed", e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = stageRestoreObjects(canceled, pkg, target, operationID); e == nil {
		t.Fatal("canceled staging accepted")
	}
	if empty, e := target.Empty(ctx); e != nil || !empty {
		t.Fatal("canceled stage leaked", e)
	}
	registrationFailure := errors.New("synthetic registration rejection")
	called := false
	if _, e = stageRegisteredObjects(ctx, pkg, target, operationID, func(_ context.Context, reference string) error {
		called = true
		entries, e := os.ReadDir(filepath.Join(targetRoot, reference))
		if e != nil || len(entries) != 0 {
			t.Fatal("payload preceded registration", e)
		}
		return registrationFailure
	}); !errors.Is(e, registrationFailure) || !called {
		t.Fatal("registration gate bypassed", e)
	}
	if empty, e := target.Empty(ctx); e != nil || !empty {
		t.Fatal("rejected registration residue", e)
	}
	// A privileged owner changing source bytes after authentication must not
	// silently create a valid generation; checksum failure cleans only target.
	objectPath := filepath.Join(sourceRoot, pkg.temp.Reference(), "object-"+m.Objects[0].ID)
	if e = os.WriteFile(objectPath, bytes.Repeat([]byte{'x'}, len(entries[1].body)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = stageRestoreObjects(ctx, pkg, target, operationID); !errors.Is(e, ErrRestoreObjects) {
		t.Fatal("changed source accepted", e)
	}
	if empty, e := target.Empty(ctx); e != nil || !empty {
		t.Fatal("failed staging residue", e)
	}
	// Unknown publication is a retention condition, not permission to clean.
	var reference string
	_, e = stageRegisteredObjects(ctx, pkg, target, operationID, func(_ context.Context, ref string) error { reference = ref; return storage.ErrCommitUnknown })
	var retained *retainedGeneration
	if !errors.Is(e, storage.ErrCommitUnknown) || !errors.As(e, &retained) || retained.Reference != reference {
		t.Fatal("lost uncertain generation", e)
	}
	entriesOnDisk, e := os.ReadDir(filepath.Join(targetRoot, reference))
	if e != nil || len(entriesOnDisk) != 0 {
		t.Fatal("unknown registration wrote payload or deleted generation", e)
	}
}
