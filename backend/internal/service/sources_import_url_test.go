package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/repository"
)

func TestFetchSourcesFromURLValidationAndSSRF(t *testing.T) {
	t.Run("rejects invalid scheme and empty url", func(t *testing.T) {
		svc := NewSourceService(nil)
		for _, bad := range []string{"", "   ", "file:///etc/passwd", "ftp://example.com/s.json"} {
			if _, err := svc.FetchSourcesFromURL(context.Background(), bad); !errors.Is(err, repository.ErrInvalidArgument) {
				t.Fatalf("url %q: err = %v, want ErrInvalidArgument", bad, err)
			}
		}
	})

	t.Run("propagates SSRF blocked error from fetcher", func(t *testing.T) {
		ft := &headerRecordingFetcher{
			fn: func(context.Context, string, http.Header) (*fetcher.Response, error) {
				return nil, fetcher.ErrSSRFBlocked
			},
		}
		svc := NewSourceService(nil).WithFetcher(ft)
		if _, err := svc.FetchSourcesFromURL(context.Background(), "http://127.0.0.1/sources.json"); !errors.Is(err, fetcher.ErrSSRFBlocked) {
			t.Fatalf("err = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("rejects upstream 4xx/5xx and returns valid payload", func(t *testing.T) {
		ft := &headerRecordingFetcher{
			fn: func(_ context.Context, target string, _ http.Header) (*fetcher.Response, error) {
				if strings.HasSuffix(target, "/404") {
					return &fetcher.Response{StatusCode: 404, Body: []byte("not found")}, nil
				}
				return &fetcher.Response{StatusCode: 200, Body: []byte(`[{"bookSourceUrl":"https://demo.example"}]`)}, nil
			},
		}
		svc := NewSourceService(nil).WithFetcher(ft)
		if _, err := svc.FetchSourcesFromURL(context.Background(), "https://demo.example/404"); !errors.Is(err, fetcher.ErrUpstreamError) {
			t.Fatalf("404 err = %v, want ErrUpstreamError", err)
		}
		got, err := svc.FetchSourcesFromURL(context.Background(), "https://demo.example/sources.json")
		if err != nil || !strings.Contains(got, "https://demo.example") {
			t.Fatalf("valid fetch got=%q err=%v", got, err)
		}
	})
}

func TestSanitizeCoverURL(t *testing.T) {
	valid := "  https://img.example/cover.jpg  "
	if got := sanitizeCoverURL(&valid); got == nil || *got != "https://img.example/cover.jpg" {
		t.Fatalf("valid cover got = %v", got)
	}
	for _, bad := range []string{"", "   ", "javascript:alert(1)", "/relative/cover.jpg", "https://img.example/" + strings.Repeat("a", 2100)} {
		b := bad
		if got := sanitizeCoverURL(&b); got != nil {
			t.Fatalf("unsafe cover %q accepted: %q", bad, *got)
		}
	}
}
