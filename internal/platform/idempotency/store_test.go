package idempotency_test

import (
	"testing"

	"backend-go/internal/platform/idempotency"
	"backend-go/internal/platform/idempotency/storetest"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMemoryStoreContract(t *testing.T) {
	storetest.RunContract(t, func(*testing.T) idempotency.Store { return idempotency.NewMemoryStore() })
}

func TestRedisStoreContract(t *testing.T) {
	storetest.RunContract(t, func(t *testing.T) idempotency.Store {
		mr := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		// miniredis does not advance TTLs on its own; a ticker keeps wall-clock semantics.
		stop := make(chan struct{})
		t.Cleanup(func() { close(stop) })
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					mr.FastForward(10 * 1e6)
					sleep10ms()
				}
			}
		}()
		return idempotency.NewRedisStore(client, "test:")
	})
}
