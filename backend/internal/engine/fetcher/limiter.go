package fetcher

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/idna"
)

const DefaultDomainInterval = 500 * time.Millisecond
const DefaultAdmissionTimeout = 5 * time.Second

var ErrRateLimit = errors.New("fetcher: domain admission unavailable")

// AdmissionStore atomically consumes a slot if available. Zero means admitted;
// positive means retry after that duration. A denial never reserves a future slot.
type AdmissionStore interface {
	Take(context.Context, string, time.Duration) (time.Duration, error)
}

type DomainLimiter struct{ store AdmissionStore }

func NewDomainLimiter(store AdmissionStore) *DomainLimiter { return &DomainLimiter{store: store} }

func domainKey(u *url.URL) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	} else {
		var err error
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return "", ErrUnsafeURL
		}
		host = strings.TrimSuffix(strings.ToLower(host), ".")
	}
	if host == "" {
		return "", ErrUnsafeURL
	}
	return fmt.Sprintf("yreader:fetch:domain:v1:%x", sha256.Sum256([]byte(host))), nil
}

func (l *DomainLimiter) wait(ctx context.Context, u *url.URL) error {
	release, err := l.acquire(ctx, u)
	if release != nil {
		release()
	}
	return err
}

func (l *DomainLimiter) acquire(ctx context.Context, u *url.URL) (func(), error) {
	key, err := domainKey(u)
	if err != nil {
		return nil, err
	}
	p, _ := ctx.Value(sourceContextKey{}).(sourcePolicy)
	if p.err != nil {
		return nil, errors.Join(ErrUpstreamError, ErrRateLimit)
	}
	var store SourceAdmissionStore
	var token string
	var release func()
	if p.limit > 0 {
		store, _ = l.store.(SourceAdmissionStore)
		if store == nil {
			return nil, errors.Join(ErrUpstreamError, ErrRateLimit)
		}
		token = uuid.NewString()
		release = releaseSource(store, p.key, token)
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultAdmissionTimeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(ErrUpstreamError, ErrRateLimit, err)
		}
		if l.store == nil {
			return nil, errors.Join(ErrUpstreamError, ErrRateLimit)
		}
		var delay time.Duration
		if store != nil {
			delay, err = store.TakeSource(ctx, key, p.key, token, p.limit, p.window, SourceLeaseDuration)
		} else {
			delay, err = l.store.Take(ctx, key, DefaultDomainInterval)
		}
		if ctx.Err() != nil {
			if release != nil {
				release()
			}
			return nil, errors.Join(ErrUpstreamError, ErrRateLimit, ctx.Err())
		}
		maxDelay := DefaultDomainInterval
		if store != nil {
			maxDelay = max(SourceLeaseDuration, p.window)
		}
		if err != nil || delay < 0 || delay > maxDelay {
			// Never return a storage error, which may contain server details.
			if release != nil {
				release()
			}
			return nil, errors.Join(ErrUpstreamError, ErrRateLimit)
		}
		if delay == 0 {
			return release, nil
		}
		if err := sleepBackoff(ctx, delay); err != nil {
			return nil, errors.Join(ErrUpstreamError, ErrRateLimit, err)
		}
	}
}

type limitedTransport struct {
	base    http.RoundTripper
	limiter *DomainLimiter
}

func (t *limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	err := validateTarget(r.URL)
	var release func()
	if err == nil {
		release, err = t.limiter.acquire(r.Context(), r.URL)
	}
	if err != nil {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, err
	}
	if release == nil {
		return t.base.RoundTrip(r)
	}
	ctx, cancel := context.WithTimeout(r.Context(), SourceRequestTimeout)
	var once sync.Once
	finish := func() { once.Do(func() { cancel(); release() }) }
	context.AfterFunc(ctx, finish)
	resp, err := t.base.RoundTrip(r.Clone(ctx))
	if err != nil {
		finish()
		return nil, err
	}
	if resp.Body == nil {
		finish()
	} else {
		resp.Body = &leaseBody{ReadCloser: resp.Body, finish: finish}
	}
	return resp, nil
}

func (t *limitedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// MemoryAdmission is process-local, bounded, and never evicts a live slot.
type MemoryAdmission struct {
	mu      sync.Mutex
	next    map[string]time.Time
	sources map[string]*sourceMemoryState
}

func NewMemoryAdmission() *MemoryAdmission { return &MemoryAdmission{next: make(map[string]time.Time)} }

func (m *MemoryAdmission) Take(ctx context.Context, key string, interval time.Duration) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if interval <= 0 {
		return 0, ErrRateLimit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if next := m.next[key]; next.After(now) {
		return next.Sub(now), nil
	}
	if _, exists := m.next[key]; !exists && len(m.next) >= 4096 {
		for k, next := range m.next {
			if !next.After(now) {
				delete(m.next, k)
			}
		}
		if len(m.next) >= 4096 {
			return 0, ErrRateLimit
		}
	}
	m.next[key] = now.Add(interval)
	return 0, nil
}
