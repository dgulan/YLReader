//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/pkg/cache"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/repository/testutil"
	"yreader/backend/internal/service"
)

type mockFetcher struct {
	fn func(context.Context, string, http.Header) (*fetcher.Response, error)
}

func (m mockFetcher) Get(ctx context.Context, u string, h http.Header) (*fetcher.Response, error) {
	return m.fn(ctx, u, h)
}

func newTestServer(t *testing.T, ft service.Fetcher) (*httptest.Server, *pgxpool.Pool) {
	return newTestServerOpt(t, ft, nil)
}

// testCache 选择正文缓存实现：设置 REDIS_ADDR 时使用真实 Redis，否则使用进程内 Memory。
// 真实 Redis 用于核对 TTL / key 布局，Memory 保证无外部依赖时测试可重复执行。
func testCache() cache.Cache {
	if addr := strings.TrimSpace(os.Getenv("REDIS_ADDR")); addr != "" {
		return cache.NewRedis(addr, 2*time.Second)
	}
	return cache.NewMemory()
}

// newTestServerOpt 允许测试替换 Deps 中的服务（例如注入 sleepFunc 可控的 TOCService）。
func newTestServerOpt(t *testing.T, ft service.Fetcher, mutate func(deps *api.Deps)) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	return newTestServerWithPool(t, ft, func(_ *pgxpool.Pool, deps *api.Deps) {
		if mutate != nil {
			mutate(deps)
		}
	})
}

// newTestServerWithPool 与 newTestServerOpt 相同，但把 pool 一并交给 mutate，
// 便于在构造期注入依赖 pool 的替身服务（例如写失败的 Storage）。
func newTestServerWithPool(t *testing.T, ft service.Fetcher, mutate func(pool *pgxpool.Pool, deps *api.Deps)) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.TestPool(t)
	if ft == nil {
		ft = fetcher.New(fetcher.Config{})
	}
	sb := sandbox.New(sandbox.Config{ScriptTimeout: 400 * time.Millisecond}, nil)
	store, err := storage.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}

	deps := api.Deps{
		Auth:         service.NewAuthService(pool, []byte("test-secret"), time.Minute, time.Hour),
		Sources:      service.NewSourceService(pool),
		Debug:        service.NewDebugService(pool, ft, sb),
		Search:       service.NewSearchService(pool, ft, sb),
		Books:        service.NewBookService(pool, store, nil),
		Progress:     service.NewProgressService(pool),
		Mapping:      service.NewMappingService(pool),
		Overrides:    service.NewOverrideService(pool),
		TOC:          service.NewTOCService(pool, ft, sb),
		Content:      service.NewContentService(pool, ft, sb, testCache(), store, nil),
		Import:       service.NewImportService(pool, store, nil),
		Settings:     service.NewSettingsService(pool, nil),
		Bookmarks:    service.NewBookmarkService(pool, nil),
		ReplaceRules: service.NewReplaceRuleService(pool, nil),
		Storage:      store,
		DBPing:       func(ctx context.Context) error { return pool.Ping(ctx) },
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps.Log = logger
	deps.Books = service.NewBookService(pool, store, logger)
	deps.Content = service.NewContentService(pool, ft, sb, testCache(), store, logger)
	deps.Import = service.NewImportService(pool, store, logger)
	deps.Settings = service.NewSettingsService(pool, logger)
	deps.Bookmarks = service.NewBookmarkService(pool, logger)
	deps.ReplaceRules = service.NewReplaceRuleService(pool, logger)
	// 后台任务：WorkerPool 默认不启动（避免跨测试抢占全局 jobs 队列），
	// 需要执行的用例通过 mutate 捕获 deps.Jobs 后自行 StartWorkers。
	wp := service.NewWorkerPool(pool, 2, logger)
	deps.Jobs = service.NewJobService(pool, deps.Content, wp, logger)
	wp.SetExecuteFunc(deps.Jobs.ExecuteJob)
	if mutate != nil {
		mutate(pool, &deps)
	}
	srv := api.NewServer(deps)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, pool
}

func seedUser(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, string, string) {
	t.Helper()
	username := "u_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	password := "Passw0rd!test"
	hash, err := service.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
		username, hash).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id, username, password
}

func seedSource(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, rawURL, rules string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO book_sources (user_id, book_source_url, book_source_name, book_source_group,
			book_source_type, enabled, enabled_explore, weight, respond_time, header, rules)
		VALUES ($1, $2, $3, '默认', 0, true, true, 0, 0, '{}', $4::jsonb)
		RETURNING id`, userID, rawURL, "src_"+rawURL, rules).Scan(&id)
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
	return id
}

func doJSON(t *testing.T, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func login(t *testing.T, base, username, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	status, resp := doJSON(t, http.MethodPost, base+"/auth/login", "", body)
	if status != http.StatusOK {
		t.Fatalf("login status %d: %s", status, resp)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if out.AccessToken == "" {
		t.Fatal("empty access token")
	}
	return out.AccessToken
}

func sourceCount(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM book_sources WHERE user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	return n
}

// 测试 1：书源 CRUD、409 乐观锁、导入 skip/update、dry_run 回滚、导出权威列 + 未知键。
func TestSourcesCRUDExportDryRun(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	uid, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	// Create
	createBody, _ := json.Marshal(map[string]any{
		"book_source_url":  "https://example.com/s1",
		"book_source_name": "Source 1",
	})
	status, resp := doJSON(t, http.MethodPost, base+"/sources", token, createBody)
	if status != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %s", status, resp)
	}
	var created struct {
		Id      string `json:"id"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("want version 1, got %d", created.Version)
	}

	// Get
	status, resp = doJSON(t, http.MethodGet, base+"/sources/"+created.Id, token, nil)
	if status != http.StatusOK {
		t.Fatalf("get: want 200, got %d: %s", status, resp)
	}

	// Update（version=1）→ version 2
	updBody, _ := json.Marshal(map[string]any{"book_source_name": "Source 1b", "version": 1})
	status, resp = doJSON(t, http.MethodPut, base+"/sources/"+created.Id, token, updBody)
	if status != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", status, resp)
	}
	var updated struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(resp, &updated)
	if updated.Version != 2 {
		t.Fatalf("want version 2, got %d", updated.Version)
	}

	// 陈旧 version → 409
	status, resp = doJSON(t, http.MethodPut, base+"/sources/"+created.Id, token, updBody)
	if status != http.StatusConflict {
		t.Fatalf("stale update: want 409, got %d: %s", status, resp)
	}

	// 导入数据（含未知键 customKey）
	importData := `[{"bookSourceUrl":"https://example.com/s2","bookSourceName":"Imported",` +
		`"bookSourceGroup":"g","bookSourceType":0,"enabled":true,"enabledExplore":true,` +
		`"weight":5,"respondTime":7,"charset":"","concurrentRate":"","loginUrl":"","loginCheckJs":"",` +
		`"header":{},"customKey":"keep-me"}]`

	before := sourceCount(t, pool, uid)
	dryBody, _ := json.Marshal(map[string]any{"data": importData, "format": "legado_json", "dry_run": true})
	status, resp = doJSON(t, http.MethodPost, base+"/sources/import", token, dryBody)
	if status != http.StatusOK {
		t.Fatalf("dry import: want 200, got %d: %s", status, resp)
	}
	var dryResult struct {
		DryRun   bool `json:"dry_run"`
		Imported int  `json:"imported"`
	}
	_ = json.Unmarshal(resp, &dryResult)
	if !dryResult.DryRun || dryResult.Imported != 1 {
		t.Fatalf("dry import: want dry_run=true imported=1, got %s", resp)
	}
	if after := sourceCount(t, pool, uid); after != before {
		t.Fatalf("dry_run leaked rows: before=%d after=%d", before, after)
	}

	// 实际导入 skip
	skipBody, _ := json.Marshal(map[string]any{"data": importData, "format": "legado_json"})
	status, resp = doJSON(t, http.MethodPost, base+"/sources/import", token, skipBody)
	if status != http.StatusOK {
		t.Fatalf("import: want 200, got %d: %s", status, resp)
	}
	var imp struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
		Updated  int `json:"updated"`
	}
	_ = json.Unmarshal(resp, &imp)
	if imp.Imported != 1 {
		t.Fatalf("import: want imported=1, got %s", resp)
	}

	// 重复导入 skip
	status, resp = doJSON(t, http.MethodPost, base+"/sources/import", token, skipBody)
	if status != http.StatusOK {
		t.Fatalf("re-import: want 200, got %d: %s", status, resp)
	}
	_ = json.Unmarshal(resp, &imp)
	if imp.Skipped != 1 {
		t.Fatalf("re-import: want skipped=1, got %s", resp)
	}

	// overwrite=update 改名
	updateData := strings.Replace(importData, `"Imported"`, `"Imported Updated"`, 1)
	updImpBody, _ := json.Marshal(map[string]any{"data": updateData, "format": "legado_json", "overwrite": "update"})
	status, resp = doJSON(t, http.MethodPost, base+"/sources/import", token, updImpBody)
	if status != http.StatusOK {
		t.Fatalf("update import: want 200, got %d: %s", status, resp)
	}
	_ = json.Unmarshal(resp, &imp)
	if imp.Updated != 1 {
		t.Fatalf("update import: want updated=1, got %s", resp)
	}

	// 导出：关系列权威覆写（camelCase）+ 未知键保留
	status, resp = doJSON(t, http.MethodGet, base+"/sources/export", token, nil)
	if status != http.StatusOK {
		t.Fatalf("export: want 200, got %d: %s", status, resp)
	}
	var exported []map[string]any
	if err := json.Unmarshal(resp, &exported); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	var found map[string]any
	for _, item := range exported {
		if item["bookSourceUrl"] == "https://example.com/s2" {
			found = item
		}
	}
	if found == nil {
		t.Fatalf("export missing s2: %s", resp)
	}
	if found["bookSourceName"] != "Imported Updated" {
		t.Fatalf("export name not overwritten: %v", found["bookSourceName"])
	}
	if found["customKey"] != "keep-me" {
		t.Fatalf("export dropped unknown key: %v", found)
	}

	// Delete
	status, _ = doJSON(t, http.MethodDelete, base+"/sources/"+created.Id, token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", status)
	}
	status, _ = doJSON(t, http.MethodGet, base+"/sources/"+created.Id, token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("get deleted: want 404, got %d", status)
	}
}

// 测试 2：/debug 的 mode/phase/input_digest 追踪、book_info 映射、JS 超时仍 200。
func TestDebugSourceRule(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	uid, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	srcID := seedSource(t, pool, uid, "https://debug.example/", `{}`).String()

	html := `<html><body><div class="title">Hello</div></body></html>`

	cases := []struct {
		name, ruleType, rule, input, wantMode, wantPhase string
	}{
		{"css-default", "search", "div.title@text", html, "default", "search"},
		{"css-explicit", "search", "@css:div.title@text", html, "css", "search"},
		{"xpath", "toc", "@XPath://div[@class=\"title\"]", html, "xpath", "toc"},
		{"json", "content", "@Json:$.data.name", `{"data":{"name":"N"}}`, "json", "content"},
		{"js", "explore", "@js:result", "hello", "js", "explore"},
		{"template", "search", "{{ 1 + 2 }}", "x", "template", "search"},
		{"book_info", "book_info", "div.title@text", html, "default", "book_info"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]any{
			"rule": tc.rule, "rule_type": tc.ruleType,
			"variables": map[string]any{"html": tc.input},
		})
		status, resp := doJSON(t, http.MethodPost, base+"/sources/"+srcID+"/debug", token, body)
		if status != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", tc.name, status, resp)
		}
		var out struct {
			TraceId        string `json:"trace_id"`
			TotalElapsedMs int    `json:"total_elapsed_ms"`
			Steps          []struct {
				Index       int     `json:"index"`
				Phase       string  `json:"phase"`
				Mode        string  `json:"mode"`
				Expr        string  `json:"expr"`
				InputDigest *string `json:"input_digest"`
				Hit         bool    `json:"hit"`
			} `json:"steps"`
		}
		if err := json.Unmarshal(resp, &out); err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if len(out.Steps) == 0 {
			t.Fatalf("%s: no steps: %s", tc.name, resp)
		}
		step := out.Steps[0]
		if step.Mode != tc.wantMode {
			t.Fatalf("%s: want mode %q, got %q (%s)", tc.name, tc.wantMode, step.Mode, resp)
		}
		if step.Phase != tc.wantPhase {
			t.Fatalf("%s: want phase %q, got %q", tc.name, tc.wantPhase, step.Phase)
		}
		if step.Index != 1 {
			t.Fatalf("%s: want index 1, got %d", tc.name, step.Index)
		}
		if step.InputDigest == nil || *step.InputDigest == "" {
			t.Fatalf("%s: input_digest empty", tc.name)
		}
		if out.TraceId == "" || !strings.HasPrefix(out.TraceId, "tr_") {
			t.Fatalf("%s: bad trace_id %q", tc.name, out.TraceId)
		}
	}

	// 非本人/不存在的书源 → 404
	body, _ := json.Marshal(map[string]any{"rule": "div", "rule_type": "search"})
	status, _ := doJSON(t, http.MethodPost, base+"/sources/"+uuid.NewString()+"/debug", token, body)
	if status != http.StatusNotFound {
		t.Fatalf("unknown source: want 404, got %d", status)
	}

	// JS 无限循环：仍 200，失败步骤 error_code=sandbox_timeout
	body, _ = json.Marshal(map[string]any{
		"rule": "@js:while(true){}", "rule_type": "search",
		"variables": map[string]any{"html": "x"},
	})
	status, resp := doJSON(t, http.MethodPost, base+"/sources/"+srcID+"/debug", token, body)
	if status != http.StatusOK {
		t.Fatalf("timeout debug: want 200, got %d: %s", status, resp)
	}
	var to struct {
		Steps []struct {
			ErrorCode *string `json:"error_code"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(resp, &to); err != nil {
		t.Fatalf("decode timeout: %v", err)
	}
	if len(to.Steps) == 0 || to.Steps[len(to.Steps)-1].ErrorCode == nil ||
		*to.Steps[len(to.Steps)-1].ErrorCode != "sandbox_timeout" {
		t.Fatalf("want sandbox_timeout step, got: %s", resp)
	}
}

// 测试 3：多源并发搜索的部分失败隔离、相对 URL 绝对化、错误码映射与 total 语义。
func TestSearchMultiSource(t *testing.T) {
	const htmlA = `<div class="book"><a class="title" href="/book/1">Book One</a>` +
		`<span class="author">Author A</span><img class="cover" src="/img/1.jpg"/></div>` +
		`<div class="book"><a class="title" href="/book/2">Book Two</a>` +
		`<span class="author">Author B</span><img class="cover" src="/img/2.jpg"/></div>`

	mock := mockFetcher{fn: func(_ context.Context, rawURL string, _ http.Header) (*fetcher.Response, error) {
		switch {
		case strings.Contains(rawURL, "mock-a.example"):
			return &fetcher.Response{StatusCode: 200, Body: []byte(htmlA), FinalURL: "http://mock-a.example/"}, nil
		case strings.Contains(rawURL, "mock-b.example"):
			return &fetcher.Response{StatusCode: 500}, nil
		case strings.Contains(rawURL, "mock-c.example"):
			return nil, fmt.Errorf("%w: mock ssrf", fetcher.ErrSSRFBlocked)
		}
		return &fetcher.Response{StatusCode: 404}, nil
	}}

	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	uid, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	rulesA := `{"search":{"searchUrl":"http://mock-a.example/search?key={{key}}&page={{page}}",` +
		`"bookList":"div.book","name":".title@text","author":".author@text",` +
		`"bookUrl":".title@href","coverUrl":"img.cover@src"}}`
	rulesB := `{"search":{"searchUrl":"http://mock-b.example/search?key={{key}}","bookList":"div.book","name":".title@text"}}`
	rulesC := `{"search":{"searchUrl":"http://mock-c.example/search?key={{key}}","bookList":"div.book","name":".title@text"}}`

	seedSource(t, pool, uid, "http://mock-a.example/", rulesA)
	seedSource(t, pool, uid, "http://mock-b.example/", rulesB)
	seedSource(t, pool, uid, "http://mock-c.example/", rulesC)

	status, resp := doJSON(t, http.MethodGet, base+"/search?keyword=%E6%96%97%E7%A0%B4", token, nil)
	if status != http.StatusOK {
		t.Fatalf("search: want 200, got %d: %s", status, resp)
	}
	var out struct {
		Total  int `json:"total"`
		Groups []struct {
			SourceId string `json:"source_id"`
			Items    []struct {
				Name     string  `json:"name"`
				BookUrl  string  `json:"book_url"`
				CoverUrl *string `json:"cover_url"`
			} `json:"items"`
		} `json:"groups"`
		Failures []struct {
			SourceId string `json:"source_id"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if out.Total != 2 {
		t.Fatalf("want total 2, got %d: %s", out.Total, resp)
	}
	if len(out.Groups) != 1 {
		t.Fatalf("want 1 group, got %d: %s", len(out.Groups), resp)
	}
	items := out.Groups[0].Items
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].BookUrl != "http://mock-a.example/book/1" {
		t.Fatalf("book_url not absolutized: %q", items[0].BookUrl)
	}
	if items[0].CoverUrl == nil || *items[0].CoverUrl != "http://mock-a.example/img/1.jpg" {
		t.Fatalf("cover_url not absolutized: %v", items[0].CoverUrl)
	}

	codes := map[string]string{}
	for _, f := range out.Failures {
		codes[f.Code] = f.Message
	}
	if _, ok := codes["source_request_failed"]; !ok {
		t.Fatalf("missing source_request_failed: %v", codes)
	}
	if _, ok := codes["blocked_private_address"]; !ok {
		t.Fatalf("missing blocked_private_address: %v", codes)
	}
	for _, msg := range codes {
		if strings.Contains(msg, "mock ssrf") || strings.Contains(msg, "500") {
			t.Fatalf("failure message leaked detail: %q", msg)
		}
	}
}
