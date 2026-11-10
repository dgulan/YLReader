//go:build integration

package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/goleak"

	"yreader/backend/internal/repository/testutil"
)

// TestMain 以 goleak 校验整个 service 包无协程泄漏。
// 白名单理由：pgxpool 的后台健康检查协程随连接池常驻，属测试基础设施而非 WorkerPool 泄漏
// （详见 docs/tech-debt.md DEF-CODE-08 白名单登记；函数名以实测堆栈为准）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).triggerHealthCheck"),
	)
}

// isolateWorkerIdentity 给测试池分配独立的「主机前缀 + 实例标识」。
// 目的：`go test ./...` 会并行启动多个包进程（api/service 等），它们共享同一台机器与
// 同一个测试库；若沿用真实 hostname 前缀，任一池的启动巡检都会把另一进程的正常 running
// 任务当作「同机旧实例」回收，造成跨包误杀。生产环境仍使用真实 hostname（本函数仅测试用）。
func isolateWorkerIdentity(wp *WorkerPool, tag string) {
	suffix := fmt.Sprintf("%s-%d-%d", tag, os.Getpid(), time.Now().UnixNano())
	wp.hostPrefix = "testhost-" + suffix + "-"
	wp.workerID = wp.hostPrefix + "self"
}

// insertReaperJob 插入一条 running 任务，用于巡检用例。
// startedAgo 控制 started_at，workerID 为 nil 时写入 NULL（历史残留任务）。
func insertReaperJob(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, workerID *string, startedAgo time.Duration) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO jobs (user_id, type, status, payload, started_at, worker_id)
		VALUES ($1, 'full_cache', 'running', '{}'::jsonb, now() - $2::interval, $3)
		RETURNING id`, userID, startedAgo.String(), workerID).Scan(&id)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, id)
	})
	return id
}

func jobStatusAndCode(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (string, string) {
	t.Helper()
	var status string
	var code *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, error_code FROM jobs WHERE id = $1`, id).Scan(&status, &code); err != nil {
		t.Fatalf("query job: %v", err)
	}
	if code == nil {
		return status, ""
	}
	return status, *code
}

// TestWorkerReaperLoop_StartupScanImmediate 验证 Start 内的同步初检：
// worker_id IS NULL 的历史残留任务在 Start 返回时即被置为 failed（不依赖 Ticker）。
func TestWorkerReaperLoop_StartupScanImmediate(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedUserForServiceTest(t, pool)

	id := insertReaperJob(t, pool, userID, nil, 1*time.Minute)

	wp := NewWorkerPool(pool, 1, nil)
	isolateWorkerIdentity(wp, "startup")
	wp.Start(context.Background())
	t.Cleanup(func() { wp.Shutdown(context.Background()) })

	status, code := jobStatusAndCode(t, pool, id)
	if status != "failed" {
		t.Fatalf("job status = %q, want failed (startup scan)", status)
	}
	if code != "worker_lease_expired" {
		t.Fatalf("error_code = %q, want worker_lease_expired", code)
	}
}

// TestWorkerReaperLoop_ReclaimsCrossNodeStale 验证 Ticker 巡检路径：
// 先 Start（此时无 2h+ 任务，初检扫不到），再插入跨节点超时任务，
// 由 100ms 周期巡检回收（真实验证 reaperLoop 而非 scanStaleOnce）。
func TestWorkerReaperLoop_ReclaimsCrossNodeStale(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedUserForServiceTest(t, pool)

	wp := NewWorkerPool(pool, 1, nil).WithReaperInterval(100 * time.Millisecond)
	isolateWorkerIdentity(wp, "ticker")
	wp.Start(context.Background())
	t.Cleanup(func() { wp.Shutdown(context.Background()) })

	other := "other-host-stale-12345"
	id := insertReaperJob(t, pool, userID, &other, 3*time.Hour)

	deadline := time.Now().Add(5 * time.Second)
	for {
		status, code := jobStatusAndCode(t, pool, id)
		if status == "failed" {
			if code != "worker_lease_expired" {
				t.Fatalf("error_code = %q, want worker_lease_expired", code)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cross-node stale job not reclaimed within 5s (status=%q)", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWorkerReaperLoop_DoesNotTouchActiveCrossNode 验证跨节点存活任务不被误杀：
// worker_id 非本机前缀、非当前实例、非 NULL，且未超 2h 租约上限 → 5 个巡检周期后仍为 running。
func TestWorkerReaperLoop_DoesNotTouchActiveCrossNode(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedUserForServiceTest(t, pool)

	other := "other-host-running-12345"
	id := insertReaperJob(t, pool, userID, &other, 1*time.Hour)

	wp := NewWorkerPool(pool, 1, nil).WithReaperInterval(100 * time.Millisecond)
	isolateWorkerIdentity(wp, "ticker")
	wp.Start(context.Background())
	t.Cleanup(func() { wp.Shutdown(context.Background()) })

	time.Sleep(500 * time.Millisecond) // 覆盖约 5 个巡检周期

	status, _ := jobStatusAndCode(t, pool, id)
	if status != "running" {
		t.Fatalf("active cross-node job status = %q, want running (must not be reclaimed)", status)
	}
}

// TestWorkerReaperLoop_NoGoroutineLeak 验证 Shutdown 无阻塞、无残留：
// 双通道等待返回后 isShutdown=true；退出后的协程归零由 TestMain 的 goleak 统一校验。
func TestWorkerReaperLoop_NoGoroutineLeak(t *testing.T) {
	pool := testutil.TestPool(t)

	wp := NewWorkerPool(pool, 2, nil).WithReaperInterval(50 * time.Millisecond)
	isolateWorkerIdentity(wp, "leak")
	wp.Start(context.Background())
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		wp.Shutdown(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown blocked longer than 5s")
	}

	wp.mu.Lock()
	shutdown := wp.isShutdown
	wp.mu.Unlock()
	if !shutdown {
		t.Fatal("isShutdown = false after Shutdown")
	}
}
