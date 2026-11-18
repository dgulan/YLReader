package repository

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TOCReferences struct {
	Progress  *ReadingProgress
	Bookmarks []*Bookmark
	Overrides []*ChapterSourceOverride
}
type BookmarkTOCMove struct {
	ID               uuid.UUID
	From, To, Offset int32
}

// LockForTOC keeps the selected mapping stable through directory publication.
// The book lock held by the caller also blocks new FK-referencing mappings.
func (r *MappingRepository) LockForTOC(ctx context.Context, tx pgx.Tx, user, book, source uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT id FROM book_source_mappings
		WHERE book_id=$2 AND source_id=$3
		AND EXISTS(SELECT 1 FROM books WHERE id=$2 AND user_id=$1)
		ORDER BY id FOR UPDATE`, user, book, source)
	return err
}

// LockTOCSource precedes the book lock, matching source-delete FK lock order.
func (r *ChapterRepository) LockTOCSource(ctx context.Context, tx pgx.Tx, user, source uuid.UUID, version int64) error {
	var current int64
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT version,enabled FROM book_sources WHERE user_id=$1 AND id=$2 FOR SHARE`, user, source).Scan(&current, &enabled); err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if !enabled || current != version {
		return ErrVersionConflict
	}
	return nil
}
func (r *ChapterRepository) LockTOCReferences(ctx context.Context, tx pgx.Tx, user, book uuid.UUID) (TOCReferences, error) {
	var out TOCReferences
	p, err := scanProgress(tx.QueryRow(ctx, `SELECT `+progressColumns+` FROM reading_progress WHERE user_id=$1 AND book_id=$2 FOR UPDATE`, user, book))
	if err != nil && err != ErrNotFound {
		return out, err
	}
	if err == nil {
		out.Progress = p
	}
	rows, err := tx.Query(ctx, `SELECT `+bookmarkColumns+` FROM bookmarks WHERE user_id=$1 AND book_id=$2 ORDER BY id FOR UPDATE`, user, book)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b Bookmark
		if err = rows.Scan(&b.ID, &b.UserID, &b.BookID, &b.ChapterIndex, &b.CharOffset, &b.ChapterTitle, &b.Excerpt, &b.Note, &b.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.Bookmarks = append(out.Bookmarks, &b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT `+overrideColumns+` FROM chapter_source_overrides WHERE user_id=$1 AND book_id=$2 ORDER BY id FOR UPDATE`, user, book)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		o, e := scanOverride(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Overrides = append(out.Overrides, o)
	}
	err = rows.Err()
	rows.Close()
	return out, err
}
func bookmarkStagingIndices(moves []BookmarkTOCMove) (map[uuid.UUID]int32, error) {
	used := map[int32]bool{}
	for _, m := range moves {
		used[m.From] = true
		used[m.To] = true
	}
	out := map[uuid.UUID]int32{}
	var next int64
	for _, m := range moves {
		if m.From == m.To {
			continue
		}
		for next <= math.MaxInt32 && used[int32(next)] {
			next++
		}
		if next > math.MaxInt32 {
			return nil, ErrInvalidArgument
		}
		out[m.ID] = int32(next)
		used[int32(next)] = true
		next++
	}
	return out, nil
}
func (r *ChapterRepository) MoveTOCBookmarks(ctx context.Context, tx pgx.Tx, user, book uuid.UUID, moves []BookmarkTOCMove) error {
	staging, err := bookmarkStagingIndices(moves)
	if err != nil {
		return err
	}
	for _, m := range moves {
		if m.From == m.To {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE bookmarks SET chapter_index=$4 WHERE user_id=$1 AND book_id=$2 AND id=$3`, user, book, m.ID, staging[m.ID]); err != nil {
			return err
		}
	}
	for _, m := range moves {
		if m.From == m.To {
			continue
		}
		tag, e := tx.Exec(ctx, `UPDATE bookmarks SET chapter_index=$4 WHERE user_id=$1 AND book_id=$2 AND id=$3`, user, book, m.ID, m.To)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("bookmark disappeared during TOC relocation")
		}
	}
	return nil
}
