package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

func TestObjectPhaseRetentionPolicy(t *testing.T) {
	for _, spec := range []struct {
		name    string
		failure error
		retain  bool
	}{{"success", nil, true}, {"commit-unknown", ErrRestoreCommitUnknown, true}, {"rollback-unknown", ErrRestoreRollback, true}, {"result-publication-unknown", ErrOutcomeRecord, true}, {"confirmed-failure", ErrRestoreDatabase, false}, {"reference-failure", ErrObjectReferences, false}} {
		t.Run(spec.name, func(t *testing.T) {
			temp := &memoryTemp{}
			stage := &stagedObjects{temp: temp}
			retained, e := finishObjectDatabasePhase(stage, spec.failure)
			if (retained != nil) != spec.retain || temp.removed == spec.retain || !errors.Is(e, spec.failure) {
				t.Fatal("unsafe cleanup decision", e)
			}
		})
	}
	temp := &memoryTemp{cleanupFails: true}
	stage := &stagedObjects{temp: temp}
	retained, e := finishObjectDatabasePhase(stage, ErrRestoreDatabase)
	if retained != stage || !errors.Is(e, ErrCleanup) || !errors.Is(e, ErrRestoreDatabase) || stage.Reference() == "" {
		t.Fatal("cleanup failure lost", e)
	}
}

func TestStagedObjectReadLifetime(t *testing.T) {
	body := []byte("synthetic content")
	temp := &memoryTemp{files: map[string][]byte{"object-11111111111111111111111111111111": body}}
	stage := &stagedObjects{temp: temp, objects: []ManifestObject{{ID: "11111111111111111111111111111111", Key: "local/book/0.txt", Bytes: int64(len(body)), SHA: sha(body)}}}
	r, e := stage.Open(context.Background(), "local/book/0.txt")
	if e != nil {
		t.Fatal(e)
	}
	if e = stage.Close(); !errors.Is(e, storage.ErrBusy) {
		t.Fatal("active reader removed", e)
	}
	got, e := io.ReadAll(r)
	if e != nil || !bytes.Equal(got, body) {
		t.Fatal("object changed", e)
	}
	r.Close()
	if _, e = stage.Open(context.Background(), "../escape"); !errors.Is(e, storage.ErrNotFound) {
		t.Fatal(e)
	}
	if e = stage.Close(); e != nil {
		t.Fatal(e)
	}
	if e = stage.Close(); e != nil {
		t.Fatal(e)
	}
}

func TestStagedObjectReadbackIntegrity(t *testing.T) {
	object := ManifestObject{ID: "11111111111111111111111111111111", Bytes: 3, SHA: sha([]byte("abc"))}
	for _, body := range [][]byte{[]byte("bad"), []byte("ab"), []byte("abcd")} {
		temp := &memoryTemp{files: map[string][]byte{"object-" + object.ID: body}}
		if e := verifyStagedObject(context.Background(), temp, object); !errors.Is(e, ErrRestoreObjects) {
			t.Fatal("corrupt persisted bytes accepted", e)
		}
	}
}
