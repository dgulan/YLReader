//go:build integration

package repository

import (
	"context"
	"testing"

	"yreader/backend/internal/repository/testutil"
)

func TestBookshelfList(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "shelf")
	repo := NewBookshelfRepository()
	ctx := context.Background()

	b1 := seedBook(t, tx, userID, src.ID, "shelf1")
	b2 := seedBook(t, tx, userID, src.ID, "shelf2")

	// 给 b1 加两条 override
	ov := NewOverrideRepository()
	for i := int32(1); i <= 2; i++ {
		if err := ov.Upsert(ctx, tx, &ChapterSourceOverride{
			UserID: userID, BookID: b1.ID, ChapterIndex: i,
			SourceID: src.ID, ChapterURL: "https://x.example/ch/" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("Upsert override %d: %v", i, err)
		}
	}

	// b2 无源（模拟源被删/在线书未绑定）
	if _, err := tx.Exec(ctx, `UPDATE books SET active_source_id = NULL, active_book_url = NULL WHERE id = $1`, b2.ID); err != nil {
		t.Fatalf("清空 b2 源: %v", err)
	}

	list, err := repo.List(ctx, tx, userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("书架条数 = %d, want 2", len(list))
	}

	byID := map[string]*BookshelfEntry{}
	for _, e := range list {
		byID[e.ID.String()] = e
	}
	e1 := byID[b1.ID.String()]
	if e1 == nil {
		t.Fatal("缺少 b1")
	}
	if e1.NeedsSource {
		t.Fatal("b1 needs_source 应为 false")
	}
	if e1.OverrideCount != 2 {
		t.Fatalf("b1 override_count = %d, want 2", e1.OverrideCount)
	}

	e2 := byID[b2.ID.String()]
	if e2 == nil {
		t.Fatal("缺少 b2")
	}
	if !e2.NeedsSource {
		t.Fatal("b2 needs_source 应为 true")
	}
	if e2.OverrideCount != 0 {
		t.Fatalf("b2 override_count = %d, want 0", e2.OverrideCount)
	}
}
