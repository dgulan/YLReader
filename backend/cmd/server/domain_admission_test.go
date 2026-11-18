package main

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
)

type unavailableAdmission struct{ calls int }

func (s *unavailableAdmission) Take(context.Context, string, time.Duration) (time.Duration, error) {
	s.calls++
	return 0, errors.New("test store unavailable")
}

type sourceBridgeAdmission struct {
	unavailableAdmission
	keys []string
}

func (s *sourceBridgeAdmission) TakeSource(_ context.Context, _ string, key, _ string, _ int, _ time.Duration, _ time.Duration) (time.Duration, error) {
	s.keys = append(s.keys, key)
	return 0, errors.New("test source unavailable")
}
func (*sourceBridgeAdmission) ReleaseSource(context.Context, string, string) error { return nil }

func TestJavaAjaxInheritsSourceAdmission(t *testing.T) {
	store := &sourceBridgeAdmission{}
	ft := fetcher.New(fetcher.Config{DomainLimiter: fetcher.NewDomainLimiter(store)})
	ctx := fetcher.WithSource(context.Background(), uuid.NewString(), "1")
	_, err := ft.Get(ctx, "https://example.com", nil)
	if !errors.Is(err, fetcher.ErrRateLimit) {
		t.Fatal(err)
	}
	sb := sandbox.New(sandbox.Config{}, sandboxFetcher{ft: ft})
	_, err = sb.Eval(ctx, `java.ajax("https://other.example/")`, nil)
	if err == nil || store.calls != 0 || len(store.keys) != 2 || store.keys[0] != store.keys[1] {
		t.Fatal("JS lost trusted source context", store, err)
	}
}

func TestSandboxFetcherUsesDomainAdmission(t *testing.T) {
	store := &unavailableAdmission{}
	ft := fetcher.New(fetcher.Config{DomainLimiter: fetcher.NewDomainLimiter(store)})
	bridge := sandboxFetcher{ft: ft}
	for _, method := range []string{"GET", "POST"} {
		if _, err := bridge.Do(context.Background(), method, "https://example.com", []byte("body")); !errors.Is(err, fetcher.ErrRateLimit) {
			t.Fatal("JS bridge bypassed domain guard", err)
		}
	}
	if store.calls != 2 {
		t.Fatal("unexpected retry or bypass", store.calls)
	}
}
