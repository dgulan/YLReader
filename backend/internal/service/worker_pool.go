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
// v1.4（S5-3）：新增常驻巡检协程与统一派生 Context，全部 worker/巡检协程
// 纳入同一个 WaitGroup，Shutdown 双通道等待，杜绝停机等待死锁与协程泄漏。
type WorkerPool struct {
	pool     *pgxpool.Pool
	capacity int
	execute  func(ctx context.Context, jobID uuid.UUID) error
	wakeCh   chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	log      *slog.Logger
	jobs     *repository.JobRepository

	workerID   string // 实例唯一标识 hostname-<pid>-<nano>
	hostPrefix string // 同主机前缀 hostname-

	// reaperInterval 是常驻巡检周期（默认 10min）；测试可经 WithReaperInterval 注入短周期。
	reaperInterval time.Duration

	// mu 保护 Start/Shutdown 的并发状态；cancel 由 Start 派生，Shutdown 使用。
	mu         sync.Mutex
	cancel     context.CancelFunc
	isShutdown bool
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
		pool:           pool,
		capacity:       capacity,
		wakeCh:         make(chan struct{}, 1),
		stopCh:         make(chan struct{}),
		log:            log,
		jobs:           repository.NewJobRepository(),
		workerID:       fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano()),
		hostPrefix:     hostname + "-",
		reaperInterval: 10 * time.Minute,
	}
}

// WithReaperInterval 供测试注入短周期（如 100ms）；生产环境不调用。
func (wp *WorkerPool) WithReaperInterval(d time.Duration) *WorkerPool {
	if d > 0 {
		wp.reaperInterval = d
	}
	return wp
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

// Start 启动 Worker 池：
//  1. 同步执行一次遗留任务自愈扫描（保持 DEF-CODE-07 的即时自愈语义）；
//  2. 启动常驻巡检协程（每 reaperInterval 触发一次）；
//  3. 启动 N 个 Worker。
//
// 全部协程统一使用 Start 内派生的 ctx，并挂在同一个 WaitGroup 上，
// 保证 Shutdown 能一次性通知所有协程退出。
func (wp *WorkerPool) Start(parent context.Context) {
	wp.mu.Lock()
	if wp.isShutdown || wp.cancel != nil {
		wp.mu.Unlock()
		wp.log.Warn("WorkerPool.Start called on active/shutdown pool; ignoring")
		return
	}
	ctx, cancel := context.WithCancel(parent)
	wp.cancel = cancel
	wp.mu.Unlock()

	// 同步初检：Start 返回后即可断言首次自愈完成，测试无异步竞态。
	wp.scanStaleOnce()

	wp.wg.Add(1)
	go wp.reaperLoop(ctx)

	for i := 0; i < wp.capacity; i++ {
		wp.wg.Add(1)
		go wp.worker(ctx, i)
	}
}

// reaperLoop 周期执行来源租约巡检（常驻，直到 Shutdown 取消派生 ctx）。
func (wp *WorkerPool) reaperLoop(ctx context.Context) {
	defer wp.wg.Done()

	ticker := time.NewTicker(wp.reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			wp.scanStaleOnce()
		}
	}
}

// scanStaleOnce 执行一次跨节点租约巡检：
// 回收「同机旧实例残留 / 超 2h 未心跳 / worker_id 为空」的 running 任务。
// 终态写入使用 Background（与 Stage 4 一致，避免 Shutdown 时丢失失败归因）。
func (wp *WorkerPool) scanStaleOnce() {
	n, err := wp.jobs.MarkStaleRunningAsFailed(context.Background(), wp.pool,
		"worker_lease_expired", "stale running job reclaimed by reaper",
		wp.hostPrefix, wp.workerID)
	if err != nil {
		wp.log.Error("reaper scan failed", "err", err)
		return
	}
	if n > 0 {
		wp.log.Info("reaper scan reclaimed stale jobs", "reclaimed", n)
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

// Shutdown 优雅退出：取消派生 ctx 通知全部协程，再双通道等待 WaitGroup 归零。
// 双通道设计避免「wg.Wait 永久阻塞」导致停机死锁：ctx 超时即返回并告警。
func (wp *WorkerPool) Shutdown(ctx context.Context) {
	wp.mu.Lock()
	cancel := wp.cancel
	wp.cancel = nil
	wp.isShutdown = true
	wp.mu.Unlock()

	wp.stopOnce.Do(func() { close(wp.stopCh) })
	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		wp.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		wp.log.Info("worker pool shutdown complete")
	case <-ctx.Done():
		// 超时分支：wg.Wait 守护协程短暂存活至 wg 归零；生产用足够长的 ctx（如 30s）。
		wp.log.Warn("worker pool shutdown timeout, some jobs may be interrupted",
			"policy", "marked as failed on next startup scan",
			"err", ctx.Err())
	}
}
