//go:build integration

package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/pkg/textnorm"
	"yreader/backend/internal/service"
)

// S5-2（DEF-OAS-08）：多源缓存预热。
const msRules = `{"toc":{"chapterList":"a.ch","chapterName":"@text","chapterUrl":"@href"},"content":{"content":"body@text"}}`

type msBook struct {
	BookID       uuid.UUID
	ActiveSource uuid.UUID
	OtherSource  uuid.UUID
}

// insertChapters 为目标源插入 n 条可读章节；urlTmpl 形如 http://host/book/1/ch%d。
func insertChapters(t *testing.T, pool *pgxpool.Pool, bookID, srcID uuid.UUID, n int, urlTmpl string) {
	t.Helper()
	for i := 0; i < n; i++ {
		idx := int32(i)
		chURL := fmt.Sprintf(urlTmpl, i+1)
		title := fmt.Sprintf("第%d章", i+1)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(chURL)))
		if _, err := pool.Exec(context.Background(), `INSERT INTO chapters
			(book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			bookID, srcID, idx, idx, title, textnorm.NormalizeChapter(title), chURL, hash); err != nil {
			t.Fatalf("insert chapter %d: %v", i, err)
		}
	}
}

// seedMultiSourceBook 建一本双源书：s1 为活跃源，s2 通过 mappings 绑定；两源各插若干章节。
func seedMultiSourceBook(t *testing.T, pool *pgxpool.Pool, base, token string, userID uuid.UUID, name string, s1Chapters, s2Chapters int) msBook {
	t.Helper()
	s1 := seedSource(t, pool, userID, "http://mock-ms1.example/", msRules)
	s2 := seedSource(t, pool, userID, "http://mock-ms2.example/", msRules)
	status, created := createBook(t, base, token, name, s1, "http://mock-ms1.example/book/1")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create book = %d (%+v)", status, created)
	}
	insertChapters(t, pool, created.ID, s1, s1Chapters, "http://mock-ms1.example/book/1/ch%d")
	if status, raw := doJSON(t, http.MethodPost, base+"/books/"+created.ID.String()+"/mappings", token, mustJSON(t, map[string]any{
		"book_url": "http://mock-ms2.example/book/1", "source_id": s2,
	})); status != http.StatusCreated {
		t.Fatalf("add mapping = %d (%s)", status, raw)
	}
	insertChapters(t, pool, created.ID, s2, s2Chapters, "http://mock-ms2.example/book/1/ch%d")
	return msBook{BookID: created.ID, ActiveSource: s1, OtherSource: s2}
}

// msFetcher 对任意 URL 返回成功正文，并统计抓取次数。
type msFetcher struct {
	mu    sync.Mutex
	calls int
}

func (f *msFetcher) Get(_ context.Context, rawURL string, _ http.Header) (*fetcher.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return &fetcher.Response{StatusCode: 200, Body: []byte("<html><body><p>章节正文内容</p></body></html>"), FinalURL: rawURL}, nil
}

func (f *msFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func cacheJob(t *testing.T, base, token string, bookID uuid.UUID, body map[string]any) (int, []byte) {
	t.Helper()
	return doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/cache", token, mustJSON(t, body))
}

// waitJobDone 轮询任务至终态（30s 上限），返回 status 与 result 字典。
func waitJobDone(t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID) (string, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var raw []byte
		if err := pool.QueryRow(context.Background(), `SELECT status, coalesce(result,'{}'::jsonb) FROM jobs WHERE id=$1`, jobID).
			Scan(&status, &raw); err != nil {
			t.Fatalf("read job: %v", err)
		}
		if status == "succeeded" || status == "failed" || status == "canceled" {
			var res map[string]any
			if err := json.Unmarshal(raw, &res); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			return status, res
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %s not finished in 30s", jobID)
	return "", nil
}

// startPool 启动 WorkerPool 并注册清理。

func TestM3b2CacheActiveSourceAccepted(t *testing.T) {
	ts, pool := newTestServerWithPool(t, &msFetcher{}, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	mb := seedMultiSourceBook(t, pool, base, token, userID, "显式活跃源书", 3, 3)
	if status, raw := cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": mb.ActiveSource}); status != http.StatusAccepted {
		t.Fatalf("explicit active source = %d, want 202 (%s)", status, raw)
	}
}

func TestCacheMultiSourcePreheat(t *testing.T) {
	f := &msFetcher{}
	var jobs *service.JobService
	ts, pool := newTestServerWithPool(t, f, func(_ *pgxpool.Pool, deps *api.Deps) {
		deps.Jobs.StartWorkers(context.Background())
		jobs = deps.Jobs
	})
	t.Cleanup(func() {
		if jobs != nil {
			jobs.StopWorkers(context.Background())
		}
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	mb := seedMultiSourceBook(t, pool, base, token, userID, "双源预热书", 3, 4)

	var j1, j2 struct {
		Id uuid.UUID `json:"id"`
	}
	status, raw := cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": mb.ActiveSource})
	if status != http.StatusAccepted {
		t.Fatalf("cache s1 = %d (%s)", status, raw)
	}
	if err := json.Unmarshal(raw, &j1); err != nil {
		t.Fatalf("unmarshal j1: %v", err)
	}
	status, raw = cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": mb.OtherSource})
	if status != http.StatusAccepted {
		t.Fatalf("cache s2 = %d (%s)", status, raw)
	}
	if err := json.Unmarshal(raw, &j2); err != nil {
		t.Fatalf("unmarshal j2: %v", err)
	}

	st1, res1 := waitJobDone(t, pool, j1.Id)
	st2, res2 := waitJobDone(t, pool, j2.Id)
	if st1 != "succeeded" || st2 != "succeeded" {
		t.Fatalf("job statuses = %s/%s, want succeeded", st1, st2)
	}
	if got := res1["target_source_id"]; got != mb.ActiveSource.String() {
		t.Fatalf("j1 target_source_id = %v, want %s", got, mb.ActiveSource)
	}
	if got := res2["target_source_id"]; got != mb.OtherSource.String() {
		t.Fatalf("j2 target_source_id = %v, want %s", got, mb.OtherSource)
	}
	if res1["total"] == res2["total"] {
		t.Fatalf("两源章节数应不同（3 vs 4），total = %v/%v", res1["total"], res2["total"])
	}
	if f.count() == 0 {
		t.Fatalf("fetcher 未被调用")
	}
}

func TestCacheNonActiveSourceDoesNotChangeActive(t *testing.T) {
	var jobs *service.JobService
	ts, pool := newTestServerWithPool(t, &msFetcher{}, func(_ *pgxpool.Pool, deps *api.Deps) {
		deps.Jobs.StartWorkers(context.Background())
		jobs = deps.Jobs
	})
	t.Cleanup(func() {
		if jobs != nil {
			jobs.StopWorkers(context.Background())
		}
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	mb := seedMultiSourceBook(t, pool, base, token, userID, "不改活跃源书", 2, 2)
	status, raw := cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": mb.OtherSource})
	if status != http.StatusAccepted {
		t.Fatalf("cache s2 = %d (%s)", status, raw)
	}
	var job struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st, _ := waitJobDone(t, pool, job.Id); st != "succeeded" {
		t.Fatalf("job status = %s, want succeeded", st)
	}
	if got := dbString(t, pool, `SELECT active_source_id::text FROM books WHERE id=$1`, mb.BookID); got != mb.ActiveSource.String() {
		t.Fatalf("books.active_source_id = %s, want %s", got, mb.ActiveSource)
	}
}

func TestCacheNonActiveSourceWithoutChaptersRejected(t *testing.T) {
	ts, pool := newTestServerWithPool(t, &msFetcher{}, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	mb := seedMultiSourceBook(t, pool, base, token, userID, "无章节源书", 2, 0)
	status, raw := cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": mb.OtherSource})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("cache empty source = %d, want 422 (%s)", status, raw)
	}
	if !strings.Contains(string(raw), "relocation_failed") {
		t.Fatalf("error body = %s, want relocation_failed", raw)
	}
}

func TestCacheSameSourceDedup(t *testing.T) {
	// 不启动 Worker：首个任务保持 queued，第二次同书同源必须命中 409 防重。
	ts, pool := newTestServerWithPool(t, &msFetcher{}, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	mb := seedMultiSourceBook(t, pool, base, token, userID, "同源防重书", 2, 2)
	if status, raw := cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": mb.OtherSource}); status != http.StatusAccepted {
		t.Fatalf("first cache = %d (%s)", status, raw)
	}
	if status, raw := cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": mb.OtherSource}); status != http.StatusConflict {
		t.Fatalf("second cache same source = %d, want 409 (%s)", status, raw)
	}
}

// TestCacheActiveSourceMappingMissingTolerated：活跃源 mapping 缺失时仍允许默认预热，并打 Warn。
func TestCacheActiveSourceMappingMissingTolerated(t *testing.T) {
	var buf bytes.Buffer
	var jobs *service.JobService
	ts, pool := newTestServerWithPool(t, &msFetcher{}, func(pool *pgxpool.Pool, deps *api.Deps) {
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		wp := service.NewWorkerPool(pool, 2, logger)
		jobs = service.NewJobService(pool, deps.Content, wp, logger)
		wp.SetExecuteFunc(jobs.ExecuteJob)
		deps.Jobs = jobs
		jobs.StartWorkers(context.Background())
	})
	t.Cleanup(func() {
		if jobs != nil {
			jobs.StopWorkers(context.Background())
		}
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	mb := seedMultiSourceBook(t, pool, base, token, userID, "缺映射书", 2, 2)
	if _, err := pool.Exec(context.Background(), `DELETE FROM book_source_mappings WHERE book_id=$1 AND source_id=$2`, mb.BookID, mb.ActiveSource); err != nil {
		t.Fatalf("delete active mapping: %v", err)
	}
	status, raw := cacheJob(t, base, token, mb.BookID, map[string]any{})
	if status != http.StatusAccepted {
		t.Fatalf("default cache without active mapping = %d, want 202 (%s)", status, raw)
	}
	var job struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st, _ := waitJobDone(t, pool, job.Id); st != "succeeded" {
		t.Fatalf("job status = %s, want succeeded", st)
	}
	if !strings.Contains(buf.String(), "active source mapping missing") {
		t.Fatalf("日志未包含 Warn：%s", buf.String())
	}
}

// TestCacheSameBookDifferentSourceConcurrent：同书两源并发均 202（锁粒度为 book+source）。
func TestCacheSameBookDifferentSourceConcurrent(t *testing.T) {
	var jobs *service.JobService
	ts, pool := newTestServerWithPool(t, &msFetcher{}, func(_ *pgxpool.Pool, deps *api.Deps) {
		deps.Jobs.StartWorkers(context.Background())
		jobs = deps.Jobs
	})
	t.Cleanup(func() {
		if jobs != nil {
			jobs.StopWorkers(context.Background())
		}
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	mb := seedMultiSourceBook(t, pool, base, token, userID, "跨源并发热书", 3, 3)
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i, src := range []uuid.UUID{mb.ActiveSource, mb.OtherSource} {
		wg.Add(1)
		go func(i int, src uuid.UUID) {
			defer wg.Done()
			st, _ := cacheJob(t, base, token, mb.BookID, map[string]any{"source_id": src})
			statuses[i] = st
		}(i, src)
	}
	wg.Wait()
	if statuses[0] != http.StatusAccepted || statuses[1] != http.StatusAccepted {
		t.Fatalf("concurrent statuses = %v, want [202 202]", statuses)
	}
}
