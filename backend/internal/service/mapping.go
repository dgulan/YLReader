package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// SwitchSourceResult reports the selected target chapter and relocation confidence.
type SwitchSourceResult struct {
	BookID        uuid.UUID
	SourceID      uuid.UUID
	Strategy      string
	Confidence    float64
	ChapterIndex  int32
	ChapterRatio  float64
	CharOffset    int32
	ChapterTitle  string
	TotalChapters int
	DryRun        bool
}

// MappingService 编排整本换源（含单章覆盖的级联清空）。
type MappingService struct {
	pool      *pgxpool.Pool
	books     *repository.BookRepository
	mappings  *repository.MappingRepository
	sources   *repository.SourceRepository
	overrides *repository.OverrideRepository
	progress  *repository.ProgressRepository
}

// NewMappingService 构造换源服务。
func NewMappingService(pool *pgxpool.Pool) *MappingService {
	return &MappingService{
		pool:      pool,
		books:     repository.NewBookRepository(),
		mappings:  repository.NewMappingRepository(),
		sources:   repository.NewSourceRepository(),
		overrides: repository.NewOverrideRepository(),
		progress:  repository.NewProgressRepository(),
	}
}

// Switch 在单个事务内完成换源：
//  1. 校验书籍归属与目标源（属于当前用户且 enabled）；
//  2. 解析 targetBookURL（显式优先，否则继承该书在该源的历史 mapping URL；都没有 → 400）；
//  3. 在目标目录按精确/模糊/整书比例定位；
//  4. 原子更新活跃源、进度与元数据，清空单章覆盖；
//  5. dry_run 回滚，否则提交；
//  6. 返回真实策略、置信度与目标位置，不在事务内发起网络抓取。
func (s *MappingService) Switch(ctx context.Context, userID, bookID, sourceID uuid.UUID, bookURL *string, dryRun bool) (*SwitchSourceResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.books.Lock(ctx, tx, userID, bookID); err != nil {
		return nil, err
	}
	book, err := s.books.Get(ctx, tx, userID, bookID)
	if err != nil {
		return nil, err
	}
	// 本地书没有在线源，不允许整本换源。
	if book.BookType == repository.BookTypeLocal {
		return nil, fmt.Errorf("%w: local books cannot be switched", repository.ErrInvalidArgument)
	}
	src, err := s.sources.Get(ctx, tx, userID, sourceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("%w: target source not found", repository.ErrInvalidArgument)
		}
		return nil, err
	}
	if !src.Enabled {
		return nil, fmt.Errorf("%w: target source disabled", repository.ErrInvalidArgument)
	}

	target := ""
	if bookURL != nil && strings.TrimSpace(*bookURL) != "" {
		target = strings.TrimSpace(*bookURL)
		if verr := validateHTTPURL(target); verr != nil {
			return nil, verr
		}
	} else {
		m, merr := s.mappings.GetBySourceID(ctx, tx, userID, bookID, sourceID)
		if merr != nil {
			if errors.Is(merr, repository.ErrNotFound) {
				return nil, fmt.Errorf("%w: target source has no existing book_url, book_url is required", repository.ErrInvalidArgument)
			}
			return nil, merr
		}
		if strings.TrimSpace(m.BookURL) == "" {
			return nil, fmt.Errorf("%w: target source has no existing book_url, book_url is required", repository.ErrInvalidArgument)
		}
		target = m.BookURL
	}

	prog, err := s.progress.Get(ctx, tx, userID, bookID)
	if err != nil {
		return nil, err
	}
	ch, strategy, confidence, total, err := relocateChapter(ctx, tx, userID, book, sourceID, prog)
	if err != nil {
		return nil, err
	}
	// TOC word_count may be estimated; only the displayed body is authoritative.
	offset := int32(0)
	res := &SwitchSourceResult{BookID: bookID, SourceID: sourceID, Strategy: strategy, Confidence: confidence, ChapterIndex: *ch.ChapterIndex, ChapterRatio: prog.ChapterRatio, CharOffset: offset, ChapterTitle: ch.Title, TotalChapters: total, DryRun: dryRun}
	if !dryRun {
		if err := s.books.UpdateActiveSource(ctx, tx, userID, bookID, sourceID, target); err != nil {
			return nil, err
		}
		if err := s.overrides.DeleteBookOverrides(ctx, tx, userID, bookID); err != nil {
			return nil, err
		}
		exists, err := s.progress.Exists(ctx, tx, userID, bookID)
		if err != nil {
			return nil, err
		}
		expected := prog.Version
		prog.ChapterIndex = *ch.ChapterIndex
		prog.ChapterTitle = ch.Title
		prog.NormalizedTitle = ch.NormalizedTitle
		prog.CharOffset = offset
		prog.ProgressPercent = (float64(prog.ChapterIndex) + prog.ChapterRatio) / float64(total) * 100
		if exists {
			err = s.progress.Update(ctx, tx, prog, expected)
		} else {
			err = s.progress.InsertInitial(ctx, tx, prog)
		}
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE books SET total_chapters=$3 WHERE user_id=$1 AND id=$2`, userID, bookID, total); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE book_source_mappings SET last_synced_at=now() WHERE book_id=$1 AND source_id=$2 AND book_url=$3`, bookID, sourceID, target); err != nil {
			return nil, err
		}
	}

	if dryRun {
		return res, nil // deferred rollback
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// List 返回某本书的全部多源绑定（按 created_at 升序；无绑定返回空切片）。
func (s *MappingService) List(ctx context.Context, userID, bookID uuid.UUID) ([]*repository.BookSourceMapping, error) {
	if _, err := s.books.Get(ctx, s.pool, userID, bookID); err != nil {
		return nil, err
	}
	return s.mappings.ListByBook(ctx, s.pool, userID, bookID)
}

// Add 新增一条备用源绑定（pending）。源不存在/禁用/非本人 → ErrUnprocessable(422)；
// 相同 (book, source, url) 重复 → ErrDuplicate(409)。
func (s *MappingService) Add(ctx context.Context, userID, bookID, sourceID uuid.UUID, bookURL string) (*repository.BookSourceMapping, error) {
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, err
	}
	src, err := s.sources.Get(ctx, s.pool, userID, sourceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("%w: source not found", ErrUnprocessable)
		}
		return nil, err
	}
	if !src.Enabled {
		return nil, fmt.Errorf("%w: source disabled", ErrUnprocessable)
	}
	u := strings.TrimSpace(bookURL)
	if verr := validateHTTPURL(u); verr != nil {
		return nil, verr
	}

	srcName, authorName := book.Name, book.Author
	m := &repository.BookSourceMapping{
		BookID:     bookID,
		SourceID:   sourceID,
		BookURL:    u,
		Name:       &srcName,
		Author:     &authorName,
		SyncStatus: repository.MappingStatusPending,
	}
	if err := s.mappings.Insert(ctx, s.pool, userID, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Delete 删除一条备用源绑定；删除当前活跃源绑定 → ErrMappingActive(409)。
func (s *MappingService) Delete(ctx context.Context, userID, bookID, mappingID uuid.UUID) error {
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return err
	}
	m, err := s.mappings.GetByID(ctx, s.pool, userID, bookID, mappingID)
	if err != nil {
		return err
	}
	if book.ActiveSourceID != nil && m.SourceID == *book.ActiveSourceID {
		return fmt.Errorf("%w: cannot delete active source mapping", ErrMappingActive)
	}
	return s.mappings.DeleteByID(ctx, s.pool, userID, bookID, mappingID)
}
