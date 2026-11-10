package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ProgressRepository 负责 reading_progress 的读写。
// 注意：reading_progress.version 由应用层显式维护（触发器仅刷 server_updated_at）。
type ProgressRepository struct{}

// NewProgressRepository 构造进度仓库。
func NewProgressRepository() *ProgressRepository { return &ProgressRepository{} }

const progressColumns = `id, user_id, book_id, chapter_index, char_offset, chapter_title,
	normalized_title, chapter_ratio::float8, progress_percent::float8,
	client_updated_at, server_updated_at, version, device_id`

func scanProgress(row pgx.Row) (*ReadingProgress, error) {
	var p ReadingProgress
	err := row.Scan(
		&p.ID, &p.UserID, &p.BookID, &p.ChapterIndex, &p.CharOffset, &p.ChapterTitle,
		&p.NormalizedTitle, &p.ChapterRatio, &p.ProgressPercent,
		&p.ClientUpdatedAt, &p.ServerUpdatedAt, &p.Version, &p.DeviceID,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// Get 读取阅读进度；无记录时返回零值并置 Version=1（对齐 OpenAPI「无记录返回 0 值」）。
func (r *ProgressRepository) Get(ctx context.Context, q Querier, userID, bookID uuid.UUID) (*ReadingProgress, error) {
	p, err := scanProgress(q.QueryRow(ctx, `SELECT `+progressColumns+`
		FROM reading_progress WHERE user_id = $1 AND book_id = $2`, userID, bookID))
	if err == ErrNotFound {
		return &ReadingProgress{UserID: userID, BookID: bookID, Version: 1}, nil
	}
	return p, err
}

// Update 以带版本检查的 Upsert 写入进度：//   - 无记录：插入 version=1；
//   - 有记录：仅当 version = expectedVersion 时自增并更新；
//   - 影响行数为 0（版本不匹配或记录不存在）时返回 ErrVersionConflict。
func (r *ProgressRepository) Update(ctx context.Context, q Querier, p *ReadingProgress, expectedVersion int64) error {
	if p.ProgressPercent == 0 && p.ChapterRatio > 0 {
		p.ProgressPercent = p.ChapterRatio * 100
	}
	err := q.QueryRow(ctx, `
		INSERT INTO reading_progress (
			user_id, book_id, chapter_index, char_offset, chapter_title, normalized_title,
			chapter_ratio, progress_percent, client_updated_at, version, device_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7::float8::numeric(6,5),$8::float8::numeric(5,2),$9,1,$10)
		ON CONFLICT (user_id, book_id) DO UPDATE SET
			chapter_index = EXCLUDED.chapter_index,
			char_offset = EXCLUDED.char_offset,
			chapter_title = EXCLUDED.chapter_title,
			normalized_title = EXCLUDED.normalized_title,
			chapter_ratio = EXCLUDED.chapter_ratio,
			progress_percent = EXCLUDED.progress_percent,
			client_updated_at = EXCLUDED.client_updated_at,
			device_id = EXCLUDED.device_id,
			version = reading_progress.version + 1
		WHERE reading_progress.version = $11
		RETURNING version, server_updated_at`,
		p.UserID, p.BookID, p.ChapterIndex, p.CharOffset, p.ChapterTitle, p.NormalizedTitle,
		p.ChapterRatio, p.ProgressPercent, p.ClientUpdatedAt, p.DeviceID, expectedVersion,
	).Scan(&p.Version, &p.ServerUpdatedAt)
	if err == pgx.ErrNoRows {
		return ErrVersionConflict
	}
	return err
}

// Exists 判断某用户某书是否已有持久化进度记录（用于区分「合成 version=1」与真实记录）。
func (r *ProgressRepository) Exists(ctx context.Context, q Querier, userID, bookID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reading_progress WHERE user_id = $1 AND book_id = $2)`, userID, bookID).Scan(&ok)
	return ok, err
}

// InsertInitial 为「客户端已看到合成 version=1」的首次 PUT 落库。
// 语义：无记录时插入 version=2（客户端基于 version=1 完成一次推进）；
// 并发下若记录刚被创建且仍为 version=1，则按乐观锁自增到 2；其余版本冲突。
func (r *ProgressRepository) InsertInitial(ctx context.Context, q Querier, p *ReadingProgress) error {
	if p.ProgressPercent == 0 && p.ChapterRatio > 0 {
		p.ProgressPercent = p.ChapterRatio * 100
	}
	err := q.QueryRow(ctx, `
		INSERT INTO reading_progress (
			user_id, book_id, chapter_index, char_offset, chapter_title, normalized_title,
			chapter_ratio, progress_percent, client_updated_at, version, device_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7::float8::numeric(6,5),$8::float8::numeric(5,2),$9,2,$10)
		ON CONFLICT (user_id, book_id) DO UPDATE SET
			chapter_index = EXCLUDED.chapter_index,
			char_offset = EXCLUDED.char_offset,
			chapter_title = EXCLUDED.chapter_title,
			normalized_title = EXCLUDED.normalized_title,
			chapter_ratio = EXCLUDED.chapter_ratio,
			progress_percent = EXCLUDED.progress_percent,
			client_updated_at = EXCLUDED.client_updated_at,
			device_id = EXCLUDED.device_id,
			version = reading_progress.version + 1
		WHERE reading_progress.version = 1
		RETURNING version, server_updated_at`,
		p.UserID, p.BookID, p.ChapterIndex, p.CharOffset, p.ChapterTitle, p.NormalizedTitle,
		p.ChapterRatio, p.ProgressPercent, p.ClientUpdatedAt, p.DeviceID,
	).Scan(&p.Version, &p.ServerUpdatedAt)
	if err == pgx.ErrNoRows {
		return ErrVersionConflict
	}
	return err
}
