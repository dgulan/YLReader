package browserassist

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"yreader/backend/internal/engine/fetcher"
)

type HTTPGatewayConfig struct {
	Endpoint       string
	ControlIPs     []netip.Addr
	AllowedOrigins []string
	Timeout        time.Duration
}

// HTTPGateway is ONLY a bounded control channel. The browser deployment must
// enforce an egress proxy/network policy for every navigation and subresource.
// This type is deliberately not registered by cmd/server.
type HTTPGateway struct {
	endpoint string
	timeout  time.Duration
	client   *http.Client
	allowed  map[string]bool
	control  map[netip.Addr]bool
	resolve  func(context.Context, string) ([]netip.Addr, error)
	dial     func(context.Context, string, string) (net.Conn, error)
}

func NewHTTPGateway(c HTTPGatewayConfig) (*HTTPGateway, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || len(c.Endpoint) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "/v1" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, ErrScope
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 || len(c.ControlIPs) == 0 || len(c.ControlIPs) > 16 || len(c.AllowedOrigins) == 0 || len(c.AllowedOrigins) > 64 {
		return nil, ErrScope
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	if c.Timeout < time.Millisecond || c.Timeout > 30*time.Second {
		return nil, ErrScope
	}
	g := &HTTPGateway{endpoint: u.String(), timeout: c.Timeout, allowed: map[string]bool{}, control: map[netip.Addr]bool{}}
	for _, ip := range c.ControlIPs {
		ip = ip.Unmap()
		if !ip.IsValid() || ip.Zone() != "" || (!ip.IsLoopback() && !ip.IsPrivate()) {
			return nil, ErrScope
		}
		g.control[ip] = true
	}
	for _, raw := range c.AllowedOrigins {
		o, e := origin(raw)
		if e != nil {
			return nil, ErrScope
		}
		v, _ := url.Parse(raw)
		if (v.Path != "" && v.Path != "/") || v.RawQuery != "" || v.ForceQuery {
			return nil, ErrScope
		}
		g.allowed[o] = true
	}
	g.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if ip, e := netip.ParseAddr(host); e == nil {
			return []netip.Addr{ip}, nil
		}
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	g.dial = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 32 << 10, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: c.Timeout}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, ErrUnavailable
		}
		ips, e := g.resolve(ctx, host)
		if e != nil || len(ips) == 0 || len(ips) > 16 {
			return nil, ErrUnavailable
		}
		for _, ip := range ips {
			if !g.control[ip.Unmap()] {
				return nil, ErrUnavailable
			}
		}
		// No failover to a different address or a second browser command.
		return g.dial(ctx, network, net.JoinHostPort(ips[0].Unmap().String(), port))
	}
	g.client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return g, nil
}

func (g *HTTPGateway) Exchange(parent context.Context, payload []byte, limit int) ([]byte, error) {
	if len(payload) > 32<<10 || limit < 1 || limit > MaxResponseBytes || !validJSON(payload) {
		return nil, ErrProtocol
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || len(fields) != 3 || fields["cmd"] == nil || fields["url"] == nil || fields["maxTimeout"] == nil {
		return nil, ErrProtocol
	}
	var in struct {
		Cmd     string `json:"cmd"`
		URL     string `json:"url"`
		Timeout int64  `json:"maxTimeout"`
	}
	if json.Unmarshal(payload, &in) != nil || in.Cmd != "request.get" || in.Timeout < 1 || in.Timeout > 30000 {
		return nil, ErrProtocol
	}
	o, e := origin(in.URL)
	if e != nil || !g.allowed[o] {
		return nil, ErrScope
	}
	ctx, cancel := context.WithTimeout(parent, g.timeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	target, _ := url.Parse(in.URL)
	ips, e := g.resolve(ctx, target.Hostname())
	if e != nil || len(ips) == 0 || len(ips) > 64 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	for _, ip := range ips {
		if fetcher.IsBlockedIP(ip) || ip.Zone() != "" {
			return nil, ErrScope
		}
	}
	deadline, _ := ctx.Deadline()
	ms := time.Until(deadline).Milliseconds()
	if ms < 1 {
		return nil, context.DeadlineExceeded
	}
	if in.Timeout > ms {
		in.Timeout = ms
	}
	encoded, _ := json.Marshal(in)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(encoded))
	if e != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	resp, e := g.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > int64(limit) || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return nil, ErrProtocol
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	if len(raw) > limit {
		return nil, ErrProtocol
	}
	return raw, nil
}
