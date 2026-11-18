package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateInputReturnsConservativeJSONSize(t *testing.T) {
	cases := []map[string]any{
		{"data": strings.Repeat("a", 1<<10)},
		{"data": `"quoted"\<&>` + "\n\t"},
		{"nested": map[string]any{"list": []any{1, true, nil, "中文"}}},
	}
	for _, vars := range cases {
		size, err := validateInput(context.Background(), vars, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(vars)
		if err != nil {
			t.Fatal(err)
		}
		if size < len(raw) {
			t.Fatalf("budget size %d underestimates encoded %d", size, len(raw))
		}
	}
}

func TestByteBudgetSerializesLargeAdmissions(t *testing.T) {
	b := newByteBudget(100)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.acquire(ctx, 30); err != nil { // costs 60
		t.Fatal(err)
	}
	blocked, cancelBlocked := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelBlocked()
	if err := b.acquire(blocked, 30); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("second large admission not blocked", err)
	}
	b.release(30)
	if err := b.acquire(ctx, 30); err != nil {
		t.Fatal("budget not released", err)
	}
	b.release(30)
	if err := b.acquire(ctx, 50); err != nil { // costs 100, exactly capacity
		t.Fatal("capacity not fully usable", err)
	}
	b.release(50)
}

func TestByteBudgetRejectsOversizedSingleInput(t *testing.T) {
	b := newByteBudget(100)
	if err := b.acquire(context.Background(), 51); !errors.Is(err, ErrSandboxOutputLimit) {
		t.Fatal(err)
	}
}

func TestNormalizeConfigKeepsOneMaximumInputAdmissible(t *testing.T) {
	c := normalizeConfig(Config{InputLimit: 1 << 20})
	if c.InflightBytes != 2<<20 {
		t.Fatal(c.InflightBytes)
	}
	c = normalizeConfig(Config{InputLimit: 1 << 20, InflightBytes: 1 << 20})
	if c.InflightBytes != 2<<20 {
		t.Fatal("small explicit budget not clamped", c.InflightBytes)
	}
	c = normalizeConfig(Config{InputLimit: 1 << 20, InflightBytes: 8 << 20})
	if c.InflightBytes != 8<<20 {
		t.Fatal("larger explicit budget overridden", c.InflightBytes)
	}
}
