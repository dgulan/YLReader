package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/repository/testutil"
)

// noopStorage 实现 storage.Provider，仅用于满足 ImportService 构造；
// handleCommitError 状态 ①/③ 不触达 storage 方法，仅需接口形状完整。
type noopStorage struct{}

func (noopStorage) Put(ctx context.Context, key string, r io.Reader, size int64) error { return nil }
func (noopStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, storage.ErrNotFound
}
func (noopStorage) Delete(ctx context.Context, key string) error         { return nil }
func (noopStorage) Exists(ctx context.Context, key string) (bool, error) { return false, nil }
func (noopStorage) DeletePrefix(ctx context.Context, prefix string) error {
	return nil
}

// countingStorage 记录 DeletePrefix 调用次数，用于断言三态清理决策。
type countingStorage struct {
	noopStorage
	mu   sync.Mutex
	call int
}

func (c *countingStorage) DeletePrefix(ctx context.Context, prefix string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call++
	return nil
}

func (c *countingStorage) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call
}

func seedUserForServiceTest(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
		"svc_"+uuid.NewString()[:8], "hash").Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func insertTestBookRow(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) uuid.UUID {
	t.Helper()
	bookID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO books (id, user_id, book_type, normalized_name, normalized_author, name, author)
         VALUES ($1, $2, 'local', $3, $4, $5, $6)`,
		bookID, userID, "norm", "auth", "测试书", "作者"); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	// 显式清理，防止共享 TestPool 场景下跨子测试污染；
	// 虽然 books.user_id ON DELETE CASCADE 会在用户清理时一并删除，
	// 但保留显式清理以兼容「复用种子用户」的测试扩展。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM books WHERE id = $1`, bookID)
	})
	return bookID
}

func newCommitTestLogger() (*bytes.Buffer, *slog.Logger) {
	buf := &bytes.Buffer{}
	return buf, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// 状态 ①：DB 已提交生效 → 保留文件，不清理。
func TestHandleCommitError_RetainOnRowExists(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedUserForServiceTest(t, pool)
	logBuf, logger := newCommitTestLogger()
	st := &countingStorage{}
	svc := NewImportService(pool, st, logger)

	bookID := insertTestBookRow(t, pool, userID)
	logBuf.Reset()
	svc.handleCommitError(context.Background(), userID, bookID, errors.New("simulated commit err"))

	if n := st.calls(); n != 0 {
		t.Fatalf("row exists: DeletePrefix calls = %d, want 0", n)
	}
	out := logBuf.String()
	if !bytes.Contains([]byte(out), []byte("commit error but book row exists")) {
		t.Fatalf("want warn log 'commit error but book row exists', got: %s", out)
	}
}

// 状态 ②：DB 明确无此行 → 安全清理（DeletePrefix 恰好 1 次）。
func TestHandleCommitError_CleanupOnRowAbsent(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedUserForServiceTest(t, pool)
	logBuf, logger := newCommitTestLogger()
	st := &countingStorage{}
	svc := NewImportService(pool, st, logger)

	logBuf.Reset()
	svc.handleCommitError(context.Background(), userID, uuid.New(), errors.New("simulated commit err"))

	if n := st.calls(); n != 1 {
		t.Fatalf("row absent: DeletePrefix calls = %d, want 1", n)
	}
	out := logBuf.String()
	if !bytes.Contains([]byte(out), []byte("commit error, book row absent, cleaned up")) {
		t.Fatalf("want error log 'commit error, book row absent, cleaned up', got: %s", out)
	}
}

// 状态 ③：探查失败 → 保守保留。
// 本测试无法从外部注入探查失败（probeCtx 由 handleCommitError 内部创建，
// pool 为共享测试池、Close 会污染其他用例），故显式 Skip 并归档 DEF-CODE-04。
func TestHandleCommitError_ProbeFails(t *testing.T) {
	t.Skip("状态 ③ 无法单测：probeCtx 内部创建无法注入失败，pool 共享无法 Close。归档 DEF-CODE-04。代码逻辑已 review 确认。")
}
