package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"yreader/backend/internal/pkg/cache"
)

var errRedisIdentity = errors.New("backup: isolated Redis identity unavailable or changed")

type redisIdentityPin struct {
	RunID    string `json:"run_id"`
	Database int    `json:"database"`
}

func (p redisIdentityPin) valid() bool {
	b, e := hex.DecodeString(p.RunID)
	return e == nil && len(b) == 20 && hex.EncodeToString(b) == p.RunID && p.Database >= 0 && p.Database <= 15
}

func isolatedRedisClient(raw string) (*cache.Redis, error) {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 16384 || u.Scheme != "redis" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.RawPath != "" || len(u.Path) < 2 {
		return nil, errRedisIdentity
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 {
		return nil, errRedisIdentity
	}
	db, e := strconv.Atoi(u.Path[1:])
	if e != nil || db < 0 || db > 15 || u.Path != "/"+strconv.Itoa(db) {
		return nil, errRedisIdentity
	}
	c, e := cache.ConfiguredRedis(raw, "", time.Second)
	if e != nil || c == nil {
		return nil, errRedisIdentity
	}
	return c, nil
}

// No writes: a trusted fixture/provisioning flow must preseed the scoped key
// with distinct random values on each independently pinned Redis process.
func probeHandoverRedisPair(ctx context.Context, sourceURL, targetURL, operation string, source, target redisIdentityPin, sourceWitness, targetWitness []byte) (string, error) {
	if ctx == nil || !validUUID(operation) || !source.valid() || !target.valid() || source.RunID == target.RunID ||
		len(sourceWitness) < 32 || len(sourceWitness) > 64 || len(targetWitness) < 32 || len(targetWitness) > 64 || bytes.Equal(sourceWitness, targetWitness) {
		return "", errRedisIdentity
	}
	sc, e := isolatedRedisClient(sourceURL)
	if e != nil {
		return "", errRedisIdentity
	}
	tc, e := isolatedRedisClient(targetURL)
	if e != nil {
		return "", errRedisIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, check := range []struct {
		client  *cache.Redis
		pin     redisIdentityPin
		witness []byte
	}{{sc, source, sourceWitness}, {tc, target, targetWitness}, {sc, source, sourceWitness}, {tc, target, targetWitness}} {
		seen, e := check.client.ObserveServerWitness(ctx, "ylreader:handover:"+operation)
		matches := e == nil && seen.RunID == check.pin.RunID && seen.Database == check.pin.Database && bytes.Equal(seen.Witness, check.witness)
		clear(seen.Witness)
		if !matches {
			return "", errRedisIdentity
		}
	}
	data, _ := json.Marshal(struct {
		Format   string           `json:"format"`
		Version  int              `json:"version"`
		Identity redisIdentityPin `json:"identity"`
	}{"ylreader-redis-identity", 1, target})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
