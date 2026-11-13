package service

import (
	"context"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// ProgressService 编排阅读进度读写（乐观锁 + books.last_read_at 刷新）。
type ProgressService struct {
	pool     *pgxpool.Pool
	books    *repository.BookRepository
	progress *repository.ProgressRepository
}

// NewProgressService 构造进度服务。
func NewProgressService(pool *pgxpool.Pool) *ProgressService {
	return &ProgressService{
		pool:     pool,
		books:    repository.NewBookRepository(),
		progress: repository.NewProgressRepository(),
	}
}

// Get 读取进度：书籍不存在 → ErrNotFound(404)；无进度记录 → 0 值 + Version=1（200，不得 404）。
func (s *ProgressService) Get(ctx context.Context, userID, bookID uuid.UUID) (*repository.ReadingProgress, error) {
	if _, err := s.books.Get(ctx, s.pool, userID, bookID); err != nil {
		return nil, err
	}
	return s.progress.Get(ctx, s.pool, userID, bookID)
}

// Update 按 expectedVersion 乐观锁推进进度，并在同一事务内刷新 last_read_at。
func (s *ProgressService) Update(ctx context.Context, userID uuid.UUID, p *repository.ReadingProgress, expectedVersion int64) (*repository.ReadingProgress, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.books.Lock(ctx, tx, userID, p.BookID); err != nil {
		return nil, err
	}
	book, err := s.books.Get(ctx, tx, userID, p.BookID)
	if err != nil {
		return nil, err
	}
	p.ProgressPercent = math.Min(100, math.Max(0, (float64(p.ChapterIndex)+p.ChapterRatio)/float64(max(book.TotalChapters, 1))*100))
	// 无持久化记录时，GET 对外承诺的是合成 version=1；首次 PUT 视为基于
	// 该状态的一次推进，落库为 version=2（与契约 1→2 对齐）。
	exists, err := s.progress.Exists(ctx, tx, userID, p.BookID)
	if err != nil {
		return nil, err
	}
	if exists {
		if err := s.progress.Update(ctx, tx, p, expectedVersion); err != nil {
			return nil, err
		}
	} else {
		if expectedVersion != 1 {
			return nil, repository.ErrVersionConflict
		}
		if err := s.progress.InsertInitial(ctx, tx, p); err != nil {
			return nil, err
		}
	}
	if err := s.books.TouchLastRead(ctx, tx, userID, p.BookID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}
