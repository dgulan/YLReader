package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
)

type headerPublicationProof struct {
	binding PackageBinding
	owner   string
	stage   *stagedObjects
	ring    *headercrypto.Keyring
}
type publicationResult struct {
	Receipt  publicationReceipt
	Executed bool
}

func checkPublicationOwner(r io.Reader, operation, backup, sha string) error {
	data, e := io.ReadAll(io.LimitReader(r, 4097))
	if e != nil || len(data) > 4096 {
		return ErrPublicationState
	}
	var owner struct {
		Operation string `json:"operation_id"`
		Backup    string `json:"backup_id"`
		SHA       string `json:"package_sha256"`
	}
	if exactJSON(data, &owner) != nil || owner.Operation != operation || owner.Backup != backup || owner.SHA != sha {
		return ErrPublicationState
	}
	return nil
}

func publicationObjects(stage *stagedObjects) ([]ManifestObject, error) {
	stage.mu.Lock()
	objects := append([]ManifestObject(nil), stage.objects...)
	stage.mu.Unlock()
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	seen := map[string]bool{}
	for _, object := range objects {
		if storage.ValidatePublicationKey(object.Key) != nil {
			return nil, storage.ErrInvalidKey
		}
		if seen[object.Key] {
			return nil, storage.ErrInvalidKey
		}
		for prefix := object.Key; ; {
			i := strings.LastIndexByte(prefix, '/')
			if i < 0 {
				break
			}
			prefix = prefix[:i]
			if seen[prefix] {
				return nil, storage.ErrInvalidKey
			}
		}
		seen[object.Key] = true
	}
	return objects, nil
}

func verifyPublishedObject(ctx context.Context, tree interface {
	Get(context.Context, string) (io.ReadCloser, error)
}, object ManifestObject) error {
	r, e := tree.Get(ctx, object.Key)
	if e != nil {
		return ErrRestoreObjects
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(packageContextReader{ctx, r}, object.Bytes+1))
	closed := r.Close()
	if e != nil || closed != nil || n != object.Bytes || hex.EncodeToString(h.Sum(nil)) != object.SHA {
		return ErrRestoreObjects
	}
	return nil
}

func validatePublicationDatabase(ctx context.Context, cfg *pgx.ConnConfig, pkg *VerifiedPackage, stage *stagedObjects, ring *headercrypto.Keyring) error {
	return validatePublicationDatabaseSessions(ctx, cfg, pkg, stage, ring, nil)
}

func validatePublicationDatabaseSessions(ctx context.Context, cfg *pgx.ConnConfig, pkg *VerifiedPackage, stage *stagedObjects, ring *headercrypto.Keyring, allowance *handoverSessionAllowance) error {
	if allowance != nil && !allowance.valid() {
		return ErrRestoreDatabase
	}
	c, e := pgx.ConnectConfig(ctx, cfg.Copy())
	if e != nil {
		return ErrRestoreDatabase
	}
	defer c.Close(ctx)
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return ErrRestoreDatabase
	}
	defer tx.Rollback(ctx)
	var owned, safe, exclusive bool
	var version int
	e = tx.QueryRow(ctx, `SELECT (SELECT pg_get_userbyid(datdba)=current_user FROM pg_database WHERE datname=current_database()),(SELECT NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) FROM pg_roles WHERE rolname=current_user),NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND usesysid IS NOT NULL AND COALESCE(backend_type,'client backend')<>'autovacuum worker'),current_setting('server_version_num')::integer`).Scan(&owned, &safe, &exclusive, &version)
	if e != nil {
		return targetDeniedStageError("publication_db_query")
	}
	if !owned || !safe {
		return targetDeniedStageError("publication_db_role_or_owner")
	}
	if !exclusive {
		return targetDeniedStageError("publication_db_not_exclusive")
	}
	if version < 160000 || version >= 170000 {
		return targetDeniedStageError("publication_db_version")
	}
	if migration.Check(ctx, tx) != nil {
		return ErrRestoreDatabase
	}
	for table, want := range manifestCountMap(pkg.manifest.Counts) {
		if table == "auth_sessions" && allowance != nil {
			want += int64(len(allowance.ids))
		}
		var n int64
		if tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{"public", table}.Sanitize()).Scan(&n) != nil || n != want {
			return ErrRestoreDatabase
		}
	}
	if allowance != nil {
		var known bool
		if tx.QueryRow(ctx, `SELECT count(*)=$2 AND COALESCE(bool_and(user_id=ANY($3::uuid[]) AND revoked_at IS NOT NULL AND created_at >= $4 AND created_at <= $5),false) FROM public.auth_sessions WHERE id=ANY($1::uuid[])`, allowance.ids, len(allowance.ids), allowance.users, allowance.start, allowance.end).Scan(&known) != nil || !known {
			return ErrRestoreDatabase
		}
	}
	if e = validateRestoredHeaders(ctx, tx, pkg.manifest.HeaderKIDs, ring); e != nil {
		return e
	}
	if e = validateRestoredReferences(ctx, tx, stage); e != nil {
		return e
	}
	var secured bool
	if tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM auth_sessions WHERE revoked_at IS NULL) AND NOT EXISTS(SELECT 1 FROM jobs WHERE status IN ('queued','running'))`).Scan(&secured) != nil || !secured {
		return ErrObjectReferences
	}
	if tx.Commit(ctx) != nil {
		return ErrRestoreDatabase
	}
	return nil
}

// Publishes only a new isolated object root. No application start, deployment,
// ready_for_cutover transition or automatic cleanup occurs on this path.
func (g *restoreGuard) PublishObjects(ctx context.Context, stage *stagedObjects, owner, confirmed string, now time.Time) (result publicationResult, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrPublication) {
			err = errors.Join(ErrPublication, err)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.locations == nil || confirmed != g.plan.TargetID {
		return result, ErrPublicationState
	}
	op, _, e := (Control{Files: g.locations.files[3]}).Load(ctx, g.plan.OperationID)
	if e != nil {
		return result, e
	}
	a, _, e := (attemptStore{Files: g.locations.files[3]}).Load(ctx, g.plan.OperationID)
	if e != nil {
		return result, e
	}
	if !matchingRecordedOperation(op, a) || op.OwnerID != owner || a.PlanDigest != g.plan.Digest || a.BackupID != g.plan.BackupID || a.TargetFingerprint != g.plan.TargetFingerprint || a.DatabaseOutcome != "committed" || a.Generation == nil {
		return result, ErrPublicationState
	}
	expected := publicationFor(g.plan, owner, *a.Generation)
	store := publicationStore{Files: g.locations.files[3]}
	prior, _, e := store.Load(ctx, op.ID)
	if e == nil {
		binding := prior
		binding.State = "planned"
		binding.TreeFingerprint = ""
		if binding != expected {
			return result, ErrPublicationState
		}
		result.Receipt = prior
		if prior.State != "verified" {
			return result, ErrPublication
		}
		return result, nil
	}
	if !errors.Is(e, storage.ErrNotFound) {
		return result, e
	}
	if g.publicationStarted {
		return result, ErrPublication
	}
	proof := g.headerProof
	if stage == nil || proof == nil || proof.stage != stage || proof.owner != owner || proof.binding != g.packageBinding || stage.binding != g.packageBinding || stage.Reference() != *a.Generation || op.Phase != "validating" || op.CancelRequested {
		return result, ErrPublicationState
	}
	objects, e := publicationObjects(stage)
	if e != nil {
		return result, e
	}
	pin, e := stage.open(ctx, "restore-owner.json")
	if e != nil {
		return result, e
	}
	defer pin.Close()
	if e = checkPublicationOwner(pin, op.ID, op.BackupID, g.packageBinding.PackageSHA); e != nil {
		return result, e
	}
	cfg, e := g.recheckBindingsLocked(ctx, confirmed, now, PostgresTargetInspector{}, false)
	if e != nil {
		return result, e
	}
	stage.mu.Lock()
	location, ok := stage.temp.(locatedFiles)
	stage.mu.Unlock()
	if !ok {
		return result, storage.ErrCapability
	}
	info, e := location.Location(ctx)
	if e != nil {
		return result, e
	}
	only, ok := g.locations.files[1].(storage.RestoreDirectory)
	if !ok {
		return result, storage.ErrCapability
	}
	if e = only.OnlyDirectory(ctx, *a.Generation, info.Identity); e != nil {
		return result, e
	}
	creator, ok := g.locations.files[1].(storage.PublicationDirectories)
	if !ok {
		return result, storage.ErrCapability
	}
	if e = validatePublicationDatabase(ctx, cfg, g.verified, stage, proof.ring); e != nil {
		return result, e
	}
	var total int64
	for _, object := range objects {
		total += object.Bytes
	}
	available, e := g.locations.files[1].AvailableBytes(ctx)
	if e != nil || available < total+4096 {
		return result, ErrPackageLimit
	}
	g.publicationStarted = true
	if e = store.Create(ctx, expected); e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	result.Receipt = expected
	result.Executed = true
	tree, e := creator.NewPublication(ctx, op.ID)
	if e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	defer func() {
		if closed := tree.Close(); closed != nil {
			err = errors.Join(err, ErrPublication, closed)
		}
	}()
	treeLocation, e := tree.Location(ctx)
	if e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	next := expected
	next.State = "copying"
	next.TreeFingerprint = fingerprint(treeLocation, "publication")
	if e = updatePublication(ctx, store, &result.Receipt, next); e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	for _, object := range objects {
		r, e := stage.Open(ctx, object.Key)
		if e != nil {
			return result, errors.Join(ErrPublication, e)
		}
		h := sha256.New()
		writeErr := tree.CreateOnly(ctx, object.Key, io.TeeReader(r, h), object.Bytes)
		closeErr := r.Close()
		if writeErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != object.SHA {
			return result, errors.Join(ErrPublication, writeErr, closeErr)
		}
		if e = verifyPublishedObject(ctx, tree, object); e != nil {
			return result, errors.Join(ErrPublication, e)
		}
	}
	// Re-open configured locations and re-check data/keys before rename.
	cfg, e = g.recheckBindingsLocked(ctx, confirmed, time.Now().UTC(), PostgresTargetInspector{}, false)
	if e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	if e = validatePublicationDatabase(ctx, cfg, g.verified, stage, proof.ring); e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	latest, _, e := (Control{Files: g.locations.files[3]}).Load(ctx, op.ID)
	if e != nil || latest.CancelRequested || latest.Phase != "validating" || !matchingRecordedOperation(latest, a) {
		return result, ErrPublication
	}
	next = result.Receipt
	next.State = "publishing"
	if e = updatePublication(ctx, store, &result.Receipt, next); e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	if e = tree.Commit(ctx); e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	for _, object := range objects {
		if e = verifyPublishedObject(ctx, tree, object); e != nil {
			return result, errors.Join(ErrPublication, e)
		}
	}
	next = result.Receipt
	next.State = "verified"
	if e = updatePublication(ctx, store, &result.Receipt, next); e != nil {
		return result, errors.Join(ErrPublication, e)
	}
	g.publicationConfirmed = true
	return result, nil
}

func updatePublication(ctx context.Context, store publicationStore, current *publicationReceipt, next publicationReceipt) error {
	stored, token, e := store.Load(ctx, current.OperationID)
	if e != nil {
		return e
	}
	if stored != *current {
		return storage.ErrConflict
	}
	if e = store.Update(ctx, current.OperationID, token, next); e != nil {
		return e
	}
	*current = next
	return nil
}
