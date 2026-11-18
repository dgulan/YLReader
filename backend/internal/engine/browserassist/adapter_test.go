package browserassist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type gatewayFunc func(context.Context, []byte, int) ([]byte, error)

func (f gatewayFunc) Exchange(c context.Context, b []byte, n int) ([]byte, error) { return f(c, b, n) }

var fixtureScope = Scope{"user-a", "source-a", "https://books.example", "exit-v1"}

func fixture() map[string]any {
	return map[string]any{"status": "ok", "solution": map[string]any{"status": 200, "url": "https://books.example/read/1", "userAgent": "test-browser", "cookies": []any{map[string]any{"name": "cf_clearance", "value": "private-clearance", "domain": ".books.example", "path": "/read", "secure": true, "expires": float64(time.Now().Add(time.Hour).Unix())}}}}
}
func fixtureAdapter(t *testing.T, edit func(map[string]any)) *Adapter {
	t.Helper()
	a, e := New(gatewayFunc(func(_ context.Context, b []byte, n int) ([]byte, error) {
		var request map[string]any
		if json.Unmarshal(b, &request) != nil || len(request) != 3 || request["cmd"] != "request.get" || n != MaxResponseBytes || request["maxTimeout"].(float64) > 5000 {
			t.Error("unexpected helper request")
		}
		r := fixture()
		if edit != nil {
			edit(r)
		}
		return json.Marshal(r)
	}), 0)
	if e != nil {
		t.Fatal(e)
	}
	return a
}

func TestCredentialIsolationAndRedaction(t *testing.T) {
	a := fixtureAdapter(t, nil)
	c, e := a.Solve(context.Background(), fixtureScope, "https://books.example/read/1")
	if e != nil {
		t.Fatal(e)
	}
	h, e := c.Headers(fixtureScope, "https://books.example:443/read/2?q=1")
	if e != nil || h.Get("User-Agent") != "test-browser" || h.Get("Cookie") != "cf_clearance=private-clearance" {
		t.Fatal("pair lost")
	}
	for _, scope := range []Scope{{"other", "source-a", fixtureScope.Origin, "exit-v1"}, {"user-a", "other", fixtureScope.Origin, "exit-v1"}, {"user-a", "source-a", fixtureScope.Origin, "exit-v2"}} {
		if _, e = c.Headers(scope, "https://books.example/read/1"); !errors.Is(e, ErrCredential) {
			t.Fatal("scope leak")
		}
	}
	for _, target := range []string{"https://evil.example/read/1", "http://books.example/read/1", "https://books.example/reader", "https://books.example/other", "https://books.example:444/read"} {
		if _, e = c.Headers(fixtureScope, target); !errors.Is(e, ErrCredential) {
			t.Fatal("target leak")
		}
	}
	expired := c
	expired.expires = time.Now()
	if _, e = expired.Headers(fixtureScope, "https://books.example/read/1"); e == nil {
		t.Fatal("expired accepted")
	}
	if c.ExpiresAt().After(time.Now().Add(10 * time.Minute)) {
		t.Fatal("TTL cap lost")
	}
	b, _ := json.Marshal(c)
	if string(b) != "{}" || strings.Contains(fmt.Sprintf("%v %#v", c, c), "private-clearance") {
		t.Fatal("secret serialization")
	}
}

func TestRejectInvalidHelperCredentials(t *testing.T) {
	cases := map[string]func(map[string]any){
		"error":             func(r map[string]any) { r["status"] = "error" },
		"challenge remains": func(r map[string]any) { r["solution"].(map[string]any)["status"] = 403 },
		"cross origin":      func(r map[string]any) { r["solution"].(map[string]any)["url"] = "https://evil.example/" },
		"missing UA":        func(r map[string]any) { delete(r["solution"].(map[string]any), "userAgent") },
		"UA injection":      func(r map[string]any) { r["solution"].(map[string]any)["userAgent"] = "a\r\nCookie: bad" },
	}
	for name, value := range map[string]any{"domain": "example", "path": "other", "secure": false, "expires": float64(1), "value": "a; b"} {
		k, v := name, value
		cases[name] = func(r map[string]any) { r["solution"].(map[string]any)["cookies"].([]any)[0].(map[string]any)[k] = v }
	}
	cases["duplicate cookie"] = func(r map[string]any) {
		s := r["solution"].(map[string]any)
		cs := s["cookies"].([]any)
		s["cookies"] = append(cs, cs[0])
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := fixtureAdapter(t, edit).Solve(context.Background(), fixtureScope, "https://books.example/read"); !errors.Is(e, ErrProtocol) {
				t.Fatal(e)
			}
		})
	}
}

func TestInvalidTargetsNeverInvokeHelper(t *testing.T) {
	calls := 0
	a, _ := New(gatewayFunc(func(context.Context, []byte, int) ([]byte, error) { calls++; return nil, nil }), 0)
	for _, target := range []string{"http://books.example/", "https://127.0.0.1/", "https://u:p@books.example/", "https://books.example:8191/", "https://books.example/#token", "https://other.example/"} {
		if _, e := a.Solve(context.Background(), fixtureScope, target); !errors.Is(e, ErrScope) {
			t.Fatal("invalid target accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid target executed")
	}
}

func TestMalformedOrOversizedHelperReply(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", MaxResponseBytes+1), `{"status":"ok","Status":"error"}`, `{} {}`, strings.Repeat("[", 34) + strings.Repeat("]", 34)} {
		calls := 0
		a, _ := New(gatewayFunc(func(context.Context, []byte, int) ([]byte, error) { calls++; return []byte(body), nil }), 0)
		if _, e := a.Solve(context.Background(), fixtureScope, "https://books.example/"); !errors.Is(e, ErrProtocol) || calls != 1 {
			t.Fatal("bad reply accepted or retried")
		}
	}
}

func TestCancellationRetainsOccupiedSlot(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	a, _ := New(gatewayFunc(func(context.Context, []byte, int) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil, errors.New("private helper details")
	}), time.Second)
	first, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := a.Solve(first, fixtureScope, "https://books.example/"); done <- e }()
	<-started
	cancel()
	queued, stop := context.WithCancel(context.Background())
	stop()
	if _, e := a.Solve(queued, fixtureScope, "https://books.example/"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	select {
	case <-done:
		t.Fatal("abandoned uncancellable helper")
	default:
	}
	if calls.Load() != 1 {
		t.Fatal("helper slot escaped")
	}
	deadline, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer end()
	if _, e := a.Solve(deadline, fixtureScope, "https://books.example/"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("queued deadline lost", e)
	}
	if calls.Load() != 1 {
		t.Fatal("queued work reached helper")
	}
	close(release)
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	_, e := a.Solve(context.Background(), fixtureScope, "https://books.example/")
	if !errors.Is(e, ErrUnavailable) || strings.Contains(e.Error(), "private") {
		t.Fatal("error leaked")
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected retries")
	}
}
