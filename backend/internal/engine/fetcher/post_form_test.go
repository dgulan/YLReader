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

func TestFetchPOSTNeverRetriesForm(t *testing.T) {
	f := New(Config{Retries: 3, backoff: func(int) time.Duration { t.Fatal("POST retry backoff invoked"); return 0 }})
	calls := 0
	f.client.Transport = auditTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil || r.Method != "POST" || string(body) != "q=a+b&page=2" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("form request changed: %s %q %v", r.Method, body, err)
		}
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unavailable")), Request: r}, nil
	})
	_, err := f.Fetch(context.Background(), "POST", "https://books.example/find", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, strings.NewReader("q=a+b&page=2"))
	if !errors.Is(err, ErrUpstreamError) || calls != 1 {
		t.Fatal("POST form was replayed", calls, err)
	}
}
