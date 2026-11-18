//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
)

// The harness creates a new network-none/tmpfs cluster; no ambient DSN is read.
func TestHandoverDatabaseIdentityIsolated(t *testing.T) {
	if os.Getenv("BACKUP_IDENTITY_ISOLATED") != "1" {
		t.Skip("dedicated disposable identity harness required")
	}
	cleanPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const raw = "postgres://reader:@127.0.0.1:5432/yreader_a1_iso_identity?sslmode=disable"
	const adminRaw = "postgres://postgres:@127.0.0.1:5432/yreader_a1_iso_identity?sslmode=disable"
	connect := func(raw string) *pgx.Conn {
		t.Helper()
		cfg, e := ParseTarget(raw)
		if e != nil {
			t.Fatal("fixture target rejected")
		}
		c, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal("fixture connection failed")
		}
		return c
	}
	adminSQL := func(query string) {
		t.Helper()
		c := connect(adminRaw)
		defer c.Close(ctx)
		if _, e := c.Exec(ctx, query); e != nil {
			t.Fatal("fixture setup failed")
		}
	}
	admin := connect(adminRaw)
	var target, source databaseIdentityPin
	e := admin.QueryRow(ctx, `SELECT (SELECT system_identifier::text FROM pg_catalog.pg_control_system()),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname='yreader_a1_iso_identity'),
 (SELECT oid FROM pg_catalog.pg_roles WHERE rolname='reader'),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname='postgres')`).Scan(&target.SystemID, &target.DatabaseOID, &target.RoleOID, &source.DatabaseOID)
	admin.Close(ctx)
	if e != nil {
		t.Fatal("fixture pins unavailable")
	}
	source.SystemID, source.RoleOID = target.SystemID, target.RoleOID
	adminSQL(`REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC; GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO reader`)
	reader := connect(raw)
	if e = migration.Up(ctx, reader); e != nil {
		t.Fatal("fixture baseline failed")
	}
	if _, e = reader.Exec(ctx, `CREATE TABLE public.identity_sentinel(id integer PRIMARY KEY,note text); INSERT INTO public.identity_sentinel VALUES(1,'中文 😀 unchanged')`); e != nil {
		t.Fatal("fixture marker failed")
	}
	reader.Close(ctx)
	// Snapshot uses fixture admin, then closes before each real probe.
	snapshot := func() string {
		t.Helper()
		c := connect(adminRaw)
		defer c.Close(ctx)
		var text string
		e := c.QueryRow(ctx, `SELECT json_build_array(
 (SELECT json_agg(t ORDER BY version) FROM public.schema_migrations t),
 (SELECT json_agg(t ORDER BY id) FROM public.identity_sentinel t),
 (SELECT proacl::text FROM pg_catalog.pg_proc WHERE oid='pg_catalog.pg_control_system()'::regprocedure))::text`).Scan(&text)
		if e != nil {
			t.Fatal("fixture snapshot failed")
		}
		return text
	}
	before := snapshot()
	hash := ""
	t.Run("success_readonly", func(t *testing.T) {
		want, _ := target.fingerprint()
		got, e := probeHandoverDatabase(ctx, raw, source, target)
		if e != nil || got != want {
			t.Fatal("valid pinned target denied", e)
		}
		hash = got
		if snapshot() != before {
			t.Fatal("probe changed data, ledger or ACL")
		}
	})
	denied := func(t *testing.T, s, p databaseIdentityPin) {
		t.Helper()
		before := snapshot()
		h, e := probeHandoverDatabase(ctx, raw, s, p)
		if h != "" || !errors.Is(e, errDatabaseIdentity) {
			t.Fatal("unsafe target accepted or partial identity returned")
		}
		if snapshot() != before {
			t.Fatal("denied probe changed target")
		}
	}
	t.Run("missing_function_permission", func(t *testing.T) {
		adminSQL(`REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM reader`)
		defer adminSQL(`GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO reader`)
		c := connect(adminRaw)
		var allowed bool
		e := c.QueryRow(ctx, `SELECT pg_catalog.has_function_privilege('reader','pg_catalog.pg_control_system()','EXECUTE')`).Scan(&allowed)
		c.Close(ctx)
		if e != nil || allowed {
			t.Fatal("fixture did not establish missing function permission")
		}
		denied(t, source, target)
	})
	t.Run("database_oid_drift", func(t *testing.T) { p := target; p.DatabaseOID++; denied(t, source, p) })
	t.Run("cluster_drift", func(t *testing.T) { p := target; p.SystemID = "123"; denied(t, source, p) })
	t.Run("role_oid_drift", func(t *testing.T) { p := target; p.RoleOID++; denied(t, source, p) })
	t.Run("source_target_alias_other_role", func(t *testing.T) { s := target; s.RoleOID++; denied(t, s, target) })
	t.Run("other_session", func(t *testing.T) { c := connect(raw); defer c.Close(ctx); denied(t, source, target) })
	t.Run("bad_ledger", func(t *testing.T) {
		adminSQL(`UPDATE public.schema_migrations SET checksum='broken'`)
		defer func() {
			c := connect(adminRaw)
			defer c.Close(ctx)
			if _, e := c.Exec(ctx, `UPDATE public.schema_migrations SET checksum=$1`, migration.Expected().Checksum); e != nil {
				t.Error("fixture reset failed")
			}
		}()
		denied(t, source, target)
	})
	t.Run("ledger_view", func(t *testing.T) {
		adminSQL(`ALTER TABLE public.schema_migrations RENAME TO saved_ledger; CREATE VIEW public.schema_migrations AS SELECT * FROM public.saved_ledger`)
		defer adminSQL(`DROP VIEW public.schema_migrations; ALTER TABLE public.saved_ledger RENAME TO schema_migrations`)
		denied(t, source, target)
	})
	t.Run("role_createdb", func(t *testing.T) {
		adminSQL(`ALTER ROLE reader CREATEDB`)
		defer adminSQL(`ALTER ROLE reader NOCREATEDB`)
		denied(t, source, target)
	})
	t.Run("role_membership", func(t *testing.T) {
		adminSQL(`CREATE ROLE identity_extra; GRANT identity_extra TO reader`)
		defer adminSQL(`REVOKE identity_extra FROM reader; DROP ROLE identity_extra`)
		denied(t, source, target)
	})
	t.Run("cancel", func(t *testing.T) {
		c, stop := context.WithCancel(ctx)
		stop()
		h, e := probeHandoverDatabase(c, raw, source, target)
		if h != "" || e != errDatabaseIdentity {
			t.Fatal("canceled probe accepted")
		}
	})
	if t.Failed() {
		return
	}
	if hash == "" || snapshot() != before {
		t.Fatal("fixture not retained unchanged")
	}
	evidence, _ := json.Marshal(struct {
		Scope       string `json:"scope"`
		Version     int    `json:"version"`
		DatabaseSHA string `json:"database_identity_sha256"`
		ReadOnly    bool   `json:"readonly_fixture_unchanged"`
		Deployment  bool   `json:"deployment_authorized"`
		Ready       bool   `json:"ready_for_cutover"`
	}{"isolated-database-identity-fixture", 1, hash, true, false, false})
	t.Log("DATABASE_IDENTITY_EVIDENCE " + string(evidence))
}
