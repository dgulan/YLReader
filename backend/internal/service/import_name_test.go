package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/pkg/textnorm"
	"yreader/backend/internal/repository"
	"yreader/backend/internal/repository/testutil"
)

// insertBookWithName 直接插入一本占位书，用于把 nextAvailableName 的候选名占满。
func insertBookWithName(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, baseName string, suffix int) {
	t.Helper()
	name := baseName
	if suffix > 0 {
		name = fmt.Sprintf("%s (%d)", baseName, suffix)
	}
	normName := textnorm.NormalizeBook(name)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO books (user_id, book_type, normalized_name, normalized_author, name, author)
		 VALUES ($1, 'local', $2, '', $3, '')`,
		userID, normName, name)
	if err != nil {
		t.Fatalf("insert book %q: %v", name, err)
	}
}

// TestNextAvailableNameExhausted 验证 nextAvailableName 在占满 (1)~(99) 后返回错误。
// 通过直接占用所有候选名达到耗尽，避免并发模拟的不确定性。
func TestNextAvailableNameExhausted(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedUserForServiceTest(t, pool)

	// 直接 INSERT 99 本 "耗尽测试 (n)" 占位，让 nextAvailableName 找不到可用名。
	for i := 1; i <= 99; i++ {
		insertBookWithName(t, pool, userID, "耗尽测试", i)
	}

	svc := NewImportService(pool, noopStorage{}, nil)
	_, _, err := svc.nextAvailableName(context.Background(), userID, "耗尽测试", "")
	if err == nil {
		t.Fatalf("nextAvailableName should exhaust after 99 attempts")
	}
	if !errors.Is(err, repository.ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}
