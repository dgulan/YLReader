//go:build integration && linux

package backup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/service"
)

func assertBehaviorLedgerReview(t *testing.T, g *restoreGuard, p Plan, raw, mode string, transport *serviceDispatchFixture, inputs handoverProbeInputs, clock func() time.Time) {
	t.Helper()
	ctx := context.Background()
	transport.pool.Close()
	connect := func() *pgx.Conn {
		t.Helper()
		cfg, e := ParseTarget(raw)
		if e != nil {
			t.Fatal(e)
		}
		conn, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			t.Fatal(e)
		}
		return conn
	}
	snapshot := func() string {
		t.Helper()
		c := connect()
		defer c.Close(ctx)
		var value string
		if c.QueryRow(ctx, `SELECT json_build_array((SELECT json_agg(t ORDER BY id) FROM public.users t),(SELECT json_agg(t ORDER BY id) FROM public.auth_sessions t),(SELECT json_agg(t ORDER BY version) FROM public.schema_migrations t))::text`).Scan(&value) != nil {
			t.Fatal("session fixture snapshot")
		}
		return value
	}
	exec := func(query string, args ...any) {
		t.Helper()
		c := connect()
		defer c.Close(ctx)
		if _, e := c.Exec(ctx, query, args...); e != nil {
			t.Fatal("session fixture mutation failed")
		}
	}
	observe := func() BehaviorSessionObservation {
		t.Helper()
		before := snapshot()
		controlBefore := statusFixtureBytes(t, g.config.Control.Root+"/"+g.config.Control.Namespace)
		r := observeBehaviorSessions(ctx, g.locations.files[3], p, raw, inputs.SourceDatabase, inputs.TargetDatabase, clock())
		if snapshot() != before {
			t.Fatal("read-only session observation wrote database")
		}
		controlAfter := statusFixtureBytes(t, g.config.Control.Root+"/"+g.config.Control.Namespace)
		if len(controlBefore) != len(controlAfter) {
			t.Fatal("session observation wrote ledger")
		}
		for key, value := range controlBefore {
			if controlAfter[key] != value {
				t.Fatal("session observation changed control bytes/modes")
			}
		}
		if r.Dispatch || r.Retry || r.Deployment || r.Persistence != "not_inferred" {
			t.Fatal("observation granted authority")
		}
		return r
	}
	ledger := DiagnoseBehaviorLedger(ctx, g.locations.files[3], p.OperationID, clock())
	expectedLedger := "outcome_unknown"
	expectedDB := "known_sessions_revoked"
	known, revoked, unaccounted := 1, 1, 0
	if mode == "good" {
		expectedLedger = "responses_observed"
		known, revoked = 2, 2
	}
	if mode == "lost" {
		expectedDB = "unaccounted_active"
		known, revoked, unaccounted = 0, 0, 1
	}
	if mode == "intent_ack" {
		expectedDB = "no_known_sessions"
		known, revoked = 0, 0
	}
	t.Run("ledger_response_claims", func(t *testing.T) {
		if ledger.Status != expectedLedger || ledger.Known != known || ledger.Dispatch || ledger.Retry {
			t.Fatalf("wrong ledger observation: status=%s reason=%s known=%d", ledger.Status, ledger.Reason, ledger.Known)
		}
	})
	t.Run("ledger_database_observation", func(t *testing.T) {
		r := observe()
		if r.Database != expectedDB || r.Ledger != expectedLedger || r.Known != known || r.Revoked != revoked || r.Unaccounted != unaccounted {
			t.Fatalf("wrong database observation: db=%s ledger=%s", r.Database, r.Ledger)
		}
	})
	if mode == "good" {
		_, a, e := stableBehaviorLedger(ctx, g.locations.files[3], p.OperationID, clock())
		if e != nil || len(a.known) != 2 {
			t.Fatal("known fixture sessions")
		}
		session := a.known[0]
		other := a.known[1]
		t.Run("ledger_expired_but_active", func(t *testing.T) {
			c := connect()
			var revoked, expires time.Time
			if c.QueryRow(ctx, `SELECT revoked_at,expires_at FROM public.auth_sessions WHERE id=$1`, session.SessionID).Scan(&revoked, &expires) != nil {
				c.Close(ctx)
				t.Fatal("fixture timestamps")
			}
			c.Close(ctx)
			exec(`UPDATE public.auth_sessions SET revoked_at=NULL,expires_at=now()-interval '1 second' WHERE id=$1`, session.SessionID)
			defer exec(`UPDATE public.auth_sessions SET revoked_at=$1,expires_at=$2 WHERE id=$3`, revoked, expires, session.SessionID)
			r := observe()
			if r.Database != "known_session_active" || r.Active != 1 {
				t.Fatal("expiry treated as revocation")
			}
		})
		t.Run("ledger_missing_session", func(t *testing.T) {
			c := connect()
			var row string
			if c.QueryRow(ctx, `SELECT row_to_json(t)::text FROM public.auth_sessions t WHERE id=$1`, session.SessionID).Scan(&row) != nil {
				c.Close(ctx)
				t.Fatal("fixture row")
			}
			c.Close(ctx)
			exec(`DELETE FROM public.auth_sessions WHERE id=$1`, session.SessionID)
			defer exec(`INSERT INTO public.auth_sessions SELECT * FROM json_populate_record(NULL::public.auth_sessions,$1::json)`, row)
			r := observe()
			if r.Database != "known_session_missing" || r.Missing != 1 {
				t.Fatal("missing session accepted")
			}
		})
		t.Run("ledger_foreign_owner", func(t *testing.T) {
			exec(`UPDATE public.auth_sessions SET user_id=$1 WHERE id=$2`, other.UserID, session.SessionID)
			defer exec(`UPDATE public.auth_sessions SET user_id=$1 WHERE id=$2`, session.UserID, session.SessionID)
			r := observe()
			if r.Database != "owner_mismatch" || r.Foreign != 1 {
				t.Fatal("foreign session accepted")
			}
		})
		t.Run("ledger_unaccounted_active", func(t *testing.T) {
			id := uuid.New()
			exec(`INSERT INTO public.auth_sessions(id,user_id,refresh_token_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')`, id, session.UserID, id.String())
			defer exec(`DELETE FROM public.auth_sessions WHERE id=$1`, id)
			r := observe()
			if r.Database != "unaccounted_active" || r.Unaccounted != 1 {
				t.Fatal("extra active session accepted")
			}
		})
	}
	// Recreate the test adapter pool only for the fixture operator's explicit
	// cleanup of intentionally lost responses, after all exclusive reads finish.
	pool := newIsolatedTargetPool(t, ctx, raw)
	t.Cleanup(pool.Close)
	transport.pool = pool
	transport.auth = service.NewAuthService(pool, inputs.TargetJWT, time.Minute, time.Hour)
	encoded, _ := json.Marshal(map[string]any{"scope": "isolated-ledger-session-observation", "mode": mode, "ledger_status": expectedLedger, "database_status": expectedDB, "known_sessions": known, "revoked_sessions": revoked, "unaccounted_active": unaccounted, "readonly": true, "dispatch_authorized": false, "retry_authorized": false})
	t.Logf("BEHAVIOR_LEDGER_EVIDENCE %s", encoded)
}
