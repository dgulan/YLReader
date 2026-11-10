//go:build integration

package repository

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"yreader/backend/internal/repository/testutil"
)

// bulkSeedBookshelf 用 generate_series 批量造 500 本书与 N 条 override，并刷新统计。
func bulkSeedBookshelf(tb testing.TB, q Querier, userID, sourceID uuid.UUID, books, overrides int) {
	tb.Helper()
	ctx := context.Background()
	if _, err := q.Exec(ctx, `
		INSERT INTO books (user_id, book_type, normalized_name, normalized_author, name, author,
		                   status, active_source_id, active_book_url)
		SELECT $1, 'online', 'bench_book_' || g, 'bench_author', 'Bench ' || g, 'Author',
		       'unknown', $2, 'https://example.com/bench/' || g
		FROM generate_series(1, $3) g`, userID, sourceID, books); err != nil {
		tb.Fatalf("批量造书失败: %v", err)
	}
	if overrides > 0 {
		if _, err := q.Exec(ctx, `
			INSERT INTO chapter_source_overrides (user_id, book_id, chapter_index, source_id, chapter_url)
			SELECT $1, b.id, 1, $2, 'https://x.example/' || b.id
			FROM books b WHERE b.user_id = $1 LIMIT $3`, userID, sourceID, overrides); err != nil {
			tb.Fatalf("批量造 override 失败: %v", err)
		}
	}
	if _, err := q.Exec(ctx, `ANALYZE books`); err != nil {
		tb.Fatalf("ANALYZE books: %v", err)
	}
	if _, err := q.Exec(ctx, `ANALYZE chapter_source_overrides`); err != nil {
		tb.Fatalf("ANALYZE chapter_source_overrides: %v", err)
	}
}

// TestBookshelfExplain 验证 v_bookshelf（含 override_count 相关子查询）命中复合索引。
func TestBookshelfExplain(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	src := seedSource(t, tx, userID, "explain")
	bulkSeedBookshelf(t, tx, userID, src.ID, 500, 50)

	ctx := context.Background()

	// 数据自检：50 条 override 且 v_bookshelf 的 override_count 之和为 50。
	var ovCount, sumOverride int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chapter_source_overrides WHERE user_id=$1 AND status='active'`, userID).Scan(&ovCount); err != nil {
		t.Fatalf("统计 override: %v", err)
	}
	if ovCount != 50 {
		t.Fatalf("override 数 = %d, want 50", ovCount)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(override_count),0) FROM v_bookshelf WHERE user_id=$1`, userID).Scan(&sumOverride); err != nil {
		t.Fatalf("统计 override_count: %v", err)
	}
	if sumOverride != 50 {
		t.Fatalf("override_count 总和 = %d, want 50", sumOverride)
	}

	// 50 行的小表下计划器可能选 Seq Scan；强制关闭 seqscan 以证明该复合索引
	// 能服务此相关子查询（正式环境由计划器按代价自行选择）。
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("SET LOCAL: %v", err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM v_bookshelf WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("读取计划: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	rows.Close()
	t.Logf("书架查询执行计划:\n%s", plan.String())

	if !strings.Contains(plan.String(), "ix_chapter_overrides_user_book") {
		t.Fatalf("执行计划未命中 ix_chapter_overrides_user_book")
	}
}

// BenchmarkBookshelf 测量 500 本书 + 50 条 override 时书架列表查询的 p95。
func BenchmarkBookshelf(b *testing.B) {
	pool := testutil.TestPool(b)
	ctx := context.Background()

	// 数据在 b.N 之外一次性提交（并行基准需要连接池可见的已提交数据）。
	userID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, username, password_hash) VALUES ($1, $2, 'bench')`,
		userID, "bench_"+userID.String()); err != nil {
		b.Fatalf("造基准用户: %v", err)
	}
	var sourceID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO book_sources (user_id, book_source_url, book_source_name, header, rules)
		 VALUES ($1, $2, 'BenchSrc', '{}', '{}') RETURNING id`,
		userID, "https://bench.example/"+userID.String()).Scan(&sourceID); err != nil {
		b.Fatalf("造基准书源: %v", err)
	}
	bulkSeedBookshelf(b, pool, userID, sourceID, 500, 50)
	b.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})

	repo := NewBookshelfRepository()
	var mu sync.Mutex
	lat := make([]time.Duration, 0, 1024)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			start := time.Now()
			if _, err := repo.List(ctx, pool, userID); err != nil {
				b.Errorf("List: %v", err)
				return
			}
			d := time.Since(start)
			mu.Lock()
			lat = append(lat, d)
			mu.Unlock()
		}
	})
	b.StopTimer()

	if len(lat) == 0 {
		b.Fatal("无延迟样本")
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p95 := lat[int(float64(len(lat)-1)*0.95)]
	b.Logf("书架列表查询 p95 = %v（样本 %d，最大 %v）", p95, len(lat), lat[len(lat)-1])
	if p95 > 300*time.Millisecond {
		b.Fatalf("p95 = %v，超过 300ms", p95)
	}
}
