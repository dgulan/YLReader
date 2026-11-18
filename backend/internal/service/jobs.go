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

// ErrJobActive 表示该书该源已有进行中的 full_cache 任务（契约 409）。
var ErrJobActive = errors.New("job already active")

// ErrRelocationFailed 表示多源预热的目标源不可用（不在 mappings / 无章节），契约 422 relocation_failed。
var ErrRelocationFailed = errors.New("relocation failed")

// CacheBookInput 是 POST /books/{bookId}/cache 的业务入参。
type CacheBookInput struct {
	FromChapter *int
	ToChapter   *int
	SourceID    *uuid.UUID
}

// JobService 负责后台异步任务的创建、查询、取消与执行（full_cache / toc_refresh / chapter_prefetch / source_health）。
type JobService struct {
	pool     *pgxpool.Pool
	jobs     *repository.JobRepository
	books    *repository.BookRepository
	sources  *repository.SourceRepository
	chapters *repository.ChapterRepository
	content  *ContentService
	toc      *TOCService
	health   *SourceHealthService
	wp       *WorkerPool
	log      *slog.Logger
	onMetric func(jobType, status string)

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
		health:   NewSourceHealthService(pool, log),
		wp:       wp,
		log:      log,
		runs:     make(map[uuid.UUID]context.CancelFunc),
	}
}

// WithTOC 注入目录服务以支持 toc_refresh 任务。
func (s *JobService) WithTOC(toc *TOCService) *JobService {
	s.toc = toc
	return s
}

// WithHealth 注入书源健康度服务以支持 source_health 任务。
func (s *JobService) WithHealth(h *SourceHealthService) *JobService {
	s.health = h
	return s
}

// WithJobMetric 注入任务状态指标回调。
func (s *JobService) WithJobMetric(fn func(jobType, status string)) *JobService {
	s.onMetric = fn
	return s
}

func (s *JobService) recordMetric(jobType, status string) {
	if s.onMetric != nil {
		s.onMetric(jobType, status)
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

	// S5-2 五层门禁（顺序固定）：① 活跃源存在 ② 目标源缺省=活跃源
	// ③ 目标源 ∈ mappings（活跃源缺失映射时容忍并 Warn）④ 目标源可用 ⑤ 目标源有可读章节。
	if book.ActiveSourceID == nil {
		return nil, fmt.Errorf("%w: no active source", ErrUnprocessable)
	}
	activeSourceID := *book.ActiveSourceID
	targetSourceID := activeSourceID
	if in.SourceID != nil {
		targetSourceID = *in.SourceID
	}

	// ③ 目标源必须已绑定到该书；显式非活跃源严格要求，活跃源映射缺失仅 Warn（数据不一致容忍）。
	inMappings, err := s.jobs.HasMappingForSource(ctx, s.pool, userID, bookID, targetSourceID)
	if err != nil {
		return nil, err
	}
	if !inMappings {
		if targetSourceID != activeSourceID {
			return nil, fmt.Errorf("%w: source %s not bound to book %s", ErrRelocationFailed, targetSourceID, bookID)
		}
		s.log.Warn("active source mapping missing; data inconsistency tolerated for default preheat",
			"book_id", bookID, "source_id", targetSourceID)
	}

	// ④ 目标源必须存在且启用。
	src, err := s.sources.Get(ctx, s.pool, userID, targetSourceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("%w: target source not found", ErrUnprocessable)
		}
		return nil, err
	}
	if !src.Enabled {
		return nil, fmt.Errorf("%w: target source disabled", ErrUnprocessable)
	}

	// ⑤ 目标源必须有可读章节（chapter_index IS NOT NULL），否则无法预热。
	readable, err := s.chapters.CountReadable(ctx, s.pool, userID, bookID, targetSourceID)
	if err != nil {
		return nil, err
	}
	if readable == 0 {
		return nil, fmt.Errorf("%w: target source has no readable chapters", ErrRelocationFailed)
	}
	sourceID := src.ID

	// v1.3（DEF-CODE-06）+ S5-2：查重与入队原子化，锁粒度改为 (book, source)，
	// 同书不同源可并行入队；同书同源第二次仍返回 409。
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('cache:' || $1::text || ':' || $2::text, 0))`,
		bookID.String(), sourceID.String(),
	); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}

	busy, err := s.jobs.HasActiveFullCache(ctx, tx, userID, bookID, sourceID)
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
	s.recordMetric("full_cache", "queued")
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
	s.recordMetric(job.Type, "canceled")
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
	s.recordMetric(job.Type, "running")

	switch job.Type {
	case "full_cache":
		err = s.executeFullCache(ctx, job, 0)
	case "chapter_prefetch":
		err = s.executeFullCache(ctx, job, 20)
	case "toc_refresh":
		err = s.executeTOCRefresh(ctx, job)
	case "source_health":
		err = s.executeSourceHealth(ctx, job)
	default:
		s.failJob(ctx, jobID, "internal_error", "unsupported job type: "+job.Type)
		s.recordMetric(job.Type, "failed")
		return fmt.Errorf("unsupported job type: %s", job.Type)
	}
	if err != nil {
		s.recordMetric(job.Type, "failed")
		return err
	}
	if st, serr := s.jobs.GetStatus(context.Background(), s.pool, jobID); serr == nil && st == "succeeded" {
		s.recordMetric(job.Type, "succeeded")
	}
	return nil
}

func (s *JobService) registerRun(jobID uuid.UUID, cancel context.CancelFunc) func() {
	s.mu.Lock()
	s.runs[jobID] = cancel
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.runs, jobID)
		s.mu.Unlock()
	}
}

func (s *JobService) executeFullCache(ctx context.Context, job *repository.Job, maxChapters int) error {
	jobID := job.ID
	userID := job.UserID

	var p struct {
		BookID      string `json:"book_id"`
		SourceID    string `json:"source_id"`
		FromChapter *int   `json:"from_chapter"`
		ToChapter   *int   `json:"to_chapter"`
	}
	// 终态写入一律使用独立的 Background 上下文：Shutdown 窗口内父 ctx 已取消时，
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
	if maxChapters > 0 {
		from := 0
		if p.FromChapter != nil && *p.FromChapter >= 0 {
			from = *p.FromChapter
		}
		to := from + 4
		if p.ToChapter != nil && *p.ToChapter >= from {
			to = min(*p.ToChapter, from+maxChapters-1)
		}
		p.FromChapter = &from
		p.ToChapter = &to
	}

	jobCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	defer s.registerRun(jobID, cancel)()

	// 源信息用 jobCtx 查询：跟随 60min 预算，Shutdown 取消时才能如实归因为取消，
	// 而不是误报 "source missing"（P1-1）。
	src, err := s.sources.Get(jobCtx, s.pool, userID, sourceID)
	if err != nil {
		s.failJob(jobCtx, jobID, "internal_error", "source missing")
		return err
	}

	chapters, err := s.chapters.ListByBookSourceRange(jobCtx, s.pool, userID, bookID, sourceID, p.FromChapter, p.ToChapter)
	if err != nil {
		s.failJob(jobCtx, jobID, "internal_error", err.Error())
		return err
	}
	// to_chapter 超出目标源范围时静默截断，如实记入 skipped_out_of_range。
	readableTotal, cErr := s.chapters.CountReadable(jobCtx, s.pool, userID, bookID, sourceID)
	if cErr != nil {
		s.failJob(jobCtx, jobID, "internal_error", cErr.Error())
		return cErr
	}
	skipped := 0
	if p.ToChapter != nil && *p.ToChapter >= readableTotal {
		skipped = *p.ToChapter - readableTotal + 1
	}
	total := len(chapters)
	if total == 0 {
		return s.jobs.MarkSucceeded(context.Background(), s.pool, jobID, 100,
			map[string]any{"total": 0, "succeeded": 0, "attempted": 0,
				"target_source_id": sourceID.String(), "skipped_out_of_range": skipped})
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
		if cerr := s.content.WarmChapterForSource(jobCtx, userID, bookID, src, ch); cerr != nil {
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
		"total":                total,
		"succeeded":            succeeded,
		"attempted":            done,
		"target_source_id":     sourceID.String(),
		"skipped_out_of_range": skipped,
	})
}

func (s *JobService) executeTOCRefresh(ctx context.Context, job *repository.Job) error {
	if s.toc == nil {
		s.failJob(ctx, job.ID, "internal_error", "toc service unavailable")
		return errors.New("toc service unavailable")
	}
	var p struct {
		BookID   string  `json:"book_id"`
		SourceID *string `json:"source_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		s.failJob(ctx, job.ID, "internal_error", "invalid job payload")
		return err
	}
	bookID, err := uuid.Parse(p.BookID)
	if err != nil {
		s.failJob(ctx, job.ID, "internal_error", "invalid book id in payload")
		return err
	}
	var sourceID *uuid.UUID
	if p.SourceID != nil && *p.SourceID != "" {
		sid, perr := uuid.Parse(*p.SourceID)
		if perr != nil {
			s.failJob(ctx, job.ID, "internal_error", "invalid source id in payload")
			return perr
		}
		sourceID = &sid
	}

	jobCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	defer s.registerRun(job.ID, cancel)()

	view, err := s.toc.GetChapters(jobCtx, job.UserID, bookID, sourceID, true)
	if err != nil {
		if st, serr := s.jobs.GetStatus(context.Background(), s.pool, job.ID); serr == nil && st == "canceled" {
			return nil
		}
		code := "source_request_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "source_timeout"
		}
		s.failJob(ctx, job.ID, code, "toc refresh failed")
		return err
	}
	return s.jobs.MarkSucceeded(context.Background(), s.pool, job.ID, 100, map[string]any{
		"book_id":   bookID.String(),
		"source_id": view.SourceID.String(),
		"total":     view.Total,
	})
}

func (s *JobService) executeSourceHealth(ctx context.Context, job *repository.Job) error {
	if s.content == nil || s.content.ft == nil {
		s.failJob(ctx, job.ID, "internal_error", "fetcher unavailable")
		return errors.New("fetcher unavailable")
	}
	var p struct {
		SourceID string `json:"source_id"`
		ProbeURL string `json:"probe_url"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		s.failJob(ctx, job.ID, "internal_error", "invalid job payload")
		return err
	}
	sourceID, err := uuid.Parse(p.SourceID)
	if err != nil {
		s.failJob(ctx, job.ID, "internal_error", "invalid source id in payload")
		return err
	}

	jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	defer s.registerRun(job.ID, cancel)()

	src, err := s.sources.Get(jobCtx, s.pool, job.UserID, sourceID)
	if err != nil {
		s.failJob(ctx, job.ID, "not_found", "source missing")
		return err
	}
	target := src.BookSourceURL
	if p.ProbeURL != "" {
		target = p.ProbeURL
	}
	start := time.Now()
	resp, ferr := s.content.ft.Get(sourceFetchContext(jobCtx, src), target, parseSourceHeaders(src.Header))
	elapsed := time.Since(start)
	ok := ferr == nil && resp != nil && resp.StatusCode < 400
	if !ok {
		if ferr == nil && resp != nil {
			ferr = fmt.Errorf("upstream status %d", resp.StatusCode)
		}
		s.health.RecordFailure(job.UserID, src.ID, ferr)
	} else {
		s.health.RecordSuccess(job.UserID, src.ID, elapsed)
	}
	return s.jobs.MarkSucceeded(context.Background(), s.pool, job.ID, 100, map[string]any{
		"source_id":  src.ID.String(),
		"ok":         ok,
		"elapsed_ms": elapsed.Milliseconds(),
	})
}

// failJob 记录失败终态。始终使用独立 context：Shutdown 窗口内父 ctx 已取消时，
// 任务状态仍必须可靠落库（P3-1，M3c 闭环）。
func (s *JobService) failJob(_ context.Context, jobID uuid.UUID, code, message string) {
	_ = s.jobs.MarkFailed(context.Background(), s.pool, jobID, code, message)
}
