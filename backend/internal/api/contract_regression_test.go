package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yreader/backend/internal/repository"
)

func TestShelfProgressPreservesVersionAndOffset(t *testing.T) {
	idx, offset, version := int32(3), int32(120), int64(7)
	book := toAPIBookFromShelf(&repository.BookshelfEntry{
		ProgressChapterIndex: &idx, ProgressCharOffset: &offset, ProgressVersion: &version,
	})
	if book.Progress == nil || book.Progress.Version != 7 || book.Progress.CharOffset != 120 || book.Progress.ChapterIndex != 3 {
		t.Fatalf("progress lost: %+v", book.Progress)
	}
	if book := toAPIBookFromShelf(&repository.BookshelfEntry{}); book.Progress != nil {
		t.Fatal("unread book must not invent progress")
	}
}

func TestIdempotencyKeyContract(t *testing.T) {
	operations := []struct{ method, path string }{
		{"POST", "/api/v1/sources"}, {"POST", "/api/v1/sources/import"}, {"POST", "/api/v1/books"},
		{"PUT", "/api/v1/books/id/progress"}, {"POST", "/api/v1/books/id/cache"},
	}
	for _, op := range operations {
		for _, n := range []int{0, 7, 8, 128, 129} {
			var logs bytes.Buffer
			h := IdempotencyKeyValidation(slog.New(slog.NewJSONHandler(&logs, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			r := httptest.NewRequest(op.method, op.path, nil)
			r.Header.Set("Idempotency-Key", strings.Repeat("k", n))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 400
			if n >= 8 && n <= 128 {
				want = 204
			}
			if w.Code != want {
				t.Fatalf("%s %s length=%d: %d", op.method, op.path, n, w.Code)
			}
			if want == 204 && !strings.Contains(logs.String(), "idempotency_key") {
				t.Fatal("missing correlation log")
			}
		}
	}
	var logs bytes.Buffer
	h := IdempotencyKeyValidation(slog.New(slog.NewJSONHandler(&logs, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, path := range []string{"/api/v1/books", "/api/v1/healthz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 204 {
			t.Fatal("absent key must remain optional")
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/books", nil)
	r.Header.Add("Idempotency-Key", "12345678")
	r.Header.Add("Idempotency-Key", "abcdefgh")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("duplicate keys must be rejected")
	}
}
