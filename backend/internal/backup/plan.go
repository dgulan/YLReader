package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/migration"
	"yreader/backend/internal/pkg/storage"
)

var ErrPlan = errors.New("backup: invalid, expired or changed restoration plan")

type planError struct {
	stage string
}

func (e planError) Error() string {
	return ErrPlan.Error() + " (" + e.stage + ")"
}

func (planError) Unwrap() error { return ErrPlan }

func planStageError(stage string) error {
	if stage == "" {
		return ErrPlan
	}
	return planError{stage: stage}
}

func preservePlanError(err error, fallbackStage string) error {
	var pe planError
	if errors.As(err, &pe) {
		return pe
	}
	return planStageError(fallbackStage)
}

type DatabaseTarget struct {
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Database string `json:"database"`
}
type Plan struct {
	Format             string         `json:"plan_format"`
	Version            int            `json:"plan_version"`
	OperationID        string         `json:"operation_id"`
	BackupID           string         `json:"backup_id"`
	PackageSHA         string         `json:"package_sha256"`
	ManifestSHA        string         `json:"manifest_sha256"`
	SchemaVersion      int            `json:"schema_version"`
	MigrationChecksum  string         `json:"migration_checksum"`
	SourceID           string         `json:"source_instance_id"`
	TargetID           string         `json:"target_instance_id"`
	Database           DatabaseTarget `json:"target_database"`
	TargetFingerprint  string         `json:"target_storage_fingerprint"`
	ControlFingerprint string         `json:"control_fingerprint"`
	TempFingerprint    string         `json:"temporary_fingerprint"`
	Limits             Limits         `json:"limits"`
	// Preserve timestamp strings: redundant fractional zeros are significant
	// canonical JSON bytes even when they represent the same instant.
	Created string   `json:"created_at"`
	Expires string   `json:"expires_at"`
	Steps   []string `json:"steps"`
	Digest  string   `json:"plan_digest"`
}

func steps() []string {
	return []string{"verify_package", "verify_target", "stage_objects", "load_data", "validate", "revoke_sessions", "write_report"}
}
func validSHA(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func (p Plan) shape() error {
	if p.Format != "ylreader-restore-plan" || p.Version != 1 || !validUUID(p.OperationID) || !validUUID(p.BackupID) || !validUUID(p.SourceID) || !validUUID(p.TargetID) || p.SourceID == p.TargetID || p.SchemaVersion != 1 || p.MigrationChecksum != migration.Expected().Checksum {
		return ErrPlan
	}
	if !validSHA(p.PackageSHA) || !validSHA(p.ManifestSHA) || !validSHA(p.TargetFingerprint) || !validSHA(p.ControlFingerprint) || !validSHA(p.TempFingerprint) || p.TargetFingerprint == p.ControlFingerprint || p.TargetFingerprint == p.TempFingerprint || p.ControlFingerprint == p.TempFingerprint {
		return ErrPlan
	}
	created, e1 := time.Parse(time.RFC3339Nano, p.Created)
	expires, e2 := time.Parse(time.RFC3339Nano, p.Expires)
	if e1 != nil || e2 != nil || !utcText.MatchString(p.Created) || !utcText.MatchString(p.Expires) || !validStamp(created) || !validStamp(expires) || expires.Sub(created) != 15*time.Minute || p.Limits.Validate() != nil || !reflect.DeepEqual(p.Steps, steps()) {
		return ErrPlan
	}
	if p.Database.Host != "127.0.0.1" && p.Database.Host != "::1" && p.Database.Host != "localhost" || p.Database.Port == 0 || !isolatedDB.MatchString(p.Database.Database) {
		return ErrPlan
	}
	return nil
}
func (p Plan) digest() (string, error) {
	if p.shape() != nil {
		return "", ErrPlan
	}
	data, e := json.Marshal(p)
	if e != nil {
		return "", ErrPlan
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return "", ErrPlan
	}
	delete(value, "plan_digest")
	// Every plan value is ASCII after shape validation. encoding/json sorts map
	// keys recursively; the fixed schema contains no floats or HTML characters.
	data, e = json.Marshal(value)
	if e != nil {
		return "", ErrPlan
	}
	for _, b := range data {
		if b > 127 {
			return "", ErrPlan
		}
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func (p Plan) Validate() error {
	digest, e := p.digest()
	if e != nil || !validSHA(p.Digest) || digest != p.Digest {
		return ErrPlan
	}
	return nil
}
func LoadPlan(r io.Reader) (Plan, error) {
	var p Plan
	if r == nil {
		return p, ErrPlan
	}
	data, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if e != nil || exactJSON(data, &p) != nil || p.Validate() != nil {
		return Plan{}, ErrPlan
	}
	return p, nil
}

// PackageBinding contains outputs of package authentication/manifest validation.
// Merely constructing this value does not authenticate any backup bytes.
type PackageBinding struct {
	BackupID, SourceID, PackageSHA, ManifestSHA string
	SchemaVersion                               int
	MigrationChecksum                           string
}

func (b PackageBinding) validate() error {
	if !validUUID(b.BackupID) || !validUUID(b.SourceID) || !validSHA(b.PackageSHA) || !validSHA(b.ManifestSHA) || b.SchemaVersion != 1 || b.MigrationChecksum != migration.Expected().Checksum {
		return ErrPlan
	}
	return nil
}

type TargetInspector interface {
	CheckEmpty(context.Context, *pgx.ConnConfig) error
}

func readTarget(ctx context.Context, c Config) (*pgx.ConnConfig, error) {
	data, e := storage.ReadBackupSecret(ctx, c.Target.DSNFile, 16384)
	if e != nil {
		return nil, e
	}
	return ParseTarget(strings.TrimSpace(string(data)))
}

// PreparePlan performs read-only filesystem and database checks. It does not
// create missing targets, authenticate package bytes or grant apply permission.
func PreparePlan(ctx context.Context, c Config, binding PackageBinding, operationID string, now time.Time, inspector TargetInspector) (Plan, error) {
	if c.Validate() != nil || binding.validate() != nil || binding.SourceID != c.Source.InstanceID || inspector == nil || !validStamp(now.UTC().Truncate(time.Microsecond)) {
		return Plan{}, planStageError("plan_preflight")
	}
	target, e := readTarget(ctx, c)
	if e != nil {
		return Plan{}, e
	}
	locations, e := OpenLocations(ctx, c)
	if e != nil {
		return Plan{}, e
	}
	defer locations.Close()
	snapshot, e := locations.Snapshot(ctx)
	if e != nil {
		return Plan{}, e
	}
	if e = inspector.CheckEmpty(ctx, target); e != nil {
		return Plan{}, preservePlanError(e, "target_check")
	}
	if operationID == "" {
		operationID = uuid.NewString()
	}
	created := now.UTC().Truncate(time.Microsecond)
	p := Plan{Format: "ylreader-restore-plan", Version: 1, OperationID: operationID, BackupID: binding.BackupID, PackageSHA: binding.PackageSHA, ManifestSHA: binding.ManifestSHA, SchemaVersion: binding.SchemaVersion, MigrationChecksum: binding.MigrationChecksum, SourceID: binding.SourceID, TargetID: c.Target.InstanceID, Database: DatabaseTarget{target.Host, target.Port, target.Database}, TargetFingerprint: snapshot.Target, ControlFingerprint: snapshot.Control, TempFingerprint: snapshot.Temporary, Limits: c.Limits, Created: created.Format(time.RFC3339Nano), Expires: created.Add(15 * time.Minute).Format(time.RFC3339Nano), Steps: steps()}
	p.Digest, e = p.digest()
	if e != nil {
		return Plan{}, e
	}
	return p, nil
}

func CheckPlan(ctx context.Context, p Plan, c Config, binding PackageBinding, confirmed string, now time.Time, inspector TargetInspector) error {
	if e := checkPlanFields(p, c, binding, confirmed, now, inspector); e != nil {
		return e
	}
	return checkPlanLocations(ctx, p, c, inspector)
}

func checkPlanFields(p Plan, c Config, binding PackageBinding, confirmed string, now time.Time, inspector TargetInspector) error {
	created, _ := time.Parse(time.RFC3339Nano, p.Created)
	expires, _ := time.Parse(time.RFC3339Nano, p.Expires)
	if p.Validate() != nil || c.Validate() != nil || binding.validate() != nil || inspector == nil || confirmed != p.TargetID || now.Before(created) || !now.Before(expires) || p.TargetID != c.Target.InstanceID || p.SourceID != c.Source.InstanceID || p.SourceID != binding.SourceID || p.BackupID != binding.BackupID || p.PackageSHA != binding.PackageSHA || p.ManifestSHA != binding.ManifestSHA || p.SchemaVersion != binding.SchemaVersion || p.MigrationChecksum != binding.MigrationChecksum || p.Limits != c.Limits {
		return planStageError("plan_fields")
	}
	return nil
}

func checkPlanLocations(ctx context.Context, p Plan, c Config, inspector TargetInspector) error {
	target, e := readTarget(ctx, c)
	if e != nil {
		return e
	}
	if (DatabaseTarget{target.Host, target.Port, target.Database}) != p.Database {
		return planStageError("target_database_mismatch")
	}
	locations, e := OpenLocations(ctx, c)
	if e != nil {
		return e
	}
	defer locations.Close()
	snapshot, e := locations.Snapshot(ctx)
	if e != nil {
		return e
	}
	if snapshot.Target != p.TargetFingerprint || snapshot.Control != p.ControlFingerprint || snapshot.Temporary != p.TempFingerprint {
		return planStageError("location_fingerprint")
	}
	if e := inspector.CheckEmpty(ctx, target); e != nil {
		return preservePlanError(e, "target_check")
	}
	return nil
}
