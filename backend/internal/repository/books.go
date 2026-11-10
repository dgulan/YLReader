package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BookRepository 负责 books（逻辑书）的读写。
type BookRepository struct{}

// NewBookRepository 构造书籍仓库。
func NewBookRepository() *BookRepository { return &BookRepository{} }

const bookColumns = `id, user_id, book_type, normalized_name, normalized_author, name, author,
	cover_url, custom_cover_url, intro, kind, word_count, status, active_source_id,
	active_book_url, total_chapters, latest_chapter_title, latest_chapter_at, last_read_at,
	version, created_at, updated_at, file_hash`

func scanBook(row pgx.Row) (*Book, error) {
	var b Book
	err := row.Scan(
		&b.ID, &b.UserID, &b.BookType, &b.NormalizedName, &b.NormalizedAuthor, &b.Name, &b.Author,
		&b.CoverURL, &b.CustomCoverURL, &b.Intro, &b.Kind, &b.WordCount, &b.Status, &b.ActiveSourceID,
		&b.ActiveBookURL, &b.TotalChapters, &b.LatestChapterTitle, &b.LatestChapterAt, &b.LastReadAt,
		&b.Version, &b.CreatedAt, &b.UpdatedAt, &b.FileHash,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

// Create 插入一本逻辑书。normalized_* 由调用方（service）用 pkg/textnorm 预先算好。
// 预分配 b.ID（非 uuid.Nil）时按该 ID 落库，便于服务层先把书 ID 用于对象存储路径。
func (r *BookRepository) Create(ctx context.Context, q Querier, b *Book) error {
	if b.BookType == "" {
		b.BookType = BookTypeOnline
	}
	if b.Status == "" {
		b.Status = BookStatusUnknown
	}
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	err := q.QueryRow(ctx, `
		INSERT INTO books (
			id, user_id, book_type, normalized_name, normalized_author, name, author,
			cover_url, custom_cover_url, intro, kind, word_count, status,
			active_source_id, active_book_url, total_chapters, latest_chapter_title,
			latest_chapter_at, last_read_at, file_hash
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		RETURNING version, created_at, updated_at`,
		b.ID, b.UserID, b.BookType, b.NormalizedName, b.NormalizedAuthor, b.Name, b.Author,
		b.CoverURL, b.CustomCoverURL, b.Intro, b.Kind, b.WordCount, b.Status,
		b.ActiveSourceID, b.ActiveBookURL, b.TotalChapters, b.LatestChapterTitle,
		b.LatestChapterAt, b.LastReadAt, b.FileHash,
	).Scan(&b.Version, &b.CreatedAt, &b.UpdatedAt)
	if isUniqueViolation(err) {
		// 多 %w：调用方既能 errors.Is(ErrDuplicate)，也能 errors.As(*pgconn.PgError) 判断具体约束。
		return fmt.Errorf("%w: %w", ErrDuplicate, err)
	}
	return err
}

// GetByFileHash 按用户 + 文件 SHA-256 查找已导入的本地书。
func (r *BookRepository) GetByFileHash(ctx context.Context, q Querier, userID uuid.UUID, fileHash string) (*Book, error) {
	return scanBook(q.QueryRow(ctx, `SELECT `+bookColumns+`
		FROM books WHERE user_id = $1 AND file_hash = $2`, userID, fileHash))
}

// Get 按用户 + 主键读取逻辑书。
func (r *BookRepository) Get(ctx context.Context, q Querier, userID, bookID uuid.UUID) (*Book, error) {
	return scanBook(q.QueryRow(ctx, `SELECT `+bookColumns+`
		FROM books WHERE user_id = $1 AND id = $2`, userID, bookID))
}

// FindByNormalized 按 (user_id, normalized_name, normalized_author) 读取逻辑书。
func (r *BookRepository) FindByNormalized(ctx context.Context, q Querier, userID uuid.UUID, normalizedName, normalizedAuthor string) (*Book, error) {
	return scanBook(q.QueryRow(ctx, `SELECT `+bookColumns+`
		FROM books WHERE user_id = $1 AND normalized_name = $2 AND normalized_author = $3`,
		userID, normalizedName, normalizedAuthor))
}

// UpdateActiveSource 在调用方事务内完成换源的四步流转：
//
//  1. 更新 books.active_source_id + active_book_url；
//  2. 目标源 B 的 mapping 置为 active（不存在则新建）；
//  3. 原源 A 的 active mapping 置为 stale（保留 URL/目录信息）；
//  4. 其他备用源 mapping 保持不变。
//
// 重要：本方法要求 q 为 pgx.Tx；传 *pgxpool.Pool 会导致四步非原子。
// 另外，v1.1 契约 §4.6.4 规定「整本书换源需在同一事务内清空
// chapter_source_overrides」；调用方应在同一 tx 中额外调用
// OverrideRepository.DeleteBookOverrides。
func (r *BookRepository) UpdateActiveSource(ctx context.Context, q Querier, userID, bookID, newSourceID uuid.UUID, newBookURL string) error {
	// 0. 锁定并读取当前 active source，作为「原源 A」。
	var oldSourceID *uuid.UUID
	err := q.QueryRow(ctx, `SELECT active_source_id FROM books WHERE user_id = $1 AND id = $2 FOR UPDATE`,
		userID, bookID).Scan(&oldSourceID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}

	// 1. 更新逻辑书的活动源。
	tag, err := q.Exec(ctx, `UPDATE books SET active_source_id = $3, active_book_url = $4
		WHERE user_id = $1 AND id = $2`, userID, bookID, newSourceID, newBookURL)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	// 2. 目标源 B 的 mapping 置 active（按 (book_id, source_id, book_url) 定位，不存在则新建）。
	tag, err = q.Exec(ctx, `UPDATE book_source_mappings SET sync_status = 'active'
		WHERE book_id = $1 AND source_id = $2 AND book_url = $3`, bookID, newSourceID, newBookURL)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := q.Exec(ctx, `INSERT INTO book_source_mappings (book_id, source_id, book_url, sync_status)
			VALUES ($1, $2, $3, 'active')`, bookID, newSourceID, newBookURL); err != nil {
			return err
		}
	}

	// 3. 原源 A 的 active mapping 置 stale。
	if oldSourceID != nil && *oldSourceID != newSourceID {
		if _, err := q.Exec(ctx, `UPDATE book_source_mappings SET sync_status = 'stale'
			WHERE book_id = $1 AND source_id = $2 AND sync_status = 'active'`,
			bookID, *oldSourceID); err != nil {
			return err
		}
	}
	return nil
}

// BookPatch 是 PATCH /books/{id} 的可选字段集合。字段为 nil 表示保留旧值，
// 为 &"" 表示清空。
type BookPatch struct {
	Name           *string
	Author         *string
	CustomCoverURL *string
	Intro          *string
	Status         *BookStatus
}

// UpdateMeta 按 version 乐观锁更新书籍元数据。version 由 bump_row_version 触发器维护，
// 这里绝不写 SET version = version + 1。
// 返回 ErrNotFound（书不存在）/ ErrVersionConflict（版本不一致）。
func (r *BookRepository) UpdateMeta(ctx context.Context, q Querier, userID, bookID uuid.UUID, p BookPatch, expectedVersion int64) (*Book, error) {
	var status *string
	if p.Status != nil {
		s := string(*p.Status)
		status = &s
	}
	var version int64
	var updatedAt time.Time
	err := q.QueryRow(ctx, `
		UPDATE books SET
			name = COALESCE($3, name),
			author = COALESCE($4, author),
			custom_cover_url = COALESCE($5, custom_cover_url),
			intro = COALESCE($6, intro),
			status = COALESCE($7::book_status, status)
		WHERE id = $1 AND user_id = $2 AND version = $8
		RETURNING version, updated_at`,
		bookID, userID, p.Name, p.Author, p.CustomCoverURL, p.Intro, status, expectedVersion,
	).Scan(&version, &updatedAt)
	if err == nil {
		b, gerr := r.Get(ctx, q, userID, bookID)
		if gerr != nil {
			return nil, gerr
		}
		return b, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	// 未命中：区分「书不存在(404)」与「版本不一致(409)」。
	var cur int64
	if serr := q.QueryRow(ctx, `SELECT version FROM books WHERE id = $1 AND user_id = $2`, bookID, userID).Scan(&cur); serr != nil {
		if serr == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, serr
	}
	return nil, ErrVersionConflict
}

// TouchLastRead 刷新 books.last_read_at（进度写入后调用）。
func (r *BookRepository) TouchLastRead(ctx context.Context, q Querier, userID, bookID uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE books SET last_read_at = now() WHERE id = $1 AND user_id = $2`, bookID, userID)
	return err
}

// Delete 删除逻辑书；不存在返回 ErrNotFound（外键级联清理 mappings/overrides/progress）。
func (r *BookRepository) Delete(ctx context.Context, q Querier, userID, bookID uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM books WHERE id = $1 AND user_id = $2`, bookID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MappingRepository 负责 book_source_mappings 的读写。该表没有 user_id 列，
// 读写均通过 JOIN books / ensureBookOwned 实施租户隔离。
type MappingRepository struct{}

// NewMappingRepository 构造源绑定仓库。
func NewMappingRepository() *MappingRepository { return &MappingRepository{} }

const mappingColumns = `m.id, m.book_id, m.source_id, m.book_url, m.toc_url, m.name, m.author, m.cover_url,
	m.latest_chapter_title, m.total_chapters, m.sync_status, m.failure_count, m.last_synced_at, m.created_at, m.updated_at`

func scanMapping(row pgx.Row) (*BookSourceMapping, error) {
	var m BookSourceMapping
	err := row.Scan(
		&m.ID, &m.BookID, &m.SourceID, &m.BookURL, &m.TOCURL, &m.Name, &m.Author, &m.CoverURL,
		&m.LatestChapterTitle, &m.TotalChapters, &m.SyncStatus, &m.FailureCount, &m.LastSyncedAt,
		&m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// Get 按 (book_id, source_id, book_url) 读取一条绑定（限当前用户）。
func (r *MappingRepository) Get(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID, bookURL string) (*BookSourceMapping, error) {
	return scanMapping(q.QueryRow(ctx, `SELECT `+mappingColumns+`
		FROM book_source_mappings m
		JOIN books b ON b.id = m.book_id AND b.user_id = $1
		WHERE m.book_id = $2 AND m.source_id = $3 AND m.book_url = $4`,
		userID, bookID, sourceID, bookURL))
}

// GetBySourceID 读取某用户某本书在某源下的任意一条绑定（active 优先，其次新近）。
func (r *MappingRepository) GetBySourceID(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID) (*BookSourceMapping, error) {
	return scanMapping(q.QueryRow(ctx, `SELECT `+mappingColumns+`
		FROM book_source_mappings m
		JOIN books b ON b.id = m.book_id AND b.user_id = $1
		WHERE m.book_id = $2 AND m.source_id = $3
		ORDER BY (m.sync_status = 'active') DESC, m.updated_at DESC
		LIMIT 1`, userID, bookID, sourceID))
}

// GetActive 读取某用户某本书某源的 active 绑定。
func (r *MappingRepository) GetActive(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID) (*BookSourceMapping, error) {
	return scanMapping(q.QueryRow(ctx, `SELECT `+mappingColumns+`
		FROM book_source_mappings m
		JOIN books b ON b.id = m.book_id AND b.user_id = $1
		WHERE m.book_id = $2 AND m.source_id = $3 AND m.sync_status = 'active'`,
		userID, bookID, sourceID))
}

// Upsert 按 (book_id, source_id, book_url) 插入或更新一条绑定（先校验书籍归属）。
func (r *MappingRepository) Upsert(ctx context.Context, q Querier, userID uuid.UUID, m *BookSourceMapping) error {
	if m.SyncStatus == "" {
		m.SyncStatus = MappingStatusPending
	}
	if err := ensureBookOwned(ctx, q, userID, m.BookID); err != nil {
		return err
	}
	err := q.QueryRow(ctx, `
		INSERT INTO book_source_mappings (book_id, source_id, book_url, toc_url, name, author, cover_url,
			latest_chapter_title, total_chapters, sync_status, failure_count, last_synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (book_id, source_id, book_url) DO UPDATE SET
			toc_url = EXCLUDED.toc_url, name = EXCLUDED.name, author = EXCLUDED.author,
			cover_url = EXCLUDED.cover_url, latest_chapter_title = EXCLUDED.latest_chapter_title,
			total_chapters = EXCLUDED.total_chapters, sync_status = EXCLUDED.sync_status,
			failure_count = EXCLUDED.failure_count, last_synced_at = EXCLUDED.last_synced_at
		RETURNING id, created_at, updated_at`,
		m.BookID, m.SourceID, m.BookURL, m.TOCURL, m.Name, m.Author, m.CoverURL,
		m.LatestChapterTitle, m.TotalChapters, m.SyncStatus, m.FailureCount, m.LastSyncedAt,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
	return err
}

// UpsertStandby 追加一条「备用源」映射（sync_status='pending'）。
// 语义：冲突（同 book_id/source_id/book_url 已存在）时不做任何变更——
// 绝不把已存在的 active 降级为 pending，也不改写已有元数据。
// 专门服务「从另一个源加入书架」场景；后续 switch-source 会把 pending 升为 active。
func (r *MappingRepository) UpsertStandby(ctx context.Context, q Querier, userID uuid.UUID, m *BookSourceMapping) error {
	if err := ensureBookOwned(ctx, q, userID, m.BookID); err != nil {
		return err
	}
	err := q.QueryRow(ctx, `
		INSERT INTO book_source_mappings (book_id, source_id, book_url, toc_url, name, author, cover_url,
			latest_chapter_title, total_chapters, sync_status, failure_count, last_synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$11)
		ON CONFLICT (book_id, source_id, book_url) DO NOTHING
		RETURNING id, created_at, updated_at`,
		m.BookID, m.SourceID, m.BookURL, m.TOCURL, m.Name, m.Author, m.CoverURL,
		m.LatestChapterTitle, m.TotalChapters, m.FailureCount, m.LastSyncedAt,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
	if err == pgx.ErrNoRows {
		// 已存在：保持原状（可能是 active，不动）。
		return nil
	}
	return err
}

// ListByBook 列出某用户某本书的全部源绑定。
func (r *MappingRepository) ListByBook(ctx context.Context, q Querier, userID, bookID uuid.UUID) ([]*BookSourceMapping, error) {
	rows, err := q.Query(ctx, `SELECT `+mappingColumns+`
		FROM book_source_mappings m
		JOIN books b ON b.id = m.book_id AND b.user_id = $1
		WHERE m.book_id = $2 ORDER BY m.created_at ASC, m.id`, userID, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*BookSourceMapping, 0, 4)
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateTOCMeta 更新书籍目录元数据（总数 / 最新章节）。
// latestAt 为 nil 时保持原值（COALESCE），避免目录缺少发布时间时把已有值清空。
func (r *BookRepository) UpdateTOCMeta(ctx context.Context, q Querier, userID, bookID uuid.UUID, totalChapters int, latestTitle string, latestAt *time.Time) error {
	_, err := q.Exec(ctx, `UPDATE books SET
			total_chapters = $3,
			latest_chapter_title = $4,
			latest_chapter_at = COALESCE($5, latest_chapter_at)
		WHERE id = $1 AND user_id = $2`, bookID, userID, totalChapters, latestTitle, latestAt)
	return err
}

// UpdateLastSyncedAt 只更新目录同步时间，绝不触碰 sync_status
// （避免误改状态或触发 uq_mappings_one_active 冲突）。
func (r *MappingRepository) UpdateLastSyncedAt(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE book_source_mappings m SET last_synced_at = $4
		WHERE m.book_id = $1 AND m.source_id = $2
		  AND EXISTS (SELECT 1 FROM books b WHERE b.id = $1 AND b.user_id = $3)`,
		bookID, sourceID, userID, at)
	return err
}

// Insert 新增一条「备用源」映射（pending）。冲突（同 book_id/source_id/book_url）
// 返回 ErrDuplicate（与 Upsert 的覆盖语义区分），供 addMapping 端点使用。
func (r *MappingRepository) Insert(ctx context.Context, q Querier, userID uuid.UUID, m *BookSourceMapping) error {
	if m.SyncStatus == "" {
		m.SyncStatus = MappingStatusPending
	}
	if err := ensureBookOwned(ctx, q, userID, m.BookID); err != nil {
		return err
	}
	err := q.QueryRow(ctx, `
		INSERT INTO book_source_mappings (book_id, source_id, book_url, toc_url, name, author, cover_url,
			latest_chapter_title, total_chapters, sync_status, failure_count, last_synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id, created_at, updated_at`,
		m.BookID, m.SourceID, m.BookURL, m.TOCURL, m.Name, m.Author, m.CoverURL,
		m.LatestChapterTitle, m.TotalChapters, m.SyncStatus, m.FailureCount, m.LastSyncedAt,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

// GetByID 按 id 读取一条绑定（限当前用户与书）。
func (r *MappingRepository) GetByID(ctx context.Context, q Querier, userID, bookID, mappingID uuid.UUID) (*BookSourceMapping, error) {
	return scanMapping(q.QueryRow(ctx, `SELECT `+mappingColumns+`
		FROM book_source_mappings m
		JOIN books b ON b.id = m.book_id AND b.user_id = $1
		WHERE m.id = $3 AND m.book_id = $2`, userID, bookID, mappingID))
}

// DeleteByID 删除一条绑定；未命中返回 ErrNotFound。
func (r *MappingRepository) DeleteByID(ctx context.Context, q Querier, userID, bookID, mappingID uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM book_source_mappings m
		WHERE m.id = $3 AND m.book_id = $2
		  AND EXISTS (SELECT 1 FROM books b WHERE b.id = $2 AND b.user_id = $1)`,
		userID, bookID, mappingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
