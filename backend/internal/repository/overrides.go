package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OverrideRepository 负责 chapter_source_overrides（章节级源覆盖）的读写。
// 该表含 user_id 列，查询直接按 o.user_id 过滤并命中
// ix_chapter_overrides_user_book 复合索引，无需 JOIN books。
type OverrideRepository struct{}

// NewOverrideRepository 构造覆盖仓库。
func NewOverrideRepository() *OverrideRepository { return &OverrideRepository{} }

const overrideColumns = `id, user_id, book_id, chapter_index, source_id, chapter_url,
	chapter_title, normalized_title, status, created_at, updated_at`

func scanOverride(row pgx.Row) (*ChapterSourceOverride, error) {
	var o ChapterSourceOverride
	err := row.Scan(
		&o.ID, &o.UserID, &o.BookID, &o.ChapterIndex, &o.SourceID, &o.ChapterURL,
		&o.ChapterTitle, &o.NormalizedTitle, &o.Status, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &o, nil
}

// Upsert 写入单章覆盖；冲突时更新源/URL/标题并显式重置 status='active'。
func (r *OverrideRepository) Upsert(ctx context.Context, q Querier, o *ChapterSourceOverride) error {
	if o.Status == "" {
		o.Status = OverrideStatusActive
	}
	err := q.QueryRow(ctx, `
		INSERT INTO chapter_source_overrides (user_id, book_id, chapter_index, source_id, chapter_url,
			chapter_title, normalized_title, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'active')
		ON CONFLICT (book_id, chapter_index) DO UPDATE SET
			source_id = EXCLUDED.source_id,
			chapter_url = EXCLUDED.chapter_url,
			chapter_title = EXCLUDED.chapter_title,
			normalized_title = EXCLUDED.normalized_title,
			status = 'active'
		RETURNING id, status, created_at, updated_at`,
		o.UserID, o.BookID, o.ChapterIndex, o.SourceID, o.ChapterURL,
		o.ChapterTitle, o.NormalizedTitle,
	).Scan(&o.ID, &o.Status, &o.CreatedAt, &o.UpdatedAt)
	return err
}

// Get 读取单章覆盖（user_id + book_id + chapter_index）。
func (r *OverrideRepository) Get(ctx context.Context, q Querier, userID, bookID uuid.UUID, chapterIndex int32) (*ChapterSourceOverride, error) {
	return scanOverride(q.QueryRow(ctx, `SELECT `+overrideColumns+`
		FROM chapter_source_overrides
		WHERE user_id = $1 AND book_id = $2 AND chapter_index = $3`,
		userID, bookID, chapterIndex))
}

// ListByBook 列出某本书的全部覆盖。
func (r *OverrideRepository) ListByBook(ctx context.Context, q Querier, userID, bookID uuid.UUID) ([]*ChapterSourceOverride, error) {
	rows, err := q.Query(ctx, `SELECT `+overrideColumns+`
		FROM chapter_source_overrides
		WHERE user_id = $1 AND book_id = $2 ORDER BY chapter_index`, userID, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*ChapterSourceOverride, 0, 16)
	for rows.Next() {
		o, err := scanOverride(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Delete 删除单章覆盖（重置）。
func (r *OverrideRepository) Delete(ctx context.Context, q Querier, userID, bookID uuid.UUID, chapterIndex int32) error {
	tag, err := q.Exec(ctx, `DELETE FROM chapter_source_overrides
		WHERE user_id = $1 AND book_id = $2 AND chapter_index = $3`, userID, bookID, chapterIndex)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteBookOverrides 清空整本书的覆盖（整本换源时在同一事务内调用，见 §4.6.4）。
func (r *OverrideRepository) DeleteBookOverrides(ctx context.Context, q Querier, userID, bookID uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM chapter_source_overrides WHERE user_id = $1 AND book_id = $2`,
		userID, bookID)
	return err
}

// MarkStaleFrom 把 chapter_index >= from 的覆盖标记为 stale（目录刷新越界时调用）。
func (r *OverrideRepository) MarkStaleFrom(ctx context.Context, q Querier, userID, bookID uuid.UUID, from int32) error {
	_, err := q.Exec(ctx, `UPDATE chapter_source_overrides SET status = 'stale'
		WHERE user_id = $1 AND book_id = $2 AND chapter_index >= $3 AND status = 'active'`,
		userID, bookID, from)
	return err
}
