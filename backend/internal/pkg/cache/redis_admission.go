package cache

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// Atomic and TTL-based: denied attempts do not extend or reserve the slot.
const domainAdmissionScript = `
local ttl = redis.call('PTTL', KEYS[1])
if ttl == -2 then
  redis.call('SET', KEYS[1], '1', 'PX', ARGV[1])
  return 0
end
if ttl < 0 then return -1 end
return math.max(1, ttl)
`

var errAdmission = errors.New("redis: domain admission unavailable")

// Take shares the Redis endpoint/authentication/database used by the cache,
// but callers must fail closed instead of treating an error as a cache miss.
func (r *Redis) Take(ctx context.Context, key string, interval time.Duration) (time.Duration, error) {
	ms := interval.Milliseconds()
	if ms < 1 || ms > 60_000 {
		return 0, errAdmission
	}
	conn, err := r.dial(ctx)
	if err != nil {
		return 0, errAdmission
	}
	defer conn.Close()
	// A canceled caller must not stay blocked until the socket timeout.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := ctx.Err(); err != nil {
		return 0, errAdmission
	}
	if err := writeCommand(conn, "EVAL", domainAdmissionScript, "1", key, strconv.FormatInt(ms, 10)); err != nil {
		return 0, errAdmission
	}
	line, err := readLine(bufReader(conn))
	if err != nil || len(line) < 2 || line[0] != ':' {
		return 0, errAdmission
	}
	n, err := strconv.ParseInt(line[1:], 10, 64)
	if err != nil || n < 0 || n > ms {
		return 0, errAdmission
	}
	return time.Duration(n) * time.Millisecond, nil
}
