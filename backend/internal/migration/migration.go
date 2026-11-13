package migration

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"github.com/jackc/pgx/v5"
)

//go:embed 001_baseline.sql
var baseline string

type Database interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}
type Entry struct {
	Version  int64  `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}

func Expected() Entry {
	return Entry{1, "baseline_v1_5", fmt.Sprintf("%x", sha256.Sum256([]byte(baseline)))}
}
func Status(ctx context.Context, db Database) ([]Entry, error) {
	var exists bool
	rows, err := db.Query(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	if rows.Next() {
		err = rows.Scan(&exists)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !exists {
		return []Entry{}, nil
	}
	rows, err = db.Query(ctx, `SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Version, &e.Name, &e.Checksum); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func validate(entries []Entry) error {
	expected := Expected()
	for _, e := range entries {
		if e != expected {
			return fmt.Errorf("unknown migration or checksum mismatch at version %d", e.Version)
		}
	}
	return nil
}
func Check(ctx context.Context, db Database) error {
	entries, err := Status(ctx, db)
	if err != nil {
		return err
	}
	if err = validate(entries); err != nil {
		return err
	}
	if len(entries) != 1 {
		return fmt.Errorf("migration required: expected version 1")
	}
	return nil
}
func Up(ctx context.Context, db Beginner) error { return up(ctx, db, baseline) }
func up(ctx context.Context, db Beginner, sql string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7931461250613)`); err != nil {
		return err
	}
	entries, err := Status(ctx, tx)
	if err != nil {
		return err
	}
	if err = validate(entries); err != nil {
		return err
	}
	if len(entries) == 0 {
		if _, err = tx.Exec(ctx, sql); err != nil {
			return err
		}
		e := Expected()
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)`, e.Version, e.Name, e.Checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
