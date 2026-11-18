package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectionIdentityLexicalEquivalence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "inputs")
	c := Config{}
	c.Encryption.Identity = root + string(filepath.Separator) + string(filepath.Separator) + "identity.txt"
	r := InspectionRequest{IdentityKey: "identity.txt"}
	if !CheckInspectionIdentity(root, r, c) {
		t.Fatal("equivalent identity rejected")
	}
	r.IdentityKey = "other.txt"
	if CheckInspectionIdentity(root, r, c) {
		t.Fatal("different identity accepted")
	}
}

type changingInspectionReader struct {
	data   []byte
	probes int
}

func (r *changingInspectionReader) ReadAt(p []byte, off int64) (int, error) {
	if off == int64(len(r.data)) && len(p) == 1 {
		r.probes++
		if r.probes == 2 {
			r.data[0] ^= 1
		}
		return 0, io.EOF
	}
	return bytes.NewReader(r.data).ReadAt(p, off)
}

func TestInspectionRejectsSourceDrift(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, o := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	r := &changingInspectionReader{data: append([]byte(nil), data...)}
	if result, e := InspectPackageReadOnly(context.Background(), r, id, o); e == nil || result != nil || r.probes != 2 {
		t.Fatal("changed source accepted")
	}
}

func TestInspectionAuthenticatesWithoutScratch(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	v, e := InspectPackageReadOnly(context.Background(), bytes.NewReader(data), id, options)
	if e != nil {
		t.Fatal(e)
	}
	s := v.Summary()
	if !s.Authenticated || s.BackupID != m.BackupID || s.ManifestSHA == "" || s.PackageSHA != sha(data) || s.ObjectCount != 1 || s.TotalBytes != m.TotalBytes || s.DatabaseVerified || s.HeaderVerified || s.AuthorTrusted || s.Execution || s.Deployment {
		t.Fatal("incorrect inspection claims")
	}
	raw, _ := json.Marshal(s)
	for _, secret := range []string{id.String(), string(entries[0].body), m.Objects[0].Key, "正文"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private content in summary")
		}
	}
	if _, e = v.PreparePlan(context.Background(), Config{}, time.Now()); e == nil {
		t.Fatal("unbound config accepted")
	}
	var zero ReadOnlyInspection
	if _, e = zero.PreparePlan(context.Background(), Config{}, time.Now()); e == nil {
		t.Fatal("zero capability accepted")
	}
}

func TestInspectionRejectsTamperingAndUnsupportedArchives(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	base, opts := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	for _, name := range []string{"wrong-key", "corruption", "truncated", "appended", "wrong-digest", "missing-header", "object-digest", "duplicate-entry", "cancelled", "nil-context"} {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), base...)
			o := opts
			key := id
			ctx := context.Background()
			switch name {
			case "wrong-key":
				key = identityFixture(t)
			case "corruption":
				data[len(data)/2] ^= 1
				o.Descriptor.SHA256 = sha(data)
			case "truncated":
				data = data[:len(data)-1]
			case "appended":
				data = append(data, 0)
			case "wrong-digest":
				o.Descriptor.SHA256 = strings.Repeat("0", 64)
			case "missing-header":
				o.HeaderKIDs = nil
			case "object-digest":
				bad := m
				bad.Objects = append([]ManifestObject(nil), m.Objects...)
				bad.Objects[0].SHA = strings.Repeat("0", 64)
				data, o = encryptFixture(t, tarFixture(t, bad, entries, nil), id)
			case "duplicate-entry":
				data, o = encryptFixture(t, tarFixture(t, m, append(entries, entries[1]), nil), id)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			}
			if v, e := InspectPackageReadOnly(ctx, bytes.NewReader(data), key, o); e == nil || v != nil {
				t.Fatal("invalid inspection accepted")
			}
		})
	}
}

func TestInspectionRequestStrictAndStoredReads(t *testing.T) {
	m, entries := packageFixture(t)
	id := identityFixture(t)
	data, o := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	r := InspectionRequest{Format: "ylreader-package-inspection-request", Version: 1, PackageKey: "package.age", IdentityKey: "identity.txt", BackupID: o.Descriptor.BackupID, Bytes: o.Descriptor.Bytes, SHA: o.Descriptor.SHA256, HeaderKIDs: o.HeaderKIDs, Limits: o.Limits}
	encode := func(r InspectionRequest) []byte { b, _ := json.Marshal(r); return b }
	if _, e := LoadInspectionRequest(bytes.NewReader(encode(r))); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"unknown", "duplicate", "traversal", "limit", "kid-order", "oversize"} {
		t.Run(name, func(t *testing.T) {
			bad := r
			raw := encode(r)
			switch name {
			case "unknown":
				raw = append([]byte(`{"secret":1,`), raw[1:]...)
			case "duplicate":
				raw = append([]byte(`{"request_version":1,`), raw[1:]...)
			case "traversal":
				bad.PackageKey = "../escape"
				raw = encode(bad)
			case "limit":
				bad.Limits.Ciphertext = 257 << 20
				raw = encode(bad)
			case "kid-order":
				bad.HeaderKIDs = []string{"z", "a"}
				raw = encode(bad)
			case "oversize":
				raw = append(raw, bytes.Repeat([]byte(" "), 16<<10)...)
			}
			if _, e := LoadInspectionRequest(bytes.NewReader(raw)); e == nil {
				t.Fatal("unsafe request accepted")
			}
		})
	}
	store := &memoryTemp{files: map[string][]byte{"package.age": data, "identity.txt": []byte(id.String() + "\n")}}
	if _, e := InspectStoredPackage(context.Background(), store, r); e != nil {
		t.Fatal(e)
	}
	if len(store.created) != 0 || store.removed {
		t.Fatal("read inspection wrote files")
	}
	store.files["identity.txt"] = []byte(strings.Repeat("x", 4097))
	if _, e := InspectStoredPackage(context.Background(), store, r); e == nil {
		t.Fatal("oversize identity accepted")
	}
}
