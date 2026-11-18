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

func TestHTTPStatusPreservedAfterRetries(t *testing.T) {
	f := New(Config{backoff: func(int) time.Duration { return 0 }})
	calls := 0
	f.client.Transport = auditTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("SECRET body")), Request: r}, nil
	})
	_, err := f.Get(context.Background(), "https://example.com/SECRET?token=SECRET", nil)
	var status *HTTPStatusError
	if calls != 3 || !errors.Is(err, ErrUpstreamError) || !errors.As(err, &status) || status.StatusCode != 503 {
		t.Fatal(calls, err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "example.com") {
		t.Fatal("upstream status leaked response or URL")
	}
}
