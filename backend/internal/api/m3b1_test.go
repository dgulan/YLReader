//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"yreader/backend/internal/engine/fetcher"
)

// TestM3b1IdempotentChapterCount 覆盖 TD-M3a-01：幂等命中（预检查与并发冲突两条
// 分支）都必须回填 chapter_count，而不是 Go 零值 0。
func TestM3b1IdempotentChapterCount(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	_ = userID
	token := login(t, base, username, password)

	data := buildEPUB(t, "幂等之书", "作者子", []epubChapter{
		{Title: "第一章", Content: "一"},
		{Title: "第二章", Content: "二"},
	}, nil)

	status, raw := importBook(t, base, token, "idem.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("first import = %d, want 201: %s", status, raw)
	}
	first := decodeImport(t, raw)
	if first.ChapterCount != 2 {
		t.Fatalf("first chapter_count = %d, want 2", first.ChapterCount)
	}

	// 预检查分支：同一文件再次导入必须 200 且 chapter_count == total_chapters。
	status, raw = importBook(t, base, token, "idem.epub", nil, nil, data)
	if status != http.StatusOK {
		t.Fatalf("second import = %d, want 200: %s", status, raw)
	}
	second := decodeImport(t, raw)
	if second.ChapterCount <= 0 {
		t.Fatalf("idempotent chapter_count = %d, want > 0", second.ChapterCount)
	}
	if second.ChapterCount != first.Book.TotalChapters {
		t.Fatalf("idempotent chapter_count = %d, want total_chapters %d",
			second.ChapterCount, first.Book.TotalChapters)
	}

	// 并发冲突分支：两请求竞争同一新文件，落败方 200 也必须带 chapter_count。
	conc := buildEPUB(t, "并发幂等之书", "作者丑", []epubChapter{
		{Title: "第一章", Content: "一"},
		{Title: "第二章", Content: "二"},
	}, nil)
	type outcome struct {
		status int
		body   importedBook
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, body := importBook(t, base, token, "conc2.epub", nil, nil, conc)
			var parsed importedBook
			_ = json.Unmarshal(body, &parsed)
			results <- outcome{status: st, body: parsed}
		}()
	}
	wg.Wait()
	close(results)

	counts := map[int]int{}
	for r := range results {
		counts[r.status]++
		if r.status == http.StatusOK && r.body.ChapterCount <= 0 {
			t.Fatalf("conflict-branch chapter_count = %d, want > 0", r.body.ChapterCount)
		}
	}
	if counts[http.StatusCreated] != 1 || counts[http.StatusOK] != 1 {
		t.Fatalf("concurrent statuses = %v, want one 201 and one 200", counts)
	}
}

// TestM3b1Settings 覆盖 Settings 的 GET / PATCH / null 删除语义。
func TestM3b1Settings(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	status, raw := doJSON(t, http.MethodGet, base+"/settings", token, nil)
	if status != http.StatusOK {
		t.Fatalf("get settings = %d, want 200: %s", status, raw)
	}
	if string(raw) != "{}\n" && string(raw) != "{}" {
		t.Fatalf("empty settings body = %q, want {} (not null)", string(raw))
	}

	status, raw = doJSON(t, http.MethodPatch, base+"/settings", token,
		mustJSON(t, map[string]any{"theme": "dark", "font_size": 18}))
	if status != http.StatusOK {
		t.Fatalf("patch settings = %d, want 200: %s", status, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode settings: %v: %s", err, raw)
	}
	if got["theme"] != "dark" || got["font_size"] != float64(18) {
		t.Fatalf("settings = %v, want theme=dark font_size=18", got)
	}

	status, raw = doJSON(t, http.MethodPatch, base+"/settings", token,
		mustJSON(t, map[string]any{"theme": nil}))
	if status != http.StatusOK {
		t.Fatalf("patch settings null = %d, want 200: %s", status, raw)
	}
	got = nil
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode settings: %v: %s", err, raw)
	}
	if _, ok := got["theme"]; ok {
		t.Fatalf("theme should be deleted, got %v", got)
	}
	if got["font_size"] != float64(18) {
		t.Fatalf("font_size should be preserved, got %v", got)
	}
}

// TestM3b1Bookmarks 覆盖书签增删查与同位置冲突。
func TestM3b1Bookmarks(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	srcID := seedSource(t, pool, userID, "https://m3b1.example/book", "{}")
	status, book := createBook(t, base, token, "书签之书", srcID, "https://m3b1.example/book/1")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create book = %d", status)
	}

	// 空列表必须返回 [] 而不是 null。
	status, raw := doJSON(t, http.MethodGet, base+"/books/"+book.ID.String()+"/bookmarks", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list bookmarks = %d: %s", status, raw)
	}
	if string(raw) != "[]\n" && string(raw) != "[]" {
		t.Fatalf("empty bookmarks = %q, want []", string(raw))
	}

	body := mustJSON(t, map[string]any{
		"chapter_index": 3, "char_offset": 12, "chapter_title": "第三章", "note": "记一笔",
	})
	status, raw = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/bookmarks", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create bookmark = %d, want 201: %s", status, raw)
	}
	var bm struct {
		ID           uuid.UUID `json:"id"`
		BookID       uuid.UUID `json:"book_id"`
		ChapterIndex int       `json:"chapter_index"`
		CharOffset   int       `json:"char_offset"`
	}
	if err := json.Unmarshal(raw, &bm); err != nil {
		t.Fatalf("decode bookmark: %v: %s", err, raw)
	}
	if bm.BookID != book.ID || bm.ChapterIndex != 3 || bm.CharOffset != 12 {
		t.Fatalf("bookmark = %+v", bm)
	}

	// 同位置重复创建 → 409。
	status, raw = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/bookmarks", token, body)
	if status != http.StatusConflict {
		t.Fatalf("duplicate bookmark = %d, want 409: %s", status, raw)
	}

	// 列表含 1 条。
	status, raw = doJSON(t, http.MethodGet, base+"/books/"+book.ID.String()+"/bookmarks", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list bookmarks = %d: %s", status, raw)
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode bookmark list: %v: %s", err, raw)
	}
	if len(list) != 1 {
		t.Fatalf("bookmarks len = %d, want 1", len(list))
	}

	status, raw = doJSON(t, http.MethodDelete, base+"/bookmarks/"+bm.ID.String(), token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete bookmark = %d, want 204: %s", status, raw)
	}
	status, _ = doJSON(t, http.MethodDelete, base+"/bookmarks/"+bm.ID.String(), token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("delete missing bookmark = %d, want 404", status)
	}
}

// TestM3b1BookmarkTenantAndBookCheck 覆盖越权删除与不存在的书。
func TestM3b1BookmarkTenantAndBookCheck(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userA, nameA, passA := seedUser(t, pool)
	tokenA := login(t, base, nameA, passA)
	_, nameB, passB := seedUser(t, pool)
	tokenB := login(t, base, nameB, passB)

	srcID := seedSource(t, pool, userA, "https://m3b1b.example/book", "{}")
	status, book := createBook(t, base, tokenA, "越权之书", srcID, "https://m3b1b.example/book/1")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create book = %d", status)
	}
	status, raw := doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/bookmarks", tokenA,
		mustJSON(t, map[string]any{"chapter_index": 1, "char_offset": 0}))
	if status != http.StatusCreated {
		t.Fatalf("create bookmark = %d: %s", status, raw)
	}
	var bm struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &bm); err != nil {
		t.Fatalf("decode bookmark: %v", err)
	}

	status, raw = doJSON(t, http.MethodDelete, base+"/bookmarks/"+bm.ID.String(), tokenB, nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-tenant delete = %d, want 404: %s", status, raw)
	}

	status, raw = doJSON(t, http.MethodPost, base+"/books/"+uuid.NewString()+"/bookmarks", tokenA,
		mustJSON(t, map[string]any{"chapter_index": 1, "char_offset": 0}))
	if status != http.StatusBadRequest {
		t.Fatalf("bookmark on missing book = %d, want 400: %s", status, raw)
	}
}

// TestM3b1ReplaceRulesCRUD 覆盖规则 CRUD、enabled 过滤与排序。
func TestM3b1ReplaceRulesCRUD(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	create := func(name, pattern string, order int, enabled bool) uuid.UUID {
		t.Helper()
		status, raw := doJSON(t, http.MethodPost, base+"/replace-rules", token, mustJSON(t, map[string]any{
			"name": name, "pattern": pattern, "replacement": "", "scope": "content",
			"is_enabled": enabled, "order_index": order,
		}))
		if status != http.StatusCreated {
			t.Fatalf("create rule %s = %d: %s", name, status, raw)
		}
		var rule struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(raw, &rule); err != nil {
			t.Fatalf("decode rule: %v", err)
		}
		return rule.ID
	}

	low := create("低优先", `\s+$`, 1, true)
	high := create("高优先", `(?m)^广告.*$`, 2, false)

	status, raw := doJSON(t, http.MethodGet, base+"/replace-rules", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list rules = %d: %s", status, raw)
	}
	var rules []struct {
		ID         uuid.UUID `json:"id"`
		OrderIndex int       `json:"order_index"`
		IsEnabled  bool      `json:"is_enabled"`
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatalf("decode rules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules len = %d, want 2", len(rules))
	}
	if rules[0].ID != low || rules[1].ID != high {
		t.Fatalf("rules order = %v, want low(order 1) then high(order 2)", rules)
	}

	// enabled=true 只返回启用的那条。
	status, raw = doJSON(t, http.MethodGet, base+"/replace-rules?enabled=true", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list enabled rules = %d: %s", status, raw)
	}
	rules = nil
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatalf("decode rules: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != low {
		t.Fatalf("enabled filter = %v, want only %s", rules, low)
	}

	status, raw = doJSON(t, http.MethodPut, base+"/replace-rules/"+low.String(), token, mustJSON(t, map[string]any{
		"name": "低优先改", "pattern": `[ ]+$`, "replacement": "", "scope": "content",
		"is_enabled": false, "order_index": 3,
	}))
	if status != http.StatusOK {
		t.Fatalf("update rule = %d: %s", status, raw)
	}
	var updated struct {
		Name      string `json:"name"`
		IsEnabled bool   `json:"is_enabled"`
	}
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("decode updated rule: %v", err)
	}
	if updated.Name != "低优先改" || updated.IsEnabled {
		t.Fatalf("updated rule = %+v", updated)
	}

	status, _ = doJSON(t, http.MethodDelete, base+"/replace-rules/"+low.String(), token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete rule = %d, want 204", status)
	}
	status, _ = doJSON(t, http.MethodDelete, base+"/replace-rules/"+low.String(), token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("delete missing rule = %d, want 404", status)
	}
}

// TestM3b1ReplaceRuleRegexValidation 覆盖创建 422 与更新 400 的正则校验。
func TestM3b1ReplaceRuleRegexValidation(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	status, raw := doJSON(t, http.MethodPost, base+"/replace-rules", token, mustJSON(t, map[string]any{
		"name": "坏正则", "pattern": `(?<invalid`, "is_enabled": true,
	}))
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid regex create = %d, want 422: %s", status, raw)
	}
	var errResp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Code != "rule_execution_failed" {
		t.Fatalf("create 422 code = %q, want rule_execution_failed", errResp.Error.Code)
	}

	status, raw = doJSON(t, http.MethodPost, base+"/replace-rules", token, mustJSON(t, map[string]any{
		"name": "好正则", "pattern": `\d+`, "is_enabled": true,
	}))
	if status != http.StatusCreated {
		t.Fatalf("create valid rule = %d: %s", status, raw)
	}
	var rule struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatalf("decode rule: %v", err)
	}

	status, raw = doJSON(t, http.MethodPut, base+"/replace-rules/"+rule.ID.String(), token,
		mustJSON(t, map[string]any{"name": "好正则", "pattern": `(?<invalid`, "is_enabled": true}))
	if status != http.StatusBadRequest {
		t.Fatalf("invalid regex update = %d, want 400: %s", status, raw)
	}
	errResp = struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}{}
	if err := json.Unmarshal(raw, &errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Code != "validation_error" {
		t.Fatalf("update 400 code = %q, want validation_error", errResp.Error.Code)
	}
}

// ensure time import 被使用（探针用例保留以防未来删除并发断言）。
var _ = time.Now

// ensure fetcher import 被使用（部分用例通过 newTestServer(nil) 走默认 Fetcher）。
var _ = fetcher.New

// TestM3b1ConcurrentNameAuthorConflict 覆盖 TD-M3a-03 的核心场景：同名不同文件
// 并发导入必须走 name_author 分支，且不得再出现 1s 固定退避。
func TestM3b1ConcurrentNameAuthorConflict(t *testing.T) {
	// 本用例断言的是墙钟耗时（< 600ms），因此不参与 t.Parallel：并行套件
	// 会把两本小书的导入推到数百毫秒，制造与实现无关的抖动。
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data1 := buildEPUB(t, "并发同名书", "作者丙", []epubChapter{{Title: "第一章", Content: "正文内容A"}}, nil)
	data2 := buildEPUB(t, "并发同名书", "作者丙", []epubChapter{{Title: "第一章", Content: "正文内容B"}}, nil)

	start := time.Now()
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	wg.Add(2)
	go func() { defer wg.Done(); statuses[0], _ = importBook(t, base, token, "book1.epub", nil, nil, data1) }()
	go func() { defer wg.Done(); statuses[1], _ = importBook(t, base, token, "book2.epub", nil, nil, data2) }()
	wg.Wait()
	elapsed := time.Since(start)
	t.Logf("concurrent name/author import elapsed: %v (statuses=%v)", elapsed, statuses)

	// 核心断言：走 name_author 分支时耗时 < 600ms（修复前 1000ms）。
	if elapsed >= 600*time.Millisecond {
		t.Fatalf("concurrent name conflict took %v, want < 600ms", elapsed)
	}

	ok201, ok4xx := 0, 0
	for i, st := range statuses {
		switch st {
		case http.StatusCreated:
			ok201++
		case http.StatusBadRequest, http.StatusConflict:
			ok4xx++
		default:
			t.Fatalf("import [%d] unexpected status = %d, want 201/400/409", i, st)
		}
	}
	// P2-1 断言收紧：重试循环保证两并发均收敛成功（第二本自动重命名）。
	if ok201 != 2 {
		t.Fatalf("2-way concurrent import must be all 201, got statuses=%v", statuses)
	}
	_ = ok4xx

	n := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND normalized_name LIKE $2`,
		userID, "并发同名书%")
	if n != 2 {
		t.Fatalf("books after 2-way concurrent import = %d, want 2", n)
	}
}

// TestM3b1Concurrent3NameConflict 覆盖 3 本同名书并发导入，
// 验证重试循环在多级冲突下的收敛性：至少 2 本 201，无 500。
func TestM3b1Concurrent3NameConflict(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	datas := [][]byte{
		buildEPUB(t, "三重同名书", "作者丁", []epubChapter{{Title: "第一章", Content: "内容A"}}, nil),
		buildEPUB(t, "三重同名书", "作者丁", []epubChapter{{Title: "第一章", Content: "内容B"}}, nil),
		buildEPUB(t, "三重同名书", "作者丁", []epubChapter{{Title: "第一章", Content: "内容C"}}, nil),
	}

	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := 0; i < 3; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = importBook(t, base, token, fmt.Sprintf("book%d.epub", i+1), nil, nil, datas[i])
		}()
	}
	wg.Wait()

	// 断言：3 本必须全部 201（重试上限 5 次足以覆盖 3 并发），无 500。
	ok201 := 0
	for i, st := range statuses {
		if st == http.StatusInternalServerError {
			t.Fatalf("import [%d] got 500, statuses=%v", i, statuses)
		}
		if st == http.StatusCreated {
			ok201++
		}
	}
	if ok201 != 3 {
		t.Fatalf("3-way concurrent import must be all 201, got statuses=%v", statuses)
	}
	t.Logf("3-way concurrent statuses=%v (201=%d)", statuses, ok201)

	n := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND normalized_name LIKE '三重同名书%'`,
		userID)
	if n != 3 {
		t.Fatalf("books after 3-way concurrent import = %d, want 3", n)
	}
}

// TestM3b1Concurrent10NameAuthorConflict 覆盖 10 路同名同作者并发导入（v1.3 / DEF-CODE-03）：
// 断言 10 个请求全部 201、库中严格 10 本、无嵌套畸形名，墙钟耗时 <= 2500ms。
func TestM3b1Concurrent10NameAuthorConflict(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM books WHERE user_id = $1`, userID)
	})

	const total = 10
	datas := make([][]byte, total)
	for i := 0; i < total; i++ {
		datas[i] = buildEPUB(t, "十重同名书", "作者戊",
			[]epubChapter{{Title: "第一章", Content: fmt.Sprintf("并发内容%d", i)}}, nil)
	}

	start := time.Now()
	var wg sync.WaitGroup
	statuses := make([]int, total)
	for i := 0; i < total; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = importBook(t, base, token, fmt.Sprintf("c%d.epub", i+1), nil, nil, datas[i])
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	t.Logf("10-way concurrent name/author import elapsed: %v (statuses=%v)", elapsed, statuses)

	if elapsed > 2500*time.Millisecond {
		t.Fatalf("10-way concurrent import took %v, want <= 2500ms", elapsed)
	}
	for i, st := range statuses {
		if st != http.StatusCreated {
			t.Fatalf("import [%d] status = %d, want 201 (statuses=%v)", i, st, statuses)
		}
	}

	count := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND normalized_name LIKE '十重同名书%'`, userID)
	if count != total {
		t.Fatalf("books after 10-way concurrent import = %d, want %d", count, total)
	}
	nested := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND normalized_name LIKE '十重同名书%(%(%'`, userID)
	if nested != 0 {
		t.Fatalf("nested rename detected: %d rows", nested)
	}

	rows, err := pool.Query(context.Background(),
		`SELECT name FROM books WHERE user_id=$1 AND normalized_name LIKE '十重同名书%' ORDER BY name`, userID)
	if err != nil {
		t.Fatalf("query names: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate names: %v", err)
	}
	t.Logf("10-way names: %v", names)
}
