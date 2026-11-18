package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"filippo.io/age"
	"yreader/backend/internal/pkg/storage"
)

type memoryScratch struct {
	free         int64
	temps        []*memoryTemp
	cleanupFails bool
}

func (s *memoryScratch) AvailableBytes(context.Context) (int64, error) { return s.free, nil }
func (s *memoryScratch) NewTemp(context.Context) (storage.BackupTemp, error) {
	temp := &memoryTemp{files: map[string][]byte{}, cleanupFails: s.cleanupFails}
	s.temps = append(s.temps, temp)
	return temp, nil
}

type memoryTemp struct {
	files                           map[string][]byte
	created                         []string
	removed, retained, cleanupFails bool
}
type memoryAt struct{ *bytes.Reader }

func (*memoryAt) Close() error        { return nil }
func (*memoryTemp) Reference() string { return "synthetic-scratch" }
func (m *memoryTemp) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.files[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &memoryAt{bytes.NewReader(data)}, nil
}
func (m *memoryTemp) CreateOnly(ctx context.Context, key string, r io.Reader, size int64) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if _, ok := m.files[key]; ok {
		return storage.ErrConflict
	}
	data, e := io.ReadAll(io.LimitReader(r, size+1))
	if e != nil {
		return e
	}
	if int64(len(data)) != size {
		return io.ErrUnexpectedEOF
	}
	m.files[key] = data
	m.created = append(m.created, key)
	return nil
}
func (m *memoryTemp) Remove() error {
	if m.cleanupFails {
		return errors.New("synthetic cleanup fault")
	}
	m.removed = true
	m.files = nil
	return nil
}
func (m *memoryTemp) Retain() error {
	m.retained = true
	return nil
}

type archiveEntry struct {
	name string
	body []byte
	kind byte
	pax  map[string]string
}

func sha(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func packageFixture(t *testing.T) (Manifest, []archiveEntry) {
	t.Helper()
	data, e := os.ReadFile("../../../docs/examples/backup-manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	if e = json.Unmarshal(data, &m); e != nil {
		t.Fatal(e)
	}
	database := []byte("PGDMPsynthetic; not an executable PostgreSQL archive")
	body := []byte("正文 😀\x00")
	m.Database.Bytes = int64(len(database))
	m.Database.SHA = sha(database)
	m.Objects[0].Bytes = int64(len(body))
	m.Objects[0].SHA = sha(body)
	m.TotalBytes = m.Database.Bytes + m.Objects[0].Bytes
	return m, []archiveEntry{{"database.dump", database, tar.TypeReg, nil}, {"objects/" + m.Objects[0].ID, body, tar.TypeReg, nil}}
}
func tarFixture(t *testing.T, m Manifest, entries []archiveEntry, rawManifest []byte) []byte {
	t.Helper()
	if rawManifest == nil {
		var e error
		rawManifest, e = json.Marshal(m)
		if e != nil {
			t.Fatal(e)
		}
	}
	entries = append([]archiveEntry{{"manifest.json", rawManifest, tar.TypeReg, nil}}, entries...)
	var b bytes.Buffer
	writer := tar.NewWriter(&b)
	for _, entry := range entries {
		format := tar.FormatUSTAR
		if entry.pax != nil {
			format = tar.FormatPAX
		}
		header := &tar.Header{Name: entry.name, Size: int64(len(entry.body)), Mode: 0600, Typeflag: entry.kind, Format: format, PAXRecords: entry.pax}
		if entry.kind == tar.TypeSymlink || entry.kind == tar.TypeLink {
			header.Linkname = "../../synthetic-target"
			header.Size = 0
		}
		if e := writer.WriteHeader(header); e != nil {
			t.Fatal(e)
		}
		if header.Size > 0 {
			if _, e := writer.Write(entry.body); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := writer.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func encryptFixture(t *testing.T, plain []byte, id *age.X25519Identity, extra ...age.Recipient) ([]byte, VerifyOptions) {
	t.Helper()
	var b bytes.Buffer
	recipients := append([]age.Recipient{id.Recipient()}, extra...)
	writer, e := age.Encrypt(&b, recipients...)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = writer.Write(plain); e != nil {
		t.Fatal(e)
	}
	if e = writer.Close(); e != nil {
		t.Fatal(e)
	}
	encrypted := b.Bytes()
	return encrypted, VerifyOptions{Descriptor: PackageDescriptor{BackupID: "30000000-0000-4000-8000-000000000001", Bytes: int64(len(encrypted)), SHA256: sha(encrypted)}, Limits: Limits{Ciphertext: 1 << 20, Plaintext: 1 << 20, Manifest: 128 << 10, Objects: 10, Object: 256 << 10}, HeaderKIDs: []string{"primary"}}
}
func identityFixture(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	return id
}

func TestPackageAuthenticatedRoundTrip(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	scratch := &memoryScratch{free: 1 << 30}
	verified, e := VerifyPackage(context.Background(), bytes.NewReader(data), id, options, scratch)
	if e != nil {
		t.Fatal(e)
	}
	if verified.ObjectCount() != 1 || verified.Binding().SourceID != m.SourceID || verified.Binding().PackageSHA != sha(data) {
		t.Fatal("incorrect binding")
	}
	database, e := verified.OpenDatabase(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = verified.Close(); !errors.Is(e, storage.ErrBusy) {
		t.Fatal("removed live readers")
	}
	got, _ := io.ReadAll(database)
	database.Close()
	if !bytes.Equal(got, entries[0].body) {
		t.Fatal("database bytes changed")
	}
	object, e := verified.OpenObject(context.Background(), m.Objects[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	got, _ = io.ReadAll(object)
	object.Close()
	if !bytes.Equal(got, entries[1].body) {
		t.Fatal("object bytes changed")
	}
	if e = verified.Close(); e != nil {
		t.Fatal(e)
	}
	if !scratch.temps[0].removed {
		t.Fatal("scratch leaked")
	}
	if e = verified.Close(); e != nil {
		t.Fatal("cleanup not idempotent")
	}
}

func TestPackageAuthenticationBeforePlaintextUse(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	valid, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	tests := map[string][]byte{"mid-chunk": append([]byte(nil), valid...), "final-tag": append([]byte(nil), valid...), "truncated": valid[:len(valid)-1], "trailing": append(append([]byte(nil), valid...), 0)}
	tests["mid-chunk"][len(valid)/2] ^= 1
	tests["final-tag"][len(valid)-1] ^= 1
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			option := options
			option.Descriptor.Bytes = int64(len(data))
			option.Descriptor.SHA256 = sha(data)
			scratch := &memoryScratch{free: 1 << 30}
			if v, e := VerifyPackage(context.Background(), bytes.NewReader(data), id, option, scratch); e == nil || v != nil {
				t.Fatal("unauthenticated package accepted")
			}
			for _, key := range scratch.temps[0].created {
				if key != "package.age" {
					t.Fatal("plaintext used before full authentication", key)
				}
			}
			if !scratch.temps[0].removed {
				t.Fatal("scratch leaked")
			}
		})
	}
	wrong := identityFixture(t)
	scratch := &memoryScratch{free: 1 << 30}
	if _, e := VerifyPackage(context.Background(), bytes.NewReader(valid), wrong, options, scratch); !errors.Is(e, ErrPackageIntegrity) {
		t.Fatal("wrong key accepted", e)
	}
	multiple, option := encryptFixture(t, tarFixture(t, m, entries, nil), id, wrong.Recipient())
	if _, e := VerifyPackage(context.Background(), bytes.NewReader(multiple), id, option, &memoryScratch{free: 1 << 30}); !errors.Is(e, ErrPackageIntegrity) {
		t.Fatal("multiple recipients accepted", e)
	}
}

func TestPackageManifestAndArchiveRejections(t *testing.T) {
	id := identityFixture(t)
	for _, name := range []string{"object-hash", "database-hash", "object-size", "duplicate-id", "unsafe-key", "unknown-field", "duplicate-field", "schema", "missing-object", "extra-object", "symlink", "hardlink", "pax", "wrong-order", "short-trailer", "trailing-junk", "kid-coverage", "no-database-magic", "nonzero-padding"} {
		t.Run(name, func(t *testing.T) {
			m, entries := packageFixture(t)
			var raw []byte
			switch name {
			case "object-hash":
				m.Objects[0].SHA = strings.Repeat("0", 64)
			case "database-hash":
				m.Database.SHA = strings.Repeat("0", 64)
			case "object-size":
				m.Objects[0].Bytes++
				m.TotalBytes++
			case "duplicate-id":
				m.Objects = append(m.Objects, m.Objects[0])
				m.ObjectCount++
				m.TotalBytes += m.Objects[0].Bytes
			case "unsafe-key":
				m.Objects[0].Key = "a/../escape"
			case "unknown-field":
				raw, _ = json.Marshal(m)
				raw = append([]byte(`{"unexpected":1,`), raw[1:]...)
			case "duplicate-field":
				raw, _ = json.Marshal(m)
				raw = append([]byte(`{"format_version":1,`), raw[1:]...)
			case "schema":
				m.Migrations[0].Checksum = strings.Repeat("0", 64)
			case "missing-object":
				entries = entries[:1]
			case "extra-object":
				entries = append(entries, archiveEntry{"objects/" + strings.Repeat("2", 32), []byte("extra"), tar.TypeReg, nil})
			case "symlink":
				entries[1].kind = tar.TypeSymlink
			case "hardlink":
				entries[1].kind = tar.TypeLink
			case "pax":
				entries[1].pax = map[string]string{"comment": "synthetic"}
			case "wrong-order":
				entries[0], entries[1] = entries[1], entries[0]
			case "no-database-magic":
				entries[0].body[0] = 'X'
				m.Database.SHA = sha(entries[0].body)
			}
			plain := tarFixture(t, m, entries, raw)
			if name == "short-trailer" {
				plain = plain[:len(plain)-512]
			}
			if name == "trailing-junk" {
				junk := make([]byte, 512)
				junk[0] = 1
				plain = append(plain, junk...)
			}
			if name == "nonzero-padding" {
				manifestBytes, _ := json.Marshal(m)
				plain[512+len(manifestBytes)] = 1
			}
			data, option := encryptFixture(t, plain, id)
			if name == "kid-coverage" {
				option.HeaderKIDs = nil
			}
			scratch := &memoryScratch{free: 1 << 30}
			verified, e := VerifyPackage(context.Background(), bytes.NewReader(data), id, option, scratch)
			if e == nil || verified != nil {
				t.Fatal("invalid package accepted")
			}
			if len(scratch.temps) != 1 || !scratch.temps[0].removed {
				t.Fatal("failed package residue")
			}
		})
	}
}

func TestPackageLimitsCancellationAndCleanupFailure(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	for _, name := range []string{"cipher", "plaintext", "manifest", "objects", "object", "space"} {
		t.Run(name, func(t *testing.T) {
			option := options
			scratch := &memoryScratch{free: 1 << 30}
			switch name {
			case "cipher":
				option.Limits.Ciphertext = 1
			case "plaintext":
				option.Limits.Plaintext = 1
			case "manifest":
				option.Limits.Manifest = 1
			case "objects":
				option.Limits.Objects = 0
			case "object":
				option.Limits.Object = 1
			case "space":
				scratch.free = 1
			}
			if _, e := VerifyPackage(context.Background(), bytes.NewReader(data), id, option, scratch); !errors.Is(e, ErrPackageLimit) {
				t.Fatal("limit not enforced", e)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scratch := &memoryScratch{free: 1 << 30}
	if _, e := VerifyPackage(ctx, bytes.NewReader(data), id, options, scratch); !errors.Is(e, context.Canceled) || len(scratch.temps) != 0 {
		t.Fatal("canceled input used")
	}
	option := options
	option.Descriptor.SHA256 = strings.Repeat("0", 64)
	scratch = &memoryScratch{free: 1 << 30, cleanupFails: true}
	_, e := VerifyPackage(context.Background(), bytes.NewReader(data), id, option, scratch)
	var failure *CleanupFailure
	if !errors.Is(e, ErrCleanup) || !errors.As(e, &failure) || failure.ScratchReference != "synthetic-scratch" {
		t.Fatal("cleanup failure hidden", e)
	}
}

func TestManifestUTF8ChunkBoundaries(t *testing.T) {
	good := []byte(strings.Repeat("a", 32767) + "中文 😀")
	if hash, e := manifestUTF8Hash(context.Background(), bytes.NewReader(good)); e != nil || hash != sha(good) {
		t.Fatal("UTF-8/hash boundary", e)
	}
	for _, bad := range [][]byte{append([]byte("ascii"), 0xff), {0xe4, 0xb8}} {
		if _, e := manifestUTF8Hash(context.Background(), bytes.NewReader(bad)); !errors.Is(e, ErrPackageSchema) {
			t.Fatal("invalid UTF-8 accepted")
		}
	}
	for _, raw := range []string{`"\ud800"`, `"\udc00"`, `"\ud800x"`} {
		if pairedUnicodeEscapes([]byte(raw)) {
			t.Fatal("unpaired escape accepted")
		}
	}
	for _, raw := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `"replacement �"`} {
		if !pairedUnicodeEscapes([]byte(raw)) {
			t.Fatal("valid escaped Unicode rejected")
		}
	}
}

func TestManifestLimitsBeforeJSONAllocation(t *testing.T) {
	m, _ := packageFixture(t)
	base, _ := json.Marshal(m)
	limits := Limits{Ciphertext: 4 << 20, Plaintext: 4 << 20, Manifest: 4 << 20, Objects: 10000, Object: 1 << 20}
	hugeString := strings.Replace(string(base), `"format":"ylreader-instance-backup"`, `"format":"`+strings.Repeat("x", 100000)+`"`, 1)
	hugeHeaderArray := strings.Replace(string(base), `"required_header_kids":["primary"]`, `"required_header_kids":[`+strings.Repeat(`"x",`, 400000)+`"x"]`, 1)
	hugeObject := strings.Replace(string(base), `"key":"local/book/0.txt"`, `"key":[`+strings.Repeat(`{},`, 15000)+`{}]`, 1)
	hugeSpace := strings.Replace(string(base), `"objects":[`, `"objects":[`+strings.Repeat(" ", 100000), 1)
	for name, data := range map[string]string{"string": hugeString, "header-array": hugeHeaderArray, "record": hugeObject, "whitespace": hugeSpace} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseManifest(context.Background(), strings.NewReader(data), limits); !errors.Is(e, ErrPackageLimit) {
				t.Fatal("decoder allocation limit not applied", e)
			}
		})
	}
}

func TestManifestStreamsPastOneMiB(t *testing.T) {
	m, _ := packageFixture(t)
	m.Objects = make([]ManifestObject, 8000)
	m.ObjectCount = int64(len(m.Objects))
	m.TotalBytes = m.Database.Bytes
	for i := range m.Objects {
		m.Objects[i] = ManifestObject{ID: fmt.Sprintf("%032x", i+1), Key: fmt.Sprintf("local/book/%d.txt", i), SHA: sha(nil)}
	}
	data, e := json.Marshal(m)
	if e != nil || len(data) <= 1<<20 {
		t.Fatal("fixture does not exercise streaming")
	}
	limits := Limits{Ciphertext: 4 << 20, Plaintext: 4 << 20, Manifest: 4 << 20, Objects: 10000, Object: 1 << 20}
	parsed, e := parseManifest(context.Background(), bytes.NewReader(data), limits)
	if e != nil || len(parsed.Objects) != 8000 {
		t.Fatal("large valid manifest rejected", e)
	}
}

type cancelPackageReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r cancelPackageReader) Read(p []byte) (int, error) {
	n, e := r.reader.Read(p)
	r.cancel()
	return n, e
}

type secretErrorReader struct{}

func (secretErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic-secret-must-not-leak")
}

func TestPackageMidstreamCancelAndReadError(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	ctx, cancel := context.WithCancel(context.Background())
	scratch := &memoryScratch{free: 1 << 30}
	if _, e := VerifyPackage(ctx, cancelPackageReader{bytes.NewReader(data), cancel}, id, options, scratch); !errors.Is(e, context.Canceled) || !scratch.temps[0].removed {
		t.Fatal("midstream cancel failed cleanup", e)
	}
	scratch = &memoryScratch{free: 1 << 30}
	if _, e := VerifyPackage(context.Background(), secretErrorReader{}, id, options, scratch); !errors.Is(e, ErrPackageIntegrity) || strings.Contains(e.Error(), "synthetic-secret") {
		t.Fatal("source error leaked", e)
	}
}
