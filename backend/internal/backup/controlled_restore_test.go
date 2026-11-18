package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/pkg/storage"
)

func TestControlledRestoreParamsAndSanitization(t *testing.T) {
	valid := ControlledRestoreParams{
		PlanDigest:     strings.Repeat("a", 64),
		ConfirmTarget:  "20000000-0000-4000-8000-000000000001",
		OwnerID:        "50000000-0000-4000-8000-000000000001",
		RequestKey:     "request.json",
		ConfigKey:      "config.json",
		PlanKey:        "plan.json",
		HeaderKeysPath: filepath.Join(t.TempDir(), "header-keys.json"),
		PGRestorePath:  filepath.Join(t.TempDir(), "pg_restore"),
		Timeout:        2 * time.Minute,
	}
	if valid.Validate() != nil {
		t.Fatal("valid params rejected")
	}
	badCases := []func(*ControlledRestoreParams){
		func(p *ControlledRestoreParams) { p.PlanDigest = "short" },
		func(p *ControlledRestoreParams) { p.ConfirmTarget = "20000000-0000-4000-8000-0000000000AF" },
		func(p *ControlledRestoreParams) { p.OwnerID = uuid.Nil.String() },
		func(p *ControlledRestoreParams) { p.RequestKey = "../secret" },
		func(p *ControlledRestoreParams) { p.PlanKey = p.ConfigKey },
		func(p *ControlledRestoreParams) { p.HandoverKey = p.PlanKey },
		func(p *ControlledRestoreParams) { p.HeaderKeysPath = "relative.json" },
		func(p *ControlledRestoreParams) { p.PGRestorePath = "pg_restore" },
		func(p *ControlledRestoreParams) { p.Timeout = 500 * time.Millisecond },
		func(p *ControlledRestoreParams) { p.Timeout = 31 * time.Minute },
	}
	for i, mutate := range badCases {
		bad := valid
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("bad param case %d accepted", i)
		}
	}

	base := ControlledRestoreResult{StatusCode: "RESTORE_VALIDATING_PUBLISHED", ExecutionAuthorized: true, ReadyForCutover: true, DeploymentAuthorized: true, RetrySafe: true}
	for _, tc := range []struct {
		err  error
		code string
	}{
		{ErrRestoreCommitUnknown, "RESTORE_COMMIT_UNKNOWN"},
		{ErrOutcomeRecord, "RESTORE_COMMIT_UNKNOWN"},
		{ErrPublication, "RESTORE_PUBLICATION_FAILED"},
		{errRegistrationUnknown, "RESTORE_REGISTRATION_UNKNOWN"},
		{errHandoverUnconfirmed, "RESTORE_HANDOVER_UNCONFIRMED"},
		{context.Canceled, "RESTORE_CANCELED"},
		{context.DeadlineExceeded, "RESTORE_CANCELED"},
		{ErrPlan, "RESTORE_REJECTED"},
	} {
		out := SanitizeControlledRestoreResult(base, tc.err)
		if out.StatusCode != tc.code || out.RetrySafe || out.ExecutionAuthorized || out.ReadyForCutover || out.DeploymentAuthorized {
			t.Fatalf("sanitization failed for %v: %+v", tc.err, out)
		}
	}
}

func TestControlledHandoverSpecAndPreflightRejections(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("chmod")
	}
	writeSecret := func(name string, content []byte) string {
		p := filepath.Join(dir, name)
		if os.WriteFile(p, content, 0600) != nil {
			t.Fatal("write secret")
		}
		return p
	}
	hdrPath := writeSecret("hdr.json", []byte(`{}`))
	srcJWT := writeSecret("src.jwt", []byte("source-jwt-secret-material-32bytes!"))
	dstJWT := writeSecret("dst.jwt", []byte("target-jwt-secret-material-32bytes!"))
	srcW := writeSecret("src.witness", make([]byte, 32))
	dstW := writeSecret("dst.witness", make([]byte, 32))

	targetID := "20000000-0000-4000-8000-000000000001"
	spec := controlledHandoverSpec{
		Format:            "ylreader-handover-execution-spec",
		Version:           1,
		Mode:              "partial_review",
		SourceDatabase:    databaseIdentityPin{"123456789", 100, 10},
		TargetDatabase:    databaseIdentityPin{"123456789", 200, 10},
		ExpectedRuntime:   runtimeConfigBinding{targetID, "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002", "10000000-0000-4000-8000-000000000003", "primary"},
		ActualRuntime:     runtimeConfigBinding{targetID, "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002", "10000000-0000-4000-8000-000000000003", "primary"},
		SourceJWTFile:     srcJWT,
		TargetJWTFile:     dstJWT,
		SourceRedisURL:    "redis://127.0.0.1:16379/0",
		TargetRedisURL:    "redis://127.0.0.1:16380/0",
		SourceRedis:       redisIdentityPin{strings.Repeat("a", 40), 0},
		TargetRedis:       redisIdentityPin{strings.Repeat("b", 40), 0},
		SourceWitnessFile: srcW,
		TargetWitnessFile: dstW,
	}
	encoded, _ := json.Marshal(spec)
	f := &memoryFiles{data: map[string][]byte{"handover.json": encoded}}
	readFn := storage.ReadBackupSecret
	if runtime.GOOS != "linux" {
		readFn = func(_ context.Context, path string, _ int64) ([]byte, error) { return os.ReadFile(path) }
	}
	loaded, e := loadControlledHandoverWith(ctx, f, "handover.json", targetID, readFn)
	if e != nil {
		t.Fatalf("expected valid handover spec load, got %v", e)
	}
	loaded.closeSecrets()
	for _, buf := range [][]byte{loaded.sourceJWT, loaded.targetJWT, loaded.sourceWitness, loaded.targetWitness} {
		for _, b := range buf {
			if b != 0 {
				t.Fatal("secret buffer not cleared")
			}
		}
	}

	// Reject bad modes/insecure secret permissions on Linux OpenBackupFiles
	if runtime.GOOS == "linux" && os.Chmod(srcJWT, 0644) == nil {
		if _, e := loadControlledHandover(ctx, f, "handover.json", targetID); e == nil {
			t.Fatal("world-readable secret file accepted")
		}
		_ = os.Chmod(srcJWT, 0600)
	}

	// RunControlledRestoreFromInput rejects mismatched plan digest before touching storage.
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c := Config{Format: "ylreader-backup-localfs-experiment", Version: 1, Source: Endpoint{InstanceID: "10000000-0000-4000-8000-000000000001", DSNFile: writeSecret("s.dsn", []byte("x")), Storage: LocalStorage{"localfs", filepath.Join(dir, "s"), "ns"}}, Target: Endpoint{InstanceID: targetID, DSNFile: writeSecret("t.dsn", []byte("x")), Storage: LocalStorage{"localfs", filepath.Join(dir, "t"), "ns"}}, Backup: LocalStorage{"localfs", filepath.Join(dir, "b"), "ns"}, Control: LocalStorage{"localfs", filepath.Join(dir, "c"), "ns"}, Limits: Limits{1 << 20, 1 << 20, 1 << 20, 10, 1 << 20}}
	c.Temporary.Root = filepath.Join(dir, "tmp")
	c.QuiescenceFile = filepath.Join(dir, "q")
	idPath := filepath.Join(dir, "identity.txt")
	c.Encryption.Identity = idPath
	r := InspectionRequest{Format: "ylreader-package-inspection-request", Version: 1, PackageKey: "package.age", IdentityKey: "identity.txt", BackupID: "30000000-0000-4000-8000-000000000001", Bytes: 128, SHA: strings.Repeat("b", 64), HeaderKIDs: []string{"primary"}, Limits: c.Limits}
	p := Plan{Format: "ylreader-restore-plan", Version: 1, OperationID: "40000000-0000-4000-8000-000000000001", BackupID: r.BackupID, PackageSHA: r.SHA, ManifestSHA: strings.Repeat("c", 64), SchemaVersion: 1, MigrationChecksum: strings.Repeat("d", 64), SourceID: c.Source.InstanceID, TargetID: targetID, Database: DatabaseTarget{"127.0.0.1", 5432, "yreader_a1_iso_test"}, TargetFingerprint: strings.Repeat("1", 64), ControlFingerprint: strings.Repeat("2", 64), TempFingerprint: strings.Repeat("3", 64), Limits: c.Limits, Created: now.Format(time.RFC3339Nano), Expires: now.Add(15 * time.Minute).Format(time.RFC3339Nano), Steps: steps()}
	p.Digest, _ = p.digest()
	reqBytes, _ := json.Marshal(r)
	cfgBytes, _ := json.Marshal(c)
	planBytes, _ := json.Marshal(p)
	in := &memoryFiles{data: map[string][]byte{"request.json": reqBytes, "config.json": cfgBytes, "plan.json": planBytes}}
	reader, _ := storage.OpenBackupFiles(dir)
	if reader != nil {
		defer reader.Close()
	}
	res, err := RunControlledRestoreFromInput(ctx, dir, ControlledRestoreParams{
		PlanDigest:     strings.Repeat("f", 64),
		ConfirmTarget:  targetID,
		OwnerID:        "50000000-0000-4000-8000-000000000001",
		RequestKey:     "request.json",
		ConfigKey:      "config.json",
		PlanKey:        "plan.json",
		HeaderKeysPath: hdrPath,
		PGRestorePath:  hdrPath,
		Timeout:        time.Minute,
	}, in, func() time.Time { return now })
	if !errors.Is(err, ErrPlan) || res.StatusCode != "RESTORE_REJECTED" || res.RetrySafe || res.ExecutionAuthorized || res.ReadyForCutover || res.DeploymentAuthorized {
		t.Fatalf("mismatched digest should fail with sanitized rejection: err=%v res=%+v", err, res)
	}
}

type diskRetainedTemp struct {
	root     string
	ref      string
	retained bool
	removed  bool
}

func newDiskRetainedTemp(t *testing.T, files map[string][]byte) *diskRetainedTemp {
	t.Helper()
	base := t.TempDir()
	ref := ".scratch-testgen"
	dir := filepath.Join(base, ref)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &diskRetainedTemp{root: dir, ref: ref}
}

func (d *diskRetainedTemp) Reference() string { return d.ref }
func (d *diskRetainedTemp) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(d.root, key))
}
func (d *diskRetainedTemp) CreateOnly(_ context.Context, key string, r io.Reader, size int64) error {
	body, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.root, key), body, 0600)
}
func (d *diskRetainedTemp) Remove() error {
	d.removed = true
	return os.RemoveAll(d.root)
}
func (d *diskRetainedTemp) Retain() error {
	d.retained = true
	return nil
}

func TestExecuteControlledRestoreStagesDiskRetention(t *testing.T) {
	files := map[string][]byte{
		"restore-owner.json":                      []byte(`{"operation_id":"op-1"}`),
		"object-11111111111111111111111111111111": []byte("staged-object-bytes"),
	}
	assertFilesPresent := func(t *testing.T, dir string) {
		t.Helper()
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || string(got) != string(want) {
				t.Fatalf("staged file %s missing or corrupted on disk after retain: err=%v got=%q", name, err, got)
			}
		}
	}

	cases := []struct {
		name       string
		dbRes      recordedDatabaseResult
		dbErr      error
		pubRes     publicationResult
		pubErr     error
		handover   func(context.Context, *stagedObjects, *ControlledRestoreResult) error
		wantRetain bool
		wantCode   string
	}{
		{
			name:       "commit_unknown_retains_scratch_on_disk",
			dbRes:      recordedDatabaseResult{Operation: Operation{Phase: "validating"}, Attempt: restoreAttempt{DatabaseOutcome: "unknown"}},
			dbErr:      ErrRestoreCommitUnknown,
			wantRetain: true,
			wantCode:   "RESTORE_COMMIT_UNKNOWN",
		},
		{
			name:       "outcome_record_failure_retains_scratch_on_disk",
			dbRes:      recordedDatabaseResult{Operation: Operation{Phase: "validating"}, Attempt: restoreAttempt{DatabaseOutcome: "unknown"}},
			dbErr:      ErrOutcomeRecord,
			wantRetain: true,
			wantCode:   "RESTORE_COMMIT_UNKNOWN",
		},
		{
			name:       "context_canceled_retains_scratch_on_disk",
			dbRes:      recordedDatabaseResult{Operation: Operation{Phase: "canceled"}},
			dbErr:      context.Canceled,
			wantRetain: true,
			wantCode:   "RESTORE_CANCELED",
		},
		{
			name:       "publication_failure_retains_scratch_on_disk",
			dbRes:      recordedDatabaseResult{Operation: Operation{Phase: "validating"}, Attempt: restoreAttempt{DatabaseOutcome: "committed"}, HeadersChecked: true},
			pubErr:     ErrPublication,
			wantRetain: true,
			wantCode:   "RESTORE_PUBLICATION_FAILED",
		},
		{
			name:       "validating_published_retains_scratch_on_disk",
			dbRes:      recordedDatabaseResult{Operation: Operation{Phase: "validating"}, Attempt: restoreAttempt{DatabaseOutcome: "committed"}, HeadersChecked: true},
			pubRes:     publicationResult{Receipt: publicationReceipt{State: "verified"}},
			wantRetain: true,
			wantCode:   "RESTORE_VALIDATING_PUBLISHED",
		},
		{
			name:   "handover_unconfirmed_retains_scratch_on_disk",
			dbRes:  recordedDatabaseResult{Operation: Operation{Phase: "validating"}, Attempt: restoreAttempt{DatabaseOutcome: "committed"}, HeadersChecked: true},
			pubRes: publicationResult{Receipt: publicationReceipt{State: "verified"}},
			handover: func(_ context.Context, stage *stagedObjects, res *ControlledRestoreResult) error {
				if stage.Reference() != ".scratch-testgen" {
					t.Fatalf("expected active stage reference during handover, got %q", stage.Reference())
				}
				res.Phase = "validating"
				res.Outcome = "handover_partial_unregistered"
				res.StatusCode = "RESTORE_HANDOVER_PARTIAL_UNREGISTERED"
				return errHandoverUnconfirmed
			},
			wantRetain: true,
			wantCode:   "RESTORE_HANDOVER_PARTIAL_UNREGISTERED",
		},
		{
			name:       "confirmed_rollback_removes_scratch_from_disk",
			dbRes:      recordedDatabaseResult{Operation: Operation{Phase: "failed"}, Attempt: restoreAttempt{DatabaseOutcome: "rolled_back"}},
			dbErr:      ErrRestoreDatabase,
			wantRetain: false,
			wantCode:   "RESTORE_REJECTED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			temp := newDiskRetainedTemp(t, files)
			stage := &stagedObjects{temp: temp}
			res := ControlledRestoreResult{ArtifactsRetained: true}
			ops := controlledRestoreOps{
				runDB: func(context.Context, *stagedObjects) (recordedDatabaseResult, error) {
					return tc.dbRes, tc.dbErr
				},
				publish: func(context.Context, *stagedObjects) (publicationResult, error) {
					return tc.pubRes, tc.pubErr
				},
				handover: tc.handover,
			}
			_ = executeControlledRestoreStages(context.Background(), stage, ops, &res)
			if res.StatusCode != tc.wantCode {
				t.Fatalf("unexpected status code: got %q want %q", res.StatusCode, tc.wantCode)
			}
			if tc.wantRetain {
				if !temp.retained || temp.removed {
					t.Fatalf("expected Retain() without Remove(): retained=%v removed=%v", temp.retained, temp.removed)
				}
				assertFilesPresent(t, temp.root)
			} else {
				if !temp.removed || temp.retained {
					t.Fatalf("expected Remove() without Retain(): retained=%v removed=%v", temp.retained, temp.removed)
				}
				if _, err := os.Stat(temp.root); !os.IsNotExist(err) {
					t.Fatalf("expected %s removed on confirmed rollback, stat err=%v", temp.root, err)
				}
			}
		})
	}
}
