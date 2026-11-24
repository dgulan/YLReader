package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

// 显式选源上限、进程内并发限制和单源预算。
const (
	searchSourceLimit   = 200
	searchConcurrency   = 16
	searchSourceTimeout = 8 * time.Second
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
	SourceURL  string
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
	health  *SourceHealthService
	logger  *slog.Logger
	slots   chan struct{}
}

// NewSearchService 构造搜索服务。
func NewSearchService(pool *pgxpool.Pool, ft Fetcher, sb *sandbox.Sandbox) *SearchService {
	return &SearchService{
		pool:    pool,
		sources: repository.NewSourceRepository(),
		ft:      ft,
		sb:      sb,
		health:  NewSourceHealthService(pool, nil),
		logger:  slog.Default(),
		slots:   make(chan struct{}, searchConcurrency),
	}
}

// WithHealth 注入自定义书源健康度服务（测试用）。
func (s *SearchService) WithHealth(h *SourceHealthService) *SearchService {
	s.health = h
	return s
}

func (s *SearchService) WithLogger(logger *slog.Logger) *SearchService {
	s.logger = logger
	return s
}

// Search 执行多源并发搜索。部分失败隔离：任何单源失败都只进 Failures。
func (s *SearchService) Search(ctx context.Context, userID uuid.UUID, keyword string, sourceIDs []uuid.UUID, page, pageSize int) (*SearchOutcome, error) {
	return s.SearchProgress(ctx, userID, keyword, sourceIDs, page, pageSize, nil)
}

// SearchProgress reports nonempty groups in completion order and returns the ranked outcome.
func (s *SearchService) SearchProgress(ctx context.Context, userID uuid.UUID, keyword string, sourceIDs []uuid.UUID, page, pageSize int, onGroup func(GroupResult)) (*SearchOutcome, error) {
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

	s.searchSources(ctx, sources, keyword, page, pageSize, out, onGroup)
	s.logSearchSummary(ctx, len(sources), out)
	return out, nil
}

func (s *SearchService) logSearchSummary(ctx context.Context, eligible int, out *SearchOutcome) {
	hits, empty, timeouts, upstream, canceled := 0, 0, 0, 0, 0
	examples := make([]string, 0, 3)
	for _, group := range out.Groups {
		if len(group.Items) == 0 {
			empty++
		} else {
			hits++
		}
	}
	for _, failure := range out.Failures {
		kind, code, safeMessage := ClassifySourceFailure(failure.Err)
		var status *fetcher.HTTPStatusError
		switch {
		case errors.Is(failure.Err, context.Canceled):
			canceled++
		case kind == repository.FailureKindTimeout:
			timeouts++
		case errors.As(failure.Err, &status), kind == repository.FailureKindNetwork, kind == repository.FailureKindAuth:
			upstream++
		}
		if len(examples) < 3 {
			examples = append(examples, code+": "+safeMessage)
		}
	}
	s.logger.InfoContext(ctx, "source search summary", "eligible", eligible, "matched", hits, "empty", empty,
		"timeouts", timeouts, "http_network_errors", upstream, "canceled", canceled, "failures", len(out.Failures), "examples", examples)
}

func (s *SearchService) searchSources(ctx context.Context, sources []*repository.BookSource, keyword string, page, pageSize int, out *SearchOutcome, onGroup func(GroupResult)) {
	type result struct {
		group  GroupResult
		err    error
		weight int32
	}
	results := make([]result, len(sources))
	jobs := make(chan int)
	var workers sync.WaitGroup
	var publish sync.Mutex
	run := func(index int) (res result) {
		src := sources[index]
		defer func() {
			if recover() != nil {
				res.err = fmt.Errorf("search source panic")
			}
		}()
		select {
		case <-ctx.Done():
			res.err = ctx.Err()
			return
		case s.slots <- struct{}{}:
		}
		defer func() { <-s.slots }()
		start := time.Now()
		items, err := s.searchOne(ctx, src, keyword, page, pageSize)
		if err != nil {
			res.err = err
			return
		}
		sort.SliceStable(items, func(i, j int) bool { return searchRelevance(items[i], keyword) > searchRelevance(items[j], keyword) })
		if len(items) > pageSize {
			items = items[:pageSize]
		}
		res.group = GroupResult{SourceID: src.ID, SourceName: src.BookSourceName, SourceURL: src.BookSourceURL, ElapsedMs: int(time.Since(start).Milliseconds()), Items: items}
		res.weight = src.Weight
		return
	}
	for range min(searchConcurrency, len(sources)) {
		workers.Go(func() {
			for index := range jobs {
				res := run(index)
				results[index] = res
				if onGroup != nil && res.err == nil && len(res.group.Items) > 0 {
					publish.Lock()
					if ctx.Err() == nil {
						onGroup(res.group)
					}
					publish.Unlock()
				}
			}
		})
	}
dispatch:
	for i := range sources {
		select {
		case jobs <- i:
		case <-ctx.Done():
			for j := i; j < len(sources); j++ {
				results[j].err = ctx.Err()
			}
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	var ranked []result
	for i, res := range results {
		if res.err != nil {
			out.Failures = append(out.Failures, SearchFailure{SourceID: sources[i].ID, Err: res.err})
		} else {
			ranked = append(ranked, res)
		}
	}
	score := func(g GroupResult) int {
		if len(g.Items) == 0 {
			return -1
		}
		return searchRelevance(g.Items[0], keyword)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if x, y := score(a.group), score(b.group); x != y {
			return x > y
		}
		if a.weight != b.weight {
			return a.weight > b.weight
		}
		return a.group.SourceID.String() < b.group.SourceID.String()
	})
	for _, res := range ranked {
		out.Groups = append(out.Groups, res.group)
	}
}

func searchRelevance(item BookItem, keyword string) int {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	title := strings.ToLower(strings.TrimSpace(item.Name))
	author := strings.ToLower(strings.TrimSpace(item.Author))
	switch {
	case title == keyword:
		return 4
	case strings.HasPrefix(title, keyword):
		return 3
	case strings.Contains(title, keyword):
		return 2
	case strings.Contains(author, keyword):
		return 1
	default:
		return 0
	}
}

// pickSources 解析参与搜索的书源：去重、容量防御、单次全查、内存三路分流。
func (s *SearchService) pickSources(ctx context.Context, userID uuid.UUID, sourceIDs []uuid.UUID) ([]*repository.BookSource, []SearchFailure, error) {
	if len(sourceIDs) == 0 {
		list, err := s.sources.ListEnabledForSearch(ctx, s.pool, userID)
		if err != nil {
			return nil, nil, err
		}
		eligible := list[:0]
		for _, src := range list {
			rules := parseSearchRules(src.Rules)
			if strings.TrimSpace(rules.SearchURL) != "" && strings.TrimSpace(rules.BookList) != "" {
				eligible = append(eligible, src)
			}
		}
		return eligible, nil, nil
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
	if len(dedup) > searchSourceLimit {
		return nil, nil, fmt.Errorf("%w: at most %d source_ids", repository.ErrInvalidArgument, searchSourceLimit)
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
	start := time.Now()
	// 单源硬超时：慢源不得拖垮整体搜索预算（部分失败隔离）。
	ctx, cancel := context.WithTimeout(ctx, searchSourceTimeout)
	defer cancel()
	ctx = sourceFetchContext(ctx, src)
	rules := parseSearchRules(src.Rules)
	if rules.SearchURL == "" || rules.BookList == "" {
		err := fmt.Errorf("%w: source has no search rule", sandbox.ErrUnsupportedRule)
		s.health.RecordFailure(src.UserID, src.ID, err)
		return nil, err
	}

	request, err := s.buildSearchRequest(ctx, src.BookSourceURL, rules.SearchURL, keyword, page)
	if err != nil {
		s.health.RecordFailure(src.UserID, src.ID, err)
		return nil, err
	}
	target := resolveURL(src.BookSourceURL, request.url)
	headers := scopeHeadersForTarget(src.BookSourceURL, target, mergeHeaders(parseSourceHeaders(src.Header), request.header))
	ctx = fetcher.WithResponseCharset(ctx, request.charset)
	var resp *fetcher.Response
	if request.method == http.MethodGet {
		resp, err = s.ft.Get(ctx, target, headers)
	} else if full, ok := s.ft.(interface {
		Fetch(context.Context, string, string, http.Header, io.Reader) (*fetcher.Response, error)
	}); ok {
		resp, err = full.Fetch(ctx, request.method, target, headers, strings.NewReader(request.body))
	} else {
		err = requestOptionError()
	}
	if err != nil {
		s.health.RecordFailure(src.UserID, src.ID, err)
		return nil, err
	}
	if resp == nil {
		return nil, fetcher.ErrUpstreamError
	}
	if resp.StatusCode >= 400 {
		// 4xx/5xx 拦截：绝不把错误页送入 bookList 解析。
		err = fmt.Errorf("%w: upstream status %d", &fetcher.HTTPStatusError{StatusCode: resp.StatusCode}, resp.StatusCode)
		s.health.RecordFailure(src.UserID, src.ID, err)
		return nil, err
	}

	base := resp.FinalURL
	if base == "" {
		base = target
	}
	body := string(resp.Body)
	vars := map[string]any{"key": keyword, "page": page}
	items, err := s.parseBookList(ctx, body, base, rules, vars)
	if len(items) == 0 && ctx.Err() == nil {
		if item, ok := s.searchDetail(ctx, src.Rules, body, base, keyword, page, target != base); ok {
			items, err = []BookItem{item}, nil
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		s.health.RecordFailure(src.UserID, src.ID, err)
		return nil, err
	}
	s.health.RecordSuccess(src.UserID, src.ID, time.Since(start))
	return items, nil
}

func (s *SearchService) searchDetail(ctx context.Context, raw json.RawMessage, body, base, keyword string, page int, redirected bool) (BookItem, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || ValidateSourceURL(base) != nil {
		return BookItem{}, false
	}
	rule := func(field string) string {
		return firstNonEmpty(nestedText(fields, "bookInfo", field), nestedText(fields, "ruleBookInfo", field))
	}
	engine := parser.New(s.sb)
	engine.SetVariable("baseUrl", base)
	engine.SetVariable("key", keyword)
	engine.SetVariableAny("page", page)
	eval := func(field string) string {
		expression := rule(field)
		if expression == "" {
			return ""
		}
		value, e := engine.Eval(ctx, expression, body)
		if e != nil {
			return ""
		}
		return strings.TrimSpace(value)
	}
	name := eval("name")
	if name == "" || (!redirected && !strings.Contains(strings.ToLower(name), strings.ToLower(keyword))) {
		return BookItem{}, false
	}
	cover := normalizeCoverURL(eval("coverUrl"), base)
	if cover == nil {
		cover = extractBookCover([]byte(body), base)
	}
	return BookItem{Name: name, Author: eval("author"), BookURL: base, CoverURL: derefString(cover), Intro: eval("intro")}, true
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
func (s *SearchService) parseBookList(ctx context.Context, content, baseURL string, rules searchRules, globals ...map[string]any) ([]BookItem, error) {
	if strings.TrimSpace(rules.BookList) == "" {
		return nil, fmt.Errorf("%w: empty bookList", sandbox.ErrUnsupportedRule)
	}
	engine := parser.New(s.sb)
	setGlobals := func() {
		engine.SetVariable("baseUrl", baseURL)
		if len(globals) > 0 {
			for key, value := range globals[0] {
				engine.SetVariableAny(key, value)
			}
		}
	}
	setGlobals()
	nodes, err := engine.EvalListNodes(ctx, rules.BookList, content)
	if err != nil {
		return nil, err
	}
	items := make([]BookItem, 0, len(nodes))
	for _, node := range nodes {
		// 每个 item 开始时重置一次变量池：item 内字段共享，跨 item 隔离。
		engine.ResetVars()
		setGlobals()
		name := strings.TrimSpace(evalBookField(ctx, engine, node, rules.Name))
		author := evalBookField(ctx, engine, node, rules.Author)
		bookURL := resolveURL(baseURL, evalBookField(ctx, engine, node, rules.BookURL))
		// 与加书契约一致：错误对象、空字段和非 HTTP(S) 地址不是书籍。
		if name == "" || validateHTTPURL(bookURL) != nil {
			continue
		}
		items = append(items, BookItem{
			Name:          name,
			Author:        author,
			BookURL:       bookURL,
			CoverURL:      derefString(normalizeCoverURL(evalBookField(ctx, engine, node, rules.CoverURL), baseURL)),
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

func sourceFetchContext(ctx context.Context, src *repository.BookSource) context.Context {
	return fetcher.WithSource(ctx, src.ID.String(), derefString(src.ConcurrentRate))
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
