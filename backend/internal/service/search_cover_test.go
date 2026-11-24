package service

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

type searchBatchFetcher struct {
	active, peak, calls atomic.Int32
}

func (f *searchBatchFetcher) Get(ctx context.Context, target string, _ http.Header) (*fetcher.Response, error) {
	f.calls.Add(1)
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for peak := f.peak.Load(); active > peak; peak = f.peak.Load() {
		if f.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Millisecond):
	}
	body := `<article><a href="/book">相似书名</a></article><article><a href="/exact">书名</a></article>`
	return &fetcher.Response{StatusCode: 200, FinalURL: target, Body: []byte(body)}, nil
}

func TestSearchPoolProcessesMoreThanTwoHundredAndRanksBeforeTruncation(t *testing.T) {
	ft := &searchBatchFetcher{}
	service := NewSearchService(nil, ft, nil)
	sources := make([]*repository.BookSource, 233)
	for i := range sources {
		sources[i] = &repository.BookSource{ID: uuid.New(), Weight: int32(i), BookSourceURL: "https://books.example/", Rules: []byte(`{"searchUrl":"/search?q={{key}}","ruleSearch":{"bookList":"article","name":"a@text","bookUrl":"a@href"}}`)}
	}
	out := &SearchOutcome{}
	service.searchSources(t.Context(), sources, "书名", 1, 1, out, nil, nil)
	if len(out.Groups) != len(sources) || len(out.Failures) != 0 || ft.calls.Load() != int32(len(sources)) || ft.peak.Load() > searchConcurrency {
		t.Fatalf("groups=%d failures=%d calls=%d peak=%d", len(out.Groups), len(out.Failures), ft.calls.Load(), ft.peak.Load())
	}
	if out.Groups[0].SourceID != sources[len(sources)-1].ID || len(out.Groups[0].Items) != 1 || out.Groups[0].Items[0].Name != "书名" {
		t.Fatalf("ranking lost the exact match: %+v", out.Groups[0])
	}
}

func TestLegacyAndDynamicSearchURL(t *testing.T) {
	for _, charset := range []string{"gbk", "gb2312", "gb18030"} {
		r, err := parseSearchRequest(`/search/{{page}},{"method":"POST","body":"searchkey={{key}}","charset":"`+charset+`"}`, "中文", 2)
		if err != nil || r.url != "/search/2" || r.body != "searchkey=%D6%D0%CE%C4" {
			t.Fatalf("%s: %+v %v", charset, r, err)
		}
	}
	s := &SearchService{sb: sandbox.New(sandbox.Config{}, nil)}
	for _, rule := range []string{
		`@js: baseUrl + "search?q=" + encodeURIComponent(key) + "&p=" + page`,
		`search<js> baseUrl + result + "?q=" + encodeURIComponent(key) + "&p=" + page </js>`,
	} {
		r, err := s.buildSearchRequest(t.Context(), "https://books.example/", rule, "a&b", 2)
		if err != nil || r.url != "https://books.example/search?q=a%26b&p=2" {
			t.Fatalf("dynamic request: %+v %v", r, err)
		}
	}
}

func TestCoverCandidatesUseFinalResponseURL(t *testing.T) {
	base := "https://books.example/detail/index"
	bodies := map[string]string{
		`<meta property="og:image" content="nocover.jpg"><img src="cover/1.jpg">`:                            "https://books.example/detail/cover/1.jpg",
		`<img src="/logo.png"><img class="cover" src="data:image/png,blank" data-src="//img.example/1.jpg">`: "https://img.example/1.jpg",
		`<img src="/fallback.png"><meta content="/cover/1.jpg" name="twitter:image">`:                        "https://books.example/cover/1.jpg",
	}
	for body, want := range bodies {
		if got := derefString(extractBookCover([]byte(body), base)); got != want {
			t.Fatalf("%s: got %s, want %s", strings.TrimSpace(body), got, want)
		}
	}
}

type heldSearchFetcher struct {
	release <-chan struct{}
}

func (f heldSearchFetcher) Get(ctx context.Context, target string, _ http.Header) (*fetcher.Response, error) {
	if strings.Contains(target, "slow.example") {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &fetcher.Response{FinalURL: target, Body: []byte(`<article><a href="/book">书名</a></article>`)}, nil
}

func TestSearchPublishesAvailableGroupBeforeSlowSource(t *testing.T) {
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	svc := NewSearchService(nil, heldSearchFetcher{release: release}, nil)
	sources := []*repository.BookSource{
		{ID: uuid.New(), BookSourceURL: "https://slow.example/", Weight: 10},
		{ID: uuid.New(), BookSourceURL: "https://fast.example/"},
	}
	for _, source := range sources {
		source.Rules = []byte(`{"searchUrl":"/search?q={{key}}","ruleSearch":{"bookList":"article","name":"a@text","bookUrl":"a@href"}}`)
	}
	out := &SearchOutcome{}
	published, finished := make(chan uuid.UUID, 2), make(chan struct{})
	probes := make(chan int, 2)
	go func() {
		defer close(finished)
		svc.searchSources(ctx, sources, "书名", 1, 20, out, func(group GroupResult) {
			published <- group.SourceID
		}, func(count int) { probes <- count })
	}()
	select {
	case got := <-published:
		if got != sources[1].ID {
			t.Fatalf("first group was not from the fast source: %s", got)
		}
	case <-ctx.Done():
		t.Fatal("the fast group was held until the slow source returned")
	}
	select {
	case count := <-probes:
		if count != 1 {
			t.Fatalf("expected first probe before slow source, got %d", count)
		}
	case <-ctx.Done():
		t.Fatal("the first probe was held until the slow source returned")
	}
	close(release)
	<-finished
	select {
	case count := <-probes:
		if count != 2 {
			t.Fatalf("expected two probes, got %d", count)
		}
	default:
		t.Fatal("missing final probe")
	}
	if len(out.Groups) != 2 || out.Groups[0].SourceID != sources[0].ID {
		t.Fatalf("unexpected final ranking: %+v", out.Groups)
	}
}
