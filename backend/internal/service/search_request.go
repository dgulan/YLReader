package service

import (
	"bytes"
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

	"yreader/backend/internal/engine/sandbox"
)

type searchRequest struct {
	url, method, body string
	header            http.Header
}

var searchOptionsStart = regexp.MustCompile(`,\s*\{`)

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
				if text != "" && !strings.EqualFold(text, "utf-8") && !strings.EqualFold(text, "utf8") {
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
	replace := func(value string) string {
		return strings.NewReplacer("{{key}}", url.QueryEscape(keyword), "{{page}}", strconv.Itoa(page)).Replace(value)
	}
	if r.method == http.MethodGet {
		r.url = expandSearchURL(target, keyword, page)
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
		}
		media, params, err := mime.ParseMediaType(contentType)
		if err != nil || media != "application/x-www-form-urlencoded" {
			return r, requestOptionError()
		}
		for key, value := range params {
			if key != "charset" || (!strings.EqualFold(value, "utf-8") && !strings.EqualFold(value, "utf8")) {
				return r, requestOptionError()
			}
		}
		r.header.Set("Content-Type", contentType)
	}
	return r, nil
}
