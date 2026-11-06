// Package idempotency implements layer 1 (edge) of the three-layer idempotency
// design: it de-duplicates client retries and concurrent duplicates for HTTP and
// gRPC using a fenced lease stored in Redis.
//
// It is an optimisation and a fast path, NOT the source of truth. Redis can lose
// keys (eviction, async-replication failover, FLUSHALL), so every state-writing
// use case must also enforce idempotency in the database (UNIQUE key +
// fingerprint, layer 2) and every consumer must de-duplicate with the inbox
// (layer 3).
package idempotency

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	// ErrLeaseLost means the lease expired or was taken over; the caller no longer owns the key.
	ErrLeaseLost = errors.New("idempotency lease lost")
	// ErrInvalidKey means the client supplied a malformed idempotency key.
	ErrInvalidKey = errors.New("invalid idempotency key")
)

const (
	// HeaderIdempotencyKey is the standard header (draft-ietf-httpapi-idempotency-key-header).
	HeaderIdempotencyKey = "Idempotency-Key"
	// HeaderLegacyIdempotencyKey is still accepted for backward compatibility.
	HeaderLegacyIdempotencyKey = "X-Idempotency-Key"
	// HeaderReplayed is set to "true" on responses served from the idempotency cache.
	HeaderReplayed = "Idempotent-Replayed"

	// MetadataIdempotencyKey is the gRPC metadata key.
	MetadataIdempotencyKey = "idempotency-key"
	// MetadataLegacyIdempotencyKey is the legacy gRPC metadata key.
	MetadataLegacyIdempotencyKey = "x-idempotency-key"

	DefaultInFlightTTL  = 30 * time.Second
	DefaultCompletedTTL = 24 * time.Hour
	DefaultMaxBodyBytes = 1 << 20
	// persistTimeout bounds Complete/Release, which run on a context detached from the request.
	persistTimeout = 2 * time.Second
)

var keyPattern = regexp.MustCompile(`^[\x21-\x7E]{1,128}$`)

// ValidKey reports whether key is 1-128 printable, non-space ASCII characters.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// State is the lifecycle of an idempotency record.
type State string

const (
	StateStarted   State = "STARTED"
	StateCompleted State = "COMPLETED"
)

// Record is the stored idempotency state for one key.
type Record struct {
	State       State
	Fingerprint string

	// HTTP replay data.
	StatusCode  int
	ContentType string
	Headers     map[string]string
	Body        []byte // HTTP body, or a marshalled anypb.Any for gRPC success

	// gRPC deterministic-error replay data (GRPCCode != 0 means "replay this error").
	GRPCCode    uint32
	GRPCMessage string
}

// Clone returns a deep copy so stores never alias caller memory.
func (r *Record) Clone() *Record {
	if r == nil {
		return nil
	}
	cp := *r
	if r.Headers != nil {
		cp.Headers = make(map[string]string, len(r.Headers))
		for k, v := range r.Headers {
			cp.Headers[k] = v
		}
	}
	cp.Body = append([]byte(nil), r.Body...)
	return &cp
}

// Lease proves ownership of an in-flight key. Token is the fencing token.
type Lease struct {
	Key   string
	Token string
}

// Store persists idempotency state. Contract (verified by storetest.RunContract):
//
//   - Acquire succeeds for exactly one concurrent caller per key. Losers get the
//     existing Record (State STARTED or COMPLETED) and a zero Lease.
//   - Complete, Release and Extend only take effect for the current lease owner;
//     otherwise they return ErrLeaseLost and change nothing.
//   - Records are copied; mutating a Record after Complete never changes stored data.
//   - A store never "fails open" by itself: connectivity problems are returned as
//     errors and the middleware applies the configured FailureMode.
type Store interface {
	Acquire(ctx context.Context, key, fingerprint string, inFlightTTL time.Duration) (Lease, *Record, error)
	Complete(ctx context.Context, lease Lease, rec *Record, completedTTL time.Duration) error
	Release(ctx context.Context, lease Lease) error
	Extend(ctx context.Context, lease Lease, inFlightTTL time.Duration) error
}

// FailureMode decides what happens when the Store is unreachable.
type FailureMode int

const (
	// FailClosed rejects the request (503 / UNAVAILABLE). Required for money and stock.
	FailClosed FailureMode = iota
	// FailOpen runs the handler without edge dedupe; layer 2 (DB) must protect the write.
	FailOpen
)

// ParseFailureMode converts "closed"/"open" to a FailureMode.
func ParseFailureMode(s string) (FailureMode, error) {
	switch s {
	case "", "closed":
		return FailClosed, nil
	case "open":
		return FailOpen, nil
	}
	return FailClosed, errors.New(`failure mode must be "closed" or "open"`)
}

// Observer receives decisions for metrics. metrics.Metrics implements it; nil is allowed.
type Observer interface {
	IncIdempotency(result string)
}

// Result labels reported to Observer.
const (
	ResultAcquired   = "acquired"
	ResultReplayed   = "replayed"
	ResultInProgress = "in_progress"
	ResultMismatch   = "mismatch"
	ResultLeaseLost  = "lease_lost"
	ResultStoreError = "store_error"
	ResultBypassed   = "bypassed"
	ResultReleased   = "released"
)

func observe(o Observer, result string) {
	if o != nil {
		o.IncIdempotency(result)
	}
}

// persistContext detaches from request cancellation so a result is still saved
// after the client disconnects or the request deadline passes.
func persistContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), persistTimeout)
}

// heartbeat extends the lease every ttl/3 until the returned stop func is called,
// so a slow handler does not lose its lease. A lost lease stops the heartbeat.
func heartbeat(ctx context.Context, store Store, lease Lease, ttl time.Duration, obs Observer) (stop func()) {
	if ttl < 3*time.Millisecond {
		return func() {}
	}
	hbCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				callCtx, c := context.WithTimeout(hbCtx, persistTimeout)
				err := store.Extend(callCtx, lease, ttl)
				c()
				if errors.Is(err, ErrLeaseLost) {
					observe(obs, ResultLeaseLost)
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

type keyCtx struct{}

// WithKey stores the client's idempotency key in ctx so outbound calls can derive
// their own keys from it (see DerivedKey).
func WithKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyCtx{}, key)
}

// KeyFrom returns the client's idempotency key from ctx, or "".
func KeyFrom(ctx context.Context) string {
	k, _ := ctx.Value(keyCtx{}).(string)
	return k
}

// DerivedKey returns "<clientKey>:<suffix>" for a downstream call, or "" if ctx has
// no key. Calling the same dependency twice for the same client request then
// carries the same key, so retries chain safely instead of double-charging.
func DerivedKey(ctx context.Context, suffix string) string {
	k := KeyFrom(ctx)
	if k == "" {
		return ""
	}
	return k + ":" + suffix
}
