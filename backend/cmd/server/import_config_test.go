package main

import (
	"testing"
	"time"
)

func TestImportTimeoutConfiguration(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 90 * time.Second, "90s": 90 * time.Second, "1s": time.Second, "10m": 10 * time.Minute, " 2m ": 2 * time.Minute} {
		got, err := parseImportTimeout(raw)
		if err != nil || got != want {
			t.Fatalf("%q: %v %v", raw, got, err)
		}
	}
	for _, raw := range []string{"0", "0s", "-1s", "500ms", "11m", "garbage"} {
		if _, err := parseImportTimeout(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
