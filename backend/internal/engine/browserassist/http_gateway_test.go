package browserassist

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func localGateway(t *testing.T, h http.HandlerFunc) (*HTTPGateway, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	g, e := NewHTTPGateway(HTTPGatewayConfig{Endpoint: s.URL + "/v1", ControlIPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, AllowedOrigins: []string{"https://books.example"}})
	if e != nil {
		t.Fatal(e)
	}
	g.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "books.example" {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	return g, s
}
func command() []byte {
	return []byte(`{"cmd":"request.get","url":"https://books.example/read/1","maxTimeout":5000}`)
}

func TestHTTPGatewayProtocolRoundTrip(t *testing.T) {
	var calls atomic.Int32
	g, _ := localGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("control request boundary")
		}
		var req map[string]any
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req) != 3 || req["cmd"] != "request.get" {
			t.Error("command mismatch")
		}
		json.NewEncoder(w).Encode(fixture())
	})
	a, _ := New(g, 0)
	cred, e := a.Solve(context.Background(), fixtureScope, "https://books.example/read/1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = cred.Headers(fixtureScope, "https://books.example/read/2"); e != nil || calls.Load() != 1 {
		t.Fatal("round trip or retries", e, calls.Load())
	}
}

func TestHTTPGatewayRejectsRedirectAndLargeBody(t *testing.T) {
	for _, kind := range []string{"redirect", "large", "chunked", "compressed", "error"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			g, _ := localGateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch kind {
				case "redirect":
					w.Header().Set("Location", "http://127.0.0.1:9/private")
					w.WriteHeader(307)
				case "large":
					w.Header().Set("Content-Length", "2048")
					w.Write([]byte(strings.Repeat("x", 2048)))
				case "chunked":
					w.(http.Flusher).Flush()
					w.Write([]byte(strings.Repeat("x", 2048)))
				case "compressed":
					w.Header().Set("Content-Encoding", "gzip")
					w.Write([]byte("x"))
				default:
					w.WriteHeader(503)
				}
			})
			if _, e := g.Exchange(context.Background(), command(), 1024); !errors.Is(e, ErrProtocol) || calls.Load() != 1 {
				t.Fatal("failed open or replayed", e, calls.Load())
			}
		})
	}
}

func TestHTTPGatewayScopeAndPayloadRejection(t *testing.T) {
	var calls atomic.Int32
	g, _ := localGateway(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, body := range []string{`{"cmd":"request.post","url":"https://books.example/","maxTimeout":1000}`, `{"cmd":"request.get","url":"https://evil.example/","maxTimeout":1000}`, `{"cmd":"request.get","url":"https://books.example/","maxTimeout":1000,"cookies":[]}`, `{"cmd":"request.get","url":"https://books.example/","maxTimeout":1000,"Cmd":"request.post"}`} {
		if _, e := g.Exchange(context.Background(), []byte(body), 1024); e == nil {
			t.Fatal("invalid payload accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid commands reached helper")
	}
}

func TestHTTPGatewayRejectsDNSDrift(t *testing.T) {
	for _, control := range []bool{false, true} {
		var calls atomic.Int32
		g, _ := localGateway(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
		g.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
			if host == "books.example" {
				if !control {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
				}
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2")}, nil
		}
		if _, e := g.Exchange(context.Background(), command(), 1024); e == nil {
			t.Fatal("DNS drift accepted")
		}
		if calls.Load() != 0 {
			t.Fatal("unexpected network request")
		}
	}
}

func TestHTTPGatewayCancellationClosesBody(t *testing.T) {
	stopped, started, force := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(force)
	g, _ := localGateway(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
		case <-force:
		}
		close(stopped)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := g.Exchange(ctx, command(), 1024); done <- e }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("helper never started")
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("helper socket leaked")
	}
}

func TestHTTPGatewayDNSDeadline(t *testing.T) {
	g, _ := localGateway(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected helper call") })
	g.timeout = 20 * time.Millisecond
	g.resolve = func(ctx context.Context, _ string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() }
	if _, e := g.Exchange(context.Background(), command(), 1024); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}

func TestHTTPGatewayPinsDialAddress(t *testing.T) {
	g, _ := localGateway(t, func(http.ResponseWriter, *http.Request) {})
	g.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		host, _, e := net.SplitHostPort(address)
		if e != nil || host != "127.0.0.1" {
			t.Error("not pinned")
		}
		return nil, errors.New("secret target details")
	}
	if _, e := g.Exchange(context.Background(), command(), 1024); !errors.Is(e, ErrUnavailable) || strings.Contains(e.Error(), "secret") {
		t.Fatal("dial error leak")
	}
}

func TestHTTPGatewayConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://u:p@helper:8191/v1", "http://helper/v1", "http://helper:8191/other", "http://helper:8191/v1?url=x", "http://helper:8191/%76%31"} {
		if _, e := NewHTTPGateway(HTTPGatewayConfig{Endpoint: endpoint, ControlIPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, AllowedOrigins: []string{"https://books.example"}}); e == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	if _, e := NewHTTPGateway(HTTPGatewayConfig{Endpoint: "http://helper:8191/v1", ControlIPs: []netip.Addr{netip.MustParseAddr("8.8.8.8")}, AllowedOrigins: []string{"https://books.example"}}); e == nil {
		t.Fatal("public control target accepted")
	}
}

func TestHTTPGatewayPreservesTLSVerification(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer s.Close()
	g, e := NewHTTPGateway(HTTPGatewayConfig{Endpoint: s.URL + "/v1", ControlIPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, AllowedOrigins: []string{"https://books.example"}})
	if e != nil {
		t.Fatal(e)
	}
	g.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "books.example" {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, e = g.Exchange(context.Background(), command(), 1024); !errors.Is(e, ErrUnavailable) || calls.Load() != 0 {
		t.Fatal("untrusted TLS accepted", e)
	}
}
