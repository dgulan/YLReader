package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// WorkerPool 是「内存唤醒 + DB 权威队列」混合模型（architecture §6.2）：
// jobs 表是唯一权威队列，Worker 通过 FOR UPDATE SKIP LOCKED 原子租约任务；
// Notify 仅用于降低延迟，丢失不影响正确性（5s ticker 兜底轮询）。
// v1.3（M3c）：增加实例标识与主机前缀，用于多实例租约隔离与自愈。
type WorkerPool struct {
	pool     *pgxpool.Pool
	capacity int
	execute  func(ctx context.Context, jobID uuid.UUID) error
	wakeCh   chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	log      *slog.Logger

	workerID   string // 实例唯一标识 hostname-<pid>-<nano>
	hostPrefix string // 同主机前缀 hostname-
}

// NewWorkerPool 构造 Worker 池。
// 说明：与指令草案的 NewWorkerPool(capacity, log) 相比多一个 pool 参数——
// DB 是权威队列，claimOne 必须持有连接池；该签名差异不影响契约。
func NewWorkerPool(pool *pgxpool.Pool, capacity int, log *slog.Logger) *WorkerPool {
	if log == nil {
		log = slog.Default()
	}
	if capacity <= 0 {
		capacity = 1
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "localhost"
	}
	return &WorkerPool{
		pool:       pool,
		capacity:   capacity,
		wakeCh:     make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		log:        log,
		workerID:   fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano()),
		hostPrefix: hostname + "-",
	}
}

// SetExecuteFunc 注入任务执行回调（main.go 中为 jobSvc.ExecuteJob）。
func (wp *WorkerPool) SetExecuteFunc(fn func(ctx context.Context, jobID uuid.UUID) error) {
	wp.execute = fn
}

// Notify 非阻塞唤醒：已有 pending 信号则丢弃（合并）。
func (wp *WorkerPool) Notify() {
	select {
	case wp.wakeCh <- struct{}{}:
	default:
	}
}

// Start 先把遗留 running 任务置为 failed（进程重启自愈），再启动 N 个 Worker。
func (wp *WorkerPool) Start(ctx context.Context) {
	repo := repository.NewJobRepository()
	if n, err := repo.MarkStaleRunningAsFailed(context.Background(), wp.pool,
		"service_shutdown", "worker pool restarted before task completed",
		wp.hostPrefix, wp.workerID); err != nil {
		wp.log.Warn("mark stale running jobs failed", "err", err)
	} else if n > 0 {
		wp.log.Info("marked stale running jobs as failed", "count", n)
	}

	for i := 0; i < wp.capacity; i++ {
		wp.wg.Add(1)
		go wp.worker(ctx, i)
	}
}

// worker 是单 Worker 主循环：排空队列 → 阻塞等待 Notify/ticker/退出。
func (wp *WorkerPool) worker(ctx context.Context, id int) {
	defer wp.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		for {
			jobID, ok, err := wp.claimOne(ctx)
			if err != nil {
				wp.log.Warn("claim job failed", "worker", id, "err", err)
				break
			}
			if !ok {
				break
			}
			if wp.execute == nil {
				wp.log.Warn("execute func not set; job stays running", "worker", id, "job_id", jobID)
				break
			}
			if err := wp.execute(ctx, jobID); err != nil {
				wp.log.Warn("execute job failed", "worker", id, "job_id", jobID, "err", err)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-wp.stopCh:
			return
		case <-wp.wakeCh:
		case <-ticker.C:
		}
	}
}

// claimOne 原子租约一个 queued 任务并置为 running，同时登记当前 workerID（v1.3 租约）。
func (wp *WorkerPool) claimOne(ctx context.Context) (uuid.UUID, bool, error) {
	var jobID uuid.UUID
	err := wp.pool.QueryRow(ctx, `
		UPDATE jobs
		   SET status = 'running', started_at = now(), worker_id = $1
		 WHERE id = (
		     SELECT id FROM jobs
		      WHERE status = 'queued'
		      ORDER BY created_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		RETURNING id`, wp.workerID).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return jobID, true, nil
}

// Shutdown 优雅退出：不再拾取新任务，等待 Worker 结束或 ctx 超时。
func (wp *WorkerPool) Shutdown(ctx context.Context) {
	wp.stopOnce.Do(func() { close(wp.stopCh) })
	done := make(chan struct{})
	go func() {
		wp.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		wp.log.Info("worker pool shutdown complete")
	case <-ctx.Done():
		wp.log.Warn("worker pool shutdown timeout, some jobs may be interrupted",
			"policy", "marked as failed on next startup scan")
	}
}
