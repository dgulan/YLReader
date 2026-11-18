// Design experiment only. Does not import or implement reader business logic.
package prototype

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filippo.io/age"
)

func TestTarEnvelope(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	entries := []struct {
		name string
		body []byte
	}{
		{"manifest.json", []byte(`{"format":"ylreader-instance-backup","format_version":1}`)},
		{"database.dump", []byte("synthetic archive; not PostgreSQL data")},
		{"objects/11111111111111111111111111111111", []byte("正文 😀\x00")},
	}
	var plain bytes.Buffer
	tw := tar.NewWriter(&plain)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0600, Size: int64(len(e.body)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	verified, err := decrypt(id, encrypt(t, id, plain.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(verified))
	for _, e := range entries {
		header, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != e.name || header.Format != tar.FormatUSTAR || !bytes.Equal(body, e.body) {
			t.Fatal("tar entry differs")
		}
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatal("unexpected archive entry")
	}
}

func encrypt(t *testing.T, id *age.X25519Identity, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := age.Encrypt(&out, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func decrypt(id *age.X25519Identity, data []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r) // Header success alone is insufficient; read to verified EOF.
}

func TestOfficialCLIInterop(t *testing.T) {
	cli := os.Getenv("BACKUP_AGE_CLI")
	if cli == "" {
		t.Fatal("BACKUP_AGE_CLI required; no silent skip")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "synthetic-identity.txt")
	if err = os.WriteFile(keyFile, []byte(id.String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, 65535, 65536, 65537, 131089} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			plain := make([]byte, size)
			for i := range plain {
				plain[i] = byte(i % 251)
			}
			t.Run("library-to-cli", func(t *testing.T) {
				cmd := exec.Command(cli, "--decrypt", "--identity", keyFile, "-o", "-")
				cmd.Stdin = bytes.NewReader(encrypt(t, id, plain))
				out, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out, plain) {
					t.Fatal("plaintext differs")
				}
			})
			t.Run("cli-to-library", func(t *testing.T) {
				cmd := exec.Command(cli, "--encrypt", "--recipient", id.Recipient().String(), "-o", "-")
				cmd.Stdin = bytes.NewReader(plain)
				out, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				result, err := decrypt(id, out)
				if err != nil || !bytes.Equal(result, plain) {
					t.Fatalf("roundtrip: %v", err)
				}
			})
		})
	}
}

func TestRejectUnauthenticatedPackage(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	wrong, _ := age.GenerateX25519Identity()
	valid := encrypt(t, id, bytes.Repeat([]byte("synthetic 中文 😀\x00"), 12000))
	header := append([]byte(nil), valid...)
	header[0] ^= 1
	middle := append([]byte(nil), valid...)
	middle[len(middle)/2] ^= 1
	last := append([]byte(nil), valid...)
	last[len(last)-1] ^= 1
	cases := map[string][]byte{"header": header, "middle-chunk": middle, "final-tag": last,
		"truncated-header": valid[:20], "truncated-final": valid[:len(valid)-1],
		"missing-final-chunk": valid[:len(valid)/2], "trailing-data": append(append([]byte(nil), valid...), 0)}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decrypt(id, data)
			if err == nil {
				t.Fatal("accepted unauthenticated bytes")
			}
		})
	}
	t.Run("wrong-key", func(t *testing.T) {
		if _, err := decrypt(wrong, valid); err == nil {
			t.Fatal("accepted wrong identity")
		}
	})
	t.Run("fresh-randomness", func(t *testing.T) {
		if bytes.Equal(valid, encrypt(t, id, bytes.Repeat([]byte("synthetic 中文 😀\x00"), 12000))) {
			t.Fatal("ciphertexts repeat")
		}
	})
}
