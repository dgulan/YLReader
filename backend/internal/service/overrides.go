package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// ErrNoMatchingSource 表示 chapter_url 的 host 未匹配到用户启用书源（→422 no_matching_source）。
var ErrNoMatchingSource = errors.New("service: no matching source")

// OverrideService 编排单章源覆盖（Upsert / 幂等删除）。
type OverrideService struct {
	pool      *pgxpool.Pool
	books     *repository.BookRepository
	sources   *repository.SourceRepository
	overrides *repository.OverrideRepository
	content   *ContentService
}

// NewOverrideService 构造单章覆盖服务。
func NewOverrideService(pool *pgxpool.Pool) *OverrideService {
	return &OverrideService{
		pool:      pool,
		books:     repository.NewBookRepository(),
		sources:   repository.NewSourceRepository(),
		overrides: repository.NewOverrideRepository(),
	}
}

// WithContent 注入正文服务以支持设置覆盖时的正文试抓与缓存刷新。
func (s *OverrideService) WithContent(c *ContentService) *OverrideService {
	s.content = c
	return s
}

// Set 设置/更新单章覆盖（兼容不读取试抓预览的调用方）。
func (s *OverrideService) Set(ctx context.Context, userID, bookID uuid.UUID, chapterIndex int32, chapterURL string, sourceID *uuid.UUID) (*repository.ChapterSourceOverride, *repository.BookSource, error) {
	ov, src, _, err := s.SetWithPreview(ctx, userID, bookID, chapterIndex, chapterURL, sourceID)
	return ov, src, err
}

// SetWithPreview 设置/更新单章覆盖。sourceID 为 nil 时按 chapter_url 的 host 在启用书源中自动匹配
// （v1.1 §4.6.2）；匹配不到返回 ErrNoMatchingSource。注入 ContentService 时先试抓正文并刷新缓存，
// 返回前 512 Unicode code point 预览；试抓失败则不落库。
func (s *OverrideService) SetWithPreview(ctx context.Context, userID, bookID uuid.UUID, chapterIndex int32, chapterURL string, sourceID *uuid.UUID) (*repository.ChapterSourceOverride, *repository.BookSource, string, error) {
	if chapterIndex < 0 {
		return nil, nil, "", fmt.Errorf("%w: chapter_index must be >= 0", repository.ErrInvalidArgument)
	}
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, nil, "", err
	}
	// 本地书正文直接来自对象存储，不支持单章覆盖。
	if book.BookType == repository.BookTypeLocal {
		return nil, nil, "", fmt.Errorf("%w: local books do not support chapter overrides", repository.ErrInvalidArgument)
	}
	chapterURL = strings.TrimSpace(chapterURL)
	if err := validateHTTPURL(chapterURL); err != nil {
		return nil, nil, "", err
	}

	var src *repository.BookSource
	if sourceID != nil {
		got, err := s.sources.Get(ctx, s.pool, userID, *sourceID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, nil, "", fmt.Errorf("%w: source not found", repository.ErrInvalidArgument)
			}
			return nil, nil, "", err
		}
		if !got.Enabled {
			return nil, nil, "", fmt.Errorf("%w: source disabled", repository.ErrInvalidArgument)
		}
		src = got
	} else {
		matched, err := s.matchByHost(ctx, userID, chapterURL)
		if err != nil {
			return nil, nil, "", err
		}
		src = matched
	}

	var preview string
	var writeCache func(context.Context)
	if s.content != nil {
		fetched, cacheFn, ferr := s.content.TrialFetchForOverride(ctx, userID, bookID, chapterIndex, src, chapterURL)
		if ferr != nil {
			return nil, nil, "", ferr
		}
		preview = truncatePreviewRunes(fetched, 512)
		writeCache = cacheFn
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := repository.NewChapterRepository().LockTOCSource(ctx, tx, userID, src.ID, src.Version); err != nil {
		return nil, nil, "", err
	}
	if err := s.books.Lock(ctx, tx, userID, bookID); err != nil {
		return nil, nil, "", err
	}
	ov := &repository.ChapterSourceOverride{
		UserID:       userID,
		BookID:       bookID,
		ChapterIndex: chapterIndex,
		SourceID:     src.ID,
		ChapterURL:   chapterURL,
		Status:       repository.OverrideStatusActive,
	}
	if err := s.overrides.Upsert(ctx, tx, ov); err != nil {
		return nil, nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, "", err
	}
	if writeCache != nil {
		writeCache(ctx)
	}
	return ov, src, preview, nil
}

func truncatePreviewRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return ""
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}

// Delete 幂等删除单章覆盖：记录不存在也视为成功。
func (s *OverrideService) Delete(ctx context.Context, userID, bookID uuid.UUID, chapterIndex int32) error {
	if chapterIndex < 0 {
		return fmt.Errorf("%w: chapter_index must be >= 0", repository.ErrInvalidArgument)
	}
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return err
	}
	if book.BookType == repository.BookTypeLocal {
		return fmt.Errorf("%w: local books do not support chapter source overrides", repository.ErrInvalidArgument)
	}
	err = s.overrides.Delete(ctx, s.pool, userID, bookID, chapterIndex)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	return err
}

// matchByHost 按规范化 host 在用户启用书源中匹配（列表已按 weight DESC, updated_at DESC 排序）。
func (s *OverrideService) matchByHost(ctx context.Context, userID uuid.UUID, chapterURL string) (*repository.BookSource, error) {
	u, err := url.Parse(chapterURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: invalid chapter_url", repository.ErrInvalidArgument)
	}
	target := normalizeHost(u)
	sources, err := s.sources.ListEnabledForMatch(ctx, s.pool, userID)
	if err != nil {
		return nil, err
	}
	for _, src := range sources {
		su, perr := url.Parse(src.BookSourceURL)
		if perr != nil || su.Host == "" {
			continue
		}
		if normalizeHost(su) == target {
			return src, nil
		}
	}
	return nil, fmt.Errorf("%w: host %s", ErrNoMatchingSource, target)
}

// normalizeHost 规范化 host：小写、剥离 www.、去端口（v1.1 §4.6.2）。
func normalizeHost(u *url.URL) string {
	h := strings.ToLower(u.Hostname())
	h = strings.TrimPrefix(h, "www.")
	return h
}
