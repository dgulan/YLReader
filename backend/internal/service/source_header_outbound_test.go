package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseSourceHeaders_SanitizesAndFilters(t *testing.T) {
	raw := json.RawMessage(`{
		"user-agent": "Legado/3.0",
		"Cookie": "sid=abc123",
		"X-Custom-Token": "tok-1",
		"Host": "evil.example.com",
		"Content-Length": "999",
		"Transfer-Encoding": "chunked",
		"Proxy-Authorization": "Basic xxx",
		"Referer": "https://site.example.com/\r\nX-Injected: 1",
		"Accept\r\nBad": "text/html",
		"X-Null": "a\u0000b",
		"X-NonString": 123
	}`)
	h := parseSourceHeaders(raw)
	if h == nil {
		t.Fatal("expected non-nil headers")
	}
	if got := h.Get("User-Agent"); got != "Legado/3.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := h.Get("Cookie"); got != "sid=abc123" {
		t.Fatalf("Cookie = %q", got)
	}
	if got := h.Get("X-Custom-Token"); got != "tok-1" {
		t.Fatalf("X-Custom-Token = %q", got)
	}
	for _, forbidden := range []string{"Host", "Content-Length", "Transfer-Encoding", "Proxy-Authorization", "Referer", "X-Null", "X-NonString"} {
		if got := h.Get(forbidden); got != "" {
			t.Fatalf("forbidden/unsafe header %s leaked: %q", forbidden, got)
		}
	}
}

func TestMergeHeaders_OverridePrecedence(t *testing.T) {
	base := http.Header{
		"User-Agent": []string{"BaseUA"},
		"Cookie":     []string{"a=1"},
	}
	override := http.Header{
		"User-Agent": []string{"SearchUA"},
		"Accept":     []string{"application/json"},
	}
	merged := mergeHeaders(base, override)
	if got := merged.Get("User-Agent"); got != "SearchUA" {
		t.Fatalf("User-Agent = %q, want SearchUA", got)
	}
	if got := merged.Get("Cookie"); got != "a=1" {
		t.Fatalf("Cookie = %q, want a=1", got)
	}
	if got := merged.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
}

func TestTruncatePreviewRunes(t *testing.T) {
	text := strings.Repeat("阅😀", 300) // 600 runes
	got := truncatePreviewRunes(text, 512)
	if utf8.RuneCountInString(got) != 512 {
		t.Fatalf("rune count = %d, want 512", utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated preview is not valid UTF-8")
	}
	if short := truncatePreviewRunes("短文本", 512); short != "短文本" {
		t.Fatalf("short = %q", short)
	}
}
