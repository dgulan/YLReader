package cache

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCacheMaxBytesAndEviction(t *testing.T) {
	if defaultMemoryMaxBytes != 64<<20 {
		t.Fatalf("defaultMemoryMaxBytes = %d, want 64 MiB", defaultMemoryMaxBytes)
	}

	now := time.Unix(1000, 0)
	m := NewMemoryWithLimit(20)
	m.now = func() time.Time { return now }
	ctx := context.Background()

	// Oversized single entry (> maxBytes) must be ignored cleanly.
	if err := m.SetEX(ctx, "huge", make([]byte, 30), time.Minute); err != nil {
		t.Fatalf("SetEX oversized: %v", err)
	}
	if m.Len() != 0 || m.UsedBytes() != 0 {
		t.Fatalf("oversized item stored: len=%d bytes=%d", m.Len(), m.UsedBytes())
	}

	// Insert two entries totaling 16 bytes ("k1"+6 = 8, "k2"+6 = 8).
	_ = m.SetEX(ctx, "k1", []byte("123456"), time.Second)
	_ = m.SetEX(ctx, "k2", []byte("abcdef"), time.Minute)
	if m.UsedBytes() != 16 {
		t.Fatalf("UsedBytes = %d, want 16", m.UsedBytes())
	}

	// Advance clock so k1 expires, then insert k3 (8 bytes): expired k1 should be evicted first, keeping k2.
	now = now.Add(2 * time.Second)
	_ = m.SetEX(ctx, "k3", []byte("uvwxyz"), time.Minute)
	if _, err := m.Get(ctx, "k2"); err != nil {
		t.Fatalf("k2 should still be cached after expired k1 eviction: %v", err)
	}
	if _, err := m.Get(ctx, "k1"); err != ErrMiss {
		t.Fatalf("k1 should have been evicted: %v", err)
	}

	// Touching k2 made it MRU, so inserting k4 (8 bytes) must evict oldest live entry k3.
	_ = m.SetEX(ctx, "k4", []byte("999999"), time.Minute)
	if m.UsedBytes() > 20 {
		t.Fatalf("UsedBytes %d exceeded budget 20", m.UsedBytes())
	}
	if _, err := m.Get(ctx, "k3"); err != ErrMiss {
		t.Fatalf("oldest entry k3 should be evicted: %v", err)
	}
	if _, err := m.Get(ctx, "k2"); err != nil {
		t.Fatalf("recently used k2 should remain: %v", err)
	}
}
