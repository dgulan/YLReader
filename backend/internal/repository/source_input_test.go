package repository

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
)

func TestImportedSourceURLPreflight(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "https://192.168.1.1", "http://[::1]", "https://user:pass@example.com", "https://example.com:65536", "https://example.com:", "https://example.com:abc"} {
		b, _ := json.Marshal(map[string]string{"bookSourceUrl": raw})
		if _, e := parseSource(b, uuid.New()); e == nil {
			t.Fatal("unsafe source accepted", raw)
		}
	}
	for _, raw := range []string{"https://example.com", "http://www.630book.cc-By Dark"} {
		b, _ := json.Marshal(map[string]string{"bookSourceUrl": raw})
		if _, e := parseSource(b, uuid.New()); e != nil {
			t.Fatal("compatible source rejected", e)
		}
	}
}
func TestSourceHeaderObjectAtWriteBoundary(t *testing.T) {
	for _, raw := range []string{`[]`, `true`, `null`, `1`, `"text"`, `"[1,2]"`, `"null"`} {
		extracted := extractHeader(map[string]json.RawMessage{"header": json.RawMessage(raw)})
		if string(extracted) != "{}" {
			t.Fatal("nonobject imported", raw)
		}
		if _, e := sealSourceHeaderValue(&BookSource{Header: json.RawMessage(raw)}); e == nil {
			t.Fatal("nonobject sealed", raw)
		}
	}
	for _, raw := range []string{`{}`, `{"User-Agent":"reader"}`, `"{\"Cookie\":\"synthetic\"}"`} {
		extracted := extractHeader(map[string]json.RawMessage{"header": json.RawMessage(raw)})
		if !sourceHeaderObject(extracted) {
			t.Fatal("object lost")
		}
	}
}
