package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"yreader/backend/internal/pkg/storage"
)

func attemptExample() restoreAttempt {
	return restoreAttempt{Format: "ylreader-restore-attempt", Version: 1, OperationID: "40000000-0000-4000-8000-000000000001", BackupID: "30000000-0000-4000-8000-000000000001", PlanDigest: strings.Repeat("1", 64), OwnerID: "50000000-0000-4000-8000-000000000001", TargetFingerprint: strings.Repeat("2", 64), DatabaseOutcome: "not_started"}
}

func TestAttemptPersistenceAndOutcomes(t *testing.T) {
	ctx := context.Background()
	store := attemptStore{&memoryFiles{data: map[string][]byte{}}}
	initial := attemptExample()
	if e := store.Create(ctx, initial); e != nil {
		t.Fatal(e)
	}
	if e := store.Create(ctx, initial); !errors.Is(e, storage.ErrConflict) {
		t.Fatal(e)
	}
	a, token, e := store.Load(ctx, initial.OperationID)
	if e != nil || a.Generation != nil {
		t.Fatal(e)
	}
	ref := ".scratch-" + strings.Repeat("a", 32)
	a.Generation = &ref
	if e = store.Update(ctx, a.OperationID, token, a); e != nil {
		t.Fatal(e)
	}
	if e = store.Update(ctx, a.OperationID, token, a); !errors.Is(e, storage.ErrConflict) {
		t.Fatal("stale CAS accepted", e)
	}
	for _, outcome := range []string{"unknown", "committed"} {
		a, token, e = store.Load(ctx, a.OperationID)
		if e != nil {
			t.Fatal(e)
		}
		a.DatabaseOutcome = outcome
		if e = store.Update(ctx, a.OperationID, token, a); e != nil {
			t.Fatal(e)
		}
	}
	a, token, e = store.Load(ctx, a.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*restoreAttempt){func(a *restoreAttempt) { a.DatabaseOutcome = "not_started" }, func(a *restoreAttempt) { a.DatabaseOutcome = "rolled_back" }, func(a *restoreAttempt) { a.Generation = nil }, func(a *restoreAttempt) { a.OwnerID = "50000000-0000-4000-8000-000000000002" }, func(a *restoreAttempt) { a.PlanDigest = strings.Repeat("3", 64) }} {
		next := a
		mutate(&next)
		if e = store.Update(ctx, a.OperationID, token, next); !errors.Is(e, ErrAttempt) {
			t.Fatal("immutable attempt changed", e)
		}
	}
}

func TestAttemptRejectsMalformedJSON(t *testing.T) {
	data, _ := json.Marshal(attemptExample())
	valid := string(data)
	if _, e := decodeAttempt([]byte(strings.Replace(valid, "owner_id", "OWNER_ID", 1))); !errors.Is(e, ErrAttempt) {
		t.Fatal("case-folded field accepted", e)
	}
	for name, bad := range map[string]string{"duplicate": strings.Replace(valid, "{", "{\"attempt_version\":1,", 1), "missing": strings.Replace(valid, "\"attempt_version\":1,", "", 1), "unknown": strings.Replace(valid, "{", "{\"extra\":true,", 1), "null-owner": strings.Replace(valid, "\"50000000-0000-4000-8000-000000000001\"", "null", 1), "generation-path": strings.Replace(valid, "\"generation_reference\":null", "\"generation_reference\":\"../arbitrary\"", 1), "unknown-before-generation": strings.Replace(valid, "not_started", "unknown", 1), "over-limit": valid + strings.Repeat(" ", 4096), "trailing": valid + "{}"} {
		t.Run(name, func(t *testing.T) {
			if _, e := decodeAttempt([]byte(bad)); !errors.Is(e, ErrAttempt) {
				t.Fatal(e)
			}
		})
	}
}

func TestAttemptSingleGenerationWinner(t *testing.T) {
	store := attemptStore{&memoryFiles{data: map[string][]byte{}}}
	ctx := context.Background()
	a := attemptExample()
	if e := store.Create(ctx, a); e != nil {
		t.Fatal(e)
	}
	_, token, _ := store.Load(ctx, a.OperationID)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, letter := range []string{"a", "b"} {
		wg.Add(1)
		go func(letter string) {
			defer wg.Done()
			next := a
			ref := ".scratch-" + strings.Repeat(letter, 32)
			next.Generation = &ref
			results <- store.Update(ctx, a.OperationID, token, next)
		}(letter)
	}
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		} else if !errors.Is(e, storage.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("multiple generation owners", wins)
	}
}
