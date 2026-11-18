package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
)

// ControlledRestoreParams contains the explicit operator authorization inputs
// required for controlled restore execution. Nothing is inferred from inspect/plan.
type ControlledRestoreParams struct {
	PlanDigest     string
	ConfirmTarget  string
	OwnerID        string
	RequestKey     string
	ConfigKey      string
	PlanKey        string
	HeaderKeysPath string
	PGRestorePath  string
	HandoverKey    string
	Timeout        time.Duration
}

// Validate checks shape and bounds of every explicit authorization input.
func (p ControlledRestoreParams) Validate() error {
	if !validSHA(p.PlanDigest) || !canonicalUUID(p.ConfirmTarget) || !canonicalUUID(p.OwnerID) || p.OwnerID == uuid.Nil.String() ||
		!safeKey(p.RequestKey) || !safeKey(p.ConfigKey) || !safeKey(p.PlanKey) ||
		!safeAbsolute(p.HeaderKeysPath) || !safeAbsolute(p.PGRestorePath) ||
		p.Timeout < time.Second || p.Timeout > 30*time.Minute {
		return ErrPlan
	}
	if p.RequestKey == p.ConfigKey || p.RequestKey == p.PlanKey || p.ConfigKey == p.PlanKey {
		return ErrPlan
	}
	if p.HandoverKey != "" {
		if !safeKey(p.HandoverKey) || p.HandoverKey == p.RequestKey || p.HandoverKey == p.ConfigKey || p.HandoverKey == p.PlanKey {
			return ErrPlan
		}
	}
	return nil
}

func canonicalUUID(s string) bool {
	u, e := uuid.Parse(s)
	return e == nil && u.String() == s
}

// ControlledRestoreResult is the sanitized status summary emitted by `backup restore`.
// It never includes DSNs, file paths, secrets, raw error strings, or cutover authority.
type ControlledRestoreResult struct {
	Format               string `json:"restore_format"`
	Version              int    `json:"restore_version"`
	Scope                string `json:"scope"`
	OperationID          string `json:"operation_id,omitempty"`
	BackupID             string `json:"backup_id,omitempty"`
	PlanDigest           string `json:"plan_digest,omitempty"`
	TargetID             string `json:"target_instance_id,omitempty"`
	OwnerID              string `json:"owner_id,omitempty"`
	Phase                string `json:"operation_phase"`
	Outcome              string `json:"outcome"`
	StatusCode           string `json:"status_code"`
	DatabaseOutcome      string `json:"database_outcome"`
	HeadersChecked       bool   `json:"headers_checked"`
	PublicationState     string `json:"publication_state"`
	HandoverReviewStatus string `json:"handover_review_status"`
	RegistrationState    string `json:"registration_state"`
	ArtifactsRetained    bool   `json:"artifacts_retained"`
	RetrySafe            bool   `json:"retry_safe"`
	ExecutionAuthorized  bool   `json:"execution_authorized"`
	ReadyForCutover      bool   `json:"ready_for_cutover"`
	DeploymentAuthorized bool   `json:"deployment_authorized"`
}

func newControlledRestoreResult(p ControlledRestoreParams) ControlledRestoreResult {
	return ControlledRestoreResult{
		Format:               "ylreader-controlled-restore",
		Version:              1,
		Scope:                "isolated-controlled-restore",
		PlanDigest:           p.PlanDigest,
		TargetID:             p.ConfirmTarget,
		OwnerID:              p.OwnerID,
		Phase:                "rejected",
		Outcome:              "rejected",
		StatusCode:           "RESTORE_REJECTED",
		DatabaseOutcome:      "not_started",
		PublicationState:     "not_started",
		HandoverReviewStatus: "not_requested",
		RegistrationState:    "not_requested",
	}
}

// SanitizeControlledRestoreResult enforces the non-escalation invariants on a
// caller-visible controlled restore summary before emitting CLI output.
func SanitizeControlledRestoreResult(res ControlledRestoreResult, err error) ControlledRestoreResult {
	res.Format = "ylreader-controlled-restore"
	res.Version = 1
	res.Scope = "isolated-controlled-restore"
	res.RetrySafe = false
	res.ExecutionAuthorized = false
	res.ReadyForCutover = false
	res.DeploymentAuthorized = false
	if err == nil {
		return res
	}
	if res.StatusCode == "" || res.StatusCode == "RESTORE_VALIDATING_PUBLISHED" || res.StatusCode == "RESTORE_VALIDATING_REGISTERED" {
		switch {
		case errors.Is(err, ErrRestoreCommitUnknown) || errors.Is(err, ErrOutcomeRecord):
			res.Phase = "unknown"
			res.Outcome = "commit_unknown"
			res.StatusCode = "RESTORE_COMMIT_UNKNOWN"
			res.DatabaseOutcome = "unknown"
			res.ArtifactsRetained = true
		case errors.Is(err, ErrPublication):
			res.Phase = "validating"
			res.Outcome = "publication_failed"
			res.StatusCode = "RESTORE_PUBLICATION_FAILED"
			if res.PublicationState == "" || res.PublicationState == "not_started" {
				res.PublicationState = "retained_unconfirmed"
			}
			res.ArtifactsRetained = true
		case errors.Is(err, errRegistrationUnknown):
			res.Phase = "validating"
			res.Outcome = "registration_unknown"
			res.StatusCode = "RESTORE_REGISTRATION_UNKNOWN"
			res.RegistrationState = "unknown_retained"
			res.ArtifactsRetained = true
		case errors.Is(err, errHandoverUnconfirmed):
			res.Phase = "validating"
			res.Outcome = "handover_unconfirmed"
			res.StatusCode = "RESTORE_HANDOVER_UNCONFIRMED"
			res.HandoverReviewStatus = "unconfirmed"
			res.ArtifactsRetained = true
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			res.Outcome = "canceled"
			res.StatusCode = "RESTORE_CANCELED"
		default:
			res.StatusCode = "RESTORE_REJECTED"
			if res.Outcome == "" {
				res.Outcome = "rejected"
			}
		}
	}
	return res
}

type controlledHandoverSpec struct {
	Format            string               `json:"handover_format"`
	Version           int                  `json:"handover_version"`
	Mode              string               `json:"mode"`
	SourceDatabase    databaseIdentityPin  `json:"source_database"`
	TargetDatabase    databaseIdentityPin  `json:"target_database"`
	ExpectedRuntime   runtimeConfigBinding `json:"expected_runtime"`
	ActualRuntime     runtimeConfigBinding `json:"actual_runtime"`
	SourceJWTFile     string               `json:"source_jwt_file"`
	TargetJWTFile     string               `json:"target_jwt_file"`
	SourceRedisURL    string               `json:"source_redis_url"`
	TargetRedisURL    string               `json:"target_redis_url"`
	SourceRedis       redisIdentityPin     `json:"source_redis"`
	TargetRedis       redisIdentityPin     `json:"target_redis"`
	SourceWitnessFile string               `json:"source_witness_file"`
	TargetWitnessFile string               `json:"target_witness_file"`
}

func (s controlledHandoverSpec) validate(confirmTarget string) error {
	if s.Format != "ylreader-handover-execution-spec" || s.Version != 1 || s.Mode != "partial_review" ||
		!databasePinsSeparate(s.SourceDatabase, s.TargetDatabase) ||
		!s.ExpectedRuntime.valid() || s.ExpectedRuntime != s.ActualRuntime || s.ActualRuntime.TargetID != confirmTarget ||
		!s.SourceRedis.valid() || !s.TargetRedis.valid() || s.SourceRedis.RunID == s.TargetRedis.RunID {
		return errHandoverReview
	}
	if _, e := isolatedRedisClient(s.SourceRedisURL); e != nil {
		return errRedisIdentity
	}
	if _, e := isolatedRedisClient(s.TargetRedisURL); e != nil {
		return errRedisIdentity
	}
	for _, p := range []string{s.SourceJWTFile, s.TargetJWTFile, s.SourceWitnessFile, s.TargetWitnessFile} {
		if !safeAbsolute(p) {
			return errHandoverReview
		}
	}
	return nil
}

type loadedControlledHandover struct {
	spec          controlledHandoverSpec
	sourceJWT     []byte
	targetJWT     []byte
	sourceWitness []byte
	targetWitness []byte
}

func (l *loadedControlledHandover) closeSecrets() {
	if l == nil {
		return
	}
	clear(l.sourceJWT)
	clear(l.targetJWT)
	clear(l.sourceWitness)
	clear(l.targetWitness)
}

func readBoundedInputKey(ctx context.Context, r RestoreRecordReader, key string, limit int64) ([]byte, error) {
	if !safeKey(key) {
		return nil, ErrPlan
	}
	f, e := r.Get(ctx, key)
	if e != nil {
		return nil, e
	}
	data, e := io.ReadAll(io.LimitReader(f, limit+1))
	ce := f.Close()
	if e != nil || ce != nil || int64(len(data)) > limit || ctx.Err() != nil {
		return nil, ErrPackageSchema
	}
	return data, nil
}

func loadControlledHandover(ctx context.Context, r RestoreRecordReader, key, confirmTarget string) (*loadedControlledHandover, error) {
	return loadControlledHandoverWith(ctx, r, key, confirmTarget, storage.ReadBackupSecret)
}

func loadControlledHandoverWith(ctx context.Context, r RestoreRecordReader, key, confirmTarget string, readFn func(context.Context, string, int64) ([]byte, error)) (*loadedControlledHandover, error) {
	data, e := readBoundedInputKey(ctx, r, key, 64<<10)
	if e != nil {
		return nil, e
	}
	var spec controlledHandoverSpec
	if exactJSON(data, &spec) != nil || spec.validate(confirmTarget) != nil {
		return nil, errHandoverReview
	}
	out := &loadedControlledHandover{spec: spec}
	cleanup := true
	defer func() {
		if cleanup {
			out.closeSecrets()
		}
	}()
	readSecret := func(path string, max int64, trim bool) ([]byte, error) {
		b, e := readFn(ctx, path, max)
		if e != nil {
			return nil, errHandoverReview
		}
		if trim {
			b = bytesTrimSpaceCopy(b)
		}
		if len(b) == 0 {
			clear(b)
			return nil, errHandoverReview
		}
		return b, nil
	}
	if out.sourceJWT, e = readSecret(spec.SourceJWTFile, 8192, true); e != nil {
		return nil, e
	}
	if out.targetJWT, e = readSecret(spec.TargetJWTFile, 8192, true); e != nil {
		return nil, e
	}
	if out.sourceWitness, e = readSecret(spec.SourceWitnessFile, 256, false); e != nil {
		return nil, e
	}
	if out.targetWitness, e = readSecret(spec.TargetWitnessFile, 256, false); e != nil {
		return nil, e
	}
	cleanup = false
	return out, nil
}

func bytesTrimSpaceCopy(b []byte) []byte {
	trimmed := strings.TrimSpace(string(b))
	clear(b)
	return []byte(trimmed)
}

// RunControlledRestoreFromInput validates all explicit authorization parameters,
// re-authenticates the backup package against the frozen plan, executes under
// restoreGuard (empty target -> stage -> DB+header tx -> publish -> optional
// partial handover collection), and returns sanitized state on every outcome without
// retrying writes or deleting retained artifacts on uncertainty.
func RunControlledRestoreFromInput(ctx context.Context, inputRoot string, params ControlledRestoreParams, inputReader RestoreRecordReader, clock func() time.Time) (res ControlledRestoreResult, err error) {
	res = newControlledRestoreResult(params)
	defer func() {
		res = SanitizeControlledRestoreResult(res, err)
	}()
	if ctx == nil || inputReader == nil || clock == nil || !safeAbsolute(inputRoot) || params.Validate() != nil {
		return res, ErrPlan
	}
	now := clock().UTC().Truncate(time.Microsecond)
	if !validStamp(now) {
		return res, ErrPlan
	}
	if e := ctx.Err(); e != nil {
		return res, e
	}

	reqBytes, e := readBoundedInputKey(ctx, inputReader, params.RequestKey, 16<<10)
	if e != nil {
		return res, e
	}
	request, e := LoadInspectionRequest(strings.NewReader(string(reqBytes)))
	if e != nil {
		return res, e
	}
	res.BackupID = request.BackupID

	cfgBytes, e := readBoundedInputKey(ctx, inputReader, params.ConfigKey, 1<<20)
	if e != nil {
		return res, e
	}
	cfg, e := LoadConfig(strings.NewReader(string(cfgBytes)))
	if e != nil {
		return res, e
	}
	if !CheckInspectionIdentity(inputRoot, request, cfg) || request.Limits != cfg.Limits {
		return res, ErrPlan
	}

	planBytes, e := readBoundedInputKey(ctx, inputReader, params.PlanKey, 1<<20)
	if e != nil {
		return res, e
	}
	plan, e := LoadPlan(strings.NewReader(string(planBytes)))
	if e != nil {
		return res, e
	}
	res.OperationID = plan.OperationID
	if plan.Digest != params.PlanDigest || plan.BackupID != request.BackupID ||
		plan.SourceID != cfg.Source.InstanceID || plan.TargetID != params.ConfirmTarget ||
		cfg.Target.InstanceID != params.ConfirmTarget || plan.Limits != cfg.Limits {
		return res, ErrPlan
	}

	ring, e := LoadRestoreHeaderKeyring(ctx, params.HeaderKeysPath)
	if e != nil {
		return res, e
	}
	if _, e = newHeaderVerifier(request.HeaderKIDs, ring); e != nil {
		return res, e
	}

	var handover *loadedControlledHandover
	if params.HandoverKey != "" {
		handover, e = loadControlledHandover(ctx, inputReader, params.HandoverKey, params.ConfirmTarget)
		if e != nil {
			return res, e
		}
		defer handover.closeSecrets()
	}

	scratch, e := storage.OpenBackupFiles(cfg.Temporary.Root)
	if e != nil {
		return res, e
	}
	defer scratch.Close()

	idBytes, e := readBoundedInputKey(ctx, inputReader, request.IdentityKey, 4096)
	defer clear(idBytes)
	if e != nil {
		return res, ErrPackageIntegrity
	}
	identity, e := age.ParseX25519Identity(strings.TrimSpace(string(idBytes)))
	if e != nil {
		return res, ErrPackageIntegrity
	}

	pkgStream, e := inputReader.Get(ctx, request.PackageKey)
	if e != nil {
		return res, ErrPackageIntegrity
	}
	pkg, e := VerifyPackage(ctx, pkgStream, identity, VerifyOptions{
		Descriptor: PackageDescriptor{BackupID: request.BackupID, Bytes: request.Bytes, SHA256: request.SHA},
		Limits:     request.Limits,
		HeaderKIDs: request.HeaderKIDs,
	}, scratch)
	closeErr := pkgStream.Close()
	if e != nil {
		return res, e
	}
	defer pkg.Close()
	if closeErr != nil {
		return res, ErrPackageIntegrity
	}

	inspector := PostgresTargetInspector{}
	g, e := acquireRestoreGuard(ctx, plan, cfg, pkg, params.ConfirmTarget, now, inspector)
	if e != nil {
		return res, e
	}
	defer g.Close()

	stage, existingOp, e := g.StageRecorded(ctx, pkg, params.OwnerID, params.ConfirmTarget, now, inspector)
	if e != nil {
		if existingOp.Phase != "" {
			res.Phase = existingOp.Phase
		}
		res.ArtifactsRetained = true
		return res, e
	}
	if stage == nil {
		res.Phase = existingOp.Phase
		res.Outcome = "rejected"
		res.StatusCode = "RESTORE_ALREADY_RECORDED"
		res.ArtifactsRetained = true
		return res, ErrAttempt
	}
	defer stage.Close()
	res.Phase = existingOp.Phase
	res.ArtifactsRetained = true

	tool := PostgresArchiveTool{Executable: params.PGRestorePath}
	dbRes, e := g.RunDatabaseWithHeaders(ctx, stage, params.OwnerID, params.ConfirmTarget, now, tool, cfg.Limits.Plaintext, ring)
	if dbRes.Operation.Phase != "" {
		res.Phase = dbRes.Operation.Phase
	}
	if dbRes.Attempt.DatabaseOutcome != "" {
		res.DatabaseOutcome = dbRes.Attempt.DatabaseOutcome
	}
	res.HeadersChecked = dbRes.HeadersChecked
	if e != nil {
		if errors.Is(e, ErrRestoreCommitUnknown) || errors.Is(e, ErrOutcomeRecord) || dbRes.Attempt.DatabaseOutcome == "unknown" {
			res.Phase = "unknown"
			res.Outcome = "commit_unknown"
			res.StatusCode = "RESTORE_COMMIT_UNKNOWN"
			res.DatabaseOutcome = "unknown"
		} else if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			res.Outcome = "canceled"
			res.StatusCode = "RESTORE_CANCELED"
		} else {
			res.Outcome = "rejected"
			res.StatusCode = "RESTORE_REJECTED"
		}
		return res, e
	}

	pubRes, e := g.PublishObjects(ctx, stage, params.OwnerID, params.ConfirmTarget, now)
	if pubRes.Receipt.State != "" {
		res.PublicationState = pubRes.Receipt.State
	}
	if e != nil {
		res.Outcome = "publication_failed"
		res.StatusCode = "RESTORE_PUBLICATION_FAILED"
		if res.PublicationState == "" || res.PublicationState == "not_started" {
			res.PublicationState = "retained_unconfirmed"
		}
		return res, e
	}
	res.PublicationState = "verified"

	if handover == nil {
		res.Phase = "validating"
		res.Outcome = "validated_published"
		res.StatusCode = "RESTORE_VALIDATING_PUBLISHED"
		return res, nil
	}
	return res, runControlledHandover(ctx, g, stage, plan, cfg, ring, params.OwnerID, handover, clock, &res)
}

func runControlledHandover(
	ctx context.Context,
	g *restoreGuard,
	stage *stagedObjects,
	plan Plan,
	cfg Config,
	ring *headercrypto.Keyring,
	ownerID string,
	handover *loadedControlledHandover,
	clock func() time.Time,
	res *ControlledRestoreResult,
) error {
	dsnBytes, e := storage.ReadBackupSecret(ctx, cfg.Target.DSNFile, 16384)
	defer clear(dsnBytes)
	if e != nil {
		res.HandoverReviewStatus = "unconfirmed"
		res.StatusCode = "RESTORE_HANDOVER_UNCONFIRMED"
		res.Outcome = "handover_unconfirmed"
		return e
	}
	dsnRaw := strings.TrimSpace(string(dsnBytes))
	if _, e := ParseTarget(dsnRaw); e != nil {
		res.HandoverReviewStatus = "unconfirmed"
		res.StatusCode = "RESTORE_HANDOVER_UNCONFIRMED"
		res.Outcome = "handover_unconfirmed"
		return e
	}
	inputs := handoverProbeInputs{
		DatabaseURL:     dsnRaw,
		SourceDatabase:  handover.spec.SourceDatabase,
		TargetDatabase:  handover.spec.TargetDatabase,
		ExpectedRuntime: handover.spec.ExpectedRuntime,
		ActualRuntime:   handover.spec.ActualRuntime,
		SourceJWT:       handover.sourceJWT,
		TargetJWT:       handover.targetJWT,
		Ring:            ring,
		RequiredKIDs:    append([]string(nil), g.verified.manifest.HeaderKIDs...),
		SourceRedisURL:  handover.spec.SourceRedisURL,
		TargetRedisURL:  handover.spec.TargetRedisURL,
		SourceRedis:     handover.spec.SourceRedis,
		TargetRedis:     handover.spec.TargetRedis,
		SourceWitness:   handover.sourceWitness,
		TargetWitness:   handover.targetWitness,
		Guard:           g,
		Stage:           stage,
		OwnerID:         ownerID,
	}
	probes, e := inputs.probes(plan)
	if e != nil {
		res.HandoverReviewStatus = "unconfirmed"
		res.StatusCode = "RESTORE_HANDOVER_UNCONFIRMED"
		res.Outcome = "handover_unconfirmed"
		return e
	}
	report, e := collectHandoverReview(ctx, g.locations.files[3], plan, probes, clock)
	if e != nil {
		res.HandoverReviewStatus = "unconfirmed"
		res.StatusCode = "RESTORE_HANDOVER_UNCONFIRMED"
		res.Outcome = "handover_unconfirmed"
		return e
	}
	if e := persistCollectedHandoverReview(ctx, g.locations.files[3], plan, report, clock); e != nil {
		res.HandoverReviewStatus = "unconfirmed"
		res.StatusCode = "RESTORE_HANDOVER_UNCONFIRMED"
		res.Outcome = "handover_unconfirmed"
		return e
	}
	res.HandoverReviewStatus = "partial_saved"
	res.RegistrationState = "absent"
	res.Outcome = "partial_review_saved"
	res.StatusCode = "RESTORE_HANDOVER_PARTIAL_UNREGISTERED"
	return errRegistration
}
