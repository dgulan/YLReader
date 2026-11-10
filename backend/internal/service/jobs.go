package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// ErrUnprocessable 表示请求语义不可处理（契约 422），例如本地书预热、
// 缺少可用活跃书源、addMapping 的源不可用。
var ErrUnprocessable = errors.New("unprocessable request")

// ErrMappingActive 表示禁止删除当前活跃书源映射（契约 409）。
var ErrMappingActive = errors.New("cannot delete active source mapping")

// ErrJobFinal 表示任务已处于终态，无法取消（契约 409）。
var ErrJobFinal = errors.New("job already in final state")

// ErrJobActive 表示该书已有进行中的 full_cache 任务（契约 409）。
var ErrJobActive = errors.New("job already active")

// CacheBookInput 是 POST /books/{bookId}/cache 的业务入参。
type CacheBookInput struct {
	FromChapter *int
	ToChapter   *int
	SourceID    *uuid.UUID
}

// JobService 负责整本书预热任务的创建、查询、取消与执行。
type JobService struct {
	pool     *pgxpool.Pool
	jobs     *repository.JobRepository
	books    *repository.BookRepository
	sources  *repository.SourceRepository
	chapters *repository.ChapterRepository
	content  *ContentService
	wp       *WorkerPool
	log      *slog.Logger

	mu   sync.Mutex
	runs map[uuid.UUID]context.CancelFunc
}

// NewJobService 构造任务服务（log 为 nil 时回退默认 logger）。
func NewJobService(pool *pgxpool.Pool, content *ContentService, wp *WorkerPool, log *slog.Logger) *JobService {
	if log == nil {
		log = slog.Default()
	}
	return &JobService{
		pool:     pool,
		jobs:     repository.NewJobRepository(),
		books:    repository.NewBookRepository(),
		sources:  repository.NewSourceRepository(),
		chapters: repository.NewChapterRepository(),
		content:  content,
		wp:       wp,
		log:      log,
		runs:     make(map[uuid.UUID]context.CancelFunc),
	}
}

// StartWorkers 启动内部 Worker 池（测试与 main.go 均可调用）。
func (s *JobService) StartWorkers(ctx context.Context) {
	if s.wp != nil {
		s.wp.Start(ctx)
	}
}

// StopWorkers 优雅停止 Worker 池。
func (s *JobService) StopWorkers(ctx context.Context) {
	if s.wp != nil {
		s.wp.Shutdown(ctx)
	}
}

// Notify 唤醒 Worker（测试可直接调用以加速）。
func (s *JobService) Notify() {
	if s.wp != nil {
		s.wp.Notify()
	}
}

// CacheBook 创建整本预热任务：校验归属/本地书/书源，单书防重，插入 queued 后唤醒 Worker。
func (s *JobService) CacheBook(ctx context.Context, userID, bookID uuid.UUID, in CacheBookInput) (*repository.Job, error) {
	book, err := s.books.Get(ctx, s.pool, userID, bookID)
	if err != nil {
		return nil, err
	}
	if book.BookType == repository.BookTypeLocal {
		return nil, fmt.Errorf("%w: local books cannot be cached from network", ErrUnprocessable)
	}

	// CacheBook 三层门禁：① 必须有活跃源；② 活跃源必须启用；③ 显式 source_id 必须等于活跃源。
	// 第③条是 v1.3 收口语义——当前缓存任务只预热活跃源目录，多源预热留给后续重构（DEF-OAS-08）。
	if book.ActiveSourceID == nil {
		return nil, fmt.Errorf("%w: no active source", ErrUnprocessable)
	}
	src, err := s.sources.Get(ctx, s.pool, userID, *book.ActiveSourceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("%w: active source not found", ErrUnprocessable)
		}
		return nil, err
	}
	if !src.Enabled {
		return nil, fmt.Errorf("%w: active source disabled", ErrUnprocessable)
	}
	if in.SourceID != nil && *in.SourceID != *book.ActiveSourceID {
		return nil, fmt.Errorf("%w: explicit source_id must equal the active source", ErrUnprocessable)
	}
	sourceID := src.ID

	// v1.3（DEF-CODE-06）：「查重 + 入队」必须原子化。前置只读校验已在上面完成，
	// 此处不再做无锁 HasActiveFullCache（否则命中瞬间并发任务完成会误报 409），
	// 而是把查重与插入放进持有 64 位排他咨询锁的显式事务内。
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('cache:' || $1::text, 0))`,
		bookID.String(),
	); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}

	busy, err := s.jobs.HasActiveFullCache(ctx, tx, userID, bookID)
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, fmt.Errorf("%w: full cache job already queued or running", ErrJobActive)
	}

	payload := map[string]any{
		"book_id":   bookID.String(),
		"source_id": sourceID.String(),
	}
	if in.FromChapter != nil {
		payload["from_chapter"] = *in.FromChapter
	}
	if in.ToChapter != nil {
		payload["to_chapter"] = *in.ToChapter
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	job, err := s.jobs.Create(ctx, tx, userID, "full_cache", raw)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit cache job: %w", err)
	}
	// 任务已在 DB 权威队列中；Notify 只是唤醒优化（丢失由 5s ticker 兜底）。
	s.Notify()
	return job, nil
}

// Get 读取任务。
func (s *JobService) Get(ctx context.Context, userID, jobID uuid.UUID) (*repository.Job, error) {
	return s.jobs.GetByID(ctx, s.pool, userID, jobID)
}

// Cancel 取消任务：终态 → ErrJobFinal；queued/running → canceled 并中断执行。
func (s *JobService) Cancel(ctx context.Context, userID, jobID uuid.UUID) error {
	job, err := s.jobs.GetByID(ctx, s.pool, userID, jobID)
	if err != nil {
		return err
	}
	switch job.Status {
	case "succeeded", "failed", "canceled":
		return fmt.Errorf("%w: %s", ErrJobFinal, job.Status)
	}

	ok, err := s.jobs.Cancel(ctx, s.pool, userID, jobID)
	if err != nil {
		return err
	}
	if !ok {
		// 并发下已被 Worker 推进到终态。
		return fmt.Errorf("%w: job state changed", ErrJobFinal)
	}

	s.mu.Lock()
	cancel := s.runs[jobID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// ExecuteJob 执行单个任务（由 WorkerPool 调用；任务已由 claimOne 置为 running）。
func (s *JobService) ExecuteJob(ctx context.Context, jobID uuid.UUID) error {
	job, err := s.jobs.GetAny(ctx, s.pool, jobID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil // 任务已被删除（防御）
	}
	if err != nil {
		return err
	}
	userID := job.UserID

	var p struct {
		BookID      string `json:"book_id"`
		SourceID    string `json:"source_id"`
		FromChapter *int   `json:"from_chapter"`
		ToChapter   *int   `json:"to_chapter"`
	}
	// 终态写入一律使用独立 context.Background()：Shutdown 窗口内父 ctx 已取消时，
	// 任务状态仍必须可靠落库，避免 job 永远停在 running。
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, "internal_error", "invalid job payload")
		return err
	}
	bookID, err := uuid.Parse(p.BookID)
	if err != nil {
		_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, "internal_error", "invalid book id in payload")
		return err
	}
	sourceID, err := uuid.Parse(p.SourceID)
	if err != nil {
		_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, "internal_error", "invalid source id in payload")
		return err
	}

	jobCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	s.mu.Lock()
	s.runs[jobID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.runs, jobID)
		s.mu.Unlock()
	}()

	chapters, err := s.chapters.ListByBookSourceRange(jobCtx, s.pool, userID, bookID, sourceID, p.FromChapter, p.ToChapter)
	if err != nil {
		s.failJob(jobCtx, jobID, "internal_error", err.Error())
		return err
	}
	total := len(chapters)
	if total == 0 {
		return s.jobs.MarkSucceeded(context.Background(), s.pool, jobID, 100,
			map[string]any{"total": 0, "succeeded": 0, "attempted": 0})
	}

	done, succeeded := 0, 0
	for _, ch := range chapters {
		if jobCtx.Err() != nil {
			// Shutdown/取消由 Cancel 负责；预算超时必须显式落 source_timeout 终态。
			if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
				_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, "source_timeout",
					"cache job exceeded 60m budget")
			}
			return nil
		}
		if st, serr := s.jobs.GetStatus(context.Background(), s.pool, jobID); serr == nil && st == "canceled" {
			return nil
		}
		idx := 0
		if ch.ChapterIndex != nil {
			idx = int(*ch.ChapterIndex)
		}
		if _, cerr := s.content.Get(jobCtx, userID, bookID, &idx, nil); cerr != nil {
			if jobCtx.Err() != nil {
				if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
					_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, "source_timeout",
						"cache job exceeded 60m budget")
				}
				return nil
			}
			s.log.Warn("cache chapter failed", "job_id", jobID, "chapter", idx, "err", cerr)
		} else {
			succeeded++
		}
		done++
		// 进度只按成功章节推进：循环内与终态同一口径，杜绝进度条倒退。
		progress := int16(succeeded * 100 / total)
		_ = s.jobs.UpdateProgress(jobCtx, s.pool, jobID, progress)
	}

	if succeeded == 0 {
		_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, "content_unavailable",
			"all chapters failed to fetch content")
		return fmt.Errorf("all chapters failed to cache")
	}

	// done = 已尝试章节数（含失败）；succeeded = 成功章节数。键名与变量语义严格对齐。
	finalProgress := int16(succeeded * 100 / total)
	return s.jobs.MarkSucceeded(context.Background(), s.pool, jobID, finalProgress, map[string]any{
		"total":     total,
		"succeeded": succeeded,
		"attempted": done,
	})
}

// failJob 记录失败终态。始终使用独立 context：Shutdown 窗口内父 ctx 已取消时，
// 任务状态仍必须可靠落库（P3-1，M3c 闭环）。
func (s *JobService) failJob(_ context.Context, jobID uuid.UUID, code, message string) {
	_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, code, message)
}
