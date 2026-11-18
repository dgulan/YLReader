package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"yreader/backend/internal/engine/fetcher"
)

type tocAdapterFetcher struct{ calls []string }

func (f *tocAdapterFetcher) Get(_ context.Context, target string, _ http.Header) (*fetcher.Response, error) {
	f.calls = append(f.calls, target)
	pages := map[string]string{
		"https://books.example/toc":   `<div id="list"><dl><dt>最新章节</dt><dd><a href="/c3">第3章</a></dd><dt>作品正文</dt><dd><a href="/c1">第1章</a></dd><dd><a href="/c2">第2章</a></dd></dl></div><a href="/toc/2">下一页</a>`,
		"https://books.example/toc/2": `<div id="list"><dl><dt>最新章节</dt><dd><a href="/c3">第3章</a></dd><dt>作品正文</dt><dd><a href="/c3">第3章</a></dd></dl></div>`,
	}
	body, ok := pages[target]
	if !ok {
		return nil, fmt.Errorf("unexpected fixture request")
	}
	return &fetcher.Response{StatusCode: 200, FinalURL: target, Body: []byte(body)}, nil
}
func TestTOCExplicitPaginationAdapter(t *testing.T) {
	f := &tocAdapterFetcher{}
	s := &TOCService{ft: f, sleep: func(context.Context, time.Duration) error { return nil }}
	rules := tocRules{ChapterList: `//div[@id='list']//dt[contains(.,'正文')]/following-sibling::dd`, ChapterName: "a@text", ChapterUrl: "a@href", NextTocURL: `//a[contains(normalize-space(.),'下一页')]/@href`}
	got, err := s.fetchTOC(context.Background(), rules, "https://books.example/toc")
	if err != nil || len(got) != 3 || len(f.calls) != 2 {
		t.Fatalf("chapters=%+v calls=%v error=%v", got, f.calls, err)
	}
	for i, ch := range got {
		if ch.Title != fmt.Sprintf("第%d章", i+1) || ch.ChapterIndex == nil || *ch.ChapterIndex != int32(i) || ch.URL != fmt.Sprintf("https://books.example/c%d", i+1) {
			t.Fatal(i, ch)
		}
	}
}
func TestTOCDoesNotGuessUnspecifiedPagination(t *testing.T) {
	f := &tocAdapterFetcher{}
	s := &TOCService{ft: f, sleep: func(context.Context, time.Duration) error { return nil }}
	got, err := s.fetchTOC(context.Background(), tocRules{ChapterList: "id.list@dd", ChapterName: "a@text", ChapterUrl: "a@href"}, "https://books.example/toc")
	if err != nil || len(got) != 3 || len(f.calls) != 1 || got[0].Title != "第3章" {
		t.Fatal(len(got), f.calls, err)
	}
}
