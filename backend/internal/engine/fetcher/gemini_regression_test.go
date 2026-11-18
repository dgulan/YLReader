package fetcher

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

type auditTransport func(*http.Request) (*http.Response, error)

func (f auditTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGeminiRetryBody(t *testing.T) {
	for _, replayable := range []bool{true, false} {
		f := New(Config{backoff: func(int) time.Duration { return 0 }})
		calls := 0
		f.client.Transport = auditTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			data, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if string(data) != "正文 request payload" {
				t.Fatal("body changed", string(data))
			}
			code := 503
			if calls > 1 {
				code = 200
			}
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
		})
		var body io.Reader = strings.NewReader("正文 request payload")
		if !replayable {
			body = io.NopCloser(body)
		}
		_, err := f.Fetch(context.Background(), "PUT", "https://example.com", nil, body)
		if replayable && (err != nil || calls != 2) {
			t.Fatal(calls, err)
		}
		if !replayable && (err == nil || calls != 1) {
			t.Fatal("non-replayable body retried", calls, err)
		}
	}
}
func TestGeminiCharsetFallbackAndExplicitLabel(t *testing.T) {
	for _, tc := range []struct{ body, ct, want string }{
		{"中文裸正文", "", "中文裸正文"},
		{`<meta charset="gbk">中文`, "text/html", `<meta charset="gbk">中文`},
	} {
		raw, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		got, err := New(Config{}).decode(&http.Response{Header: http.Header{"Content-Type": {tc.ct}}, Body: io.NopCloser(bytes.NewReader(raw))}, "https://example.com")
		if err != nil || string(got.Body) != tc.want {
			t.Fatalf("decoded=%q error=%v", got.Body, err)
		}
	}
	got, err := New(Config{}).decode(&http.Response{Header: http.Header{"Content-Type": {"text/plain; charset=iso-8859-1"}}, Body: io.NopCloser(bytes.NewReader([]byte{0xe9}))}, "https://example.com")
	if err != nil || string(got.Body) != "é" {
		t.Fatal("explicit western charset changed", err)
	}
}
