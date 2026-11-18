package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
	"yreader/backend/internal/service"
)

var errBehaviorAccounts = errors.New("backup: restored account verification unavailable or changed")

type verifiedBehaviorAccounts struct {
	guard                            *restoreGuard
	plan, consent, runtime, database string
	redis                            string
	records                          [3]string
	users                            []uuid.UUID
	versions                         []int
	checked, expires                 time.Time
}

func (verifiedBehaviorAccounts) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[restored account observation; no execution authority]")
}
func (verifiedBehaviorAccounts) MarshalJSON() ([]byte, error) { return nil, errBehaviorAccounts }

func validBehaviorAccountInput(prepared *preparedBehaviorInput, sourceKey, targetKey []byte) bool {
	if prepared == nil || prepared.consent.validate() != nil || len(prepared.accounts) != len(prepared.consent.Accounts) || len(sourceKey) < 32 || len(sourceKey) > 4096 || len(targetKey) < 32 || len(targetKey) > 4096 || bytes.Equal(sourceKey, targetKey) {
		return false
	}
	for i, a := range prepared.accounts {
		if !a.valid(prepared.consent.Accounts[i].UserID) || a.UserID == uuid.Nil.String() {
			return false
		}
	}
	return true
}

func accountObservation(ctx context.Context, tx pgx.Tx, source, target databaseIdentityPin, cfg *pgx.ConnConfig) error {
	_, err := accountObservationStage(ctx, tx, source, target, cfg, true)
	return err
}

// HTTP session resolution permits the explicitly bound API's own connections;
// all other role, pin, transaction and ledger checks remain identical.
func accountObservationPolicy(ctx context.Context, tx pgx.Tx, source, target databaseIdentityPin, cfg *pgx.ConnConfig, requireExclusive bool) error {
	_, err := accountObservationStage(ctx, tx, source, target, cfg, requireExclusive)
	return err
}

func accountObservationStage(ctx context.Context, tx pgx.Tx, source, target databaseIdentityPin, cfg *pgx.ConnConfig, requireExclusive bool) (string, error) {
	var actual databaseIdentityPin
	var db, user string
	var safe, readOnly, repeatable, exclusive bool
	var version int
	e := tx.QueryRow(ctx, `SELECT c.system_identifier::text,d.oid,r.oid,d.datname,r.rolname,
 d.datdba=r.oid AND r.rolcanlogin AND current_user=session_user
 AND NOT(r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid),
 pg_catalog.current_setting('transaction_read_only')='on',
 pg_catalog.current_setting('transaction_isolation')='repeatable read',
 NOT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE datid=d.oid AND pid<>pg_catalog.pg_backend_pid() AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker'),
 pg_catalog.current_setting('server_version_num')::integer
 FROM pg_catalog.pg_control_system() c
 JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()
 JOIN pg_catalog.pg_roles r ON r.rolname=current_user`).Scan(&actual.SystemID, &actual.DatabaseOID, &actual.RoleOID, &db, &user, &safe, &readOnly, &repeatable, &exclusive, &version)
	if e != nil {
		return "account_tx_query", errBehaviorAccounts
	}
	if !databasePinsSeparate(source, target) || actual != target || db != cfg.Database || user != cfg.User || !safe || !readOnly || !repeatable || version < 160000 || version >= 170000 {
		return "account_tx_identity", errBehaviorAccounts
	}
	if requireExclusive && !exclusive {
		return "account_tx_exclusive", errBehaviorAccounts
	}
	want := migration.Expected()
	var match bool
	if tx.QueryRow(ctx, `SELECT count(*)=1 AND COALESCE(bool_and(version=$1 AND name=$2 AND checksum=$3),false) FROM public.schema_migrations`, want.Version, want.Name, want.Checksum).Scan(&match) != nil || !match {
		return "account_tx_ledger", errBehaviorAccounts
	}
	return "ok", nil
}

// No authentication service calls: fixed SELECTs in one read-only transaction.
func probeRestoredAccounts(ctx context.Context, raw string, source, target databaseIdentityPin, sourceKey, targetKey []byte, prepared *preparedBehaviorInput, clock func() time.Time) (users []uuid.UUID, err error) {
	users, _, err = probeRestoredAccountsStage(ctx, raw, source, target, sourceKey, targetKey, prepared, clock)
	return users, err
}

func probeRestoredAccountsStage(ctx context.Context, raw string, source, target databaseIdentityPin, sourceKey, targetKey []byte, prepared *preparedBehaviorInput, clock func() time.Time) (users []uuid.UUID, stage string, err error) {
	users, _, stage, err = probeRestoredAccountsVersions(ctx, raw, source, target, sourceKey, targetKey, prepared, clock)
	return users, stage, err
}

func probeRestoredAccountsVersions(ctx context.Context, raw string, source, target databaseIdentityPin, sourceKey, targetKey []byte, prepared *preparedBehaviorInput, clock func() time.Time) (users []uuid.UUID, versions []int, stage string, err error) {
	if ctx == nil || clock == nil || !validBehaviorAccountInput(prepared, sourceKey, targetKey) || !databasePinsSeparate(source, target) {
		return nil, nil, "account_input", errBehaviorAccounts
	}
	cfg, e := ParseTarget(raw)
	if e != nil {
		return nil, nil, "account_target_parse", errBehaviorAccounts
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return nil, nil, "account_db_connect", errBehaviorAccounts
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if conn.Close(closeCtx) != nil {
			users = nil
			versions = nil
			stage = "account_conn_close"
			err = errBehaviorAccounts
		}
	}()
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, nil, "account_tx_begin", errBehaviorAccounts
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if obsStage, e := accountObservationStage(ctx, tx, source, target, cfg, true); e != nil {
		return nil, nil, obsStage, errBehaviorAccounts
	}
	versions = make([]int, len(prepared.accounts))
	expiries := make([]time.Time, len(prepared.accounts))
	for i, a := range prepared.accounts {
		if ctx.Err() != nil {
			return nil, nil, "account_ctx_canceled", errBehaviorAccounts
		}
		id, e := uuid.Parse(a.UserID)
		if e != nil || id == uuid.Nil {
			return nil, nil, "account_input", errBehaviorAccounts
		}
		var username, hash string
		var disabled bool
		if tx.QueryRow(ctx, `SELECT username,password_hash,token_version,is_disabled FROM public.users WHERE id=$1`, id).Scan(&username, &hash, &versions[i], &disabled) != nil || disabled || username != a.Username || checkOldBehaviorAccessAt(a.OldAccess, sourceKey, id, versions[i], clock()) != nil {
			return nil, nil, "account_user_query", errBehaviorAccounts
		}
		ok, e := service.VerifyPassword(hash, a.Password)
		if e != nil || !ok || ctx.Err() != nil {
			return nil, nil, "account_password_verify", errBehaviorAccounts
		}
		sum := sha256.Sum256([]byte(a.OldRefresh))
		var owner uuid.UUID
		var revoked bool
		if tx.QueryRow(ctx, `SELECT user_id,revoked_at IS NOT NULL,expires_at FROM public.auth_sessions WHERE refresh_token_hash=$1`, hex.EncodeToString(sum[:])).Scan(&owner, &revoked, &expiries[i]) != nil || owner != id || !revoked || !expiries[i].After(clock()) {
			return nil, nil, "account_refresh_session", errBehaviorAccounts
		}
		users = append(users, id)
	}
	now := clock()
	if !validStamp(now) || now.Before(prepared.consent.Issued) || !now.Before(prepared.consent.Expires) {
		return nil, nil, "account_freshness", errBehaviorAccounts
	}
	for i, a := range prepared.accounts {
		if checkOldBehaviorAccessAt(a.OldAccess, sourceKey, users[i], versions[i], now) != nil || !expiries[i].After(now) {
			return nil, nil, "account_freshness", errBehaviorAccounts
		}
	}
	if ctx.Err() != nil || tx.Commit(ctx) != nil {
		return nil, nil, "account_tx_commit", errBehaviorAccounts
	}
	return users, versions, "verified", nil
}

func (g *restoreGuard) verifyBehaviorAccounts(ctx context.Context, prepared *preparedBehaviorInput, inputs handoverProbeInputs, clock func() time.Time) (*verifiedBehaviorAccounts, error) {
	if g == nil || ctx == nil || clock == nil || inputs.Guard != g || inputs.Stage == nil || !validBehaviorAccountInput(prepared, inputs.SourceJWT, inputs.TargetJWT) {
		g.setAccountStage("preflight_input")
		return nil, errBehaviorAccounts
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	g.mu.Lock()
	p := g.plan
	g.mu.Unlock()
	probes, e := inputs.probes(p)
	if e != nil {
		g.setAccountStage("probes_init")
		return nil, errBehaviorAccounts
	}
	probeFailedStage := ""
	origDB, origRuntime, origRedis, origData := probes.Database, probes.Runtime, probes.Redis, probes.Data
	probes.Database = func(ctx context.Context) (string, error) {
		v, err := origDB(ctx)
		if err != nil && probeFailedStage == "" {
			probeFailedStage = "handover_database_probe"
		}
		return v, err
	}
	probes.Runtime = func(ctx context.Context) (string, error) {
		v, err := origRuntime(ctx)
		if err != nil && probeFailedStage == "" {
			probeFailedStage = "handover_runtime_probe"
		}
		return v, err
	}
	probes.Redis = func(ctx context.Context) (string, error) {
		v, err := origRedis(ctx)
		if err != nil && probeFailedStage == "" {
			probeFailedStage = "handover_redis_probe"
		}
		return v, err
	}
	if origData != nil {
		probes.Data = func(ctx context.Context, s diagnosticSnapshot) (handoverDataProof, error) {
			v, err := origData(ctx, s)
			if err != nil && probeFailedStage == "" {
				probeFailedStage = "handover_data_probe"
			}
			return v, err
		}
	}
	// Existing guard-backed probes authenticate the data and explicit runtime assembly.
	draft, e := collectHandoverReview(ctx, diagnosticFiles{guardBehaviorReader{g}}, p, probes, clock)
	if e != nil {
		if probeFailedStage == "" {
			probeFailedStage = "handover_review_collect"
		}
		g.setAccountStage(probeFailedStage)
		return nil, errBehaviorAccounts
	}
	for _, c := range draft.Checks {
		want := "pass"
		if c.ID == "jwt_rotation" || c.ID == "runtime_behavior" {
			want = "unavailable"
		}
		if c.Status != want {
			g.setAccountStage("handover_checks_status")
			return nil, errBehaviorAccounts
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil || !g.publicationConfirmed || inputs.OwnerID != prepared.consent.OwnerID {
		g.accountStage = "guard_state"
		return nil, errBehaviorAccounts
	}
	s, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
	if e != nil || draft.validateBindingsFor(p, s, draft.TargetIdentity, clock()) != nil || prepared.consent.matches(p, s, draft.TargetIdentity.Runtime, clock()) != nil {
		g.accountStage = "snapshot_binding"
		return nil, errBehaviorAccounts
	}
	cfg, e := g.recheckBindingsLocked(ctx, p.TargetID, clock(), PostgresTargetInspector{}, false)
	accountCfg, parseErr := ParseTarget(inputs.DatabaseURL)
	if e != nil || parseErr != nil || cfg.User != accountCfg.User {
		g.accountStage = "recheck_bindings"
		return nil, errBehaviorAccounts
	}
	users, versions, probeStage, e := probeRestoredAccountsVersions(ctx, inputs.DatabaseURL, inputs.SourceDatabase, inputs.TargetDatabase, inputs.SourceJWT, inputs.TargetJWT, prepared, clock)
	if e != nil {
		g.accountStage = probeStage
		return nil, errBehaviorAccounts
	}
	if _, e = g.recheckBindingsLocked(ctx, p.TargetID, clock(), PostgresTargetInspector{}, false); e != nil {
		g.accountStage = "post_recheck_bindings"
		return nil, errBehaviorAccounts
	}
	after, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
	now := clock()
	if e != nil || after.tokens != s.tokens || prepared.consent.matches(p, after, draft.TargetIdentity.Runtime, now) != nil || !now.Before(draft.Expires) || ctx.Err() != nil {
		g.accountStage = "post_snapshot_binding"
		return nil, errBehaviorAccounts
	}
	g.accountStage = "verified"
	return &verifiedBehaviorAccounts{guard: g, plan: p.Digest, consent: prepared.consent.ID, runtime: draft.TargetIdentity.Runtime, database: draft.TargetIdentity.Database, redis: draft.TargetIdentity.Redis, records: s.tokens, users: users, versions: versions, checked: now, expires: prepared.consent.Expires}, nil
}

// Pins access to the live control handle without retaining it across guard Close.
type guardBehaviorReader struct{ g *restoreGuard }

func (r guardBehaviorReader) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	if r.g.locations == nil {
		return nil, errBehaviorAccounts
	}
	return r.g.locations.files[3].Get(ctx, key)
}
