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

	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
)

// tocMock 是可按 URL 返回固定 HTML 的 Fetcher，并记录调用次数，
// 用于验证「缓存命中不重复抓取」「refresh 强制重抓」「翻页」。
type tocMock struct {
	mu    sync.Mutex
	pages map[string]string
	calls []string
}

func newTOCMock(pages map[string]string) *tocMock {
	return &tocMock{pages: pages}
}

func (m *tocMock) Get(ctx context.Context, u string, h http.Header) (*fetcher.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, u)
	body, ok := m.pages[u]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("toc mock: unexpected url %q", u)
	}
	return &fetcher.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
		FinalURL:   u,
	}, nil
}

func (m *tocMock) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *tocMock) Set(u, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pages[u] = body
}

// tocRulesJSON 生成书源 rules（toc 对象形态）。
func tocRulesJSON(list, name, url, volume, next string) string {
	rules := map[string]any{
		"toc": map[string]any{
			"chapterList": list,
			"chapterName": name,
			"chapterUrl":  url,
			"isVolume":    volume,
			"nextTocUrl":  next,
		},
	}
	b, _ := json.Marshal(rules)
	return string(b)
}

// tocPage 生成含章节列表的 HTML 页；volFirst=true 时首行为卷名行（含 span.flag=1，无 href）。
func tocPage(volFirst bool, hrefs []string, next string) string {
	html := `<html><body><ul class="list">`
	if volFirst {
		html += `<li class="volume"><span class="flag">1</span><a>第一卷 风起</a></li>`
	}
	for i, href := range hrefs {
		html += fmt.Sprintf(`<li><a href="%s">第%d章 标题%d</a></li>`, href, i+1, i+1)
	}
	html += `</ul>`
	if next != "" {
		html += fmt.Sprintf(`<a class="next" href="%s">下一页</a>`, next)
	}
	html += `</body></html>`
	return html
}

type chapterItem struct {
	ChapterIndex       *int    `json:"chapter_index"`
	IsVolume           bool    `json:"is_volume"`
	IsOverridden       *bool   `json:"is_overridden"`
	OverrideStatus     *string `json:"override_status"`
	OverrideSourceName *string `json:"override_source_name"`
	SortOrder          int     `json:"sort_order"`
	Title              string  `json:"title"`
	Url                string  `json:"url"`
}

type chapterListResp struct {
	BookID      uuid.UUID     `json:"book_id"`
	Items       []chapterItem `json:"items"`
	RefreshedAt *string       `json:"refreshed_at"`
	SourceID    uuid.UUID     `json:"source_id"`
	Total       int           `json:"total"`
}

func getChapters(t *testing.T, base, token, bookID, query string) (int, chapterListResp) {
	t.Helper()
	status, raw := doJSON(t, http.MethodGet, base+"/books/"+bookID+"/chapters"+query, token, nil)
	var out chapterListResp
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode chapter list: %v: %s", err, raw)
		}
	}
	return status, out
}

// TestTOCFetchCacheRefreshAndVolumeNull 覆盖：卷名行 chapter_index=null、total 只数可读章节、
// 首次自动抓取、二次命中 DB 缓存不抓取、refresh=true 强制重抓并刷新 refreshed_at。
func TestTOCFetchCacheRefreshAndVolumeNull(t *testing.T) {
	const tocURL = "https://toc1.example.com/book/1"
	mock := newTOCMock(map[string]string{
		tocURL: tocPage(true, []string{"/c/1", "/c/2"}, ""),
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	src := seedSource(t, pool, userID, "https://toc1.example.com", tocRulesJSON("ul.list li", "a@text", "a@href", "span.flag@text", ""))
	_, book := createBook(t, base, token, "目录测试书", src, tocURL)

	status, list := getChapters(t, base, token, book.ID.String(), "")
	if status != http.StatusOK {
		t.Fatalf("chapters status %d", status)
	}
	// total 只统计可读章节；items 含卷名行（Legado 目录结构要求保留卷名）。
	if list.Total != 2 || len(list.Items) != 3 {
		t.Fatalf("total=%d len=%d want 2/3 (volume + 2 readable)", list.Total, len(list.Items))
	}
	if !list.Items[0].IsVolume || list.Items[0].ChapterIndex != nil {
		t.Fatalf("volume row: isVolume=%v chapterIndex=%v", list.Items[0].IsVolume, list.Items[0].ChapterIndex)
	}
	if list.Items[1].ChapterIndex == nil || *list.Items[1].ChapterIndex != 0 {
		t.Fatalf("first readable chapter index = %v, want 0", list.Items[1].ChapterIndex)
	}
	if list.RefreshedAt == nil || *list.RefreshedAt == "" {
		t.Fatal("refreshed_at should be set after fetch")
	}
	fetches := mock.Count()
	if fetches != 1 {
		t.Fatalf("fetch count = %d, want 1", fetches)
	}

	// 二次点查应命中 DB 缓存，不再抓取。
	if status, _ := getChapters(t, base, token, book.ID.String(), ""); status != http.StatusOK {
		t.Fatalf("second get status %d", status)
	}
	if got := mock.Count(); got != fetches {
		t.Fatalf("cache miss: fetch count %d -> %d", fetches, got)
	}

	// refresh=true 强制重抓。
	status, refreshed := getChapters(t, base, token, book.ID.String(), "?refresh=true")
	if status != http.StatusOK {
		t.Fatalf("refresh status %d", status)
	}
	if got := mock.Count(); got != fetches+1 {
		t.Fatalf("refresh fetch count = %d, want %d", got, fetches+1)
	}
	if refreshed.RefreshedAt == nil {
		t.Fatal("refreshed_at should be set after refresh")
	}
}

// TestTOCPaginationLoopAndSleep 覆盖：多页拼接、每页间防抖 sleep 次数、环路立即终止不死循环。
func TestTOCPaginationLoopAndSleep(t *testing.T) {
	const (
		p1 = "https://toc2.example.com/book/1?page=1"
		p2 = "https://toc2.example.com/book/1?page=2"
		p3 = "https://toc2.example.com/book/1?page=3"
	)
	mock := newTOCMock(map[string]string{
		p1: tocPage(false, []string{"/c/1", "/c/2"}, p2),
		p2: tocPage(false, []string{"/c/3", "/c/4"}, p3),
		// 第 4 页指针回到第 1 页：必须被 visited 集合截断。
		p3: tocPage(false, []string{"/c/5", "/c/6"}, p1),
	})
	var sleepCount int
	var mu sync.Mutex
	ts, pool := newTestServerOpt(t, mock, func(deps *api.Deps) {
		deps.TOC = deps.TOC.WithSleep(func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			sleepCount++
			mu.Unlock()
			return nil
		})
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	src := seedSource(t, pool, userID, "https://toc2.example.com", tocRulesJSON("ul.list li", "a@text", "a@href", "", "a.next@href"))
	_, book := createBook(t, base, token, "翻页测试书", src, p1)

	status, list := getChapters(t, base, token, book.ID.String(), "")
	if status != http.StatusOK {
		t.Fatalf("chapters status %d", status)
	}
	if list.Total != 6 || len(list.Items) != 6 {
		t.Fatalf("paged total=%d len=%d want 6/6", list.Total, len(list.Items))
	}
	for i, it := range list.Items {
		if it.ChapterIndex == nil || *it.ChapterIndex != i {
			t.Fatalf("item %d chapter_index = %v, want %d", i, it.ChapterIndex, i)
		}
		if it.SortOrder != i {
			t.Fatalf("item %d sort_order = %d, want %d", i, it.SortOrder, i)
		}
	}
	mu.Lock()
	got := sleepCount
	mu.Unlock()
	if got != 2 {
		t.Fatalf("sleep calls = %d, want 2 (page1->2, page2->3)", got)
	}
	if fetches := mock.Count(); fetches != 3 {
		t.Fatalf("page fetches = %d, want 3 (loop must be cut)", fetches)
	}
}

// TestTOCPreviewIsolation 覆盖：显式 source_id 预览模式不改 books.active_source_id 与 total_chapters，
// 且 ChapterList.total 取目标源可读章节数而非 books.total_chapters。
func TestTOCPreviewIsolation(t *testing.T) {
	const (
		urlS1 = "https://toc3.example.com/s1/1"
		urlS2 = "https://toc3.example.com/s2/1"
	)
	mock := newTOCMock(map[string]string{
		urlS1: tocPage(false, []string{"/s1/1", "/s1/2", "/s1/3", "/s1/4", "/s1/5", "/s1/6", "/s1/7", "/s1/8", "/s1/9", "/s1/10"}, ""),
		urlS2: tocPage(false, []string{"/s2/1", "/s2/2", "/s2/3", "/s2/4", "/s2/5"}, ""),
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://toc3-s1.example.com", tocRulesJSON("ul.list li", "a@text", "a@href", "", ""))
	s2 := seedSource(t, pool, userID, "https://toc3-s2.example.com", tocRulesJSON("ul.list li", "a@text", "a@href", "", ""))
	_, book := createBook(t, base, token, "预览隔离书", s1, urlS1)

	// S1 作为活跃源抓 10 章，写入 books.total_chapters=10。
	if status, list := getChapters(t, base, token, book.ID.String(), ""); status != http.StatusOK || list.Total != 10 {
		t.Fatalf("s1 fetch status=%d total=%d", status, list.Total)
	}
	// S2 需要一条 mapping 才能定位目录页（换源预览模式的 book_url 来源）。
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO book_source_mappings (book_id, source_id, book_url, sync_status)
		VALUES ($1, $2, $3, 'pending')`, book.ID, s2, urlS2); err != nil {
		t.Fatalf("seed s2 mapping: %v", err)
	}

	status, preview := getChapters(t, base, token, book.ID.String(), "?source_id="+s2.String())
	if status != http.StatusOK {
		t.Fatalf("preview status %d", status)
	}
	if preview.Total != 5 || len(preview.Items) != 5 {
		t.Fatalf("preview total=%d len=%d want 5/5", preview.Total, len(preview.Items))
	}
	if preview.SourceID != s2 {
		t.Fatalf("preview source_id = %s want %s", preview.SourceID, s2)
	}
	if got := dbString(t, pool, `SELECT active_source_id::text FROM books WHERE id = $1`, book.ID); got != s1.String() {
		t.Fatalf("active_source_id = %s want %s", got, s1)
	}
	if got := dbInt(t, pool, `SELECT total_chapters FROM books WHERE id = $1`, book.ID); got != 10 {
		t.Fatalf("total_chapters = %d want 10 (preview must not touch books)", got)
	}
}

// TestTOCOverrideAssociationAndStale 覆盖：章节覆盖关联字段、预览刷新不误杀覆盖、
// 活跃源目录缩水时越界覆盖被标记 stale。
func TestTOCOverrideAssociationAndStale(t *testing.T) {
	const (
		urlS1 = "https://toc4.example.com/s1/1"
		urlS2 = "https://toc4.example.com/s2/1"
	)
	s1Hrefs := []string{"/s1/1", "/s1/2", "/s1/3", "/s1/4", "/s1/5", "/s1/6", "/s1/7", "/s1/8", "/s1/9", "/s1/10"}
	mock := newTOCMock(map[string]string{
		urlS1: tocPage(false, s1Hrefs, ""),
		urlS2: tocPage(false, []string{"/s2/1", "/s2/2", "/s2/3", "/s2/4", "/s2/5"}, ""),
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://toc4-s1.example.com", tocRulesJSON("ul.list li", "a@text", "a@href", "", ""))
	s2 := seedSource(t, pool, userID, "https://toc4-s2.example.com", tocRulesJSON("ul.list li", "a@text", "a@href", "", ""))
	_, book := createBook(t, base, token, "覆盖标记书", s1, urlS1)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO book_source_mappings (book_id, source_id, book_url, sync_status)
		VALUES ($1, $2, $3, 'pending')`, book.ID, s2, urlS2); err != nil {
		t.Fatalf("seed s2 mapping: %v", err)
	}
	if status, list := getChapters(t, base, token, book.ID.String(), ""); status != http.StatusOK || list.Total != 10 {
		t.Fatalf("s1 fetch status=%d total=%d", status, list.Total)
	}

	// 第 8 章改用 S2 源正文。
	st, raw := doJSON(t, http.MethodPost, base+"/books/"+book.ID.String()+"/chapters/8/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://toc4-s2.example.com/s2/8", "source_id": s2.String()}))
	if st != http.StatusOK {
		t.Fatalf("set override status %d: %s", st, raw)
	}

	status, list := getChapters(t, base, token, book.ID.String(), "?refresh=true")
	if status != http.StatusOK {
		t.Fatalf("refresh status %d", status)
	}
	var ch8 *chapterItem
	for i := range list.Items {
		if list.Items[i].ChapterIndex != nil && *list.Items[i].ChapterIndex == 8 {
			ch8 = &list.Items[i]
		}
	}
	if ch8 == nil {
		t.Fatal("chapter 8 not found")
	}
	if ch8.IsOverridden == nil || !*ch8.IsOverridden {
		t.Fatalf("chapter 8 is_overridden = %v, want true", ch8.IsOverridden)
	}
	if ch8.OverrideStatus == nil || *ch8.OverrideStatus != "active" {
		t.Fatalf("chapter 8 override_status = %v, want active", ch8.OverrideStatus)
	}
	if ch8.OverrideSourceName == nil || *ch8.OverrideSourceName != "src_https://toc4-s2.example.com" {
		t.Fatalf("chapter 8 override_source_name = %v", ch8.OverrideSourceName)
	}

	// 预览刷新 S2（5 章）绝不应把第 8 章覆盖标记为 stale。
	if st, _ := getChapters(t, base, token, book.ID.String(), "?source_id="+s2.String()+"&refresh=true"); st != http.StatusOK {
		t.Fatalf("s2 preview refresh status %d", st)
	}
	if got := dbString(t, pool, `SELECT status FROM chapter_source_overrides WHERE book_id = $1 AND chapter_index = 8`, book.ID); got != "active" {
		t.Fatalf("override status after s2 preview = %q, want active", got)
	}

	// 活跃源 S1 目录缩水到 5 章 → 第 8 章覆盖越界，应被标记 stale。
	mock.Set(urlS1, tocPage(false, s1Hrefs[:5], ""))
	if st, _ := getChapters(t, base, token, book.ID.String(), "?refresh=true"); st != http.StatusOK {
		t.Fatalf("s1 shrink refresh status %d", st)
	}
	if got := dbString(t, pool, `SELECT status FROM chapter_source_overrides WHERE book_id = $1 AND chapter_index = 8`, book.ID); got != "stale" {
		t.Fatalf("override status after shrink = %q, want stale", got)
	}
	if got := dbInt(t, pool, `SELECT total_chapters FROM books WHERE id = $1`, book.ID); got != 5 {
		t.Fatalf("books.total_chapters after shrink = %d, want 5", got)
	}
}
