package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFollowupManifestBracketPairing(t *testing.T) {
	limits := Limits{Ciphertext: 4 << 20, Plaintext: 4 << 20, Manifest: 4 << 20, Objects: 10000, Object: 1 << 20}
	for _, bad := range []string{`{]`, `{"objects":[}` + strings.Repeat("x", 100000), `{}}`, `]`, `{"objects":[{"key":[]}]}}`} {
		if _, err := parseManifest(context.Background(), strings.NewReader(bad), limits); !errors.Is(err, ErrPackageSchema) {
			t.Fatal("mismatch must be schema error", err)
		}
	}
	for _, good := range []string{`{"objects":[{"value":"[}\\\""}]}`, `{"objects":[],"other":{"value":[1,2]}}`} {
		guard := &manifestReader{reader: strings.NewReader(good), documentLimit: 4 << 20}
		if _, err := io.ReadAll(guard); err != nil {
			t.Fatal("quoted brackets misclassified", err)
		}
	}
	guard := &manifestReader{reader: strings.NewReader(strings.Repeat("[", 9)), documentLimit: 4 << 20}
	if _, err := io.ReadAll(guard); !errors.Is(err, ErrPackageLimit) {
		t.Fatal("depth bound lost", err)
	}
}
