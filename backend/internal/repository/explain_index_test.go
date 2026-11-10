//go:build integration

package repository

import (
	"context"
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
//
// 与 TestBookSourcesRulesGINExplain 统一采用「提交式种子」：未提交事务内 INSERT + ANALYZE
// 时规划器仍用旧统计，会退回 (book_id, source_id) btree 位图，无法确定性断言 GIN。
func TestChaptersTitleTrigramExplain(t *testing.T) {
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

	var sourceID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO book_sources (user_id, book_source_url, book_source_name, rules)
		VALUES ($1, 'https://seed.example/chapters', 'seed-chapters', '{}'::jsonb)
		RETURNING id`, userID).Scan(&sourceID); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	var bookID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO books (user_id, book_type, normalized_name, normalized_author, name, author, active_source_id, active_book_url)
		VALUES ($1, 'online', 'explain-book', '', 'explain-book', '', $2, 'https://seed.example/book')
		RETURNING id`, userID, sourceID).Scan(&bookID); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chapters (book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash, is_volume)
		SELECT $1, $2, i, i, t, t,
		       'https://seed.example/ch-' || i,
		       encode(sha256(('https://seed.example/ch-' || i)::bytea), 'hex'),
		       false
		  FROM (SELECT i,
		               CASE WHEN i BETWEEN 8888 AND 8897
		                    THEN 'n:8888:九霄风云录特别篇-' || i
		                    ELSE 'n:' || i || ':普通章节标题' || i END AS t
		          FROM generate_series(0, 9999) AS i) AS x`, bookID, sourceID); err != nil {
		t.Fatalf("seed chapters: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE chapters`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// 关闭顺序扫描，确定性证明索引可服务该谓词（等价断言见 TestBookshelfExplain）。
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("set enable_seqscan: %v", err)
	}
	// 种子里存在 0.3~0.6 相似度的干扰行（都以 "n:" 前缀 + 数字开头）：在 0.6 阈值下
	// 规划器估计的匹配行数仍偏多，会改走 (book_id, source_id) 的 btree 位图扫描。
	// 抬到 0.85 后索引匹配集合收敛到 10 条目标特征行，GIN 代价 (≈134) 低于 btree (≈788)，
	// 从而确定性证明 ix_chapters_title_trgm 可服务该 % 谓词。该阈值是「索引可选用性」验证
	// 手段，不代表生产 relocate_fuzzy 在 0.6 阈值下的实际计划（见 DEF-CODE-11 证据边界）。
	if _, err := tx.Exec(ctx, `SET LOCAL pg_trgm.similarity_threshold = 0.85`); err != nil {
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

// TestRelocateFuzzyExplain 以生产默认阈值 0.6 验证 v1.5 的 relocate_fuzzy 能走
// ix_chapters_title_trgm：函数体内先 `%` 前置谓词（GIN 可服务），再 similarity 二次过滤。
// 种子提交后再 EXPLAIN（与另两个 EXPLAIN 用例同一模式），否则规划器沿用旧统计。
func TestRelocateFuzzyExplain(t *testing.T) {
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

	var sourceID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO book_sources (user_id, book_source_url, book_source_name, rules)
		VALUES ($1, 'https://seed.example/relocate', 'seed-relocate', '{}'::jsonb)
		RETURNING id`, userID).Scan(&sourceID); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	var bookID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO books (user_id, book_type, normalized_name, normalized_author, name, author, active_source_id, active_book_url)
		VALUES ($1, 'online', 'relocate-book', '', 'relocate-book', '', $2, 'https://seed.example/book')
		RETURNING id`, userID, sourceID).Scan(&bookID); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chapters (book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash, is_volume)
		SELECT $1, $2, i, i, t, t,
		       'https://seed.example/rc-' || i,
		       encode(sha256(('https://seed.example/rc-' || i)::bytea), 'hex'),
		       false
		  FROM (SELECT i,
		               CASE WHEN i BETWEEN 8888 AND 8897
		                    THEN 'n:8888:九霄风云录特别篇-' || i
		                    ELSE 'n:' || i || ':普通章节标题' || i END AS t
		          FROM generate_series(0, 9999) AS i) AS x`, bookID, sourceID); err != nil {
		t.Fatalf("seed chapters: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE chapters`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	// 事务级 GUC：SET LOCAL 只在事务内生效，Rollback 后自动失效，绝不污染共享连接池。
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("set enable_seqscan: %v", err)
	}

	// 语义验证：0.6 阈值下最相近的可读章节是 8888 号目标行。
	var gotIndex int
	if err := tx.QueryRow(ctx,
		`SELECT chapter_index FROM relocate_fuzzy($1, $2, $3, 8888, 100, 0.6)`,
		bookID, sourceID, "n:8888:九霄风云录特别篇").Scan(&gotIndex); err != nil {
		t.Fatalf("call relocate_fuzzy: %v", err)
	}
	if gotIndex != 8888 {
		t.Fatalf("relocate_fuzzy index = %d, want 8888", gotIndex)
	}

	explain := func(query string, args ...any) string {
		t.Helper()
		rows, qerr := tx.Query(ctx, query, args...)
		if qerr != nil {
			t.Fatalf("explain: %v", qerr)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var line string
			if serr := rows.Scan(&line); serr != nil {
				t.Fatalf("scan plan: %v", serr)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		return b.String()
	}

	// 宽窗口（范围不裁剪）：% 前置谓词使 trgm GIN 成为最优路径。
	wide := explain(`
		EXPLAIN (FORMAT TEXT)
		SELECT c.chapter_index, c.title,
		       similarity(c.normalized_title, $3) AS sim
		  FROM chapters c
		 WHERE c.book_id = $1 AND c.source_id = $2 AND c.is_volume = false
		   AND c.chapter_index BETWEEN 0 AND 9999
		   AND c.normalized_title % $3
		   AND similarity(c.normalized_title, $3) >= 0.6
		 ORDER BY sim DESC, abs(c.chapter_index - 8888) ASC
		 LIMIT 1`, bookID, sourceID, "n:8888:九霄风云录特别篇")
	if strings.Contains(wide, "Seq Scan on chapters") {
		t.Fatalf("wide-window plan uses Seq Scan, want GIN index:\n%s", wide)
	}
	if !strings.Contains(wide, "ix_chapters_title_trgm") {
		t.Fatalf("wide-window plan does not use ix_chapters_title_trgm:\n%s", wide)
	}

	// 默认 ±100 窗口：范围扫描只有约 200 行，(book,source,index) 唯一索引本身更便宜，
	// 规划器可能优先选它；此处只要求不走 Seq Scan（GIN 的价值在宽窗口与大目录回退）。
	narrow := explain(`
		EXPLAIN (FORMAT TEXT)
		SELECT c.chapter_index, c.title,
		       similarity(c.normalized_title, $3) AS sim
		  FROM chapters c
		 WHERE c.book_id = $1 AND c.source_id = $2 AND c.is_volume = false
		   AND c.chapter_index BETWEEN 8788 AND 8988
		   AND c.normalized_title % $3
		   AND similarity(c.normalized_title, $3) >= 0.6
		 ORDER BY sim DESC, abs(c.chapter_index - 8888) ASC
		 LIMIT 1`, bookID, sourceID, "n:8888:九霄风云录特别篇")
	if strings.Contains(narrow, "Seq Scan on chapters") {
		t.Fatalf("narrow-window plan uses Seq Scan:\n%s", narrow)
	}
}
