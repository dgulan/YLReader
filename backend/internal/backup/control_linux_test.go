//go:build linux

package backup

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

func TestControlWithDurableLocalFiles(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	first, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := storage.OpenBackupFiles(root)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	controls := []Control{{first}, {second}}
	o := example(t)
	ctx := context.Background()
	if e = controls[0].Create(ctx, o); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- controls[i%2].Update(ctx, o.ID, 1, advance(o, "staging"), false)
		}(i)
	}
	wg.Wait()
	close(results)
	winners := 0
	for e := range results {
		if e == nil {
			winners++
		} else if !errors.Is(e, storage.ErrConflict) && !errors.Is(e, storage.ErrBusy) {
			t.Fatal(e)
		}
	}
	if winners != 1 {
		t.Fatalf("durable CAS winners=%d", winners)
	}
	got, _, e := controls[1].Load(ctx, o.ID)
	if e != nil || got.Generation != 2 || got.Phase != "staging" {
		t.Fatalf("persisted state: %+v %v", got, e)
	}
}
