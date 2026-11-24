package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

func TestContentPaginationRevisionInvalidatesOldCache(t *testing.T) {
	source := json.RawMessage(`{"ruleContent":{"content":"#content@text"}}`)
	var rules []*repository.ReplaceRule
	old, _ := json.Marshal(struct {
		Pagination string
		Source     json.RawMessage
		Rules      []*repository.ReplaceRule
	}{"complete-pages-v2", source, rules})
	if contentRulesFingerprint(source, rules) == fmt.Sprintf("%x", sha256.Sum256(old)) {
		t.Fatal("old partial cache can still hit")
	}
}

type paginationFetcher struct {
	calls int
	mode  string
}

func (f *paginationFetcher) Get(ctx context.Context, target string, _ http.Header) (*fetcher.Response, error) {
	f.calls++
	if f.calls == 2 {
		switch f.mode {
		case "network":
			return nil, errors.New("upstream disconnected")
		case "timeout":
			return nil, context.DeadlineExceeded
		case "status":
			return &fetcher.Response{StatusCode: 503}, nil
		case "empty":
			return &fetcher.Response{StatusCode: 200, Body: []byte(`<div></div>`)}, nil
		}
	}
	next := ""
	if f.calls == 1 || f.mode == "limit" {
		next = fmt.Sprintf(`<a class="next" href="/%d">next</a>`, f.calls+1)
	}
	if f.mode == "cycle" {
		next = `<a class="next" href="/1">next</a>`
	}
	return &fetcher.Response{StatusCode: 200, FinalURL: target, Body: []byte(`<div id="content">完整正文</div>` + next)}, nil
}
func TestContentPaginationNeverReturnsPartial(t *testing.T) {
	for _, mode := range []string{"good", "network", "timeout", "status", "empty", "cycle", "limit"} {
		t.Run(mode, func(t *testing.T) {
			f := &paginationFetcher{mode: mode}
			text, e := (&ContentService{ft: f}).fetchContent(context.Background(), contentRules{Content: "#content@text", NextContentURL: "a.next@href"}, "https://books.example/1")
			if mode == "good" {
				if e != nil || strings.Count(text, "完整正文") != 2 {
					t.Fatal(text, e)
				}
			} else if e == nil || text != "" {
				t.Fatal("partial content escaped", text, e)
			}
			if mode == "limit" && f.calls != maxContentPages {
				t.Fatal("pagination bound lost")
			}
		})
	}
}

func TestSuduguChapterPagination(t *testing.T) {
	const first = "https://www.sudugu520.com/426/464502.html"
	const second = "https://www.sudugu520.com/426/464502-2.html"
	const third = "https://www.sudugu520.com/426/464502-3.html"
	const nextChapter = "https://www.sudugu520.com/426/464503-2.html"
	const explicitReferer = "https://www.sudugu520.com/426/"
	for _, tc := range []struct {
		name    string
		rule    string
		referer string
	}{
		{"source rule", `@css:.prenext > span:nth-last-child(1) > a[href*="-"][href$=".html"]@href`, ""},
		{"CSS contains", `a:contains(下一页)@href`, ""},
		{"Legado text", `text.下一页@href`, ""},
		{"regular expression", `:<a href="([^"]+)">下一页</a>`, ""},
		{"JavaScript", `@js:(String(result).match(/<a href="([^"]+)">下一页<\/a>/) || [])[1] || ""`, ""},
		{"configured Referer", `@css:a:contains(下一页)@href`, explicitReferer},
		{"no configured rule", "", ""},
		{"empty configured selector result", `@css:.missing a@href`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := &headerRecordingFetcher{}
			srcHeaders := http.Header{"User-Agent": {"YLReader"}}
			if tc.referer != "" {
				srcHeaders.Set("Referer", tc.referer)
			}
			ft.fn = func(_ context.Context, target string, headers http.Header) (*fetcher.Response, error) {
				if headers.Get("User-Agent") != "YLReader" {
					t.Errorf("missing source header for %s", target)
				}
				wantReferer := tc.referer
				if wantReferer == "" {
					wantReferer = map[string]string{second: first, third: second}[target]
				}
				if got := headers.Get("Referer"); got != wantReferer {
					t.Errorf("Referer for %s = %q, want %q", target, got, wantReferer)
				}
				switch target {
				case first:
					return &fetcher.Response{StatusCode: 200, FinalURL: first, Body: []byte(`
						<div class="con"><p>第一段</p><p>首屏第二段</p></div>
						<div class="prenext"><span>上一章</span><a href="/426/#dir">目录</a><span><a href="464502-2.html">下一页</a></span></div>`)}, nil
				case second:
					return &fetcher.Response{StatusCode: 200, FinalURL: second, Body: []byte(`
						<div class="con"><p>第二段</p><p>第二页末段</p></div>
						<div class="prenext"><span><a href="464502.html">上一页</a></span><a href="/426/#dir">目录</a><span><a href="/426/464502-3.html">下一页</a></span></div>`)}, nil
				case third:
					return &fetcher.Response{StatusCode: 200, FinalURL: third, Body: []byte(`
						<div class="con"><p>第三段</p><p>本章末段</p></div>
						<div class="prenext"><span><a href="464502-2.html">上一页</a></span><a href="/426/#dir">目录</a><span><a href="/426/464503-2.html#top">下一章</a></span></div>`)}, nil
				default:
					return nil, fmt.Errorf("unexpected page %s", target)
				}
			}
			svc := &ContentService{ft: ft, sb: sandbox.New(sandbox.Config{}, nil)}
			got, err := svc.fetchContentWithHeaders(t.Context(), contentRules{
				Content: "@css:.con > p@text", NextContentURL: tc.rule,
			}, first, srcHeaders, func(context.Context) (string, error) { return nextChapter, nil })
			if err != nil || got != "第一段\n首屏第二段\n第二段\n第二页末段\n第三段\n本章末段" || len(ft.calls) != 3 {
				t.Fatalf("content=%q, requests=%d, error=%v", got, len(ft.calls), err)
			}
			if got := srcHeaders.Get("Referer"); got != tc.referer {
				t.Errorf("source headers were changed: Referer = %q", got)
			}
		})
	}
}
