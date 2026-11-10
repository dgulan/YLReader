package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClient 是测试用 HTTPClient：真实发起请求并透传 ctx，不做 SSRF 过滤
// （SSRF 由 fetcher 包负责，sandbox 只验证超时取消语义）。
type fakeClient struct{}

func (fakeClient) Do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// TestSandbox_InfiniteLoopTimeout 覆盖 §11.1 第 2 条：while(true) → 3s 超时，
// Runtime 被丢弃（后续求值仍正常）。
func TestSandbox_InfiniteLoopTimeout(t *testing.T) {
	sb := New(Config{}, nil)
	start := time.Now()
	_, err := sb.Eval(context.Background(), "while(true){}", nil)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrSandboxTimeout) {
		t.Fatalf("expected ErrSandboxTimeout, got %v", err)
	}
	if elapsed < 2700*time.Millisecond || elapsed > 3600*time.Millisecond {
		t.Fatalf("timeout in %v, want ~3s (±300ms)", elapsed)
	}
	// 丢弃后的运行时不影响后续执行
	out, err := sb.Eval(context.Background(), "result = 'ok'", nil)
	if err != nil || out != "ok" {
		t.Fatalf("follow-up eval failed: %v %q", err, out)
	}
}

// TestSandbox_AjaxTimeout 覆盖 §11.1 第 3 条：java.ajax 指向慢速服务器 → 3s 返回
// 超时，且服务端连接被关闭。
func TestSandbox_AjaxTimeout(t *testing.T) {
	var closed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // 阻塞直到客户端取消
		closed.Store(true)
	}))
	defer srv.Close()

	sb := New(Config{}, fakeClient{})
	start := time.Now()
	_, err := sb.Eval(context.Background(), "result = java.ajax('"+srv.URL+"/slow')", nil)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrSandboxTimeout) {
		t.Fatalf("expected ErrSandboxTimeout, got %v", err)
	}
	if elapsed < 2700*time.Millisecond || elapsed > 3600*time.Millisecond {
		t.Fatalf("timeout in %v, want ~3s (±300ms)", elapsed)
	}
	// 连接关闭由服务端 goroutine 异步观测；负载下可能稍有延迟，最多等 2s。
	deadline := time.Now().Add(2 * time.Second)
	for !closed.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !closed.Load() {
		t.Error("server did not observe connection close on timeout")
	}
}

// TestSandbox_OutputLimit 覆盖 §11.1 第 4 条：超 1 MiB 输出 → sandbox_output_limit。
func TestSandbox_OutputLimit(t *testing.T) {
	sb := New(Config{}, nil)
	big := strings.Repeat("x", 1100000) // 1.1 MB > 1,048,576
	_, err := sb.Eval(context.Background(), "result = bigStr", map[string]any{"bigStr": big})
	if !errors.Is(err, ErrSandboxOutputLimit) {
		t.Fatalf("expected ErrSandboxOutputLimit, got %v", err)
	}
}

// TestSandbox_InfiniteRecursion 覆盖 §3.3.1 栈深限制：无限递归触发 goja 栈溢出
// （RangeError），归为 ErrRuleExecution，且不会被误判为 ErrSandboxTimeout，进程不崩溃。
func TestSandbox_InfiniteRecursion(t *testing.T) {
	sb := New(Config{}, nil)
	_, err := sb.Eval(context.Background(), "function f(){return f();} f();", nil)
	if !errors.Is(err, ErrRuleExecution) {
		t.Fatalf("expected ErrRuleExecution, got %v", err)
	}
	if errors.Is(err, ErrSandboxTimeout) {
		t.Fatalf("must not be classified as ErrSandboxTimeout, got %v", err)
	}
}

func TestSandbox_NormalEval(t *testing.T) {
	sb := New(Config{}, nil)
	out, err := sb.Eval(context.Background(), "result = 'hello ' + 'world'", nil)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if out != "hello world" {
		t.Fatalf("got %q", out)
	}
}

func TestSandbox_BridgeBase64Md5(t *testing.T) {
	sb := New(Config{}, nil)
	out, err := sb.Eval(context.Background(), "result = java.base64Encode('abc') + '|' + java.md5Encode('abc')", nil)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if out != "YWJj|900150983cd24fb0d6963f7d28e17f72" {
		t.Fatalf("got %q", out)
	}
}

func TestSandbox_SourceVariables(t *testing.T) {
	sb := New(Config{}, nil)
	out, err := sb.Eval(context.Background(), "source.setVariable('k', 'v'); result = source.getVariable('k')", nil)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if out != "v" {
		t.Fatalf("got %q", out)
	}
}

// TestSandbox_NoFileAccess 白名单之外不暴露 file/process/require 等入口。
func TestSandbox_NoFileAccess(t *testing.T) {
	sb := New(Config{}, nil)
	out, err := sb.Eval(context.Background(), "result = typeof java + ',' + typeof source + ',' + typeof require + ',' + typeof process + ',' + typeof Packages", nil)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if out != "object,object,undefined,undefined,undefined" {
		t.Fatalf("whitelist violation: got %q", out)
	}
}

// TestSandbox_RuntimeReuse 验证 Runtime 池化复用且正常执行不被污染。
func TestSandbox_RuntimeReuse(t *testing.T) {
	sb := New(Config{}, nil)
	for i := 0; i < 5; i++ {
		out, err := sb.Eval(context.Background(), "result = 'n' + "+itoa(i), nil)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		want := "n" + itoa(i)
		if out != want {
			t.Fatalf("iter %d: got %q want %q", i, out, want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
