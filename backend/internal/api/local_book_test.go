//go:build integration

package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"

	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/repository/testutil"
	"yreader/backend/internal/service"
)

// testSentinelKey 用于在服务端注入业务侧哨兵 value（避免裸 string 作 context key）。
type testSentinelKey struct{}

const testSentinelValue = "biz-token"

// ---- 构造器：TXT / EPUB ----

// buildTXT 用 UTF-8 编码拼出多章文本。
func buildTXT(chapters []string) []byte {
	var b bytes.Buffer
	for i, c := range chapters {
		fmt.Fprintf(&b, "第%d章 标题%d\n%s\n\n", i+1, i+1, c)
	}
	return b.Bytes()
}

type epubChapter struct {
	Title   string
	Content string
	Volume  bool // 纯父级节点：只出卷名行，不含正文
}

// buildEPUB 生成最小可用 EPUB（container + OPF + NCX + XHTML）。
func buildEPUB(t *testing.T, title, author string, chapters []epubChapter, extra map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}

	write("META-INF/container.xml", []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`))

	var manifest, spine, navPoints strings.Builder
	idx := 0
	for _, ch := range chapters {
		idx++
		href := fmt.Sprintf("ch%d.xhtml", idx)
		fmt.Fprintf(&manifest, `<item id="ch%d" href="%s" media-type="application/xhtml+xml"/>`, idx, href)
		if ch.Volume {
			// 卷名行只出现在 TOC（无 src），不进 spine。
			fmt.Fprintf(&navPoints, `<navPoint id="np%d"><navLabel><text>%s</text></navLabel><content src=""/><navPoint id="np%dc"><navLabel><text>%s</text></navLabel><content src="%s"/></navPoint></navPoint>`,
				idx, ch.Title, idx, ch.Title+" 正文", href)
			continue
		}
		fmt.Fprintf(&spine, `<itemref idref="ch%d"/>`, idx)
		fmt.Fprintf(&navPoints, `<navPoint id="np%d"><navLabel><text>%s</text></navLabel><content src="%s"/></navPoint>`,
			idx, ch.Title, href)
		write("OEBPS/"+href, []byte(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>%s</title></head>
<body><h2>%s</h2><p>%s</p></body></html>`, ch.Title, ch.Title, ch.Content)))
	}

	write("OEBPS/content.opf", []byte(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>%s</dc:title><dc:creator>%s</dc:creator>
  </metadata>
  <manifest>%s<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/></manifest>
  <spine toc="ncx">%s</spine>
</package>`, title, author, manifest.String(), spine.String())))

	write("OEBPS/toc.ncx", []byte(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <navMap>%s</navMap>
</ncx>`, navPoints.String())))

	for name, data := range extra {
		write(name, data)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// ---- 导入 / 查询辅助 ----

func importBook(t *testing.T, base, token, filename string, title, author *string, data []byte) (int, []byte) {
	t.Helper()
	q := url.Values{}
	q.Set("filename", filename)
	if title != nil {
		q.Set("title", *title)
	}
	if author != nil {
		q.Set("author", *author)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/books/import?"+q.Encode(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("new import request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("import request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

type importedBook struct {
	Book struct {
		ID            uuid.UUID `json:"id"`
		Name          string    `json:"name"`
		BookType      string    `json:"book_type"`
		TotalChapters int       `json:"total_chapters"`
		FileHash      *string   `json:"file_hash"`
		ActiveBookURL *string   `json:"active_book_url"`
		NeedsSource   *bool     `json:"needs_source"`
		Progress      *struct{} `json:"progress"`
	} `json:"book"`
	ChapterCount int      `json:"chapter_count"`
	FileType     string   `json:"file_type"`
	FileHash     string   `json:"file_hash"`
	Warnings     []string `json:"warnings"`
}

func decodeImport(t *testing.T, raw []byte) importedBook {
	t.Helper()
	var out importedBook
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode import response: %v\n%s", err, raw)
	}
	return out
}

func localSourceIDOf(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(dbString(t, pool,
		`SELECT id FROM book_sources WHERE user_id=$1 AND book_source_type=3 LIMIT 1`, userID))
	if err != nil {
		t.Fatalf("local source id: %v", err)
	}
	return id
}

func TestLocalEPUBImportAndRead(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	_ = userID

	data := buildEPUB(t, "测试之书", "作者甲", []epubChapter{
		{Title: "第一章 开端", Content: "正文一"},
		{Title: "第二章 继续", Content: "正文二"},
	}, nil)
	status, raw := importBook(t, base, token, "book.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("import status = %d, want 201: %s", status, raw)
	}
	res := decodeImport(t, raw)
	if res.Book.BookType != "local" {
		t.Fatalf("book_type = %q, want local", res.Book.BookType)
	}
	if res.ChapterCount < 1 {
		t.Fatalf("chapter_count = %d, want >=1", res.ChapterCount)
	}
	// 导入响应中的 book.progress 必须是显式 null（非字段缺失）。
	if !bytes.Contains(raw, []byte(`"progress":null`)) {
		t.Fatalf("import response must contain explicit progress:null: %s", raw)
	}

	status, raw = doJSON(t, http.MethodGet, base+"/books/"+res.Book.ID.String()+"/chapters", token, nil)
	if status != http.StatusOK {
		t.Fatalf("chapters status = %d: %s", status, raw)
	}
	var list struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode chapters: %v", err)
	}
	if list.Total != res.ChapterCount {
		t.Fatalf("chapters total = %d, want %d (无卷名行样本)", list.Total, res.ChapterCount)
	}

	status, raw = doJSON(t, http.MethodGet, base+"/books/"+res.Book.ID.String()+"/content?chapter_index=0", token, nil)
	if status != http.StatusOK {
		t.Fatalf("content status = %d: %s", status, raw)
	}
	var content struct {
		SourceID uuid.UUID `json:"source_id"`
		Content  string    `json:"content"`
		TraceID  *string   `json:"trace_id"`
	}
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content.SourceID != localSourceIDOf(t, pool, userID) {
		t.Fatalf("content source_id = %s, want local source", content.SourceID)
	}
	if content.TraceID != nil {
		t.Fatalf("trace_id = %v, want nil", *content.TraceID)
	}
	if !bytes.Contains(raw, []byte(`"trace_id":null`)) {
		t.Fatalf("content response must contain explicit trace_id:null: %s", raw)
	}
	if !strings.Contains(content.Content, "正文一") {
		t.Fatalf("content = %q, want contains 正文一", content.Content)
	}
}

func TestLocalTXTEncodingsAndVoting(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	plain := "第一章 开端\n内容一\n\n第二章 发展\n内容二\n\n第三章 结局\n内容三\n"
	utf16le := append([]byte{0xFF, 0xFE}, utf16Encode(t, plain, true)...)
	utf16be := append([]byte{0xFE, 0xFF}, utf16Encode(t, plain, false)...)
	gbk := gbkEncode(t, plain)

	cases := []struct {
		name     string
		data     []byte
		wantChap int
	}{
		{"gbk.txt", gbk, 3},
		{"le.txt", utf16le, 3},
		{"be.txt", utf16be, 3},
		{"utf8.txt", []byte(plain), 3},
		{"big5.txt", big5Encode(t, "第一章 開端\n內容一\n第二章 發展\n內容二\n"), 2},
		{"short.txt", []byte("第一章 甲\n内容A\n第二章 乙\n内容B\n"), 2},
		{"indent-full.txt", []byte("　　第一章 甲\n内容A\n　　第二章 乙\n内容B\n"), 2},
		{"indent-half.txt", []byte("  第一章 甲\n内容A\n  第二章 乙\n内容B\n"), 2},
	}
	for _, c := range cases {
		status, raw := importBook(t, base, token, c.name, nil, nil, c.data)
		if status != http.StatusCreated {
			t.Fatalf("%s: status = %d, want 201: %s", c.name, status, raw)
		}
		res := decodeImport(t, raw)
		if res.ChapterCount != c.wantChap {
			t.Fatalf("%s: chapter_count = %d, want %d (warnings=%v)", c.name, res.ChapterCount, c.wantChap, res.Warnings)
		}
	}

	// 无标题短篇：整本单章 + warning。
	status, raw := importBook(t, base, token, "no-title.txt", nil, nil, []byte("只是一段没有章节标记的正文。"))
	if status != http.StatusCreated {
		t.Fatalf("no-title status = %d: %s", status, raw)
	}
	res := decodeImport(t, raw)
	if res.ChapterCount != 1 {
		t.Fatalf("no-title chapter_count = %d, want 1", res.ChapterCount)
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("no-title import must produce a warning")
	}
}

func TestLocalImportIdempotentAndMetadata(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "幂等之书", "作者乙", []epubChapter{
		{Title: "第一卷 起始", Volume: true},
		{Title: "第一章 甲", Content: "甲"},
		{Title: "第二章 乙", Content: "乙"},
		{Title: "第三章 丙", Content: "丙"},
	}, nil)

	status, raw := importBook(t, base, token, "dup.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("first import = %d: %s", status, raw)
	}
	first := decodeImport(t, raw)

	status, raw = importBook(t, base, token, "dup.epub", nil, nil, data)
	if status != http.StatusOK {
		t.Fatalf("second import = %d, want 200: %s", status, raw)
	}
	second := decodeImport(t, raw)
	if second.Book.ID != first.Book.ID {
		t.Fatalf("second import book id = %s, want %s", second.Book.ID, first.Book.ID)
	}

	if got := dbInt(t, pool, `SELECT total_chapters FROM books WHERE id=$1 AND user_id=$2`, first.Book.ID, userID); got != first.ChapterCount {
		t.Fatalf("books.total_chapters = %d, want %d", got, first.ChapterCount)
	}
	var hash string
	if err := pool.QueryRow(context.Background(), `SELECT file_hash FROM books WHERE id=$1`, first.Book.ID).Scan(&hash); err != nil {
		t.Fatalf("select file_hash: %v", err)
	}
	if hash == "" || hash != first.FileHash {
		t.Fatalf("file_hash = %q, want %q", hash, first.FileHash)
	}
	// 卷名行不进 total：非卷章节数 = 3。
	if first.ChapterCount != 3 {
		t.Fatalf("chapter_count = %d, want 3 (volume row excluded)", first.ChapterCount)
	}
	// active_book_url 与 active_source_id 必须成对：local:<bookID>。
	if first.Book.ActiveBookURL == nil || *first.Book.ActiveBookURL != "local:"+first.Book.ID.String() {
		t.Fatalf("active_book_url = %v, want local:%s", first.Book.ActiveBookURL, first.Book.ID)
	}
}

func TestLocalSameNameRenameAndConflict(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data1 := buildEPUB(t, "同名之书", "作者丙", []epubChapter{{Title: "第一章", Content: "一"}}, nil)
	status, raw := importBook(t, base, token, "a.epub", nil, nil, data1)
	if status != http.StatusCreated {
		t.Fatalf("first import = %d: %s", status, raw)
	}
	first := decodeImport(t, raw)

	// 不同文件、同名同作者 → 自动重命名 (1)，仍 201。
	data2 := buildEPUB(t, "同名之书", "作者丙", []epubChapter{{Title: "第一章", Content: "二"}}, nil)
	status, raw = importBook(t, base, token, "b.epub", nil, nil, data2)
	if status != http.StatusCreated {
		t.Fatalf("second import = %d, want 201: %s", status, raw)
	}
	second := decodeImport(t, raw)
	if second.Book.ID == first.Book.ID {
		t.Fatalf("second book must be a new row")
	}
	if !strings.Contains(second.Book.Name, "(1)") {
		t.Fatalf("second book name = %q, want suffix (1)", second.Book.Name)
	}

	// 显式指定已存在的书名（同作者）→ 409。
	explicit := first.Book.Name
	status, raw = importBook(t, base, token, "c.epub", &explicit, nil, buildEPUB(t, "x", "作者丙", []epubChapter{{Title: "第一章", Content: "三"}}, nil))
	if status != http.StatusConflict {
		t.Fatalf("explicit conflict = %d, want 409: %s", status, raw)
	}
}

func TestLocalEndpointsRejectLocalBook(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "受限之书", "作者丁", []epubChapter{{Title: "第一章", Content: "一"}}, nil)
	status, raw := importBook(t, base, token, "r.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("import = %d: %s", status, raw)
	}
	bookID := decodeImport(t, raw).Book.ID
	onlineSource := seedSource(t, pool, userID, "https://example.com/src", `{}`)

	status, raw = doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/switch-source", token, mustJSON(t, map[string]any{
		"source_id": onlineSource.String(), "book_url": "https://example.com/book/1",
	}))
	if status != http.StatusBadRequest {
		t.Fatalf("switch-source on local book = %d, want 400: %s", status, raw)
	}
	status, raw = doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/chapters/0/override", token,
		mustJSON(t, map[string]any{"chapter_url": "https://example.com/c/1"}))
	if status != http.StatusBadRequest {
		t.Fatalf("override on local book = %d, want 400: %s", status, raw)
	}
	status, raw = doJSON(t, http.MethodDelete, base+"/books/"+bookID.String()+"/chapters/0/override", token, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("delete override on local book = %d, want 400: %s", status, raw)
	}
}

// newTestServerWithSentinel 与 newTestServerWithPool 相同，但在服务端 Handler 外层
// 注入业务哨兵 value，用于验证清理 ctx 未继承业务请求上下文。
func newTestServerWithSentinel(t *testing.T, mutate func(pool *pgxpool.Pool, deps *api.Deps)) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.TestPool(t)
	ft := fetcher.New(fetcher.Config{})
	sb := sandbox.New(sandbox.Config{ScriptTimeout: 400 * time.Millisecond}, nil)
	store, err := storage.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := api.Deps{
		Auth:         service.NewAuthService(pool, []byte("test-secret"), time.Minute, time.Hour),
		Sources:      service.NewSourceService(pool),
		Books:        service.NewBookService(pool, store, logger),
		Progress:     service.NewProgressService(pool),
		Mapping:      service.NewMappingService(pool),
		Overrides:    service.NewOverrideService(pool),
		Content:      service.NewContentService(pool, ft, sb, testCache(), store, logger),
		Import:       service.NewImportService(pool, store, logger),
		Settings:     service.NewSettingsService(pool, logger),
		Bookmarks:    service.NewBookmarkService(pool, logger),
		ReplaceRules: service.NewReplaceRuleService(pool, logger),
		Storage:      store,
		DBPing:       func(ctx context.Context) error { return pool.Ping(ctx) },
		Log:          logger,
	}
	if mutate != nil {
		mutate(pool, &deps)
	}
	srv := api.NewServer(deps)
	baseHandler := srv.Handler()

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), testSentinelKey{}, testSentinelValue))
		baseHandler.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	return ts, pool
}

// failingStorage 在第 failAt 次 Put 时返回错误，并记录每次写入的 ctx。
type failingStorage struct {
	inner             storage.Provider
	mu                sync.Mutex
	puts              int
	failAt            int
	ctxList           []context.Context
	deleteCtx         context.Context
	deletePrefixCalls int
}

func (f *failingStorage) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	f.mu.Lock()
	f.puts++
	n := f.puts
	f.ctxList = append(f.ctxList, ctx)
	f.mu.Unlock()
	if n == f.failAt {
		return errors.New("injected storage failure")
	}
	return f.inner.Put(ctx, key, r, size)
}
func (f *failingStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return f.inner.Get(ctx, key)
}
func (f *failingStorage) Delete(ctx context.Context, key string) error {
	return f.inner.Delete(ctx, key)
}
func (f *failingStorage) Exists(ctx context.Context, key string) (bool, error) {
	return f.inner.Exists(ctx, key)
}
func (f *failingStorage) DeletePrefix(ctx context.Context, prefix string) error {
	f.mu.Lock()
	f.deleteCtx = ctx
	f.deletePrefixCalls++
	f.mu.Unlock()
	return f.inner.DeletePrefix(ctx, prefix)
}

func TestLocalImportFailureCompensation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inner, err := storage.NewLocalFS(dir)
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	failStore := &failingStorage{inner: inner, failAt: 2}

	ts, pool := newTestServerWithSentinel(t, func(p *pgxpool.Pool, deps *api.Deps) {
		deps.Import = service.NewImportService(p, failStore, discardLogger())
		deps.Storage = failStore
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "失败之书", "作者戊", []epubChapter{
		{Title: "第一章", Content: "一"},
		{Title: "第二章", Content: "二"},
		{Title: "第三章", Content: "三"},
	}, nil)
	status, raw := importBook(t, base, token, "fail.epub", nil, nil, data)
	if status != http.StatusInternalServerError {
		t.Fatalf("import with failing storage = %d, want 500: %s", status, raw)
	}
	if got := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND name='失败之书'`, userID); got != 0 {
		t.Fatalf("books rows after failure = %d, want 0", got)
	}

	failStore.mu.Lock()
	cleanupCtx := failStore.deleteCtx
	calls := failStore.deletePrefixCalls
	failStore.mu.Unlock()

	if calls == 0 || cleanupCtx == nil {
		t.Fatal("DeletePrefix not called during cleanup")
	}
	// ① 未继承业务哨兵 value。
	if v := cleanupCtx.Value(testSentinelKey{}); v != nil {
		t.Fatalf("cleanup ctx inherited business sentinel value: %v", v)
	}
	// ② 具有独立 Deadline（不论早晚）。
	if _, ok := cleanupCtx.Deadline(); !ok {
		t.Fatal("cleanup ctx has no deadline")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLocalSourceIsolationAndContent(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "隔离之书", "作者己", []epubChapter{{Title: "第一章", Content: "内容"}}, nil)
	status, raw := importBook(t, base, token, "iso.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("import = %d: %s", status, raw)
	}
	bookID := decodeImport(t, raw).Book.ID

	status, raw = doJSON(t, http.MethodGet, base+"/sources?page_size=100", token, nil)
	if status != http.StatusOK {
		t.Fatalf("sources = %d: %s", status, raw)
	}
	if strings.Contains(string(raw), "本地书库") {
		t.Fatalf("/sources must not expose local library source: %s", raw)
	}

	status, raw = doJSON(t, http.MethodGet, base+"/search?keyword=abc", token, nil)
	if status != http.StatusOK {
		t.Fatalf("search = %d: %s", status, raw)
	}
	if strings.Contains(string(raw), "本地书库") {
		t.Fatalf("/search must not use local library source: %s", raw)
	}

	status, raw = doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/content?chapter_index=0", token, nil)
	if status != http.StatusOK {
		t.Fatalf("local content = %d: %s", status, raw)
	}
}

func TestLocalBookshelfSemantics(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "书架之书", "作者庚", []epubChapter{{Title: "第一章", Content: "一"}}, nil)
	status, raw := importBook(t, base, token, "shelf.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("import = %d: %s", status, raw)
	}
	bookID := decodeImport(t, raw).Book.ID

	status, raw = doJSON(t, http.MethodGet, base+"/books?page_size=100", token, nil)
	if status != http.StatusOK {
		t.Fatalf("bookshelf = %d: %s", status, raw)
	}
	var page struct {
		Items []struct {
			ID          uuid.UUID `json:"id"`
			BookType    string    `json:"book_type"`
			NeedsSource *bool     `json:"needs_source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode bookshelf: %v", err)
	}
	var found bool
	for _, it := range page.Items {
		if it.ID == bookID {
			found = true
			if it.BookType != "local" {
				t.Fatalf("book_type = %q, want local", it.BookType)
			}
			if it.NeedsSource == nil || *it.NeedsSource {
				t.Fatalf("needs_source = %v, want false", it.NeedsSource)
			}
		}
	}
	if !found {
		t.Fatalf("imported book missing from bookshelf")
	}
}

func TestLocalProgressSync(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "进度之书", "作者辛", []epubChapter{
		{Title: "第一章", Content: "一"},
		{Title: "第二章", Content: "二"},
	}, nil)
	status, raw := importBook(t, base, token, "prog.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("import = %d: %s", status, raw)
	}
	bookID := decodeImport(t, raw).Book.ID

	status, raw = doJSON(t, http.MethodGet, base+"/books/"+bookID.String()+"/progress", token, nil)
	if status != http.StatusOK {
		t.Fatalf("get progress = %d: %s", status, raw)
	}
	var prog struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(raw, &prog)
	if prog.Version != 1 {
		t.Fatalf("initial progress version = %d, want 1", prog.Version)
	}

	status, raw = doJSONH(t, http.MethodPut, base+"/books/"+bookID.String()+"/progress", token,
		mustJSON(t, map[string]any{"chapter_index": 1, "chapter_ratio": 0.5}),
		map[string]string{"If-Match": `"1"`})
	if status != http.StatusOK {
		t.Fatalf("put progress = %d: %s", status, raw)
	}
	status, raw = doJSONH(t, http.MethodPut, base+"/books/"+bookID.String()+"/progress", token,
		mustJSON(t, map[string]any{"chapter_index": 0}),
		map[string]string{"If-Match": `"1"`})
	if status != http.StatusConflict {
		t.Fatalf("stale progress = %d, want 409: %s", status, raw)
	}
}

func TestLocalDeleteCleansFiles(t *testing.T) {
	t.Parallel()
	var store storage.Provider
	ts, pool := newTestServerWithPool(t, nil, func(_ *pgxpool.Pool, deps *api.Deps) {
		store = deps.Storage
	})
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "删除之书", "作者壬", []epubChapter{{Title: "第一章", Content: "一"}}, nil)
	status, raw := importBook(t, base, token, "del.epub", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("import = %d: %s", status, raw)
	}
	bookID := decodeImport(t, raw).Book.ID

	key := "local/" + bookID.String() + "/0.txt"
	if ok, err := store.Exists(context.Background(), key); err != nil || !ok {
		t.Fatalf("chapter file must exist before delete: ok=%v err=%v", ok, err)
	}

	status, raw = doJSON(t, http.MethodDelete, base+"/books/"+bookID.String(), token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", status, raw)
	}
	if got := dbInt(t, pool, `SELECT count(*) FROM books WHERE id=$1 AND user_id=$2`, bookID, userID); got != 0 {
		t.Fatalf("book still exists after delete")
	}
	if ok, err := store.Exists(context.Background(), key); err != nil || ok {
		t.Fatalf("chapter file must be removed after delete: ok=%v err=%v", ok, err)
	}
}

func TestLocalCreatePreallocatedIDAndFileHash(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildTXT([]string{"甲", "乙"})
	status, raw := importBook(t, base, token, "id.txt", nil, nil, data)
	if status != http.StatusCreated {
		t.Fatalf("import = %d: %s", status, raw)
	}
	res := decodeImport(t, raw)
	if res.Book.ID == uuid.Nil {
		t.Fatalf("preallocated book id must be non-nil")
	}
	if res.Book.FileHash == nil || *res.Book.FileHash == "" {
		t.Fatalf("file_hash must be present")
	}
	var activeSource *uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT active_source_id FROM books WHERE id=$1 AND user_id=$2`, res.Book.ID, userID).Scan(&activeSource); err != nil {
		t.Fatalf("select active_source_id: %v", err)
	}
	if activeSource == nil {
		t.Fatalf("active_source_id must be set for local books")
	}
}

func TestLocalConcurrentImport(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	data := buildEPUB(t, "并发之书", "作者癸", []epubChapter{{Title: "第一章", Content: "一"}}, nil)
	start := time.Now()
	type outcome struct {
		status int
		id     uuid.UUID
		body   string
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, raw := importBook(t, base, token, "conc.epub", nil, nil, data)
			var res importedBook
			_ = json.Unmarshal(raw, &res)
			results <- outcome{status: status, id: res.Book.ID, body: string(raw)}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	t.Logf("concurrent import elapsed: %v", elapsed)
	close(results)

	var got []outcome
	for r := range results {
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("got %d outcomes", len(got))
	}
	if got[0].id != got[1].id {
		t.Fatalf("concurrent imports must converge to one book: %s vs %s\nbody0=%s\nbody1=%s",
			got[0].id, got[1].id, got[0].body, got[1].body)
	}
	counts := map[int]int{}
	for _, r := range got {
		counts[r.status]++
	}
	if counts[http.StatusCreated] != 1 || counts[http.StatusOK] != 1 {
		t.Fatalf("concurrent import statuses = %v, want one 201 and one 200", counts)
	}
	if elapsed >= 1000*time.Millisecond {
		t.Fatalf("concurrent import took %v, want < 1000ms (1s backoff removed)", elapsed)
	}
	if n := dbInt(t, pool, `SELECT count(*) FROM books WHERE user_id=$1 AND file_hash<>''`, userID); n != 1 {
		t.Fatalf("books with hash = %d, want 1", n)
	}
}

func TestLocalDetectFileType(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	epub := buildEPUB(t, "嗅探", "作者", []epubChapter{{Title: "第一章", Content: "一"}}, nil)
	// .txt + PK 魔数 → 按 EPUB 处理。
	status, raw := importBook(t, base, token, "actually.txt", nil, nil, epub)
	if status != http.StatusCreated {
		t.Fatalf(".txt with PK = %d, want 201: %s", status, raw)
	}
	if res := decodeImport(t, raw); res.FileType != "epub" {
		t.Fatalf("file_type = %q, want epub", res.FileType)
	}
	// .epub + 普通文本 → 按 TXT 处理。
	status, raw = importBook(t, base, token, "actually.epub", nil, nil, []byte("第一章 甲\n内容\n第二章 乙\n内容"))
	if status != http.StatusCreated {
		t.Fatalf(".epub with text = %d, want 201: %s", status, raw)
	}
	if res := decodeImport(t, raw); res.FileType != "txt" {
		t.Fatalf("file_type = %q, want txt", res.FileType)
	}
}

func TestLocalFilenameSanitize(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	// 无扩展名 → 400。
	if status, raw := importBook(t, base, token, "../../etc/passwd", nil, nil, buildTXT([]string{"一"})); status != http.StatusBadRequest {
		t.Fatalf("no-extension = %d, want 400: %s", status, raw)
	}
	// 路径穿越 + .txt → 只保留基名，正常导入（每个用例用不同内容避免按哈希幂等）。
	if status, raw := importBook(t, base, token, "../../evil.txt", nil, nil, buildTXT([]string{"二"})); status != http.StatusCreated {
		t.Fatalf("traversal txt = %d, want 201: %s", status, raw)
	}
	if status, raw := importBook(t, base, token, `..\..\evil2.txt`, nil, nil, buildTXT([]string{"三"})); status != http.StatusCreated {
		t.Fatalf("windows traversal = %d, want 201: %s", status, raw)
	}
	if status, raw := importBook(t, base, token, "a%2Fb.txt", nil, nil, buildTXT([]string{"四"})); status != http.StatusCreated {
		t.Fatalf("percent-encoded slash = %d, want 201: %s", status, raw)
	}
}

func TestLocalRejectBadExtensions(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	for _, name := range []string{"a.doc", "a.pdf", "noext"} {
		if status, raw := importBook(t, base, token, name, nil, nil, []byte("第一章 甲\n正文")); status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", name, status, raw)
		}
	}
	// .doc + UTF-16 BOM → 400。
	if status, raw := importBook(t, base, token, "b.doc", nil, nil, []byte{0xFF, 0xFE, 'a', 0}); status != http.StatusBadRequest {
		t.Fatalf(".doc with BOM = %d, want 400: %s", status, raw)
	}
	// .docx + PK 魔数 → 400。
	if status, raw := importBook(t, base, token, "c.docx", nil, nil, []byte("PK\x03\x04zzz")); status != http.StatusBadRequest {
		t.Fatalf(".docx with PK = %d, want 400: %s", status, raw)
	}
}

func TestLocalEnsureSourceConcurrent(t *testing.T) {
	t.Parallel()
	_, pool := newTestServer(t, nil)
	userID, _, _ := seedUser(t, pool)

	svc := service.NewLocalSourceService(pool)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.EnsureLocalSource(context.Background(), userID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ensure local source: %v", err)
		}
	}
	if n := dbInt(t, pool, `SELECT count(*) FROM book_sources WHERE user_id=$1 AND book_source_type=3`, userID); n != 1 {
		t.Fatalf("local source rows = %d, want 1", n)
	}
}

func TestLocalImportPayloadTooLarge(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	huge := io.LimitReader(zeroReader{}, int64(101<<20))
	req, err := http.NewRequest(http.MethodPost, base+"/books/import?filename=huge.txt", huge)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(101 << 20)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("huge import: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge import = %d, want 413: %s", resp.StatusCode, raw)
	}
	if !bytes.Contains(raw, []byte("payload_too_large")) {
		t.Fatalf("413 body must carry payload_too_large: %s", raw)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestLocalDRMAndFontObfuscation(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	drm := buildEPUB(t, "加密之书", "作者", []epubChapter{{Title: "第一章", Content: "一"}}, map[string][]byte{
		"META-INF/encryption.xml": []byte(`<?xml version="1.0"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <EncryptedData><EncryptionMethod Algorithm="http://www.w3.org/2001/04/xmlenc#aes128-cbc"/>
  <CipherData><CipherReference URI="OEBPS/ch1.xhtml"/></CipherData></EncryptedData>
</encryption>`),
	})
	status, raw := importBook(t, base, token, "drm.epub", nil, nil, drm)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("drm import = %d, want 422: %s", status, raw)
	}
	if !bytes.Contains(raw, []byte(`"unsupported_rule"`)) || !bytes.Contains(raw, []byte(`"drm_protected"`)) {
		t.Fatalf("drm error body = %s", raw)
	}

	fontOnly := buildEPUB(t, "混淆字体", "作者", []epubChapter{{Title: "第一章", Content: "一"}}, map[string][]byte{
		"META-INF/encryption.xml": []byte(`<?xml version="1.0"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <EncryptedData><EncryptionMethod Algorithm="http://www.idpf.org/2008/embedding"/>
  <CipherData><CipherReference URI="OEBPS/font.ttf"/></CipherData></EncryptedData>
</encryption>`),
	})
	if status, raw := importBook(t, base, token, "font.epub", nil, nil, fontOnly); status != http.StatusCreated {
		t.Fatalf("font obfuscation import = %d, want 201: %s", status, raw)
	}
}

// ---- 编码辅助 ----

func utf16Encode(t *testing.T, s string, littleEndian bool) []byte {
	t.Helper()
	buf := make([]byte, 0, len(s)*2)
	for _, r := range s {
		if r > 0xFFFF {
			r = '?'
		}
		if littleEndian {
			buf = append(buf, byte(r), byte(r>>8))
		} else {
			buf = append(buf, byte(r>>8), byte(r))
		}
	}
	return buf
}

func gbkEncode(t *testing.T, s string) []byte {
	t.Helper()
	out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("gbk encode: %v", err)
	}
	return out
}

func big5Encode(t *testing.T, s string) []byte {
	t.Helper()
	out, err := traditionalchinese.Big5.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("big5 encode: %v", err)
	}
	return out
}

// TestLocalTXT3000Chapters 记录 3000 章 TXT 的导入耗时（并发 8 写入，90s 预算）。
func TestLocalTXT3000Chapters(t *testing.T) {
	t.Parallel()
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	_, username, password := seedUser(t, pool)
	token := login(t, base, username, password)

	var b bytes.Buffer
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&b, "第%d章 标题%d\n这是第%d章的正文内容，用于测量批量导入耗时。\n\n", i, i, i)
	}
	start := time.Now()
	status, raw := importBook(t, base, token, "big.txt", nil, nil, b.Bytes())
	elapsed := time.Since(start)
	if status != http.StatusCreated {
		t.Fatalf("3000-chapter import = %d: %s", status, raw)
	}
	res := decodeImport(t, raw)
	if res.ChapterCount != 3000 {
		t.Fatalf("chapter_count = %d, want 3000", res.ChapterCount)
	}
	t.Logf("3000 章 TXT 导入耗时: %s（chapter_count=%d）", elapsed.Round(time.Millisecond), res.ChapterCount)
}
