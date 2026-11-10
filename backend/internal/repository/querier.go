// Package repository 提供纯数据访问层。
//
// 事务边界约定：Repository 内部绝不调用 pool.Begin()。所有方法都接收
// Querier（*pgxpool.Pool 或 pgx.Tx 均满足），由调用方决定事务范围。
package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier 是 Repository 的统一数据库访问抽象。*pgxpool.Pool 与 pgx.Tx
// 均原生满足该接口，因此同一套 SQL 既能跑在池上也能跑在事务里。
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

var (
	// ErrNotFound 表示按主键/唯一键未找到记录。
	ErrNotFound = errors.New("repository: not found")
	// ErrVersionConflict 表示乐观锁版本不匹配（影响行数为 0）。
	ErrVersionConflict = errors.New("repository: version conflict")
	// ErrDuplicate 表示违反唯一约束（例如同一用户重复的书源 URL）。
	ErrDuplicate = errors.New("repository: duplicate")
	// ErrInvalidArgument 表示入参不满足契约（例如同时缺少 chapter_index 与 chapter_id）。
	ErrInvalidArgument = errors.New("repository: invalid argument")
)
