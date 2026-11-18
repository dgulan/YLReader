package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

func TestPublicationReceiptTransitions(t *testing.T) {
	op := example(t)
	p := Plan{OperationID: op.ID, BackupID: op.BackupID, Digest: op.PlanDigest, PackageSHA: strings.Repeat("1", 64), ManifestSHA: strings.Repeat("2", 64), TargetFingerprint: strings.Repeat("3", 64)}
	r := publicationFor(p, op.OwnerID, ".scratch-"+strings.Repeat("a", 32))
	store := publicationStore{&memoryFiles{data: map[string][]byte{}}}
	ctx := context.Background()
	if e := store.Create(ctx, r); e != nil {
		t.Fatal(e)
	}
	if e := store.Create(ctx, r); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	for _, state := range []string{"copying", "publishing", "verified"} {
		old, token, e := store.Load(ctx, r.OperationID)
		if e != nil {
			t.Fatal(e)
		}
		next := old
		next.State = state
		next.TreeFingerprint = strings.Repeat("4", 64)
		if e = store.Update(ctx, r.OperationID, token, next); e != nil {
			t.Fatal(e)
		}
		r = next
	}
	_, token, _ := store.Load(ctx, r.OperationID)
	next := r
	next.State = "publishing"
	if e := store.Update(ctx, r.OperationID, token, next); !errors.Is(e, ErrPublicationState) {
		t.Fatal("state rollback allowed", e)
	}
	bytes, _ := json.Marshal(r)
	for _, bad := range []string{strings.Replace(string(bytes), "owner_id", "OWNER_ID", 1), strings.Replace(string(bytes), `"publication_version":1`, `"publication_version":1,"publication_version":1`, 1)} {
		key, _ := publicationKey(r.OperationID)
		store.Files.(*memoryFiles).data[key] = []byte(bad)
		if _, _, e := store.Load(ctx, r.OperationID); !errors.Is(e, ErrPublicationState) {
			t.Fatal("invalid receipt accepted", e)
		}
	}
}

func TestPublicationKeySetRejectsPrefixConflicts(t *testing.T) {
	for _, keys := range [][]string{{"a", "a/b"}, {"a", "a-b", "a/b"}, {"a/b", "a"}, {"same", "same"}, {"../escape"}, {"a/.hidden"}, {strings.Repeat("x", 256)}, {strings.Repeat("a/", 64) + "b"}} {
		s := &stagedObjects{}
		for _, key := range keys {
			s.objects = append(s.objects, ManifestObject{Key: key})
		}
		if _, e := publicationObjects(s); !errors.Is(e, storage.ErrInvalidKey) {
			t.Fatal("invalid key set accepted", keys, e)
		}
	}
	s := &stagedObjects{objects: []ManifestObject{{Key: "local/book/0.txt"}, {Key: "fonts/中文.ttf"}}}
	if _, e := publicationObjects(s); e != nil {
		t.Fatal(e)
	}
}
