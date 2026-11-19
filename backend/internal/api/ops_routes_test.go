package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"yreader/backend/internal/api"
)

func TestMetricsAndPprofRestrictedToOpsHandler(t *testing.T) {
	t.Setenv("PPROF_ENABLED", "true")
	srv := api.NewServer(api.Deps{})

	pubHandler := srv.Handler()
	for _, p := range []string{"/metrics", "/debug/pprof/"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		pubHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("public %s status = %d, want 404", p, rec.Code)
		}
	}

	opsHandler := srv.OpsHandler()
	for _, p := range []string{"/metrics", "/debug/pprof/"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		opsHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ops %s status = %d, want 200", p, rec.Code)
		}
	}
}
