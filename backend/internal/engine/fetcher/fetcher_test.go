package fetcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// TestIsBlockedIP_BlockedMatrix 覆盖 §11.1 第 6 条的 SSRF IP 矩阵。
func TestIsBlockedIP_BlockedMatrix(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"127.0.0.2",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.169.254",
		"100.64.0.1",
		"198.18.0.1",
		"240.0.0.1",
		"0.0.0.0",
		"::1",
		"::ffff:127.0.0.1", // v4-mapped loopback
		"fe80::1",
		"ff02::1",
		"64:ff9b::192.0.2.1",
		"64:ff9b:1::1",
		"2002::1",
		"2001::1",
		"2001:db8::1",
		"::",
	}
	for _, s := range blocked {
		addr := netip.MustParseAddr(s)
		if !IsBlockedIP(addr) {
			t.Errorf("IsBlockedIP(%s) = false, want true", s)
		}
	}
}

func TestIsBlockedIP_Allowed(t *testing.T) {
	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",
		"2606:2800:220:1:248:1893:25c8:1946",
		"2001:4860:4860::8888",
	}
	for _, s := range allowed {
		addr := netip.MustParseAddr(s)
		if IsBlockedIP(addr) {
			t.Errorf("IsBlockedIP(%s) = true, want false", s)
		}
	}
}

// TestDialContext_SkipsBlockedIPs 覆盖 §11.1 第 8 条：DNS 返回 [坏 IP, 好 IP] 时
// 跳过被阻断地址、只拨号好 IP，连接成功（验证不是只试第一个）。
func TestDialContext_SkipsBlockedIPs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, goodPort, _ := net.SplitHostPort(ln.Addr().String())

	var dialed []string
	cfg := &Config{
		resolveFunc: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("127.0.0.1"), // 被阻断
				netip.MustParseAddr("8.8.8.8"),   // 允许
			}, nil
		},
		dialFunc: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			if strings.HasPrefix(addr, "8.8.8.8:") {
				return net.Dial(network, net.JoinHostPort("127.0.0.1", goodPort))
			}
			return nil, fmt.Errorf("unexpected dial to %s", addr)
		},
	}

	conn, err := dialContext(context.Background(), "tcp", net.JoinHostPort("example.test", goodPort), cfg)
	if err != nil {
		t.Fatalf("dialContext: %v", err)
	}
	conn.Close()

	if len(dialed) != 1 || !strings.HasPrefix(dialed[0], "8.8.8.8:") {
		t.Fatalf("expected to dial only 8.8.8.8 (skip blocked 127.0.0.1), got %v", dialed)
	}
}

// TestDialContext_AllBlocked 全部 IP 被阻断时返回 ErrSSRFBlocked。
func TestDialContext_AllBlocked(t *testing.T) {
	cfg := &Config{
		resolveFunc: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		},
	}
	_, err := dialContext(context.Background(), "tcp", "evil.test:80", cfg)
	if err == nil || !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("expected ErrSSRFBlocked, got %v", err)
	}
}

// TestDialContext_IPLiteralSkipsDNS 覆盖 §3.5.2 第 1 步：host 已是 IP 字面量时
// 直接 IsBlockedIP 校验 + 拨号，不调用 DNS resolve。
func TestDialContext_IPLiteralSkipsDNS(t *testing.T) {
	resolveCalled := false
	cfg := &Config{
		resolveFunc: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			resolveCalled = true
			return nil, fmt.Errorf("resolve should not be called for IP literal")
		},
		dialFunc: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, fmt.Errorf("dial %s", addr)
		},
	}

	// 阻断的字面量：直接 ErrSSRFBlocked，不拨号、不解析。
	_, err := dialContext(context.Background(), "tcp", "127.0.0.1:80", cfg)
	if err == nil || !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("expected ErrSSRFBlocked, got %v", err)
	}
	if resolveCalled {
		t.Fatal("resolveFunc was called for blocked IP literal")
	}

	// 放行的字面量：走拨号（dialFunc 记录），不解析。
	_, err = dialContext(context.Background(), "tcp", "8.8.8.8:80", cfg)
	if err == nil || !strings.Contains(err.Error(), "dial 8.8.8.8:80") {
		t.Fatalf("expected dial attempt, got %v", err)
	}
	if resolveCalled {
		t.Fatal("resolveFunc was called for allowed IP literal")
	}
}

// TestFetch_RedirectToLoopbackBlocked 覆盖 §11.1 第 7 条：302 → 127.0.0.1 被拦截，
// 且未向内网目标发出任何请求。
func TestFetch_RedirectToLoopbackBlocked(t *testing.T) {
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Store(true)
	}))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:"+targetPort+"/secret", http.StatusFound)
	}))
	defer redirector.Close()
	_, redirectorPort, _ := net.SplitHostPort(redirector.Listener.Addr().String())

	f := newLoopbackFetcher()
	_, err := f.Get(context.Background(), "http://redirector.test:"+redirectorPort+"/start", nil)
	if err == nil {
		t.Fatal("expected error for redirect to loopback, got nil")
	}
	if !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("expected ErrSSRFBlocked, got %v", err)
	}
	if targetHit.Load() {
		t.Error("loopback target was reached; SSRF redirect guard failed")
	}
}

// TestFetch_RetryOn5xx 覆盖幂等重试：5xx 重试 2 次后成功。
func TestFetch_RetryOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	f := newLoopbackFetcher()
	resp, err := f.Get(context.Background(), "http://retry.test:"+port+"/", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != 200 || string(resp.Body) != "ok" {
		t.Fatalf("unexpected response: %d %q", resp.StatusCode, resp.Body)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

// TestFetch_NoRetryOn4xx 4xx 不重试。
func TestFetch_NoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	f := newLoopbackFetcher()
	resp, err := f.Get(context.Background(), "http://retry.test:"+port+"/", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 attempt (no retry on 4xx), got %d", calls.Load())
	}
}

// TestFetch_GBKDecoding 覆盖编码自动转换（响应头 charset → UTF-8）。
func TestFetch_GBKDecoding(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("第一章 风起"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		w.Write(gbk)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	f := newLoopbackFetcher()
	resp, err := f.Get(context.Background(), "http://enc.test:"+port+"/", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(resp.Body) != "第一章 风起" {
		t.Fatalf("expected UTF-8 body %q, got %q", "第一章 风起", resp.Body)
	}
}

// TestFetch_CrossHostStripsAuth 跨 host 重定向剥离 Authorization/Cookie。
func TestFetch_CrossHostStripsAuth(t *testing.T) {
	var gotAuth atomic.Value
	gotAuth.Store("")
	gotCookie := atomic.Value{}
	gotCookie.Store("")

	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotCookie.Store(r.Header.Get("Cookie"))
		w.Write([]byte("done"))
	}))
	defer second.Close()
	_, secondPort, _ := net.SplitHostPort(second.Listener.Addr().String())

	// 用一个不同的 host 名做重定向目标（走 fake resolver → 8.8.8.8 → loopback 路由）。
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.test:"+secondPort+"/", http.StatusFound)
	}))
	defer first.Close()
	_, firstPort, _ := net.SplitHostPort(first.Listener.Addr().String())

	f := newLoopbackFetcher()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer secret")
	hdr.Set("Cookie", "session=abc")
	_, err := f.Get(context.Background(), "http://first.test:"+firstPort+"/", hdr)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth.Load() != "" {
		t.Errorf("Authorization leaked across host: %q", gotAuth.Load())
	}
	if gotCookie.Load() != "" {
		t.Errorf("Cookie leaked across host: %q", gotCookie.Load())
	}
}

// newLoopbackFetcher 返回一个能连通回环测试服务器、同时保留对字面量回环地址 SSRF
// 阻断的 Fetcher。测试用假 host 会被解析为 8.8.8.8，拨号时改写回 127.0.0.1 同端口。
func newLoopbackFetcher() *Fetcher {
	return New(Config{
		resolveFunc: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			if ip, err := netip.ParseAddr(host); err == nil {
				return []netip.Addr{ip}, nil
			}
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		dialFunc: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			return net.Dial(network, net.JoinHostPort("127.0.0.1", port))
		},
		backoff: func(attempt int) time.Duration { return time.Millisecond },
	})
}
