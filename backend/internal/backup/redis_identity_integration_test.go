//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/pkg/cache"
)

func TestHandoverRedisIdentityIsolated(t *testing.T) {
	if os.Getenv("BACKUP_REDIS_IDENTITY_ISOLATED") != "1" {
		t.Skip("dedicated Redis identity pair required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const sourceURL = "redis://127.0.0.1:16379/0"
	const targetURL = "redis://127.0.0.1:16380/0"
	const operation = "00000000-0000-4000-8000-000000000001"
	key := "ylreader:handover:" + operation
	source := redisIdentityPin{os.Getenv("IDENTITY_SOURCE_RUN_ID"), 0}
	target := redisIdentityPin{os.Getenv("IDENTITY_TARGET_RUN_ID"), 0}
	if !source.valid() || !target.valid() || source.RunID == target.RunID {
		t.Fatal("harness pins required")
	}
	sc, e := cache.ConfiguredRedis(sourceURL, "", time.Second)
	if e != nil {
		t.Fatal("source fixture config")
	}
	tc, e := cache.ConfiguredRedis(targetURL, "", time.Second)
	if e != nil {
		t.Fatal("target fixture config")
	}
	sw, tw := make([]byte, 32), make([]byte, 32)
	defer clear(sw)
	defer clear(tw)
	if _, e = rand.Read(sw); e != nil {
		t.Fatal("fixture entropy")
	}
	if _, e = rand.Read(tw); e != nil {
		t.Fatal("fixture entropy")
	}
	if bytes.Equal(sw, tw) {
		t.Fatal("fixture collision")
	}
	for _, pair := range []struct {
		c *cache.Redis
		w []byte
	}{{sc, sw}, {tc, tw}} {
		if pair.c.SetEX(ctx, key, pair.w, 2*time.Minute) != nil || pair.c.SetEX(ctx, "identity-sentinel", []byte("中文 😀 retained"), 2*time.Minute) != nil {
			t.Fatal("fixture seeding failed")
		}
	}
	ttlBefore, e := tc.TTL(ctx, key)
	if e != nil {
		t.Fatal("fixture TTL")
	}
	deny := func(t *testing.T, surl, turl string, s, p redisIdentityPin, a, b []byte) {
		t.Helper()
		if h, e := probeHandoverRedisPair(ctx, surl, turl, operation, s, p, a, b); h != "" || e != errRedisIdentity {
			t.Fatal("unsafe Redis target accepted")
		}
	}
	hash := ""
	t.Run("success_readonly", func(t *testing.T) {
		h, e := probeHandoverRedisPair(ctx, sourceURL, targetURL, operation, source, target, sw, tw)
		if e != nil || !validSHA(h) {
			t.Fatal("isolated pair denied", e)
		}
		hash = h
	})
	t.Run("endpoint_alias", func(t *testing.T) { deny(t, sourceURL, sourceURL, source, target, sw, tw) })
	t.Run("same_process_other_db", func(t *testing.T) {
		p := source
		p.Database = 1
		deny(t, sourceURL, "redis://127.0.0.1:16379/1", source, p, sw, tw)
	})
	t.Run("run_id_drift", func(t *testing.T) {
		p := target
		p.RunID = strings.Repeat("0", 40)
		deny(t, sourceURL, targetURL, source, p, sw, tw)
	})
	t.Run("database_drift", func(t *testing.T) { p := target; p.Database = 1; deny(t, sourceURL, targetURL, source, p, sw, tw) })
	t.Run("wrong_witness", func(t *testing.T) { deny(t, sourceURL, targetURL, source, target, sw, bytes.Repeat([]byte("x"), 32)) })
	t.Run("same_witness", func(t *testing.T) { deny(t, sourceURL, targetURL, source, target, sw, sw) })
	t.Run("missing_witness", func(t *testing.T) {
		if h, e := probeHandoverRedisPair(ctx, sourceURL, targetURL, "00000000-0000-4000-8000-000000000099", source, target, sw, tw); h != "" || e != errRedisIdentity {
			t.Fatal("missing witness accepted")
		}
	})
	t.Run("acl_denied", func(t *testing.T) {
		deny(t, sourceURL, "redis://identity_ro:synthetic-only@127.0.0.1:16380/0", source, target, sw, tw)
	})
	t.Run("cancel", func(t *testing.T) {
		c, stop := context.WithCancel(ctx)
		stop()
		if h, e := probeHandoverRedisPair(c, sourceURL, targetURL, operation, source, target, sw, tw); h != "" || e != errRedisIdentity {
			t.Fatal("canceled observation accepted")
		}
	})
	t.Run("retained_witnesses_and_sentinels", func(t *testing.T) {
		for _, pair := range []struct {
			c *cache.Redis
			w []byte
		}{{sc, sw}, {tc, tw}} {
			got, e := pair.c.Get(ctx, key)
			if e != nil || !bytes.Equal(got, pair.w) {
				t.Fatal("witness changed")
			}
			clear(got)
			got, e = pair.c.Get(ctx, "identity-sentinel")
			if e != nil || string(got) != "中文 😀 retained" {
				t.Fatal("other key changed")
			}
		}
		after, e := tc.TTL(ctx, key)
		if e != nil || after <= 0 || after > ttlBefore {
			t.Fatal("witness TTL renewed or lost")
		}
	})
	if t.Failed() {
		return
	}
	if hash == "" {
		t.Fatal("no successful observation")
	}
	evidence, _ := json.Marshal(struct {
		Scope      string `json:"scope"`
		Version    int    `json:"version"`
		RedisSHA   string `json:"redis_identity_sha256"`
		Retained   bool   `json:"witnesses_and_sentinels_retained"`
		Deployment bool   `json:"deployment_authorized"`
		Ready      bool   `json:"ready_for_cutover"`
	}{"isolated-redis-identity-fixture", 1, hash, true, false, false})
	t.Log("REDIS_IDENTITY_EVIDENCE " + string(evidence))
}
