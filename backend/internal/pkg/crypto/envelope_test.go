package crypto

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func testRing(t *testing.T, current string, kids ...string) *Keyring {
	t.Helper()
	keys := make(map[string][]byte, len(kids))
	for i, kid := range kids {
		key := bytes.Repeat([]byte{byte('A' + i)}, KeySize)
		keys[kid] = key
	}
	ring, err := NewKeyring(current, keys)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return ring
}

func TestEnvelopeRoundTrip(t *testing.T) {
	ring := testRing(t, "k1", "k1")
	userID, sourceID := uuid.New(), uuid.New()
	aad := AAD(userID, sourceID)

	plain := []byte(`{"Cookie":"session=abc; token=xyz","User-Agent":"yreader"}`)
	env, err := ring.Seal(plain, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(env, EnvelopePrefix+":k1:") {
		t.Fatalf("envelope prefix = %q", env)
	}
	if raw := strings.SplitN(env, ":", 3)[2]; raw == base64.StdEncoding.EncodeToString(plain) {
		t.Fatal("ciphertext must not equal plaintext")
	}

	got, err := ring.Open(env, aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: %s", got)
	}
}

func TestEnvelopeTamperRejected(t *testing.T) {
	ring := testRing(t, "k1", "k1")
	aad := AAD(uuid.New(), uuid.New())
	env, err := ring.Seal([]byte(`{"Cookie":"a"}`), aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	buf, err := base64.StdEncoding.DecodeString(strings.SplitN(env, ":", 3)[2])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	buf[len(buf)-1] ^= 0x01 // 翻转密文最后 1 bit（落在 tag 上）

	tampered := EnvelopePrefix + ":k1:" + base64.StdEncoding.EncodeToString(buf)
	if _, err := ring.Open(tampered, aad); err == nil {
		t.Fatal("tampered envelope must fail to decrypt")
	}
}

func TestEnvelopeKidRotation(t *testing.T) {
	// k1 为旧代，k2 为当前代：旧密文可解，新密文用 k2 写。
	ring := testRing(t, "k2", "k1", "k2")
	aad := AAD(uuid.New(), uuid.New())

	oldRing := testRing(t, "k1", "k1")
	oldEnv, err := oldRing.Seal([]byte(`{"Cookie":"legacy"}`), aad)
	if err != nil {
		t.Fatalf("old Seal: %v", err)
	}
	if _, err := ring.Open(oldEnv, aad); err != nil {
		t.Fatalf("old kid must still open: %v", err)
	}

	newEnv, err := ring.Seal([]byte(`{"Cookie":"fresh"}`), aad)
	if err != nil {
		t.Fatalf("new Seal: %v", err)
	}
	if !strings.HasPrefix(newEnv, EnvelopePrefix+":k2:") {
		t.Fatalf("new envelope must use current kid k2, got %q", newEnv)
	}

	// 未配置的 kid 必须显式失败。
	onlyK2 := testRing(t, "k2", "k2")
	if _, err := onlyK2.Open(oldEnv, aad); err == nil {
		t.Fatal("retired kid must not open without its key")
	}
}

func TestEnvelopeAADMismatchRejected(t *testing.T) {
	ring := testRing(t, "k1", "k1")
	userA, userB := uuid.New(), uuid.New()
	src := uuid.New()
	env, err := ring.Seal([]byte(`{"Cookie":"tenant-a"}`), AAD(userA, src))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := ring.Open(env, AAD(userB, src)); err == nil {
		t.Fatal("cross-tenant replay must fail")
	}
	if _, err := ring.Open(env, AAD(userA, uuid.New())); err == nil {
		t.Fatal("cross-source replay must fail")
	}
}

func TestKeyringValidation(t *testing.T) {
	if _, err := NewKeyring("", map[string][]byte{}); err == nil {
		t.Fatal("empty keyring must fail")
	}
	if _, err := NewKeyring("", map[string][]byte{"k1": make([]byte, 16)}); err == nil {
		t.Fatal("short key must fail")
	}
	if _, err := NewKeyring("", map[string][]byte{"k1": make([]byte, 32), "k2": make([]byte, 32)}); err == nil {
		t.Fatal("multi-key without current must fail")
	}
}
