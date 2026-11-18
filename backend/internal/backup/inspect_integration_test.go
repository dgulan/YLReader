//go:build integration

package backup

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// This test must be given a freshly provisioned, dedicated disposable cluster.
// It never falls back to DATABASE_URL/TEST_DATABASE_URL or an existing test DB.
func TestPostgresTargetInspectorIsolated(t *testing.T) {
	raw := os.Getenv("BACKUP_INSPECT_DSN")
	if raw == "" {
		t.Skip("dedicated BACKUP_INSPECT_DSN required")
	}
	cleanPG(t)
	cfg, e := ParseTarget(raw)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	inspector := PostgresTargetInspector{}
	if e = inspector.CheckEmpty(ctx, cfg); e != nil {
		t.Fatal("empty owned target rejected", e)
	}
	connection, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = inspector.CheckEmpty(ctx, cfg); !errors.Is(e, ErrPlan) {
		connection.Close(ctx)
		t.Fatal("concurrent connection accepted", e)
	}
	connection.Close(ctx)
	mutate := func(statement string) {
		t.Helper()
		connection, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal(e)
		}
		defer connection.Close(ctx)
		if _, e = connection.Exec(ctx, statement); e != nil {
			t.Fatal(e)
		}
	}
	for _, fixture := range []struct{ name, create, drop string }{
		{"table", "CREATE TABLE public.synthetic_check(id int)", "DROP TABLE public.synthetic_check"},
		{"function", "CREATE FUNCTION public.synthetic_check() RETURNS int LANGUAGE sql AS 'SELECT 1'", "DROP FUNCTION public.synthetic_check()"},
		{"type", "CREATE TYPE public.synthetic_check AS ENUM ('x')", "DROP TYPE public.synthetic_check"},
		{"schema", "CREATE SCHEMA synthetic_check", "DROP SCHEMA synthetic_check"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			mutate(fixture.create)
			defer mutate(fixture.drop)
			if e := inspector.CheckEmpty(ctx, cfg); !errors.Is(e, ErrPlan) {
				t.Fatal("nonpristine target accepted", e)
			}
		})
	}
	wrongOwner := cfg.Copy()
	wrongOwner.User = "postgres"
	wrongOwner.Password = ""
	if e = inspector.CheckEmpty(ctx, wrongOwner); !errors.Is(e, ErrPlan) {
		t.Fatal("privileged/wrong-owner role accepted", e)
	}
	if e = inspector.CheckEmpty(ctx, cfg); e != nil {
		t.Fatal("inspector left persistent changes", e)
	}
}
