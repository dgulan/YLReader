package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"sync"

	"filippo.io/age"
	"yreader/backend/internal/pkg/storage"
)

var ErrCleanup = errors.New("backup: temporary cleanup failed")

type CleanupFailure struct{ ScratchReference string }

func (*CleanupFailure) Error() string { return ErrCleanup.Error() }
func (*CleanupFailure) Unwrap() error { return ErrCleanup }

type PackageDescriptor struct {
	BackupID string
	Bytes    int64
	SHA256   string
}
type VerifyOptions struct {
	Descriptor PackageDescriptor
	Limits     Limits
	HeaderKIDs []string
}
type ScratchFactory interface {
	NewTemp(context.Context) (storage.BackupTemp, error)
	AvailableBytes(context.Context) (int64, error)
}

// VerifiedPackage owns only private staging data. Authentication proves content
// integrity for this recipient, NOT the identity/trustworthiness of its author.
type VerifiedPackage struct {
	mu                 sync.Mutex
	temp               storage.BackupTemp
	manifest           Manifest
	binding            PackageBinding
	readers            int
	verificationLimits Limits
}

func (v *VerifiedPackage) Binding() PackageBinding {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.binding
}
func (v *VerifiedPackage) ObjectCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.manifest.Objects)
}
func (v *VerifiedPackage) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.temp == nil {
		return nil
	}
	if v.readers != 0 {
		return storage.ErrBusy
	}
	if e := v.temp.Remove(); e != nil {
		return &CleanupFailure{v.temp.Reference()}
	}
	v.temp = nil
	v.manifest.Objects = nil
	return nil
}

type packageReader struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (r *packageReader) Close() error {
	var result error
	r.once.Do(func() { result = r.ReadCloser.Close(); r.done() })
	return result
}
func (v *VerifiedPackage) open(ctx context.Context, key string) (io.ReadCloser, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.temp == nil {
		return nil, ErrPackageIntegrity
	}
	reader, e := v.temp.Get(ctx, key)
	if e != nil {
		return nil, e
	}
	v.readers++
	return &packageReader{ReadCloser: reader, done: func() { v.mu.Lock(); v.readers--; v.mu.Unlock() }}, nil
}
func (v *VerifiedPackage) OpenDatabase(ctx context.Context) (io.ReadCloser, error) {
	return v.open(ctx, "database.dump")
}
func (v *VerifiedPackage) OpenObject(ctx context.Context, id string) (io.ReadCloser, error) {
	v.mu.Lock()
	index := sort.Search(len(v.manifest.Objects), func(i int) bool { return v.manifest.Objects[i].ID >= id })
	found := index < len(v.manifest.Objects) && v.manifest.Objects[index].ID == id
	v.mu.Unlock()
	if !found {
		return nil, ErrPackageSchema
	}
	return v.open(ctx, "object-"+id)
}

type singleX25519 struct{ identity *age.X25519Identity }

func (i singleX25519) Unwrap(stanzas []*age.Stanza) ([]byte, error) {
	if len(stanzas) != 1 || stanzas[0].Type != "X25519" {
		return nil, ErrPackageIntegrity
	}
	return i.identity.Unwrap(stanzas)
}

type packageContextReader struct {
	context.Context
	io.Reader
}

func (r packageContextReader) Read(p []byte) (int, error) {
	if e := r.Err(); e != nil {
		return 0, e
	}
	return r.Reader.Read(p)
}

func freeSpace(ctx context.Context, factory ScratchFactory, needed int64) error {
	available, e := factory.AvailableBytes(ctx)
	if e != nil {
		return e
	}
	if available < needed+(64<<10) {
		return ErrPackageLimit
	}
	return nil
}

func VerifyPackage(ctx context.Context, source io.Reader, identity *age.X25519Identity, options VerifyOptions, scratch ScratchFactory) (result *VerifiedPackage, err error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	descriptor, limits := options.Descriptor, options.Limits
	if source == nil || identity == nil || scratch == nil || limits.Validate() != nil {
		return nil, ErrPackageSchema
	}
	if !validUUID(descriptor.BackupID) || !validSHA(descriptor.SHA256) || descriptor.Bytes < 1 {
		return nil, ErrPackageSchema
	}
	if descriptor.Bytes > limits.Ciphertext {
		return nil, ErrPackageLimit
	}
	if e := freeSpace(ctx, scratch, descriptor.Bytes); e != nil {
		return nil, e
	}
	temp, e := scratch.NewTemp(ctx)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if e := temp.Remove(); e != nil {
				err = errors.Join(err, &CleanupFailure{temp.Reference()})
			}
		}
	}()
	hash := sha256.New()
	if e = temp.CreateOnly(ctx, "package.age", io.TeeReader(packageContextReader{ctx, source}, hash), descriptor.Bytes); e != nil {
		return nil, ErrPackageIntegrity
	}
	if hex.EncodeToString(hash.Sum(nil)) != descriptor.SHA256 {
		return nil, ErrPackageIntegrity
	}
	cipher, e := temp.Get(ctx, "package.age")
	if e != nil {
		return nil, e
	}
	defer cipher.Close()
	cipherAt, ok := cipher.(io.ReaderAt)
	if !ok {
		return nil, storage.ErrCapability
	}
	plaintext, size, e := age.DecryptReaderAt(cipherAt, descriptor.Bytes, singleX25519{identity})
	if e != nil {
		return nil, ErrPackageIntegrity
	}
	tarBudget := limits.Plaintext + limits.Manifest + (limits.Objects+2)*1024 + 10240
	if size < 1024 {
		return nil, ErrPackageIntegrity
	}
	if size > tarBudget || size > 1<<40 {
		return nil, ErrPackageLimit
	}
	if e = freeSpace(ctx, scratch, size*2); e != nil {
		return nil, e
	}
	// ReaderAt authenticates individual chunks. Read every byte before parsing
	// or publishing plaintext; final-chunk validation alone is insufficient.
	if _, e = io.Copy(io.Discard, packageContextReader{ctx, io.NewSectionReader(plaintext, 0, size)}); e != nil {
		return nil, ErrPackageIntegrity
	}
	if e = temp.CreateOnly(ctx, "authenticated.tar", packageContextReader{ctx, io.NewSectionReader(plaintext, 0, size)}, size); e != nil {
		return nil, ErrPackageIntegrity
	}
	clear, e := temp.Get(ctx, "authenticated.tar")
	if e != nil {
		return nil, e
	}
	defer clear.Close()
	archive, ok := clear.(io.ReaderAt)
	if !ok {
		return nil, storage.ErrCapability
	}
	manifest, manifestSHA, e := verifyArchive(ctx, temp, archive, size, options)
	if e != nil {
		return nil, e
	}
	return &VerifiedPackage{temp: temp, manifest: manifest, verificationLimits: options.Limits, binding: PackageBinding{BackupID: manifest.BackupID, SourceID: manifest.SourceID, PackageSHA: descriptor.SHA256, ManifestSHA: manifestSHA, SchemaVersion: manifest.SchemaVersion, MigrationChecksum: manifest.Migrations[0].Checksum}}, nil
}

func blockAt(reader io.ReaderAt, offset int64) ([512]byte, error) {
	var block [512]byte
	_, e := reader.ReadAt(block[:], offset)
	if e != nil {
		return block, ErrPackageIntegrity
	}
	return block, nil
}
func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}
func headerAt(reader io.ReaderAt, offset int64) (*tar.Header, bool, error) {
	block, e := blockAt(reader, offset)
	if e != nil {
		return nil, false, e
	}
	if allZero(block[:]) {
		return nil, true, nil
	}
	// Inspect physical headers, so PAX/GNU metadata cannot be silently consumed.
	if block[156] != tar.TypeReg || !allZero(block[345:500]) {
		return nil, false, ErrPackageIntegrity
	}
	if end := bytes.IndexByte(block[:100], 0); end >= 0 && !allZero(block[end:100]) {
		return nil, false, ErrPackageIntegrity
	}
	h, e := tar.NewReader(bytes.NewReader(block[:])).Next()
	if e != nil || h.Format != tar.FormatUSTAR || h.Linkname != "" || h.Size < 0 || h.Typeflag != tar.TypeReg {
		return nil, false, ErrPackageIntegrity
	}
	return h, false, nil
}

func verifyArchive(ctx context.Context, temp storage.BackupTemp, reader io.ReaderAt, size int64, options VerifyOptions) (Manifest, string, error) {
	return verifyArchiveConsumer(ctx, temp.CreateOnly, reader, size, options)
}

type archiveConsumer func(context.Context, string, io.Reader, int64) error

func verifyArchiveConsumer(ctx context.Context, consume archiveConsumer, reader io.ReaderAt, size int64, options VerifyOptions) (Manifest, string, error) {
	var manifest Manifest
	manifestSHA := ""
	if size%512 != 0 {
		return manifest, "", ErrPackageIntegrity
	}
	offset := int64(0)
	index := 0
	for {
		if e := ctx.Err(); e != nil {
			return Manifest{}, "", e
		}
		if size-offset < 512 {
			return Manifest{}, "", ErrPackageIntegrity
		}
		header, end, e := headerAt(reader, offset)
		if e != nil {
			return Manifest{}, "", e
		}
		if end {
			trailer := size - offset
			if index < 2 || index != len(manifest.Objects)+2 || trailer < 1024 || trailer > 10240 {
				return Manifest{}, "", ErrPackageIntegrity
			}
			buffer := make([]byte, 512)
			for at := offset; at < size; at += 512 {
				if _, e = reader.ReadAt(buffer, at); e != nil || !allZero(buffer) {
					return Manifest{}, "", ErrPackageIntegrity
				}
			}
			return manifest, manifestSHA, nil
		}
		body := offset + 512
		if header.Size > size-body {
			return Manifest{}, "", ErrPackageIntegrity
		}
		next := body + header.Size + (512-header.Size%512)%512
		if next > size {
			return Manifest{}, "", ErrPackageIntegrity
		}
		padding := make([]byte, next-(body+header.Size))
		if len(padding) > 0 {
			if _, e = reader.ReadAt(padding, body+header.Size); e != nil || !allZero(padding) {
				return Manifest{}, "", ErrPackageIntegrity
			}
		}
		switch {
		case index == 0:
			if header.Name != "manifest.json" || header.Size < 1 {
				return Manifest{}, "", ErrPackageIntegrity
			}
			if header.Size > options.Limits.Manifest {
				return Manifest{}, "", ErrPackageLimit
			}
			manifestSHA, e = manifestUTF8Hash(ctx, io.NewSectionReader(reader, body, header.Size))
			if e != nil {
				return Manifest{}, "", e
			}
			manifest, e = parseManifest(ctx, io.NewSectionReader(reader, body, header.Size), options.Limits)
			if e != nil {
				return Manifest{}, "", e
			}
			if manifest.BackupID != options.Descriptor.BackupID {
				return Manifest{}, "", ErrPackageSchema
			}
			available := map[string]bool{}
			for _, kid := range options.HeaderKIDs {
				available[kid] = true
			}
			for _, kid := range manifest.HeaderKIDs {
				if !available[kid] {
					return Manifest{}, "", ErrHeaderKey
				}
			}
		case index == 1:
			if header.Name != "database.dump" || header.Size != manifest.Database.Bytes || header.Size < 5 {
				return Manifest{}, "", ErrPackageIntegrity
			}
			var magic [5]byte
			if _, e = reader.ReadAt(magic[:], body); e != nil || string(magic[:]) != "PGDMP" {
				return Manifest{}, "", ErrPackageIntegrity
			}
			if e = consumeVerified(ctx, consume, "database.dump", reader, body, header.Size, manifest.Database.SHA); e != nil {
				return Manifest{}, "", e
			}
		default:
			objectIndex := index - 2
			if objectIndex >= len(manifest.Objects) {
				return Manifest{}, "", ErrPackageIntegrity
			}
			object := manifest.Objects[objectIndex]
			if header.Name != "objects/"+object.ID || header.Size != object.Bytes {
				return Manifest{}, "", ErrPackageIntegrity
			}
			if e = consumeVerified(ctx, consume, "object-"+object.ID, reader, body, header.Size, object.SHA); e != nil {
				return Manifest{}, "", e
			}
		}
		index++
		offset = next
	}
}
func consumeVerified(ctx context.Context, consume archiveConsumer, name string, reader io.ReaderAt, offset, size int64, expected string) error {
	hash := sha256.New()
	input := io.TeeReader(packageContextReader{ctx, io.NewSectionReader(reader, offset, size)}, hash)
	if consume(ctx, name, input, size) != nil {
		return ErrPackageIntegrity
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return ErrPackageIntegrity
	}
	return nil
}
