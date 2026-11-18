package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"

	"yreader/backend/internal/pkg/storage"
)

var ErrRestoreObjects = errors.New("backup: object staging or verification failed")
var ErrObjectReferences = errors.New("backup: restored object or entity references invalid")

type locatedFiles interface {
	Location(context.Context) (storage.DirectoryLocation, error)
}

// Objects stay in an owned private generation. Original keys are a lookup index,
// never host paths. This is NOT publication into the application's namespace.
type stagedObjects struct {
	mu      sync.Mutex
	temp    storage.BackupTemp
	binding PackageBinding
	objects []ManifestObject // sorted by key
	readers int
}

func stageRestoreObjects(ctx context.Context, pkg *VerifiedPackage, target storage.BackupFiles, operationID string) (stage *stagedObjects, err error) {
	return stageRegisteredObjects(ctx, pkg, target, operationID, nil)
}

type retainedGeneration struct{ Reference string }

func (e *retainedGeneration) Error() string {
	return "backup: object generation retained pending reconciliation"
}

func stageRegisteredObjects(ctx context.Context, pkg *VerifiedPackage, target storage.BackupFiles, operationID string, register func(context.Context, string) error) (stage *stagedObjects, err error) {
	if pkg == nil || target == nil || !validUUID(operationID) {
		return nil, ErrRestoreObjects
	}
	pin, e := pkg.OpenDatabase(ctx)
	if e != nil {
		return nil, ErrRestoreObjects
	}
	defer pin.Close()
	pkg.mu.Lock()
	source, ok := pkg.temp.(locatedFiles)
	objects := append([]ManifestObject(nil), pkg.manifest.Objects...)
	binding := pkg.binding
	pkg.mu.Unlock()
	if !ok {
		return nil, storage.ErrCapability
	}
	a, e := source.Location(ctx)
	if e != nil {
		return nil, ErrRestoreObjects
	}
	b, e := target.Location(ctx)
	if e != nil || disjoint([]storage.DirectoryLocation{a, b}) != nil {
		return nil, ErrLocations
	}
	empty, e := target.Empty(ctx)
	if e != nil || !empty {
		return nil, ErrLocations
	}
	var total int64
	for _, object := range objects {
		total += object.Bytes
	}
	available, e := target.AvailableBytes(ctx)
	if e != nil || available < total+4096 {
		return nil, ErrPackageLimit
	}
	temp, e := target.NewTemp(ctx)
	if e != nil {
		return nil, ErrRestoreObjects
	}
	defer func() {
		if err != nil {
			if errors.Is(err, storage.ErrCommitUnknown) {
				if retained, ok := temp.(storage.RetainedBackupTemp); ok {
					err = errors.Join(err, retained.Retain())
				} else {
					err = errors.Join(err, storage.ErrCapability)
				}
				err = errors.Join(err, &retainedGeneration{temp.Reference()})
				return
			}
			if e := temp.Remove(); e != nil {
				err = errors.Join(err, &CleanupFailure{temp.Reference()})
			}
		}
	}()
	// The control receipt must durably identify the generation before the first
	// owner/object payload byte is written into it.
	if register != nil {
		if e = register(ctx, temp.Reference()); e != nil {
			return nil, e
		}
	}
	owner, _ := json.Marshal(struct {
		OperationID string `json:"operation_id"`
		BackupID    string `json:"backup_id"`
		PackageSHA  string `json:"package_sha256"`
	}{operationID, binding.BackupID, binding.PackageSHA})
	if e = temp.CreateOnly(ctx, "restore-owner.json", bytes.NewReader(owner), int64(len(owner))); e != nil {
		return nil, ErrRestoreObjects
	}
	for _, object := range objects {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		available, e = target.AvailableBytes(ctx)
		if e != nil || available < object.Bytes {
			return nil, ErrPackageLimit
		}
		input, e := pkg.OpenObject(ctx, object.ID)
		if e != nil {
			return nil, ErrRestoreObjects
		}
		hash := sha256.New()
		e = temp.CreateOnly(ctx, "object-"+object.ID, io.TeeReader(input, hash), object.Bytes)
		closeErr := input.Close()
		if e != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != object.SHA {
			return nil, ErrRestoreObjects
		}
		// Verify persisted bytes, not just the bytes supplied to the write call.
		if e = verifyStagedObject(ctx, temp, object); e != nil {
			return nil, e
		}
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return &stagedObjects{temp: temp, binding: binding, objects: objects}, nil
}

func verifyStagedObject(ctx context.Context, temp storage.BackupTemp, object ManifestObject) error {
	r, e := temp.Get(ctx, "object-"+object.ID)
	if e != nil {
		return ErrRestoreObjects
	}
	hash := sha256.New()
	n, e := io.Copy(hash, io.LimitReader(packageContextReader{ctx, r}, object.Bytes+1))
	closed := r.Close()
	if e != nil || closed != nil || n != object.Bytes || hex.EncodeToString(hash.Sum(nil)) != object.SHA {
		return ErrRestoreObjects
	}
	return nil
}

func (s *stagedObjects) open(ctx context.Context, flat string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.temp == nil {
		return nil, ErrRestoreObjects
	}
	r, e := s.temp.Get(ctx, flat)
	if e != nil {
		return nil, ErrRestoreObjects
	}
	s.readers++
	return &packageReader{ReadCloser: r, done: func() { s.mu.Lock(); s.readers--; s.mu.Unlock() }}, nil
}

func (s *stagedObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	object, ok := s.metadata(key)
	if !ok {
		return nil, storage.ErrNotFound
	}
	return s.open(ctx, "object-"+object.ID)
}

func (s *stagedObjects) metadata(key string) (ManifestObject, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := sort.Search(len(s.objects), func(i int) bool { return s.objects[i].Key >= key })
	if i == len(s.objects) || s.objects[i].Key != key {
		return ManifestObject{}, false
	}
	return s.objects[i], true
}

// Close removes only this operation's owned generation. Call only after a
// confirmed rollback, or an explicit later handoff/cleanup of this generation.
func (s *stagedObjects) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.temp == nil {
		return nil
	}
	if s.readers != 0 {
		return storage.ErrBusy
	}
	if e := s.temp.Remove(); e != nil {
		return &CleanupFailure{s.temp.Reference()}
	}
	s.temp = nil
	s.objects = nil
	return nil
}

func (s *stagedObjects) Reference() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.temp == nil {
		return ""
	}
	return s.temp.Reference()
}

// On success retain objects for subsequent key validation/publication. On an
// ambiguous commit/rollback retain them for operator reconciliation as well.
func finishObjectDatabasePhase(stage *stagedObjects, err error) (*stagedObjects, error) {
	if err == nil || errors.Is(err, ErrRestoreCommitUnknown) || errors.Is(err, ErrRestoreRollback) || errors.Is(err, ErrOutcomeRecord) || errors.Is(err, ErrPublication) {
		return stage, err
	}
	if e := stage.Close(); e != nil {
		return stage, errors.Join(err, e)
	}
	return nil, err
}

// Internal database+object substep; it does not advance a persistent operation
// to ready_for_cutover/completed, grant apply authorization, or start an app.
func restoreObjectDatabasePhase(ctx context.Context, rawTarget string, pkg *VerifiedPackage, tool PostgresArchiveTool, budget int64, target storage.BackupFiles, operationID string) (databaseRestoreResult, *stagedObjects, error) {
	if budget < 1 || budget > 1<<40 {
		return databaseRestoreResult{}, nil, ErrArchiveData
	}
	// Reject business targets before creating any target object generation.
	if _, e := ParseTarget(rawTarget); e != nil {
		return databaseRestoreResult{}, nil, e
	}
	stage, e := stageRestoreObjects(ctx, pkg, target, operationID)
	if e != nil {
		return databaseRestoreResult{}, nil, e
	}
	pin, e := stage.open(ctx, "restore-owner.json")
	if e != nil {
		retained, err := finishObjectDatabasePhase(stage, e)
		return databaseRestoreResult{}, retained, err
	}
	result, e := restoreDatabaseValidated(ctx, rawTarget, pkg, tool, budget, stage)
	pin.Close()
	retained, e := finishObjectDatabasePhase(stage, e)
	return result, retained, e
}
