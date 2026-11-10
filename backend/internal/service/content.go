package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	maxContentPages    = 5
	contentBudget      = 10 * time.Second
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
		log:       log,
	}
}

// Get 读取章节正文。chapterIndex 与 chapterID 必须且只能提供一个（由 handler 校验）。
func (s *ContentService) Get(ctx context.Context, userID, bookID uuid.UUID, chapterIndex *int, chapterID *int64) (*ChapterContentView, error) {
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, err
	}
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
	if payload, ok := s.loadCache(ctx, key, targetSourceID); ok {
		return s.assemble(ctx, userID, bookID, activeSourceID, targetSourceID, chapter, payload.Content, payload.WordCount,
			payload.AppliedReplaceRuleIDs, payload.TraceID, isOverridden, true)
	}

	content, traceID, appliedIDs, err := s.fetchAndClean(ctx, userID, src, targetURL)
	if err != nil {
		return nil, err
	}
	wordCount := utf8.RuneCountInString(content)
	s.storeCache(ctx, key, cachedPayload{
		Content: content, WordCount: wordCount, AppliedReplaceRuleIDs: appliedIDs,
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
	if _, ok := s.loadCache(ctx, key, src.ID); ok {
		return nil
	}
	content, traceID, appliedIDs, err := s.fetchAndClean(ctx, userID, src, ch.URL)
	if err != nil {
		return err
	}
	s.storeCache(ctx, key, cachedPayload{
		Content:               content,
		WordCount:             utf8.RuneCountInString(content),
		AppliedReplaceRuleIDs: appliedIDs,
		SourceID:              src.ID,
		TraceID:               traceID,
	})
	return nil
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
func (s *ContentService) fetchAndClean(ctx context.Context, userID uuid.UUID, src *repository.BookSource, startURL string) (string, string, []uuid.UUID, error) {
	rules := parseContentRules(src.Rules)
	traceID := "tr_" + uuid.NewString()

	content, err := s.fetchContent(ctx, rules, startURL)
	if err != nil {
		return "", "", nil, err
	}

	if rules.ReplaceRegex != "" {
		content = applySourceReplaceRegex(content, rules.ReplaceRegex)
	}

	appliedIDs := make([]uuid.UUID, 0, 8)
	content, appliedIDs = s.applyUserRules(ctx, userID, content)

	content = normalizeContent(content)
	if strings.TrimSpace(content) == "" {
		return "", "", nil, fmt.Errorf("%w: empty content after clean", sandbox.ErrRuleExecution)
	}
	return content, traceID, appliedIDs, nil
}

func (s *ContentService) fetchContent(ctx context.Context, rules contentRules, startURL string) (string, error) {
	if strings.TrimSpace(rules.Content) == "" {
		return "", fmt.Errorf("%w: content rule is empty", sandbox.ErrRuleExecution)
	}
	ctx, cancel := context.WithTimeout(ctx, contentBudget)
	defer cancel()

	engine := parser.New(s.sb)
	visited := make(map[string]bool, maxContentPages)
	parts := make([]string, 0, maxContentPages)
	pageURL := strings.TrimSpace(startURL)

	for pages := 0; pageURL != "" && pages < maxContentPages; pages++ {
		if visited[pageURL] {
			break
		}
		visited[pageURL] = true

		resp, err := s.ft.Get(ctx, pageURL, nil)
		if err != nil {
			if pages == 0 {
				return "", err
			}
			break
		}
		if resp.StatusCode >= 400 {
			upstreamErr := fmt.Errorf("%w: upstream status %d", fetcher.ErrUpstreamError, resp.StatusCode)
			if pages == 0 {
				return "", upstreamErr
			}
			break
		}
		body := string(resp.Body)
		node, err := parser.NewHTMLDocumentNode(body)
		if err != nil {
			if pages == 0 {
				return "", fmt.Errorf("%w: parse content page: %v", sandbox.ErrRuleExecution, err)
			}
			break
		}
		text, err := engine.EvalField(ctx, node, rules.Content)
		if err != nil {
			if pages == 0 {
				return "", err
			}
			break
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
		if strings.TrimSpace(rules.NextContentURL) == "" {
			break
		}
		base := strings.TrimSpace(resp.FinalURL)
		if base == "" {
			base = pageURL
		}
		next, err := engine.EvalNextURL(ctx, rules.NextContentURL, body)
		if err != nil {
			break
		}
		next = strings.TrimSpace(resolveURL(base, next))
		if next == "" || next == pageURL || visited[next] {
			break
		}
		pageURL = next
	}
	return strings.Join(parts, "\n"), nil
}

// applyUserRules 依次应用用户级 replace_rules（RE2），非法正则跳过不阻塞阅读。
func (s *ContentService) applyUserRules(ctx context.Context, userID uuid.UUID, content string) (string, []uuid.UUID) {
	rules, err := s.rules.ListEnabledForContent(ctx, s.pool, userID)
	if err != nil {
		s.log.Warn("load replace rules failed", "err", err)
		return content, []uuid.UUID{}
	}
	applied := make([]uuid.UUID, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		if !re.MatchString(content) {
			continue
		}
		content = re.ReplaceAllString(content, r.Replacement)
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
	re, err := regexp.Compile(pattern)
	if err != nil {
		return content
	}
	return re.ReplaceAllString(content, replacement)
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
		out.Content = rawText(obj, "ruleContent")
	}
	if out.NextContentURL == "" {
		out.NextContentURL = firstNonEmpty(rawText(obj, "ruleContentNextContentUrl"), nestedText(obj, "ruleContent", "nextContentUrl"))
	}
	if out.ReplaceRegex == "" {
		out.ReplaceRegex = firstNonEmpty(rawText(obj, "ruleContentReplaceRegex"), nestedText(obj, "ruleContent", "replaceRegex"))
	}
	return out
}
