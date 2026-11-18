package browserassist

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEgressProxyFallsBackOnlyWithinVerifiedDNSAnswers(t *testing.T) {
	p, client, _, calls := proxyFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("fallback")) })
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("9.9.9.9"), netip.MustParseAddr("8.8.8.8")}, nil
	}
	original := p.dial
	attempts := []string{}
	p.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		attempts = append(attempts, address)
		if address == "9.9.9.9:443" {
			return nil, errors.New("unreachable")
		}
		return original(ctx, network, address)
	}
	r, e := client.Get("https://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(r.Body)
	r.Body.Close()
	if e != nil || string(body) != "fallback" || len(attempts) != 2 || attempts[0] != "9.9.9.9:443" || attempts[1] != "8.8.8.8:443" || calls.Load() != 1 {
		t.Fatal("verified address fallback failed", e)
	}
}

func proxyFixture(t *testing.T, handler http.HandlerFunc) (*EgressProxy, *http.Client, string, *atomic.Int32) {
	t.Helper()
	target := httptest.NewTLSServer(handler)
	t.Cleanup(target.Close)
	p, e := NewEgressProxy(EgressProxyConfig{Hosts: []string{"example.com"}, Lifetime: 2 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	calls := &atomic.Int32{}
	p.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		if address != "8.8.8.8:443" {
			t.Error("unverified dial target")
		}
		return (&net.Dialer{}).DialContext(ctx, network, target.Listener.Addr().String())
	}
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	proxyURL, _ := url.Parse(server.URL)
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return p, &http.Client{Transport: tr, Timeout: 4 * time.Second}, server.URL, calls
}

func TestEgressProxyTLSPassthroughAndRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		_, client, _, calls := proxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.TLS.ServerName != "example.com" {
				t.Error("SNI changed")
			}
			if redirect {
				w.Header().Set("Location", "https://other.example/")
				w.WriteHeader(302)
				return
			}
			w.Write([]byte("proxied"))
		})
		resp, e := client.Get("https://example.com/")
		if redirect {
			if e == nil {
				resp.Body.Close()
				t.Fatal("cross-host redirect allowed")
			}
		} else {
			if e != nil {
				t.Fatal(e)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(b) != "proxied" {
				t.Fatal("TLS bytes lost")
			}
		}
		if calls.Load() != 1 {
			t.Fatal("unexpected upstream connection", calls.Load())
		}
	}
}

func TestEgressProxyRejectsSNIAndPlaintext(t *testing.T) {
	_, client, address, calls := proxyFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unauthorized traffic") })
	client.Transport.(*http.Transport).TLSClientConfig.ServerName = "other.example"
	if resp, e := client.Get("https://example.com/"); e == nil {
		resp.Body.Close()
		t.Fatal("SNI mismatch allowed")
	}
	conn, e := net.Dial("tcp", strings.TrimPrefix(address, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	response, e := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if e != nil || response.StatusCode != 200 {
		t.Fatal("CONNECT setup", e)
	}
	io.WriteString(conn, "GET / HTTP/1.1\r\n\r\n")
	b := make([]byte, 1)
	if _, e = conn.Read(b); e == nil {
		t.Fatal("plaintext accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid hello reached upstream")
	}
}

func connectRequest(target string) *http.Request {
	return &http.Request{Method: "CONNECT", ProtoMajor: 1, Host: target, RequestURI: target, Header: make(http.Header), Body: http.NoBody}
}

func TestEgressProxyRejectsSpecialAndMixedDNS(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "100.64.0.1"} {
		p, _ := NewEgressProxy(EgressProxyConfig{Hosts: []string{"example.com"}})
		p.resolve = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(ip)}, nil
		}
		p.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Error("blocked DNS dialed")
			return nil, ErrScope
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, connectRequest("example.com:443"))
		if w.Code != 403 {
			t.Fatal(ip, w.Code)
		}
	}
}

func TestEgressProxyRechecksDNS(t *testing.T) {
	p, client, _, calls := proxyFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	var resolutions atomic.Int32
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		if resolutions.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("192.168.1.1")}, nil
	}
	resp, e := client.Get("https://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp, e = client.Get("https://example.com/"); e == nil {
		resp.Body.Close()
		t.Fatal("rebound host accepted")
	}
	if calls.Load() != 1 || resolutions.Load() != 2 {
		t.Fatal("DNS not pinned/rechecked")
	}
}

func TestEgressProxyAuthorityAndCapacity(t *testing.T) {
	p, _ := NewEgressProxy(EgressProxyConfig{Hosts: []string{"example.com"}, MaxTunnels: 1})
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		t.Error("unexpected resolution")
		return nil, ErrScope
	}
	for _, target := range []string{"127.0.0.1:443", "example.com:80", "other.example:443", "u:p@example.com:443", "example.com:443/path"} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, connectRequest(target))
		if w.Code != 403 {
			t.Fatal(target, w.Code)
		}
	}
	p.slots <- struct{}{}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, connectRequest("example.com:443"))
	<-p.slots
	if w.Code != 503 {
		t.Fatal("capacity ignored")
	}
	w = httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/", nil))
	if w.Code != 405 {
		t.Fatal("forward HTTP enabled")
	}
}

func TestEgressProxyLifetimeReleasesSlot(t *testing.T) {
	p, _ := NewEgressProxy(EgressProxyConfig{Hosts: []string{"example.com"}, Lifetime: 30 * time.Millisecond, MaxTunnels: 1})
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	p.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("incomplete hello dialed")
		return nil, ErrScope
	}
	s := httptest.NewServer(p)
	defer s.Close()
	conn, e := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	resp, e := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if e != nil || resp.StatusCode != 200 {
		t.Fatal(e)
	}
	if _, e = conn.Read(make([]byte, 1)); e == nil {
		t.Fatal("incomplete hello not closed")
	}
	until := time.Now().Add(time.Second)
	for len(p.slots) != 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(p.slots) != 0 {
		t.Fatal("tunnel slot leaked")
	}
}

func TestEgressProxyByteLimitTruncatesOversizedResponse(t *testing.T) {
	p, client, _, _ := proxyFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(strings.Repeat("x", 128<<10))) })
	p.maxBytes = 64 << 10
	resp, e := client.Get("https://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e == nil || len(b) >= 128<<10 {
		t.Fatal("oversized stream not terminated", len(b), e)
	}
}
