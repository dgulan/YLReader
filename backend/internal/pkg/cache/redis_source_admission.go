package cache

import (
	"context"
	"strconv"
	"time"
)

const sourceAdmissionScript = `
local n, window, lease = tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4])
local ttl = redis.call('PTTL', KEYS[1])
if ttl == -1 then return -1 end
local mt, lt = redis.call('TYPE', KEYS[2]).ok, redis.call('TYPE', KEYS[3]).ok
if (mt ~= 'none' and mt ~= 'hash') or (lt ~= 'none' and lt ~= 'zset') then return -1 end
if mt == 'none' and lt ~= 'none' then return -1 end
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000 + math.floor(tonumber(clock[2])/1000)
local policy = ARGV[2]..'/'..ARGV[3]
local tokens, updated = n, now
if mt ~= 'none' then
  local remaining = redis.call('PTTL', KEYS[2])
  if remaining < 0 then return -1 end
  if lt ~= 'none' and redis.call('PTTL', KEYS[3]) < 0 then return -1 end
  if redis.call('HGET', KEYS[2], 'p') ~= policy then return math.max(1, remaining) end
  tokens = tonumber(redis.call('HGET', KEYS[2], 't'))
  updated = tonumber(redis.call('HGET', KEYS[2], 'u'))
  if not tokens or not updated or not (updated >= 0 and updated <= now) or not (tokens >= 0 and tokens <= n) then return -1 end
end
if redis.call('ZCARD', KEYS[3]) > 256 then return -1 end
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now)
local count = redis.call('ZCARD', KEYS[3])
local cap = 256
if window == 0 then cap = n end
if count >= cap then
  local first = redis.call('ZRANGE', KEYS[3], 0, 0, 'WITHSCORES')
  return math.max(1, tonumber(first[2])-now)
end
if window > 0 then
  tokens = math.min(n, tokens+(now-updated)*n/window)
  if tokens < 1 then return math.max(1, math.ceil((1-tokens)*window/n)) end
end
if ttl >= 0 then return math.max(1, ttl) end
local lifetime = math.max(lease, window)
redis.call('SET', KEYS[1], '1', 'PX', ARGV[5])
redis.call('ZADD', KEYS[3], now+lease, ARGV[1])
if window > 0 then tokens = tokens-1 else tokens = 0 end
redis.call('HSET', KEYS[2], 'p', policy, 't', tostring(tokens), 'u', tostring(now))
redis.call('PEXPIRE', KEYS[2], lifetime)
redis.call('PEXPIRE', KEYS[3], lifetime)
return 0
`

const sourceReleaseScript = `
local mt, lt = redis.call('TYPE', KEYS[1]).ok, redis.call('TYPE', KEYS[2]).ok
if (mt ~= 'none' and mt ~= 'hash') or (lt ~= 'none' and lt ~= 'zset') then return -1 end
redis.call('ZREM', KEYS[2], ARGV[1])
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000 + math.floor(tonumber(clock[2])/1000)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
local p = redis.call('HGET', KEYS[1], 'p')
if p and string.sub(p, -2) == '/0' and redis.call('ZCARD', KEYS[2]) == 0 then
  redis.call('DEL', KEYS[1], KEYS[2])
end
return 0
`

// admissionEval only accepts bounded integer replies and suppresses server details.
func (r *Redis) admissionEval(ctx context.Context, args []string, max int64) (time.Duration, error) {
	conn, err := r.dial(ctx)
	if err != nil {
		return 0, errAdmission
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if ctx.Err() != nil {
		return 0, errAdmission
	}
	if err := writeCommand(conn, args...); err != nil {
		return 0, errAdmission
	}
	line, err := readLine(bufReader(conn))
	if err != nil || len(line) < 2 || line[0] != ':' {
		return 0, errAdmission
	}
	n, err := strconv.ParseInt(line[1:], 10, 64)
	if err != nil || n < 0 || n > max {
		return 0, errAdmission
	}
	return time.Duration(n) * time.Millisecond, nil
}

func (r *Redis) TakeSource(ctx context.Context, domain, source, token string, limit int, window, lease time.Duration) (time.Duration, error) {
	if limit < 1 || limit > 256 || window < 0 || window > 600000*time.Millisecond || window%time.Millisecond != 0 || lease < time.Millisecond || lease > time.Minute || lease%time.Millisecond != 0 {
		return 0, errAdmission
	}
	args := []string{"EVAL", sourceAdmissionScript, "3", domain, source + ":meta", source + ":leases", token,
		strconv.Itoa(limit), strconv.FormatInt(window.Milliseconds(), 10), strconv.FormatInt(lease.Milliseconds(), 10), "500"}
	return r.admissionEval(ctx, args, max(lease, window).Milliseconds())
}

func (r *Redis) ReleaseSource(ctx context.Context, source, token string) error {
	_, err := r.admissionEval(ctx, []string{"EVAL", sourceReleaseScript, "2", source + ":meta", source + ":leases", token}, 0)
	return err
}
