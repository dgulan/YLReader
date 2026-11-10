//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"yreader/backend/internal/repository/testutil"
)

func TestOverrideUpsertGet(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "ovr")
	book := seedBook(t, tx, userID, src.ID, "ovr")
	other := seedSource(t, tx, userID, "ovr2")
	repo := NewOverrideRepository()
	ctx := context.Background()

	o := &ChapterSourceOverride{
		UserID: userID, BookID: book.ID, ChapterIndex: 5,
		SourceID: other.ID, ChapterURL: "https://other.example/ch/5",
		ChapterTitle: ptr("第5章 风起"), NormalizedTitle: ptr("n:5:风起"),
	}
	if err := repo.Upsert(ctx, tx, o); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if o.Status != OverrideStatusActive {
		t.Fatalf("Upsert 后 status = %s, want active", o.Status)
	}

	got, err := repo.Get(ctx, tx, userID, book.ID, 5)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SourceID != other.ID || got.ChapterURL != "https://other.example/ch/5" {
		t.Fatalf("Get 字段不匹配: %+v", got)
	}

	// 用户隔离：其他用户读不到
	otherUser := mustSeedUser(t, tx)
	if _, err := repo.Get(ctx, tx, otherUser, book.ID, 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨用户 Get err = %v, want ErrNotFound", err)
	}
}

func TestOverrideResetActive(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "ovr3")
	book := seedBook(t, tx, userID, src.ID, "ovr3")
	repo := NewOverrideRepository()
	ctx := context.Background()

	o := &ChapterSourceOverride{
		UserID: userID, BookID: book.ID, ChapterIndex: 2,
		SourceID: src.ID, ChapterURL: "https://x.example/ch/2",
	}
	if err := repo.Upsert(ctx, tx, o); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// 标记 stale
	if err := repo.MarkStaleFrom(ctx, tx, userID, book.ID, 2); err != nil {
		t.Fatalf("MarkStaleFrom: %v", err)
	}
	got, err := repo.Get(ctx, tx, userID, book.ID, 2)
	if err != nil {
		t.Fatalf("Get after stale: %v", err)
	}
	if got.Status != OverrideStatusStale {
		t.Fatalf("status = %s, want stale", got.Status)
	}

	// 再次 Upsert → 显式重置 active
	if err := repo.Upsert(ctx, tx, o); err != nil {
		t.Fatalf("Upsert reset: %v", err)
	}
	got, _ = repo.Get(ctx, tx, userID, book.ID, 2)
	if got.Status != OverrideStatusActive {
		t.Fatalf("重置后 status = %s, want active", got.Status)
	}
}

func TestOverrideDelete(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "ovr4")
	book := seedBook(t, tx, userID, src.ID, "ovr4")
	repo := NewOverrideRepository()
	ctx := context.Background()

	o := &ChapterSourceOverride{
		UserID: userID, BookID: book.ID, ChapterIndex: 7,
		SourceID: src.ID, ChapterURL: "https://x.example/ch/7",
	}
	if err := repo.Upsert(ctx, tx, o); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := repo.Delete(ctx, tx, userID, book.ID, 7); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, tx, userID, book.ID, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("二次 Delete err = %v, want ErrNotFound", err)
	}

	// DeleteBookOverrides 清空
	for i := int32(1); i <= 3; i++ {
		o := &ChapterSourceOverride{
			UserID: userID, BookID: book.ID, ChapterIndex: i,
			SourceID: src.ID, ChapterURL: "https://x.example/ch/" + uuid.NewString(),
		}
		if err := repo.Upsert(ctx, tx, o); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}
	if err := repo.DeleteBookOverrides(ctx, tx, userID, book.ID); err != nil {
		t.Fatalf("DeleteBookOverrides: %v", err)
	}
	list, err := repo.ListByBook(ctx, tx, userID, book.ID)
	if err != nil {
		t.Fatalf("ListByBook: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("DeleteBookOverrides 后仍有 %d 条", len(list))
	}
}
