package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

// Fetcher 是 /search 依赖的窄接口。生产注入 *fetcher.Fetcher；测试注入 mock，
// 避免 httptest.Server 监听 127.0.0.1 被真实 SSRF 拦截。
type Fetcher interface {
	Get(ctx context.Context, url string, header http.Header) (*fetcher.Response, error)
}

// BookItem 是单个书源的搜索结果条目。
type BookItem struct {
	Name          string
	Author        string
	BookURL       string
	CoverURL      string
	Intro         string
	Kind          string
	LatestChapter string
	WordCount     *int
}

// GroupResult 是单个成功书源的结果组。
type GroupResult struct {
	SourceID   uuid.UUID
	SourceName string
	ElapsedMs  int
	Items      []BookItem
}

// SearchFailure 记录单个书源的失败（Err 由 HTTP 层统一映射为错误码）。
type SearchFailure struct {
	SourceID uuid.UUID
	Err      error
}

// SearchOutcome 是 /search 的聚合结果。
type SearchOutcome struct {
	Groups   []GroupResult
	Failures []SearchFailure
}

// SearchService 编排多源并发搜索。
type SearchService struct {
	pool    *pgxpool.Pool
	sources *repository.SourceRepository
	ft      Fetcher
	sb      *sandbox.Sandbox
}

// NewSearchService 构造搜索服务。
func NewSearchService(pool *pgxpool.Pool, ft Fetcher, sb *sandbox.Sandbox) *SearchService {
	return &SearchService{
		pool:    pool,
		sources: repository.NewSourceRepository(),
		ft:      ft,
		sb:      sb,
	}
}

// Search 执行多源并发搜索。部分失败隔离：任何单源失败都只进 Failures。
func (s *SearchService) Search(ctx context.Context, userID uuid.UUID, keyword string, sourceIDs []uuid.UUID, page, pageSize int) (*SearchOutcome, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("%w: keyword is required", repository.ErrInvalidArgument)
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	out := &SearchOutcome{Groups: []GroupResult{}, Failures: []SearchFailure{}}
	sources, early, err := s.pickSources(ctx, userID, sourceIDs)
	if err != nil {
		return nil, err
	}
	out.Failures = append(out.Failures, early...)

	// 全局并发度 ≤ 10；严禁 errgroup 在单源失败时 cancel 整个 context。
	sem := semaphore.NewWeighted(10)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, src := range sources {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.Acquire(ctx, 1); err != nil {
				mu.Lock()
				out.Failures = append(out.Failures, SearchFailure{SourceID: src.ID, Err: err})
				mu.Unlock()
				return
			}
			defer sem.Release(1)

			limiter := newSourceLimiter(derefString(src.ConcurrentRate))
			if err := limiter.acquire(ctx); err != nil {
				mu.Lock()
				out.Failures = append(out.Failures, SearchFailure{SourceID: src.ID, Err: err})
				mu.Unlock()
				return
			}
			defer limiter.release()

			start := time.Now()
			items, err := s.searchOne(ctx, src, keyword, page, pageSize)
			if err != nil {
				mu.Lock()
				out.Failures = append(out.Failures, SearchFailure{SourceID: src.ID, Err: err})
				mu.Unlock()
				return
			}
			mu.Lock()
			out.Groups = append(out.Groups, GroupResult{
				SourceID:   src.ID,
				SourceName: src.BookSourceName,
				ElapsedMs:  int(time.Since(start).Milliseconds()),
				Items:      items,
			})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}

// pickSources 解析参与搜索的书源：去重、容量防御、单次全查、内存三路分流。
func (s *SearchService) pickSources(ctx context.Context, userID uuid.UUID, sourceIDs []uuid.UUID) ([]*repository.BookSource, []SearchFailure, error) {
	if len(sourceIDs) == 0 {
		enabled := true
		list, _, err := s.sources.List(ctx, s.pool, userID, repository.SourceListFilter{
			Enabled: &enabled, Sort: "weight_desc", Limit: 20, ExcludeLocal: true,
		})
		return list, nil, err
	}

	seen := make(map[uuid.UUID]struct{}, len(sourceIDs))
	dedup := make([]uuid.UUID, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		dedup = append(dedup, id)
	}
	if len(dedup) > 20 {
		return nil, nil, fmt.Errorf("%w: at most 20 source_ids", repository.ErrInvalidArgument)
	}

	// 一次 SQL 查出全部匹配项（不过滤 enabled，留给内存分流）。
	list, _, err := s.sources.List(ctx, s.pool, userID, repository.SourceListFilter{IDs: dedup, Limit: len(dedup), ExcludeLocal: true})
	if err != nil {
		return nil, nil, err
	}
	found := make(map[uuid.UUID]*repository.BookSource, len(list))
	for _, src := range list {
		found[src.ID] = src
	}

	failures := make([]SearchFailure, 0, len(dedup))
	ok := make([]*repository.BookSource, 0, len(dedup))
	for _, id := range dedup {
		src, exists := found[id]
		switch {
		case !exists:
			failures = append(failures, SearchFailure{SourceID: id, Err: ErrSourceNotFound})
		case !src.Enabled:
			failures = append(failures, SearchFailure{SourceID: id, Err: ErrSourceDisabled})
		default:
			ok = append(ok, src)
		}
	}
	return ok, failures, nil
}

// searchOne 抓取并解析单个书源的搜索结果。
func (s *SearchService) searchOne(ctx context.Context, src *repository.BookSource, keyword string, page, pageSize int) ([]BookItem, error) {
	rules := parseSearchRules(src.Rules)
	if rules.SearchURL == "" || rules.BookList == "" {
		return nil, fmt.Errorf("%w: source has no search rule", sandbox.ErrUnsupportedRule)
	}

	target := expandSearchURL(rules.SearchURL, keyword, page)
	resp, err := s.ft.Get(ctx, target, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		// 4xx/5xx 拦截：绝不把错误页送入 bookList 解析。
		return nil, fmt.Errorf("%w: upstream status %d", fetcher.ErrUpstreamError, resp.StatusCode)
	}

	base := resp.FinalURL
	if base == "" {
		base = src.BookSourceURL
	}
	items, err := s.parseBookList(ctx, string(resp.Body), base, rules)
	if err != nil {
		return nil, err
	}
	if len(items) > pageSize {
		items = items[:pageSize]
	}
	return items, nil
}

type searchRules struct {
	SearchURL, BookList, Name, Author, BookURL, CoverURL, Intro, Kind, LastChapter, WordCount string
}

// parseSearchRules 优先读取结构化 rules.search 对象，缺失时回退顶层扁平字段。
func parseSearchRules(raw json.RawMessage) searchRules {
	var obj map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &obj) != nil {
		return searchRules{}
	}
	var out searchRules
	if nested, ok := obj["search"]; ok {
		var m map[string]json.RawMessage
		if json.Unmarshal(nested, &m) == nil {
			out.SearchURL = rawText(m, "searchUrl")
			out.BookList = rawText(m, "bookList")
			out.Name = rawText(m, "name")
			out.Author = rawText(m, "author")
			out.BookURL = rawText(m, "bookUrl")
			out.CoverURL = rawText(m, "coverUrl")
			out.Intro = rawText(m, "intro")
			out.Kind = rawText(m, "kind")
			out.LastChapter = rawText(m, "lastChapter")
			out.WordCount = rawText(m, "wordCount")
		}
	}
	if out.SearchURL == "" {
		out.SearchURL = rawText(obj, "searchUrl")
	}
	if out.BookList == "" {
		out.BookList = firstNonEmpty(rawText(obj, "ruleSearchList"), nestedText(obj, "ruleSearch", "bookList"))
	}
	if out.Name == "" {
		out.Name = firstNonEmpty(rawText(obj, "ruleSearchName"), nestedText(obj, "ruleSearch", "name"))
	}
	if out.Author == "" {
		out.Author = firstNonEmpty(rawText(obj, "ruleSearchAuthor"), nestedText(obj, "ruleSearch", "author"))
	}
	if out.BookURL == "" {
		out.BookURL = firstNonEmpty(rawText(obj, "ruleSearchBookUrl"), nestedText(obj, "ruleSearch", "bookUrl"))
	}
	if out.CoverURL == "" {
		out.CoverURL = firstNonEmpty(rawText(obj, "ruleSearchCoverUrl"), nestedText(obj, "ruleSearch", "coverUrl"))
	}
	if out.Intro == "" {
		out.Intro = firstNonEmpty(rawText(obj, "ruleSearchIntro"), nestedText(obj, "ruleSearch", "intro"))
	}
	if out.Kind == "" {
		out.Kind = firstNonEmpty(rawText(obj, "ruleSearchKind"), nestedText(obj, "ruleSearch", "kind"))
	}
	if out.LastChapter == "" {
		out.LastChapter = firstNonEmpty(rawText(obj, "ruleSearchLastChapter"), nestedText(obj, "ruleSearch", "lastChapter"))
	}
	if out.WordCount == "" {
		out.WordCount = firstNonEmpty(rawText(obj, "ruleSearchWordCount"), nestedText(obj, "ruleSearch", "wordCount"))
	}
	return out
}

// expandSearchURL 替换 {{key}}（URL 编码）与 {{page}}；无模板时按 Legado 约定追加参数。
func expandSearchURL(tpl, keyword string, page int) string {
	enc := url.QueryEscape(keyword)
	if strings.Contains(tpl, "{{key}}") || strings.Contains(tpl, "{{page}}") {
		out := strings.ReplaceAll(tpl, "{{key}}", enc)
		return strings.ReplaceAll(out, "{{page}}", strconv.Itoa(page))
	}
	sep := "?"
	if strings.Contains(tpl, "?") {
		sep = "&"
	}
	return tpl + sep + "searchKey=" + enc + "&page=" + strconv.Itoa(page)
}

// parseBookList 用解析引擎的 RuleNode 列表切分解析 bookList，再对每个节点做
// 字段级求值（M1c：替换原 CSS-MVP，支持 CSS/XPath/JSON/Regex/JS 全模式）。
func (s *SearchService) parseBookList(ctx context.Context, content, baseURL string, rules searchRules) ([]BookItem, error) {
	if strings.TrimSpace(rules.BookList) == "" {
		return nil, fmt.Errorf("%w: empty bookList", sandbox.ErrUnsupportedRule)
	}
	engine := parser.New(s.sb)
	nodes, err := engine.EvalListNodes(ctx, rules.BookList, content)
	if err != nil {
		return nil, err
	}
	items := make([]BookItem, 0, len(nodes))
	for _, node := range nodes {
		// 每个 item 开始时重置一次变量池：item 内字段共享，跨 item 隔离。
		engine.ResetVars()
		items = append(items, BookItem{
			Name:          evalBookField(ctx, engine, node, rules.Name),
			Author:        evalBookField(ctx, engine, node, rules.Author),
			BookURL:       resolveURL(baseURL, evalBookField(ctx, engine, node, rules.BookURL)),
			CoverURL:      resolveURL(baseURL, evalBookField(ctx, engine, node, rules.CoverURL)),
			Intro:         evalBookField(ctx, engine, node, rules.Intro),
			Kind:          evalBookField(ctx, engine, node, rules.Kind),
			LatestChapter: evalBookField(ctx, engine, node, rules.LastChapter),
		})
	}
	return items, nil
}

// evalBookField 在单本书节点上执行字段规则；失败时按空值处理（与 Legado 一致，
// 单字段缺失不阻断整条搜索结果）。
func evalBookField(ctx context.Context, engine *parser.Engine, node parser.RuleNode, rule string) string {
	if strings.TrimSpace(rule) == "" {
		return ""
	}
	v, err := engine.EvalField(ctx, node, rule)
	if err != nil {
		return ""
	}
	return v
}

// resolveURL 以 base 为基准把相对路径转为绝对 URL；已是绝对 URL 时保留。
func resolveURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if parsed.IsAbs() || base == "" {
		return ref
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return baseURL.ResolveReference(parsed).String()
}

// sourceLimiter 实现源级 concurrent_rate：纯整数→并发信号量；"n/ms"→频率窗口。
type sourceLimiter struct {
	sem *semaphore.Weighted
	lim *rate.Limiter
}

func newSourceLimiter(spec string) *sourceLimiter {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return &sourceLimiter{}
	}
	if strings.Contains(spec, "/") {
		parts := strings.SplitN(spec, "/", 2)
		n, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		ms, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err1 == nil && err2 == nil && n > 0 && ms > 0 {
			return &sourceLimiter{lim: rate.NewLimiter(rate.Every(time.Duration(ms)*time.Millisecond/time.Duration(n)), n)}
		}
		return &sourceLimiter{}
	}
	n, err := strconv.Atoi(spec)
	if err == nil && n > 0 {
		return &sourceLimiter{sem: semaphore.NewWeighted(int64(n))}
	}
	return &sourceLimiter{}
}

func (l *sourceLimiter) acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if l.sem != nil {
		return l.sem.Acquire(ctx, 1)
	}
	if l.lim != nil {
		return l.lim.Wait(ctx)
	}
	return nil
}

func (l *sourceLimiter) release() {
	if l != nil && l.sem != nil {
		l.sem.Release(1)
	}
}

func rawText(obj map[string]json.RawMessage, key string) string {
	raw, ok := obj[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func nestedText(obj map[string]json.RawMessage, parent, key string) string {
	raw, ok := obj[parent]
	if !ok {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return rawText(m, key)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
