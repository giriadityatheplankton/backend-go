package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Each key is one Redis hash so ownership (field "tok") can be checked atomically
// by Lua without decoding the payload:
//
//	tok  fencing token of the current owner
//	st   STARTED | COMPLETED
//	fp   request fingerprint
//	code / ct / hdr / body / gc / gm   replay payload (set by Complete)
var (
	acquireScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  redis.call('HSET', KEYS[1], 'tok', ARGV[1], 'st', 'STARTED', 'fp', ARGV[2])
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  return {1}
end
return {0, redis.call('HGETALL', KEYS[1])}`)

	completeScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'tok') ~= ARGV[1] or redis.call('HGET', KEYS[1], 'st') ~= 'STARTED' then
  return 0
end
redis.call('HSET', KEYS[1], 'st', 'COMPLETED', 'code', ARGV[2], 'ct', ARGV[3], 'hdr', ARGV[4], 'body', ARGV[5], 'gc', ARGV[6], 'gm', ARGV[7])
if ARGV[8] ~= '' then redis.call('HSET', KEYS[1], 'fp', ARGV[8]) end
redis.call('PEXPIRE', KEYS[1], ARGV[9])
return 1`)

	releaseScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'tok') == ARGV[1] and redis.call('HGET', KEYS[1], 'st') == 'STARTED' then
  return redis.call('DEL', KEYS[1])
end
return 0`)

	extendScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'tok') == ARGV[1] and redis.call('HGET', KEYS[1], 'st') == 'STARTED' then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0`)
)

// RedisStore is the production Store. Use a Redis dedicated to state (AOF on,
// maxmemory-policy noeviction), separate from the cache Redis.
type RedisStore struct {
	client redis.UniversalClient
	prefix string
}

// NewRedisStore creates a RedisStore. prefix namespaces keys per service
// (e.g. "orders:") so services sharing a Redis do not collide.
func NewRedisStore(client redis.UniversalClient, prefix string) *RedisStore {
	return &RedisStore{client: client, prefix: prefix}
}

func (s *RedisStore) key(k string) string { return s.prefix + "idempotency:{" + k + "}" }

func (s *RedisStore) check() error {
	if s == nil || s.client == nil {
		return errors.New("idempotency: redis client is not configured")
	}
	return nil
}

func (s *RedisStore) Acquire(ctx context.Context, key, fingerprint string, ttl time.Duration) (Lease, *Record, error) {
	if err := s.check(); err != nil {
		return Lease{}, nil, err
	}
	tok := newToken()
	res, err := acquireScript.Run(ctx, s.client, []string{s.key(key)}, tok, fingerprint, ttl.Milliseconds()).Slice()
	if err != nil {
		return Lease{}, nil, fmt.Errorf("idempotency acquire: %w", err)
	}
	if len(res) == 0 {
		return Lease{}, nil, errors.New("idempotency acquire: empty script reply")
	}
	if n, _ := res[0].(int64); n == 1 {
		return Lease{Key: key, Token: tok}, nil, nil
	}
	if len(res) < 2 {
		return Lease{}, nil, errors.New("idempotency acquire: malformed script reply")
	}
	flat, _ := res[1].([]any)
	rec, err := parseRecord(flat)
	if err != nil {
		return Lease{}, nil, err
	}
	return Lease{}, rec, nil
}

func parseRecord(flat []any) (*Record, error) {
	fields := make(map[string]string, len(flat)/2)
	for i := 0; i+1 < len(flat); i += 2 {
		k, _ := flat[i].(string)
		v, _ := flat[i+1].(string)
		fields[k] = v
	}
	rec := &Record{State: State(fields["st"]), Fingerprint: fields["fp"]}
	if rec.State != StateStarted && rec.State != StateCompleted {
		return nil, fmt.Errorf("idempotency: corrupt record state %q", rec.State)
	}
	if rec.State == StateCompleted {
		rec.StatusCode, _ = strconv.Atoi(fields["code"])
		rec.ContentType = fields["ct"]
		rec.Body = []byte(fields["body"])
		gc, _ := strconv.ParseUint(fields["gc"], 10, 32)
		rec.GRPCCode = uint32(gc)
		rec.GRPCMessage = fields["gm"]
		if h := fields["hdr"]; h != "" {
			if err := json.Unmarshal([]byte(h), &rec.Headers); err != nil {
				return nil, fmt.Errorf("idempotency: corrupt headers: %w", err)
			}
		}
	}
	return rec, nil
}

func (s *RedisStore) Complete(ctx context.Context, l Lease, rec *Record, ttl time.Duration) error {
	if err := s.check(); err != nil {
		return err
	}
	hdr, err := json.Marshal(rec.Headers)
	if err != nil {
		return fmt.Errorf("idempotency: encode headers: %w", err)
	}
	n, err := completeScript.Run(ctx, s.client, []string{s.key(l.Key)},
		l.Token, rec.StatusCode, rec.ContentType, string(hdr), rec.Body,
		rec.GRPCCode, rec.GRPCMessage, rec.Fingerprint, ttl.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("idempotency complete: %w", err)
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *RedisStore) Release(ctx context.Context, l Lease) error {
	if err := s.check(); err != nil {
		return err
	}
	n, err := releaseScript.Run(ctx, s.client, []string{s.key(l.Key)}, l.Token).Int()
	if err != nil {
		return fmt.Errorf("idempotency release: %w", err)
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *RedisStore) Extend(ctx context.Context, l Lease, ttl time.Duration) error {
	if err := s.check(); err != nil {
		return err
	}
	n, err := extendScript.Run(ctx, s.client, []string{s.key(l.Key)}, l.Token, ttl.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("idempotency extend: %w", err)
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
