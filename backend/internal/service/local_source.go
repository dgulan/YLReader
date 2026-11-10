package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// localSourceURL 返回用户本地书库虚拟源的 URL。
func localSourceURL(userID uuid.UUID) string {
	return "local://" + userID.String()
}

// LocalSourceService 负责为每个用户维护一个「本地书库」虚拟书源（book_source_type = 3）。
//
// 该源只作为 chapters.source_id / active_source_id 的挂载点，不参与在线检索：
// GET /sources 与 /search 通过 SourceListFilter.ExcludeLocal 屏蔽。
type LocalSourceService struct {
	pool    *pgxpool.Pool
	sources *repository.SourceRepository
}

// NewLocalSourceService 构造本地书库虚拟源服务。
func NewLocalSourceService(pool *pgxpool.Pool) *LocalSourceService {
	return &LocalSourceService{pool: pool, sources: repository.NewSourceRepository()}
}

// EnsureLocalSource 幂等地创建并返回当前用户的本地书库虚拟源。
// 并发场景下依赖 uq_book_sources_user_url 的 23505，冲突后重查即自愈。
func (s *LocalSourceService) EnsureLocalSource(ctx context.Context, userID uuid.UUID) (*repository.BookSource, error) {
	rawURL := localSourceURL(userID)
	src, err := s.sources.GetByURL(ctx, s.pool, userID, rawURL)
	if err == nil {
		return src, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	src = &repository.BookSource{
		UserID:         userID,
		BookSourceURL:  rawURL,
		BookSourceName: "本地书库",
		BookSourceType: 3,
		// 必须 enabled=true：M2 的源可用性校验会对禁用源返回 400。
		Enabled: true,
		Weight:  0,
		Rules:   json.RawMessage(`{}`),
	}
	if cerr := s.sources.Create(ctx, s.pool, src); cerr != nil {
		if errors.Is(cerr, repository.ErrDuplicate) {
			return s.sources.GetByURL(ctx, s.pool, userID, rawURL)
		}
		return nil, fmt.Errorf("ensure local source: %w", cerr)
	}
	return src, nil
}
