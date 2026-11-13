//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strings"
	"testing"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/repository"
	"yreader/backend/internal/repository/testutil"
)

type relocationFixture struct {
	pool             *pgxpool.Pool
	user, book, a, b uuid.UUID
}

func relocationSeed(t *testing.T, n int) relocationFixture {
	t.Helper()
	ctx := context.Background()
	pool := testutil.TestPool(t)
	f := relocationFixture{pool: pool, user: seedUserForServiceTest(t, pool), book: uuid.New(), a: uuid.New(), b: uuid.New()}
	for _, src := range []uuid.UUID{f.a, f.b} {
		if _, err := pool.Exec(ctx, `INSERT INTO book_sources(id,user_id,book_source_url,book_source_name,rules) VALUES($1,$2,$3,'source','{"content":{"content":"body@text"}}')`, src, f.user, "https://"+src.String()+".example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO books(id,user_id,book_type,name,author,active_source_id,active_book_url,total_chapters) VALUES($1,$2,'online','book','author',$3,'https://example.com/book',100)`, f.book, f.user, f.a); err != nil {
		t.Fatal(err)
	}
	for _, src := range []uuid.UUID{f.a, f.b} {
		count := n
		if src == f.a {
			count = 100
		}
		if count == 0 {
			continue
		}
		if _, err := pool.Exec(ctx, `INSERT INTO chapters(book_id,source_id,chapter_index,sort_order,title,normalized_title,url,url_hash) SELECT $1,$2,i,i,'chapter'||i,'different'||i,'https://example.com/'||i,encode(sha256(i::text::bytea),'hex') FROM generate_series(0,$3::int-1) i`, f.book, src, count); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO book_source_mappings(book_id,source_id,book_url,sync_status) VALUES($1,$2,'https://example.com/book','active'),($1,$3,'https://example.com/target','pending')`, f.book, f.a, f.b); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO reading_progress(user_id,book_id,chapter_index,char_offset,chapter_title,normalized_title,chapter_ratio,version) VALUES($1,$2,40,50,'oldtitle','uniquetitleabcdef',0.5,2)`, f.user, f.book); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestSwitchRelocationStrategies(t *testing.T) {
	for _, kind := range []string{"exact", "fuzzy", "ratio"} {
		t.Run(kind, func(t *testing.T) {
			f := relocationSeed(t, 50)
			ctx := context.Background()
			want := int32(20)
			if kind == "exact" {
				want = 42
				_, err := f.pool.Exec(ctx, `UPDATE chapters SET normalized_title='uniquetitleabcdef',title=CASE WHEN chapter_index=42 THEN 'oldtitle' ELSE 'other' END WHERE book_id=$1 AND source_id=$2 AND chapter_index IN(38,42)`, f.book, f.b)
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "fuzzy" {
				want = 45
				_, err := f.pool.Exec(ctx, `UPDATE chapters SET normalized_title='uniquetitleabcdeg' WHERE book_id=$1 AND source_id=$2 AND chapter_index=45`, f.book, f.b)
				if err != nil {
					t.Fatal(err)
				}
			}
			s := NewMappingService(f.pool)
			preview, err := s.Switch(ctx, f.user, f.book, f.b, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Strategy != kind || preview.ChapterIndex != want || preview.Confidence <= 0 {
				t.Fatalf("preview %+v", preview)
			}
			p, err := s.progress.Get(ctx, f.pool, f.user, f.book)
			if err != nil {
				t.Fatal(err)
			}
			if p.Version != 2 || p.CharOffset != 50 {
				t.Fatalf("preview mutated progress %+v", p)
			}
			result, err := s.Switch(ctx, f.user, f.book, f.b, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Strategy != kind || result.ChapterIndex != want {
				t.Fatalf("result %+v", result)
			}
			p, err = s.progress.Get(ctx, f.pool, f.user, f.book)
			if err != nil {
				t.Fatal(err)
			}
			if p.Version != 3 || p.ChapterIndex != want || p.CharOffset != 0 || p.ChapterRatio != .5 {
				t.Fatalf("progress %+v", p)
			}
			stale := *p
			if _, err := NewProgressService(f.pool).Update(ctx, f.user, &stale, 2); !errors.Is(err, repository.ErrVersionConflict) {
				t.Fatalf("stale update: %v", err)
			}
		})
	}
}
func TestSwitchEmptyDirectoryUnchanged(t *testing.T) {
	f := relocationSeed(t, 0)
	_, err := NewMappingService(f.pool).Switch(context.Background(), f.user, f.book, f.b, nil, false)
	if !errors.Is(err, ErrRelocationFailed) {
		t.Fatal(err)
	}
	var active uuid.UUID
	if err = f.pool.QueryRow(context.Background(), `SELECT active_source_id FROM books WHERE id=$1`, f.book).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != f.a {
		t.Fatal("source changed on failure")
	}
}

type relocationFetcher struct{ before func() }

func (f relocationFetcher) Get(context.Context, string, http.Header) (*fetcher.Response, error) {
	if f.before != nil {
		f.before()
	}
	return &fetcher.Response{StatusCode: 200, Body: []byte("<body>" + strings.Repeat("甲😀", 50) + "</body>")}, nil
}
func TestDeferredOffsetAndConcurrentProgress(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprint(concurrent), func(t *testing.T) {
			f := relocationSeed(t, 50)
			ctx := context.Background()
			s := NewMappingService(f.pool)
			// An advertised TOC count must not be trusted as the actual text length.
			if _, err := f.pool.Exec(ctx, `UPDATE chapters SET word_count=999 WHERE book_id=$1 AND source_id=$2`, f.book, f.b); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Switch(ctx, f.user, f.book, f.b, nil, false); err != nil {
				t.Fatal(err)
			}
			ft := relocationFetcher{}
			if concurrent {
				ft.before = func() {
					p, err := s.progress.Get(ctx, f.pool, f.user, f.book)
					if err != nil {
						t.Fatal(err)
					}
					p.CharOffset = 77
					if _, err = NewProgressService(f.pool).Update(ctx, f.user, p, p.Version); err != nil {
						t.Fatal(err)
					}
				}
			}
			cs := NewContentService(f.pool, ft, nil, nil, nil, nil)
			idx := 20
			if _, err := cs.Get(ctx, f.user, f.book, &idx, nil); err != nil {
				t.Fatal(err)
			}
			p, err := s.progress.Get(ctx, f.pool, f.user, f.book)
			if err != nil {
				t.Fatal(err)
			}
			want := int32(50)
			if concurrent {
				want = 77
			}
			if p.CharOffset != want || p.Version != 4 {
				t.Fatalf("offset/version %+v", p)
			}
		})
	}
}
func TestSwitchRollsBackOnProgressFailure(t *testing.T) {
	f := relocationSeed(t, 50)
	ctx := context.Background()
	name := "fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := f.pool.Exec(ctx, `INSERT INTO chapter_source_overrides(user_id,book_id,chapter_index,source_id,chapter_url) VALUES($1,$2,40,$3,'https://example.com/override')`, f.user, f.book, f.b); err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected progress write failure'; END $$; CREATE TRIGGER %s BEFORE UPDATE ON reading_progress FOR EACH ROW WHEN(OLD.book_id='%s'::uuid) EXECUTE FUNCTION %s()`, name, name, f.book, name)
	if _, err := f.pool.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER %s ON reading_progress; DROP FUNCTION %s()`, name, name))
	})
	if _, err := NewMappingService(f.pool).Switch(ctx, f.user, f.book, f.b, nil, false); err == nil {
		t.Fatal("expected injected error")
	}
	var active uuid.UUID
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT active_source_id,(SELECT count(*) FROM chapter_source_overrides WHERE book_id=$1) FROM books WHERE id=$1`, f.book).Scan(&active, &count); err != nil {
		t.Fatal(err)
	}
	if active != f.a || count != 1 {
		t.Fatal("failed switch did not roll back")
	}
}

func TestContentSurvivesDeferredOffsetWriteFailure(t *testing.T) {
	f := relocationSeed(t, 50)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE reading_progress SET char_offset=0 WHERE book_id=$1`, f.book); err != nil {
		t.Fatal(err)
	}
	name := "fail_fill_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected offset failure'; END $$; CREATE TRIGGER %s BEFORE UPDATE ON reading_progress FOR EACH ROW WHEN(OLD.book_id='%s'::uuid) EXECUTE FUNCTION %s()`, name, name, f.book, name)
	if _, err := f.pool.Exec(ctx, statement); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON reading_progress; DROP FUNCTION IF EXISTS %s()`, name, name))
	})
	cs := NewContentService(f.pool, relocationFetcher{}, nil, nil, nil, nil)
	index := 40
	view, err := cs.Get(ctx, f.user, f.book, &index, nil)
	if err != nil || view == nil || view.Content != strings.Repeat("甲😀", 50) {
		t.Fatalf("body lost to optional write: %v", err)
	}
	p, err := repository.NewProgressRepository().Get(ctx, f.pool, f.user, f.book)
	if err != nil || p.CharOffset != 0 || p.Version != 2 {
		t.Fatalf("failed fill mutated progress: %+v %v", p, err)
	}
}
