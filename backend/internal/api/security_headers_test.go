package api

import (
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersOnErrorsAndHealth(t *testing.T) {
	for _, path := range []string{"/healthz", "/api/v1/books", "/not-a-route"} {
		w := httptest.NewRecorder()
		NewServer(Deps{}).Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		for key, value := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
			if w.Header().Get(key) != value {
				t.Fatalf("%s missing %s", path, key)
			}
		}
	}
}
