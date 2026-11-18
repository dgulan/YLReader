//go:build integration

package fetcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestPublicChallengeProbe(t *testing.T) {
	path := os.Getenv("TEST_PUBLIC_CHALLENGE_URLS")
	if path == "" {
		t.Skip("explicit public URL matrix required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	if json.Unmarshal(data, &urls) != nil || len(urls) == 0 || len(urls) > 8 {
		t.Fatal("invalid probe matrix")
	}
	f := New(Config{})
	for i, u := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		resp, err := f.Get(ctx, u, nil)
		cancel()
		record := map[string]any{"index": i, "seconds": time.Since(start).Seconds(), "challenge": errors.Is(err, ErrChallengeRequired)}
		if resp != nil {
			record["status"] = resp.StatusCode
			record["server"] = resp.Header.Get("Server")
			record["cf_mitigated"] = resp.Header.Get("cf-mitigated")
			record["body_sha256"] = fmt.Sprintf("%x", sha256.Sum256(resp.Body))
		}
		if err != nil {
			code := "other"
			switch {
			case errors.Is(err, ErrChallengeRequired):
				code = "challenge"
			case errors.Is(err, context.DeadlineExceeded):
				code = "timeout"
			case errors.Is(err, ErrSSRFBlocked):
				code = "blocked"
			case errors.Is(err, ErrUpstreamError):
				code = "upstream"
			}
			record["error"] = code
		}
		b, _ := json.Marshal(record)
		fmt.Println("CHALLENGE_PROBE " + string(b))
	}
}
