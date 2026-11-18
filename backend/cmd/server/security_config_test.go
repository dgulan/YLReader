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
