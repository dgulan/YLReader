package repository

import (
	"context"
	"fmt"
	"yreader/backend/internal/migration"
)

// CheckSchema verifies the v1.5 bookshelf shape without reading user rows or
// executing DDL. It protects existing volumes that initdb will not upgrade.
func CheckSchema(ctx context.Context, q Querier) error {
	rows, err := q.Query(ctx, `SELECT id, progress_char_offset, progress_version FROM v_bookshelf LIMIT 0`)
	if err != nil {
		return fmt.Errorf("schema requirement bookshelf_progress_v1_5 is missing; apply the reviewed schema before startup: %w", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return migration.Check(ctx, q)
}
