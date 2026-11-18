//go:build integration && linux

package browserassist

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRelayPrivateSocketValidation(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "proxy.sock")
	listener, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	os.Chmod(path, 0600)
	if _, e = NewLoopbackRelay(path, 0, 0); e != nil {
		t.Fatal(e)
	}
	os.Chmod(dir, 0755)
	if _, e = NewLoopbackRelay(path, 0, 0); e == nil {
		t.Fatal("public directory accepted")
	}
	os.Chmod(dir, 0700)
	link := filepath.Join(dir, "link")
	if e = os.Symlink(dir, link); e != nil {
		t.Fatal(e)
	}
	if _, e = NewLoopbackRelay(filepath.Join(link, "proxy.sock"), 0, 0); e == nil {
		t.Fatal("symlink accepted")
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("not a socket"), 0600)
	if _, e = NewLoopbackRelay(file, 0, 0); e == nil {
		t.Fatal("ordinary file accepted")
	}
	relay, _ := NewLoopbackRelay(path, 0, 0)
	tcp, e := net.Listen("tcp", "0.0.0.0:0")
	if e != nil {
		t.Fatal(e)
	}
	defer tcp.Close()
	if relay.Serve(context.Background(), tcp) == nil {
		t.Fatal("non-loopback relay accepted")
	}
}

type topologyInfo struct {
	Certificate []byte
	Target      string
}

// Test-only roles. The private-target dial mapping is never available through
// a production constructor/configuration.
func TestRelayTopologyHelper(t *testing.T) {
	role := os.Getenv("TEST_RELAY_TOPOLOGY_ROLE")
	if role == "" {
		t.Skip("dedicated container helper only")
	}
	const socket = "/ipc/proxy.sock"
	if role == "proxy" {
		target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS.ServerName != "example.com" {
				t.Error("unexpected SNI")
			}
			w.Write([]byte("topology-ok"))
		}))
		defer target.Close()
		proxy, _ := NewEgressProxy(EgressProxyConfig{Hosts: []string{"example.com"}, Lifetime: 10 * time.Second})
		proxy.resolve = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		proxy.dial = func(ctx context.Context, n, address string) (net.Conn, error) {
			if address != "8.8.8.8:443" {
				t.Error("unverified dial")
			}
			return (&net.Dialer{}).DialContext(ctx, n, target.Listener.Addr().String())
		}
		listener, e := net.Listen("unix", socket)
		if e != nil {
			t.Fatal(e)
		}
		defer listener.Close()
		if e = os.Chmod(socket, 0600); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		server := &http.Server{Handler: proxy, ReadHeaderTimeout: time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		info, _ := json.Marshal(topologyInfo{pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.Certificate().Raw}), target.Listener.Addr().String()})
		if e = os.WriteFile("/ipc/info.tmp", info, 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.Rename("/ipc/info.tmp", "/ipc/info.json"); e != nil {
			t.Fatal(e)
		}
		for ctx.Err() == nil {
			if _, e := os.Stat("/ipc/stop"); e == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		server.Close()
		<-done
		for end := time.Now().Add(2 * time.Second); len(proxy.slots) > 0 && time.Now().Before(end); {
			time.Sleep(time.Millisecond)
		}
		if len(proxy.slots) != 0 {
			t.Fatal("proxy tunnel leaked")
		}
		os.WriteFile("/ipc/stopped", []byte("ok"), 0600)
		return
	}
	if role != "client" {
		t.Fatal("invalid topology role")
	}
	var info topologyInfo
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		b, e := os.ReadFile("/ipc/info.json")
		if e == nil && json.Unmarshal(b, &info) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(info.Certificate) == 0 {
		t.Fatal("fixture not ready")
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	for _, i := range interfaces {
		if i.Flags&net.FlagLoopback == 0 {
			t.Fatal("unexpected network interface")
		}
	}
	for _, target := range []struct{ n, a string }{{"tcp", info.Target}, {"tcp", "8.8.8.8:443"}, {"tcp", "[2606:4700:4700::1111]:443"}, {"udp", "8.8.8.8:53"}, {"udp", "8.8.8.8:443"}} {
		c, e := net.DialTimeout(target.n, target.a, 200*time.Millisecond)
		if e == nil {
			c.Close()
			t.Fatal("direct bypass available", target.n)
		}
	}
	relay, e := NewLoopbackRelay(socket, 4, 10*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	tcp, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relay.Serve(ctx, tcp) }()
	u, _ := url.Parse("http://" + tcp.Addr().String())
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(info.Certificate) {
		t.Fatal("invalid fixture CA")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{RootCAs: roots}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, e := client.Get("https://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || string(body) != "topology-ok" {
		t.Fatal("proxy path failed", e)
	}
	for _, bad := range []string{"https://other.example/", "https://127.0.0.1/"} {
		if r, e := client.Get(bad); e == nil {
			r.Body.Close()
			t.Fatal("unauthorized target accepted")
		}
	}
	// Keep an incomplete TLS tunnel open, then cancel at the proxy process.
	raw, e := net.Dial("tcp", tcp.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(raw, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	ack, e := http.ReadResponse(bufio.NewReader(raw), &http.Request{Method: "CONNECT"})
	if e != nil || ack.StatusCode != 200 {
		t.Fatal("idle tunnel setup", e)
	}
	if e = os.WriteFile("/ipc/stop", []byte("stop"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Read(make([]byte, 1)); e == nil {
		t.Fatal("proxy cancellation did not close tunnel")
	} else if n, ok := e.(net.Error); ok && n.Timeout() {
		t.Fatal("tunnel only stopped at test timeout")
	}
	if r, e := client.Get("https://example.com/"); e == nil {
		r.Body.Close()
		t.Fatal("stopped proxy still usable")
	}
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if len(relay.slots) != 0 {
		t.Fatal("relay connection leaked")
	}
}

func TestRelayCapacityAndCancellation(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	socket := filepath.Join(dir, "relay.sock")
	unix, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer unix.Close()
	os.Chmod(socket, 0600)
	relay, e := NewLoopbackRelay(socket, 1, 2*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	tcp, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relay.Serve(ctx, tcp) }()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := unix.Accept(); accepted <- c }()
	first, e := net.Dial("tcp", tcp.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	io.WriteString(first, "ping")
	var peer net.Conn
	select {
	case peer = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("Unix dial did not arrive")
	}
	if peer == nil {
		t.Fatal("missing Unix peer")
	}
	defer peer.Close()
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = io.ReadFull(peer, make([]byte, 4)); e != nil {
		t.Fatal(e)
	}
	second, e := net.Dial("tcp", tcp.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	second.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = second.Read(make([]byte, 1)); e == nil {
		t.Fatal("relay capacity bypassed")
	} else if n, ok := e.(net.Error); ok && n.Timeout() {
		t.Fatal("capacity did not reject immediately")
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if len(relay.slots) != 0 {
		t.Fatal("relay slot leaked")
	}
}
