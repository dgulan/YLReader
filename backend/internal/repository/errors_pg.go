package repository

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// isNoRows 判断错误是否为 pgx 的「无结果行」。
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// isUniqueViolation 判断错误是否为 PostgreSQL 唯一约束冲突（23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
