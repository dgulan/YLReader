package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
)

var errDatabaseIdentity = errors.New("backup: isolated database identity unavailable or denied")

// Supplied by trusted provisioning, independently of the connection being
// inspected. Clones and OID reuse require additional external fencing.
type databaseIdentityPin struct {
	SystemID    string `json:"system_identifier"`
	DatabaseOID uint32 `json:"database_oid"`
	RoleOID     uint32 `json:"role_oid"`
}

func (p databaseIdentityPin) valid() bool {
	n, e := strconv.ParseUint(p.SystemID, 10, 64)
	return e == nil && n != 0 && strconv.FormatUint(n, 10) == p.SystemID && p.DatabaseOID != 0 && p.RoleOID != 0
}

func databasePinsSeparate(source, target databaseIdentityPin) bool {
	return source.valid() && target.valid() && (source.SystemID != target.SystemID || source.DatabaseOID != target.DatabaseOID)
}

func (p databaseIdentityPin) fingerprint() (string, error) {
	if !p.valid() {
		return "", errDatabaseIdentity
	}
	data, e := json.Marshal(struct {
		Format      string `json:"format"`
		Version     int    `json:"version"`
		SystemID    string `json:"system_identifier"`
		DatabaseOID uint32 `json:"database_oid"`
		RoleOID     uint32 `json:"role_oid"`
	}{"ylreader-database-identity", 1, p.SystemID, p.DatabaseOID, p.RoleOID})
	if e != nil {
		return "", errDatabaseIdentity
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// probeHandoverDatabase checks a pinned isolated target using fixed read-only
// queries. It never repairs a ledger, grants permissions, or writes a report.
func probeHandoverDatabase(ctx context.Context, raw string, source, target databaseIdentityPin) (string, error) {
	if ctx == nil || !databasePinsSeparate(source, target) {
		return "", errDatabaseIdentity
	}
	cfg, e := ParseTarget(raw)
	if e != nil {
		return "", errDatabaseIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return "", errDatabaseIdentity
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = c.Close(closeCtx)
	}()
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return "", errDatabaseIdentity
	}
	defer tx.Rollback(ctx)
	var actual databaseIdentityPin
	var database, user string
	var owned, safe, login, sameUser, noMembership, exclusive, readOnly, isolation bool
	var version int
	e = tx.QueryRow(ctx, `SELECT control.system_identifier::text,d.oid,r.oid,d.datname,r.rolname,
 d.datdba=r.oid,NOT(r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls),r.rolcanlogin,
 current_user=session_user,
 NOT EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members WHERE member=r.oid),
 NOT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE datid=d.oid AND pid<>pg_catalog.pg_backend_pid() AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker'),
 pg_catalog.current_setting('server_version_num')::integer,
 pg_catalog.current_setting('transaction_read_only')='on',
 pg_catalog.current_setting('transaction_isolation')='repeatable read'
 FROM pg_catalog.pg_control_system() AS control
 JOIN pg_catalog.pg_database AS d ON d.datname=pg_catalog.current_database()
 JOIN pg_catalog.pg_roles AS r ON r.rolname=current_user`).Scan(&actual.SystemID, &actual.DatabaseOID, &actual.RoleOID, &database, &user,
		&owned, &safe, &login, &sameUser, &noMembership, &exclusive, &version, &readOnly, &isolation)
	if e != nil || !actual.valid() || actual != target || database != cfg.Database || user != cfg.User ||
		!owned || !safe || !login || !sameUser || !noMembership || !exclusive || !readOnly || !isolation || version < 160000 || version >= 170000 {
		return "", errDatabaseIdentity
	}
	var ledgerTable, ledgerMatch bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relname='schema_migrations' AND c.relkind='r'
 AND c.relowner=$1 AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity)`, actual.RoleOID).Scan(&ledgerTable)
	if e != nil || !ledgerTable {
		return "", errDatabaseIdentity
	}
	want := migration.Expected()
	e = tx.QueryRow(ctx, `SELECT pg_catalog.count(*)=1 AND COALESCE(pg_catalog.bool_and(version=$1 AND name=$2 AND checksum=$3),false)
 FROM public.schema_migrations`, want.Version, want.Name, want.Checksum).Scan(&ledgerMatch)
	if e != nil || !ledgerMatch {
		return "", errDatabaseIdentity
	}
	if tx.Commit(ctx) != nil {
		return "", errDatabaseIdentity
	}
	return actual.fingerprint()
}
