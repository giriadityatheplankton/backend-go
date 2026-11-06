// Package storetest holds the behavioural contract every idempotency.Store must
// satisfy (Liskov): run it against the memory store and the Redis store.
package storetest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/pkg/idempotency"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunContract exercises the Store contract. newStore must return an empty store.
func RunContract(t *testing.T, newStore func(t *testing.T) idempotency.Store) {
	ctx := context.Background()
	ttl := time.Minute

	t.Run("exactly one concurrent acquirer wins", func(t *testing.T) {
		s := newStore(t)
		var winners int32
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				l, existing, err := s.Acquire(ctx, "k", "fp", ttl)
				require.NoError(t, err)
				if existing == nil {
					assert.NotEmpty(t, l.Token)
					atomic.AddInt32(&winners, 1)
				} else {
					assert.Equal(t, idempotency.StateStarted, existing.State)
				}
			}()
		}
		wg.Wait()
		assert.Equal(t, int32(1), winners)
	})

	t.Run("complete then replay with identical payload", func(t *testing.T) {
		s := newStore(t)
		l, _, err := s.Acquire(ctx, "k", "fp", ttl)
		require.NoError(t, err)
		rec := &idempotency.Record{StatusCode: 201, ContentType: "application/json", Headers: map[string]string{"Location": "/o/1"}, Body: []byte{0, 1, 2, 255}, GRPCCode: 5, GRPCMessage: "m"}
		require.NoError(t, s.Complete(ctx, l, rec, ttl))

		_, got, err := s.Acquire(ctx, "k", "fp", ttl)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, idempotency.StateCompleted, got.State)
		assert.Equal(t, "fp", got.Fingerprint)
		assert.Equal(t, 201, got.StatusCode)
		assert.Equal(t, "application/json", got.ContentType)
		assert.Equal(t, map[string]string{"Location": "/o/1"}, got.Headers)
		assert.Equal(t, []byte{0, 1, 2, 255}, got.Body)
		assert.Equal(t, uint32(5), got.GRPCCode)
		assert.Equal(t, "m", got.GRPCMessage)
	})

	t.Run("stored record is not aliased", func(t *testing.T) {
		s := newStore(t)
		l, _, _ := s.Acquire(ctx, "k", "fp", ttl)
		rec := &idempotency.Record{StatusCode: 200, Body: []byte("abc"), Headers: map[string]string{"A": "1"}}
		require.NoError(t, s.Complete(ctx, l, rec, ttl))
		rec.Body[0] = 'X'
		rec.Headers["A"] = "2"
		_, got, _ := s.Acquire(ctx, "k", "fp", ttl)
		assert.Equal(t, []byte("abc"), got.Body)
		assert.Equal(t, "1", got.Headers["A"])
	})

	t.Run("complete by non-owner returns ErrLeaseLost", func(t *testing.T) {
		s := newStore(t)
		l, _, _ := s.Acquire(ctx, "k", "fp", ttl)
		err := s.Complete(ctx, idempotency.Lease{Key: "k", Token: "someone-else"}, &idempotency.Record{StatusCode: 200}, ttl)
		assert.ErrorIs(t, err, idempotency.ErrLeaseLost)
		_, got, _ := s.Acquire(ctx, "k", "fp", ttl)
		assert.Equal(t, idempotency.StateStarted, got.State, "foreign Complete must not change state")
		assert.NoError(t, s.Release(ctx, l))
	})

	t.Run("release by non-owner keeps the lease", func(t *testing.T) {
		s := newStore(t)
		_, _, _ = s.Acquire(ctx, "k", "fp", ttl)
		assert.ErrorIs(t, s.Release(ctx, idempotency.Lease{Key: "k", Token: "someone-else"}), idempotency.ErrLeaseLost)
		_, got, _ := s.Acquire(ctx, "k", "fp", ttl)
		require.NotNil(t, got, "key must still be locked")
	})

	t.Run("release lets the next caller acquire", func(t *testing.T) {
		s := newStore(t)
		l, _, _ := s.Acquire(ctx, "k", "fp", ttl)
		require.NoError(t, s.Release(ctx, l))
		l2, existing, err := s.Acquire(ctx, "k", "fp", ttl)
		require.NoError(t, err)
		assert.Nil(t, existing)
		assert.NotEqual(t, l.Token, l2.Token)
	})

	t.Run("expired lease cannot overwrite or delete the new owner", func(t *testing.T) {
		s := newStore(t)
		a, _, _ := s.Acquire(ctx, "k", "fp", 50*time.Millisecond)
		time.Sleep(120 * time.Millisecond)
		b, existing, err := s.Acquire(ctx, "k", "fp", ttl)
		require.NoError(t, err)
		require.Nil(t, existing, "lease A expired so B acquires")

		assert.ErrorIs(t, s.Complete(ctx, a, &idempotency.Record{StatusCode: 200}, ttl), idempotency.ErrLeaseLost)
		assert.ErrorIs(t, s.Release(ctx, a), idempotency.ErrLeaseLost)
		_, got, _ := s.Acquire(ctx, "k", "fp", ttl)
		require.NotNil(t, got, "B's lease must survive A's late calls")
		assert.NoError(t, s.Complete(ctx, b, &idempotency.Record{StatusCode: 201}, ttl))
	})

	t.Run("extend keeps the lease alive only for the owner", func(t *testing.T) {
		s := newStore(t)
		l, _, _ := s.Acquire(ctx, "k", "fp", 100*time.Millisecond)
		assert.ErrorIs(t, s.Extend(ctx, idempotency.Lease{Key: "k", Token: "x"}, ttl), idempotency.ErrLeaseLost)
		require.NoError(t, s.Extend(ctx, l, ttl))
		time.Sleep(200 * time.Millisecond)
		_, got, _ := s.Acquire(ctx, "k", "fp", ttl)
		require.NotNil(t, got, "extended lease must still be held")
	})

	t.Run("completed record expires", func(t *testing.T) {
		s := newStore(t)
		l, _, _ := s.Acquire(ctx, "k", "fp", ttl)
		require.NoError(t, s.Complete(ctx, l, &idempotency.Record{StatusCode: 200}, 80*time.Millisecond))
		time.Sleep(160 * time.Millisecond)
		_, existing, err := s.Acquire(ctx, "k", "fp", ttl)
		require.NoError(t, err)
		assert.Nil(t, existing)
	})
}
