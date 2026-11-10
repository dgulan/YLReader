//go:build integration

package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestMetricsEndpointExposed 验证 /metrics 免鉴权可访问，且 §8.2 指标已预置零值 series。
func TestMetricsEndpointExposed(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
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

// TestPprofDisabledByDefault 验证默认环境不挂载 pprof。
func TestPprofDisabledByDefault(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (pprof disabled by default)", resp.StatusCode)
	}
}

// TestPprofEnabledWhenConfigured 验证 PPROF_ENABLED=true 时 pprof 已挂载。
// 注意：必须先 t.Setenv 再构造 Server，路由在构造期读取环境变量。
func TestPprofEnabledWhenConfigured(t *testing.T) {
	t.Setenv("PPROF_ENABLED", "true")
	ts, _ := newTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
