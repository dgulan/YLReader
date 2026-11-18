//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"yreader/backend/internal/repository/testutil"
)

func TestSourceHealthRepository_RecordFailureAndSuccess(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	ctx := context.Background()

	userID := mustSeedUser(t, tx)
	src := &BookSource{
		ID:              uuid.New(),
		UserID:          userID,
		BookSourceURL:   "https://health.example.com",
		BookSourceName:  "HealthTest",
		BookSourceGroup: "默认",
		Enabled:         true,
		Header:          []byte(`{}`),
		Rules:           []byte(`{}`),
	}
	if err := NewSourceRepository().Create(ctx, tx, src); err != nil {
		t.Fatalf("create source: %v", err)
	}

	repo := NewSourceHealthRepository()
	if err := repo.RecordFailure(ctx, tx, userID, src.ID, FailureKindTimeout, "source_timeout", "request timed out", 5*time.Minute); err != nil {
		t.Fatalf("RecordFailure 1: %v", err)
	}
	if err := repo.RecordFailure(ctx, tx, userID, src.ID, FailureKindNetwork, "source_request_failed", "upstream failed", 5*time.Minute); err != nil {
		t.Fatalf("RecordFailure 2: %v", err)
	}

	got, err := NewSourceRepository().Get(ctx, tx, userID, src.ID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if got.FailureCount != 2 || got.LastCheckedAt == nil {
		t.Fatalf("after 2 failures: failure_count=%d last_checked_at=%v", got.FailureCount, got.LastCheckedAt)
	}

	var activeFailures int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM source_failures WHERE user_id=$1 AND source_id=$2 AND resolved_at IS NULL`, userID, src.ID).Scan(&activeFailures); err != nil {
		t.Fatalf("count active failures: %v", err)
	}
	if activeFailures != 2 {
		t.Fatalf("activeFailures = %d, want 2", activeFailures)
	}

	if err := repo.RecordSuccess(ctx, tx, userID, src.ID, 120); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	got, err = NewSourceRepository().Get(ctx, tx, userID, src.ID)
	if err != nil {
		t.Fatalf("get source after success: %v", err)
	}
	if got.FailureCount != 0 || got.RespondTime != 120 || got.LastSuccessAt == nil {
		t.Fatalf("after success: failure_count=%d respond_time=%d last_success_at=%v", got.FailureCount, got.RespondTime, got.LastSuccessAt)
	}

	if err := tx.QueryRow(ctx, `SELECT count(*) FROM source_failures WHERE user_id=$1 AND source_id=$2 AND resolved_at IS NULL`, userID, src.ID).Scan(&activeFailures); err != nil {
		t.Fatalf("count active failures after resolve: %v", err)
	}
	if activeFailures != 0 {
		t.Fatalf("activeFailures after success = %d, want 0", activeFailures)
	}
}
