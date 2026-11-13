//go:build integration

package service

import (
	"context"
	"strings"
	"testing"
	"yreader/backend/internal/pkg/cache"
)

func TestReplacementChangesInvalidateCachedContent(t *testing.T) {
	f := relocationSeed(t, 50)
	ctx := context.Background()
	calls := 0
	cs := NewContentService(f.pool, relocationFetcher{before: func() { calls++ }}, nil, cache.NewMemory(), nil, nil)
	index := 0
	read := func(want string, cached bool) {
		t.Helper()
		view, err := cs.Get(ctx, f.user, f.book, &index, nil)
		if err != nil || view.Content != strings.Repeat(want+"😀", 50) || view.Cached != cached {
			t.Fatalf("body/cache mismatch: %+v %v", view, err)
		}
	}
	read("甲", false)
	read("甲", true)
	svc := NewReplaceRuleService(f.pool, nil)
	input := ReplaceRuleInput{Name: "replacement", Pattern: "甲", Replacement: "乙", Scope: "content", IsEnabled: true}
	rule, err := svc.Create(ctx, f.user, input)
	if err != nil {
		t.Fatal(err)
	}
	read("乙", false)
	read("乙", true)
	input.Replacement = "丙"
	if _, err = svc.Update(ctx, f.user, rule.ID, input); err != nil {
		t.Fatal(err)
	}
	read("丙", false)
	input.IsEnabled = false
	if _, err = svc.Update(ctx, f.user, rule.ID, input); err != nil {
		t.Fatal(err)
	}
	read("甲", false)
	if err = svc.Delete(ctx, f.user, rule.ID); err != nil {
		t.Fatal(err)
	}
	read("甲", true)
	if calls != 4 {
		t.Fatalf("fetches=%d", calls)
	}
}
