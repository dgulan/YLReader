//go:build integration

package service

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/pkg/cache"
	"yreader/backend/internal/repository"
)

type sourceAdmissionProbe struct {
	source            string
	calls, domainOnly int
}

func (p *sourceAdmissionProbe) Take(context.Context, string, time.Duration) (time.Duration, error) {
	p.domainOnly++
	return 0, errors.New("test stop")
}
func (p *sourceAdmissionProbe) TakeSource(_ context.Context, _ string, source, _ string, n int, window, _ time.Duration) (time.Duration, error) {
	if n == 1 && window == 0 {
		p.source = source
	}
	p.calls++
	return 0, errors.New("test stop")
}
func (*sourceAdmissionProbe) ReleaseSource(context.Context, string, string) error { return nil }

func TestSourceAdmissionAllServiceEntrypoints(t *testing.T) {
	var identity string
	// One source identity must be shared across independently constructed services.
	f := relocationSeed(t, 2)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `UPDATE book_sources SET concurrent_rate='1',rules='{"search":{"searchUrl":"https://example.com/search","bookList":"a"},"toc":{"chapterList":"a","chapterName":"text","chapterUrl":"href"},"content":{"content":"body@text"}}' WHERE id=$1`, f.a)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"search", "toc", "content", "worker", "debug"} {
		t.Run(entry, func(t *testing.T) {
			probe := &sourceAdmissionProbe{}
			ft := fetcher.New(fetcher.Config{DomainLimiter: fetcher.NewDomainLimiter(probe)})
			cs := NewContentService(f.pool, ft, nil, cache.NewMemory(), nil, nil)
			var got error
			switch entry {
			case "search":
				out, e := NewSearchService(f.pool, ft, nil).Search(ctx, f.user, "test", []uuid.UUID{f.a}, 1, 20)
				if e != nil {
					t.Fatal(e)
				}
				if len(out.Failures) != 1 {
					t.Fatal("expected isolated source failure", out)
				}
				got = out.Failures[0].Err
			case "toc":
				_, got = NewTOCService(f.pool, ft, nil).GetChapters(ctx, f.user, f.book, nil, true)
			case "content":
				idx := 0
				_, got = cs.Get(ctx, f.user, f.book, &idx, nil)
			case "worker":
				src, e := repository.NewSourceRepository().Get(ctx, f.pool, f.user, f.a)
				if e != nil {
					t.Fatal(e)
				}
				ch, e := repository.NewChapterRepository().GetByIndex(ctx, f.pool, f.user, f.book, f.a, 0)
				if e != nil {
					t.Fatal(e)
				}
				got = cs.WarmChapterForSource(ctx, f.user, f.book, src, ch)
			case "debug":
				_, _, got = NewDebugService(f.pool, ft, nil).Debug(ctx, f.user, f.a, "content", "body@text", nil, nil, 1024)
			}
			if !errors.Is(got, fetcher.ErrRateLimit) || probe.calls != 1 || probe.domainOnly != 0 || probe.source == "" {
				t.Fatal("source policy not propagated", probe, got)
			}
			if identity == "" {
				identity = probe.source
			} else if identity != probe.source {
				t.Fatal("services use different source identities")
			}
		})
	}
}
