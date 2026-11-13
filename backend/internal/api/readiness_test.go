package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessDependencyStates(t *testing.T) {
	ok := func(context.Context) error { return nil }
	fail := func(context.Context) error { return errors.New("unavailable") }
	for _, tc := range []struct {
		name   string
		deps   Deps
		code   int
		checks map[string]string
	}{
		{"optional Redis absent", Deps{DBPing: ok}, 200, map[string]string{"postgres": "ok"}},
		{"all configured", Deps{DBPing: ok, RedisPing: ok, SchemaPing: ok}, 200, map[string]string{"postgres": "ok", "redis": "ok", "schema": "ok"}},
		{"Redis failed", Deps{DBPing: ok, RedisPing: fail}, 503, map[string]string{"postgres": "ok", "redis": "fail"}},
		{"PG absent", Deps{}, 503, map[string]string{"postgres": "fail"}},
		{"PG failed", Deps{DBPing: fail}, 503, map[string]string{"postgres": "fail"}},
		{"schema missing", Deps{DBPing: ok, SchemaPing: fail}, 503, map[string]string{"postgres": "ok", "schema": "fail"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRecorder()
			NewServer(tc.deps).Readyz(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			var payload struct {
				Status string            `json:"status"`
				Checks map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if r.Code != tc.code || len(payload.Checks) != len(tc.checks) {
				t.Fatalf("status/body: %d %s", r.Code, r.Body.String())
			}
			for key, value := range tc.checks {
				if payload.Checks[key] != value {
					t.Fatalf("check %s: %+v", key, payload)
				}
			}
		})
	}
}
