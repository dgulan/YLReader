//go:build integration

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

func tocCompleteSnapshot(t *testing.T, f relocationFixture) string {
	t.Helper()
	var result string
	err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
	 'book',(SELECT to_jsonb(b) FROM books b WHERE id=$1),
	 'chapters',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM chapters c WHERE book_id=$1),
	 'progress',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM reading_progress p WHERE book_id=$1),
	 'bookmarks',(SELECT jsonb_agg(to_jsonb(b) ORDER BY id) FROM bookmarks b WHERE book_id=$1),
	 'overrides',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM chapter_source_overrides o WHERE book_id=$1),
	 'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM book_source_mappings m WHERE book_id=$1)
	)::text`, f.book).Scan(&result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestTOCIncompleteRefreshPreservesWholeSnapshot(t *testing.T) {
	for _, kind := range []string{"network", "timeout", "http", "empty", "bad chapter", "cycle", "limit", "wait"} {
		t.Run(kind, func(t *testing.T) {
			f, s, _ := tocStateSeed(t)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, `UPDATE book_sources SET rules=jsonb_set(rules,'{toc,nextTocUrl}','"a.next@href"') WHERE id=$1`, f.a); err != nil {
				t.Fatal(err)
			}
			p := &repository.ReadingProgress{UserID: f.user, BookID: f.book, ChapterIndex: 0, CharOffset: 12, ClientUpdatedAt: time.Now()}
			if err := repository.NewProgressRepository().InsertInitial(ctx, f.pool, p); err != nil {
				t.Fatal(err)
			}
			if _, err := NewBookmarkService(f.pool, nil).Create(ctx, f.user, f.book, CreateBookmarkInput{ChapterIndex: 0, CharOffset: 12}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewOverrideService(f.pool).Set(ctx, f.user, f.book, 2, "https://example.com/override", &f.b); err != nil {
				t.Fatal(err)
			}
			before := tocCompleteSnapshot(t, f)
			expected := sandbox.ErrRuleExecution
			if kind == "network" || kind == "http" {
				expected = fetcher.ErrUpstreamError
			}
			if kind == "timeout" || kind == "wait" {
				expected = context.DeadlineExceeded
			}
			ft := &tocCompleteFetcher{}
			ft.page = func(_ context.Context, target string) (*fetcher.Response, error) {
				body := tocCompletePage("a", "/page2")
				status := 200
				if target != "https://example.com/book" {
					body = tocCompletePage("b", "")
					switch kind {
					case "network":
						return nil, fetcher.ErrUpstreamError
					case "timeout":
						return nil, context.DeadlineExceeded
					case "http":
						status = 503
					case "empty":
						body = "<div>错误页面</div>"
					case "bad chapter":
						body = `<li><a>无地址</a></li>`
					case "cycle":
						body = tocCompletePage("b", "/book")
					case "limit":
						body = tocCompletePage("b", "/page"+strconv.Itoa(ft.calls))
					}
				}
				return &fetcher.Response{StatusCode: status, FinalURL: target, Body: []byte(body)}, nil
			}
			s.ft = ft
			s.WithSleep(func(context.Context, time.Duration) error {
				if kind == "wait" {
					return context.DeadlineExceeded
				}
				return nil
			})
			if err := tocStateRefresh(t, f, s); !errors.Is(err, expected) {
				t.Fatal(err)
			}
			if got := tocCompleteSnapshot(t, f); got != before {
				t.Fatal("failed pagination changed persisted reading state")
			}
			cached, err := s.GetChapters(ctx, f.user, f.book, nil, false)
			if err != nil || cached.Total != 3 {
				t.Fatal(cached, err)
			}
			// A subsequent complete retry must be allowed to publish all four chapters.
			s.ft = &tocCompleteFetcher{page: func(_ context.Context, target string) (*fetcher.Response, error) {
				body := tocCompletePage("a", "/page2")
				if target != "https://example.com/book" {
					body = tocCompletePage("b", "") + tocCompletePage("c", "") + tocCompletePage("d", "")
				}
				return &fetcher.Response{StatusCode: 200, FinalURL: target, Body: []byte(body)}, nil
			}}
			s.WithSleep(func(context.Context, time.Duration) error { return nil })
			if err := tocStateRefresh(t, f, s); err != nil {
				t.Fatal("retry failed", err)
			}
			cached, err = s.GetChapters(ctx, f.user, f.book, nil, false)
			if err != nil || cached.Total != 4 {
				t.Fatal(cached, err)
			}
		})
	}
}
