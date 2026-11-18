package backup

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
)

type databaseObservation struct {
	State       string // pristine, populated, busy, incompatible; never a commit proof.
	CountsMatch bool
}

// Read-only point-in-time evidence for interrupted operations. Does not alter
// attempt/operation records, acquire ownership, permit cleanup, or infer COMMIT.
func observeRecordedDatabase(ctx context.Context, rawTarget string, expected ManifestCounts) (databaseObservation, error) {
	cfg, e := ParseTarget(rawTarget)
	if e != nil {
		return databaseObservation{}, e
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return databaseObservation{}, ErrRestoreDatabase
	}
	defer c.Close(ctx)
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return databaseObservation{}, ErrRestoreDatabase
	}
	defer tx.Rollback(ctx)
	var owned, safe, exclusive bool
	var version int
	e = tx.QueryRow(ctx, `SELECT
 (SELECT pg_get_userbyid(datdba)=current_user FROM pg_database WHERE datname=current_database()),
 (SELECT NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) FROM pg_roles WHERE rolname=current_user),
 NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker'),
 current_setting('server_version_num')::integer`).Scan(&owned, &safe, &exclusive, &version)
	if e != nil {
		return databaseObservation{}, ErrRestoreDatabase
	}
	if !owned || !safe || version < 160000 || version >= 170000 {
		return databaseObservation{}, ErrTargetDenied
	}
	if !exclusive {
		return databaseObservation{State: "busy"}, nil
	}
	if checkPristineTransaction(ctx, tx) == nil {
		if tx.Commit(ctx) != nil {
			return databaseObservation{}, ErrRestoreDatabase
		}
		return databaseObservation{State: "pristine"}, nil
	}
	if migration.Check(ctx, tx) != nil {
		return databaseObservation{State: "incompatible"}, nil
	}
	match := true
	for table, want := range manifestCountMap(expected) {
		var count int64
		if tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{"public", table}.Sanitize()).Scan(&count) != nil {
			return databaseObservation{State: "incompatible"}, nil
		}
		if count != want {
			match = false
		}
	}
	if tx.Commit(ctx) != nil {
		return databaseObservation{}, ErrRestoreDatabase
	}
	return databaseObservation{State: "populated", CountsMatch: match}, nil
}
