package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// CreateBookmarkInput 是新建书签的业务入参。
type CreateBookmarkInput struct {
	ChapterIndex int32
	CharOffset   int32
	ChapterTitle *string
	Excerpt      *string
	Note         *string
}

// BookmarkService 负责书签的增删查。
type BookmarkService struct {
	pool      *pgxpool.Pool
	bookmarks *repository.BookmarkRepository
	books     *repository.BookRepository
	log       *slog.Logger
}

// NewBookmarkService 构造 BookmarkService。
func NewBookmarkService(pool *pgxpool.Pool, log *slog.Logger) *BookmarkService {
	if log == nil {
		log = slog.Default()
	}
	return &BookmarkService{
		pool:      pool,
		bookmarks: repository.NewBookmarkRepository(),
		books:     repository.NewBookRepository(),
		log:       log,
	}
}

// List 返回某书的书签；空结果为 []。
func (s *BookmarkService) List(ctx context.Context, userID, bookID uuid.UUID) ([]*repository.Bookmark, error) {
	items, err := s.bookmarks.ListByBook(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	if items == nil {
		items = []*repository.Bookmark{}
	}
	return items, nil
}

// Create 新建书签；书不存在/不属于当前用户时返回 ErrInvalidArgument。
func (s *BookmarkService) Create(ctx context.Context, userID, bookID uuid.UUID, in CreateBookmarkInput) (*repository.Bookmark, error) {
	if _, err := s.books.Get(ctx, s.pool, userID, bookID); err != nil {
		return nil, fmt.Errorf("%w: book not found", repository.ErrInvalidArgument)
	}
	if in.ChapterIndex < 0 || in.CharOffset < 0 {
		return nil, fmt.Errorf("%w: chapter_index and char_offset must be non-negative", repository.ErrInvalidArgument)
	}
	title := ""
	if in.ChapterTitle != nil {
		title = *in.ChapterTitle
	}
	b := &repository.Bookmark{
		UserID:       userID,
		BookID:       bookID,
		ChapterIndex: in.ChapterIndex,
		CharOffset:   in.CharOffset,
		ChapterTitle: title,
		Excerpt:      in.Excerpt,
		Note:         in.Note,
	}
	if err := s.bookmarks.Create(ctx, s.pool, b); err != nil {
		if errors.Is(err, repository.ErrDuplicate) {
			return nil, err
		}
		return nil, fmt.Errorf("create bookmark: %w", err)
	}
	return b, nil
}

// Delete 删除书签；不存在返回 ErrNotFound。
func (s *BookmarkService) Delete(ctx context.Context, userID, bookmarkID uuid.UUID) error {
	return s.bookmarks.Delete(ctx, s.pool, userID, bookmarkID)
}
