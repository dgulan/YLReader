package backup

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

var localChapterObjectKey = regexp.MustCompile(`^local/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/[0-9]+\.txt$`)

func validateRestoredReferences(ctx context.Context, tx pgx.Tx, objects *stagedObjects) error {
	var valid bool
	e := tx.QueryRow(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM books b JOIN book_sources s ON s.id=b.active_source_id WHERE s.user_id<>b.user_id)
 AND NOT EXISTS(SELECT 1 FROM chapters c JOIN books b ON b.id=c.book_id JOIN book_sources s ON s.id=c.source_id WHERE s.user_id<>b.user_id)
 AND NOT EXISTS(SELECT 1 FROM book_source_mappings m JOIN books b ON b.id=m.book_id JOIN book_sources s ON s.id=m.source_id WHERE s.user_id<>b.user_id)
 AND NOT EXISTS(SELECT 1 FROM reading_progress p JOIN books b ON b.id=p.book_id WHERE p.user_id<>b.user_id)
 AND NOT EXISTS(SELECT 1 FROM bookmarks p JOIN books b ON b.id=p.book_id WHERE p.user_id<>b.user_id)
 AND NOT EXISTS(SELECT 1 FROM chapter_source_overrides o JOIN books b ON b.id=o.book_id JOIN book_sources s ON s.id=o.source_id WHERE o.user_id<>b.user_id OR s.user_id<>b.user_id)
 AND NOT EXISTS(SELECT 1 FROM source_failures f JOIN book_sources s ON s.id=f.source_id WHERE f.user_id<>s.user_id)
 AND NOT EXISTS(SELECT 1 FROM books b LEFT JOIN book_sources s ON s.id=b.active_source_id WHERE b.book_type='local' AND (s.id IS NULL OR s.book_source_type<>3 OR s.book_source_url<>'local://'||b.user_id::text OR b.active_book_url IS DISTINCT FROM 'local:'||b.id::text))
 AND NOT EXISTS(SELECT 1 FROM chapters c JOIN books b ON b.id=c.book_id WHERE b.book_type='local' AND (c.source_id IS DISTINCT FROM b.active_source_id OR (NOT c.is_volume AND (c.chapter_index IS NULL OR c.url IS DISTINCT FROM 'local:'||b.id::text||':'||c.chapter_index::text))))
 AND NOT EXISTS(SELECT 1 FROM reading_progress p JOIN books b ON b.id=p.book_id WHERE b.book_type='local' AND NOT EXISTS(SELECT 1 FROM chapters c WHERE c.book_id=b.id AND c.source_id=b.active_source_id AND NOT c.is_volume AND c.chapter_index=p.chapter_index))
 AND NOT EXISTS(SELECT 1 FROM bookmarks p JOIN books b ON b.id=p.book_id WHERE b.book_type='local' AND NOT EXISTS(SELECT 1 FROM chapters c WHERE c.book_id=b.id AND c.source_id=b.active_source_id AND NOT c.is_volume AND c.chapter_index=p.chapter_index))`).Scan(&valid)
	if e != nil || !valid {
		return ErrObjectReferences
	}
	rows, e := tx.Query(ctx, `SELECT b.id::text,c.chapter_index FROM books b JOIN chapters c ON c.book_id=b.id WHERE b.book_type='local' AND NOT c.is_volume ORDER BY b.id,c.chapter_index`)
	if e != nil {
		return ErrObjectReferences
	}
	defer rows.Close()
	expected := make(map[string]bool)
	for rows.Next() {
		var book string
		var index int32
		if rows.Scan(&book, &index) != nil {
			return ErrObjectReferences
		}
		key := fmt.Sprintf("local/%s/%d.txt", book, index)
		expected[key] = true
		metadata, ok := objects.metadata(key)
		if !ok {
			return ErrObjectReferences
		}
		r, e := objects.Open(ctx, key)
		if e != nil {
			return ErrObjectReferences
		}
		// Local text is UTF-8; validation stays streaming even for large chapters.
		digest, e := manifestUTF8Hash(ctx, r)
		closed := r.Close()
		if e != nil || closed != nil || digest != metadata.SHA {
			return ErrObjectReferences
		}
	}
	if rows.Err() != nil {
		return ErrObjectReferences
	}
	for _, object := range objects.objects {
		if localChapterObjectKey.MatchString(object.Key) && !expected[object.Key] {
			return ErrObjectReferences
		}
	}
	return nil
}
