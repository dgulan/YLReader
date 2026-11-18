package browserassist

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"yreader/backend/internal/engine/fetcher"
)

type EgressProxyConfig struct {
	Hosts             []string
	MaxTunnels        int
	Lifetime          time.Duration
	BytesPerDirection int64
}

// EgressProxy must listen only on a private/IPC interface and be combined with
// network-level denial of browser direct egress. It does not decrypt HTTPS.
type EgressProxy struct {
	hosts    map[string]bool
	slots    chan struct{}
	lifetime time.Duration
	maxBytes int64
	resolve  func(context.Context, string) ([]netip.Addr, error)
	dial     func(context.Context, string, string) (net.Conn, error)
}

func NewEgressProxy(c EgressProxyConfig) (*EgressProxy, error) {
	if len(c.Hosts) == 0 || len(c.Hosts) > 64 {
		return nil, ErrScope
	}
	if c.MaxTunnels == 0 {
		c.MaxTunnels = 4
	}
	if c.Lifetime == 0 {
		c.Lifetime = 30 * time.Second
	}
	if c.BytesPerDirection == 0 {
		c.BytesPerDirection = 16 << 20
	}
	if c.MaxTunnels < 1 || c.MaxTunnels > 16 || c.Lifetime < time.Millisecond || c.Lifetime > 2*time.Minute || c.BytesPerDirection < 64<<10 || c.BytesPerDirection > 64<<20 {
		return nil, ErrScope
	}
	p := &EgressProxy{hosts: map[string]bool{}, slots: make(chan struct{}, c.MaxTunnels), lifetime: c.Lifetime, maxBytes: c.BytesPerDirection}
	for _, h := range c.Hosts {
		o, e := origin("https://" + h)
		if e != nil || o != "https://"+strings.ToLower(h) {
			return nil, ErrScope
		}
		p.hosts[strings.TrimPrefix(o, "https://")] = true
	}
	p.resolve = func(ctx context.Context, h string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
	}
	p.dial = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	return p, nil
}

type helloPeek struct {
	net.Conn
	reader    *bufio.Reader
	prefix    bytes.Buffer
	remaining int
}

func (c *helloPeek) Read(b []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, io.EOF
	}
	if len(b) > c.remaining {
		b = b[:c.remaining]
	}
	n, e := c.reader.Read(b)
	c.remaining -= n
	c.prefix.Write(b[:n])
	return n, e
}
func (c *helloPeek) Write(b []byte) (int, error) { return len(b), nil } // discard local TLS alerts
func (c *helloPeek) Close() error                { return nil }         // actual socket lifetime belongs to handler

func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reject := func(status int) { http.Error(w, "proxy request rejected", status) }
	if r.Method != http.MethodConnect || r.ProtoMajor != 1 || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		reject(405)
		return
	}
	host, port, e := net.SplitHostPort(r.RequestURI)
	if e != nil || port != "443" || !strings.EqualFold(r.Host, r.RequestURI) || !p.hosts[strings.ToLower(host)] {
		reject(403)
		return
	}
	if _, e = origin("https://" + r.RequestURI); e != nil {
		reject(403)
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		reject(503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), p.lifetime)
	defer cancel()
	ips, e := p.resolve(ctx, host)
	if e != nil || len(ips) == 0 || len(ips) > 64 {
		reject(502)
		return
	}
	for _, ip := range ips {
		if fetcher.IsBlockedIP(ip) || ip.Zone() != "" {
			reject(403)
			return
		}
	}
	if ctx.Err() != nil {
		reject(504)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		reject(500)
		return
	}
	client, rw, e := hijacker.Hijack()
	if e != nil {
		return
	}
	defer client.Close()
	deadline, _ := ctx.Deadline()
	client.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	if _, e = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
		return
	}
	if rw.Flush() != nil {
		return
	}
	peek := &helloPeek{Conn: client, reader: rw.Reader, remaining: 64 << 10}
	matched := false
	tlsConn := tls.Server(peek, &tls.Config{GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) {
		matched = strings.EqualFold(info.ServerName, host)
		return nil, ErrProtocol
	}})
	_ = tlsConn.HandshakeContext(ctx)
	if !matched || ctx.Err() != nil {
		return
	}
	var upstream net.Conn
	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		upstream, e = p.dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), "443"))
		if e == nil {
			break
		}
	}
	if e != nil || upstream == nil || ctx.Err() != nil {
		if upstream != nil {
			upstream.Close()
		}
		return
	}
	defer upstream.Close()
	upstream.SetDeadline(deadline)
	stopUp := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stopUp()
	if _, e = io.Copy(upstream, bytes.NewReader(peek.prefix.Bytes())); e != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(upstream, io.LimitReader(rw.Reader, p.maxBytes-int64(peek.prefix.Len())))
		done <- struct{}{}
	}()
	go func() { io.Copy(client, io.LimitReader(upstream, p.maxBytes)); done <- struct{}{} }()
	<-done
	client.Close()
	upstream.Close()
	<-done
}
