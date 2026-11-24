//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/pkg/cache"
	"yreader/backend/internal/repository"
)

type tocStateFetcher struct {
	urls   []string
	before func()
}

func (f *tocStateFetcher) Get(_ context.Context, target string, _ http.Header) (*fetcher.Response, error) {
	if f.before != nil {
		call := f.before
		f.before = nil
		call()
	}
	body := "<body>原创正文 " + target + "</body>"
	if target == "https://example.com/book" || target == "https://example.com/target" {
		body = "<ul>"
		for _, url := range f.urls {
			body += fmt.Sprintf(`<li><a href="/%s">%s</a></li>`, url, url)
		}
		body += "</ul>"
	}
	return &fetcher.Response{StatusCode: 200, FinalURL: target, Body: []byte(body)}, nil
}
func tocStateSeed(t *testing.T) (relocationFixture, *TOCService, *tocStateFetcher) {
	t.Helper()
	f := relocationSeed(t, 3)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `DELETE FROM reading_progress WHERE book_id=$1;`, f.book)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(ctx, `UPDATE book_sources SET rules='{"toc":{"chapterList":"li","chapterName":"a@text","chapterUrl":"a@href"},"content":{"content":"body@text"}}' WHERE id=$1`, f.a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(ctx, `UPDATE books SET total_chapters=3 WHERE id=$1`, f.book)
	if err != nil {
		t.Fatal(err)
	}
	r := repository.NewChapterRepository()
	if _, err = r.ReplaceTOC(ctx, f.pool, f.user, f.book, f.a, tocStateRows("a", "b", "c")); err != nil {
		t.Fatal(err)
	}
	ft := &tocStateFetcher{urls: []string{"b", "a", "c"}}
	return f, NewTOCService(f.pool, ft, nil), ft
}
func tocStateRefresh(t *testing.T, f relocationFixture, s *TOCService) error {
	t.Helper()
	ctx := context.Background()
	b, err := s.books.Get(ctx, f.pool, f.user, f.book)
	if err != nil {
		t.Fatal(err)
	}
	src, err := s.sources.Get(ctx, f.pool, f.user, f.a)
	if err != nil {
		t.Fatal(err)
	}
	return s.refresh(ctx, f.user, b, src)
}
func TestTOCStateReorderPreservesReferencesAndCache(t *testing.T) {
	f, s, ft := tocStateSeed(t)
	ctx := context.Background()
	p := &repository.ReadingProgress{UserID: f.user, BookID: f.book, ChapterIndex: 0, CharOffset: 17, ChapterRatio: .25, ClientUpdatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	device := "phone"
	p.DeviceID = &device
	pr := repository.NewProgressRepository()
	if err := pr.InsertInitial(ctx, f.pool, p); err != nil {
		t.Fatal(err)
	}
	before := *p
	br := repository.NewBookmarkRepository()
	notes := "保留备注"
	marks := []*repository.Bookmark{{UserID: f.user, BookID: f.book, ChapterIndex: 0, CharOffset: 9, ChapterTitle: "旧标题a", Note: &notes}, {UserID: f.user, BookID: f.book, ChapterIndex: 1, CharOffset: 9, ChapterTitle: "旧标题b"}}
	for _, b := range marks {
		if err := br.Create(ctx, f.pool, b); err != nil {
			t.Fatal(err)
		}
	}
	cs := NewContentService(f.pool, ft, nil, cache.NewMemory(), nil, nil)
	book, _ := s.books.Get(ctx, f.pool, f.user, f.book)
	index := 0
	old, err := cs.get(ctx, f.user, f.book, &index, nil, book, false)
	if err != nil || !strings.Contains(old.Content, "/a") {
		t.Fatal(old, err)
	}
	src, err := s.sources.Get(ctx, f.pool, f.user, f.a)
	if err != nil {
		t.Fatal(err)
	}
	oldRows, err := s.chapters.GetTOC(ctx, f.pool, f.user, f.book, f.a)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.WarmChapterForSource(ctx, f.user, f.book, src, oldRows[1]); err != nil {
		t.Fatal(err)
	}
	if err := tocStateRefresh(t, f, s); err != nil {
		t.Fatal(err)
	}
	got, err := pr.Get(ctx, f.pool, f.user, f.book)
	if err != nil || got.ChapterIndex != 1 || got.CharOffset != 17 || got.ChapterRatio != .25 || got.Version != before.Version+1 || !got.ClientUpdatedAt.Equal(before.ClientUpdatedAt) || *got.DeviceID != device {
		t.Fatal(got, err)
	}
	if _, err := NewProgressService(f.pool).Update(ctx, f.user, &before, before.Version); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatal("stale progress accepted", err)
	}
	book2, _ := s.books.Get(ctx, f.pool, f.user, f.book)
	if book2.Version != book.Version+1 {
		t.Fatal("book context not invalidated")
	}
	list, err := br.ListByBook(ctx, f.pool, f.user, f.book)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	for _, b := range list {
		for _, original := range marks {
			if b.ID == original.ID && (b.ChapterIndex != 1-original.ChapterIndex || b.CharOffset != original.CharOffset || b.ChapterTitle != original.ChapterTitle || !b.CreatedAt.Equal(original.CreatedAt)) {
				t.Fatal("bookmark changed", b, original)
			}
		}
		if b.ID == marks[0].ID && (b.Note == nil || *b.Note != notes) {
			t.Fatal("note lost")
		}
	}
	gotContent, err := cs.get(ctx, f.user, f.book, &index, nil, book2, false)
	if err != nil || gotContent.Cached || !strings.Contains(gotContent.Content, "/b") {
		t.Fatal("old indexed cache reused", gotContent, err)
	}
	rows, _ := s.chapters.GetTOC(ctx, f.pool, f.user, f.book, f.a)
	if err := cs.WarmChapterForSource(ctx, f.user, f.book, src, rows[1]); err != nil {
		t.Fatal(err)
	}
	index = 1
	warmed, err := cs.get(ctx, f.user, f.book, &index, nil, book2, false)
	if err != nil || !warmed.Cached || !strings.Contains(warmed.Content, "/a") {
		t.Fatal(warmed, err)
	}
}
func TestTOCStateRejectsUnsafeRefreshAtomically(t *testing.T) {
	for _, kind := range []string{"missing", "override", "source changed", "book changed", "mapping changed", "directory changed"} {
		t.Run(kind, func(t *testing.T) {
			f, s, ft := tocStateSeed(t)
			ctx := context.Background()
			pr := repository.NewProgressRepository()
			p := &repository.ReadingProgress{UserID: f.user, BookID: f.book, ChapterIndex: 0, CharOffset: 21, ClientUpdatedAt: time.Now()}
			if err := pr.InsertInitial(ctx, f.pool, p); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory changed":
				ft.before = func() {
					if _, err := s.chapters.ReplaceTOC(ctx, f.pool, f.user, f.book, f.a, tocStateRows("a", "b", "c")); err != nil {
						t.Fatal(err)
					}
				}
			case "missing":
				ft.urls = []string{"b", "c"}
			case "override":
				if _, _, err := NewOverrideService(f.pool).Set(ctx, f.user, f.book, 0, "https://example.com/override", &f.b); err != nil {
					t.Fatal(err)
				}
			case "source changed":
				ft.before = func() {
					if _, err := f.pool.Exec(ctx, `UPDATE book_sources SET book_source_name='changed' WHERE id=$1`, f.a); err != nil {
						t.Fatal(err)
					}
				}
			case "book changed":
				ft.before = func() {
					if _, err := f.pool.Exec(ctx, `UPDATE books SET active_book_url='https://example.com/changed' WHERE id=$1`, f.book); err != nil {
						t.Fatal(err)
					}
				}
			case "mapping changed":
				ft.before = func() {
					if _, err := f.pool.Exec(ctx, `UPDATE book_source_mappings SET toc_url='https://example.com/new' WHERE book_id=$1 AND source_id=$2`, f.book, f.a); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tocStateRefresh(t, f, s); !errors.Is(err, ErrRelocationFailed) {
				t.Fatal(err)
			}
			rows, err := s.chapters.GetTOC(ctx, f.pool, f.user, f.book, f.a)
			if err != nil || len(rows) != 3 || rows[0].URL != "https://example.com/a" {
				t.Fatal(rows, err)
			}
			got, _ := pr.Get(ctx, f.pool, f.user, f.book)
			if got.Version != p.Version || got.ChapterIndex != 0 || got.CharOffset != 21 {
				t.Fatal(got)
			}
			var synced *time.Time
			if err := f.pool.QueryRow(ctx, `SELECT last_synced_at FROM book_source_mappings WHERE book_id=$1 AND source_id=$2`, f.book, f.a).Scan(&synced); err != nil || synced != nil {
				t.Fatal("failed refresh published sync time", err)
			}
		})
	}
}
func TestTOCStateWritersRespectBookLock(t *testing.T) {
	f, s, _ := tocStateSeed(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.books.Lock(ctx, tx, f.user, f.book); err != nil {
		t.Fatal(err)
	}
	if _, err := s.chapters.LockTOCReferences(ctx, tx, f.user, f.book); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"bookmark", "override"} {
		t.Run(kind, func(t *testing.T) {
			deadline, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			var err error
			if kind == "bookmark" {
				_, err = NewBookmarkService(f.pool, nil).Create(deadline, f.user, f.book, CreateBookmarkInput{ChapterIndex: 0})
			} else {
				_, _, err = NewOverrideService(f.pool).Set(deadline, f.user, f.book, 0, "https://example.com/override", &f.b)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("writer escaped held book lock", err)
			}
		})
	}
}
func TestTOCStateUsesProgressWrittenDuringFetch(t *testing.T) {
	f, s, ft := tocStateSeed(t)
	ctx := context.Background()
	pr := repository.NewProgressRepository()
	ft.before = func() {
		p := &repository.ReadingProgress{UserID: f.user, BookID: f.book, ChapterIndex: 1, CharOffset: 33, ClientUpdatedAt: time.Now()}
		if _, err := NewProgressService(f.pool).Update(ctx, f.user, p, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := tocStateRefresh(t, f, s); err != nil {
		t.Fatal(err)
	}
	p, err := pr.Get(ctx, f.pool, f.user, f.book)
	if err != nil || p.ChapterIndex != 0 || p.CharOffset != 33 || p.Version != 3 {
		t.Fatal(p, err)
	}
}
func TestTOCStateDefaultAndAppendVersions(t *testing.T) {
	f, s, ft := tocStateSeed(t)
	ctx := context.Background()
	pr := repository.NewProgressRepository()
	if err := tocStateRefresh(t, f, s); err != nil {
		t.Fatal(err)
	}
	p, _ := pr.Get(ctx, f.pool, f.user, f.book)
	if p.Version != 2 || p.ChapterIndex != 0 || p.CharOffset != 0 {
		t.Fatal(p)
	}
	b, _ := s.books.Get(ctx, f.pool, f.user, f.book)
	ft.urls = []string{"b", "a", "c", "d"}
	if err := tocStateRefresh(t, f, s); err != nil {
		t.Fatal(err)
	}
	p2, _ := pr.Get(ctx, f.pool, f.user, f.book)
	b2, _ := s.books.Get(ctx, f.pool, f.user, f.book)
	if p2.Version != p.Version || b2.Version != b.Version+1 {
		t.Fatal("append invalidated unchanged positions")
	}
}
func TestTOCStateDatabaseFailureRollsBackAllWrites(t *testing.T) {
	f, s, ft := tocStateSeed(t)
	ctx := context.Background()
	ft.urls = []string{"b", "a", "c", "d"}
	p := &repository.ReadingProgress{UserID: f.user, BookID: f.book, ChapterIndex: 0, CharOffset: 7, ClientUpdatedAt: time.Now()}
	pr := repository.NewProgressRepository()
	if err := pr.InsertInitial(ctx, f.pool, p); err != nil {
		t.Fatal(err)
	}
	br := repository.NewBookmarkRepository()
	mark := &repository.Bookmark{UserID: f.user, BookID: f.book, ChapterIndex: 0, CharOffset: 7}
	if err := br.Create(ctx, f.pool, mark); err != nil {
		t.Fatal(err)
	}
	book, _ := s.books.Get(ctx, f.pool, f.user, f.book)
	constraint := "test_toc_" + strings.ReplaceAll(f.book.String(), "-", "")
	if _, err := f.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE books ADD CONSTRAINT %s CHECK (id <> '%s'::uuid OR total_chapters <= 3)`, constraint, f.book)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `ALTER TABLE books DROP CONSTRAINT `+constraint); err != nil {
			t.Error(err)
		}
	})
	if err := tocStateRefresh(t, f, s); err == nil {
		t.Fatal("injected database failure accepted")
	}
	rows, _ := s.chapters.GetTOC(ctx, f.pool, f.user, f.book, f.a)
	got, _ := pr.Get(ctx, f.pool, f.user, f.book)
	marks, _ := br.ListByBook(ctx, f.pool, f.user, f.book)
	book2, _ := s.books.Get(ctx, f.pool, f.user, f.book)
	if len(rows) != 3 || rows[0].URL != "https://example.com/a" || got.Version != p.Version || got.ChapterIndex != 0 || len(marks) != 1 || marks[0].ID != mark.ID || marks[0].ChapterIndex != 0 || book.Version != book2.Version {
		t.Fatal("partial refresh survived rollback", got, marks, book2)
	}
}
