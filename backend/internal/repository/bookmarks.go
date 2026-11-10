package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Bookmark 对应 bookmarks 表的一行。
type Bookmark struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	BookID       uuid.UUID
	ChapterIndex int32
	CharOffset   int32
	ChapterTitle string
	Excerpt      *string
	Note         *string
	CreatedAt    time.Time
}

const bookmarkColumns = `id, user_id, book_id, chapter_index, char_offset, chapter_title, excerpt, note, created_at`

// BookmarkRepository 读写书签。
type BookmarkRepository struct{}

// NewBookmarkRepository 构造 BookmarkRepository。
func NewBookmarkRepository() *BookmarkRepository { return &BookmarkRepository{} }

// Create 插入书签；唯一键冲突时返回 ErrDuplicate。
func (r *BookmarkRepository) Create(ctx context.Context, q Querier, b *Bookmark) error {
	err := q.QueryRow(ctx,
		`INSERT INTO bookmarks (user_id, book_id, chapter_index, char_offset, chapter_title, excerpt, note)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at`,
		b.UserID, b.BookID, b.ChapterIndex, b.CharOffset, b.ChapterTitle, b.Excerpt, b.Note,
	).Scan(&b.ID, &b.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

// ListByBook 返回某书的书签（按章节与偏移升序）。
func (r *BookmarkRepository) ListByBook(ctx context.Context, q Querier, userID, bookID uuid.UUID) ([]*Bookmark, error) {
	rows, err := q.Query(ctx,
		`SELECT `+bookmarkColumns+`
		   FROM bookmarks
		  WHERE user_id = $1 AND book_id = $2
		  ORDER BY chapter_index ASC, char_offset ASC, created_at ASC`,
		userID, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Bookmark, 0)
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.UserID, &b.BookID, &b.ChapterIndex, &b.CharOffset,
			&b.ChapterTitle, &b.Excerpt, &b.Note, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete 按 id 删除（限本用户）；未命中返回 ErrNotFound。
func (r *BookmarkRepository) Delete(ctx context.Context, q Querier, userID, bookmarkID uuid.UUID) error {
	tag, err := q.Exec(ctx,
		`DELETE FROM bookmarks WHERE user_id = $1 AND id = $2`, userID, bookmarkID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
