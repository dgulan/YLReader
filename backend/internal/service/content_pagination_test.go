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
	"yreader/backend/internal/repository"
)

func TestContentPaginationRevisionInvalidatesOldCache(t *testing.T) {
	source := json.RawMessage(`{"ruleContent":{"content":"#content@text"}}`)
	var rules []*repository.ReplaceRule
	old, _ := json.Marshal(struct {
		Source json.RawMessage
		Rules  []*repository.ReplaceRule
	}{source, rules})
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
