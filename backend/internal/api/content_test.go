//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"yreader/backend/internal/pkg/cache"
)

// contentRulesJSON 生成同时含 toc 与 content 的书源规则。
func contentRulesJSON(list, name, url, volume, nextToc, contentRule, nextContent string) string {
	rules := map[string]any{
		"toc": map[string]any{
			"chapterList": list,
			"chapterName": name,
			"chapterUrl":  url,
			"isVolume":    volume,
			"nextTocUrl":  nextToc,
		},
		"content": map[string]any{
			"content":        contentRule,
			"nextContentUrl": nextContent,
		},
	}
	b, _ := json.Marshal(rules)
	return string(b)
}

type chapterContentResp struct {
	Chapter struct {
		Id           int64  `json:"id"`
		Title        string `json:"title"`
		ChapterIndex *int   `json:"chapter_index"`
		IsVolume     bool   `json:"is_volume"`
	} `json:"chapter"`
	Content               string      `json:"content"`
	Cached                bool        `json:"cached"`
	WordCount             int         `json:"word_count"`
	AppliedReplaceRuleIDs []uuid.UUID `json:"applied_replace_rule_ids"`
	TraceID               *string     `json:"trace_id"`
	SourceID              *uuid.UUID  `json:"source_id"`
	IsOverridden          *bool       `json:"is_overridden"`
	PrevChapterIndex      *int        `json:"prev_chapter_index"`
	NextChapterIndex      *int        `json:"next_chapter_index"`
}

type apiError struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func getContent(t *testing.T, base, token, bookID, query string) (int, chapterContentResp) {
	t.Helper()
	status, raw := doJSON(t, http.MethodGet, base+"/books/"+bookID+"/content"+query, token, nil)
	var out chapterContentResp
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode content: %v: %s", err, raw)
		}
	}
	return status, out
}

func errorCode(t *testing.T, raw []byte) string {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(raw, &e); err != nil {
		return ""
	}
	return e.Error.Code
}

// seedBookWithTOC 创建书 + 源，并抓取一次目录，使 chapters 表就绪。
func seedBookWithTOC(t *testing.T, base, token string, bookID *uuid.UUID, src uuid.UUID, tocURL, name string) uuid.UUID {
	t.Helper()
	_, book := createBook(t, base, token, name, src, tocURL)
	*bookID = book.ID
	if status, list := getChapters(t, base, token, book.ID.String(), ""); status != http.StatusOK || list.Total == 0 {
		t.Fatalf("seed toc status=%d total=%d", status, list.Total)
	}
	return book.ID
}

// TestContentActiveSourcePaginationAndReplace 覆盖：活跃源抓取、两页拼接、replace_rules 净化、
// Chapter 嵌套完整性、字数与前後章索引。
func TestContentActiveSourcePaginationAndReplace(t *testing.T) {
	const (
		bookURL = "https://mc1.example.com/book/1"
		c1      = "https://mc1.example.com/c/1"
		c1p2    = "https://mc1.example.com/c/1?page=2"
	)
	mock := newTOCMock(map[string]string{
		bookURL: tocPage(false, []string{"/c/1", "/c/2"}, ""),
		c1: `<html><body><div id="content"><p>第一段正文</p><p>广告词</p></div>` +
			`<a class="next" href="` + c1p2 + `">下一页</a></body></html>`,
		c1p2: `<html><body><div id="content"><p>第二段正文</p></div></body></html>`,
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	src := seedSource(t, pool, userID, "https://mc1.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@html", "a.next@href"))

	// 预置 1 条 content 作用域替换规则。
	var ruleID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO replace_rules (user_id, name, pattern, replacement, scope, is_enabled, order_index)
		VALUES ($1, '去广告', '广告词', '广告', 'content', true, 0) RETURNING id`, userID).Scan(&ruleID); err != nil {
		t.Fatalf("seed replace rule: %v", err)
	}

	var bookID uuid.UUID
	seedBookWithTOC(t, base, token, &bookID, src, bookURL, "正文测试书")

	status, resp := getContent(t, base, token, bookID.String(), "?chapter_index=0")
	if status != http.StatusOK {
		t.Fatalf("content status %d", status)
	}
	if resp.Cached {
		t.Fatal("first request must not be cached")
	}
	if resp.IsOverridden == nil || *resp.IsOverridden {
		t.Fatalf("is_overridden = %v, want false", resp.IsOverridden)
	}
	if resp.Chapter.Title == "" || resp.Chapter.ChapterIndex == nil || *resp.Chapter.ChapterIndex != 0 {
		t.Fatalf("nested chapter = %+v", resp.Chapter)
	}
	if resp.WordCount != utf8.RuneCountInString(resp.Content) {
		t.Fatalf("word_count=%d runes=%d", resp.WordCount, utf8.RuneCountInString(resp.Content))
	}
	if len(resp.AppliedReplaceRuleIDs) != 1 || resp.AppliedReplaceRuleIDs[0] != ruleID {
		t.Fatalf("applied rules = %v, want [%s]", resp.AppliedReplaceRuleIDs, ruleID)
	}
	if !contains(resp.Content, "第一段正文") || !contains(resp.Content, "第二段正文") {
		t.Fatalf("paged content missing: %q", resp.Content)
	}
	if contains(resp.Content, "广告词") || !contains(resp.Content, "广告") {
		t.Fatalf("replace rule not applied: %q", resp.Content)
	}
	if resp.PrevChapterIndex != nil {
		t.Fatalf("prev = %v, want null", resp.PrevChapterIndex)
	}
	if resp.NextChapterIndex == nil || *resp.NextChapterIndex != 1 {
		t.Fatalf("next = %v, want 1", resp.NextChapterIndex)
	}
	if resp.TraceID == nil || *resp.TraceID == "" {
		t.Fatal("trace_id should be set")
	}
}

// TestContentCacheHit 覆盖：第二次读取命中缓存、字段与 trace_id 稳定、不再发生网络抓取。
func TestContentCacheHit(t *testing.T) {
	const (
		bookURL = "https://mc2.example.com/book/1"
		c0      = "https://mc2.example.com/c/0"
	)
	mock := newTOCMock(map[string]string{
		bookURL: tocPage(false, []string{"/c/0", "/c/1"}, ""),
		c0:      `<html><body><div id="content"><p>缓存正文</p></div></body></html>`,
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	src := seedSource(t, pool, userID, "https://mc2.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@html", ""))

	var bookID uuid.UUID
	seedBookWithTOC(t, base, token, &bookID, src, bookURL, "缓存测试书")

	if st, _ := getContent(t, base, token, bookID.String(), "?chapter_index=0"); st != http.StatusOK {
		t.Fatalf("first status %d", st)
	}
	_, r1 := getContent(t, base, token, bookID.String(), "?chapter_index=0")
	before := mock.Count()

	second, r2 := getContent(t, base, token, bookID.String(), "?chapter_index=0")
	if second != http.StatusOK {
		t.Fatalf("second status %d", second)
	}
	if !r2.Cached {
		t.Fatal("second request should be cached")
	}
	if r2.Content != r1.Content || r2.WordCount != r1.WordCount {
		t.Fatal("cached payload mismatch")
	}
	if r1.TraceID == nil || r2.TraceID == nil || *r1.TraceID != *r2.TraceID {
		t.Fatalf("trace_id unstable: %v vs %v", r1.TraceID, r2.TraceID)
	}
	if got := mock.Count(); got != before {
		t.Fatalf("cache hit must not fetch: %d -> %d", before, got)
	}
}

// TestContentOverrideRouting 覆盖：覆盖优先路由、正文来自备用源、chapter 元数据仍取活跃源。
func TestContentOverrideRouting(t *testing.T) {
	const (
		s1BookURL = "https://mc3-s1.example.com/book/1"
		s1c0      = "https://mc3-s1.example.com/c/0"
		s1c1      = "https://mc3-s1.example.com/c/1"
		s2BookURL = "https://mc3-s2.example.com/book/1"
		s2c1      = "https://mc3-s2.example.com/c/1"
	)
	mock := newTOCMock(map[string]string{
		s1BookURL: tocPage(false, []string{"/c/0", "/c/1"}, ""),
		s1c0:      `<html><body><div id="content"><p>S1 第一章正文</p></div></body></html>`,
		s1c1:      `<html><body><div id="content"><p>S1 正文</p></div></body></html>`,
		s2BookURL: tocPage(false, []string{"/c/0", "/c/1"}, ""),
		s2c1:      `<html><body><div id="content"><p>S2 覆盖正文</p></div></body></html>`,
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://mc3-s1.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@html", ""))
	s2 := seedSource(t, pool, userID, "https://mc3-s2.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@html", ""))

	var bookID uuid.UUID
	seedBookWithTOC(t, base, token, &bookID, s1, s1BookURL, "覆盖路由书")

	if st, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/chapters/1/override", token,
		mustJSON(t, map[string]any{"chapter_url": s2c1, "source_id": s2.String()})); st != http.StatusOK {
		t.Fatalf("set override status %d: %s", st, raw)
	}

	status, resp := getContent(t, base, token, bookID.String(), "?chapter_index=1")
	if status != http.StatusOK {
		t.Fatalf("content status %d", status)
	}
	if resp.IsOverridden == nil || !*resp.IsOverridden {
		t.Fatalf("is_overridden = %v, want true", resp.IsOverridden)
	}
	if resp.SourceID == nil || *resp.SourceID != s2 {
		t.Fatalf("source_id = %v, want %s", resp.SourceID, s2)
	}
	if !contains(resp.Content, "S2 覆盖正文") {
		t.Fatalf("content should come from S2: %q", resp.Content)
	}
	// 混合语义：chapter 标题仍是活跃源 S1 的目录标题。
	if !contains(resp.Chapter.Title, "第2章") {
		t.Fatalf("chapter title = %q, want active-source title", resp.Chapter.Title)
	}
	// 翻页位点必须以活跃源 S1 的章节编号空间为准：
	// 覆盖章 index=1 的前后章应为 prev=0 / next=null；若误用 targetSourceID(S2)，
	// chapters 表在 S2 下无行，prev/next 会双双为 nil。
	if resp.PrevChapterIndex == nil || *resp.PrevChapterIndex != 0 {
		t.Fatalf("prev_chapter_index = %v, want 0 (active source basis)", resp.PrevChapterIndex)
	}
	if resp.NextChapterIndex != nil {
		t.Fatalf("next_chapter_index = %v, want null (last chapter of active source)", resp.NextChapterIndex)
	}

	// 未覆盖章 index=0：prev=null、next=1，同样以活跃源为准。
	status0, resp0 := getContent(t, base, token, bookID.String(), "?chapter_index=0")
	if status0 != http.StatusOK {
		t.Fatalf("chapter0 status %d", status0)
	}
	if resp0.IsOverridden != nil && *resp0.IsOverridden {
		t.Fatalf("chapter0 should not be overridden")
	}
	if resp0.SourceID == nil || *resp0.SourceID != s1 {
		t.Fatalf("chapter0 source_id = %v, want active source %s", resp0.SourceID, s1)
	}
	if resp0.PrevChapterIndex != nil {
		t.Fatalf("chapter0 prev = %v, want null", resp0.PrevChapterIndex)
	}
	if resp0.NextChapterIndex == nil || *resp0.NextChapterIndex != 1 {
		t.Fatalf("chapter0 next = %v, want 1", resp0.NextChapterIndex)
	}
}

// TestContentParamValidation 覆盖：参数互斥/越界/显式 source_id/prefetch、卷名防御、
// chapter_id 合法命中与备用源 chapter_id 穿透拦截。
func TestContentParamValidation(t *testing.T) {
	const (
		bookURL = "https://mc4.example.com/book/1"
		c0      = "https://mc4.example.com/c/0"
	)
	mock := newTOCMock(map[string]string{
		bookURL: tocPage(true, []string{"/c/0"}, ""),
		c0:      `<html><body><div id="content"><p>校验正文</p></div></body></html>`,
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://mc4.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "span.flag@text", "", "div#content@html", ""))
	s2 := seedSource(t, pool, userID, "https://mc4-s2.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@html", ""))

	var bookID uuid.UUID
	seedBookWithTOC(t, base, token, &bookID, s1, bookURL, "参数校验书")

	// 同时传 / 都不传 / 负数 / 显式 source_id / prefetch 一律 400。
	if st, raw := doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/content?chapter_index=0&chapter_id=1", token, nil); st != http.StatusBadRequest {
		t.Fatalf("both params status=%d %s", st, raw)
	}
	if st, _ := doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/content", token, nil); st != http.StatusBadRequest {
		t.Fatalf("no params status=%d", st)
	}
	if st, _ := doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/content?chapter_index=-1", token, nil); st != http.StatusBadRequest {
		t.Fatalf("negative index status=%d", st)
	}
	if st, _ := doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/content?chapter_index=0&source_id="+s2.String(), token, nil); st != http.StatusBadRequest {
		t.Fatalf("explicit source_id status=%d", st)
	}
	if st, _ := doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/content?chapter_index=0&prefetch=true", token, nil); st != http.StatusBadRequest {
		t.Fatalf("prefetch status=%d", st)
	}
	// 越界索引 → 404。
	if st, raw := doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/content?chapter_index=99", token, nil); st != http.StatusNotFound {
		t.Fatalf("out-of-range status=%d %s", st, raw)
	}

	// 卷名行 → 400 validation_error。
	var volumeChapterID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM chapters WHERE book_id = $1 AND is_volume = true LIMIT 1`, bookID).Scan(&volumeChapterID); err != nil {
		t.Fatalf("query volume chapter: %v", err)
	}
	{
		st, raw := doJSON(t, http.MethodGet, fmt.Sprintf("%s/books/%s/content?chapter_id=%d", base, bookID, volumeChapterID), token, nil)
		if st != http.StatusBadRequest || errorCode(t, raw) != "validation_error" {
			t.Fatalf("volume status=%d code=%s", st, errorCode(t, raw))
		}
	}

	// 活跃源合法 chapter_id → 200。
	var activeChapterID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM chapters WHERE book_id = $1 AND source_id = $2 AND chapter_index IS NOT NULL LIMIT 1`,
		bookID, s1).Scan(&activeChapterID); err != nil {
		t.Fatalf("query active chapter: %v", err)
	}
	if st, resp := getContent(t, base, token, bookID.String(), fmt.Sprintf("?chapter_id=%d", activeChapterID)); st != http.StatusOK || resp.Chapter.Id != activeChapterID {
		t.Fatalf("chapter_id lookup status=%d id=%d", st, resp.Chapter.Id)
	}

	// 备用源 chapter_id → 404（GetByID 严格限定 active 源）。
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO book_source_mappings (book_id, source_id, book_url, sync_status)
		VALUES ($1, $2, $3, 'pending')`, bookID, s2, "https://mc4-s2.example.com/book/1"); err != nil {
		t.Fatalf("seed s2 mapping: %v", err)
	}
	mock.Set("https://mc4-s2.example.com/book/1", tocPage(false, []string{"/c/0", "/c/1", "/c/2"}, ""))
	if st, _ := getChapters(t, base, token, bookID.String(), "?source_id="+s2.String()+"&refresh=true"); st != http.StatusOK {
		t.Fatalf("s2 preview status=%d", st)
	}
	var foreignChapterID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM chapters WHERE book_id = $1 AND source_id = $2 LIMIT 1`, bookID, s2).Scan(&foreignChapterID); err != nil {
		t.Fatalf("query foreign chapter: %v", err)
	}
	if st, raw := doJSON(t, http.MethodGet, fmt.Sprintf("%s/books/%s/content?chapter_id=%d", base, bookID, foreignChapterID), token, nil); st != http.StatusNotFound {
		t.Fatalf("foreign chapter_id status=%d %s", st, raw)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestContentCacheRedisKeyAndTTL 用真实 Redis 校验缓存写入：key 布局、TTL≈7 天、二次请求命中且 trace 稳定。
// 需要 REDIS_ADDR 指向可用 Redis（redis:7-alpine）；未设置时跳过。
func TestContentCacheRedisKeyAndTTL(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if addr == "" {
		t.Skip("REDIS_ADDR 未设置，跳过真实 Redis 校验")
	}
	const (
		bookURL = "https://mc7.example.com/book/1"
		c0      = "https://mc7.example.com/c/0"
	)
	mock := newTOCMock(map[string]string{
		bookURL: tocPage(false, []string{"/c/0", "/c/1"}, ""),
		c0:      `<html><body><div id="content"><p>Redis 缓存正文</p></div></body></html>`,
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	src := seedSource(t, pool, userID, "https://mc7.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@html", ""))

	var bookID uuid.UUID
	seedBookWithTOC(t, base, token, &bookID, src, bookURL, "Redis 缓存书")

	first, r1 := getContent(t, base, token, bookID.String(), "?chapter_index=0")
	if first != http.StatusOK {
		t.Fatalf("first status %d", first)
	}
	if r1.Cached {
		t.Fatal("first request must be a miss")
	}
	if r1.TraceID == nil {
		t.Fatal("missing trace_id")
	}

	rc := cache.NewRedis(addr, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 测试隔离：key 以 bookID 命名空间隔离（每个测试 createBook 生成新 UUID），
	// 并在用例结束时按同一前缀清理，避免真实 Redis 中堆积历史键。
	cleanupPrefix := "yreader:cache:content:" + bookID.String() + ":*"
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		if old, err := rc.Keys(cctx, cleanupPrefix); err == nil && len(old) > 0 {
			_, _ = rc.Delete(cctx, old...)
		}
	})

	prefix := "yreader:cache:content:" + bookID.String() + ":"
	keys, err := rc.Keys(ctx, prefix+"*")
	if err != nil {
		t.Fatalf("redis KEYS: %v", err)
	}
	want := prefix + src.String() + ":0"
	found := false
	for _, k := range keys {
		if k == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("cache key not written, got %v, want %s", keys, want)
	}

	ttl, err := rc.TTL(ctx, want)
	if err != nil {
		t.Fatalf("redis TTL: %v", err)
	}
	if ttl <= 590*time.Second || ttl > 604800*time.Second {
		t.Fatalf("ttl = %v, want ≈604800s", ttl)
	}

	before := mock.Count()
	second, r2 := getContent(t, base, token, bookID.String(), "?chapter_index=0")
	if second != http.StatusOK {
		t.Fatalf("second status %d", second)
	}
	if !r2.Cached || r2.Content != r1.Content {
		t.Fatalf("redis cache hit failed: cached=%v contentEqual=%v", r2.Cached, r2.Content == r1.Content)
	}
	if r2.TraceID == nil || *r2.TraceID != *r1.TraceID {
		t.Fatalf("trace_id unstable: %v vs %v", r1.TraceID, r2.TraceID)
	}
	if got := mock.Count(); got != before {
		t.Fatalf("cache hit must not fetch: %d -> %d", before, got)
	}
}
