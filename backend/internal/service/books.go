package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/html"

	"yreader/backend/internal/engine/fetcher"
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
	cover := normalizeCoverURL(derefString(in.CoverURL), bookURL)
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
	if rule := strings.TrimSpace(parseBookInfoCoverRule(src.Rules)); rule != "" {
		engine := parser.New(s.sb)
		engine.SetVariable("baseUrl", base)
		if raw, eerr := engine.Eval(fetchCtx, rule, string(resp.Body)); eerr == nil {
			for _, candidate := range strings.Split(raw, "\n") {
				if cover := normalizeCoverURL(candidate, base); cover != nil {
					return cover
				}
			}
		}
	}
	return extractBookCover(resp.Body, base)
}

func extractBookCover(body []byte, base string) *string {
	tokenizer := html.NewTokenizer(strings.NewReader(string(body)))
	var marked, first *string
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		tag := tokenizer.Token()
		attrs := make(map[string]string, len(tag.Attr))
		for _, attr := range tag.Attr {
			attrs[attr.Key] = attr.Val
		}
		switch tag.Data {
		case "meta":
			switch strings.ToLower(firstNonEmpty(attrs["property"], attrs["name"])) {
			case "og:image", "og:image:url", "og:image:secure_url", "twitter:image":
				if cover := normalizeCoverURL(attrs["content"], base); cover != nil {
					return cover
				}
			}
		case "img":
			if marked != nil {
				continue
			}
			var cover *string
			for _, k := range []string{"data-src", "data-original", "data-lazy-src", "src"} {
				cover = normalizeCoverURL(attrs[k], base)
				if cover != nil {
					break
				}
			}
			if cover == nil {
				continue
			}
			if first == nil {
				first = cover
			}
			label := strings.ToLower(attrs["class"] + " " + attrs["id"])
			if strings.Contains(label, "cover") || strings.Contains(label, "bookimg") || strings.Contains(label, "book-img") {
				marked = cover
			}
		}
	}
	if marked != nil {
		return marked
	}
	return first
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
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	origin := ""
	if b.ActiveSourceID != nil {
		if src, serr := s.sources.Get(ctx, s.pool, userID, *b.ActiveSourceID); serr == nil {
			origin = src.BookSourceURL
			fetchCtx = sourceFetchContext(fetchCtx, src)
		}
	}
	base := origin
	if b.ActiveBookURL != nil && validateHTTPURL(*b.ActiveBookURL) == nil {
		base = *b.ActiveBookURL
	}
	cover := normalizeCoverURL(derefString(b.CustomCoverURL), base)
	if cover == nil {
		cover = normalizeCoverURL(derefString(b.CoverURL), base)
	}
	if cover == nil || ValidateSourceURL(*cover) != nil {
		return nil, "", repository.ErrNotFound
	}
	headers := http.Header{
		"User-Agent": {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36"},
		"Accept":     {"image/*"},
	}
	for _, candidate := range []string{origin, base, *cover} {
		if ValidateSourceURL(candidate) == nil {
			referer, _ := url.Parse(candidate)
			headers.Set("Referer", (&url.URL{Scheme: referer.Scheme, Host: referer.Host, Path: "/"}).String())
			break
		}
	}
	var resp *fetcher.Response
	if images, ok := s.ft.(interface {
		GetImage(context.Context, string, http.Header, int64) (*fetcher.Response, error)
	}); ok {
		resp, err = images.GetImage(fetchCtx, *cover, headers, maxCoverProxyBytes)
	} else {
		resp, err = s.ft.Get(fetchCtx, *cover, headers)
	}
	if err != nil || resp == nil || resp.StatusCode >= 400 || len(resp.Body) == 0 || len(resp.Body) > maxCoverProxyBytes {
		return nil, "", repository.ErrNotFound
	}
	contentType := http.DetectContentType(resp.Body)
	if contentType == "application/octet-stream" && len(resp.Body) >= 16 &&
		string(resp.Body[4:8]) == "ftyp" && (string(resp.Body[8:12]) == "avif" || string(resp.Body[8:12]) == "avis") {
		contentType = "image/avif"
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
	if len(resolved) > 2048 || ValidateSourceURL(resolved) != nil {
		return nil
	}
	u, _ := url.Parse(resolved)
	path := strings.ToLower(u.Path)
	filename := path[strings.LastIndex(path, "/")+1:]
	switch filename {
	case "nocover.jpg", "no-cover.jpg", "nocover.png", "no-cover.png", "noimage.jpg", "no-image.png", "cover-placeholder.png", "placeholder.gif", "spacer.gif", "blank.gif":
		return nil
	}
	return &resolved
}
