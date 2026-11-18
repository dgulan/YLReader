package main

import (
	"strings"
	"testing"
)

func TestProductionJWTStrength(t *testing.T) {
	for _, s := range []string{"", "short", strings.Repeat("a", 31)} {
		if validateJWTSecret(s, "production") == nil {
			t.Fatal("weak key accepted")
		}
	}
	if validateJWTSecret(strings.Repeat("a", 32), "production") != nil || validateJWTSecret("", "development") != nil {
		t.Fatal("valid/dev key rejected")
	}
}

func TestParseSandboxConfig(t *testing.T) {
	// Defaults match ADR-020 / ADR-021 baseline (32 MiB, 64 MiB inflight, 4 concurrent).
	cfg, err := parseSandboxConfig("", "", "")
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if cfg.InputLimit != 32<<20 || cfg.InflightBytes != 64<<20 || cfg.MaxConcurrent != 4 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	// Custom valid values.
	cfg, err = parseSandboxConfig(" 16 ", " 64 ", " 8 ")
	if err != nil {
		t.Fatalf("valid custom config rejected: %v", err)
	}
	if cfg.InputLimit != 16<<20 || cfg.InflightBytes != 64<<20 || cfg.MaxConcurrent != 8 {
		t.Fatalf("unexpected custom config: %+v", cfg)
	}

	// Boundary valid values.
	cfg, err = parseSandboxConfig("1", "2", "32")
	if err != nil {
		t.Fatalf("boundary config rejected: %v", err)
	}
	if cfg.InputLimit != 1<<20 || cfg.InflightBytes != 2<<20 || cfg.MaxConcurrent != 32 {
		t.Fatalf("unexpected boundary config: %+v", cfg)
	}

	// Fail-closed on invalid values or violated 2x inflight invariant.
	badCases := [][3]string{
		{"0", "", ""},
		{"-1", "", ""},
		{"33", "", ""},
		{"abc", "", ""},
		{"", "63", ""},
		{"16", "31", ""},
		{"", "0", ""},
		{"", "4097", ""},
		{"", "xyz", ""},
		{"", "", "0"},
		{"", "", "33"},
		{"", "", "many"},
	}
	for _, tc := range badCases {
		if _, err := parseSandboxConfig(tc[0], tc[1], tc[2]); err == nil {
			t.Fatalf("invalid sandbox config accepted: %+v", tc)
		}
	}
}
