package service

import (
	"testing"
)

func TestParseContentRules(t *testing.T) {
	nested := []byte(`{"content":{"content":"div#c@html","nextContentUrl":"a.next@href","replaceRegex":"a.*##"}}`)
	got := parseContentRules(nested)
	if got.Content != "div#c@html" || got.NextContentURL != "a.next@href" || got.ReplaceRegex != "a.*##" {
		t.Fatalf("nested parse = %+v", got)
	}

	flat := []byte(`{"ruleContent":".c@text","ruleContentNextContentUrl":"#next@href","ruleContentReplaceRegex":"x##y"}`)
	got = parseContentRules(flat)
	if got.Content != ".c@text" || got.NextContentURL != "#next@href" || got.ReplaceRegex != "x##y" {
		t.Fatalf("flat parse = %+v", got)
	}

	if empty := parseContentRules(nil); empty != (contentRules{}) {
		t.Fatalf("nil parse = %+v", empty)
	}
}

func TestNormalizeContent(t *testing.T) {
	in := "<p>第一段</p><br/><p>第二段\r\n\r\n\r\n第三段  </p>"
	want := "第一段\n第二段\n\n第三段"
	if got := normalizeContent(in); got != want {
		t.Fatalf("normalizeContent = %q, want %q", got, want)
	}
}

func TestApplySourceReplaceRegex(t *testing.T) {
	if got := applySourceReplaceRegex("abc123def", `\d+`); got != "abcdef" {
		t.Fatalf("plain regex = %q", got)
	}
	if got := applySourceReplaceRegex("广告词广告词", "广告词##广告"); got != "广告广告" {
		t.Fatalf("with replacement = %q", got)
	}
	if got := applySourceReplaceRegex("keep", "(["); got != "keep" {
		t.Fatalf("invalid regex must not change content: %q", got)
	}
}
