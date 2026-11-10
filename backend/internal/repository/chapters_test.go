//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"yreader/backend/internal/repository/testutil"
)

func buildTOC(n int, withVolume bool) []Chapter {
	out := make([]Chapter, 0, n+1)
	var sort int32
	if withVolume {
		sort++
		out = append(out, Chapter{
			SortOrder: sort, Title: "第一卷 初入", NormalizedTitle: "第一卷初入",
			IsVolume: true,
		})
	}
	for i := 1; i <= n; i++ {
		sort++
		idx := int32(i)
		out = append(out, Chapter{
			ChapterIndex:    &idx,
			SortOrder:       sort,
			Title:           fmt.Sprintf("第%d章 标题%d", i, i),
			NormalizedTitle: fmt.Sprintf("n:%d:标题%d", i, i),
			URL:             fmt.Sprintf("https://example.com/ch/%d", i),
			URLHash:         fmt.Sprintf("ch-%d", i),
		})
	}
	return out
}

func TestReplaceTOC(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "toc")
	book := seedBook(t, tx, userID, src.ID, "toc")
	repo := NewChapterRepository()
	ctx := context.Background()

	// 1000 章 → 1000 行
	total, err := repo.ReplaceTOC(ctx, tx, userID, book.ID, src.ID, buildTOC(1000, false))
	if err != nil {
		t.Fatalf("ReplaceTOC 1000: %v", err)
	}
	if total != 1000 {
		t.Fatalf("ReplaceTOC 返回 %d, want 1000", total)
	}
	toc, err := repo.GetTOC(ctx, tx, userID, book.ID, src.ID)
	if err != nil {
		t.Fatalf("GetTOC: %v", err)
	}
	if len(toc) != 1000 {
		t.Fatalf("GetTOC len = %d, want 1000", len(toc))
	}

	// 1200 章（含卷行）→ 1201 行，可读 1200
	total, err = repo.ReplaceTOC(ctx, tx, userID, book.ID, src.ID, buildTOC(1200, true))
	if err != nil {
		t.Fatalf("ReplaceTOC 1200: %v", err)
	}
	if total != 1200 {
		t.Fatalf("ReplaceTOC 返回 %d, want 1200", total)
	}
	toc, err = repo.GetTOC(ctx, tx, userID, book.ID, src.ID)
	if err != nil {
		t.Fatalf("GetTOC 1200: %v", err)
	}
	if len(toc) != 1201 {
		t.Fatalf("GetTOC len = %d, want 1201（含卷行）", len(toc))
	}

	// 删除重建为 800 章
	total, err = repo.ReplaceTOC(ctx, tx, userID, book.ID, src.ID, buildTOC(800, false))
	if err != nil {
		t.Fatalf("ReplaceTOC 800: %v", err)
	}
	if total != 800 {
		t.Fatalf("ReplaceTOC 返回 %d, want 800", total)
	}
	toc, err = repo.GetTOC(ctx, tx, userID, book.ID, src.ID)
	if err != nil {
		t.Fatalf("GetTOC 800: %v", err)
	}
	if len(toc) != 800 {
		t.Fatalf("GetTOC len = %d, want 800", len(toc))
	}

	// 按 chapter_index 精确读取
	ch, err := repo.GetByIndex(ctx, tx, userID, book.ID, src.ID, 42)
	if err != nil {
		t.Fatalf("GetByIndex: %v", err)
	}
	if ch.Title != "第42章 标题42" {
		t.Fatalf("GetByIndex title = %q", ch.Title)
	}
	if ch.ChapterIndex == nil || *ch.ChapterIndex != 42 {
		t.Fatalf("GetByIndex chapter_index = %v, want 42", ch.ChapterIndex)
	}
}
