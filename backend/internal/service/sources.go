package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// 搜索/调试用例的合成错误：用于把「书源不存在」「书源被禁用」变成
// failures[] 里的独立条目，而不是整体失败。
var (
	ErrSourceNotFound = errors.New("service: source not found")
	ErrSourceDisabled = errors.New("service: source disabled")
)

// SourceService 编排书源 CRUD 与 Legado 导入导出。
type SourceService struct {
	pool    *pgxpool.Pool
	sources *repository.SourceRepository
	ie      *repository.ImportExportRepository
}

// NewSourceService 构造书源服务。
func NewSourceService(pool *pgxpool.Pool) *SourceService {
	return &SourceService{
		pool:    pool,
		sources: repository.NewSourceRepository(),
		ie:      repository.NewImportExportRepository(),
	}
}

// List 分页检索书源。
func (s *SourceService) List(ctx context.Context, userID uuid.UUID, f repository.SourceListFilter) ([]*repository.BookSource, int, error) {
	return s.sources.List(ctx, s.pool, userID, f)
}

// Get 点查书源（归属校验）。
func (s *SourceService) Get(ctx context.Context, userID, id uuid.UUID) (*repository.BookSource, error) {
	return s.sources.Get(ctx, s.pool, userID, id)
}

// Create 新建书源（src.UserID 必须已设置）。
func (s *SourceService) Create(ctx context.Context, userID uuid.UUID, src *repository.BookSource) error {
	src.UserID = userID
	// v1.4（ADR-010）：AAD = user_id|source_id，因此 id 必须在加密前确定。
	// Service 显式预生成，Repository.Create 会保留该 id（不再依赖 DB DEFAULT）。
	if src.ID == uuid.Nil {
		src.ID = uuid.New()
	}
	return s.sources.Create(ctx, s.pool, src)
}

// Update 乐观锁更新书源（src.UserID 必须已设置）。
func (s *SourceService) Update(ctx context.Context, userID uuid.UUID, src *repository.BookSource, expectedVersion int64) error {
	src.UserID = userID
	return s.sources.Update(ctx, s.pool, src, expectedVersion)
}

// Delete 删除书源。
func (s *SourceService) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.sources.Delete(ctx, s.pool, userID, id)
}

// Import 导入书源。dry_run 时在 tx 内完整执行后回滚，保证报告统计真实。
func (s *SourceService) Import(ctx context.Context, userID uuid.UUID, sources []json.RawMessage, opts repository.ImportOptions) (*repository.ImportReport, error) {
	if !opts.DryRun {
		return s.ie.Import(ctx, s.pool, userID, sources, opts)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return s.ie.Import(ctx, tx, userID, sources, opts)
}

// Export 导出书源（关系列权威覆写 + 未知键保留）。
func (s *SourceService) Export(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]json.RawMessage, error) {
	return s.ie.Export(ctx, s.pool, userID, ids)
}
