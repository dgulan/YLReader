package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/cache"
)

func TestSourcePolicyParsing(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		n      int
		window time.Duration
	}{
		{"", 0, 0}, {"  ", 0, 0}, {"003", 3, 0}, {" 02 / 01000 ", 2, time.Second}, {"256/600000", 256, 10 * time.Minute},
	} {
		p := parseSourcePolicy(tc.raw)
		if p.err != nil || p.limit != tc.n || p.window != tc.window {
			t.Fatal(tc.raw, p)
		}
	}
	for _, raw := range []string{"0", "-1", "+1", "257", "1/0", "1/600001", "1/2/3", "1.2", "３", "999999999999999999999", "1/", strings.Repeat("/", 10000)} {
		if p := parseSourcePolicy(raw); p.err == nil {
			t.Fatal("invalid policy allowed", raw)
		}
	}
}

type combinedStore interface {
	AdmissionStore
	SourceAdmissionStore
}

func TestSourceAdmissionStoreConformance(t *testing.T) {
	for _, driver := range []string{"memory", "redis"} {
		t.Run(driver, func(t *testing.T) {
			var store combinedStore = NewMemoryAdmission()
			if driver == "redis" {
				addr := os.Getenv("TEST_REDIS_ADDR")
				if addr == "" {
					t.Skip("requires isolated TEST_REDIS_ADDR")
				}
				store = cache.NewRedis(addr, time.Second)
			}
			ctx := context.Background()
			key := func() string { return "yreader:test:source:" + uuid.NewString() }
			take := func(domain, source, token string, n int, window, lease time.Duration) time.Duration {
				t.Helper()
				d, err := store.TakeSource(ctx, domain, source, token, n, window, lease)
				if err != nil {
					t.Fatal(err)
				}
				return d
			}
			t.Run("atomic-denial", func(t *testing.T) {
				s, d1, d2 := key(), key(), key()
				if d := take(d1, s, "a", 1, 0, time.Second); d != 0 {
					t.Fatal(d)
				}
				if d := take(d2, s, "b", 1, 0, time.Second); d <= 0 {
					t.Fatal("source overflow")
				}
				if d, err := store.Take(ctx, d2, DefaultDomainInterval); err != nil || d != 0 {
					t.Fatal("denial spent host budget", d, err)
				}
				s2 := key()
				if d := take(d2, s2, "c", 1, 0, time.Second); d <= 0 {
					t.Fatal("host overflow")
				}
				if d := take(key(), s2, "d", 1, 0, time.Second); d != 0 {
					t.Fatal("host denial reserved source", d)
				}
			})
			t.Run("parallel-policy-release-ownership", func(t *testing.T) {
				s := key()
				var count atomic.Int32
				var wg sync.WaitGroup
				winners := make(chan string, 32)
				for i := 0; i < 32; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						id := fmt.Sprint(i)
						d, err := store.TakeSource(ctx, key(), s, id, 2, 0, 5*time.Second)
						if err != nil {
							t.Error(err)
						} else if d == 0 {
							count.Add(1)
							winners <- id
						}
					}(i)
				}
				wg.Wait()
				close(winners)
				if count.Load() != 2 {
					t.Fatal("parallel overflow", count.Load())
				}
				if d := take(key(), s, "changed", 1, 0, 5*time.Second); d <= 0 {
					t.Fatal("policy reset live state")
				}
				var old string
				for id := range winners {
					old = id
					if err := store.ReleaseSource(ctx, s, id); err != nil {
						t.Fatal(err)
					}
				}
				if d := take(key(), s, "new", 1, 0, 5*time.Second); d != 0 {
					t.Fatal("idle numeric policy not changed", d)
				}
				if err := store.ReleaseSource(ctx, s, old); err != nil {
					t.Fatal(err)
				}
				if d := take(key(), s, "blocked", 1, 0, 5*time.Second); d <= 0 {
					t.Fatal("old token released new lease")
				}
			})
			t.Run("bucket-refill-and-expired-lease", func(t *testing.T) {
				s := key()
				for _, id := range []string{"a", "b"} {
					if d := take(key(), s, id, 2, time.Second, 2*time.Second); d != 0 {
						t.Fatal(d)
					}
					if err := store.ReleaseSource(ctx, s, id); err != nil {
						t.Fatal(err)
					}
				}
				if d := take(key(), s, "c", 2, time.Second, 2*time.Second); d <= 0 {
					t.Fatal("release refunded token")
				}
				time.Sleep(600 * time.Millisecond)
				if d := take(key(), s, "c", 2, time.Second, 2*time.Second); d != 0 {
					t.Fatal("bucket did not refill", d)
				}
				if d := take(key(), s, "policy", 3, time.Second, 2*time.Second); d <= 0 {
					t.Fatal("window policy reset")
				}
				s = key()
				if d := take(key(), s, "orphan", 1, 0, 20*time.Millisecond); d != 0 {
					t.Fatal(d)
				}
				time.Sleep(60 * time.Millisecond)
				if d := take(key(), s, "replacement", 1, 0, time.Second); d != 0 {
					t.Fatal("crashed lease not recovered", d)
				}
				_ = store.ReleaseSource(ctx, s, "orphan")
				if d := take(key(), s, "third", 1, 0, time.Second); d <= 0 {
					t.Fatal("expired owner released replacement")
				}
			})
		})
	}
}

func TestSourceResponseLifecycle(t *testing.T) {
	for _, outcome := range []string{"close", "eof", "cancel", "transport-error"} {
		t.Run(outcome, func(t *testing.T) {
			m := NewMemoryAdmission()
			ctx, cancel := context.WithCancel(WithSource(context.Background(), uuid.NewString(), "1"))
			defer cancel()
			p := ctx.Value(sourceContextKey{}).(sourcePolicy)
			tr := &limitedTransport{limiter: NewDomainLimiter(m), base: auditTransport(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > SourceRequestTimeout {
					t.Error("missing bounded response lifetime")
				}
				if outcome == "transport-error" {
					return nil, errors.New("test transport")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("body")), Request: r}, nil
			})}
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com", nil)
			resp, err := tr.RoundTrip(req)
			if outcome == "transport-error" {
				if err == nil {
					t.Fatal("missing error")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				m.mu.Lock()
				active := len(m.sources[p.key].leases)
				m.mu.Unlock()
				if active != 1 {
					t.Fatal("released before body completed")
				}
				switch outcome {
				case "close":
					resp.Body.Close()
				case "eof":
					io.ReadAll(resp.Body)
				case "cancel":
					cancel()
				}
			}
			until := time.Now().Add(time.Second)
			for {
				m.mu.Lock()
				s := m.sources[p.key]
				m.mu.Unlock()
				if s == nil {
					break
				}
				if time.Now().After(until) {
					t.Fatal("lease leaked")
				}
				time.Sleep(time.Millisecond)
			}
			if resp != nil {
				resp.Body.Close()
			}
		})
	}
}

func TestInvalidSourceOrMissingDriverFailsClosed(t *testing.T) {
	u, _ := url.Parse("https://example.com")
	for _, ctx := range []context.Context{
		WithSource(context.Background(), uuid.NewString(), "-1"),
		WithSource(context.Background(), "", "1"),
		WithSource(context.Background(), uuid.NewString(), "1"),
	} {
		called := false
		l := NewDomainLimiter(admissionFunc(func(context.Context, string, time.Duration) (time.Duration, error) { called = true; return 0, nil }))
		if _, err := l.acquire(ctx, u); !errors.Is(err, ErrRateLimit) || called {
			t.Fatal("invalid source bypassed", called, err)
		}
	}
}

func TestSourceMemoryStateBounds(t *testing.T) {
	m := NewMemoryAdmission()
	ctx := context.Background()
	// Capacity is independent of how long race instrumentation takes to fill
	// the map. Expiration is exercised explicitly below, without wall-clock waits.
	const fixtureLease = time.Minute
	fixtureExpiry := time.Now().Add(time.Hour)
	for i := 0; i < 4096; i++ {
		key := fmt.Sprint(i)
		if d, err := m.TakeSource(ctx, key, key, "token", 1, 0, fixtureLease); err != nil || d != 0 {
			t.Fatal(i, d, err)
		}
		m.mu.Lock()
		m.sources[key].expires = fixtureExpiry // Pin only fixture state; lease validation stays real.
		m.mu.Unlock()
	}
	if _, err := m.TakeSource(ctx, "overflow", "overflow", "token", 1, 0, fixtureLease); !errors.Is(err, ErrRateLimit) {
		t.Fatal("state cap bypassed", err)
	}
	m.mu.Lock()
	m.next["0"] = time.Now().Add(-time.Second)
	m.sources["0"].expires = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if d, err := m.TakeSource(ctx, "new", "new", "token", 1, 0, fixtureLease); err != nil || d != 0 {
		t.Fatal("expired state not reclaimed", d, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sources) != 4096 || m.sources["0"] != nil || m.sources["new"] == nil {
		t.Fatal("reclamation did not replace exactly the expired source")
	}
}

func TestSourceIdentityAndLeaseAcrossRetryRedirect(t *testing.T) {
	m := NewMemoryAdmission()
	ctx := WithSource(context.Background(), uuid.NewString(), "1")
	p := ctx.Value(sourceContextKey{}).(sourcePolicy)
	f := New(Config{DomainLimiter: NewDomainLimiter(m), backoff: func(int) time.Duration { return 0 }})
	calls := 0
	f.client.Transport.(*limitedTransport).base = auditTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if got, _ := r.Context().Value(sourceContextKey{}).(sourcePolicy); got.key != p.key {
			t.Error("source lost across retry or redirect")
		}
		m.mu.Lock()
		active := len(m.sources[p.key].leases)
		m.mu.Unlock()
		if active != 1 {
			t.Error("overlapping or missing lease", active)
		}
		status, header := 200, http.Header{}
		if calls == 1 {
			status = 503
		}
		if calls == 2 {
			status = 302
			header.Set("Location", "https://other.example/final")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader("body")), Request: r}, nil
	})
	if _, err := f.Get(ctx, "https://example.com/start", nil); err != nil || calls != 3 {
		t.Fatal(calls, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sources[p.key] != nil {
		t.Fatal("final response lease leaked")
	}
}
