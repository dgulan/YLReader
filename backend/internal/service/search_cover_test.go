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
	service.searchSources(t.Context(), sources, "书名", 1, 1, out)
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
