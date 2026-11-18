package api

import (
	"fmt"
	"strings"
	"testing"
	"yreader/backend/internal/engine/fetcher"
)

func TestChallengeFailureKeepsCodeAndRedactsDetails(t *testing.T) {
	err := fmt.Errorf("private upstream cookie=secret: %w", fetcher.ErrChallengeRequired)
	status, code := mapErrorToCode(err)
	if status != 502 || code != "source_request_failed" {
		t.Fatal(status, code)
	}
	message := sourceFailureMessage(err, code)
	if !strings.Contains(message, "浏览器验证") || strings.Contains(message, "secret") {
		t.Fatal("unsafe or ambiguous challenge message")
	}
	if sourceFailureMessage(fetcher.ErrUpstreamError, code) != messageForCode(code) {
		t.Fatal("ordinary failures relabeled")
	}
}
