//go:build integration

package repository

import (
	"context"
	"testing"
	"yreader/backend/internal/repository/testutil"
)

func TestCanceledJobCannotBeOverwritten(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	uid := testutil.SeedUser(t, tx)
	ctx := context.Background()
	r := NewJobRepository()
	j, err := r.Create(ctx, tx, uid, "full_cache", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.Cancel(ctx, tx, uid, j.ID); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if err := r.MarkFailed(ctx, tx, j.ID, "internal_error", "late failure"); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkSucceeded(ctx, tx, j.ID, 100, nil); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetByID(ctx, tx, uid, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "canceled" {
		t.Fatalf("terminal state overwritten: %s", got.Status)
	}
}
