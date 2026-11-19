//go:build integration

package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yreader/backend/internal/api"
)

// TestMetricsEndpointExposed 验证公开路由不暴露 /metrics，内部 OpsHandler 暴露 /metrics 与预置零值 series。
func TestMetricsEndpointExposed(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	pubResp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET public /metrics: %v", err)
	}
	pubResp.Body.Close()
	if pubResp.StatusCode != http.StatusNotFound {
		t.Fatalf("public /metrics status = %d, want 404", pubResp.StatusCode)
	}

	opsSrv := httptest.NewServer(api.NewServer(api.Deps{}).OpsHandler())
	defer opsSrv.Close()

	resp, err := http.Get(opsSrv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET ops /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// Prometheus 文本暴露格式固定为 text/plain; version=0.0.4; charset=utf-8
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain prefix", ct)
	}
	for _, name := range []string{
		"yreader_jobs_total",
		"yreader_sandbox_executions_total",
		"yreader_ssrf_blocked_total",
	} {
		if !strings.Contains(string(body), name) {
			t.Fatalf("metrics body missing %q", name)
		}
	}
}

// TestPprofDisabledByDefault 验证默认环境公开路由和运维路由都不挂载 pprof。
func TestPprofDisabledByDefault(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	opsSrv := httptest.NewServer(api.NewServer(api.Deps{}).OpsHandler())
	defer opsSrv.Close()

	for _, u := range []string{ts.URL + "/debug/pprof/", opsSrv.URL + "/debug/pprof/"} {
		resp, err := http.Get(u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404 (pprof disabled by default)", u, resp.StatusCode)
		}
	}
}

// TestPprofEnabledWhenConfigured 验证 PPROF_ENABLED=true 时仅 OpsHandler 挂载 pprof，公开路由仍返回 404。
func TestPprofEnabledWhenConfigured(t *testing.T) {
	t.Setenv("PPROF_ENABLED", "true")
	ts, _ := newTestServer(t, nil)

	pubResp, err := http.Get(ts.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET public /debug/pprof/: %v", err)
	}
	pubResp.Body.Close()
	if pubResp.StatusCode != http.StatusNotFound {
		t.Fatalf("public /debug/pprof/ status = %d, want 404", pubResp.StatusCode)
	}

	opsSrv := httptest.NewServer(api.NewServer(api.Deps{}).OpsHandler())
	defer opsSrv.Close()

	resp, err := http.Get(opsSrv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET ops /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
