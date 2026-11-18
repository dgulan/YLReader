package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
)

var (
	ErrRestoreDatabase      = errors.New("backup: isolated database restore failed")
	ErrRestoreCommitUnknown = errors.New("backup: database commit outcome unknown; inspect target before retry")
	ErrRestoreRollback      = errors.New("backup: database rollback could not be confirmed")
)

type databaseRestoreResult struct {
	Counts                                       ManifestCounts
	RevokedSessions, AdvancedUsers, CanceledJobs int64
}

// Internal transaction primitive ONLY. Not a complete restore/apply entry point:
// the caller must provision an independent disposable cluster, and later apply
// orchestration must retain plan/control/storage locks and verify objects/keys.
// No existing business target, database creation/drop or archived SQL execution.
func restoreIsolatedDatabase(ctx context.Context, rawTarget string, pkg *VerifiedPackage, tool PostgresArchiveTool, maxExpandedBytes int64) (result databaseRestoreResult, err error) {
	return restoreDatabaseValidated(ctx, rawTarget, pkg, tool, maxExpandedBytes, nil)
}

func restoreDatabaseValidated(ctx context.Context, rawTarget string, pkg *VerifiedPackage, tool PostgresArchiveTool, maxExpandedBytes int64, objects *stagedObjects) (result databaseRestoreResult, err error) {
	return restoreDatabaseWithGate(ctx, rawTarget, pkg, tool, maxExpandedBytes, objects, nil)
}

func restoreDatabaseWithGate(ctx context.Context, rawTarget string, pkg *VerifiedPackage, tool PostgresArchiveTool, maxExpandedBytes int64, objects *stagedObjects, gate func(context.Context, pgx.Tx) error) (result databaseRestoreResult, err error) {
	cfg, e := ParseTarget(rawTarget)
	if e != nil {
		return result, e
	}
	if pkg == nil {
		return result, ErrRestoreDatabase
	}
	if objects != nil && objects.binding != pkg.Binding() {
		return result, ErrObjectReferences
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	// Pin scratch for the ENTIRE operation, including gaps between tool reads.
	pin, e := pkg.OpenDatabase(ctx)
	if e != nil {
		return result, ErrRestoreDatabase
	}
	defer pin.Close()
	checked, e := pkg.VerifyDatabaseData(ctx, tool, maxExpandedBytes)
	if e != nil {
		return result, e
	}
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return result, ErrRestoreDatabase
	}
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		conn.Close(closeCtx)
	}()
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if e != nil {
		return result, ErrRestoreDatabase
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if e := tx.Rollback(rollbackCtx); e != nil && !errors.Is(e, pgx.ErrTxClosed) && !errors.Is(err, ErrRestoreCommitUnknown) {
			err = ErrRestoreRollback
		}
	}()
	var locked bool
	if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(7931461250613)`).Scan(&locked); e != nil || !locked {
		return result, ErrRestoreDatabase
	}
	if e = checkPristineTransaction(ctx, tx); e != nil {
		return result, e
	}
	var version int
	if e = tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version); e != nil {
		return result, ErrRestoreDatabase
	}
	if version < 160000 || version >= 170000 {
		return result, ErrTargetDenied
	}
	if _, e = tx.Exec(ctx, `SET LOCAL search_path=public,pg_catalog; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='60s'`); e != nil {
		return result, ErrRestoreDatabase
	}
	if e = migration.UpInTransaction(ctx, tx); e != nil {
		return result, ErrRestoreDatabase
	}
	// The seed and all imported ledger rows live in this same transaction.
	if _, e = tx.Exec(ctx, `DELETE FROM public.schema_migrations`); e != nil {
		return result, ErrRestoreDatabase
	}
	for _, table := range restoreTableOrder {
		if e = copyArchiveTable(ctx, conn, pkg, tool, table, maxExpandedBytes); e != nil {
			return result, e
		}
	}
	for _, seq := range []struct {
		name  string
		state SequenceState
	}{{"chapters_id_seq", checked.ChaptersSequence}, {"source_failures_id_seq", checked.FailuresSequence}} {
		if _, e = tx.Exec(ctx, `SELECT pg_catalog.setval($1::regclass,$2,$3)`, "public."+seq.name, seq.state.Value, seq.state.Called); e != nil {
			return result, ErrRestoreDatabase
		}
	}
	if e = migration.Check(ctx, tx); e != nil {
		return result, ErrRestoreDatabase
	}
	for table, want := range manifestCountMap(checked.Counts) {
		var actual int64
		// table is one of the fixed ManifestCounts JSON tags, never archive SQL.
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{"public", table}.Sanitize()).Scan(&actual); e != nil || actual != want {
			return result, ErrRestoreDatabase
		}
	}
	var pending databaseRestoreResult
	if objects != nil {
		if e = validateRestoredReferences(ctx, tx, objects); e != nil {
			return result, e
		}
	}
	pending.Counts = checked.Counts
	if gate != nil {
		if e = gate(ctx, tx); e != nil {
			return result, e
		}
	}
	tag, e := tx.Exec(ctx, `UPDATE public.auth_sessions SET revoked_at=now() WHERE revoked_at IS NULL`)
	if e != nil {
		return result, ErrRestoreDatabase
	}
	pending.RevokedSessions = tag.RowsAffected()
	tag, e = tx.Exec(ctx, `UPDATE public.users SET token_version=token_version+1`)
	if e != nil {
		return result, ErrRestoreDatabase
	}
	pending.AdvancedUsers = tag.RowsAffected()
	tag, e = tx.Exec(ctx, `UPDATE public.jobs SET status='canceled',worker_id=NULL,finished_at=now() WHERE status IN ('queued','running')`)
	if e != nil {
		return result, ErrRestoreDatabase
	}
	pending.CanceledJobs = tag.RowsAffected()
	// Cooperative restore/migration exclusion is provided by the advisory lock.
	// This check cannot fence an administrator or a new non-cooperating client;
	// the surrounding independently isolated cluster remains a prerequisite.
	var exclusive bool
	if e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker')`).Scan(&exclusive); e != nil || !exclusive {
		return result, ErrRestoreDatabase
	}
	if e = tx.Commit(ctx); e != nil {
		if errors.Is(e, pgx.ErrTxCommitRollback) {
			return result, ErrRestoreDatabase
		}
		return result, ErrRestoreCommitUnknown
	}
	committed = true
	return pending, nil
}

func copyArchiveTable(ctx context.Context, conn *pgx.Conn, pkg *VerifiedPackage, tool PostgresArchiveTool, table string, budget int64) error {
	header := ""
	for _, candidate := range strings.Split(strings.TrimSpace(archiveColumnsV1), "\n") {
		if strings.HasPrefix(candidate, "COPY public."+table+" (") {
			header = candidate
			break
		}
	}
	if header == "" {
		return ErrRestoreDatabase
	}
	archive, e := pkg.OpenDatabase(ctx)
	if e != nil {
		return ErrRestoreDatabase
	}
	defer archive.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		e := tool.stream(ctx, archive, func(ctx context.Context, source io.Reader) error {
			_, e := parseArchiveDataSelection(ctx, source, pkg.manifest.Counts, budget, table, writer)
			return e
		}, "--data-only", "--no-owner", "--no-acl", "--file=-", "--schema=public", "--table="+table)
		writer.CloseWithError(e)
		done <- e
	}()
	tag, copyErr := conn.PgConn().CopyFrom(ctx, reader, header)
	reader.CloseWithError(ErrRestoreDatabase)
	if copyErr != nil {
		cancel()
	}
	streamErr := <-done
	if copyErr != nil || streamErr != nil || tag.RowsAffected() != manifestCountMap(pkg.manifest.Counts)[table] {
		return ErrRestoreDatabase
	}
	return nil
}
