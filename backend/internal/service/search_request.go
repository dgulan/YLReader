package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"yreader/backend/internal/engine/sandbox"
)

type searchRequest struct {
	url, method, body, charset string
	header                     http.Header
}

var searchOptionsStart = regexp.MustCompile(`,\s*\{`)
var searchJSStart = regexp.MustCompile(`(?i)<js>|@js:`)
var searchJSEnd = regexp.MustCompile(`(?i)</js>`)

func requestOptionError() error {
	return fmt.Errorf("%w: unsupported search request options", sandbox.ErrUnsupportedRule)
}

// optionObject rejects duplicate keys and trailing JSON rather than accepting
// whichever security-relevant method/header appeared last.
func optionObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, requestOptionError()
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, requestOptionError()
		}
		if _, exists := out[key]; exists {
			return nil, requestOptionError()
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, requestOptionError()
		}
		out[key] = value
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return nil, requestOptionError()
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, requestOptionError()
	}
	return out, nil
}

func parseSearchRequest(raw, keyword string, page int) (searchRequest, error) {
	return parseSearchRequestMode(raw, keyword, page, true)
}

func parseSearchRequestMode(raw, keyword string, page int, appendDefaults bool) (searchRequest, error) {
	r := searchRequest{method: http.MethodGet}
	if len(raw) > 64<<10 || !utf8.ValidString(raw) {
		return r, requestOptionError()
	}
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "<js>") || strings.HasPrefix(lower, "@js:") {
		return r, requestOptionError()
	}
	target := raw
	if loc := searchOptionsStart.FindStringIndex(raw); loc != nil {
		target = strings.TrimSpace(raw[:loc[0]])
		options, err := optionObject([]byte(strings.TrimSpace(raw[loc[0]+1:])))
		if err != nil {
			return r, err
		}
		for key, value := range options {
			if key == "headers" {
				headers, err := optionObject(value)
				if err != nil {
					return r, err
				}
				r.header = make(http.Header)
				seen := map[string]bool{}
				for name, encoded := range headers {
					canonical := http.CanonicalHeaderKey(name)
					if seen[canonical] {
						return r, requestOptionError()
					}
					seen[canonical] = true
					var text string
					if json.Unmarshal(encoded, &text) != nil || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) || len(text) > 4096 || strings.ContainsAny(text, "\r\n") {
						return r, requestOptionError()
					}
					if text == "" {
						continue
					}
					if canonical != "User-Agent" && canonical != "Accept" && canonical != "Content-Type" {
						return r, requestOptionError()
					}
					r.header.Set(canonical, text)
				}
				continue
			}
			var text string
			if json.Unmarshal(value, &text) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return r, requestOptionError()
			}
			switch key {
			case "method":
				r.method = strings.ToUpper(strings.TrimSpace(text))
			case "body":
				r.body = text
			case "charset":
				r.charset = normalizeSearchCharset(text)
				if r.charset == "" && strings.TrimSpace(text) != "" {
					return r, requestOptionError()
				}
			default:
				return r, requestOptionError()
			}
		}
	}
	targetLower := strings.ToLower(target)
	if strings.Contains(targetLower, "<js>") || strings.Contains(targetLower, "</js>") || strings.Contains(targetLower, "@js:") {
		return r, requestOptionError()
	}
	if target == "" || (r.method != http.MethodGet && r.method != http.MethodPost) || (r.method == http.MethodGet && r.body != "") {
		return r, requestOptionError()
	}
	encodedKeyword := keyword
	switch r.charset {
	case "gbk", "gb2312", "gb18030":
		codec := simplifiedchinese.GBK
		if r.charset == "gb18030" {
			codec = simplifiedchinese.GB18030
		}
		encoded, err := codec.NewEncoder().String(keyword)
		if err != nil {
			return r, requestOptionError()
		}
		encodedKeyword = encoded
	}
	replace := func(value string) string {
		return strings.NewReplacer("{{key}}", url.QueryEscape(encodedKeyword), "{{page}}", strconv.Itoa(page)).Replace(value)
	}
	if r.method == http.MethodGet {
		r.url = replace(target)
		if appendDefaults {
			r.url = expandSearchURL(target, encodedKeyword, page)
		}
	} else {
		r.url, r.body = replace(target), replace(r.body)
		if len(r.body) > 64<<10 {
			return r, requestOptionError()
		}
		if r.header == nil {
			r.header = make(http.Header)
		}
		contentType := r.header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/x-www-form-urlencoded"
			if r.charset != "" && r.charset != "utf-8" {
				contentType += "; charset=" + r.charset
			}
		}
		media, params, err := mime.ParseMediaType(contentType)
		if err != nil || media != "application/x-www-form-urlencoded" {
			return r, requestOptionError()
		}
		for key, value := range params {
			want := r.charset
			if want == "" {
				want = "utf-8"
			}
			if key != "charset" || normalizeSearchCharset(value) != want {
				return r, requestOptionError()
			}
		}
		r.header.Set("Content-Type", contentType)
	}
	return r, nil
}

func normalizeSearchCharset(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "utf8", "utf-8":
		return "utf-8"
	case "gbk":
		return "gbk"
	case "gb2312":
		return "gb2312"
	case "gb18030":
		return "gb18030"
	default:
		return ""
	}
}

// buildSearchRequest only evaluates the script supplied by the source rule.
// User input is passed as data, never interpolated into JavaScript source.
func (s *SearchService) buildSearchRequest(ctx context.Context, base, raw, keyword string, page int) (searchRequest, error) {
	if len(raw) > 64<<10 || !utf8.ValidString(raw) {
		return searchRequest{}, requestOptionError()
	}
	marker := searchJSStart.FindStringIndex(raw)
	options := searchOptionsStart.FindStringIndex(raw)
	if marker == nil || (options != nil && options[0] < marker[0]) {
		return parseSearchRequest(raw, keyword, page)
	}
	prefix, script := raw[:marker[0]], raw[marker[1]:]
	suffix := ""
	if strings.EqualFold(raw[marker[0]:marker[1]], "<js>") {
		end := searchJSEnd.FindStringIndex(script)
		if end == nil {
			return searchRequest{}, requestOptionError()
		}
		suffix = strings.TrimSpace(script[end[1]:])
		if suffix != "" {
			start := searchOptionsStart.FindStringIndex(suffix)
			if start == nil || start[0] != 0 {
				return searchRequest{}, requestOptionError()
			}
		}
		script = script[:end[0]]
	}
	if s.sb == nil || strings.TrimSpace(script) == "" {
		return searchRequest{}, requestOptionError()
	}
	output, err := s.sb.Eval(ctx, script, map[string]any{"key": keyword, "page": page, "baseUrl": base, "result": strings.TrimSpace(prefix)})
	if err != nil {
		return searchRequest{}, err
	}
	// A dynamic URL is already assembled by its rule. Expand explicit templates,
	// but keep a complete URL exactly as returned when it has no templates.
	return parseSearchRequestMode(output+suffix, keyword, page, false)
}
