//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"yreader/backend/internal/repository/testutil"
)

// TestBookSourcesRulesGINExplain 验证 10k 书源下 rules 的 @> 查询命中 JSONB GIN 索引。
//
// 种子必须提交后再 EXPLAIN：若在同一个未提交事务内 INSERT + ANALYZE，
// 规划器沿用的仍是旧统计（实测会退回 ix_book_sources_group 的位图扫描），
// 从而无法确定性证明 GIN 可服务 @> 谓词。
func TestBookSourcesRulesGINExplain(t *testing.T) {
	pool := testutil.TestPool(t)
	ctx := context.Background()

	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	userID := testutil.SeedUser(t, seedTx)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	if _, err := pool.Exec(ctx, `
		INSERT INTO book_sources (user_id, book_source_url, book_source_name, rules)
		SELECT $1,
		       'https://seed.example/src-' || i,
		       'seed-' || i,
		       jsonb_build_object('search', jsonb_build_object('bookList', 'div.item-' || (i % 100)))
		  FROM generate_series(0, 9999) AS i`, userID); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE book_sources`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// 单值命中率 1%（约 100 行/10k），顺序扫描在小表上本就便宜；关闭顺序扫描以确定性证明
	// GIN 索引可服务 @> 谓词（与 TestBookshelfExplain 同一手法）。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("set enable_seqscan: %v", err)
	}

	rows, err := tx.Query(ctx, `
		EXPLAIN (FORMAT TEXT)
		SELECT id FROM book_sources
		 WHERE user_id = $1 AND rules @> '{"search":{"bookList":"div.item-42"}}'::jsonb`, userID)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	text := plan.String()
	if strings.Contains(text, "Seq Scan on book_sources") {
		t.Fatalf("plan uses Seq Scan, want GIN index:\n%s", text)
	}
	if !strings.Contains(text, "ix_book_sources_rules_gin") {
		t.Fatalf("plan does not use ix_book_sources_rules_gin:\n%s", text)
	}
}

// TestChaptersTitleTrigramExplain 验证 10k 章节下 pg_trgm 的 % 谓词命中 GIN 索引。
// 注意：similarity(a,b) >= x 是函数调用，无法走 GIN；必须用 % 作为索引谓词，
// 再用 similarity() 做二次过滤（DEF-CODE-11 记录生产 relocate_fuzzy 的同类问题）。
func TestChaptersTitleTrigramExplain(t *testing.T) {
	pool := testutil.TestPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	userID := testutil.SeedUser(t, tx)

	var sourceID uuid.UUID
	if err := tx.QueryRow(context.Background(), `
		INSERT INTO book_sources (user_id, book_source_url, book_source_name, rules)
		VALUES ($1, 'https://seed.example/chapters', 'seed-chapters', '{}'::jsonb)
		RETURNING id`, userID).Scan(&sourceID); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	var bookID uuid.UUID
	if err := tx.QueryRow(context.Background(), `
		INSERT INTO books (user_id, book_type, normalized_name, normalized_author, name, author, active_source_id, active_book_url)
		VALUES ($1, 'online', 'explain-book', '', 'explain-book', '', $2, 'https://seed.example/book')
		RETURNING id`, userID, sourceID).Scan(&bookID); err != nil {
		t.Fatalf("insert book: %v", err)
	}

	for i := 0; i < 10000; i++ {
		chURL := fmt.Sprintf("https://seed.example/ch-%d", i) // 纯字符串
		urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(chURL)))
		var title string
		if i >= 8888 && i < 8898 {
			title = fmt.Sprintf("n:8888:九霄风云录特别篇-%d", i) // 10 条目标特征行
		} else {
			title = fmt.Sprintf("n:%d:普通章节标题%d", i, i)
		}
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO chapters (book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash, is_volume)
			VALUES ($1, $2, $3, $3, $4, $4, $5, $6, false)`,
			bookID, sourceID, i, title, chURL, urlHash); err != nil {
			t.Fatalf("insert chapter %d: %v", i, err)
		}
	}
	if _, err := tx.Exec(context.Background(), `ANALYZE chapters`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// 关闭顺序扫描，确定性证明索引可服务该谓词（等价断言见 TestBookshelfExplain）。
	if _, err := tx.Exec(context.Background(), `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("set enable_seqscan: %v", err)
	}
	// 种子里存在 0.3~0.6 相似度的干扰行（都以 "n:" 前缀 + 数字开头）：在 0.6 阈值下
	// 规划器估计的匹配行数仍偏多，会改走 (book_id, source_id) 的 btree 位图扫描。
	// 抬到 0.85 后索引匹配集合收敛到 10 条目标特征行，GIN 代价 (≈134) 低于 btree (≈788)，
	// 从而确定性证明 ix_chapters_title_trgm 可服务该 % 谓词。
	if _, err := tx.Exec(context.Background(), `SET LOCAL pg_trgm.similarity_threshold = 0.85`); err != nil {
		t.Fatalf("set similarity_threshold: %v", err)
	}

	rows, err := tx.Query(context.Background(), `
		EXPLAIN (FORMAT TEXT)
		SELECT chapter_index, title, similarity(normalized_title, 'n:8888:九霄风云录特别篇') AS sim
		FROM chapters
		WHERE book_id = $1 AND source_id = $2 AND is_volume = false
		  AND normalized_title % 'n:8888:九霄风云录特别篇'
		  AND similarity(normalized_title, 'n:8888:九霄风云录特别篇') >= 0.6
		ORDER BY sim DESC LIMIT 1`, bookID, sourceID)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	text := plan.String()
	if strings.Contains(text, "Seq Scan on chapters") {
		t.Fatalf("plan uses Seq Scan, want trigram GIN:\n%s", text)
	}
	if !strings.Contains(text, "ix_chapters_title_trgm") {
		t.Fatalf("plan does not use ix_chapters_title_trgm:\n%s", text)
	}
}
