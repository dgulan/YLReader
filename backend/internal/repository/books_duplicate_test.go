package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// failingRow 模拟 INSERT 命中唯一约束时返回的错误行。
type failingRow struct{ err error }

func (r failingRow) Scan(dest ...any) error { return r.err }

// conflictQuerier 让 QueryRow 直接返回一个 23505 错误，用于验证多 %w 包装。
type conflictQuerier struct{ err error }

func (q conflictQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, q.err
}

func (q conflictQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return failingRow{err: q.err}
}

func (q conflictQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, q.err
}

// TestBookCreateMultiWrappedDuplicate 验证 Create 的「多 %w」包装：
// 调用方既能 errors.Is(err, ErrDuplicate)，也能 errors.As(err, *pgconn.PgError)
// 拿到具体约束名（ImportBook 依赖后者区分 file_hash 与 name_author 冲突）。
func TestBookCreateMultiWrappedDuplicate(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "uq_books_user_file_hash"}
	repo := NewBookRepository()
	err := repo.Create(context.Background(), conflictQuerier{err: pgErr}, &Book{
		UserID:         uuid.New(),
		Name:           "dup",
		NormalizedName: "dup",
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("errors.Is(err, ErrDuplicate) = false: %v", err)
	}
	var got *pgconn.PgError
	if !errors.As(err, &got) || got.ConstraintName != "uq_books_user_file_hash" {
		t.Fatalf("errors.As(*pgconn.PgError) failed or wrong constraint: %v", err)
	}
}

// TestBookCreateNonDuplicatePassthrough 验证非 23505 错误原样返回，不被包装成 ErrDuplicate。
func TestBookCreateNonDuplicatePassthrough(t *testing.T) {
	sentinel := errors.New("connection reset")
	repo := NewBookRepository()
	err := repo.Create(context.Background(), conflictQuerier{err: sentinel}, &Book{
		UserID:         uuid.New(),
		Name:           "x",
		NormalizedName: "x",
	})
	if errors.Is(err, ErrDuplicate) {
		t.Fatalf("unexpected ErrDuplicate: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("underlying error lost: %v", err)
	}
}
