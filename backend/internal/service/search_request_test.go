package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

func TestSearchRequestOptions(t *testing.T) {
	r, err := parseSearchRequest(`/find,{"method":"POST","body":"q={{key}}&page={{page}}","headers":{"User-Agent":"reader-test","cookie":""},"charset":"utf-8"}`, "三 国&x", 2)
	if err != nil || r.method != "POST" || r.url != "/find" || r.body != "q=%E4%B8%89+%E5%9B%BD%26x&page=2" || r.header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.header.Get("User-Agent") != "reader-test" || r.header.Get("Cookie") != "" {
		t.Fatal(r, err)
	}
	r, err = parseSearchRequest(`/find?q={{key}} , {"charset":"UTF8"}`, "a b", 1)
	if err != nil || r.method != "GET" || r.url != "/find?q=a+b" {
		t.Fatal(r, err)
	}
}

func TestSearchUnsupportedOptionsFailBeforeNetwork(t *testing.T) {
	for _, rule := range []string{
		`<JS>java.ajax('https://example.org')</JS>/find`, `@js:result`,
		`/find<js>result</js>`,
		`/find,{"method":"DELETE"}`, `/find,{"method":null}`, `/find,{"body":"q=x"}`,
		`/find,{"proxy":"http://127.0.0.1"}`,
		`/find,{"method":"POST","method":"GET"}`, `/find,{"headers":{"User-Agent":"a","user-agent":"b"}}`,
		`/find,{"headers":{"Cookie":"secret"}}`, `/find,{"headers":{"Authorization":"secret"}}`, `/find,{"headers":{"Host":"private"}}`,
		`/find,{"headers":{"User-Agent":"bad\r\nInjected: yes"}}`, `/find,{"headers":null}`, `/find,{"headers":{"Accept":null}}`,
		`/find,{"method":"POST","headers":{"Content-Type":"application/json"}}`,
		`/find,{"method":"POST","headers":{"Content-Type":"application/x-www-form-urlencoded; charset=gbk"}}`,
		`/find,{"method":"POST"} {}`, `/find,{"method":`,
	} {
		t.Run(rule, func(t *testing.T) {
			f := &relativeSearchFetcher{}
			raw, _ := json.Marshal(map[string]any{"searchUrl": rule, "ruleSearch": map[string]string{"bookList": "a"}})
			_, err := (&SearchService{ft: f}).searchOne(context.Background(), &repository.BookSource{BookSourceURL: "https://example.org", Rules: raw}, "book", 1, 20)
			if !errors.Is(err, sandbox.ErrUnsupportedRule) || f.target != "" {
				t.Fatal("unsupported request reached network", f.target, err)
			}
		})
	}
	_, err := parseSearchRequest(strings.Repeat("a", (64<<10)+1), "x", 1)
	if !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatal(err)
	}
	_, err = parseSearchRequest(`/find,{"method":"POST","body":"{{key}}"}`, strings.Repeat("中", 8000), 1)
	if !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatal("expanded body limit missing", err)
	}
}

type searchPostFetcher struct {
	calls                int
	target, method, body string
	header               http.Header
}

func (f *searchPostFetcher) Get(context.Context, string, http.Header) (*fetcher.Response, error) {
	panic("POST downgraded to GET")
}
func (f *searchPostFetcher) Fetch(_ context.Context, method, target string, header http.Header, body io.Reader) (*fetcher.Response, error) {
	f.calls++
	f.target = target
	f.method = method
	f.header = header.Clone()
	b, _ := io.ReadAll(body)
	f.body = string(b)
	return nil, fetcher.ErrSSRFBlocked
}
func TestSearchPOSTUsesSafeFetcherOnce(t *testing.T) {
	f := &searchPostFetcher{}
	raw := json.RawMessage(`{"searchUrl":"/find,{\"method\":\"POST\",\"body\":\"q={{key}}\"}","ruleSearch":{"bookList":"a"}}`)
	_, err := (&SearchService{ft: f}).searchOne(context.Background(), &repository.BookSource{BookSourceURL: "https://books.example/base/", Rules: raw}, "a b", 1, 20)
	if !errors.Is(err, fetcher.ErrSSRFBlocked) || f.calls != 1 || f.method != "POST" || f.target != "https://books.example/find" || f.body != "q=a+b" {
		t.Fatal(f, err)
	}
	// A Get-only adapter must never silently change the method.
	getOnly := &relativeSearchFetcher{}
	_, err = (&SearchService{ft: getOnly}).searchOne(context.Background(), &repository.BookSource{BookSourceURL: "https://books.example", Rules: raw}, "a", 1, 20)
	if !errors.Is(err, sandbox.ErrUnsupportedRule) || getOnly.target != "" {
		t.Fatal(getOnly, err)
	}
}

func TestSearchDynamicGBKPostWithMixedQuotedOptions(t *testing.T) {
	rule := `<js>java.put("x", "/find"); java.get("x")</js>,{
		'method': "POST", "charset": 'gbk', 'body': "q={{key}}&p={{page}}",
		'headers': {"User-Agent": 'fixture'}
	}`
	data, _ := json.Marshal(map[string]any{"searchUrl": rule, "ruleSearch": map[string]string{"bookList": "article"}})
	ft := &searchPostFetcher{}
	s := &SearchService{ft: ft, sb: sandbox.New(sandbox.Config{}, nil)}
	_, err := s.searchOne(t.Context(), &repository.BookSource{BookSourceURL: "https://books.example", Rules: data}, "中文", 3, 20)
	if !errors.Is(err, fetcher.ErrSSRFBlocked) || ft.calls != 1 || ft.body != "q=%D6%D0%CE%C4&p=3" || ft.header.Get("User-Agent") != "fixture" {
		t.Fatalf("unexpected request: %+v, %v", ft, err)
	}
	enc, err := s.sb.Eval(t.Context(), `java.encodeURI("中文 空", "GBK")`, nil)
	if err != nil || enc != "%D6%D0%CE%C4%20%BF%D5" {
		t.Fatal(enc, err)
	}
}

type detailFallbackFetcher struct{ finalURL string }

func (f detailFallbackFetcher) Get(_ context.Context, target string, headers http.Header) (*fetcher.Response, error) {
	if headers.Get("User-Agent") != "source-fixture" || headers.Get("Referer") != "https://books.example/" {
		return nil, errors.New("source headers missing")
	}
	final := f.finalURL
	if final == "" {
		final = target
	}
	return &fetcher.Response{StatusCode: 200, FinalURL: final, Body: []byte(`<h1>从金刚功开始肉身成圣</h1><span class="author">甲</span><img src="../cover.jpg">`)}, nil
}

func TestSearchDetailFallbackUsesFinalResponse(t *testing.T) {
	src := &repository.BookSource{BookSourceURL: "https://books.example", Header: []byte(`{"User-Agent":"source-fixture","Referer":"https://books.example/"}`),
		Rules: []byte(`{"searchUrl":"/find?q={{key}}","ruleSearch":{"bookList":"article"},"ruleBookInfo":{"name":"h1@text","author":".author@text","coverUrl":"img@src"}}`)}
	s := &SearchService{ft: detailFallbackFetcher{"https://books.example/book/10"}}
	items, err := s.searchOne(t.Context(), src, "从金刚功开始肉身成圣", 1, 20)
	if err != nil || len(items) != 1 || items[0].BookURL != "https://books.example/book/10" || items[0].CoverURL != "https://books.example/cover.jpg" || items[0].Author != "甲" {
		t.Fatal(items, err)
	}
	s.ft = detailFallbackFetcher{}
	items, err = s.searchOne(t.Context(), src, "无关的书", 1, 20)
	if err != nil || len(items) != 0 {
		t.Fatal("unrelated non-redirect matched", items, err)
	}
}
