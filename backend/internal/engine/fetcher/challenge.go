package fetcher

import (
	"bytes"
	"fmt"
	"mime"
	"net/http"
	"strings"
)

// ErrChallengeRequired remains an upstream error for the frozen API mapping.
// It never contains the target URL, response HTML or cookies.
var ErrChallengeRequired = fmt.Errorf("%w: browser verification required", ErrUpstreamError)

func challengeHeader(h http.Header) bool {
	return strings.EqualFold(strings.TrimSpace(h.Get("cf-mitigated")), "challenge")
}

func challengeResponse(status int, h http.Header, body []byte) bool {
	if challengeHeader(h) {
		return true
	}
	if (status != 403 && status != 503) || !strings.EqualFold(strings.TrimSpace(h.Get("Server")), "cloudflare") {
		return false
	}
	if ct := h.Get("Content-Type"); ct != "" {
		media, _, err := mime.ParseMediaType(ct)
		if err != nil || !strings.EqualFold(media, "text/html") {
			return false
		}
	}
	b := bytes.ToLower(body[:min(len(body), 64<<10)])
	return bytes.Contains(b, []byte("/cdn-cgi/challenge-platform/")) || bytes.Contains(b, []byte("cf-browser-verification")) || (bytes.Contains(b, []byte("just a moment")) && bytes.Contains(b, []byte("enable javascript and cookies to continue")))
}
