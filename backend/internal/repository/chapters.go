package repository

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ChapterRepository 负责 chapters 目录的读写。chapters 没有 user_id 列，
// 所有读写都通过 JOIN books ON books.id = chapters.book_id AND books.user_id = $1
// 或先做 ensureBookOwned 守护，实施租户隔离。
type ChapterRepository struct{}

// NewChapterRepository 构造目录仓库。
func NewChapterRepository() *ChapterRepository { return &ChapterRepository{} }

const chapterColumns = `c.id, c.book_id, c.source_id, c.chapter_index, c.sort_order, c.title, c.normalized_title,
	c.url, c.url_hash, c.is_volume, c.is_vip, c.word_count, c.published_at, c.created_at, c.updated_at`

func scanChapter(row pgx.Row) (*Chapter, error) {
	var c Chapter
	err := row.Scan(
		&c.ID, &c.BookID, &c.SourceID, &c.ChapterIndex, &c.SortOrder, &c.Title, &c.NormalizedTitle,
		&c.URL, &c.URLHash, &c.IsVolume, &c.IsVIP, &c.WordCount, &c.PublishedAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ReplaceTOC 在同一事务内先删后批量插入目录（每片 500 行），返回可读章节数
// （chapter_index IS NOT NULL 的行数）。本方法不触碰 chapter_source_overrides。
// 写操作前先用 ensureBookOwned 校验书籍归属，防止跨租户写入。
func (r *ChapterRepository) ReplaceTOC(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID, chapters []Chapter) (int, error) {
	if err := ensureBookOwned(ctx, q, userID, bookID); err != nil {
		return 0, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM chapters WHERE book_id = $1 AND source_id = $2`, bookID, sourceID); err != nil {
		return 0, err
	}

	ordered := make([]Chapter, len(chapters))
	copy(ordered, chapters)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].SortOrder < ordered[j].SortOrder })
	for i := range ordered {
		ordered[i].BookID = bookID
		ordered[i].SourceID = sourceID
	}

	const batchSize = 500
	for i := 0; i < len(ordered); i += batchSize {
		end := i + batchSize
		if end > len(ordered) {
			end = len(ordered)
		}
		if err := r.insertBatch(ctx, q, ordered[i:end]); err != nil {
			return 0, err
		}
	}

	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM chapters c
		JOIN books b ON b.id = c.book_id AND b.user_id = $1
		WHERE c.book_id = $2 AND c.source_id = $3 AND c.chapter_index IS NOT NULL`,
		userID, bookID, sourceID).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (r *ChapterRepository) insertBatch(ctx context.Context, q Querier, rows []Chapter) error {
	const cols = 12
	sql := make([]byte, 0, 256+len(rows)*64)
	sql = append(sql, `INSERT INTO chapters (book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash, is_volume, is_vip, word_count, published_at) VALUES `...)
	args := make([]any, 0, len(rows)*cols)
	for i, c := range rows {
		if i > 0 {
			sql = append(sql, ',')
		}
		base := i * cols
		sql = append(sql, '(')
		for j := 0; j < cols; j++ {
			if j > 0 {
				sql = append(sql, ',')
			}
			sql = append(sql, '$')
			sql = appendUint(sql, uint(base+j+1))
		}
		sql = append(sql, ')')
		args = append(args,
			c.BookID, c.SourceID, c.ChapterIndex, c.SortOrder, c.Title, c.NormalizedTitle,
			c.URL, c.URLHash, c.IsVolume, c.IsVIP, c.WordCount, c.PublishedAt,
		)
	}
	_, err := q.Exec(ctx, string(sql), args...)
	return err
}

func appendUint(dst []byte, v uint) []byte {
	if v == 0 {
		return append(dst, '0')
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return append(dst, buf[i:]...)
}

// GetTOC 读取某用户某本书在某源下的完整目录（按 sort_order 升序）。
func (r *ChapterRepository) GetTOC(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID) ([]*Chapter, error) {
	rows, err := q.Query(ctx, `SELECT `+chapterColumns+`
		FROM chapters c
		JOIN books b ON b.id = c.book_id AND b.user_id = $1
		WHERE c.book_id = $2 AND c.source_id = $3
		ORDER BY c.sort_order`, userID, bookID, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Chapter, 0, 256)
	for rows.Next() {
		c, err := scanChapter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetByIndex 读取某用户某源下指定 chapter_index 的章节。
func (r *ChapterRepository) GetByIndex(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID, chapterIndex int32) (*Chapter, error) {
	return scanChapter(q.QueryRow(ctx, `SELECT `+chapterColumns+`
		FROM chapters c
		JOIN books b ON b.id = c.book_id AND b.user_id = $1
		WHERE c.book_id = $2 AND c.source_id = $3 AND c.chapter_index = $4`,
		userID, bookID, sourceID, chapterIndex))
}

// CountReadable 返回某用户某源下可读章节数。
func (r *ChapterRepository) CountReadable(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM chapters c
		JOIN books b ON b.id = c.book_id AND b.user_id = $1
		WHERE c.book_id = $2 AND c.source_id = $3 AND c.chapter_index IS NOT NULL`,
		userID, bookID, sourceID).Scan(&n)
	return n, err
}

// TOCChapter 是目录行 + 单章覆盖关联（override 通过 chapter_index 关联）。
type TOCChapter struct {
	Chapter
	IsOverridden       bool
	OverrideStatus     *string
	OverrideSourceID   *uuid.UUID
	OverrideSourceName *string
	OverrideURL        *string
}

// ListWithOverrides 读取某源完整目录（按 sort_order 升序）并 LEFT JOIN
// chapter_source_overrides 与 book_sources，供 ChapterList 组装覆盖字段。
// 覆盖按 (book_id, chapter_index) 关联；卷名行 chapter_index 为 NULL 不参与关联。
func (r *ChapterRepository) ListWithOverrides(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID) ([]*TOCChapter, error) {
	rows, err := q.Query(ctx, `SELECT `+chapterColumns+`,
			(o.id IS NOT NULL) AS is_overridden,
			o.status,
			o.source_id,
			o.chapter_url,
			s.book_source_name
		FROM chapters c
		JOIN books b ON b.id = c.book_id AND b.user_id = $1
		LEFT JOIN chapter_source_overrides o
			ON o.book_id = c.book_id AND o.chapter_index = c.chapter_index AND o.user_id = $1
		LEFT JOIN book_sources s ON s.id = o.source_id AND s.user_id = $1
		WHERE c.book_id = $2 AND c.source_id = $3
		ORDER BY c.sort_order`, userID, bookID, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*TOCChapter, 0, 256)
	for rows.Next() {
		var row TOCChapter
		var overrideStatus *string
		err := rows.Scan(
			&row.ID, &row.BookID, &row.SourceID, &row.ChapterIndex, &row.SortOrder, &row.Title, &row.NormalizedTitle,
			&row.URL, &row.URLHash, &row.IsVolume, &row.IsVIP, &row.WordCount, &row.PublishedAt, &row.CreatedAt, &row.UpdatedAt,
			&row.IsOverridden, &overrideStatus, &row.OverrideSourceID, &row.OverrideURL, &row.OverrideSourceName,
		)
		if err != nil {
			return nil, err
		}
		row.OverrideStatus = overrideStatus
		out = append(out, &row)
	}
	return out, rows.Err()
}

// ListByBookSourceRange 读取可读章节（chapter_index 非空），可选 [from, to] 区间过滤。
// from/to 为 nil 时表示不限。结果按 chapter_index 升序。
func (r *ChapterRepository) ListByBookSourceRange(
	ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID, from, to *int,
) ([]*Chapter, error) {
	rows, err := q.Query(ctx, `SELECT `+chapterColumns+`
		FROM chapters c
		JOIN books b ON b.id = c.book_id AND b.user_id = $1
		WHERE c.book_id = $2 AND c.source_id = $3 AND c.chapter_index IS NOT NULL
		  AND ($4::int IS NULL OR c.chapter_index >= $4)
		  AND ($5::int IS NULL OR c.chapter_index <= $5)
		ORDER BY c.chapter_index`, userID, bookID, sourceID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Chapter, 0, 64)
	for rows.Next() {
		ch, err := scanChapter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// GetByID 按章节自增 ID 读取章节，且强制限定为 book 的活跃源。
// 传入非活跃源的 chapter_id 一律不命中（用于杜绝目录穿透）。
func (r *ChapterRepository) GetByID(ctx context.Context, q Querier, userID, bookID uuid.UUID, chapterID int64, sourceID uuid.UUID) (*Chapter, error) {
	return scanChapter(q.QueryRow(ctx, `SELECT `+chapterColumns+`
		FROM chapters c
		JOIN books b ON b.id = c.book_id AND b.user_id = $1
		WHERE c.book_id = $2 AND c.id = $3 AND c.source_id = $4`,
		userID, bookID, chapterID, sourceID))
}

// NeighborIndexes 返回同源下当前章节的前后可读章节序号（跳过卷名行）。
func (r *ChapterRepository) NeighborIndexes(ctx context.Context, q Querier, userID, bookID, sourceID uuid.UUID, curIndex int32) (prev, next *int32, err error) {
	var p, n *int32
	err = q.QueryRow(ctx, `SELECT
			(SELECT c.chapter_index FROM chapters c
			   JOIN books b ON b.id = c.book_id AND b.user_id = $1
			  WHERE c.book_id = $2 AND c.source_id = $3 AND c.chapter_index < $4
			  ORDER BY c.chapter_index DESC LIMIT 1) AS prev,
			(SELECT c.chapter_index FROM chapters c
			   JOIN books b ON b.id = c.book_id AND b.user_id = $1
			  WHERE c.book_id = $2 AND c.source_id = $3 AND c.chapter_index > $4
			  ORDER BY c.chapter_index ASC LIMIT 1) AS next`,
		userID, bookID, sourceID, curIndex).Scan(&p, &n)
	if err != nil {
		return nil, nil, err
	}
	return p, n, nil
}
