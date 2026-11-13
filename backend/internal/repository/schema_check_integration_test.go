//go:build integration

package repository

import (
	"context"
	"testing"
	"yreader/backend/internal/repository/testutil"
)

func TestSchemaCheckRejectsOldBookshelf(t *testing.T) {
	ctx := context.Background()
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_temp; CREATE TEMP VIEW v_bookshelf AS SELECT 1 AS id`); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, tx); err == nil {
		t.Fatal("old view must fail schema check")
	}
	// A rejected PostgreSQL statement aborts its transaction; use a separate one.
	tx.Rollback(ctx)
	tx = testutil.BeginTx(t, pool)
	if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_temp,public; CREATE TEMP VIEW v_bookshelf AS SELECT 1 AS id,0 AS progress_char_offset,1::bigint AS progress_version`); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, tx); err != nil {
		t.Fatal(err)
	}
}
