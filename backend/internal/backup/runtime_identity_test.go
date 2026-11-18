package backup

import (
	"bytes"
	"context"
	"strings"
	"testing"

	headercrypto "yreader/backend/internal/pkg/crypto"
)

func runtimeFixture() runtimeConfigBinding {
	return runtimeConfigBinding{
		"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003", "00000000-0000-4000-8000-000000000004", "primary"}
}

func TestRuntimeIdentityAssemblyAndNonsecretFingerprint(t *testing.T) {
	r := runtimeFixture()
	ring := headerRing(t, "primary", 1)
	source, target := bytes.Repeat([]byte("a"), 32), bytes.Repeat([]byte("b"), 32)
	before := append([]byte(nil), target...)
	h, e := probeHandoverRuntime(r, r, source, target, ring, []string{"primary"})
	if e != nil || !validSHA(h) {
		t.Fatal(e)
	}
	// Random self-test output and different secret bytes do not enter metadata digest.
	next, e := probeHandoverRuntime(r, r, source, bytes.Repeat([]byte("c"), 32), ring, []string{"primary"})
	if e != nil || h != next {
		t.Fatal("secret/self-test affected nonsecret revision digest")
	}
	if !bytes.Equal(before, target) {
		t.Fatal("caller secret modified")
	}
	r.ConfigRevision = "00000000-0000-4000-8000-000000000005"
	next, e = probeHandoverRuntime(r, r, source, target, ring, []string{"primary"})
	if e != nil || next == h {
		t.Fatal("config revision not bound")
	}
}

func TestRuntimeIdentityRejectsUnsafeAssembly(t *testing.T) {
	for _, name := range []string{"same_jwt", "short_jwt", "large_jwt", "metadata_drift", "invalid_revision", "missing_kid", "duplicate_kid", "wrong_current", "nil_ring"} {
		t.Run(name, func(t *testing.T) {
			r, actual := runtimeFixture(), runtimeFixture()
			source, target := bytes.Repeat([]byte("a"), 32), bytes.Repeat([]byte("b"), 32)
			var ring *headercrypto.Keyring = headerRing(t, "primary", 1)
			kids := []string{"primary"}
			switch name {
			case "same_jwt":
				target = source
			case "short_jwt":
				target = target[:31]
			case "large_jwt":
				target = bytes.Repeat([]byte("b"), 4097)
			case "metadata_drift":
				actual.JWTRevision = "00000000-0000-4000-8000-000000000099"
			case "invalid_revision":
				r.KeyringRevision = "SECRET"
				actual = r
			case "missing_kid":
				kids = []string{"missing"}
			case "duplicate_kid":
				kids = []string{"primary", "primary"}
			case "wrong_current":
				r.CurrentKID = "old"
				actual = r
			case "nil_ring":
				ring = nil
			}
			if h, e := probeHandoverRuntime(r, actual, source, target, ring, kids); h != "" || e != errRuntimeIdentity {
				t.Fatal("unsafe assembly or sensitive error escaped")
			}
		})
	}
}

func TestRedisIdentityIsolationInputs(t *testing.T) {
	for _, raw := range []string{"redis://localhost:6379/0", "redis://172.16.1.18:6379/0", "rediss://127.0.0.1:6379/0", "redis://127.0.0.1/0", "redis://127.0.0.1:6379/00", "redis://127.0.0.1:6379/16", "redis://127.0.0.1:6379/0?", "redis://127.0.0.1:6379/0#SECRET"} {
		if _, e := isolatedRedisClient(raw); e != errRedisIdentity {
			t.Fatal("unscoped Redis endpoint accepted")
		}
	}
	p := redisIdentityPin{strings.Repeat("a", 40), 0}
	w := bytes.Repeat([]byte("a"), 32)
	if h, e := probeHandoverRedisPair(context.Background(), "", "", "00000000-0000-4000-8000-000000000001", p, p, w, bytes.Repeat([]byte("b"), 32)); h != "" || e != errRedisIdentity {
		t.Fatal("same process accepted")
	}
	q := redisIdentityPin{strings.Repeat("b", 40), 0}
	if h, e := probeHandoverRedisPair(context.Background(), "", "", "../secret", p, q, w, w); h != "" || e != errRedisIdentity {
		t.Fatal("bad scope accepted")
	}
}
