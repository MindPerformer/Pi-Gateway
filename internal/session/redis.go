package session

import (
	"bytes"
	"context"
	"crypto/tls"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisOptions struct {
	URL      string
	Password string
	TLS      bool
	Options  Options
}

// Redis is an optional cache, never an authority for live connection ownership.
// A namespace has a single bounded expiry index; all its keys share a cluster
// hash tag so the CAS scripts remain single-slot operations.
type Redis struct {
	client  redis.UniversalClient
	options Options
	prefix  string
	owned   bool
	closed  atomic.Bool
}

var _ Store = (*Redis)(nil)

func NewRedis(options RedisOptions) (*Redis, error) {
	parsed, err := redis.ParseURL(options.URL)
	if err != nil || (!strings.HasPrefix(options.URL, "redis://") && !strings.HasPrefix(options.URL, "rediss://")) {
		return nil, ErrInvalid
	}
	o := options.Options.normalized()
	if options.Password != "" {
		parsed.Password = options.Password
	}
	if options.TLS && parsed.TLSConfig == nil {
		parsed.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if parsed.TLSConfig != nil {
		parsed.TLSConfig.MinVersion = tls.VersionTLS12
	}
	// Bound dialing, connection-pool waits and every read/write; disable retries
	// so a cache failure never stalls a successfully completed provider response.
	parsed.DialTimeout = o.CommitTimeout
	parsed.ReadTimeout = o.CommitTimeout
	parsed.WriteTimeout = o.CommitTimeout
	parsed.PoolTimeout = o.CommitTimeout
	parsed.ContextTimeoutEnabled = true
	parsed.MaxRetries = -1
	parsed.PoolSize = 8
	parsed.MinIdleConns = 0
	parsed.MaxIdleConns = 8
	parsed.MaxActiveConns = 8
	r, err := NewRedisWithClient(redis.NewClient(parsed), o)
	if err != nil {
		return nil, err
	}
	r.owned = true
	return r, nil
}

// NewRedisWithClient does not own the supplied client's lifecycle. The client
// must honor context deadlines (ContextTimeoutEnabled for go-redis clients).
func NewRedisWithClient(client redis.UniversalClient, options Options) (*Redis, error) {
	if client == nil {
		return nil, ErrInvalid
	}
	o := options.normalized()
	// Hashing the namespace inside the hash tag prevents client-controlled braces
	// in identifiers from changing Redis cluster slot selection.
	prefix := o.Namespace + ":{" + Fingerprint("namespace", o.Namespace) + "}:v1:"
	return &Redis{client: client, options: o, prefix: prefix}, nil
}

func (r *Redis) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	if r.owned {
		return r.client.Close()
	}
	return nil
}

func (r *Redis) key(responseID string) string { return r.prefix + responseKey(responseID) }
func (r *Redis) indexKey() string             { return r.prefix + "expiry" }
func (r *Redis) sizeKey() string              { return r.prefix + "sizes" }
func (r *Redis) keys(key string) []string     { return []string{key, r.indexKey(), r.sizeKey()} }

func (r *Redis) context(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, unavailable(err)
	}
	if r.closed.Load() {
		return nil, nil, ErrUnavailable
	}
	child, cancel := context.WithTimeout(ctx, r.options.CommitTimeout)
	return child, cancel, nil
}

// Never download an arbitrarily large/corrupt Redis value before size checking.
var readScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {0, ''} end
if redis.call('TYPE', KEYS[1]).ok ~= 'string' then return {3, ''} end
if redis.call('STRLEN', KEYS[1]) > tonumber(ARGV[1]) then return {3, ''} end
return {1, redis.call('GET', KEYS[1])}
`)

// The expected raw value fences the entire owner/schema/version, avoiding Lua
// floating-point conversion of int64 key/account IDs and uint64 CAS versions.
var writeScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if (current or '') ~= ARGV[1] then return 3 end
local tm = redis.call('TIME')
local now = tonumber(tm[1]) * 1000 + math.floor(tonumber(tm[2]) / 1000)
local total = tonumber(redis.call('HGET', KEYS[3], '_total') or '0')
for _, expired in ipairs(redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now)) do
  total = total - tonumber(redis.call('HGET', KEYS[3], expired) or '0')
  redis.call('HDEL', KEYS[3], expired)
end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
if redis.call('ZCARD', KEYS[2]) == 0 then
  redis.call('DEL', KEYS[2], KEYS[3])
  total = 0
else
  redis.call('HSET', KEYS[3], '_total', total)
end
local oldsize = tonumber(redis.call('HGET', KEYS[3], KEYS[1]) or '0')
local nextsize = total - oldsize + string.len(ARGV[2])
if not current and redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[4]) then return 4 end
if nextsize > tonumber(ARGV[5]) then return 4 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[3]), KEYS[1])
redis.call('HSET', KEYS[3], KEYS[1], string.len(ARGV[2]), '_total', nextsize)
for i = 2, 3 do
  if redis.call('PTTL', KEYS[i]) < tonumber(ARGV[3]) then redis.call('PEXPIRE', KEYS[i], ARGV[3]) end
end
return 1
`)

var deleteScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then return 0 end
if current ~= ARGV[1] then return 3 end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], KEYS[1])
local size = tonumber(redis.call('HGET', KEYS[3], KEYS[1]) or '0')
redis.call('HDEL', KEYS[3], KEYS[1])
redis.call('HINCRBY', KEYS[3], '_total', -size)
if redis.call('ZCARD', KEYS[2]) == 0 then redis.call('DEL', KEYS[2], KEYS[3]) end
return 1
`)

func scriptStatus(n int64, err error) error {
	if err != nil {
		return unavailable(err)
	}
	switch n {
	case 0:
		return ErrMiss
	case 1:
		return nil
	case 2:
		return ErrWrongOwner
	case 3:
		return ErrInvalid
	default:
		return ErrUnavailable
	}
}

func (r *Redis) read(ctx context.Context, key string) ([]byte, error) {
	values, err := readScript.Run(ctx, r.client, []string{key}, r.options.MaxRecordBytes).Slice()
	if err != nil {
		return nil, unavailable(err)
	}
	if len(values) != 2 {
		return nil, ErrInvalid
	}
	status, ok := values[0].(int64)
	if !ok {
		return nil, ErrInvalid
	}
	if err := scriptStatus(status, nil); err != nil {
		return nil, err
	}
	value, ok := values[1].(string)
	if !ok {
		return nil, ErrInvalid
	}
	return []byte(value), nil
}

func (r *Redis) Resolve(ctx context.Context, keyID int64, responseID string) (*Record, error) {
	if !validReference(keyID, responseID) {
		return nil, ErrInvalid
	}
	ctx, cancel, err := r.context(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	payload, err := r.read(ctx, r.key(responseID))
	if err != nil {
		return nil, err
	}
	record, err := decode(payload, responseID, r.options, time.Now())
	if err != nil {
		return nil, err
	}
	if record.KeyID != keyID {
		return nil, ErrWrongOwner
	}
	return record, nil
}

func (r *Redis) RecordCompleted(ctx context.Context, record Record) error {
	if !validReference(record.KeyID, record.ResponseID) || len(record.OpaqueState) > r.options.MaxRecordBytes {
		return ErrInvalid
	}
	ctx, cancel, err := r.context(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	key := r.key(record.ResponseID)
	raw, err := r.read(ctx, key)
	var old *Record
	if err == nil {
		old, err = decode(raw, record.ResponseID, r.options, time.Now())
		if err != nil {
			return err
		}
	} else if err != ErrMiss {
		return err
	}
	_, payload, err := prepare(record, old, r.options, time.Now())
	if err != nil {
		return err
	}
	if bytes.Equal(payload, raw) {
		return nil
	}
	result, err := writeScript.Run(ctx, r.client, r.keys(key), string(raw), string(payload), r.options.TTL.Milliseconds(), r.options.MaxEntries, r.options.MaxBytes).Int64()
	return scriptStatus(result, err)
}

func (r *Redis) Invalidate(ctx context.Context, request Invalidation) error {
	if !validInvalidation(request) {
		return ErrInvalid
	}
	ctx, cancel, err := r.context(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	key := r.key(request.ResponseID)
	raw, err := r.read(ctx, key)
	if err != nil {
		return err
	}
	record, err := decode(raw, request.ResponseID, r.options, time.Now())
	if err != nil {
		return err
	}
	if err := checkInvalidation(*record, request); err != nil {
		return err
	}
	result, err := deleteScript.Run(ctx, r.client, r.keys(key), string(raw)).Int64()
	return scriptStatus(result, err)
}
