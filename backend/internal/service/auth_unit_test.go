package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDefaultAccessTokenLifetime(t *testing.T) {
	a := NewAuthService(nil, []byte("test-only-secret-0123456789abcdef"), 0, 0)
	if a.accessTTL.Seconds() != 900 || a.refreshTTL != 30*24*time.Hour {
		t.Fatal("unexpected default token lifetime")
	}
	token, err := a.issueAccess(uuid.New(), 1)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := a.parseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	if delta := claims.ExpiresAt.Sub(claims.IssuedAt.Time); delta != 15*time.Minute {
		t.Fatalf("JWT lifetime = %v", delta)
	}
}

func TestNewAuthServiceFailClosedSecret(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: expected panic", name)
			}
		}()
		fn()
	}

	t.Setenv("ENVIRONMENT", "")
	mustPanic("empty secret", func() { NewAuthService(nil, nil, 0, 0) })
	mustPanic("short secret non-dev", func() { NewAuthService(nil, []byte("short"), 0, 0) })

	t.Setenv("ENVIRONMENT", "development")
	mustPanic("empty secret in dev", func() { NewAuthService(nil, nil, 0, 0) })
	if s := NewAuthService(nil, []byte("dev-short-key"), 0, 0); s == nil {
		t.Fatal("expected dev service with non-empty secret")
	}
}
