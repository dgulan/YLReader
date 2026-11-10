package repository

import (
	"context"

	"github.com/google/uuid"
)

// ensureBookOwned 校验 book 存在且属于 userID；不满足时返回 ErrNotFound。
// 供 chapters / mappings 等无 user_id 表的写操作做租户守护。
func ensureBookOwned(ctx context.Context, q Querier, userID, bookID uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM books WHERE id = $1 AND user_id = $2)`,
		bookID, userID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}
