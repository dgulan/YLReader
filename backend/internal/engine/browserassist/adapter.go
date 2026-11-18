// Package browserassist validates a browser-helper protocol. It has no network
// transport and is not wired into production fetching.
package browserassist

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxResponseBytes = 1 << 20

var (
	ErrScope       = errors.New("browser assistance: invalid scope")
	ErrProtocol    = errors.New("browser assistance: invalid response")
	ErrUnavailable = errors.New("browser assistance: helper unavailable")
	ErrCredential  = errors.New("browser assistance: credential not applicable")
)

// Gateway must bound response reads, honor context, and isolate ALL browser
// egress via externally enforced policy. An initial URL check and the HTTP
// control channel alone are insufficient. No production wiring is supplied.
type Gateway interface {
	Exchange(context.Context, []byte, int) ([]byte, error)
}
type Scope struct{ User, Source, Origin, EgressVersion string }
type Adapter struct {
	gateway Gateway
	timeout time.Duration
	slot    chan struct{}
	now     func() time.Time
}

func New(gateway Gateway, timeout time.Duration) (*Adapter, error) {
	if gateway == nil {
		return nil, ErrUnavailable
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	if timeout < time.Millisecond || timeout > 30*time.Second {
		return nil, ErrScope
	}
	return &Adapter{gateway: gateway, timeout: timeout, slot: make(chan struct{}, 1), now: time.Now}, nil
}

func origin(raw string) (string, error) {
	if len(raw) > 8192 {
		return "", ErrScope
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") {
		return "", ErrScope
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return "", ErrScope
	}
	if !strings.Contains(host, ".") {
		return "", ErrScope
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrScope
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", ErrScope
			}
		}
	}
	if len(host) > 253 {
		return "", ErrScope
	}
	return "https://" + host, nil
}

func scopeKey(s Scope) (string, error) {
	for _, v := range []string{s.User, s.Source, s.EgressVersion} {
		if len(v) == 0 || len(v) > 128 {
			return "", ErrScope
		}
	}
	o, e := origin(s.Origin)
	if e != nil {
		return "", e
	}
	u, _ := url.Parse(s.Origin)
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery {
		return "", ErrScope
	}
	s.Origin = o
	b, _ := json.Marshal(s)
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// Credential intentionally has no exported secret fields or JSON serializer.
type Credential struct {
	key, origin, path, ua, value string
	expires                      time.Time
}

func (c Credential) String() string       { return "<browser credential redacted>" }
func (c Credential) GoString() string     { return c.String() }
func (c Credential) ExpiresAt() time.Time { return c.expires }
func (c Credential) Headers(s Scope, target string) (http.Header, error) {
	now := time.Now()
	key, e := scopeKey(s)
	if e != nil || key != c.key || !now.Before(c.expires) {
		return nil, ErrCredential
	}
	o, e := origin(target)
	if e != nil || o != c.origin {
		return nil, ErrCredential
	}
	u, _ := url.Parse(target)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if path != c.path && !(strings.HasPrefix(path, c.path) && (strings.HasSuffix(c.path, "/") || strings.HasPrefix(strings.TrimPrefix(path, c.path), "/"))) {
		return nil, ErrCredential
	}
	return http.Header{"User-Agent": {c.ua}, "Cookie": {"cf_clearance=" + c.value}}, nil
}

func (a *Adapter) Solve(parent context.Context, s Scope, target string) (Credential, error) {
	key, e := scopeKey(s)
	if e != nil {
		return Credential{}, e
	}
	o, e := origin(target)
	authorized, _ := origin(s.Origin)
	if e != nil || o != authorized {
		return Credential{}, ErrScope
	}
	ctx, cancel := context.WithTimeout(parent, a.timeout)
	defer cancel()
	if ctx.Err() != nil {
		return Credential{}, ctx.Err()
	}
	select {
	case a.slot <- struct{}{}:
		defer func() { <-a.slot }()
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	}
	if ctx.Err() != nil {
		return Credential{}, ctx.Err()
	}
	deadline, _ := ctx.Deadline()
	ms := time.Until(deadline).Milliseconds()
	if ms < 1 {
		return Credential{}, context.DeadlineExceeded
	}
	payload, _ := json.Marshal(struct {
		Cmd     string `json:"cmd"`
		URL     string `json:"url"`
		Timeout int64  `json:"maxTimeout"`
	}{"request.get", target, ms})
	raw, e := a.gateway.Exchange(ctx, payload, MaxResponseBytes)
	if ctx.Err() != nil {
		return Credential{}, ctx.Err()
	}
	if e != nil {
		return Credential{}, ErrUnavailable
	}
	if len(raw) > MaxResponseBytes || !validJSON(raw) {
		return Credential{}, ErrProtocol
	}
	var response struct {
		Status   string `json:"status"`
		Solution struct {
			Status  int    `json:"status"`
			URL     string `json:"url"`
			UA      string `json:"userAgent"`
			Cookies []struct {
				Name, Value, Domain, Path string
				Secure                    bool
				Expires                   float64
			} `json:"cookies"`
		} `json:"solution"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Status != "ok" || response.Solution.Status < 200 || response.Solution.Status >= 300 {
		return Credential{}, ErrProtocol
	}
	solution := response.Solution
	final, e := origin(solution.URL)
	if e != nil || final != o || len(solution.UA) == 0 || len(solution.UA) > 1024 || len(solution.Cookies) > 64 {
		return Credential{}, ErrProtocol
	}
	for _, c := range solution.UA {
		if c < 32 || c > 126 {
			return Credential{}, ErrProtocol
		}
	}
	host := strings.TrimPrefix(o, "https://")
	count := 0
	var cred Credential
	now := a.now()
	for _, c := range solution.Cookies {
		if c.Name != "cf_clearance" {
			continue
		}
		count++
		if strings.ToLower(strings.TrimPrefix(c.Domain, ".")) != host || !c.Secure || !strings.HasPrefix(c.Path, "/") || len(c.Path) > 1024 || len(c.Value) == 0 || len(c.Value) > 4096 || c.Expires <= 0 || c.Expires > 1e12 {
			return Credential{}, ErrProtocol
		}
		cookie := http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, Secure: true}
		if cookie.Valid() != nil || strings.ContainsAny(c.Value, " ,") {
			return Credential{}, ErrProtocol
		}
		expires := time.Unix(int64(c.Expires), 0).Add(-30 * time.Second)
		if !expires.After(now) {
			return Credential{}, ErrProtocol
		}
		if limit := now.Add(10 * time.Minute); expires.After(limit) {
			expires = limit
		}
		cred = Credential{key: key, origin: o, path: c.Path, ua: solution.UA, value: c.Value, expires: expires}
	}
	if count != 1 {
		return Credential{}, ErrProtocol
	}
	if ctx.Err() != nil {
		return Credential{}, ctx.Err()
	}
	return cred, nil
}

// Detect duplicates throughout the bounded response; unknown helper metadata is
// accepted, but it cannot override security fields through duplicate keys.
func validJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	nodes := 0
	var walk func(int) bool
	walk = func(depth int) bool {
		nodes++
		if depth > 32 || nodes > 8192 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		if x, ok := t.(json.Delim); ok {
			switch x {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					s, ok := k.(string)
					s = strings.ToLower(s)
					if e != nil || !ok || seen[s] {
						return false
					}
					seen[s] = true
					if !walk(depth + 1) {
						return false
					}
				}
				end, e := d.Token()
				return e == nil && end == json.Delim('}')
			case '[':
				for d.More() {
					if !walk(depth + 1) {
						return false
					}
				}
				end, e := d.Token()
				return e == nil && end == json.Delim(']')
			default:
				return false
			}
		}
		return true
	}
	if !walk(0) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}
