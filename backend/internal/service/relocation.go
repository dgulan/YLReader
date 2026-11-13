package service

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"math"
	"yreader/backend/internal/repository"
)

func relocationIndex(oldIndex, oldTotal, total int) int32 {
	if total <= 0 {
		return 0
	}
	n := int(math.Round(float64(oldIndex) / float64(max(oldTotal, 1)) * float64(total)))
	return int32(max(0, min(n, total-1)))
}
func relocationOffset(ratio float64, count int) int32 {
	if math.IsNaN(ratio) || count <= 0 {
		return 0
	}
	return int32(math.Floor(math.Max(0, math.Min(1, ratio)) * float64(count)))
}
func relocateChapter(ctx context.Context, tx pgx.Tx, uid uuid.UUID, book *repository.Book, source uuid.UUID, p *repository.ReadingProgress) (*repository.Chapter, string, float64, int, error) {
	chapters := repository.NewChapterRepository()
	total, err := chapters.CountReadable(ctx, tx, uid, book.ID, source)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if total == 0 {
		return nil, "", 0, 0, ErrRelocationFailed
	}
	oldTotal := int(book.TotalChapters)
	if book.ActiveSourceID != nil {
		n, e := chapters.CountReadable(ctx, tx, uid, book.ID, *book.ActiveSourceID)
		if e != nil {
			return nil, "", 0, 0, e
		}
		if n > 0 {
			oldTotal = n
		}
		if p.NormalizedTitle == "" {
			old, e := chapters.GetByIndex(ctx, tx, uid, book.ID, *book.ActiveSourceID, p.ChapterIndex)
			if e == nil {
				p.NormalizedTitle = old.NormalizedTitle
				p.ChapterTitle = old.Title
			} else if !errors.Is(e, repository.ErrNotFound) {
				return nil, "", 0, 0, e
			}
		}
	}
	idx := relocationIndex(int(p.ChapterIndex), oldTotal, total)
	strategy, confidence := "ratio", 0.3
	if p.NormalizedTitle != "" {
		var hit int32
		err = tx.QueryRow(ctx, `SELECT chapter_index FROM chapters WHERE book_id=$1 AND source_id=$2 AND NOT is_volume AND normalized_title=$3 AND chapter_index BETWEEN greatest(0,$4::int-20) AND $4::int+20 ORDER BY abs(chapter_index-$4::int), (title=$5) DESC,chapter_index LIMIT 1`, book.ID, source, p.NormalizedTitle, p.ChapterIndex, p.ChapterTitle).Scan(&hit)
		if err == nil {
			idx = hit
			strategy = "exact"
			confidence = 1
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", 0, 0, err
		} else {
			// Transaction-local threshold prevents a pool/session GUC from dropping valid candidates.
			if _, err = tx.Exec(ctx, `SET LOCAL pg_trgm.similarity_threshold=0.3`); err != nil {
				return nil, "", 0, 0, err
			}
			err = tx.QueryRow(ctx, `SELECT chapter_index,similarity(normalized_title,$3)::float8 FROM chapters WHERE book_id=$1 AND source_id=$2 AND NOT is_volume AND chapter_index BETWEEN greatest(0,$4::int-100) AND $4::int+100 AND normalized_title % $3 AND similarity(normalized_title,$3)>=0.6 ORDER BY similarity(normalized_title,$3) DESC,abs(chapter_index-$4::int),chapter_index LIMIT 1`, book.ID, source, p.NormalizedTitle, p.ChapterIndex).Scan(&hit, &confidence)
			if err == nil {
				idx = hit
				strategy = "fuzzy"
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, "", 0, 0, err
			} else {
				confidence = 0.3
			}
		}
	}
	ch, err := chapters.GetByIndex(ctx, tx, uid, book.ID, source, idx)
	if errors.Is(err, repository.ErrNotFound) {
		err = ErrRelocationFailed
	}
	return ch, strategy, confidence, total, err
}
