//go:build integration

package repository

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"yreader/backend/internal/repository/testutil"
)

func TestProgressGetEmpty(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "prog-empty")
	book := seedBook(t, tx, userID, src.ID, "prog-empty")
	ctx := context.Background()

	p, err := NewProgressRepository().Get(ctx, tx, userID, book.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Version != 1 {
		t.Fatalf("空进度 version = %d, want 1", p.Version)
	}
	if p.ChapterIndex != 0 || p.CharOffset != 0 || p.ChapterTitle != "" {
		t.Fatalf("空进度应为零值: %+v", p)
	}
}

func TestProgressUpsert(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "prog")
	book := seedBook(t, tx, userID, src.ID, "prog")
	repo := NewProgressRepository()
	ctx := context.Background()

	// 首次写入 → version 1
	p := &ReadingProgress{
		UserID: userID, BookID: book.ID,
		ChapterIndex: 10, CharOffset: 128,
		ChapterTitle: "第10章 开始", NormalizedTitle: "n:10:开始",
		ChapterRatio: 0.45678, ProgressPercent: 45.68,
		ClientUpdatedAt: time.Now().UTC(), DeviceID: ptr("dev-1"),
	}
	if err := repo.Update(ctx, tx, p, 1); err != nil {
		t.Fatalf("首次 Update: %v", err)
	}
	if p.Version != 1 {
		t.Fatalf("首次写入 version = %d, want 1", p.Version)
	}

	// 读取并核对 numeric 往返
	got, err := repo.Get(ctx, tx, userID, book.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ChapterIndex != 10 || got.CharOffset != 128 {
		t.Fatalf("进度字段不匹配: %+v", got)
	}
	if math.Abs(got.ChapterRatio-0.45678) > 1e-9 {
		t.Fatalf("chapter_ratio = %v, want 0.45678", got.ChapterRatio)
	}
	if math.Abs(got.ProgressPercent-45.68) > 0.01 {
		t.Fatalf("progress_percent = %v, want 45.68", got.ProgressPercent)
	}

	// 带版本更新 → version 2
	p.ChapterIndex = 11
	p.ChapterTitle = "第11章 继续"
	if err := repo.Update(ctx, tx, p, 1); err != nil {
		t.Fatalf("版本 1 更新: %v", err)
	}
	if p.Version != 2 {
		t.Fatalf("更新后 version = %d, want 2", p.Version)
	}

	// 陈旧版本 → ErrVersionConflict
	p.ChapterIndex = 12
	if err := repo.Update(ctx, tx, p, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("陈旧版本 err = %v, want ErrVersionConflict", err)
	}
}
