package service

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

const maxSourceHeaderValueBytes = 8192

// parseSourceHeaders parses BookSource.Header JSON into a sanitized http.Header.
// Only safe outbound headers are permitted; control characters and hop-by-hop/host
// headers are dropped to prevent header injection or request smuggling.
func parseSourceHeaders(raw json.RawMessage) http.Header {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return nil
	}
	out := make(http.Header)
	for k, v := range obj {
		str, ok := v.(string)
		if !ok {
			continue
		}
		if !isSafeHeaderName(k) || !isSafeHeaderValue(str) {
			continue
		}
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(k))
		if !isAllowedSourceHeader(canonical) {
			continue
		}
		out.Set(canonical, str)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeHeaders merges base source headers with per-request override headers.
// Override headers take precedence over base headers with the same canonical name.
func mergeHeaders(base, override http.Header) http.Header {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(http.Header, len(base)+len(override))
	for k, vs := range base {
		out[k] = append([]string(nil), vs...)
	}
	for k, vs := range override {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func isSafeHeaderName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 128 {
		return false
	}
	return !strings.ContainsAny(trimmed, "\r\n\x00: \t")
}

func isSafeHeaderValue(val string) bool {
	if len(val) > maxSourceHeaderValueBytes {
		return false
	}
	return !strings.ContainsAny(val, "\r\n\x00")
}

func isAllowedSourceHeader(canonical string) bool {
	switch canonical {
	case "User-Agent", "Accept", "Accept-Language", "Referer", "Origin", "Cookie", "Authorization":
		return true
	}
	return strings.HasPrefix(canonical, "X-")
}

// scopeHeadersForTarget scopes source headers to the origin URL.
// Cross-host navigation or https -> http downgrade strips Cookie, Authorization,
// Proxy-Authorization, and custom X-* / credential headers.
func scopeHeadersForTarget(originURL, targetURL string, headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	if shouldStripSensitiveHeaders(originURL, targetURL) {
		return cloneWithoutSensitiveHeaders(headers)
	}
	return headers
}

func shouldStripSensitiveHeaders(originRaw, targetRaw string) bool {
	orig, err1 := url.Parse(strings.TrimSpace(originRaw))
	targ, err2 := url.Parse(strings.TrimSpace(targetRaw))
	if err1 != nil || err2 != nil || orig.Hostname() == "" || targ.Hostname() == "" {
		return true
	}
	if !strings.EqualFold(orig.Hostname(), targ.Hostname()) {
		return true
	}
	return strings.EqualFold(orig.Scheme, "https") && strings.EqualFold(targ.Scheme, "http")
}

func cloneWithoutSensitiveHeaders(headers http.Header) http.Header {
	out := make(http.Header, len(headers))
	for k, vs := range headers {
		canonical := http.CanonicalHeaderKey(k)
		if isSensitiveSourceHeader(canonical) {
			continue
		}
		out[canonical] = append([]string(nil), vs...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isSensitiveSourceHeader(canonical string) bool {
	switch canonical {
	case "Authorization", "Proxy-Authorization", "Cookie", "Cookie2":
		return true
	}
	if strings.HasPrefix(canonical, "X-") {
		return true
	}
	lower := strings.ToLower(canonical)
	return strings.Contains(lower, "auth") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "session") ||
		strings.Contains(lower, "cookie") ||
		strings.Contains(lower, "key")
}
