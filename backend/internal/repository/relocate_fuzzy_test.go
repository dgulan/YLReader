//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository/testutil"
)

// seedRelocateChapters 为指定书/源写入 n 章，标题为 "n:<i>:<标题>" 形式。
// 第 targetIndex 章使用 targetTitle，其余使用普通标题，便于相似度对照。
func seedRelocateChapters(t *testing.T, pool *pgxpool.Pool, bookID, sourceID uuid.UUID, n, targetIndex int, targetTitle string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO chapters (book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash, is_volume)
		SELECT $1, $2, i, i, t, t,
		       'https://relocate.example/rc-' || i,
		       encode(sha256(('https://relocate.example/rc-' || i)::bytea), 'hex'),
		       false
		  FROM (SELECT i,
		               CASE WHEN i = $3 THEN $4 ELSE 'n:' || i || ':普通章节标题' || i END AS t
		          FROM generate_series(0, $5) AS i) AS x`,
		bookID, sourceID, targetIndex, targetTitle, n-1); err != nil {
		t.Fatalf("seed relocate chapters: %v", err)
	}
}

// 语义等价性：`%` 前置谓词只做候选集裁剪，0.6 二次过滤保证结果与「仅 similarity 过滤」一致。
// 这是 ADR-014 的核心声明，必须在真实 pg_trgm 默认阈值（0.3）下验证。
func TestRelocateFuzzySemanticEquivalence(t *testing.T) {
	pool := testutil.TestPool(t)
	ctx := context.Background()
	userID := seedCommittedUser(t, pool)
	src := seedSource(t, pool, userID, "relocate-equiv")
	book := seedBook(t, pool, userID, src.ID, "relocate-equiv-book")

	const target = 250
	seedRelocateChapters(t, pool, book.ID, src.ID, 500, target, "n:250:九霄风云录特别篇")

	queries := []string{
		"n:250:九霄风云录特别篇",
		"n:250:九霄风云录特别篇(修订)",
		"九霄风云录",
		"n:250:九霄风云录",
	}
	for _, q := range queries {
		var withPercent, similarityOnly int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM chapters c
			 WHERE c.book_id = $1 AND c.source_id = $2 AND c.is_volume = false
			   AND c.chapter_index BETWEEN 0 AND 499
			   AND c.normalized_title % $3
			   AND similarity(c.normalized_title, $3) >= 0.6`, book.ID, src.ID, q).Scan(&withPercent); err != nil {
			t.Fatalf("count with %% for %q: %v", q, err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM chapters c
			 WHERE c.book_id = $1 AND c.source_id = $2 AND c.is_volume = false
			   AND c.chapter_index BETWEEN 0 AND 499
			   AND similarity(c.normalized_title, $3) >= 0.6`, book.ID, src.ID, q).Scan(&similarityOnly); err != nil {
			t.Fatalf("count similarity-only for %q: %v", q, err)
		}
		if withPercent != similarityOnly {
			t.Fatalf("query %q: `%%`+similarity count = %d, similarity-only count = %d (should be equal)", q, withPercent, similarityOnly)
		}
	}
}

// 低于阈值时函数必须返回 0 行（不得因 `%` 放宽而返回弱匹配）。
func TestRelocateFuzzyBelowThresholdNoRow(t *testing.T) {
	pool := testutil.TestPool(t)
	ctx := context.Background()
	userID := seedCommittedUser(t, pool)
	src := seedSource(t, pool, userID, "relocate-nomatch")
	book := seedBook(t, pool, userID, src.ID, "relocate-nomatch-book")

	seedRelocateChapters(t, pool, book.ID, src.ID, 200, 100, "n:100:九霄风云录特别篇")

	var idx int
	err := pool.QueryRow(ctx,
		`SELECT chapter_index FROM relocate_fuzzy($1, $2, $3, 100, 100, 0.6)`,
		book.ID, src.ID, "完全无关的另一种标题").Scan(&idx)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("relocate_fuzzy unrelated title: err = %v, want pgx.ErrNoRows", err)
	}
}
