package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/pkg/textnorm"
	"yreader/backend/internal/repository"
)

// 目录抓取预算（对齐 architecture.md：单次规则流程 10s 墙钟上限）。
const (
	maxTOCPages  = 20
	tocPageDelay = 100 * time.Millisecond
	tocBudget    = 10 * time.Second
)

// ChapterListView 是 /chapters 的服务层返回。
type ChapterListView struct {
	BookID      uuid.UUID
	SourceID    uuid.UUID
	Total       int
	RefreshedAt *time.Time
	Items       []*repository.TOCChapter
}

// TOCService 编排目录的抓取、差异入库与读取。
type TOCService struct {
	pool      *pgxpool.Pool
	books     *repository.BookRepository
	mappings  *repository.MappingRepository
	chapters  *repository.ChapterRepository
	overrides *repository.OverrideRepository
	sources   *repository.SourceRepository
	ft        Fetcher
	sb        *sandbox.Sandbox
	sleep     func(ctx context.Context, d time.Duration) error
	now       func() time.Time
}

// NewTOCService 构造目录服务；sleepFunc 生产用 time.After 语义，测试可注入。
func NewTOCService(pool *pgxpool.Pool, ft Fetcher, sb *sandbox.Sandbox) *TOCService {
	return &TOCService{
		pool:      pool,
		books:     repository.NewBookRepository(),
		mappings:  repository.NewMappingRepository(),
		chapters:  repository.NewChapterRepository(),
		overrides: repository.NewOverrideRepository(),
		sources:   repository.NewSourceRepository(),
		ft:        ft,
		sb:        sb,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		now: time.Now,
	}
}

// WithSleep 注入页间防抖实现（测试用）。
func (s *TOCService) WithSleep(fn func(ctx context.Context, d time.Duration) error) *TOCService {
	if fn != nil {
		s.sleep = fn
	}
	return s
}

// GetChapters 读取目录：优先本地缓存，refresh 或本地为空时回源抓取。
func (s *TOCService) GetChapters(ctx context.Context, userID, bookID uuid.UUID, sourceID *uuid.UUID, refresh bool) (*ChapterListView, error) {
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, err
	}

	src, err := s.resolveSource(ctx, userID, book, sourceID)
	if err != nil {
		return nil, err
	}

	localCount, err := s.chapters.CountReadable(ctx, s.pool, userID, bookID, src.ID)
	if err != nil {
		return nil, err
	}
	if refresh || localCount == 0 {
		if err := s.refresh(ctx, userID, book, src); err != nil {
			return nil, err
		}
	}

	items, err := s.chapters.ListWithOverrides(ctx, s.pool, userID, bookID, src.ID)
	if err != nil {
		return nil, err
	}
	total, err := s.chapters.CountReadable(ctx, s.pool, userID, bookID, src.ID)
	if err != nil {
		return nil, err
	}

	var refreshedAt *time.Time
	if m, err := s.mappings.GetBySourceID(ctx, s.pool, userID, bookID, src.ID); err == nil {
		refreshedAt = m.LastSyncedAt
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	return &ChapterListView{
		BookID:      bookID,
		SourceID:    src.ID,
		Total:       total,
		RefreshedAt: refreshedAt,
		Items:       items,
	}, nil
}

// resolveSource 决定目标源：缺省取活跃源（无活跃源 → 422 no_matching_source），
// 显式传入时校验归属与 enabled（不满足 → 400 validation_error）。
func (s *TOCService) resolveSource(ctx context.Context, userID uuid.UUID, book *repository.Book, sourceID *uuid.UUID) (*repository.BookSource, error) {
	if sourceID == nil {
		if book.ActiveSourceID == nil {
			return nil, ErrNoMatchingSource
		}
		src, err := s.sources.Get(ctx, s.pool, userID, *book.ActiveSourceID)
		if err != nil {
			return nil, err
		}
		if !src.Enabled {
			return nil, fmt.Errorf("%w: source disabled", repository.ErrInvalidArgument)
		}
		return src, nil
	}

	src, err := s.sources.Get(ctx, s.pool, userID, *sourceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("%w: source not found", repository.ErrInvalidArgument)
		}
		return nil, err
	}
	if !src.Enabled {
		return nil, fmt.Errorf("%w: source disabled", repository.ErrInvalidArgument)
	}
	return src, nil
}

// refresh 抓取目标源目录并在单事务内落库。
func (s *TOCService) refresh(ctx context.Context, userID uuid.UUID, book *repository.Book, src *repository.BookSource) error {
	bookURL, tocURL := bookURLForSource(book, src.ID, func() (*repository.BookSourceMapping, error) {
		return s.mappings.GetBySourceID(ctx, s.pool, userID, book.ID, src.ID)
	})
	if tocURL != "" {
		bookURL = tocURL
	}
	if bookURL == "" {
		return fmt.Errorf("%w: book has no url for source", repository.ErrInvalidArgument)
	}

	chapters, err := s.fetchTOC(ctx, parseTOCRules(src.Rules), bookURL)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	newTotal, err := s.chapters.ReplaceTOC(ctx, tx, userID, book.ID, src.ID, chapters)
	if err != nil {
		return err
	}
	if err := s.mappings.UpdateLastSyncedAt(ctx, tx, userID, book.ID, src.ID, s.now()); err != nil {
		return err
	}

	// 活跃源刷新：更新 books 统计与最新章节，并在目录缩水时标记越界覆盖。
	if book.ActiveSourceID != nil && *book.ActiveSourceID == src.ID {
		latestTitle, latestAt := lastReadable(chapters)
		if err := s.books.UpdateTOCMeta(ctx, tx, userID, book.ID, newTotal, latestTitle, latestAt); err != nil {
			return err
		}
		if newTotal < int(book.TotalChapters) {
			if err := s.overrides.MarkStaleFrom(ctx, tx, userID, book.ID, int32(newTotal)); err != nil {
				return err
			}
		}
	}
	// 预览模式（target != active）：只落 chapters，不动 books 元数据、不标 stale。

	return tx.Commit(ctx)
}

// bookURLForSource 取书籍在目标源下的 book_url / toc_url。
func bookURLForSource(book *repository.Book, sourceID uuid.UUID, getMapping func() (*repository.BookSourceMapping, error)) (bookURL, tocURL string) {
	if m, err := getMapping(); err == nil && m != nil {
		bookURL = m.BookURL
		if m.TOCURL != nil {
			tocURL = strings.TrimSpace(*m.TOCURL)
		}
	}
	if bookURL == "" && book.ActiveSourceID != nil && *book.ActiveSourceID == sourceID && book.ActiveBookURL != nil {
		bookURL = strings.TrimSpace(*book.ActiveBookURL)
	}
	return bookURL, tocURL
}

// fetchTOC 抓取目录（含多页翻页），返回有序章节行。
func (s *TOCService) fetchTOC(ctx context.Context, rules tocRules, startURL string) ([]repository.Chapter, error) {
	if strings.TrimSpace(rules.ChapterList) == "" {
		return nil, fmt.Errorf("%w: toc chapterList rule is empty", sandbox.ErrRuleExecution)
	}

	ctx, cancel := context.WithTimeout(ctx, tocBudget)
	defer cancel()

	engine := parser.New(s.sb)
	out := make([]repository.Chapter, 0, 512)
	visited := make(map[string]bool, maxTOCPages)

	pageURL := strings.TrimSpace(startURL)
	sortOrder := int32(0)
	chapterIndex := int32(0)
	pages := 0

	for pageURL != "" && pages < maxTOCPages {
		if visited[pageURL] {
			break
		}
		visited[pageURL] = true
		pages++

		resp, err := s.ft.Get(ctx, pageURL, nil)
		if err != nil {
			if pages == 1 {
				return nil, err
			}
			break // 容错降级：保留已抓取的前序章节
		}
		if resp.StatusCode >= 400 {
			upstreamErr := fmt.Errorf("%w: upstream status %d", fetcher.ErrUpstreamError, resp.StatusCode)
			if pages == 1 {
				return nil, upstreamErr
			}
			break
		}

		base := strings.TrimSpace(resp.FinalURL)
		if base == "" {
			base = pageURL
		}
		engine.SetVariable("baseUrl", base)
		body := string(resp.Body)

		nodes, err := engine.EvalListNodes(ctx, rules.ChapterList, body)
		if err != nil {
			if pages == 1 {
				return nil, err
			}
			break
		}
		if pages == 1 && emptyTOCHead(ctx, engine, nodes, rules) {
			return nil, fmt.Errorf("%w: no chapters extracted (rule mismatch or empty toc)", sandbox.ErrRuleExecution)
		}

		for _, node := range nodes {
			engine.ResetVars()
			title, _ := engine.EvalField(ctx, node, rules.ChapterName)
			rawURL, _ := engine.EvalField(ctx, node, rules.ChapterUrl)

			isVolume := evalFlag(ctx, engine, node, rules.IsVolume)
			chapter := repository.Chapter{
				SortOrder:       sortOrder,
				Title:           strings.TrimSpace(title),
				URL:             resolveURL(base, rawURL),
				IsVolume:        isVolume,
				IsVIP:           evalFlag(ctx, engine, node, rules.IsVIP),
				WordCount:       0,
				PublishedAt:     parseTOCTime(evalText(ctx, engine, node, rules.UpdateTime)),
				NormalizedTitle: "",
			}
			if isVolume {
				chapter.NormalizedTitle = textnorm.NormalizeChapterTitle(chapter.Title)
				chapter.URLHash = fmt.Sprintf("volume:%d", sortOrder)
				chapter.ChapterIndex = nil
			} else {
				// 单参数调用：仅从标题文本提取章节号，不含章节号时返回纯标题。
				chapter.NormalizedTitle = textnorm.NormalizeChapter(chapter.Title)
				chapter.URLHash = sha256Hex(chapter.URL)
				idx := chapterIndex
				chapter.ChapterIndex = &idx
				chapterIndex++
			}
			sortOrder++
			out = append(out, chapter)
		}

		if strings.TrimSpace(rules.NextTocURL) == "" {
			break
		}
		next, nextErr := engine.EvalNextURL(ctx, rules.NextTocURL, body)
		if nextErr != nil {
			break
		}
		next = resolveURL(base, next)
		if next == "" || next == pageURL || visited[next] {
			break
		}
		if err := s.sleep(ctx, tocPageDelay); err != nil {
			break
		}
		pageURL = next
	}

	return out, nil
}

// emptyTOCHead 判断前 10 个节点的章节名与地址是否全空（规则不匹配）。
func emptyTOCHead(ctx context.Context, engine *parser.Engine, nodes []parser.RuleNode, rules tocRules) bool {
	if len(nodes) == 0 {
		return true
	}
	limit := len(nodes)
	if limit > 10 {
		limit = 10
	}
	for i := 0; i < limit; i++ {
		engine.ResetVars()
		name, _ := engine.EvalField(ctx, nodes[i], rules.ChapterName)
		link, _ := engine.EvalField(ctx, nodes[i], rules.ChapterUrl)
		if strings.TrimSpace(name) != "" || strings.TrimSpace(link) != "" {
			return false
		}
	}
	return true
}

// evalText 求值可选规则；规则为空或求值失败返回空串。
func evalText(ctx context.Context, engine *parser.Engine, node parser.RuleNode, rule string) string {
	if strings.TrimSpace(rule) == "" {
		return ""
	}
	v, err := engine.EvalField(ctx, node, rule)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// evalFlag 求值布尔型规则：空规则默认 false；"true"/"1"/"yes" 视为真。
func evalFlag(ctx context.Context, engine *parser.Engine, node parser.RuleNode, rule string) bool {
	v := strings.ToLower(evalText(ctx, engine, node, rule))
	return v == "true" || v == "1" || v == "yes"
}

// parseTOCTime 依次尝试 RFC3339 / "2006-01-02 15:04:05" / "2006-01-02"；
// 相对时间或解析失败一律返回 nil。
func parseTOCTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	return nil
}

// lastReadable 找最后一个非卷名的可读章节。
func lastReadable(chapters []repository.Chapter) (string, *time.Time) {
	for i := len(chapters) - 1; i >= 0; i-- {
		if !chapters[i].IsVolume {
			return chapters[i].Title, chapters[i].PublishedAt
		}
	}
	return "", nil
}

// sha256Hex 返回 URL 的 sha256 十六进制摘要（url_hash）。
func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// tocRules 是 rules.toc（或扁平 ruleToc*）解析结果。
type tocRules struct {
	ChapterList string
	ChapterName string
	ChapterUrl  string
	IsVolume    string
	IsVIP       string
	UpdateTime  string
	NextTocURL  string
}

// parseTOCRules 优先读取结构化 rules.toc 对象，缺失时回退扁平 ruleToc* 与 ruleToc 嵌套。
func parseTOCRules(raw json.RawMessage) tocRules {
	var obj map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &obj) != nil {
		return tocRules{}
	}
	var out tocRules
	if nested, ok := obj["toc"]; ok {
		var m map[string]json.RawMessage
		if json.Unmarshal(nested, &m) == nil {
			out.ChapterList = rawText(m, "chapterList")
			out.ChapterName = rawText(m, "chapterName")
			out.ChapterUrl = rawText(m, "chapterUrl")
			out.IsVolume = rawText(m, "isVolume")
			out.IsVIP = rawText(m, "isVip")
			out.UpdateTime = rawText(m, "updateTime")
			out.NextTocURL = rawText(m, "nextTocUrl")
		}
	}
	if out.ChapterList == "" {
		out.ChapterList = firstNonEmpty(rawText(obj, "ruleTocChapterList"), nestedText(obj, "ruleToc", "chapterList"))
	}
	if out.ChapterName == "" {
		out.ChapterName = firstNonEmpty(rawText(obj, "ruleTocChapterName"), nestedText(obj, "ruleToc", "chapterName"))
	}
	if out.ChapterUrl == "" {
		out.ChapterUrl = firstNonEmpty(rawText(obj, "ruleTocChapterUrl"), nestedText(obj, "ruleToc", "chapterUrl"))
	}
	if out.IsVolume == "" {
		out.IsVolume = firstNonEmpty(rawText(obj, "ruleTocIsVolume"), nestedText(obj, "ruleToc", "isVolume"))
	}
	if out.IsVIP == "" {
		out.IsVIP = firstNonEmpty(rawText(obj, "ruleTocIsVip"), nestedText(obj, "ruleToc", "isVip"))
	}
	if out.UpdateTime == "" {
		out.UpdateTime = firstNonEmpty(rawText(obj, "ruleTocUpdateTime"), nestedText(obj, "ruleToc", "updateTime"))
	}
	if out.NextTocURL == "" {
		out.NextTocURL = firstNonEmpty(rawText(obj, "ruleTocNextTocUrl"), nestedText(obj, "ruleToc", "nextTocUrl"))
	}
	return out
}
