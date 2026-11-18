package backup

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	headercrypto "yreader/backend/internal/pkg/crypto"
)

func headerRing(t *testing.T, kid string, value byte) *headercrypto.Keyring {
	t.Helper()
	ring, e := headercrypto.NewKeyring(kid, map[string][]byte{kid: bytes.Repeat([]byte{value}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	return ring
}
func headerEnvelope(t *testing.T, ring *headercrypto.Keyring, user, source uuid.UUID, plain []byte) []byte {
	t.Helper()
	sealed, e := ring.Seal(plain, headercrypto.AAD(user, source))
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]string{"__enc": sealed})
	return raw
}

func TestRestoreHeaderAuthentication(t *testing.T) {
	user, source := uuid.New(), uuid.New()
	ring := headerRing(t, "primary", 1)
	raw := headerEnvelope(t, ring, user, source, []byte(`{"Cookie":"synthetic-secret-中文😀"}`))
	v, e := newHeaderVerifier([]string{"primary"}, ring)
	if e != nil {
		t.Fatal(e)
	}
	if e = v.check(user, source, raw); e != nil {
		t.Fatal(e)
	}
	if e = v.finish(); e != nil {
		t.Fatal(e)
	}
	for name, spec := range map[string]struct {
		ring         *headercrypto.Keyring
		user, source uuid.UUID
		raw          []byte
		expected     error
	}{
		"wrong-key":        {headerRing(t, "primary", 2), user, source, raw, ErrHeaderDecrypt},
		"wrong-user-aad":   {ring, uuid.New(), source, raw, ErrHeaderDecrypt},
		"wrong-source-aad": {ring, user, uuid.New(), raw, ErrHeaderDecrypt},
		"plaintext":        {ring, user, source, []byte(`{"Cookie":"synthetic-secret"}`), ErrHeaderEnvelope},
		"mixed-envelope":   {ring, user, source, bytes.Replace(raw, []byte(`{`), []byte(`{"extra":"secret",`), 1), ErrHeaderEnvelope},
		"malformed":        {ring, user, source, []byte(`{"__enc":"v1:primary:broken"}`), ErrHeaderDecrypt},
		"unknown-version":  {ring, user, source, bytes.Replace(raw, []byte("v1:"), []byte("v2:"), 1), ErrHeaderEnvelope},
		"invalid-plain":    {ring, user, source, headerEnvelope(t, ring, user, source, []byte(`[]`)), ErrHeaderEnvelope},
		"oversized-plain":  {ring, user, source, headerEnvelope(t, ring, user, source, []byte(`{"x":"`+strings.Repeat("x", 64<<10)+`"}`)), ErrHeaderEnvelope},
	} {
		t.Run(name, func(t *testing.T) {
			v, e := newHeaderVerifier([]string{"primary"}, spec.ring)
			if e != nil {
				t.Fatal(e)
			}
			e = v.check(spec.user, spec.source, spec.raw)
			if !errors.Is(e, spec.expected) || strings.Contains(e.Error(), "synthetic-secret") {
				t.Fatal("wrong or secret-bearing error", e)
			}
		})
	}
	if _, e = newHeaderVerifier([]string{"missing"}, ring); !errors.Is(e, ErrHeaderKey) {
		t.Fatal(e)
	}
	if _, e = newHeaderVerifier([]string{"primary"}, nil); !errors.Is(e, ErrHeaderKey) {
		t.Fatal(e)
	}
	empty, e := newHeaderVerifier(nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = empty.check(user, source, []byte(`{}`)); e != nil {
		t.Fatal(e)
	}
	if e = empty.finish(); e != nil {
		t.Fatal(e)
	}
	if e = empty.check(user, source, raw); !errors.Is(e, ErrHeaderInventory) {
		t.Fatal("undeclared key accepted", e)
	}
	unused, _ := newHeaderVerifier([]string{"primary"}, ring)
	if e = unused.finish(); !errors.Is(e, ErrHeaderInventory) {
		t.Fatal("unused declared key accepted", e)
	}
}

func TestRestoreKeyringStrictParsing(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	valid := `{"keyring_format":"ylreader-header-keyring","keyring_version":1,"current_kid":"primary","keys":{"primary":"` + encoded + `"}}`
	ring, e := parseRestoreHeaderKeyring([]byte(valid))
	if e != nil || !ring.HasKID("primary") || ring.HasKID("absent") {
		t.Fatal(e)
	}
	for name, bad := range map[string]string{
		"duplicate-key":   strings.Replace(valid, `"keys":{`, `"keys":{"primary":"`+encoded+`",`, 1),
		"duplicate-field": strings.Replace(valid, `{`, `{"keyring_version":1,`, 1),
		"unknown-field":   strings.Replace(valid, `{`, `{"password":"secret",`, 1),
		"wrong-case":      strings.Replace(valid, "current_kid", "CURRENT_KID", 1),
		"wrong-current":   strings.Replace(valid, `"current_kid":"primary"`, `"current_kid":"absent"`, 1),
		"bad-key":         strings.ReplaceAll(valid, encoded, "secret"),
		"null":            strings.Replace(valid, `"keys":{"primary":"`+encoded+`"}`, `"keys":null`, 1),
		"budget":          valid + strings.Repeat(" ", 64<<10),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseRestoreHeaderKeyring([]byte(bad)); !errors.Is(e, ErrHeaderKeyring) || strings.Contains(e.Error(), "secret") {
				t.Fatal(e)
			}
		})
	}
}
