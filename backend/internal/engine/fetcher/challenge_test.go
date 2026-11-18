package fetcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type challengeTransport func(*http.Request) (*http.Response, error)

func (f challengeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type challengeBody struct {
	io.Reader
	closed bool
}

func (b *challengeBody) Close() error { b.closed = true; return nil }

func TestChallengeClassificationAndAttempts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		header    http.Header
		body      string
		method    string
		challenge bool
		attempts  int
	}{
		{"explicit 200", 200, http.Header{"Cf-Mitigated": {"challenge"}}, "page", "GET", true, 1},
		{"explicit POST 503", 503, http.Header{"Cf-Mitigated": {"challenge"}}, "page", "POST", true, 1},
		{"fallback 503", 503, http.Header{"Server": {"cloudflare"}}, `<script src="/cdn-cgi/challenge-platform/run"></script>`, "GET", true, 1},
		{"fallback 403", 403, http.Header{"Server": {"Cloudflare"}}, "Just a moment... Enable JavaScript and cookies to continue", "GET", true, 1},
		{"cf ray insufficient", 403, http.Header{"Cf-Ray": {"abc"}}, "Forbidden", "GET", false, 1},
		{"ordinary forbidden", 403, http.Header{"Server": {"cloudflare"}}, "Account denied", "GET", false, 1},
		{"ordinary 503 still retries", 503, http.Header{"Server": {"cloudflare"}}, "Maintenance", "GET", false, 3},
		{"book text not challenge", 200, http.Header{"Server": {"cloudflare"}}, "Just a moment...", "GET", false, 1},
		{"unrelated error page", 403, http.Header{"Server": {"nginx"}}, "cf-browser-verification", "GET", false, 1},
		{"JSON error not HTML challenge", 403, http.Header{"Server": {"cloudflare"}, "Content-Type": {"application/json"}}, `{"path":"/cdn-cgi/challenge-platform/"}`, "GET", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			bodies := []*challengeBody{}
			f := New(Config{backoff: func(int) time.Duration { return 0 }})
			f.client = &http.Client{Transport: challengeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				b := &challengeBody{Reader: strings.NewReader(tc.body)}
				bodies = append(bodies, b)
				return &http.Response{StatusCode: tc.status, Header: tc.header, Body: b, Request: r}, nil
			})}
			resp, err := f.Fetch(context.Background(), tc.method, "https://example.com/search", nil, nil)
			if errors.Is(err, ErrChallengeRequired) != tc.challenge || calls != tc.attempts {
				t.Fatalf("challenge=%v calls=%d error=%v", tc.challenge, calls, err)
			}
			if tc.challenge && (!errors.Is(err, ErrUpstreamError) || resp != nil) {
				t.Fatal("challenge mapping or page rejection lost")
			}
			for _, b := range bodies {
				if !b.closed {
					t.Fatal("response leaked")
				}
			}
		})
	}
}
