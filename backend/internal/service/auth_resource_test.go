package service

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// Malformed tokens must be rejected before account lookup without allocating a
// string slice for every delimiter (GO-2025-3553). No real credentials are used.
func TestMalformedAccessTokenAllocationBound(t *testing.T) {
	auth := NewAuthService(nil, []byte("test-key"), 0, 0)
	token := strings.Repeat(".", 128<<10)
	for i := 0; i < 3; i++ {
		_, _, _ = auth.Authenticate(context.Background(), token)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, err := auth.Authenticate(context.Background(), token)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("malformed token: %v", err)
	}
	t.Logf("malformed token allocation: %d bytes for %d input bytes", after.TotalAlloc-before.TotalAlloc, len(token))
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > uint64(4*len(token)) {
		t.Fatalf("malformed token allocated %d bytes for %d input bytes", allocated, len(token))
	}
}
