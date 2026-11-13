package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDefaultAccessTokenLifetime(t *testing.T) {
	a := NewAuthService(nil, []byte("test-only-secret"), 0, 0)
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
