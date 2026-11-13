//go:build integration

package migration

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"sync"
	"testing"
)

func fresh(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("isolated database required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "yreader_a1_iso_mig_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close(ctx)
	})
	return pool
}
func TestFreshConcurrentAndDrift(t *testing.T) {
	p := fresh(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Up(ctx, p) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := Check(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE schema_migrations SET checksum='bad'`); err != nil {
		t.Fatal(err)
	}
	if Up(ctx, p) == nil || Check(ctx, p) == nil {
		t.Fatal("drift accepted")
	}
}
func TestLegacyDataAndRollback(t *testing.T) {
	p := fresh(t)
	ctx := context.Background()
	if err := up(ctx, p, baseline+"\nSELECT nonexistent_migration_function();"); err == nil {
		t.Fatal("expected SQL failure")
	}
	entries, err := Status(ctx, p)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed migration persisted: %v", err)
	}
	if _, err = p.Exec(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO users(username,password_hash) VALUES('preserved','hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO books(user_id,book_type,name,author) SELECT id,'online','preserved book','author' FROM users WHERE username='preserved';
 INSERT INTO reading_progress(user_id,book_id,chapter_index,char_offset) SELECT user_id,id,3,77 FROM books;
 INSERT INTO bookmarks(user_id,book_id,chapter_index,char_offset,note) SELECT user_id,id,3,77,'kept' FROM books;
 INSERT INTO app_settings(user_id,key,value) SELECT id,'reader.font_size','24'::jsonb FROM users;
 DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err = Up(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = Up(ctx, p); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM users WHERE username='preserved'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("user lost")
	}
	if err = p.QueryRow(ctx, `SELECT count(*) FROM reading_progress p JOIN bookmarks b ON b.book_id=p.book_id JOIN books k ON k.id=p.book_id JOIN app_settings s ON s.user_id=p.user_id WHERE p.char_offset=77 AND b.note='kept' AND k.name='preserved book' AND s.value='24'::jsonb`).Scan(&n); err != nil || n != 1 {
		t.Fatal("reading data lost")
	}
	if _, err = p.Exec(ctx, `UPDATE schema_migrations SET version=99`); err != nil {
		t.Fatal(err)
	}
	if Up(ctx, p) == nil {
		t.Fatal("unknown version accepted")
	}
}
