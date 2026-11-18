package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"yreader/backend/internal/engine/fetcher"
)

type legacyObjectContentFetcher struct{ calls int }

func (f *legacyObjectContentFetcher) Get(_ context.Context, target string, _ http.Header) (*fetcher.Response, error) {
	f.calls++
	return &fetcher.Response{StatusCode: 200, FinalURL: target, Body: []byte(`<div id="content"><p>原创验收段落。</p></div>`)}, nil
}
func TestLegadoObjectContentRules(t *testing.T) {
	raw := json.RawMessage(`{"ruleContent":{"content":"id.content@html","nextContentUrl":"text.下一页@href","replaceRegex":"旧文本##新文本"}}`)
	rules := parseContentRules(raw)
	if rules.Content != "id.content@html" || rules.NextContentURL != "text.下一页@href" || rules.ReplaceRegex != "旧文本##新文本" {
		t.Fatalf("object rules lost: %#v", rules)
	}
	f := &legacyObjectContentFetcher{}
	text, err := (&ContentService{ft: f}).fetchContent(context.Background(), rules, "https://books.example/chapter/1")
	if err != nil || f.calls != 1 || !strings.Contains(text, "原创验收段落。") {
		t.Fatal("legacy content never fetched or parsed", f.calls, err)
	}
}
func TestLegadoContentRuleCompatibilityAndPriority(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"content":{"content":"canonical"},"ruleContent":{"content":"legacy"}}`, "canonical"},
		{`{"ruleContent":"legacy-string"}`, "legacy-string"},
		{`{"ruleContent":{"content":null}}`, ""},
		{`{"ruleContent":{"content":42}}`, ""},
		{`{"ruleContent":null}`, ""},
	} {
		if got := parseContentRules(json.RawMessage(tc.raw)); got.Content != tc.want {
			t.Fatal(tc.raw, got.Content, tc.want)
		}
	}
}
