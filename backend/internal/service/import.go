package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/pkg/textnorm"
	"yreader/backend/internal/repository"
)

// ErrDRMProtected 表示 EPUB 正文被 DRM 加密，无法解析。
var ErrDRMProtected = errors.New("drm protected content")

// ErrCommitFailed 表示事务在 Commit 阶段失败（可能处于 In-doubt 状态）。
var ErrCommitFailed = errors.New("tx commit failed")

// maxImportCoverBytes 封面大小上限（超过则忽略封面）。
const maxImportCoverBytes = 8 << 20

// maxNameAuthorAttempts 同名同作者冲突时的自动重命名重试上限（并发导入用）。
// v1.3（M3c）：由 5 提升至 20，充分保证 10~15 路高并发导入同名书顺利收敛。
const maxNameAuthorAttempts = 20

// parsedChapter 是解析器产出的章节（未落库）。
type parsedChapter struct {
	Title       string
	IsVolume    bool
	IsVIP       bool
	PublishedAt *time.Time
	Content     string
}

type parsedBook struct {
	Title    string
	Author   string
	Cover    []byte
	CoverExt string
	Chapters []parsedChapter
}

// ImportBookResult 是导入结果（Handler 映射为 BookImportResult）。
type ImportBookResult struct {
	Book         *repository.Book
	ChapterCount int
	FileType     string
	FileHash     string
	Warnings     []string
	Created      bool
}

// ImportService 负责本地 EPUB / TXT 的解析、落库与正文落盘。
type ImportService struct {
	pool        *pgxpool.Pool
	books       *repository.BookRepository
	chapters    *repository.ChapterRepository
	mappings    *repository.MappingRepository
	localSource *LocalSourceService
	storage     storage.Provider
	log         *slog.Logger
}

// NewImportService 构造导入服务。
func NewImportService(pool *pgxpool.Pool, st storage.Provider, log *slog.Logger) *ImportService {
	return &ImportService{
		pool:        pool,
		books:       repository.NewBookRepository(),
		chapters:    repository.NewChapterRepository(),
		mappings:    repository.NewMappingRepository(),
		localSource: NewLocalSourceService(pool),
		storage:     st,
		log:         log,
	}
}

// localBookURL 是本地书的占位书址，与 active_source_id 成对出现。
func localBookURL(bookID uuid.UUID) string {
	return "local:" + bookID.String()
}

// chapterStorageKey 返回正文章节在对象存储中的键。
func chapterStorageKey(bookID uuid.UUID, chapterIndex int32) string {
	return fmt.Sprintf("local/%s/%d.txt", bookID.String(), chapterIndex)
}

// ImportBook 导入一个本地文件。file 需定位在起点，size 为字节数。
func (s *ImportService) ImportBook(
	ctx context.Context, userID uuid.UUID, file *os.File, size int64,
	fileHash, filename, fileType string, title, author *string,
) (*ImportBookResult, error) {
	src, err := s.localSource.EnsureLocalSource(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 三态预检查：命中 = 已导入；ErrNotFound = 新书；其它错误熔断。
	existing, err := s.books.GetByFileHash(ctx, s.pool, userID, fileHash)
	switch {
	case err == nil:
		return &ImportBookResult{
			Book:         existing,
			ChapterCount: int(existing.TotalChapters),
			FileType:     fileType,
			FileHash:     fileHash,
			Created:      false,
		}, nil
	case !errors.Is(err, repository.ErrNotFound):
		return nil, fmt.Errorf("check file hash: %w", err)
	}

	fallbackTitle := strings.TrimSpace(strings.TrimSuffix(filename, path_ext(filename)))
	pBook, warnings, err := s.parseByType(file, size, fileType, fallbackTitle)
	if err != nil {
		return nil, err
	}
	if pBook == nil {
		return nil, fmt.Errorf("%w: parser returned no book", repository.ErrInvalidArgument)
	}

	name := strings.TrimSpace(pBook.Title)
	if title != nil && strings.TrimSpace(*title) != "" {
		name = strings.TrimSpace(*title)
	}
	if name == "" {
		name = fallbackTitle
	}
	if name == "" {
		name = "未命名书籍"
	}
	authorName := strings.TrimSpace(pBook.Author)
	if author != nil && strings.TrimSpace(*author) != "" {
		authorName = strings.TrimSpace(*author)
	}

	// 锁定原始名：预检查与重试循环都以它为扫描基准，避免嵌套 "(1) (1)"。
	originalName := name
	explicitTitle := title != nil && strings.TrimSpace(*title) != ""
	normName, normAuthor := textnorm.NormalizeBook(name), textnorm.NormalizeAuthor(authorName)
	if _, ferr := s.books.FindByNormalized(ctx, s.pool, userID, normName, normAuthor); ferr == nil {
		if explicitTitle {
			return nil, fmt.Errorf("%w: book with same name and author already exists", repository.ErrDuplicate)
		}
		name, normName, err = s.nextAvailableName(ctx, userID, originalName, authorName)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(ferr, repository.ErrNotFound) {
		return nil, fmt.Errorf("find book: %w", ferr)
	}

	// 预分配 ID：正文落盘路径与 books.id 保持一致。
	bookID := uuid.New()
	book := &repository.Book{
		ID:               bookID,
		UserID:           userID,
		BookType:         repository.BookTypeLocal,
		NormalizedName:   normName,
		NormalizedAuthor: normAuthor,
		Name:             name,
		Author:           authorName,
		Status:           repository.BookStatusUnknown,
		ActiveSourceID:   &src.ID,
		ActiveBookURL:    ptrStr(localBookURL(bookID)),
		FileHash:         &fileHash,
	}

	toc, readableCount, latestTitle := buildLocalTOC(bookID, src.ID, pBook.Chapters)
	if readableCount == 0 {
		return nil, fmt.Errorf("%w: no readable chapters", repository.ErrInvalidArgument)
	}

	// 正文先落盘（并发 8），失败即整体清理。
	if err := s.writeChapterFiles(ctx, bookID, pBook.Chapters); err != nil {
		s.cleanupBook(bookID)
		return nil, err
	}
	if len(pBook.Cover) > 0 && len(pBook.Cover) <= maxImportCoverBytes {
		ext := strings.ToLower(pBook.CoverExt)
		if ext == "" {
			ext = "jpg"
		}
		if perr := s.storage.Put(ctx, fmt.Sprintf("local/%s/cover.%s", bookID.String(), ext),
			strings.NewReader(string(pBook.Cover)), int64(len(pBook.Cover))); perr != nil {
			s.warn("write cover failed", "book_id", bookID, "err", perr)
		}
	}

	// 并发同名不同文件：先到者占用 (user_id, normalized_name, normalized_author)，
	// 后到者按「自动重命名」有限次重试（见 M3b-1 并发同名用例）。
	// 正文文件已按预分配 bookID 落盘，重试复用同一 ID，只有最终失败才清理。
	baseName := originalName
	committed := false
	for attempt := 0; attempt < maxNameAuthorAttempts; attempt++ {
		// 非显式标题的 name_author 冲突：重新取名再试（每次从原始标题扫描）。
		if attempt > 0 && !explicitTitle {
			newName, newNorm, nerr := s.nextAvailableName(ctx, userID, baseName, authorName)
			if nerr != nil {
				break
			}
			book.Name, book.NormalizedName = newName, newNorm
		}

		err := s.importTx(ctx, userID, src, book, toc, readableCount, latestTitle)
		if err == nil {
			committed = true
			break
		}

		// ① Commit 错误优先：In-doubt 时由三态决策判定能否清理，绝不先删文件。
		if errors.Is(err, ErrCommitFailed) {
			s.handleCommitError(ctx, userID, bookID, err)
			return nil, err
		}

		// 显式标题冲突 → 直接 409，不进重试。
		if explicitTitle && errors.Is(err, repository.ErrDuplicate) {
			s.cleanupBook(bookID)
			return nil, fmt.Errorf("%w: book with same name and author already exists", repository.ErrDuplicate)
		}

		// ② 非显式标题的 name_author 冲突 → 重试。
		var pgErr *pgconn.PgError
		if errors.Is(err, repository.ErrDuplicate) && errors.As(err, &pgErr) &&
			pgErr.Code == "23505" && pgErr.ConstraintName == "uq_books_user_name_author" {
			continue
		}

		// ③ 同一文件并发双击 → 幂等命中（200）。
		if errors.Is(err, repository.ErrDuplicate) && errors.As(err, &pgErr) &&
			pgErr.Code == "23505" && pgErr.ConstraintName == "uq_books_user_file_hash" {
			existing, _ := s.waitFileHash(ctx, userID, fileHash, 40)
			if existing != nil {
				s.cleanupBook(bookID)
				return &ImportBookResult{
					Book:         existing,
					ChapterCount: int(existing.TotalChapters),
					FileType:     fileType,
					FileHash:     fileHash,
					Created:      false,
				}, nil
			}
			s.cleanupBook(bookID)
			return nil, err
		}

		// ④ 其他错误（事务异常等）：清理后原样返回。
		s.cleanupBook(bookID)
		return nil, err
	}

	if !committed {
		s.cleanupBook(bookID)
		return nil, fmt.Errorf("%w: too many books with the same name; please specify a title", repository.ErrDuplicate)
	}

	// 内存回填，避免再次查询。
	book.TotalChapters = int32(readableCount)
	book.LatestChapterTitle = ptrStr(latestTitle)

	return &ImportBookResult{
		Book:         book,
		ChapterCount: readableCount,
		FileType:     fileType,
		FileHash:     fileHash,
		Warnings:     warnings,
		Created:      true,
	}, nil
}

// importTx 在单事务内完成 Create → ReplaceTOC → mapping.Upsert → UpdateTOCMeta → Commit。
// 失败时不清理文件，由调用方 ImportBook 在重试耗尽后统一 cleanupBook。
func (s *ImportService) importTx(
	ctx context.Context, userID uuid.UUID, src *repository.BookSource,
	book *repository.Book, toc []repository.Chapter,
	readableCount int, latestTitle string,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := s.books.Create(ctx, tx, book); err != nil {
		return fmt.Errorf("create book: %w", err)
	}
	if _, err := s.chapters.ReplaceTOC(ctx, tx, userID, book.ID, src.ID, toc); err != nil {
		return fmt.Errorf("replace toc: %w", err)
	}
	if err := s.mappings.Upsert(ctx, tx, userID, &repository.BookSourceMapping{
		BookID:        book.ID,
		SourceID:      src.ID,
		BookURL:       localBookURL(book.ID),
		SyncStatus:    repository.MappingStatusActive,
		TotalChapters: int32(readableCount),
	}); err != nil {
		return fmt.Errorf("upsert mapping: %w", err)
	}
	if err := s.books.UpdateTOCMeta(ctx, tx, userID, book.ID, readableCount, latestTitle, nil); err != nil {
		return fmt.Errorf("update toc meta: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCommitFailed, err)
	}
	return nil
}

// waitFileHash 在对方事务尚未提交时做有限重试查询；未命中返回 (nil, nil)。
func (s *ImportService) waitFileHash(
	ctx context.Context, userID uuid.UUID, fileHash string, attempts int,
) (*repository.Book, error) {
	for i := 0; i < attempts; i++ {
		existing, err := s.books.GetByFileHash(ctx, s.pool, userID, fileHash)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil, nil
}

// handleCommitError 在 tx.Commit 返回错误后保守判定是否可清理落盘文件。
// 三态：行存在→保留；明确未命中→清理；探查失败→保守保留。
// 原则：宁留孤儿文件，绝不冒误删正文风险。
func (s *ImportService) handleCommitError(ctx context.Context, userID, bookID uuid.UUID, commitErr error) {
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer probeCancel()

	_, probeErr := s.books.Get(probeCtx, s.pool, userID, bookID)
	if probeErr == nil {
		// 状态 ①：DB 已提交生效 → 保留
		if s.log != nil {
			s.log.Warn("commit error but book row exists, retain storage files",
				"book_id", bookID, "err", commitErr)
		}
		return
	}

	if errors.Is(probeErr, repository.ErrNotFound) {
		// 状态 ②：明确未命中 → 安全清理
		s.cleanupBook(bookID)
		if s.log != nil {
			s.log.Error("commit error, book row absent, cleaned up",
				"book_id", bookID, "err", commitErr)
		}
		return
	}

	// 状态 ③：探查本身失败 → 保守保留，记录 probe_err
	if s.log != nil {
		s.log.Error("commit error, probe failed, files retained for gc",
			"book_id", bookID, "err", commitErr, "probe_err", probeErr)
	}
}

// nextAvailableName 生成「名称 (n)」直到同用户下不冲突。
func (s *ImportService) nextAvailableName(ctx context.Context, userID uuid.UUID, name, author string) (string, string, error) {
	for i := 1; i <= 99; i++ {
		candidate := fmt.Sprintf("%s (%d)", name, i)
		norm := textnorm.NormalizeBook(candidate)
		_, err := s.books.FindByNormalized(ctx, s.pool, userID, norm, textnorm.NormalizeAuthor(author))
		if errors.Is(err, repository.ErrNotFound) {
			return candidate, norm, nil
		}
		if err != nil {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("%w: too many books with the same name", repository.ErrDuplicate)
}

// buildLocalTOC 把解析结果转成章节行；返回可读章节数（非卷名行）与末章标题。
func buildLocalTOC(bookID, sourceID uuid.UUID, chapters []parsedChapter) ([]repository.Chapter, int, string) {
	toc := make([]repository.Chapter, 0, len(chapters))
	readable := 0
	latest := ""
	for _, ch := range chapters {
		chapter := repository.Chapter{
			BookID:   bookID,
			SourceID: sourceID,
			IsVolume: ch.IsVolume,
			IsVIP:    ch.IsVIP,
		}
		if ch.IsVolume {
			chapter.SortOrder = int32(len(toc))
			chapter.Title = ch.Title
			chapter.NormalizedTitle = textnorm.NormalizeChapterTitle(ch.Title)
			chapter.URL = fmt.Sprintf("local:%s:volume:%d", bookID.String(), int32(len(toc)))
			chapter.URLHash = sha256Hex(chapter.URL)
			toc = append(toc, chapter)
			continue
		}
		idx := int32(readable)
		chapter.ChapterIndex = &idx
		chapter.SortOrder = int32(len(toc))
		chapter.Title = ch.Title
		chapter.NormalizedTitle = textnorm.NormalizeChapter(ch.Title)
		chapter.URL = fmt.Sprintf("local:%s:%d", bookID.String(), idx)
		chapter.URLHash = sha256Hex(chapter.URL)
		readable++
		if ch.Title != "" {
			latest = ch.Title
		}
		toc = append(toc, chapter)
	}
	return toc, readable, latest
}

// writeChapterFiles 并发写入正文文件（8 worker）。
func (s *ImportService) writeChapterFiles(ctx context.Context, bookID uuid.UUID, chapters []parsedChapter) error {
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	idx := int32(0)
	for _, ch := range chapters {
		if ch.IsVolume {
			continue
		}
		chapter := ch
		index := idx
		idx++
		group.Go(func() error {
			data := chapter.Content
			key := chapterStorageKey(bookID, index)
			if err := s.storage.Put(gctx, key, strings.NewReader(data), int64(len(data))); err != nil {
				return fmt.Errorf("write chapter %d: %w", index, err)
			}
			return nil
		})
	}
	return group.Wait()
}

// cleanupBook 用独立 Context 清理已落盘文件（与业务 ctx 解耦）。
func (s *ImportService) cleanupBook(bookID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.storage.DeletePrefix(ctx, "local/"+bookID.String()+"/"); err != nil {
		s.warn("cleanup local book files failed", "book_id", bookID, "err", err)
	}
}

func (s *ImportService) warn(msg string, args ...any) {
	if s.log != nil {
		s.log.Warn(msg, args...)
	}
}

func (s *ImportService) parseByType(file *os.File, size int64, fileType, title string) (*parsedBook, []string, error) {
	switch strings.ToLower(fileType) {
	case "epub":
		return s.parseEPUB(file, size, title)
	case "txt":
		return s.parseTXT(file, size, title)
	default:
		return nil, nil, fmt.Errorf("%w: unsupported file type %q", repository.ErrInvalidArgument, fileType)
	}
}

func ptrStr(s string) *string { return &s }

func path_ext(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}
