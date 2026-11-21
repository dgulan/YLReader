package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/pkg/textnorm"
	"yreader/backend/internal/repository"
)

// CreateBookInput 是 POST /books 的领域入参。
type CreateBookInput struct {
	Name     string
	Author   string
	SourceID uuid.UUID
	BookURL  string
	CoverURL *string
}

func sanitizeCoverURL(raw *string) *string {
	if raw == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" || len(trimmed) > 2048 {
		return nil
	}
	if err := validateHTTPURL(trimmed); err != nil {
		return nil
	}
	return &trimmed
}

// BookService 编排书架读写：v_bookshelf 分页、CRUD 与多源合并。
type BookService struct {
	pool     *pgxpool.Pool
	books    *repository.BookRepository
	mappings *repository.MappingRepository
	shelf    *repository.BookshelfRepository
	progress *repository.ProgressRepository
	sources  *repository.SourceRepository
	storage  storage.Provider
	log      *slog.Logger
	ft       Fetcher
	sb       *sandbox.Sandbox
}

// NewBookService 构造书架服务；storage 为 nil 时删除本地书不会清理文件。
func NewBookService(pool *pgxpool.Pool, st storage.Provider, log *slog.Logger) *BookService {
	if log == nil {
		log = slog.Default()
	}
	return &BookService{
		pool:     pool,
		books:    repository.NewBookRepository(),
		mappings: repository.NewMappingRepository(),
		shelf:    repository.NewBookshelfRepository(),
		progress: repository.NewProgressRepository(),
		sources:  repository.NewSourceRepository(),
		storage:  st,
		log:      log,
	}
}

// WithFetcher 注入 SSRF 安全的抓取器与规则沙箱，用于加入书架时补全详情封面。
// 保持与 NewSearchService/NewDebugService 相同的依赖形态；未注入时自动补全静默跳过。
func (s *BookService) WithFetcher(ft Fetcher, sb *sandbox.Sandbox) *BookService {
	s.ft = ft
	s.sb = sb
	return s
}

// ShelfList 单查询 v_bookshelf 分页（返回当前页 + total）。
func (s *BookService) ShelfList(ctx context.Context, userID uuid.UUID, f repository.BookshelfFilter) ([]*repository.BookshelfEntry, int, error) {
	return s.shelf.ListPage(ctx, s.pool, userID, f)
}

// Get 读取书籍详情并附带进度（无进度记录时 Progress 为 0 值）。
func (s *BookService) Get(ctx context.Context, userID, bookID uuid.UUID) (*repository.Book, *repository.ReadingProgress, error) {
	b, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, nil, err
	}
	p, err := s.progress.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, nil, err
	}
	return b, p, nil
}

// Create 加入书架：先查后写（严禁依赖 23505 后在同一事务内挽救，PostgreSQL 会把事务置为 aborted）。
//
//   - 分支 B（书不存在）：创建 books + 初始 active 映射，返回 201；
//   - 分支 A（书已存在）：只追加 pending 备用映射，绝不篡改 active_source_id，返回 200。
func (s *BookService) Create(ctx context.Context, userID uuid.UUID, in CreateBookInput) (*repository.Book, int, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, 0, fmt.Errorf("%w: name is required", repository.ErrInvalidArgument)
	}
	bookURL := strings.TrimSpace(in.BookURL)
	if err := validateHTTPURL(bookURL); err != nil {
		return nil, 0, err
	}
	src, err := s.sources.Get(ctx, s.pool, userID, in.SourceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, 0, fmt.Errorf("%w: source not found", repository.ErrInvalidArgument)
		}
		return nil, 0, err
	}
	if !src.Enabled {
		return nil, 0, fmt.Errorf("%w: source disabled", repository.ErrInvalidArgument)
	}

	// 入参未带封面时回源到书源详情页抓取真实封面；抓取失败不阻断加书，
	// 前端会回退显示文字书封。抓取放在事务之前，避免网络请求长时间占用连接。
	cover := sanitizeCoverURL(in.CoverURL)
	if cover == nil {
		cover = s.fetchBookCover(ctx, src, bookURL)
	}

	author := strings.TrimSpace(in.Author)
	normName := textnorm.NormalizeBook(name)
	normAuthor := textnorm.NormalizeAuthor(author)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := s.books.FindByNormalized(ctx, tx, userID, normName, normAuthor)
	switch {
	case err == nil:
		// 已存在的书若一直没有封面，用本次抓到的详情封面补全（不覆盖自定义封面）。
		if existing.CoverURL == nil && cover != nil {
			if uerr := s.books.UpdateCover(ctx, tx, userID, existing.ID, *cover); uerr != nil {
				return nil, 0, uerr
			}
			existing.CoverURL = cover
		}
		m := &repository.BookSourceMapping{
			BookID:     existing.ID,
			SourceID:   in.SourceID,
			BookURL:    bookURL,
			SyncStatus: repository.MappingStatusPending,
		}
		if uerr := s.mappings.UpsertStandby(ctx, tx, userID, m); uerr != nil {
			return nil, 0, uerr
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return nil, 0, cerr
		}
		return existing, 200, nil

	case errors.Is(err, repository.ErrNotFound):
		b := &repository.Book{
			UserID:           userID,
			BookType:         repository.BookTypeOnline,
			NormalizedName:   normName,
			NormalizedAuthor: normAuthor,
			Name:             name,
			Author:           author,
			CoverURL:         cover,
			Status:           repository.BookStatusUnknown,
			ActiveSourceID:   &in.SourceID,
			ActiveBookURL:    &bookURL,
		}
		if cerr := s.books.Create(ctx, tx, b); cerr != nil {
			return nil, 0, cerr
		}
		m := &repository.BookSourceMapping{
			BookID:     b.ID,
			SourceID:   in.SourceID,
			BookURL:    bookURL,
			SyncStatus: repository.MappingStatusActive,
		}
		if uerr := s.mappings.Upsert(ctx, tx, userID, m); uerr != nil {
			return nil, 0, uerr
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return nil, 0, cerr
		}
		return b, 201, nil

	default:
		return nil, 0, err
	}
}

// Update 乐观锁更新书籍元数据（version 由触发器维护，SQL 中绝不 SET version）。
func (s *BookService) Update(ctx context.Context, userID, bookID uuid.UUID, patch repository.BookPatch, expectedVersion int64) (*repository.Book, error) {
	if patch.Name != nil {
		value := strings.TrimSpace(*patch.Name)
		if value == "" {
			return nil, fmt.Errorf("%w: name cannot be blank", repository.ErrInvalidArgument)
		}
		patch.Name = &value
	}
	if patch.Author != nil {
		value := strings.TrimSpace(*patch.Author)
		patch.Author = &value
	}
	if patch.Status != nil {
		switch *patch.Status {
		case repository.BookStatusUnknown, repository.BookStatusSerializing, repository.BookStatusCompleted, repository.BookStatusPaused:
		default:
			return nil, fmt.Errorf("%w: invalid book status", repository.ErrInvalidArgument)
		}
	}
	return s.books.UpdateMeta(ctx, s.pool, userID, bookID, patch, expectedVersion)
}

// Delete 删除书籍（不存在返回 ErrNotFound → 404）。
func (s *BookService) Delete(ctx context.Context, userID, bookID uuid.UUID) error {
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return err
	}
	if err := s.books.Delete(ctx, s.pool, userID, bookID); err != nil {
		return err
	}
	// 本地书：DB 删除成功后清理对象存储；与业务 ctx 解耦，失败只告警。
	if book.BookType == repository.BookTypeLocal && s.storage != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if derr := s.storage.DeletePrefix(cleanupCtx, "local/"+bookID.String()+"/"); derr != nil {
			s.log.Warn("delete local book files failed", "book_id", bookID, "err", derr)
		}
	}
	return nil
}

// validateHTTPURL 校验 http(s) 绝对 URL。
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: invalid http(s) url", repository.ErrInvalidArgument)
	}
	return nil
}

// parseBookInfoCoverRule 读取书源详情页的封面规则：
// 优先结构化 bookInfo.coverUrl，其次扁平 ruleBookInfoCoverUrl 与 ruleBookInfo.coverUrl。
func parseBookInfoCoverRule(raw json.RawMessage) string {
	var obj map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	return firstNonEmpty(
		nestedText(obj, "bookInfo", "coverUrl"),
		nestedText(obj, "bookInfo", "coverURL"),
		rawText(obj, "ruleBookInfoCoverUrl"),
		rawText(obj, "ruleBookInfoCoverURL"),
		nestedText(obj, "ruleBookInfo", "coverUrl"),
		nestedText(obj, "ruleBookInfo", "coverURL"),
	)
}

// fetchBookCover 在加入书架时抓取书源详情页并解析封面规则；规则缺失或未命中时
// 回退到 og:image / 常见封面节点的通用提取。任何失败都返回 nil，由前端回退到文字书封。
func (s *BookService) fetchBookCover(ctx context.Context, src *repository.BookSource, bookURL string) *string {
	if s.ft == nil {
		return nil
	}
	detailURL := resolveURL(src.BookSourceURL, bookURL)
	if validateHTTPURL(detailURL) != nil {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fetchCtx = sourceFetchContext(fetchCtx, src)
	headers := scopeHeadersForTarget(src.BookSourceURL, detailURL, parseSourceHeaders(src.Header))
	resp, err := s.ft.Get(fetchCtx, detailURL, headers)
	if err != nil || resp == nil || resp.StatusCode >= 400 {
		return nil
	}
	base := strings.TrimSpace(resp.FinalURL)
	if base == "" {
		base = detailURL
	}
	if rule := strings.TrimSpace(parseBookInfoCoverRule(src.Rules)); rule != "" && s.sb != nil {
		engine := parser.New(s.sb)
		engine.SetVariable("baseUrl", src.BookSourceURL)
		if raw, eerr := engine.Eval(fetchCtx, rule, string(resp.Body)); eerr == nil {
			if cover := normalizeCoverURL(raw, base); cover != nil {
				return cover
			}
		}
	}
	// 书源未配置详情封面规则或规则未命中时，兜底抓 og:image / 常见封面节点。
	return normalizeCoverURL(extractCoverFromHTML(resp.Body), base)
}

var (
	// 覆盖 <meta property="og:image" content="..."> 的两种属性顺序。
	metaCoverFirst   = regexp.MustCompile(`(?is)<meta\b[^>]*\bproperty\s*=\s*["'](?:og:image(?::url)?|twitter:image)["'][^>]*\bcontent\s*=\s*["']([^"']+)["']`)
	metaContentFirst = regexp.MustCompile(`(?is)<meta\b[^>]*\bcontent\s*=\s*["']([^"']+)["'][^>]*\bproperty\s*=\s*["'](?:og:image(?::url)?|twitter:image)["']`)
	// 常见书封 <img>：class/id 含 cover/bookimg/book-img/pic，src 可在前或在后。
	imgCoverFirst   = regexp.MustCompile(`(?is)<img\b[^>]*\b(?:class|id)\s*=\s*["'][^"']*(?:cover|bookimg|book-img|pic)[^"']*["'][^>]*\bsrc\s*=\s*["']([^"']+)["']`)
	imgSrcCoverFirst = regexp.MustCompile(`(?is)<img\b[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*\b(?:class|id)\s*=\s*["'][^"']*(?:cover|bookimg|book-img|pic)[^"']*["']`)
)

// extractCoverFromHTML 从详情页 HTML 中按优先级提取封面候选，返回解码后的原始值。
func extractCoverFromHTML(body []byte) string {
	rawHTML := string(body)
	for _, re := range []*regexp.Regexp{metaCoverFirst, metaContentFirst, imgCoverFirst, imgSrcCoverFirst} {
		if match := re.FindStringSubmatch(rawHTML); len(match) > 1 {
			if value := strings.TrimSpace(match[1]); value != "" {
				return html.UnescapeString(value)
			}
		}
	}
	return ""
}

// maxCoverProxyBytes 限制封面代理返回的字节数，避免恶意图床耗尽内存。
const maxCoverProxyBytes = 8 << 20

// coverContentTypes 仅允许栅格图片；拒绝 image/svg+xml 等可在同源执行脚本的类型。
var coverContentTypes = map[string]bool{
	"image/jpeg": true, "image/jpg": true, "image/png": true, "image/gif": true,
	"image/webp": true, "image/avif": true, "image/bmp": true,
	"image/x-icon": true, "image/vnd.microsoft.icon": true,
}

// FetchCover 以服务端身份代理抓取书籍封面，供同源 <img> 加载以规避第三方图床防盗链。
// 只读取当前用户自己的书；外链必须通过 URL/私网预检，抓取走 SSRF 安全的 fetcher。
func (s *BookService) FetchCover(ctx context.Context, userID, bookID uuid.UUID) ([]byte, string, error) {
	if s.ft == nil {
		return nil, "", repository.ErrNotFound
	}
	b, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, "", err
	}
	raw := ""
	if b.CustomCoverURL != nil {
		raw = strings.TrimSpace(*b.CustomCoverURL)
	}
	if raw == "" && b.CoverURL != nil {
		raw = strings.TrimSpace(*b.CoverURL)
	}
	if raw == "" || ValidateSourceURL(raw) != nil {
		return nil, "", repository.ErrNotFound
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	headers := make(http.Header)
	origin := ""
	if b.ActiveSourceID != nil {
		if src, serr := s.sources.Get(ctx, s.pool, userID, *b.ActiveSourceID); serr == nil {
			origin = strings.TrimSpace(src.BookSourceURL)
			if parsed := parseSourceHeaders(src.Header); parsed != nil {
				headers = parsed
			}
			fetchCtx = sourceFetchContext(fetchCtx, src)
			headers = scopeHeadersForTarget(origin, raw, headers)
		}
	}
	// 图床通常校验 Referer；跨域时 scopeHeadersForTarget 会剥掉，服务端代理需要补回书源站点。
	if origin != "" && headers.Get("Referer") == "" {
		headers.Set("Referer", origin)
	}
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", "Mozilla/5.0 (compatible; YLReader/1.0)")
	}
	resp, err := s.ft.Get(fetchCtx, raw, headers)
	if err != nil || resp == nil || resp.StatusCode >= 400 || len(resp.Body) == 0 || len(resp.Body) > maxCoverProxyBytes {
		return nil, "", repository.ErrNotFound
	}
	contentType := resp.Header.Get("Content-Type")
	if parsed, _, perr := mime.ParseMediaType(contentType); perr == nil {
		contentType = strings.ToLower(parsed)
	} else if contentType == "" {
		contentType = http.DetectContentType(resp.Body)
	}
	if !coverContentTypes[contentType] {
		return nil, "", repository.ErrNotFound
	}
	return resp.Body, contentType, nil
}

// normalizeCoverURL 规范化详情页封面：过滤占位/内联值，把相对路径与协议相对
// URL（//host/x.jpg）按详情页真实地址补全为 http(s) 绝对地址。
func normalizeCoverURL(raw, base string) *string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil
	}
	switch strings.ToLower(raw) {
	case "null", "undefined", "none", "about:blank":
		return nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		return nil
	}
	if strings.HasPrefix(raw, "//") {
		if u, err := url.Parse(base); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			raw = u.Scheme + ":" + raw
		}
	}
	resolved := resolveURL(base, raw)
	if validateHTTPURL(resolved) != nil {
		return nil
	}
	return &resolved
}
