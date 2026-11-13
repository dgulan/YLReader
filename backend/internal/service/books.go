package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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
