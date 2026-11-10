// Package fetcher 提供带 SSRF 防护、编码自动转换与幂等重试的 HTTP 抓取客户端，
// 是书源引擎唯一的出网通道。
package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

// 哨兵错误。
var (
	// ErrSSRFBlocked 表示目标地址命中内网/保留网段，已按 SSRF 策略阻断。
	ErrSSRFBlocked = errors.New("fetcher: request blocked by SSRF policy")
	// ErrBodyTooLarge 表示响应体超过 MaxBodyBytes 上限。
	ErrBodyTooLarge = errors.New("fetcher: response body exceeds limit")
	// ErrRedirectTooMany 表示重定向超过 MaxRedirects 跳数。
	ErrRedirectTooMany = errors.New("fetcher: too many redirects")
)

// 默认超时与上限。
const (
	DefaultConnectTimeout        = 5 * time.Second
	DefaultTLSHandshakeTimeout   = 5 * time.Second
	DefaultResponseHeaderTimeout = 8 * time.Second
	DefaultMaxBodyBytes          = 16 << 20 // 16 MiB
	DefaultMaxRedirects          = 5
	DefaultRetries               = 2
)

// blockedCIDRs 是 netip 内置判定之外仍需显式阻断的保留/特殊网段。
var blockedCIDRs = mustPrefixes(
	"100.64.0.0/10",  // CGNAT
	"198.18.0.0/15",  // benchmark
	"169.254.0.0/16", // link-local IPv4（冗余但显式）
	"240.0.0.0/4",    // reserved / class E
	"64:ff9b::/96",   // NAT64
	"64:ff9b:1::/48", // NAT64 local-use
	"2002::/16",      // 6to4
	"2001::/32",      // Teredo
	"2001:db8::/32",  // documentation
)

// Config 定义抓取客户端参数。零值字段取默认值。
type Config struct {
	ConnectTimeout        time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	MaxBodyBytes          int64
	MaxRedirects          int
	Retries               int

	// 以下为测试钩子（同包测试可注入，外部包不可见）。
	resolveFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)
	dialFunc    func(ctx context.Context, network, addr string) (net.Conn, error)
	backoff     func(attempt int) time.Duration
}

// Response 是解码为 UTF-8 并限制大小后的响应。
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	// FinalURL 是实际完成请求的 URL（重定向后）。供上层做相对链接绝对化；
	// 无法从响应中取到时安全回退为本次 Fetch 的原始入参。
	FinalURL string
}

// Fetcher 是 SSRF 安全的 HTTP 客户端。
type Fetcher struct {
	cfg    Config
	client *http.Client
}

// New 构建 Fetcher，用默认值填充零值配置。
func New(cfg Config) *Fetcher {
	cfg = normalize(cfg)
	f := &Fetcher{cfg: cfg}
	f.client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialContext(ctx, network, addr, &f.cfg)
			},
			TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
			ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
			// 不使用代理，确保 DialContext 的 SSRF 判定始终作用于目标地址。
			Proxy: nil,
		},
		CheckRedirect: f.checkRedirect,
	}
	return f
}

func normalize(c Config) Config {
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = DefaultConnectTimeout
	}
	if c.TLSHandshakeTimeout <= 0 {
		c.TLSHandshakeTimeout = DefaultTLSHandshakeTimeout
	}
	if c.ResponseHeaderTimeout <= 0 {
		c.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.MaxRedirects <= 0 {
		c.MaxRedirects = DefaultMaxRedirects
	}
	if c.Retries <= 0 {
		c.Retries = DefaultRetries
	}
	if c.backoff == nil {
		c.backoff = defaultBackoff
	}
	return c
}

// IsBlockedIP 判定一个 IP 是否属于 SSRF 需阻断的网段。入参为 v4-mapped v6 时先 Unmap。
func IsBlockedIP(addr netip.Addr) bool {
	a := addr.Unmap()
	if !a.IsValid() {
		return true
	}
	if !a.IsGlobalUnicast() ||
		a.IsLoopback() ||
		a.IsPrivate() ||
		a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() ||
		a.IsMulticast() ||
		a.IsUnspecified() {
		return true
	}
	for _, p := range blockedCIDRs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Get 发起一次 GET 请求并返回 UTF-8 解码后的响应。
func (f *Fetcher) Get(ctx context.Context, rawURL string, header http.Header) (*Response, error) {
	return f.Fetch(ctx, http.MethodGet, rawURL, header, nil)
}

// Fetch 发起请求，执行 SSRF 校验、重定向策略、幂等重试与编码转换。
func (f *Fetcher) Fetch(ctx context.Context, method, rawURL string, header http.Header, body io.Reader) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: parse url: %v", ErrUnsafeURL, err)
	}
	if err := validateTarget(u); err != nil {
		return nil, err
	}

	attempts := f.cfg.Retries + 1
	if !isIdempotent(method) {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, f.cfg.backoff(attempt)); err != nil {
				return nil, err
			}
		}
		resp, err := f.do(ctx, method, rawURL, header, body)
		if err != nil {
			lastErr = err
			if !isRetryableErr(err) {
				return nil, err
			}
			continue
		}
		if resp.StatusCode < 500 {
			return f.decode(resp, rawURL)
		}
		// 5xx：幂等请求重试。
		lastErr = fmt.Errorf("fetcher: upstream status %d", resp.StatusCode)
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}
	// 两个重试耗尽出口（5xx 与网络/TLS）统一包装 ErrUpstreamError，
	// 供上层 errors.Is 精确映射为 502 source_request_failed。
	return nil, fmt.Errorf("%w: %w", ErrUpstreamError, lastErr)
}

func (f *Fetcher) do(ctx context.Context, method, rawURL string, header http.Header, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return f.client.Do(req)
}

// validateTarget 校验目标 URL 的形态安全：scheme 仅 http/https、禁止
// userinfo（user:pass@）、端口必须在 1..65535。非法时返回包装后的 ErrUnsafeURL。
func validateTarget(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: unsupported scheme %q", ErrUnsafeURL, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: userinfo is not allowed", ErrUnsafeURL)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%w: invalid port %q", ErrUnsafeURL, p)
		}
	}
	return nil
}

func (f *Fetcher) decode(resp *http.Response, rawURL string) (*Response, error) {
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	r, err := charset.NewReader(resp.Body, ct)
	if err != nil {
		r = resp.Body
	}
	data, err := io.ReadAll(io.LimitReader(r, f.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstreamError, err)
	}
	if int64(len(data)) > f.cfg.MaxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	// 防御性回填 FinalURL：优先取响应携带的请求 URL（重定向后的最终地址），
	// 任一环节为 nil 时安全回退为本次 Fetch 的原始入参，严禁裸调 panic。
	finalURL := rawURL
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       data,
		FinalURL:   finalURL,
	}, nil
}

// checkRedirect 限制跳数、校验 scheme、跨 host 剥离敏感头。
func (f *Fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= f.cfg.MaxRedirects {
		return fmt.Errorf("%w: more than %d redirects", ErrRedirectTooMany, f.cfg.MaxRedirects)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("fetcher: unsupported redirect scheme %q", req.URL.Scheme)
	}
	if len(via) > 0 {
		prev := via[len(via)-1].URL
		if !strings.EqualFold(prev.Hostname(), req.URL.Hostname()) {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
	}
	return nil
}

// dialContext 解析目标全部 IP，过滤被阻断的地址，对剩余 IP 依次拨号，任一成功即返回。
// 若 host 已是 IP 字面量，则跳过 DNS 解析，直接校验并拨号（architecture.md §3.5.2 第 1 步）。
func dialContext(ctx context.Context, network, addr string, cfg *Config) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	dial := cfg.dialFunc
	if dial == nil {
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: cfg.ConnectTimeout}
			return d.DialContext(ctx, network, addr)
		}
	}

	// 第 1 步：host 已是 IP 字面量时，直接 IsBlockedIP 校验并拨号，无需 DNS 解析。
	if ip, perr := netip.ParseAddr(host); perr == nil {
		if IsBlockedIP(ip) {
			return nil, fmt.Errorf("%w: %s", ErrSSRFBlocked, ip)
		}
		return dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
	}

	resolve := cfg.resolveFunc
	if resolve == nil {
		resolve = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, network, host)
		}
	}
	ips, err := resolve(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("fetcher: no addresses resolved for %q", host)
	}

	var errs []error
	attempted := 0
	for _, ip := range ips {
		if IsBlockedIP(ip) {
			errs = append(errs, fmt.Errorf("%w: %s", ErrSSRFBlocked, ip))
			continue
		}
		attempted++
		target := ip.Unmap()
		conn, err := dial(ctx, network, net.JoinHostPort(target.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	if attempted == 0 {
		return nil, fmt.Errorf("%w: all resolved addresses for %q are blocked", ErrSSRFBlocked, host)
	}
	return nil, fmt.Errorf("fetcher: dial %q: %w", host, errors.Join(errs...))
}

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodTrace:
		return true
	}
	return false
}

func isRetryableErr(err error) bool {
	if errors.Is(err, ErrSSRFBlocked) ||
		errors.Is(err, ErrUnsafeURL) ||
		errors.Is(err, ErrRedirectTooMany) ||
		errors.Is(err, ErrBodyTooLarge) {
		return false
	}
	return true
}

func sleepBackoff(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// defaultBackoff：第 1 次重试 200ms、第 2 次 600ms，附带 ±10% 抖动。
func defaultBackoff(attempt int) time.Duration {
	var base time.Duration
	switch attempt {
	case 1:
		base = 200 * time.Millisecond
	default:
		base = 600 * time.Millisecond
	}
	jitter := time.Duration(rand.Int63n(int64(base / 10)))
	return base + jitter
}
