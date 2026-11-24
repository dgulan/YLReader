package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/pkg/cache"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/repository"
)

// 正文抓取预算与缓存策略。
const (
	maxContentPages    = 20
	contentBudget      = 10 * time.Second
	paginatedBudget    = 60 * time.Second
	contentCacheTTL    = 7 * 24 * time.Hour
	contentCachePrefix = "yreader:cache:content"
	contentPreviewMax  = 0 // 正文不截断；此处仅为对齐契约保留语义位
)

// ChapterContentView 是 /content 的服务层返回（Chapter 为活跃源逻辑章节）。
type ChapterContentView struct {
	BookID                uuid.UUID
	SourceID              uuid.UUID
	Chapter               *repository.Chapter
	Content               string
	Cached                bool
	WordCount             int
	AppliedReplaceRuleIDs []uuid.UUID
	TraceID               string
	IsOverridden          bool
	PrevChapterIndex      *int32
	NextChapterIndex      *int32
}

// cachedPayload 是写入 Redis 的正文缓存结构。
type cachedPayload struct {
	TargetURL             string      `json:"target_url"`
	RulesFingerprint      string      `json:"rules_fingerprint"`
	Content               string      `json:"content"`
	WordCount             int         `json:"word_count"`
	AppliedReplaceRuleIDs []uuid.UUID `json:"applied_replace_rule_ids"`
	SourceID              uuid.UUID   `json:"source_id"`
	TraceID               string      `json:"trace_id"`
}

// ContentService 编排正文读取：覆盖路由 → 缓存 → 抓取 → 净化 → 前后章。
type ContentService struct {
	pool      *pgxpool.Pool
	books     *repository.BookRepository
	chapters  *repository.ChapterRepository
	sources   *repository.SourceRepository
	overrides *repository.OverrideRepository
	rules     *repository.ReplaceRuleRepository
	ft        Fetcher
	sb        *sandbox.Sandbox
	cache     cache.Cache
	storage   storage.Provider
	health    *SourceHealthService
	log       *slog.Logger
}

// NewContentService 构造正文服务；cache 可为 nil（表示禁用缓存）。
func NewContentService(pool *pgxpool.Pool, ft Fetcher, sb *sandbox.Sandbox, c cache.Cache, st storage.Provider, log *slog.Logger) *ContentService {
	if log == nil {
		log = slog.Default()
	}
	return &ContentService{
		pool:      pool,
		books:     repository.NewBookRepository(),
		chapters:  repository.NewChapterRepository(),
		sources:   repository.NewSourceRepository(),
		overrides: repository.NewOverrideRepository(),
		rules:     repository.NewReplaceRuleRepository(),
		ft:        ft,
		sb:        sb,
		cache:     c,
		storage:   st,
		health:    NewSourceHealthService(pool, log),
		log:       log,
	}
}

// WithHealth 注入自定义书源健康度服务（测试用）。
func (s *ContentService) WithHealth(h *SourceHealthService) *ContentService {
	s.health = h
	return s
}

// Get 读取章节正文。chapterIndex 与 chapterID 必须且只能提供一个（由 handler 校验）。
func (s *ContentService) get(ctx context.Context, userID, bookID uuid.UUID, chapterIndex *int, chapterID *int64, book *repository.Book) (*ChapterContentView, error) {
	var err error
	if book.ActiveSourceID == nil {
		return nil, ErrNoMatchingSource
	}
	activeSourceID := *book.ActiveSourceID

	// 逻辑章节元数据永远取自活跃源，保证目录高亮/标题/翻页位点一致。
	var chapter *repository.Chapter
	switch {
	case chapterID != nil:
		chapter, err = s.chapters.GetByID(ctx, s.pool, userID, bookID, *chapterID, activeSourceID)
	default:
		chapter, err = s.chapters.GetByIndex(ctx, s.pool, userID, bookID, activeSourceID, int32(*chapterIndex))
	}
	if err != nil {
		return nil, err
	}
	if chapter.IsVolume || chapter.ChapterIndex == nil {
		return nil, fmt.Errorf("%w: volume rows have no content", repository.ErrInvalidArgument)
	}
	curIndex := *chapter.ChapterIndex

	// 本地书：直接读对象存储，严禁走 fetcher / parser / Redis（虚拟源没有解析规则）。
	if book.BookType == repository.BookTypeLocal {
		return s.getLocalContent(ctx, userID, book, chapter)
	}

	// 单章覆盖路由：命中 active 覆盖则正文改走覆盖源，但 chapter 仍是活跃源元数据。
	targetSourceID := activeSourceID
	targetURL := chapter.URL
	isOverridden := false
	if o, oerr := s.overrides.Get(ctx, s.pool, userID, bookID, curIndex); oerr == nil && o.Status == repository.OverrideStatusActive {
		targetSourceID = o.SourceID
		targetURL = o.ChapterURL
		isOverridden = true
	}

	src, err := s.sources.Get(ctx, s.pool, userID, targetSourceID)
	if err != nil || !src.Enabled {
		return nil, fmt.Errorf("%w: content source unavailable", repository.ErrInvalidArgument)
	}

	key := fmt.Sprintf("%s:%s:%s:%d", contentCachePrefix, bookID, targetSourceID, curIndex)
	userRules, err := s.rules.ListEnabledForContent(ctx, s.pool, userID)
	if err != nil {
		return nil, err
	}
	fingerprint := contentRulesFingerprint(src.Rules, userRules)
	if payload, ok := s.loadCache(ctx, key, targetSourceID); ok && payload.RulesFingerprint == fingerprint && payload.TargetURL == targetURL {
		return s.assemble(ctx, userID, bookID, activeSourceID, targetSourceID, chapter, payload.Content, payload.WordCount,
			payload.AppliedReplaceRuleIDs, payload.TraceID, isOverridden, true)
	}

	fetchStart := time.Now()
	content, traceID, appliedIDs, err := s.fetchAndClean(ctx, userID, bookID, curIndex, src, targetURL, userRules)
	if err != nil {
		s.health.RecordFailure(userID, targetSourceID, err)
		return nil, err
	}
	s.health.RecordSuccess(userID, targetSourceID, time.Since(fetchStart))
	wordCount := utf8.RuneCountInString(content)
	s.storeCache(ctx, key, cachedPayload{
		TargetURL:        targetURL,
		RulesFingerprint: fingerprint,
		Content:          content, WordCount: wordCount, AppliedReplaceRuleIDs: appliedIDs,
		SourceID: targetSourceID, TraceID: traceID,
	})
	return s.assemble(ctx, userID, bookID, activeSourceID, targetSourceID, chapter, content, wordCount, appliedIDs, traceID, isOverridden, false)
}

// WarmChapterForSource 供 Worker 预热任务使用：复用已预取的 src 与 ch，
// 消灭 O(N) DB 往返。缓存命中直接返回 nil；未命中抓取成功后写缓存并返回 nil。
func (s *ContentService) WarmChapterForSource(ctx context.Context, userID, bookID uuid.UUID,
	src *repository.BookSource, ch *repository.Chapter) error {
	if ch == nil || ch.ChapterIndex == nil {
		return nil // 卷名等无序号章节不预热
	}
	if ch.URL == "" {
		return fmt.Errorf("%w: chapter %d has empty url", repository.ErrInvalidArgument, *ch.ChapterIndex)
	}
	key := fmt.Sprintf("%s:%s:%s:%d", contentCachePrefix, bookID, src.ID, *ch.ChapterIndex)
	userRules, err := s.rules.ListEnabledForContent(ctx, s.pool, userID)
	if err != nil {
		return err
	}
	fingerprint := contentRulesFingerprint(src.Rules, userRules)
	if payload, ok := s.loadCache(ctx, key, src.ID); ok && payload.RulesFingerprint == fingerprint && payload.TargetURL == ch.URL {
		return nil
	}
	fetchStart := time.Now()
	content, traceID, appliedIDs, err := s.fetchAndClean(ctx, userID, bookID, *ch.ChapterIndex, src, ch.URL, userRules)
	if err != nil {
		s.health.RecordFailure(userID, src.ID, err)
		return err
	}
	s.health.RecordSuccess(userID, src.ID, time.Since(fetchStart))
	s.storeCache(ctx, key, cachedPayload{
		TargetURL:             ch.URL,
		RulesFingerprint:      fingerprint,
		Content:               content,
		WordCount:             utf8.RuneCountInString(content),
		AppliedReplaceRuleIDs: appliedIDs,
		SourceID:              src.ID,
		TraceID:               traceID,
	})
	return nil
}

// TrialFetchForOverride 试抓单章覆盖目标正文并准备缓存写入回调（由调用方在 DB 事务提交后执行）。
func (s *ContentService) TrialFetchForOverride(ctx context.Context, userID, bookID uuid.UUID, chapterIndex int32, src *repository.BookSource, targetURL string) (string, func(context.Context), error) {
	userRules, err := s.rules.ListEnabledForContent(ctx, s.pool, userID)
	if err != nil {
		return "", nil, err
	}
	content, traceID, appliedIDs, err := s.fetchAndClean(ctx, userID, bookID, chapterIndex, src, targetURL, userRules)
	if err != nil {
		return "", nil, err
	}
	key := fmt.Sprintf("%s:%s:%s:%d", contentCachePrefix, bookID, src.ID, chapterIndex)
	payload := cachedPayload{
		TargetURL:             targetURL,
		RulesFingerprint:      contentRulesFingerprint(src.Rules, userRules),
		Content:               content,
		WordCount:             utf8.RuneCountInString(content),
		AppliedReplaceRuleIDs: appliedIDs,
		SourceID:              src.ID,
		TraceID:               traceID,
	}
	writeCache := func(cacheCtx context.Context) {
		s.storeCache(cacheCtx, key, payload)
	}
	return content, writeCache, nil
}

// getLocalContent 读取本地书章节正文：只做用户替换规则 + 基础整理，
// 不生成 trace_id（本地读不涉及规则求值），也不写缓存。
func (s *ContentService) getLocalContent(
	ctx context.Context, userID uuid.UUID, book *repository.Book, chapter *repository.Chapter,
) (*ChapterContentView, error) {
	if book.ActiveSourceID == nil {
		return nil, ErrNoMatchingSource
	}
	if s.storage == nil {
		return nil, fmt.Errorf("%w: storage provider unavailable", repository.ErrInvalidArgument)
	}
	localSourceID := *book.ActiveSourceID
	curIndex := *chapter.ChapterIndex

	rc, err := s.storage.Get(ctx, chapterStorageKey(book.ID, curIndex))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	content, appliedIDs := s.applyUserRules(ctx, userID, string(raw))
	content = normalizeContent(content)
	wordCount := utf8.RuneCountInString(content)

	prev, next, err := s.chapters.NeighborIndexes(ctx, s.pool, userID, book.ID, localSourceID, curIndex)
	if err != nil {
		return nil, err
	}
	return &ChapterContentView{
		BookID:                book.ID,
		SourceID:              localSourceID,
		Chapter:               chapter,
		Content:               content,
		Cached:                false,
		WordCount:             wordCount,
		AppliedReplaceRuleIDs: appliedIDs,
		TraceID:               "",
		IsOverridden:          false,
		PrevChapterIndex:      prev,
		NextChapterIndex:      next,
	}, nil
}

// assemble 装配响应：正文来源用 targetSourceID（可能来自单章覆盖），
// 而前后可读章节始终以 activeSourceID 为基准——二者若混用，覆盖章会让翻页位点漂移到另一源的章节编号空间。
func (s *ContentService) assemble(ctx context.Context, userID, bookID, activeSourceID, targetSourceID uuid.UUID, chapter *repository.Chapter,
	content string, wordCount int, appliedIDs []uuid.UUID, traceID string, isOverridden, cached bool) (*ChapterContentView, error) {
	prev, next, err := s.chapters.NeighborIndexes(ctx, s.pool, userID, bookID, activeSourceID, *chapter.ChapterIndex)
	if err != nil {
		return nil, err
	}
	return &ChapterContentView{
		BookID:                bookID,
		SourceID:              targetSourceID,
		Chapter:               chapter,
		Content:               content,
		Cached:                cached,
		WordCount:             wordCount,
		AppliedReplaceRuleIDs: appliedIDs,
		TraceID:               traceID,
		IsOverridden:          isOverridden,
		PrevChapterIndex:      prev,
		NextChapterIndex:      next,
	}, nil
}

func (s *ContentService) loadCache(ctx context.Context, key string, targetSourceID uuid.UUID) (cachedPayload, bool) {
	if s.cache == nil {
		return cachedPayload{}, false
	}
	raw, err := s.cache.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, cache.ErrMiss) {
			s.log.Debug("content cache get failed", "err", err)
		}
		return cachedPayload{}, false
	}
	var p cachedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return cachedPayload{}, false
	}
	// 一致性校验：payload 来源必须与本次目标源一致，否则视为异常回源重抓。
	if p.SourceID != targetSourceID {
		return cachedPayload{}, false
	}
	return p, true
}

func (s *ContentService) storeCache(ctx context.Context, key string, p cachedPayload) {
	if s.cache == nil || strings.TrimSpace(p.Content) == "" {
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := s.cache.SetEX(ctx, key, raw, contentCacheTTL); err != nil {
		s.log.Debug("content cache set failed", "err", err)
	}
}

// fetchAndClean 完成抓取、多页拼接、书源正则与用户替换规则净化、段落标准化。
func (s *ContentService) fetchAndClean(ctx context.Context, userID, bookID uuid.UUID, chapterIndex int32, src *repository.BookSource, startURL string, userRules []*repository.ReplaceRule) (string, string, []uuid.UUID, error) {
	ctx = sourceFetchContext(ctx, src)
	ctx = fetcher.WithResponseCharset(ctx, derefString(src.Charset))
	rules := parseContentRules(src.Rules)
	traceID := "tr_" + uuid.NewString()

	var nextChapterURL string
	if strings.TrimSpace(rules.NextContentURL) != "" {
		var err error
		nextChapterURL, err = s.chapters.NextReadableURL(ctx, s.pool, userID, bookID, src.ID, chapterIndex)
		if err != nil {
			return "", "", nil, err
		}
	}
	srcHeader := scopeHeadersForTarget(src.BookSourceURL, startURL, parseSourceHeaders(src.Header))
	content, err := s.fetchContentWithHeaders(ctx, rules, startURL, srcHeader, nextChapterURL)
	if err != nil {
		return "", "", nil, err
	}

	if rules.ReplaceRegex != "" {
		content = applySourceReplaceRegex(content, rules.ReplaceRegex)
	}

	appliedIDs := make([]uuid.UUID, 0, 8)
	content, appliedIDs = applyRuleList(userRules, content)

	content = normalizeContent(content)
	if strings.TrimSpace(content) == "" {
		return "", "", nil, fmt.Errorf("%w: empty content after clean", sandbox.ErrRuleExecution)
	}
	return content, traceID, appliedIDs, nil
}

func (s *ContentService) fetchContent(ctx context.Context, rules contentRules, startURL string) (string, error) {
	return s.fetchContentWithHeaders(ctx, rules, startURL, nil, "")
}

func (s *ContentService) fetchContentWithHeaders(ctx context.Context, rules contentRules, startURL string, srcHeader http.Header, nextChapterURL string) (string, error) {
	if strings.TrimSpace(rules.Content) == "" {
		return "", fmt.Errorf("%w: content rule is empty", sandbox.ErrRuleExecution)
	}
	paginated := strings.TrimSpace(rules.NextContentURL) != ""
	budget := contentBudget
	if paginated {
		budget = paginatedBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	engine := parser.New(s.sb)
	visited := make(map[string]bool, maxContentPages)
	parts := make([]string, 0, maxContentPages)
	pageURL := contentPageURL(startURL)
	prevBase := pageURL
	nextChapterURL = contentPageURL(resolveURL(pageURL, nextChapterURL))

	for pages := 0; pageURL != "" && pages < maxContentPages; pages++ {
		if visited[pageURL] {
			return "", fmt.Errorf("%w: content pagination cycle", sandbox.ErrRuleExecution)
		}
		visited[pageURL] = true

		reqHeader := scopeHeadersForTarget(startURL, pageURL, srcHeader)
		if pages > 0 && len(reqHeader) > 0 {
			reqHeader = scopeHeadersForTarget(prevBase, pageURL, reqHeader)
		}
		resp, err := s.ft.Get(ctx, pageURL, reqHeader)
		if err != nil {
			return "", err
		}
		if resp.StatusCode >= 400 {
			upstreamErr := fmt.Errorf("%w: upstream status %d", fetcher.ErrUpstreamError, resp.StatusCode)
			return "", upstreamErr
		}
		base := contentPageURL(resp.FinalURL)
		if base == "" {
			base = pageURL
		}
		if pages > 0 && base == nextChapterURL {
			break
		}
		if base != pageURL {
			if visited[base] {
				return "", fmt.Errorf("%w: content pagination cycle after redirect", sandbox.ErrRuleExecution)
			}
			visited[base] = true
		}
		body := string(resp.Body)
		node, err := parser.NewHTMLDocumentNode(body)
		if err != nil {
			return "", fmt.Errorf("%w: parse content page: %v", sandbox.ErrRuleExecution, err)
		}
		text, err := engine.EvalField(ctx, node, rules.Content)
		if err != nil {
			return "", err
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if pages > 0 && strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("%w: empty continuation page", sandbox.ErrRuleExecution)
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
		if !paginated {
			break
		}
		next, err := engine.EvalNextURL(ctx, rules.NextContentURL, body)
		if err != nil {
			return "", err
		}
		next = contentPageURL(resolveURL(base, next))
		if next == "" || next == nextChapterURL {
			break
		}
		if next == pageURL || visited[next] || pages+1 == maxContentPages {
			return "", fmt.Errorf("%w: incomplete content pagination", sandbox.ErrRuleExecution)
		}
		prevBase = base
		pageURL = next
	}
	return strings.Join(parts, "\n"), nil
}

func contentPageURL(raw string) string {
	return strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
}

// applyUserRules 依次应用用户级 replace_rules（RE2），非法正则跳过不阻塞阅读。
func (s *ContentService) applyUserRules(ctx context.Context, userID uuid.UUID, content string) (string, []uuid.UUID) {
	rules, err := s.rules.ListEnabledForContent(ctx, s.pool, userID)
	if err != nil {
		s.log.Warn("load replace rules failed", "err", err)
		return content, []uuid.UUID{}
	}
	return applyRuleList(rules, content)
}

func contentRulesFingerprint(source json.RawMessage, rules []*repository.ReplaceRule) string {
	encoded, _ := json.Marshal(struct {
		Pagination string
		Source     json.RawMessage
		Rules      []*repository.ReplaceRule
	}{"complete-pages-v2", source, rules})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

const maxReplacedContentBytes = 32 << 20

func applyRuleList(rules []*repository.ReplaceRule, content string) (string, []uuid.UUID) {
	applied := make([]uuid.UUID, 0, len(rules))
	for _, r := range rules {
		if len(r.Pattern) == 0 || len(r.Pattern) > maxReplacePatternBytes || len(r.Replacement) > maxReplaceReplacementBytes {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		next, matched, capped := replaceBounded(re, content, r.Replacement, maxReplacedContentBytes)
		if capped || !matched {
			continue
		}
		content = next
		applied = append(applied, r.ID)
	}
	return content, applied
}

// applySourceReplaceRegex 应用书源级 replaceRegex；支持 "pattern##replacement" 形式，
// 缺省 replacement 时按空串（整段删除）处理。
func applySourceReplaceRegex(content, rule string) string {
	pattern, replacement := rule, ""
	if idx := strings.Index(rule, "##"); idx >= 0 {
		pattern, replacement = rule[:idx], rule[idx+2:]
	}
	if len(pattern) == 0 || len(pattern) > maxReplacePatternBytes || len(replacement) > maxReplaceReplacementBytes {
		return content
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return content
	}
	next, matched, capped := replaceBounded(re, content, replacement, maxReplacedContentBytes)
	if capped || !matched {
		return content
	}
	return next
}

func replaceBounded(re *regexp.Regexp, src, repl string, maxBytes int) (string, bool, bool) {
	locs := re.FindAllStringIndex(src, -1)
	if len(locs) == 0 {
		return src, false, false
	}
	var b strings.Builder
	b.Grow(min(len(src), maxBytes))
	last := 0
	for _, loc := range locs {
		if b.Len()+(loc[0]-last) > maxBytes {
			return src, false, true
		}
		b.WriteString(src[last:loc[0]])
		expanded := re.ExpandString(nil, repl, src, loc)
		if b.Len()+len(expanded) > maxBytes {
			return src, false, true
		}
		b.Write(expanded)
		last = loc[1]
	}
	if b.Len()+(len(src)-last) > maxBytes {
		return src, false, true
	}
	b.WriteString(src[last:])
	return b.String(), true, false
}

var (
	contentBRTagRe = regexp.MustCompile(`(?is)<br\s*/?>`)
	contentTagRe   = regexp.MustCompile(`(?s)<[^>]{0,512}>`)
	contentBlankRe = regexp.MustCompile(`\n{3,}`)
)

// normalizeContent 统一换行、剥离残留 HTML 标签、压缩连续空行并修剪行尾空白。
func normalizeContent(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = contentBRTagRe.ReplaceAllString(s, "\n")
	s = contentTagRe.ReplaceAllString(s, "")
	s = contentBlankRe.ReplaceAllString(s, "\n\n")

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// contentRules 是书源正文规则（对齐 Legado rules.content）。
type contentRules struct {
	Content        string
	NextContentURL string
	ReplaceRegex   string
}

func parseContentRules(raw json.RawMessage) contentRules {
	var obj map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &obj) != nil {
		return contentRules{}
	}
	var out contentRules
	if nested, ok := obj["content"]; ok {
		var m map[string]json.RawMessage
		if json.Unmarshal(nested, &m) == nil {
			out.Content = rawText(m, "content")
			out.NextContentURL = rawText(m, "nextContentUrl")
			out.ReplaceRegex = rawText(m, "replaceRegex")
		}
	}
	if out.Content == "" {
		out.Content = firstNonEmpty(rawText(obj, "ruleContent"), nestedText(obj, "ruleContent", "content"))
	}
	if out.NextContentURL == "" {
		out.NextContentURL = firstNonEmpty(rawText(obj, "ruleContentNextContentUrl"), nestedText(obj, "ruleContent", "nextContentUrl"))
	}
	if out.ReplaceRegex == "" {
		out.ReplaceRegex = firstNonEmpty(rawText(obj, "ruleContentReplaceRegex"), nestedText(obj, "ruleContent", "replaceRegex"))
	}
	return out
}

// Get resolves a deferred chapter-local offset only for the exact version read
// before fetching content. A concurrent progress/source change always wins.
func (s *ContentService) Get(ctx context.Context, userID, bookID uuid.UUID, index *int, chapterID *int64) (*ChapterContentView, error) {
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, err
	}
	var p *repository.ReadingProgress
	if book.BookType != repository.BookTypeLocal {
		p, err = repository.NewProgressRepository().Get(ctx, s.pool, userID, bookID)
		if err != nil {
			s.log.Warn("deferred offset snapshot unavailable", "book_id", bookID, "error", err)
		}
	}
	view, err := s.get(ctx, userID, bookID, index, chapterID, book)
	if err != nil {
		return nil, err
	}
	if p == nil || view.IsOverridden || view.Chapter.ChapterIndex == nil || p.ChapterIndex != *view.Chapter.ChapterIndex || p.CharOffset != 0 || p.ChapterRatio <= 0 {
		return view, nil
	}
	offset := relocationOffset(p.ChapterRatio, utf8.RuneCountInString(view.Content))
	if offset == 0 {
		return view, nil
	}
	fillCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := s.fillDeferredOffset(fillCtx, userID, bookID, view.SourceID, p, offset); err != nil {
		s.log.Warn("deferred offset update skipped; content remains available", "book_id", bookID, "error", err)
	}
	return view, nil
}

func (s *ContentService) fillDeferredOffset(ctx context.Context, userID, bookID, sourceID uuid.UUID, p *repository.ReadingProgress, offset int32) error {
	progress := repository.NewProgressRepository()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.books.Lock(ctx, tx, userID, bookID); err != nil {
		return err
	}
	current, err := s.books.Get(ctx, tx, userID, bookID)
	if err != nil {
		return err
	}
	if current.ActiveSourceID == nil || *current.ActiveSourceID != sourceID {
		return nil
	}
	exists, err := progress.Exists(ctx, tx, userID, bookID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	p.CharOffset = offset
	if err = progress.Update(ctx, tx, p, p.Version); errors.Is(err, repository.ErrVersionConflict) {
		return nil
	} else if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
