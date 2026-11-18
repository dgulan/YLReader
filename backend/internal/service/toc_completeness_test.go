package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
)

type tocCompleteFetcher struct {
	page  func(context.Context, string) (*fetcher.Response, error)
	calls int
}

func (f *tocCompleteFetcher) Get(ctx context.Context, target string, _ http.Header) (*fetcher.Response, error) {
	f.calls++
	return f.page(ctx, target)
}
func tocCompletePage(name, next string) string {
	body := fmt.Sprintf(`<ul><li><a href="/%s">%s</a></li></ul>`, name, name)
	if next != "" {
		body += fmt.Sprintf(`<a class="next" href="%s">下一页</a>`, next)
	}
	return body
}
func tocCompleteRules() tocRules {
	return tocRules{ChapterList: "li", ChapterName: "a@text", ChapterUrl: "a@href", NextTocURL: "a.next@href"}
}
func TestTOCIncompleteNeverReturnsPartialRows(t *testing.T) {
	for _, kind := range []string{"network", "http", "empty", "bad chapter", "bad next", "cycle", "self fragment", "redirect cycle", "wait timeout", "canceled", "next rule"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rules := tocCompleteRules()
			expected := sandbox.ErrRuleExecution
			ft := &tocCompleteFetcher{page: func(_ context.Context, target string) (*fetcher.Response, error) {
				body := tocCompletePage("a", "/page2")
				status := 200
				final := target
				if target == "https://example.com/page2" {
					body = tocCompletePage("b", "")
					switch kind {
					case "network":
						return nil, fetcher.ErrUpstreamError
					case "http":
						status = 503
					case "empty":
						body = "<div>没有匹配目录</div>"
					case "bad chapter":
						body = `<li><a>缺少地址</a></li>`
					case "bad next":
						body = tocCompletePage("b", "javascript:void(0)")
					case "cycle":
						body = tocCompletePage("b", "/book")
					case "redirect cycle":
						final = "https://example.com/book"
					case "canceled":
						cancel()
					}
				}
				if kind == "self fragment" {
					body = tocCompletePage("a", "/book#end")
				}
				return &fetcher.Response{StatusCode: status, FinalURL: final, Body: []byte(body)}, nil
			}}
			s := &TOCService{ft: ft, sleep: func(context.Context, time.Duration) error { return nil }}
			switch kind {
			case "network", "http":
				expected = fetcher.ErrUpstreamError
			case "wait timeout":
				expected = context.DeadlineExceeded
				s.sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }
			case "canceled":
				expected = context.Canceled
			case "next rule":
				rules.NextTocURL = "@js:throw new Error('fixture')"
			}
			got, err := s.fetchTOC(ctx, rules, "https://example.com/book")
			if !errors.Is(err, expected) || len(got) != 0 {
				t.Fatalf("partial rows=%d error=%v expected=%v", len(got), err, expected)
			}
			if ft.calls > 2 {
				t.Fatal("unexpected continuation after failure", ft.calls)
			}
		})
	}
}
func TestTOCPageLimitRequiresNormalEnd(t *testing.T) {
	for _, extra := range []bool{false, true} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			ft := &tocCompleteFetcher{}
			ft.page = func(_ context.Context, target string) (*fetcher.Response, error) {
				next := ""
				if ft.calls < maxTOCPages || extra {
					next = fmt.Sprintf("/page%d", ft.calls+1)
				}
				return &fetcher.Response{StatusCode: 200, FinalURL: target, Body: []byte(tocCompletePage(fmt.Sprint(ft.calls), next))}, nil
			}
			s := &TOCService{ft: ft, sleep: func(context.Context, time.Duration) error { return nil }}
			got, err := s.fetchTOC(context.Background(), tocCompleteRules(), "https://example.com/book")
			if ft.calls != maxTOCPages {
				t.Fatal(ft.calls)
			}
			if extra {
				if !errors.Is(err, sandbox.ErrRuleExecution) || len(got) != 0 {
					t.Fatal(len(got), err)
				}
			} else if err != nil || len(got) != maxTOCPages {
				t.Fatal(len(got), err)
			}
		})
	}
}
