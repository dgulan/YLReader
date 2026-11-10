// Package testutil 提供 PostgreSQL 集成测试的公共夹具。
//
// 约定：
//   - 连接串来自 TEST_DATABASE_URL；未设置时调用方应 t.Skip（不让 CI 失败）；
//   - 每个测试用 pgx.Tx 开启事务并在结尾回滚，测试间零数据污染；
//   - TestPool 强制 MaxConns >= 4，避免「事务内写 + 事务外读验证」并发时死锁。
package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPool 返回指向 yreader_test 的连接池；TEST_DATABASE_URL 为空时 Skip。
func TestPool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		tb.Skip("TEST_DATABASE_URL 未设置，跳过 PostgreSQL 集成测试（先 make setup-test-db 并 export TEST_DATABASE_URL）")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		tb.Fatalf("解析 TEST_DATABASE_URL 失败: %v", err)
	}
	// 事务内写 + 事务外连接核对，需要至少 2 条连接；这里给足余量。
	if cfg.MaxConns < 4 {
		cfg.MaxConns = 4
	}
	cfg.MaxConnIdleTime = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		tb.Fatalf("创建连接池失败: %v", err)
	}
	tb.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		tb.Fatalf("连接 yreader_test 失败: %v", err)
	}
	return pool
}

// BeginTx 开启事务并注册回滚清理。
func BeginTx(tb testing.TB, pool *pgxpool.Pool) pgx.Tx {
	tb.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		tb.Fatalf("开启事务失败: %v", err)
	}
	tb.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// SeedUser 插入一个随机用户名的测试用户并返回 id。
func SeedUser(tb testing.TB, tx pgx.Tx) uuid.UUID {
	tb.Helper()
	var id uuid.UUID
	err := tx.QueryRow(context.Background(),
		`INSERT INTO users (username, password_hash) VALUES ($1, 'test') RETURNING id`,
		"u_"+uuid.NewString()).Scan(&id)
	if err != nil {
		tb.Fatalf("插入测试用户失败: %v", err)
	}
	return id
}
