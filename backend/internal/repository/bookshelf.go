package repository

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BookshelfRepository 负责 v_bookshelf 视图的查询。
type BookshelfRepository struct{}

// NewBookshelfRepository 构造书架仓库。
func NewBookshelfRepository() *BookshelfRepository { return &BookshelfRepository{} }

const bookshelfColumns = `id, user_id, name, author, book_type, status, cover_url, total_chapters,
	latest_chapter_title, last_read_at, active_source_id, active_book_url, version, created_at, updated_at,
	needs_source, override_count, active_source_name,
	progress_chapter_index, progress_chapter_title, progress_ratio::float8,
	progress_percent::float8, progress_updated_at`

func scanBookshelf(row pgx.Row) (*BookshelfEntry, error) {
	var e BookshelfEntry
	err := row.Scan(
		&e.ID, &e.UserID, &e.Name, &e.Author, &e.BookType, &e.Status, &e.CoverURL, &e.TotalChapters,
		&e.LatestChapterTitle, &e.LastReadAt, &e.ActiveSourceID, &e.ActiveBookURL, &e.Version, &e.CreatedAt, &e.UpdatedAt,
		&e.NeedsSource, &e.OverrideCount, &e.ActiveSourceName,
		&e.ProgressChapterIndex, &e.ProgressChapterTitle, &e.ProgressRatio,
		&e.ProgressPercent, &e.ProgressUpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// BookshelfFilter 是 v_bookshelf 列表过滤条件。
// NeedsSource 三态：nil=不过滤；true=仅缺失活跃源；false=仅有活跃源。
type BookshelfFilter struct {
	Query       string
	Status      string
	NeedsSource *bool
	Limit       int
	Offset      int
}

// where 生成 v_bookshelf 的过滤条件与参数（参数从 $2 起，$1 固定为 userID）。
func (f BookshelfFilter) where() (string, []any) {
	conds := []string{`user_id = $1`}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args)+1)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + q + "%"
		conds = append(conds, `(name ILIKE `+arg(like)+` OR author ILIKE `+arg(like)+`)`)
	}
	if s := strings.TrimSpace(f.Status); s != "" {
		conds = append(conds, `status = `+arg(s))
	}
	if f.NeedsSource != nil {
		if *f.NeedsSource {
			conds = append(conds, `(book_type = 'online' AND active_source_id IS NULL)`)
		} else {
			// 严禁把 false 当作「不过滤」：false 表示只看有活跃源的书。
			conds = append(conds, `(book_type <> 'online' OR active_source_id IS NOT NULL)`)
		}
	}
	return strings.Join(conds, " AND "), args
}

// ListPage 按过滤条件分页查询书架，并返回同条件的总数。
func (r *BookshelfRepository) ListPage(ctx context.Context, q Querier, userID uuid.UUID, f BookshelfFilter) ([]*BookshelfEntry, int, error) {
	where, args := f.where()
	all := append([]any{userID}, args...)
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM v_bookshelf WHERE `+where, all...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	pageArgs := append(append([]any{}, all...), limit, offset)
	rows, err := q.Query(ctx, `SELECT `+bookshelfColumns+`
		FROM v_bookshelf WHERE `+where+`
		ORDER BY last_read_at DESC NULLS LAST, created_at DESC
		LIMIT $`+strconv.Itoa(len(args)+2)+` OFFSET $`+strconv.Itoa(len(args)+3),
		pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]*BookshelfEntry, 0, limit)
	for rows.Next() {
		e, err := scanBookshelf(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// List 返回某用户的书架列表（含 override_count / needs_source / 进度摘要）。
func (r *BookshelfRepository) List(ctx context.Context, q Querier, userID uuid.UUID) ([]*BookshelfEntry, error) {
	rows, err := q.Query(ctx, `SELECT `+bookshelfColumns+`
		FROM v_bookshelf WHERE user_id = $1
		ORDER BY last_read_at DESC NULLS LAST, created_at DESC, name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*BookshelfEntry, 0, 32)
	for rows.Next() {
		e, err := scanBookshelf(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
