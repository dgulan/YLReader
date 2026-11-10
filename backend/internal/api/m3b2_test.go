//go:build integration

package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

// serviceJobHolder 保存测试中启动的 Worker 池停止函数。
type serviceJobHolder struct {
	stop func(context.Context)
}

func sp(s string) *string { return &s }

// TestM3b2MappingsCRUD 覆盖 Mappings 列表/新增/删除与唯一约束。
func TestM3b2MappingsCRUD(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	s1 := seedSource(t, pool, userID, "http://mock-m1.example/", `{}`)
	if status, _ := createBook(t, base, token, "映射之书", s1, "http://mock-m1.example/book/1"); status != http.StatusCreated {
		t.Fatalf("create book = %d", status)
	}
	bookID := dbString(t, pool, `SELECT id::text FROM books WHERE user_id=$1 AND name='映射之书'`, userID)
	s2 := seedSource(t, pool, userID, "http://mock-m2.example/", `{}`)

	// 初始：仅活跃源一条绑定（createBook 已建），GET 必须是数组。
	status, raw := doJSON(t, http.MethodGet, base+"/books/"+bookID+"/mappings", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list mappings = %d", status)
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal mappings: %v (%s)", err, raw)
	}
	if len(list) != 1 {
		t.Fatalf("initial mappings = %d, want 1", len(list))
	}

	// 新增备用源绑定 → 201；重复 → 409。
	body := mustJSON(t, map[string]any{"source_id": s2, "book_url": "http://mock-m2.example/book/1"})
	status, raw = doJSON(t, http.MethodPost, base+"/books/"+bookID+"/mappings", token, body)
	if status != http.StatusCreated {
		t.Fatalf("add mapping = %d (%s)", status, raw)
	}
	var created struct {
		Id         uuid.UUID `json:"id"`
		SyncStatus string    `json:"sync_status"`
		SourceId   uuid.UUID `json:"source_id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("unmarshal mapping: %v", err)
	}
	if created.SyncStatus != "pending" || created.SourceId != s2 {
		t.Fatalf("created mapping = %+v, want pending/s2", created)
	}
	if status, _ = doJSON(t, http.MethodPost, base+"/books/"+bookID+"/mappings", token, body); status != http.StatusConflict {
		t.Fatalf("duplicate mapping = %d, want 409", status)
	}

	// 删除备用源绑定 → 204；再删 → 404。
	if status, _ = doJSON(t, http.MethodDelete, base+"/books/"+bookID+"/mappings/"+created.Id.String(), token, nil); status != http.StatusNoContent {
		t.Fatalf("delete mapping = %d, want 204", status)
	}
	if status, _ = doJSON(t, http.MethodDelete, base+"/books/"+bookID+"/mappings/"+created.Id.String(), token, nil); status != http.StatusNotFound {
		t.Fatalf("delete mapping again = %d, want 404", status)
	}
}

// TestM3b2MappingActiveDeleteRejected 删除活跃源绑定必须 409（契约红线）。
func TestM3b2MappingActiveDeleteRejected(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	s1 := seedSource(t, pool, userID, "http://mock-a1.example/", `{}`)
	if status, _ := createBook(t, base, token, "活跃映射书", s1, "http://mock-a1.example/book/1"); status != http.StatusCreated {
		t.Fatalf("create book = %d", status)
	}
	bookID := dbString(t, pool, `SELECT id::text FROM books WHERE user_id=$1 AND name='活跃映射书'`, userID)
	mappingID := dbString(t, pool, `SELECT id::text FROM book_source_mappings WHERE book_id=$1 AND sync_status='active'`, uuid.MustParse(bookID))

	status, raw := doJSON(t, http.MethodDelete, base+"/books/"+bookID+"/mappings/"+mappingID, token, nil)
	if status != http.StatusConflict {
		t.Fatalf("delete active mapping = %d, want 409 (%s)", status, raw)
	}
}

// TestM3b2MappingTenantAndBadSource 覆盖越权 404 与源不可用 422。
func TestM3b2MappingTenantAndBadSource(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	s1 := seedSource(t, pool, userID, "http://mock-b1.example/", `{}`)
	if status, _ := createBook(t, base, token, "越权映射书", s1, "http://mock-b1.example/book/1"); status != http.StatusCreated {
		t.Fatalf("create book = %d", status)
	}
	bookID := dbString(t, pool, `SELECT id::text FROM books WHERE user_id=$1 AND name='越权映射书'`, userID)

	// 另一个用户：全部 404。
	_, otherName, otherPass := seedUser(t, pool)
	otherToken := login(t, base, otherName, otherPass)
	if status, _ := doJSON(t, http.MethodGet, base+"/books/"+bookID+"/mappings", otherToken, nil); status != http.StatusNotFound {
		t.Fatalf("tenant list = %d, want 404", status)
	}
	otherSrc := seedSource(t, pool, userID, "http://mock-b2.example/", `{}`)
	body := mustJSON(t, map[string]any{"source_id": otherSrc, "book_url": "http://mock-b2.example/book/1"})
	if status, _ := doJSON(t, http.MethodPost, base+"/books/"+bookID+"/mappings", otherToken, body); status != http.StatusNotFound {
		t.Fatalf("tenant add = %d, want 404", status)
	}

	// 不存在的 source_id → 422。
	bad := mustJSON(t, map[string]any{"source_id": uuid.New(), "book_url": "http://mock-b2.example/book/2"})
	if status, _ := doJSON(t, http.MethodPost, base+"/books/"+bookID+"/mappings", token, bad); status != http.StatusUnprocessableEntity {
		t.Fatalf("bad source add = %d, want 422", status)
	}
}

// TestM3b2CacheRejects 本地书与无活跃源都必须 422。
func TestM3b2CacheRejects(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	localID := uuid.New()
	localSrc := seedSource(t, pool, userID, "local://"+userID.String(), `{}`)
	if _, err := pool.Exec(context.Background(), `INSERT INTO books (id, user_id, book_type, normalized_name, normalized_author, name, author, status, active_source_id, active_book_url)
		VALUES ($1,$2,'local','本地之书','','本地之书','','unknown',$3,$4)`,
		localID, userID, localSrc, "local:"+localID.String()); err != nil {
		t.Fatalf("insert local book: %v", err)
	}
	if status, raw := doJSON(t, http.MethodPost, base+"/books/"+localID.String()+"/cache", token, mustJSON(t, map[string]any{})); status != http.StatusUnprocessableEntity {
		t.Fatalf("cache local book = %d, want 422 (%s)", status, raw)
	}

	onlineID := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO books (id, user_id, book_type, normalized_name, normalized_author, name, author, status)
		VALUES ($1,$2,'online','无源之书','','无源之书','','unknown')`, onlineID, userID); err != nil {
		t.Fatalf("insert online book: %v", err)
	}
	if status, raw := doJSON(t, http.MethodPost, base+"/books/"+onlineID.String()+"/cache", token, mustJSON(t, map[string]any{})); status != http.StatusUnprocessableEntity {
		t.Fatalf("cache without source = %d, want 422 (%s)", status, raw)
	}
}

// TestM3b2CacheCreateAndDedup 覆盖 202 + payload 落库 + 同类任务防重 409。
func TestM3b2CacheCreateAndDedup(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	s1 := seedSource(t, pool, userID, "http://mock-c1.example/", `{
		"toc": {"chapterList":"a.ch","chapterName":"@text","chapterUrl":"@href"},
		"content": {"content":"body@text"}
	}`)
	if status, _ := createBook(t, base, token, "缓存之书", s1, "http://mock-c1.example/book/1"); status != http.StatusCreated {
		t.Fatalf("create book = %d", status)
	}
	bookID := dbString(t, pool, `SELECT id::text FROM books WHERE user_id=$1 AND name='缓存之书'`, userID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID)
	})

	from, to := 0, 2
	status, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID+"/cache", token,
		mustJSON(t, map[string]any{"from_chapter": from, "to_chapter": to}))
	if status != http.StatusAccepted {
		t.Fatalf("cache book = %d, want 202 (%s)", status, raw)
	}
	var job struct {
		Id       uuid.UUID `json:"id"`
		Status   string    `json:"status"`
		Progress int       `json:"progress"`
		Type     string    `json:"type"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatalf("unmarshal job: %v", err)
	}
	if job.Status != "queued" || job.Progress != 0 || job.Type != "full_cache" {
		t.Fatalf("job = %+v, want queued/0/full_cache", job)
	}
	if got := dbString(t, pool, `SELECT payload->>'from_chapter' FROM jobs WHERE id=$1`, job.Id); got != "0" {
		t.Fatalf("payload from_chapter = %q, want 0", got)
	}
	if got := dbString(t, pool, `SELECT payload->>'to_chapter' FROM jobs WHERE id=$1`, job.Id); got != "2" {
		t.Fatalf("payload to_chapter = %q, want 2", got)
	}
	if status, raw = doJSON(t, http.MethodPost, base+"/books/"+bookID+"/cache", token, mustJSON(t, map[string]any{})); status != http.StatusConflict {
		t.Fatalf("duplicate cache = %d, want 409 (%s)", status, raw)
	}
}

// buildMockChapterBook 造一本 100 章在线书（目录/正文均由 mock fetcher 提供）。
func buildMockChapterBook(t *testing.T, pool *pgxpool.Pool, base, token string, userID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	const totalMockChapters = 100
	srcID := seedSource(t, pool, userID, "http://mock-cache.example/", `{
		"toc": {"chapterList":"a.ch","chapterName":"@text","chapterUrl":"@href"},
		"content": {"content":"body@text"}
	}`)
	status, raw := doJSON(t, http.MethodPost, base+"/books", token, mustJSON(t, map[string]any{
		"name": name, "author": "作者", "source_id": srcID, "book_url": "http://mock-cache.example/book/1",
	}))
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create book = %d (%s)", status, raw)
	}
	var book struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &book); err != nil {
		t.Fatalf("unmarshal book: %v", err)
	}
	for i := 0; i < totalMockChapters; i++ {
		idx := int32(i)
		chURL := fmt.Sprintf("http://mock-cache.example/book/1/ch%d", i+1)
		urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(chURL)))
		if _, err := pool.Exec(context.Background(), `INSERT INTO chapters
			(book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			book.Id, srcID, idx, idx, fmt.Sprintf("第%d章", i+1),
			textnorm.NormalizeChapter(fmt.Sprintf("第%d章", i+1)), chURL, urlHash); err != nil {
			t.Fatalf("insert chapter %d: %v", i, err)
		}
	}
	return book.Id
}

func mockChapterFetcher(delay time.Duration) service.Fetcher {
	return mockFetcher{fn: func(_ context.Context, rawURL string, _ http.Header) (*fetcher.Response, error) {
		if strings.HasSuffix(rawURL, "/toc") {
			var b strings.Builder
			for i := 0; i < 100; i++ {
				fmt.Fprintf(&b, `<a class="ch" href="/book/1/ch%d">第%d章</a>`, i+1, i+1)
			}
			return &fetcher.Response{StatusCode: 200, Body: []byte("<html><body>" + b.String() + "</body></html>"), FinalURL: rawURL}, nil
		}
		if strings.Contains(rawURL, "/ch") {
			if delay > 0 {
				time.Sleep(delay)
			}
			return &fetcher.Response{StatusCode: 200, Body: []byte("<html><body><p>章节正文内容</p></body></html>"), FinalURL: rawURL}, nil
		}
		return &fetcher.Response{StatusCode: 404}, nil
	}}
}

// TestM3b2JobLifecycle 覆盖 202 progress==0 → 轮询 succeeded/progress==100。
func TestM3b2JobLifecycle(t *testing.T) {
	var jobs *serviceJobHolder
	ts, pool := newTestServerWithPool(t, mockChapterFetcher(0), func(_ *pgxpool.Pool, deps *api.Deps) {
		deps.Jobs.StartWorkers(context.Background())
		jobs = &serviceJobHolder{stop: deps.Jobs.StopWorkers}
	})
	t.Cleanup(func() {
		if jobs != nil {
			jobs.stop(context.Background())
		}
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	bookID := buildMockChapterBook(t, pool, base, token, userID, "百章之书")
	status, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/cache", token, mustJSON(t, map[string]any{}))
	if status != http.StatusAccepted {
		t.Fatalf("cache book = %d, want 202 (%s)", status, raw)
	}
	var initJob struct {
		Id       uuid.UUID `json:"id"`
		Progress int       `json:"progress"`
	}
	if err := json.Unmarshal(raw, &initJob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if initJob.Progress != 0 {
		t.Fatalf("initial progress = %d, want 0", initJob.Progress)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		_, qRaw := doJSON(t, http.MethodGet, base+"/jobs/"+initJob.Id.String(), token, nil)
		var j struct {
			Status   string `json:"status"`
			Progress int    `json:"progress"`
		}
		if err := json.Unmarshal(qRaw, &j); err != nil {
			t.Fatalf("unmarshal job: %v (%s)", err, qRaw)
		}
		if j.Status == "succeeded" {
			if j.Progress != 100 {
				t.Fatalf("final progress = %d, want 100", j.Progress)
			}
			if n := dbInt(t, pool, `SELECT count(*) FROM jobs WHERE id=$1 AND status='succeeded'`, initJob.Id); n != 1 {
				t.Fatalf("succeeded rows = %d", n)
			}
			return
		}
	}
	t.Fatalf("job not completed in 60s")
}

// TestM3b2JobCancel 覆盖运行中取消 → canceled，终态再取消 → 409。
func TestM3b2JobCancel(t *testing.T) {
	var jobs *serviceJobHolder
	ts, pool := newTestServerWithPool(t, mockChapterFetcher(50*time.Millisecond), func(_ *pgxpool.Pool, deps *api.Deps) {
		deps.Jobs.StartWorkers(context.Background())
		jobs = &serviceJobHolder{stop: deps.Jobs.StopWorkers}
	})
	t.Cleanup(func() {
		if jobs != nil {
			jobs.stop(context.Background())
		}
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	bookID := buildMockChapterBook(t, pool, base, token, userID, "取消之书")
	status, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/cache", token, mustJSON(t, map[string]any{}))
	if status != http.StatusAccepted {
		t.Fatalf("cache book = %d (%s)", status, raw)
	}
	var job struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// 等待开始执行（progress>0 或 running），再取消。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := dbString(t, pool, `SELECT status FROM jobs WHERE id=$1`, job.Id)
		if st == "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status, _ = doJSON(t, http.MethodDelete, base+"/jobs/"+job.Id.String(), token, nil); status != http.StatusNoContent {
		t.Fatalf("cancel job = %d, want 204", status)
	}

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, qRaw := doJSON(t, http.MethodGet, base+"/jobs/"+job.Id.String(), token, nil)
		var j struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(qRaw, &j)
		if j.Status == "canceled" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := dbString(t, pool, `SELECT status FROM jobs WHERE id=$1`, job.Id); got != "canceled" {
		t.Fatalf("job status = %q, want canceled", got)
	}
	if status, _ = doJSON(t, http.MethodDelete, base+"/jobs/"+job.Id.String(), token, nil); status != http.StatusConflict {
		t.Fatalf("cancel canceled job = %d, want 409", status)
	}
}

// TestM3b2ConcurrentSameNamePreallocatedID（DEF-CODE-05）：
// 基准书 + 3 并发同名同作者不同内容导入 → 全部 201，DB 严格 4 本，无嵌套括号。
func TestM3b2ConcurrentSameNamePreallocatedID(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	status, raw := importBook(t, base, token, "基准书.txt", sp("基准书"), sp("作者甲"),
		[]byte("第一章 甲\n正文甲\n第二章 乙\n正文乙\n"))
	if status != http.StatusCreated {
		t.Fatalf("base import = %d (%s)", status, raw)
	}

	const n = 3
	statuses := make([]int, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := []byte(fmt.Sprintf("第一章 内容%d\n正文%d\n第二章 结束%d\n尾%d\n", i, i, i, i))
			st, _ := importBook(t, base, token, "基准书.txt", nil, nil, body)
			statuses[i] = st
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, st := range statuses {
		if st != http.StatusCreated {
			t.Fatalf("concurrent import %d = %d, want 201 (statuses=%v)", i, st, statuses)
		}
	}
	got := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND name LIKE '基准书%'`, userID)
	if got != 4 {
		t.Fatalf("books after concurrent import = %d, want 4", got)
	}
	// 无嵌套括号：最多 (1)~(3)，不得出现 ((1))。
	bad := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND name LIKE '基准书%(%(%'`, userID)
	if bad != 0 {
		t.Fatalf("nested parentheses books = %d, want 0", bad)
	}
	t.Logf("DEF-CODE-05: 3-way concurrent same-name import elapsed=%s, names=%v",
		elapsed, bookNames(t, pool, userID))
}

func bookNames(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT name FROM books WHERE user_id=$1 AND name LIKE '基准书%' ORDER BY name`, userID)
	if err != nil {
		t.Fatalf("query names: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan name: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// TestM3b2CacheSourceMismatchRejected（v1.3）：显式 source_id 必须等于活跃源，否则 422。
func TestM3b2CacheSourceMismatchRejected(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	rules := `{"toc":{"chapterList":"a.ch","chapterName":"@text","chapterUrl":"@href"},"content":{"content":"body@text"}}`
	s1 := seedSource(t, pool, userID, "http://mock-gate1.example/", rules)
	s2 := seedSource(t, pool, userID, "http://mock-gate2.example/", rules)
	if status, created := createBook(t, base, token, "门禁之书", s1, "http://mock-gate1.example/book/1"); status != http.StatusCreated {
		t.Fatalf("create book = %d (%+v)", status, created)
	}
	bookID := dbString(t, pool, `SELECT id::text FROM books WHERE user_id=$1 AND name='门禁之书'`, userID)

	// 显式源 != 活跃源 → 422（多源预热未支持）。
	if status, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID+"/cache", token, mustJSON(t, map[string]any{"source_id": s2})); status != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched source cache = %d, want 422 (%s)", status, raw)
	}
	// 显式源 == 活跃源 → 202。
	if status, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID+"/cache", token, mustJSON(t, map[string]any{"source_id": s1})); status != http.StatusAccepted {
		t.Fatalf("matching source cache = %d, want 202 (%s)", status, raw)
	}
}

// TestM3b2CacheAllChaptersFailed（v1.3）：全部章节抓取失败 → failed / content_unavailable / progress=0。
func TestM3b2CacheAllChaptersFailed(t *testing.T) {
	var jobs *serviceJobHolder
	failing := mockFetcher{fn: func(_ context.Context, rawURL string, _ http.Header) (*fetcher.Response, error) {
		if strings.HasSuffix(rawURL, "/toc") {
			var b strings.Builder
			for i := 0; i < 100; i++ {
				fmt.Fprintf(&b, `<a class="ch" href="/book/1/ch%d">第%d章</a>`, i+1, i+1)
			}
			return &fetcher.Response{StatusCode: 200, Body: []byte("<html><body>" + b.String() + "</body></html>"), FinalURL: rawURL}, nil
		}
		return &fetcher.Response{StatusCode: 500}, nil
	}}
	ts, pool := newTestServerWithPool(t, failing, func(_ *pgxpool.Pool, deps *api.Deps) {
		deps.Jobs.StartWorkers(context.Background())
		jobs = &serviceJobHolder{stop: deps.Jobs.StopWorkers}
	})
	t.Cleanup(func() {
		if jobs != nil {
			jobs.stop(context.Background())
		}
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id=$1`, userID) })

	bookID := buildMockChapterBook(t, pool, base, token, userID, "全失败之书")
	status, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/cache", token, mustJSON(t, map[string]any{}))
	if status != http.StatusAccepted {
		t.Fatalf("cache book = %d, want 202 (%s)", status, raw)
	}
	var job struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if st := dbString(t, pool, `SELECT status FROM jobs WHERE id=$1`, job.Id); st == "failed" {
			if code := dbString(t, pool, `SELECT coalesce(error_code,'') FROM jobs WHERE id=$1`, job.Id); code != "content_unavailable" {
				t.Fatalf("error_code = %q, want content_unavailable", code)
			}
			if p := dbInt(t, pool, `SELECT progress FROM jobs WHERE id=$1`, job.Id); p != 0 {
				t.Fatalf("progress = %d, want 0", p)
			}
			return
		}
	}
	t.Fatalf("job did not reach failed within 60s")
}
