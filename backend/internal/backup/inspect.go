package backup

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

// PostgresTargetInspector only inspects a pristine isolated database owned by
// a non-privileged role. It never performs DDL, grants, cleanup or migrations.
type PostgresTargetInspector struct{}

func (PostgresTargetInspector) CheckEmpty(ctx context.Context, cfg *pgx.ConnConfig) error {
	if cfg == nil || cfg.Host != "127.0.0.1" && cfg.Host != "::1" || cfg.Port == 0 || !isolatedDB.MatchString(cfg.Database) {
		return ErrTargetDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, e := pgx.ConnectConfig(ctx, cfg.Copy())
	if e != nil {
		return planStageError("target_connect")
	}
	defer connection.Close(ctx)
	transaction, e := connection.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		return planStageError("target_tx_begin")
	}
	defer transaction.Rollback(ctx)
	if e = checkPristineTransaction(ctx, transaction); e != nil {
		return e
	}
	if e = transaction.Commit(ctx); e != nil {
		return planStageError("target_tx_commit")
	}
	return nil
}

func checkPristineTransaction(ctx context.Context, transaction pgx.Tx) error {
	var owned, unprivileged, pristine, exclusive bool
	e := transaction.QueryRow(ctx, `SELECT
 (SELECT pg_get_userbyid(datdba)=current_user FROM pg_database WHERE datname=current_database()),
 (SELECT NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) FROM pg_roles WHERE rolname=current_user),
 NOT EXISTS(SELECT 1 FROM pg_class WHERE relnamespace='public'::regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_proc WHERE pronamespace='public'::regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_type WHERE typnamespace='public'::regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_operator WHERE oprnamespace='public'::regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('public','pg_catalog','information_schema','pg_toast'))
 AND NOT EXISTS(SELECT 1 FROM pg_extension WHERE extname<>'plpgsql'),
 NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker')`).Scan(&owned, &unprivileged, &pristine, &exclusive)
	if e != nil {
		return planStageError("target_query")
	}
	if !owned || !unprivileged {
		return planStageError("target_role_or_owner")
	}
	if !pristine {
		return planStageError("target_not_pristine")
	}
	if !exclusive {
		return planStageError("target_not_exclusive")
	}
	return nil
}
