package fetcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const SourceLeaseDuration = 30 * time.Second
const SourceRequestTimeout = 20 * time.Second

// SourceAdmissionStore checks both budgets atomically. window=0 means concurrency.
type SourceAdmissionStore interface {
	TakeSource(context.Context, string, string, string, int, time.Duration, time.Duration) (time.Duration, error)
	ReleaseSource(context.Context, string, string) error
}

type sourcePolicy struct {
	key    string
	limit  int
	window time.Duration
	err    error
}
type sourceContextKey struct{}

// WithSource binds trusted database identity/policy to all downstream HTTP and JS.
func WithSource(ctx context.Context, id, spec string) context.Context {
	p := parseSourcePolicy(spec)
	if p.limit > 0 {
		uid, err := uuid.Parse(id)
		if err != nil || uid == uuid.Nil {
			p.err = ErrRateLimit
		} else {
			p.key = fmt.Sprintf("yreader:fetch:source:v1:%x", sha256.Sum256([]byte(uid.String())))
		}
	}
	return context.WithValue(ctx, sourceContextKey{}, p)
}

func parseSourcePolicy(spec string) sourcePolicy {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return sourcePolicy{}
	}
	if len(spec) > 64 {
		return sourcePolicy{err: ErrRateLimit}
	}
	parts := strings.Split(spec, "/")
	if len(parts) > 2 {
		return sourcePolicy{err: ErrRateLimit}
	}
	positive := func(raw string, max int) (int, bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return 0, false
		}
		for _, c := range raw {
			if c < '0' || c > '9' {
				return 0, false
			}
		}
		n, err := strconv.Atoi(raw)
		return n, err == nil && n > 0 && n <= max
	}
	n, ok := positive(parts[0], 256)
	if !ok {
		return sourcePolicy{err: ErrRateLimit}
	}
	p := sourcePolicy{limit: n}
	if len(parts) == 2 {
		ms, ok := positive(parts[1], 600000)
		if !ok {
			return sourcePolicy{err: ErrRateLimit}
		}
		p.window = time.Duration(ms) * time.Millisecond
	}
	return p
}

type sourceMemoryState struct {
	limit            int
	window           time.Duration
	tokens           float64
	updated, expires time.Time
	leases           map[string]time.Time
}

func (m *MemoryAdmission) TakeSource(ctx context.Context, domain, source, token string, limit int, window, lease time.Duration) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 256 || window < 0 || window > 600000*time.Millisecond || window%time.Millisecond != 0 || lease < time.Millisecond || lease > time.Minute || lease%time.Millisecond != 0 {
		return 0, ErrRateLimit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.sources == nil {
		m.sources = make(map[string]*sourceMemoryState)
	}
	s := m.sources[source]
	if s != nil && !s.expires.After(now) {
		delete(m.sources, source)
		s = nil
	}
	if s == nil {
		if len(m.sources) >= 4096 {
			for k, old := range m.sources {
				if !old.expires.After(now) {
					delete(m.sources, k)
				}
			}
		}
		if len(m.sources) >= 4096 {
			return 0, ErrRateLimit
		}
		s = &sourceMemoryState{limit: limit, window: window, tokens: float64(limit), updated: now, leases: make(map[string]time.Time)}
	}
	if s.limit != limit || s.window != window {
		return s.expires.Sub(now), nil
	}
	for id, expiry := range s.leases {
		if !expiry.After(now) {
			delete(s.leases, id)
		}
	}
	cap := 256
	if window == 0 {
		cap = limit
	}
	if len(s.leases) >= cap {
		delay := lease
		for _, expiry := range s.leases {
			if d := expiry.Sub(now); d < delay {
				delay = d
			}
		}
		return delay, nil
	}
	tokens := s.tokens
	if window > 0 {
		tokens = min(float64(limit), tokens+now.Sub(s.updated).Seconds()*float64(limit)/window.Seconds())
		if tokens < 1 {
			return max(time.Millisecond, time.Duration((1-tokens)*float64(window)/float64(limit))), nil
		}
	}
	if next := m.next[domain]; next.After(now) {
		return next.Sub(now), nil
	}
	if _, exists := m.next[domain]; !exists && len(m.next) >= 4096 {
		for k, expiry := range m.next {
			if !expiry.After(now) {
				delete(m.next, k)
			}
		}
		if len(m.next) >= 4096 {
			return 0, ErrRateLimit
		}
	}
	m.next[domain] = now.Add(DefaultDomainInterval)
	s.leases[token] = now.Add(lease)
	s.tokens, s.updated, s.expires = tokens-1, now, now.Add(max(lease, window))
	if window == 0 {
		s.tokens = 0
	}
	m.sources[source] = s
	return 0, nil
}

func (m *MemoryAdmission) ReleaseSource(_ context.Context, source, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sources[source]; s != nil {
		delete(s.leases, token)
		for id, expiry := range s.leases {
			if !expiry.After(time.Now()) {
				delete(s.leases, id)
			}
		}
		if s.window == 0 && len(s.leases) == 0 {
			delete(m.sources, source)
		}
	}
	return nil
}

type leaseBody struct {
	io.ReadCloser
	finish func()
}

func (b *leaseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.finish()
	}
	return n, err
}
func (b *leaseBody) Close() error { err := b.ReadCloser.Close(); b.finish(); return err }

func releaseSource(store SourceAdmissionStore, key, token string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = store.ReleaseSource(ctx, key, token) // Unknown cleanup retains the lease until expiry.
		})
	}
}
