package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/pkg/crypto"
)

// pendingHeaderWhere 选中「非空且尚未加密」的 header：
// 空对象（默认值）无需加密；含 __enc 的行说明已迁移完成（幂等/断点续跑依据）。
const pendingHeaderWhere = `header IS NOT NULL
	AND btrim(header::text) <> '{}'
	AND NOT (header ? '` + crypto.EnvelopeKey + `')`

// SourceHeaderProgress 在每批完成后回调（迁移工具用于日志/进度）。
type SourceHeaderProgress func(migrated int, lastID uuid.UUID)

// MigrateSourceHeaders 把历史明文 header 分批加密为 v1.4 信封（ADR-010）。
//
// 语义（契约 v1.4）：
//   - 按 id 游标分批，每批独立事务；batch<=0 视为 1000；
//   - 只处理非空且未加密的行，因此重跑幂等、失败可断点续跑；
//   - dryRun 只统计待迁移行数，不写库；
//   - 返回值是本次实际（或 dry-run 下「将要」）迁移的行数。
func MigrateSourceHeaders(ctx context.Context, pool *pgxpool.Pool, ring *crypto.Keyring, batch int, dryRun bool, progress SourceHeaderProgress) (int, error) {
	if ring == nil {
		return 0, crypto.ErrNoKey
	}
	if batch <= 0 {
		batch = 1000
	}
	type row struct {
		ID     uuid.UUID
		UserID uuid.UUID
		Header json.RawMessage
	}
	var cursor uuid.UUID
	total := 0
	for {
		var rows []row
		err := func() error {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)

			rs, err := tx.Query(ctx, `
				SELECT id, user_id, header FROM book_sources
				WHERE id > $1 AND `+pendingHeaderWhere+`
				ORDER BY id LIMIT $2`, cursor, batch)
			if err != nil {
				return err
			}
			for rs.Next() {
				var r row
				if err := rs.Scan(&r.ID, &r.UserID, &r.Header); err != nil {
					rs.Close()
					return err
				}
				rows = append(rows, r)
			}
			rs.Close()
			if err := rs.Err(); err != nil {
				return err
			}
			if dryRun || len(rows) == 0 {
				return nil
			}
			for _, r := range rows {
				sealed, err := ring.Seal(r.Header, crypto.AAD(r.UserID, r.ID))
				if err != nil {
					return fmt.Errorf("seal source %s: %w", r.ID, err)
				}
				wrapped, err := json.Marshal(map[string]string{crypto.EnvelopeKey: sealed})
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE book_sources SET header = $2 WHERE id = $1`, r.ID, wrapped); err != nil {
					return fmt.Errorf("update source %s: %w", r.ID, err)
				}
			}
			return tx.Commit(ctx)
		}()
		if err != nil {
			return total, err
		}
		if len(rows) == 0 {
			return total, nil
		}
		total += len(rows)
		cursor = rows[len(rows)-1].ID
		if progress != nil {
			progress(total, cursor)
		}
		if len(rows) < batch {
			return total, nil
		}
	}
}
