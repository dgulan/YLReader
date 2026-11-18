package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/repository"
)

type relativeSearchFetcher struct{ target string }

func (f *relativeSearchFetcher) Get(_ context.Context, u string, _ http.Header) (*fetcher.Response, error) {
	f.target = u
	return nil, fetcher.ErrSSRFBlocked
}
func TestSearchResolvesRelativeURLBeforeSafeFetcher(t *testing.T) {
	for _, tc := range []struct{ base, ref, want string }{
		{"https://example.com/base/", "/search?q={{key}}", "https://example.com/search?q=a+b"},
		{"https://example.com/base/", "find?q={{key}}", "https://example.com/base/find?q=a+b"},
		{"https://example.com/", "https://other.example/find?q={{key}}", "https://other.example/find?q=a+b"},
		{"https://example.com/", "http://127.0.0.1/find?q={{key}}", "http://127.0.0.1/find?q=a+b"},
	} {
		f := &relativeSearchFetcher{}
		s := &SearchService{ft: f}
		rules, _ := json.Marshal(map[string]any{"searchUrl": tc.ref, "ruleSearch": map[string]string{"bookList": "a", "name": "text", "bookUrl": "href"}})
		_, err := s.searchOne(context.Background(), &repository.BookSource{BookSourceURL: tc.base, Rules: rules}, "a b", 1, 20)
		if f.target != tc.want || !errors.Is(err, fetcher.ErrSSRFBlocked) {
			t.Fatalf("got %q %v", f.target, err)
		}
	}
}
