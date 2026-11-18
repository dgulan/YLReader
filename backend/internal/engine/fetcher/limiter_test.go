package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type admissionFunc func(context.Context, string, time.Duration) (time.Duration, error)

func (f admissionFunc) Take(ctx context.Context, key string, d time.Duration) (time.Duration, error) {
	return f(ctx, key, d)
}

func TestDomainKeyCanonicalization(t *testing.T) {
	for _, group := range [][]string{
		{"https://EXAMPLE.com.:443/private?a=secret", "http://example.com:8080/other"},
		{"https://例子.测试/", "https://xn--fsqu00a.xn--0zwm56d/"},
		{"https://example.com。/", "https://example.com/"},
		{"https://[::ffff:8.8.8.8]/", "https://8.8.8.8:8080/"},
	} {
		var first string
		for _, raw := range group {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			key, err := domainKey(u)
			if err != nil {
				t.Fatal(err)
			}
			if first == "" {
				first = key
			} else if key != first {
				t.Fatalf("equivalent hosts differ: %s", raw)
			}
			if strings.Contains(key, "secret") || strings.Contains(key, "example") {
				t.Fatal("raw URL in key")
			}
		}
	}
	u, _ := url.Parse("https:///")
	if _, err := domainKey(u); !errors.Is(err, ErrUnsafeURL) {
		t.Fatal(err)
	}
}

func TestMemoryAdmissionConcurrentAndBounded(t *testing.T) {
	m := NewMemoryAdmission()
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := m.Take(context.Background(), "shared", time.Hour)
			if err != nil {
				t.Error(err)
			} else if d == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatal("concurrent admissions", admitted.Load())
	}
	for i := 1; i < 4096; i++ {
		if _, err := m.Take(context.Background(), fmt.Sprint(i), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Take(context.Background(), "overflow", time.Hour); !errors.Is(err, ErrRateLimit) {
		t.Fatal("live slot evicted", err)
	}
	m.mu.Lock()
	m.next["shared"] = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if d, err := m.Take(context.Background(), "new", time.Hour); err != nil || d != 0 {
		t.Fatal(d, err)
	}
	if len(m.next) != 4096 {
		t.Fatal("capacity", len(m.next))
	}
}

func TestDomainAdmissionWaitCancellationAndFailures(t *testing.T) {
	u, _ := url.Parse("https://example.com")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	l := NewDomainLimiter(admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) { called = true; return 0, nil }))
	if err := l.wait(ctx, u); !errors.Is(err, context.Canceled) || called {
		t.Fatal(called, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	l = NewDomainLimiter(admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) { return time.Second / 2, nil }))
	if err := l.wait(ctx, u); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrRateLimit) {
		t.Fatal(err)
	}
	for _, store := range []AdmissionStore{nil,
		admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) {
			return 0, errors.New("private Redis endpoint password")
		}),
		admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) { return -time.Second, nil }),
	} {
		err := NewDomainLimiter(store).wait(context.Background(), u)
		if !errors.Is(err, ErrUpstreamError) || !errors.Is(err, ErrRateLimit) || strings.Contains(err.Error(), "password") {
			t.Fatal(err)
		}
	}
}

func TestDomainAdmissionRetriesAndRedirects(t *testing.T) {
	var keys []string
	l := NewDomainLimiter(admissionFunc(func(_ context.Context, key string, _ time.Duration) (time.Duration, error) {
		keys = append(keys, key)
		return 0, nil
	}))
	f := New(Config{DomainLimiter: l, backoff: func(int) time.Duration { return 0 }})
	transport, ok := f.client.Transport.(*limitedTransport)
	if !ok {
		t.Fatal("limiter not installed")
	}
	calls := 0
	transport.base = auditTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		code, header := 200, http.Header{}
		if calls == 1 {
			code = 503
		}
		if calls == 2 {
			code = 302
			header.Set("Location", "https://other.example/final")
		}
		return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})
	if _, err := f.Get(context.Background(), "https://example.com/start", nil); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != keys[1] || keys[1] == keys[2] {
		t.Fatal("attempt/redirect accounting", keys)
	}
}

type trackedBody struct{ closed bool }

func (*trackedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *trackedBody) Close() error           { b.closed = true; return nil }

func TestDomainAdmissionFailureNeverDialsOrRetries(t *testing.T) {
	checks := 0
	l := NewDomainLimiter(admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) {
		checks++
		return 0, errors.New("down")
	}))
	f := New(Config{DomainLimiter: l})
	f.client.Transport.(*limitedTransport).base = auditTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("outbound despite failed admission")
		return nil, nil
	})
	body := &trackedBody{}
	_, err := f.Fetch(context.Background(), "PUT", "https://example.com", nil, body)
	if !errors.Is(err, ErrRateLimit) || checks != 1 || !body.closed {
		t.Fatal(checks, body.closed, err)
	}
}

func TestDomainAdmissionWaitThenRecheck(t *testing.T) {
	u, _ := url.Parse("https://example.com")
	calls := 0
	l := NewDomainLimiter(admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) {
		calls++
		if calls == 1 {
			return time.Millisecond, nil
		}
		return 0, nil
	}))
	if err := l.wait(context.Background(), u); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
}

func TestDomainAdmissionCancellationDuringStore(t *testing.T) {
	u, _ := url.Parse("https://example.com")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := NewDomainLimiter(admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) {
		cancel()
		return 0, nil
	}))
	if err := l.wait(ctx, u); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled admission allowed dispatch", err)
	}
}
