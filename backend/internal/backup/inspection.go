package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// InspectionRequest is an internal CLI input, independent of the frozen archive.
type InspectionRequest struct {
	Format      string   `json:"request_format"`
	Version     int      `json:"request_version"`
	PackageKey  string   `json:"package_key"`
	IdentityKey string   `json:"identity_key"`
	BackupID    string   `json:"backup_id"`
	Bytes       int64    `json:"ciphertext_bytes"`
	SHA         string   `json:"package_sha256"`
	HeaderKIDs  []string `json:"available_header_kids"`
	Limits      Limits   `json:"limits"`
}

func LoadInspectionRequest(r io.Reader) (InspectionRequest, error) {
	var request InspectionRequest
	if r == nil {
		return request, ErrPackageSchema
	}
	data, e := io.ReadAll(io.LimitReader(r, (16<<10)+1))
	if e != nil || len(data) > 16<<10 || exactJSON(data, &request) != nil || request.validate() != nil {
		return InspectionRequest{}, ErrPackageSchema
	}
	return request, nil
}
func (r InspectionRequest) validate() error {
	l := r.Limits
	if r.Format != "ylreader-package-inspection-request" || r.Version != 1 || !safeKey(r.PackageKey) || !safeKey(r.IdentityKey) || r.PackageKey == r.IdentityKey || !validUUID(r.BackupID) || !validSHA(r.SHA) || r.Bytes < 1 || r.Bytes > l.Ciphertext || l.Validate() != nil || l.Ciphertext > 256<<20 || l.Plaintext > 512<<20 || l.Manifest > 4<<20 || l.Objects > 10000 || l.Object > 64<<20 || len(r.HeaderKIDs) > 10000 {
		return ErrPackageSchema
	}
	for i, k := range r.HeaderKIDs {
		if !kidName.MatchString(k) || (i > 0 && r.HeaderKIDs[i-1] >= k) {
			return ErrPackageSchema
		}
	}
	return nil
}

type PackageInspection struct {
	Format           string `json:"inspection_format"`
	Version          int    `json:"inspection_version"`
	Scope            string `json:"scope"`
	BackupID         string `json:"backup_id"`
	SourceID         string `json:"source_instance_id"`
	PackageSHA       string `json:"package_sha256"`
	ManifestSHA      string `json:"manifest_sha256"`
	SchemaVersion    int    `json:"schema_version"`
	PostgresMajor    int    `json:"postgres_major"`
	DatabaseBytes    int64  `json:"database_bytes"`
	ObjectCount      int64  `json:"object_count"`
	TotalBytes       int64  `json:"total_plaintext_bytes"`
	Authenticated    bool   `json:"package_authenticated"`
	AuthorTrusted    bool   `json:"author_trusted"`
	DatabaseVerified bool   `json:"database_semantics_verified"`
	HeaderVerified   bool   `json:"header_decrypt_verified"`
	Execution        bool   `json:"execution_authorized"`
	Deployment       bool   `json:"deployment_authorized"`
}

// ReadOnlyInspection cannot open plaintext or acquire a restore guard.
type ReadOnlyInspection struct {
	summary PackageInspection
	binding PackageBinding
	policy  Limits
}

func (v *ReadOnlyInspection) Summary() PackageInspection {
	if v == nil {
		return PackageInspection{}
	}
	return v.summary
}
func (v *ReadOnlyInspection) PreparePlan(ctx context.Context, c Config, now time.Time) (Plan, error) {
	if v == nil || !v.summary.Authenticated || c.Limits != v.policy {
		return Plan{}, ErrPackageSchema
	}
	return PreparePlan(ctx, c, v.binding, "", now, PostgresTargetInspector{})
}

type contextReaderAt struct {
	ctx    context.Context
	source io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	n, e := r.source.ReadAt(p, off)
	// ReaderAt may return EOF with a complete read at the final boundary.
	if n == len(p) && e == io.EOF {
		e = nil
	}
	return n, e
}

func InspectPackageReadOnly(ctx context.Context, source io.ReaderAt, identity *age.X25519Identity, options VerifyOptions) (*ReadOnlyInspection, error) {
	if ctx == nil || source == nil || identity == nil || options.Limits.Validate() != nil || !validUUID(options.Descriptor.BackupID) || !validSHA(options.Descriptor.SHA256) || options.Descriptor.Bytes < 1 {
		return nil, ErrPackageSchema
	}
	d, l := options.Descriptor, options.Limits
	if d.Bytes > l.Ciphertext {
		return nil, ErrPackageLimit
	}
	source = contextReaderAt{ctx, source}
	check := func() error {
		var extra [1]byte
		if n, e := source.ReadAt(extra[:], d.Bytes); n != 0 || e != io.EOF {
			return ErrPackageIntegrity
		}
		h := sha256.New()
		n, e := io.Copy(h, io.NewSectionReader(source, 0, d.Bytes))
		if e != nil || n != d.Bytes || hex.EncodeToString(h.Sum(nil)) != d.SHA256 {
			return ErrPackageIntegrity
		}
		return nil
	}
	if e := check(); e != nil {
		return nil, e
	}
	clear, size, e := age.DecryptReaderAt(source, d.Bytes, singleX25519{identity})
	if e != nil {
		return nil, ErrPackageIntegrity
	}
	if size < 1024 {
		return nil, ErrPackageIntegrity
	}
	if size > l.Plaintext+l.Manifest+(l.Objects+2)*1024+10240 || size > 1<<40 {
		return nil, ErrPackageLimit
	}
	clear = contextReaderAt{ctx, clear}
	if n, e := io.Copy(io.Discard, packageContextReader{ctx, io.NewSectionReader(clear, 0, size)}); e != nil || n != size {
		return nil, ErrPackageIntegrity
	}
	discard := func(ctx context.Context, _ string, r io.Reader, n int64) error {
		got, e := io.Copy(io.Discard, packageContextReader{ctx, io.LimitReader(r, n+1)})
		if e != nil || got != n {
			return ErrPackageIntegrity
		}
		return nil
	}
	m, msha, e := verifyArchiveConsumer(ctx, discard, clear, size, options)
	if e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	return &ReadOnlyInspection{summary: PackageInspection{Format: "ylreader-package-inspection", Version: 1, Scope: "authenticated-package-read-only", BackupID: m.BackupID, SourceID: m.SourceID, PackageSHA: d.SHA256, ManifestSHA: msha, SchemaVersion: m.SchemaVersion, PostgresMajor: m.PostgresMajor, DatabaseBytes: m.Database.Bytes, ObjectCount: m.ObjectCount, TotalBytes: m.TotalBytes, Authenticated: true}, binding: PackageBinding{BackupID: m.BackupID, SourceID: m.SourceID, PackageSHA: d.SHA256, ManifestSHA: msha, SchemaVersion: m.SchemaVersion, MigrationChecksum: m.Migrations[0].Checksum}, policy: l}, nil
}

// InspectStoredPackage only uses Get. It does not own any write capability.
func InspectStoredPackage(ctx context.Context, reader RestoreRecordReader, request InspectionRequest) (*ReadOnlyInspection, error) {
	if ctx == nil || reader == nil || request.validate() != nil {
		return nil, ErrPackageSchema
	}
	key, e := reader.Get(ctx, request.IdentityKey)
	if e != nil {
		return nil, ErrPackageIntegrity
	}
	data, e := io.ReadAll(io.LimitReader(key, 4097))
	closeErr := key.Close()
	defer clear(data)
	if e != nil || closeErr != nil || len(data) > 4096 {
		return nil, ErrPackageIntegrity
	}
	identity, e := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if e != nil {
		return nil, ErrPackageIntegrity
	}
	file, e := reader.Get(ctx, request.PackageKey)
	if e != nil {
		return nil, ErrPackageIntegrity
	}
	at, ok := file.(io.ReaderAt)
	if !ok {
		file.Close()
		return nil, ErrPackageIntegrity
	}
	result, e := InspectPackageReadOnly(ctx, at, identity, VerifyOptions{Descriptor: PackageDescriptor{BackupID: request.BackupID, Bytes: request.Bytes, SHA256: request.SHA}, Limits: request.Limits, HeaderKIDs: request.HeaderKIDs})
	if file.Close() != nil {
		return nil, ErrPackageIntegrity
	}
	return result, e
}

// CheckInspectionIdentity binds the configured identity reference to the file used.
func CheckInspectionIdentity(root string, r InspectionRequest, c Config) bool {
	return c.Encryption.Identity != "" && filepath.Clean(c.Encryption.Identity) == filepath.Join(root, filepath.FromSlash(r.IdentityKey))
}
