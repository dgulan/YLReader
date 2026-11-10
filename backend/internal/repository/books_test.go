//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"yreader/backend/internal/repository/testutil"
)

func TestBookCreateFind(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "bookfind")
	repo := NewBookRepository()
	ctx := context.Background()

	b := seedBook(t, tx, userID, src.ID, "one")

	got, err := repo.Get(ctx, tx, userID, b.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.NormalizedName != "book_one" || got.BookType != BookTypeOnline {
		t.Fatalf("Get 字段不匹配: %+v", got)
	}
	if got.ActiveSourceID == nil || *got.ActiveSourceID != src.ID {
		t.Fatalf("ActiveSourceID 不匹配")
	}

	// 按归一化键查找
	found, err := repo.FindByNormalized(ctx, tx, userID, "book_one", "author_one")
	if err != nil {
		t.Fatalf("FindByNormalized: %v", err)
	}
	if found.ID != b.ID {
		t.Fatalf("FindByNormalized 命中错误: %v vs %v", found.ID, b.ID)
	}

	// 未命中
	if _, err := repo.FindByNormalized(ctx, tx, userID, "nope", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindByNormalized 未命中 err = %v, want ErrNotFound", err)
	}

	// 唯一键冲突
	dup := &Book{
		UserID: userID, BookType: BookTypeOnline,
		NormalizedName: "book_one", NormalizedAuthor: "author_one",
		Name: "重复", Author: "作者",
	}
	if err := repo.Create(ctx, tx, dup); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate Create err = %v, want ErrDuplicate", err)
	}
}

func TestUpdateActiveSource(t *testing.T) {
	pool := testutil.TestPool(t)
	ctx := context.Background()

	// 夹具须先提交（另开事务看不到未提交数据），测试结束级联删除。
	var userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, 'test') RETURNING id`,
		"u_"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("造用户: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	srcA := seedSource(t, pool, userID, "srcA")
	srcB := seedSource(t, pool, userID, "srcB")
	book := seedBook(t, pool, userID, srcA.ID, "switch")

	mappings := NewMappingRepository()
	books := NewBookRepository()

	// A 源 mapping 为 active；B 源先有一条 stale mapping。
	if err := mappings.Upsert(ctx, pool, userID, &BookSourceMapping{
		BookID: book.ID, SourceID: srcA.ID, BookURL: "https://example.com/book/switch",
		Name: ptr("书籍_switch"), SyncStatus: MappingStatusActive,
	}); err != nil {
		t.Fatalf("upsert A mapping: %v", err)
	}
	if err := mappings.Upsert(ctx, pool, userID, &BookSourceMapping{
		BookID: book.ID, SourceID: srcB.ID, BookURL: "https://example.com/book/switch?b=1",
		Name: ptr("书籍_switch"), SyncStatus: MappingStatusStale,
	}); err != nil {
		t.Fatalf("upsert B mapping: %v", err)
	}

	// 换源 A → B（在事务内）
	tx := testutil.BeginTx(t, pool)
	if err := books.UpdateActiveSource(ctx, tx, userID, book.ID, srcB.ID, "https://example.com/book/switch?b=1"); err != nil {
		t.Fatalf("UpdateActiveSource: %v", err)
	}

	// 事务内可见
	var active string
	if err := tx.QueryRow(ctx, `SELECT active_source_id::text FROM books WHERE id=$1`, book.ID).Scan(&active); err != nil {
		t.Fatalf("tx read books: %v", err)
	}
	if active != srcB.ID.String() {
		t.Fatalf("tx 内 active_source_id = %s, want %s", active, srcB.ID)
	}
	var aStatus, bStatus string
	if err := tx.QueryRow(ctx, `SELECT sync_status FROM book_source_mappings WHERE book_id=$1 AND source_id=$2`, book.ID, srcA.ID).Scan(&aStatus); err != nil {
		t.Fatalf("tx read A mapping: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT sync_status FROM book_source_mappings WHERE book_id=$1 AND source_id=$2`, book.ID, srcB.ID).Scan(&bStatus); err != nil {
		t.Fatalf("tx read B mapping: %v", err)
	}
	if aStatus != "stale" || bStatus != "active" {
		t.Fatalf("mapping 状态 A=%s B=%s, want stale/active", aStatus, bStatus)
	}

	// 提交前不可见：外部连接读到的仍是旧值（MVCC）
	var outside string
	if err := pool.QueryRow(ctx, `SELECT active_source_id::text FROM books WHERE id=$1`, book.ID).Scan(&outside); err != nil {
		t.Fatalf("pool read books: %v", err)
	}
	if outside != srcA.ID.String() {
		t.Fatalf("提交前外部读到 active_source_id = %s, want 旧值 %s", outside, srcA.ID)
	}
}

func TestUpdateActiveSourceNewMapping(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	ctx := context.Background()

	srcA := seedSource(t, tx, userID, "srcA2")
	srcB := seedSource(t, tx, userID, "srcB2")
	book := seedBook(t, tx, userID, srcA.ID, "newmap")

	books := NewBookRepository()
	// B 无 mapping → 应新建一条 active
	if err := books.UpdateActiveSource(ctx, tx, userID, book.ID, srcB.ID, "https://example.com/book/newmap?b=1"); err != nil {
		t.Fatalf("UpdateActiveSource: %v", err)
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT sync_status FROM book_source_mappings WHERE book_id=$1 AND source_id=$2`, book.ID, srcB.ID).Scan(&status); err != nil {
		t.Fatalf("读取新建 mapping: %v", err)
	}
	if status != "active" {
		t.Fatalf("新建 mapping 状态 = %s, want active", status)
	}
}
