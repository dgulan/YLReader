package backup

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Database observations are separate from response claims and never authorize
// dispatch/retry. No session IDs, credentials or DSNs are exposed here.
type BehaviorSessionObservation struct {
	Format      string `json:"observation_format"`
	Version     int    `json:"observation_version"`
	OperationID string `json:"operation_id"`
	Ledger      string `json:"ledger_status"`
	Database    string `json:"database_status"`
	Known       int    `json:"known_sessions"`
	Revoked     int    `json:"revoked_sessions"`
	Active      int    `json:"active_known_sessions"`
	Missing     int    `json:"missing_sessions"`
	Foreign     int    `json:"owner_mismatches"`
	Unaccounted int    `json:"unaccounted_active_sessions"`
	Persistence string `json:"persistence_confirmation"`
	Dispatch    bool   `json:"dispatch_authorized"`
	Retry       bool   `json:"retry_authorized"`
	Deployment  bool   `json:"deployment_authorized"`
}

func observeBehaviorSessions(ctx context.Context, r RestoreRecordReader, p Plan, raw string, source, target databaseIdentityPin, now time.Time) (result BehaviorSessionObservation) {
	result = BehaviorSessionObservation{Format: "ylreader-behavior-session-observation", Version: 1, Database: "unavailable", Ledger: "unavailable", Persistence: "not_inferred"}
	if ctx == nil || p.Validate() != nil || !databasePinsSeparate(source, target) || !validStamp(now) {
		return
	}
	result.OperationID = p.OperationID
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, a, e := stableBehaviorLedger(ctx, r, p.OperationID, now)
	result.Ledger = a.diagnostic.Status
	if e != nil || a.intent.validate() != nil || a.intent.TargetID != p.TargetID || a.intent.PlanDigest != p.Digest || checkHandoverSnapshot(p, s.base.control) != nil || a.intent.Generation != s.base.control.op.Generation || [3]string{a.intent.OperationSHA, a.intent.AttemptSHA, a.intent.PublicationSHA} != s.base.control.tokens {
		return
	}
	cfg, e := ParseTarget(raw)
	if e != nil || (DatabaseTarget{cfg.Host, cfg.Port, cfg.Database}) != p.Database {
		return
	}
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if conn.Close(cleanup) != nil {
			result = unavailableBehaviorSessions(result, "unavailable")
		}
	}()
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if accountObservation(ctx, tx, source, target, cfg) != nil {
		return
	}
	observed := result
	observed.Known = len(a.known)
	for _, session := range a.known {
		var owner uuid.UUID
		var revoked bool
		e := tx.QueryRow(ctx, `SELECT user_id,revoked_at IS NOT NULL FROM public.auth_sessions WHERE id=$1`, session.SessionID).Scan(&owner, &revoked)
		if e == pgx.ErrNoRows {
			observed.Missing++
			continue
		}
		if e != nil {
			return
		}
		if owner != session.UserID {
			observed.Foreign++
		} else if revoked {
			observed.Revoked++
		} else {
			observed.Active++
		}
	}
	users := make([]uuid.UUID, 0, len(a.intent.Users))
	for _, id := range a.intent.Users {
		users = append(users, uuid.MustParse(id))
	}
	var active int
	if tx.QueryRow(ctx, `SELECT count(*) FROM public.auth_sessions WHERE user_id=ANY($1::uuid[]) AND revoked_at IS NULL`, users).Scan(&active) != nil {
		return
	}
	observed.Unaccounted = active - observed.Active
	if observed.Unaccounted < 0 {
		return
	}
	switch {
	case observed.Foreign > 0:
		observed.Database = "owner_mismatch"
	case observed.Missing > 0:
		observed.Database = "known_session_missing"
	case observed.Unaccounted > 0:
		observed.Database = "unaccounted_active"
	case observed.Active > 0:
		observed.Database = "known_session_active"
	case observed.Known == 0:
		observed.Database = "no_known_sessions"
	default:
		observed.Database = "known_sessions_revoked"
	}
	if ctx.Err() != nil || tx.Commit(ctx) != nil {
		return
	}
	after, _, e := stableBehaviorLedger(ctx, r, p.OperationID, now)
	if e != nil || !s.equal(after) || ctx.Err() != nil {
		result = unavailableBehaviorSessions(result, "changed")
		return
	}
	result = observed
	return
}
func unavailableBehaviorSessions(old BehaviorSessionObservation, status string) BehaviorSessionObservation {
	return BehaviorSessionObservation{Format: old.Format, Version: old.Version, OperationID: old.OperationID, Ledger: old.Ledger, Database: status, Persistence: "not_inferred"}
}
