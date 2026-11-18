package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestGeminiRequiredJSONBeforeServices(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{}`, `{"chapter_index":null}`, `{"chapter_index":4294967296}`, `{"chapter_index":-1}`, `{"chapter_index":0} {}`} {
		t.Run(body, func(t *testing.T) {
			for _, handler := range []http.HandlerFunc{
				func(w http.ResponseWriter, r *http.Request) { s.CreateBookmark(w, r, uuid.New()) },
				func(w http.ResponseWriter, r *http.Request) {
					s.UpdateProgress(w, r, uuid.New(), UpdateProgressParams{IfMatch: `"1"`})
				},
			} {
				w := httptest.NewRecorder()
				handler(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
				if w.Code != 400 {
					t.Fatalf("invalid body status=%d", w.Code)
				}
			}
		})
	}
	for _, body := range []string{`{"chapter_index":0}`, `{"chapter_index":2}`} {
		var value BookmarkCreate
		if err := decodeJSON(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)), &value, "chapter_index"); err != nil {
			t.Fatal(err)
		}
	}
}
func TestGeminiImportLimitAndMalformedJSON(t *testing.T) {
	for _, tc := range []struct {
		body   io.Reader
		status int
	}{
		{io.MultiReader(strings.NewReader(`{"data":"`), io.LimitReader(infiniteJSONByte{}, 33<<20)), 413},
		{strings.NewReader(`{"format":"legado_json"} trailing`), 400},
	} {
		w := httptest.NewRecorder()
		(&Server{}).ImportSources(w, httptest.NewRequest("POST", "/", tc.body), ImportSourcesParams{})
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d", w.Code, tc.status)
		}
	}
}

type infiniteJSONByte struct{}

func (infiniteJSONByte) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 'a'
	}
	return len(b), nil
}
func TestGeminiAuthPathAndMalformedLogout(t *testing.T) {
	if !isPublicPath("/api/v1/auth/login") || isPublicPath("/api/v1/admin/auth/login") || isPublicPath("/private/readyz") {
		t.Fatal("public path prefix bypass")
	}
	w := httptest.NewRecorder()
	(&Server{}).Logout(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"broken"`)))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, raw := range []string{`1`, `"1`, `1"`, `"-1"`, `"0"`, `"+1"`} {
		if _, e := parseIfMatch(raw); e == nil {
			t.Fatal("accepted version", raw)
		}
	}
	if v, e := parseIfMatch(`"8"`); e != nil || v != 8 {
		t.Fatal(v, e)
	}
}
func TestGeminiLogsOnlyRouteTemplates(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	r := chi.NewRouter()
	r.Use(Logging(log))
	r.Get("/books/{id}", func(w http.ResponseWriter, r *http.Request) {
		r.Context().Value(ctxAuditUser).(*auditUser).id = uuid.MustParse("00000000-0000-4000-8000-000000000001")
		w.WriteHeader(204)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/books/private-secret?token=hidden", nil))
	if strings.Contains(out.String(), "private-secret") || strings.Contains(out.String(), "hidden") {
		t.Fatal("raw path/query leaked")
	}
	var entry map[string]any
	if json.Unmarshal(out.Bytes(), &entry) != nil || entry["path"] != "/books/{id}" || entry["user_id"] != "00000000-0000-4000-8000-000000000001" {
		t.Fatal(out.String())
	}
}
