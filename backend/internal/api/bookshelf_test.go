//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// doJSONH 发送 JSON 请求并附带自定义请求头。
func doJSONH(t *testing.T, method, url, token string, body []byte, headers map[string]string) (int, []byte) {
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
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type createdBook struct {
	ID              uuid.UUID `json:"id"`
	Version         int       `json:"version"`
	Name            string    `json:"name"`
	ActiveSourceID  uuid.UUID `json:"active_source_id"`
	TotalChapters   int       `json:"total_chapters"`
	NeedsSource     *bool     `json:"needs_source"`
	OverrideCount   *int      `json:"override_count"`
	ActiveBookURL   *string   `json:"active_book_url"`
	ProgressVersion *int      `json:"-"`
}

type bookPage struct {
	Items      []createdBook `json:"items"`
	Total      int           `json:"total"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	TotalPages int           `json:"total_pages"`
}

func createBook(t *testing.T, base, token string, name string, sourceID uuid.UUID, bookURL string) (int, createdBook) {
	t.Helper()
	status, raw := doJSON(t, http.MethodPost, base+"/books", token, mustJSON(t, map[string]any{
		"name": name, "author": "作者甲", "source_id": sourceID.String(), "book_url": bookURL,
	}))
	var b createdBook
	if status == http.StatusOK || status == http.StatusCreated {
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("decode created book: %v: %s", err, raw)
		}
	}
	return status, b
}

func dbString(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("db query: %v (%s)", err, sql)
	}
	return s
}

func dbInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("db query: %v (%s)", err, sql)
	}
	return n
}

// TestBookshelfCRUDPagingAndNeedsSource 覆盖：201 新建 / 200 多源合并 / 活跃源稳定 /
// 分页 total/total_pages / needs_source 三段式 / 详情含进度 / PATCH 乐观锁 / DELETE 404。
func TestBookshelfCRUDPagingAndNeedsSource(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	s1 := seedSource(t, pool, userID, "https://src1.example.com", `{}`)
	s2 := seedSource(t, pool, userID, "https://src2.example.com", `{}`)

	// 新建（201）
	status, book := createBook(t, base, token, "测试书A", s1, "https://src1.example.com/book/1")
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", status)
	}
	if got := dbString(t, pool, `SELECT active_source_id::text FROM books WHERE id = $1`, book.ID); got != s1.String() {
		t.Fatalf("active_source_id = %s, want %s", got, s1)
	}
	if got := dbString(t, pool, `SELECT sync_status::text FROM book_source_mappings WHERE book_id = $1 AND source_id = $2`, book.ID, s1); got != "active" {
		t.Fatalf("mapping s1 status = %s, want active", got)
	}

	// 同书同作者、不同源加入 → 200，且不得篡改活跃源
	status, again := createBook(t, base, token, "测试书A", s2, "https://src2.example.com/book/9")
	if status != http.StatusOK {
		t.Fatalf("merge create status = %d, want 200", status)
	}
	if again.ID != book.ID {
		t.Fatalf("merge returned different book id: %s != %s", again.ID, book.ID)
	}
	if got := dbString(t, pool, `SELECT active_source_id::text FROM books WHERE id = $1`, book.ID); got != s1.String() {
		t.Fatalf("active_source_id mutated to %s, want %s", got, s1)
	}
	if got := dbString(t, pool, `SELECT sync_status::text FROM book_source_mappings WHERE book_id = $1 AND source_id = $2`, book.ID, s2); got != "pending" {
		t.Fatalf("standby mapping status = %s, want pending", got)
	}

	// 分页
	status, raw := doJSON(t, http.MethodGet, base+"/books?page=1&page_size=10", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d: %s", status, raw)
	}
	var page bookPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode page: %v: %s", err, raw)
	}
	if page.Total != 1 || page.TotalPages != 1 || len(page.Items) != 1 {
		t.Fatalf("page = total %d / pages %d / items %d, want 1/1/1", page.Total, page.TotalPages, len(page.Items))
	}

	// needs_source 三段式：再造一本无活跃源的「待换源」书
	var needID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO books (user_id, book_type, normalized_name, normalized_author, name, author, status)
		VALUES ($1, 'online', 'daihuanyuanshu', '', '待换源书', '', 'unknown') RETURNING id`, userID).Scan(&needID); err != nil {
		t.Fatalf("insert needs_source book: %v", err)
	}

	status, raw = doJSON(t, http.MethodGet, base+"/books?needs_source=true", token, nil)
	if status != http.StatusOK {
		t.Fatalf("needs_source=true status = %d: %s", status, raw)
	}
	var onlyNeed bookPage
	_ = json.Unmarshal(raw, &onlyNeed)
	if onlyNeed.Total != 1 || len(onlyNeed.Items) != 1 || onlyNeed.Items[0].Name != "待换源书" {
		t.Fatalf("needs_source=true = %+v, want only 待换源书", onlyNeed)
	}

	status, raw = doJSON(t, http.MethodGet, base+"/books?needs_source=false", token, nil)
	if status != http.StatusOK {
		t.Fatalf("needs_source=false status = %d: %s", status, raw)
	}
	var onlyActive bookPage
	_ = json.Unmarshal(raw, &onlyActive)
	if onlyActive.Total != 1 || len(onlyActive.Items) != 1 || onlyActive.Items[0].Name != "测试书A" {
		t.Fatalf("needs_source=false = %+v, want only 测试书A (false 分支绝不能不过滤)", onlyActive)
	}

	status, raw = doJSON(t, http.MethodGet, base+"/books", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list all status = %d: %s", status, raw)
	}
	var all bookPage
	_ = json.Unmarshal(raw, &all)
	if all.Total != 2 {
		t.Fatalf("list all total = %d, want 2", all.Total)
	}

	// 详情含 progress
	status, raw = doJSON(t, http.MethodGet, base+"/books/"+book.ID.String(), token, nil)
	if status != http.StatusOK {
		t.Fatalf("get book status = %d: %s", status, raw)
	}
	var detail struct {
		ID       uuid.UUID       `json:"id"`
		Name     string          `json:"name"`
		Progress json.RawMessage `json:"progress"`
	}
	_ = json.Unmarshal(raw, &detail)
	if detail.ID != book.ID || len(detail.Progress) == 0 || string(detail.Progress) == "null" {
		t.Fatalf("detail = %+v, want id + progress", detail)
	}

	// PATCH 乐观锁
	status, raw = doJSONH(t, http.MethodPatch, base+"/books/"+book.ID.String(), token,
		mustJSON(t, map[string]any{"name": "测试书A改名", "version": 1}), nil)
	if status != http.StatusOK {
		t.Fatalf("patch status = %d: %s", status, raw)
	}
	var patched createdBook
	_ = json.Unmarshal(raw, &patched)
	if patched.Name != "测试书A改名" || patched.Version != 2 {
		t.Fatalf("patched = %+v, want name 测试书A改名 / version 2", patched)
	}
	// 陈旧版本 → 409
	status, _ = doJSONH(t, http.MethodPatch, base+"/books/"+book.ID.String(), token,
		mustJSON(t, map[string]any{"name": "x", "version": 1}), nil)
	if status != http.StatusConflict {
		t.Fatalf("stale patch status = %d, want 409", status)
	}

	// DELETE 204，再次 DELETE 404
	status, _ = doJSON(t, http.MethodDelete, base+"/books/"+book.ID.String(), token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", status)
	}
	status, _ = doJSON(t, http.MethodDelete, base+"/books/"+book.ID.String(), token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", status)
	}
}

// TestProgressZeroValueAndConflict 覆盖：0 值进度 200 / If-Match 必需 428 /
// 成功升版本 / 陈旧版本 409 ProgressConflict。
func TestProgressZeroValueAndConflict(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://src1.example.com", `{}`)

	status, book := createBook(t, base, token, "进度书", s1, "https://src1.example.com/book/p1")
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}

	// 初次 GET：200 + version 1 + 0 值
	status, raw := doJSON(t, http.MethodGet, base+"/books/"+book.ID.String()+"/progress", token, nil)
	if status != http.StatusOK {
		t.Fatalf("get progress status = %d: %s", status, raw)
	}
	var p struct {
		Version      int     `json:"version"`
		CharOffset   int     `json:"char_offset"`
		ChapterIndex int     `json:"chapter_index"`
		ChapterRatio float64 `json:"chapter_ratio"`
	}
	_ = json.Unmarshal(raw, &p)
	if p.Version != 1 || p.CharOffset != 0 || p.ChapterIndex != 0 {
		t.Fatalf("initial progress = %+v, want version 1 / offset 0 / index 0", p)
	}

	// 缺 If-Match → 428
	status, _ = doJSON(t, http.MethodPut, base+"/books/"+book.ID.String()+"/progress", token,
		mustJSON(t, map[string]any{"chapter_index": 3, "char_offset": 120, "chapter_ratio": 0.5}))
	if status != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status = %d, want 428", status)
	}

	// If-Match: "1" 成功 → version 2
	status, raw = doJSONH(t, http.MethodPut, base+"/books/"+book.ID.String()+"/progress", token,
		mustJSON(t, map[string]any{"chapter_index": 3, "char_offset": 120, "chapter_ratio": 0.5}),
		map[string]string{"If-Match": `"1"`})
	if status != http.StatusOK {
		t.Fatalf("update progress status = %d: %s", status, raw)
	}
	_ = json.Unmarshal(raw, &p)
	if p.Version != 2 || p.ChapterIndex != 3 || p.CharOffset != 120 {
		t.Fatalf("updated progress = %+v, want version 2 / index 3 / offset 120", p)
	}

	// Shelf must carry the same persisted position and optimistic-lock version.
	status, shelfRaw := doJSON(t, http.MethodGet, base+"/books", token, nil)
	var shelf struct {
		Items []struct {
			ID       uuid.UUID `json:"id"`
			Progress struct {
				Version    int `json:"version"`
				CharOffset int `json:"char_offset"`
			} `json:"progress"`
		} `json:"items"`
	}
	if status != http.StatusOK {
		t.Fatalf("shelf status %d: %s", status, shelfRaw)
	}
	if err := json.Unmarshal(shelfRaw, &shelf); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range shelf.Items {
		if item.ID == book.ID {
			found = true
			if item.Progress.Version != 2 || item.Progress.CharOffset != 120 {
				t.Fatalf("shelf progress lost: %+v", item.Progress)
			}
		}
	}
	if !found {
		t.Fatal("updated book missing from shelf")
	}

	// 陈旧 If-Match: "1" → 409 + current/incoming
	status, raw = doJSONH(t, http.MethodPut, base+"/books/"+book.ID.String()+"/progress", token,
		mustJSON(t, map[string]any{"chapter_index": 9}), map[string]string{"If-Match": `"1"`})
	if status != http.StatusConflict {
		t.Fatalf("stale update status = %d: %s", status, raw)
	}
	var conflict struct {
		Error struct {
			Code    string `json:"code"`
			Details *struct {
				Current  map[string]any `json:"current"`
				Incoming map[string]any `json:"incoming"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &conflict); err != nil {
		t.Fatalf("decode conflict: %v: %s", err, raw)
	}
	if conflict.Error.Code != "progress_conflict" || conflict.Error.Details == nil {
		t.Fatalf("conflict body = %+v, want code progress_conflict + details", conflict)
	}
	if v, _ := conflict.Error.Details.Current["version"].(float64); v != 2 {
		t.Fatalf("conflict current.version = %v, want 2", conflict.Error.Details.Current["version"])
	}
	if v, _ := conflict.Error.Details.Incoming["chapter_index"].(float64); v != 9 {
		t.Fatalf("conflict incoming.chapter_index = %v, want 9", conflict.Error.Details.Incoming["chapter_index"])
	}
}

// TestSwitchSourceDryRunAndOverrideCleanup 覆盖：URL 继承 / dry_run 回滚 /
// 动态回填 dry_run 与当前进度 / 非 dry_run 清空单章覆盖并更新活跃源。
func TestSwitchSourceDryRunAndOverrideCleanup(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://src1.example.com", `{}`)
	s2 := seedSource(t, pool, userID, "https://src2.example.com", `{}`)

	status, book := createBook(t, base, token, "换源书", s1, "https://src1.example.com/book/s1")
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	// 用同名书 + s2 追加备用源（建立 (book, s2) 的 book_url，供 URL 继承）
	status, _ = createBook(t, base, token, "换源书", s2, "https://src2.example.com/book/s2")
	if status != http.StatusOK {
		t.Fatalf("standby create status = %d", status)
	}

	for _, source := range []uuid.UUID{s1, s2} {
		if _, err := pool.Exec(context.Background(), `INSERT INTO chapters(book_id,source_id,chapter_index,sort_order,title,normalized_title,url,url_hash) SELECT $1,$2,i,i,'章'||i,'n:'||i,'https://example.com/c/'||i,encode(sha256(i::text::bytea),'hex') FROM generate_series(0,200) i`, book.ID, source); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(context.Background(), `UPDATE books SET total_chapters=201 WHERE id=$1`, book.ID); err != nil {
		t.Fatal(err)
	}
	// 第 5 章单章覆盖
	status, raw := doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/chapters/5/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://src1.example.com/c/5", "source_id": s2.String()}))
	if status != http.StatusOK {
		t.Fatalf("set override status = %d: %s", status, raw)
	}
	// 进度 120 / 0.42
	status, raw = doJSONH(t, http.MethodPut, base+"/books/"+book.ID.String()+"/progress", token,
		mustJSON(t, map[string]any{"chapter_index": 120, "chapter_ratio": 0.42}),
		map[string]string{"If-Match": `"1"`})
	if status != http.StatusOK {
		t.Fatalf("update progress status = %d: %s", status, raw)
	}

	// 子场景 A：dry_run=true，不传 book_url → 继承 s2 历史 URL
	status, raw = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/switch-source", token,
		mustJSON(t, map[string]any{"source_id": s2.String(), "dry_run": true}))
	if status != http.StatusOK {
		t.Fatalf("dry run status = %d: %s", status, raw)
	}
	var res struct {
		BookID       uuid.UUID `json:"book_id"`
		SourceID     uuid.UUID `json:"source_id"`
		Strategy     string    `json:"strategy"`
		Confidence   float64   `json:"confidence"`
		ChapterIndex int       `json:"chapter_index"`
		ChapterRatio float64   `json:"chapter_ratio"`
		DryRun       bool      `json:"dry_run"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode switch result: %v: %s", err, raw)
	}
	if !res.DryRun || res.Strategy != "exact" || res.Confidence != 1 ||
		res.ChapterIndex != 120 || res.ChapterRatio != 0.42 {
		t.Fatalf("dry run result = %+v, want dry_run true / exact / 1 / 120 / 0.42", res)
	}
	if got := dbString(t, pool, `SELECT active_source_id::text FROM books WHERE id = $1`, book.ID); got != s1.String() {
		t.Fatalf("dry run mutated active source to %s, want %s", got, s1)
	}
	if n := dbInt(t, pool, `SELECT count(*) FROM chapter_source_overrides WHERE book_id = $1 AND user_id = $2`, book.ID, userID); n != 1 {
		t.Fatalf("dry run overrides = %d, want 1 (rollback 未生效)", n)
	}

	// 子场景 B：dry_run=false → 清空覆盖并更新活跃源
	status, raw = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/switch-source", token,
		mustJSON(t, map[string]any{"source_id": s2.String(), "dry_run": false}))
	if status != http.StatusOK {
		t.Fatalf("switch status = %d: %s", status, raw)
	}
	_ = json.Unmarshal(raw, &res)
	if res.DryRun {
		t.Fatalf("switch result dry_run = true, want false（严禁硬编码）")
	}
	if got := dbString(t, pool, `SELECT active_source_id::text FROM books WHERE id = $1`, book.ID); got != s2.String() {
		t.Fatalf("active source = %s, want %s", got, s2)
	}
	if n := dbInt(t, pool, `SELECT count(*) FROM chapter_source_overrides WHERE book_id = $1 AND user_id = $2`, book.ID, userID); n != 0 {
		t.Fatalf("overrides after switch = %d, want 0（级联清空未生效）", n)
	}
}

// TestOverrideUpsertTenantAndIdempotentDelete 覆盖：负索引 400 / 非本人源 400 /
// 禁用源 400 / Upsert 重置 active / DELETE 幂等 204。
func TestOverrideUpsertTenantAndIdempotentDelete(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://src1.example.com", `{}`)

	// 另一个用户的源
	otherID, _, _ := seedUser(t, pool)
	otherSrc := seedSource(t, pool, otherID, "https://other.example.com", `{}`)

	status, book := createBook(t, base, token, "覆盖书", s1, "https://src1.example.com/book/o1")
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}

	// 负索引 → 400
	status, _ = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/chapters/-1/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://src1.example.com/c/1", "source_id": s1.String()}))
	if status != http.StatusBadRequest {
		t.Fatalf("negative index status = %d, want 400", status)
	}
	// 非本人源 → 400
	status, _ = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/chapters/1/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://src1.example.com/c/1", "source_id": otherSrc.String()}))
	if status != http.StatusBadRequest {
		t.Fatalf("foreign source status = %d, want 400", status)
	}
	// 已禁用源 → 400
	disabled := seedSource(t, pool, userID, "https://disabled.example.com", `{}`)
	if _, err := pool.Exec(context.Background(), `UPDATE book_sources SET enabled = false WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	status, _ = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/chapters/1/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://disabled.example.com/c/1", "source_id": disabled.String()}))
	if status != http.StatusBadRequest {
		t.Fatalf("disabled source status = %d, want 400", status)
	}

	// 设置第 5 章覆盖
	status, raw := doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/chapters/5/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://src1.example.com/c/5", "source_id": s1.String()}))
	if status != http.StatusOK {
		t.Fatalf("set override status = %d: %s", status, raw)
	}
	var ov struct {
		Status     string `json:"status"`
		ChapterURL string `json:"chapter_url"`
	}
	_ = json.Unmarshal(raw, &ov)
	if ov.Status != "active" {
		t.Fatalf("override status = %q, want active", ov.Status)
	}
	// 降到 stale 后再 Upsert 应恢复 active
	if _, err := pool.Exec(context.Background(),
		`UPDATE chapter_source_overrides SET status = 'stale' WHERE book_id = $1 AND chapter_index = 5`, book.ID); err != nil {
		t.Fatal(err)
	}
	status, raw = doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/chapters/5/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://src1.example.com/c/5b", "source_id": s1.String()}))
	if status != http.StatusOK {
		t.Fatalf("re-set override status = %d: %s", status, raw)
	}
	_ = json.Unmarshal(raw, &ov)
	if ov.Status != "active" || ov.ChapterURL != "https://src1.example.com/c/5b" {
		t.Fatalf("re-set override = %+v, want active + new url", ov)
	}
	if n := dbInt(t, pool, `SELECT count(*) FROM chapter_source_overrides WHERE book_id = $1 AND chapter_index = 5`, book.ID); n != 1 {
		t.Fatalf("override rows = %d, want 1 (Upsert 不得插入重复行)", n)
	}

	// DELETE 幂等
	status, _ = doJSON(t, http.MethodDelete, base+"/books/"+book.ID.String()+"/chapters/5/override", token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete override status = %d, want 204", status)
	}
	status, _ = doJSON(t, http.MethodDelete, base+"/books/"+book.ID.String()+"/chapters/5/override", token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("second delete status = %d, want 204 (幂等)", status)
	}
}
