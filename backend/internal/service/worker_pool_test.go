//go:build integration

package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"yreader/backend/internal/repository/testutil"
)

// TestWorkerPoolMultiInstanceIsolation 覆盖 v1.3 / DEF-CODE-07：
// 启动自愈只能回收「本机旧实例 + 跨机超时 + 历史 NULL」三类，跨机且未超时的活跃任务必须保持 running。
func TestWorkerPoolMultiInstanceIsolation(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedUserForServiceTest(t, pool)
	wp := NewWorkerPool(pool, 1, nil)
	isolateWorkerIdentity(wp, "isolation")

	t.Cleanup(func() {
		wp.Shutdown(context.Background())
		_, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE user_id = $1`, userID)
	})

	ctx := context.Background()

	// 1. 远端健康实例：跨主机、10 分钟前开始 → 必须保持 running。
	remoteWorkerID := "remotehost-999-001"
	var jobA uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO jobs (user_id, type, status, worker_id, started_at)
		VALUES ($1, 'full_cache', 'running', $2, now() - interval '10 minutes')
		RETURNING id`, userID, remoteWorkerID).Scan(&jobA); err != nil {
		t.Fatalf("insert job A: %v", err)
	}

	// 2. 同机已崩溃实例：同主机前缀、1 分钟前开始 → 必须立即 failed。
	staleLocalWorkerID := wp.hostPrefix + "oldpid-002"
	var jobB uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO jobs (user_id, type, status, worker_id, started_at)
		VALUES ($1, 'full_cache', 'running', $2, now() - interval '1 minute')
		RETURNING id`, userID, staleLocalWorkerID).Scan(&jobB); err != nil {
		t.Fatalf("insert job B: %v", err)
	}

	// 3. 升级前历史残留：worker_id 为 NULL → 必须立即 failed。
	var jobC uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO jobs (user_id, type, status, worker_id, started_at)
		VALUES ($1, 'full_cache', 'running', NULL, now() - interval '1 minute')
		RETURNING id`, userID).Scan(&jobC); err != nil {
		t.Fatalf("insert job C: %v", err)
	}

	// 4. 触发启动自愈。
	wp.Start(ctx)

	// 5. 隔离断言（每处 Scan 均校验错误）。
	var statusA, statusB, statusC string
	if err := pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobA).Scan(&statusA); err != nil {
		t.Fatalf("scan job A: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobB).Scan(&statusB); err != nil {
		t.Fatalf("scan job B: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobC).Scan(&statusC); err != nil {
		t.Fatalf("scan job C: %v", err)
	}

	if statusA != "running" {
		t.Fatalf("job A (remote active instance) must remain running, got %s", statusA)
	}
	if statusB != "failed" {
		t.Fatalf("job B (stale local instance) must be failed immediately, got %s", statusB)
	}
	if statusC != "failed" {
		t.Fatalf("job C (legacy NULL worker_id) must be failed immediately, got %s", statusC)
	}
}
