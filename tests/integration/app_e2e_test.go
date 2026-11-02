//go:build integration && examples

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	orderv1 "backend-go/gen/proto/order/v1"
	"backend-go/internal/app"
	"backend-go/internal/config"
	"backend-go/internal/platform/grpcclient"
	"backend-go/internal/platform/observability/requestid"
	"backend-go/migrations"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const token = "tok-integration-0123456789abcdef"

type env struct {
	httpURL, adminURL, grpcAddr string
	db                          *sql.DB
	rdb                         *redis.Client
	stop                        func()
}

func startApp(t *testing.T) *env {
	t.Helper()
	db, dsn := migrated(t, migrations.Sources()...)
	addr := redisAddr(t)
	e := &env{db: db, httpURL: port(t), adminURL: port(t), grpcAddr: port(t)}
	for k, v := range map[string]string{
		"APP_ENV": "test", "SERVICE_NAME": "e2e", "SERVER_ADDRESS": e.httpURL, "ADMIN_ADDRESS": e.adminURL, "GRPC_ADDRESS": e.grpcAddr,
		"DB_PRIMARY_DSN": dsn, "REDIS_ADDRESSES": addr, "STREAM_PREFIX": "t:",
		"AUTH_MODE": "static", "AUTH_STATIC_TOKENS": token + ";svc-1;tenant-1;orders:write,orders:read,users:read",
		"OUTBOX_PARTITIONS": "2", "OUTBOX_POLL_INTERVAL": "20ms", "CONSUMER_CLAIM_IDLE": "300ms",
		"DRAIN_DELAY": "0s", "PRESTOP_DELAY": "0s", "SHUTDOWN_TIMEOUT": "5s", "PPROF_ENABLED": "false",
		"RATE_LIMIT_REQUESTS": "1000", "GRPC_REFLECTION": "true",
	} {
		t.Setenv(k, v)
	}
	cfg, err := config.FromEnv()
	require.NoError(t, err)

	a, err := app.New(context.Background(), cfg, app.RoleAll)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	e.stop = func() {
		a.Shutdown()
		select {
		case err := <-done:
			assert.NoError(t, err, "graceful shutdown must be clean")
		case <-time.After(15 * time.Second):
			t.Error("shutdown did not finish")
		}
	}
	t.Cleanup(func() {})
	e.rdb = redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = e.rdb.Close() })

	require.Eventually(t, func() bool {
		r, err := http.Get("http://" + e.adminURL + "/healthz/ready")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	}, 10*time.Second, 50*time.Millisecond, "service never became ready")
	return e
}

func (e *env) do(t *testing.T, method, path, key, body string, bearer string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+e.httpURL+path, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

func data(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }

func TestEndToEnd_HTTP_GRPC_Outbox_Inbox(t *testing.T) {
	e := startApp(t)
	ctx := context.Background()
	body := `{"customer_id":"c-1","amount_minor":15000,"currency":"IDR"}`

	t.Run("auth is enforced and unknown fields rejected", func(t *testing.T) {
		resp, _ := e.do(t, "POST", "/v1/orders", "k0", body, "")
		assert.Equal(t, 401, resp.StatusCode)
		resp, _ = e.do(t, "POST", "/v1/orders", "k0", body, "wrong-token")
		assert.Equal(t, 401, resp.StatusCode)
		resp, out := e.do(t, "POST", "/v1/orders", "k0", `{"customer_id":"c","amount_minor":1,"currency":"IDR","evil":1}`, token)
		assert.Equal(t, 400, resp.StatusCode, "%v", out)
		resp, _ = e.do(t, "POST", "/v1/orders", "", body, token)
		assert.Equal(t, 400, resp.StatusCode, "idempotency key is required on create")
	})

	var orderID string
	t.Run("create is idempotent at the edge (Redis) and in the DB", func(t *testing.T) {
		resp, out := e.do(t, "POST", "/v1/orders", "key-1", body, token)
		require.Equal(t, 201, resp.StatusCode, "%v", out)
		orderID = data(out)["id"].(string)
		assert.NotEmpty(t, resp.Header.Get(requestid.Header))
		assert.Equal(t, "/v1/orders/"+orderID, resp.Header.Get("Location"))

		resp, out = e.do(t, "POST", "/v1/orders", "key-1", body, token)
		assert.Equal(t, 201, resp.StatusCode, "edge replay returns the original status")
		assert.Equal(t, "true", resp.Header.Get("Idempotent-Replayed"))
		assert.Equal(t, orderID, data(out)["id"])

		resp, _ = e.do(t, "POST", "/v1/orders", "key-1", `{"customer_id":"c-1","amount_minor":1,"currency":"IDR"}`, token)
		assert.Equal(t, 422, resp.StatusCode, "same key, different payload")

		// Redis loses everything (failover/eviction/FLUSHALL): layer 2 must still protect.
		keys, _ := e.rdb.Keys(ctx, "e2e:idempotency:*").Result()
		require.NotEmpty(t, keys)
		require.NoError(t, e.rdb.Del(ctx, keys...).Err())
		resp, out = e.do(t, "POST", "/v1/orders", "key-1", body, token)
		assert.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, orderID, data(out)["id"])
		assert.Equal(t, true, data(out)["replayed"])

		var n int
		require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM example_orders`).Scan(&n))
		assert.Equal(t, 1, n, "exactly one order despite 4 identical requests")
	})

	t.Run("outbox relays order.created to the Redis stream exactly once", func(t *testing.T) {
		require.Eventually(t, func() bool {
			n, _ := e.rdb.XLen(ctx, "t:order").Result()
			return n >= 1
		}, 5*time.Second, 50*time.Millisecond)
		msgs, err := e.rdb.XRange(ctx, "t:order", "-", "+").Result()
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		assert.Equal(t, "order-created-"+orderID, msgs[0].Values["id"])
		assert.Equal(t, "order.created", msgs[0].Values["type"])
		assert.Equal(t, orderID, msgs[0].Values["key"])
		var st string
		require.NoError(t, e.db.QueryRow(`SELECT status FROM outbox_events WHERE id=$1`, "order-created-"+orderID).Scan(&st))
		assert.Equal(t, "PUBLISHED", st)
	})

	t.Run("reads: eventual and strong", func(t *testing.T) {
		resp, out := e.do(t, "GET", "/v1/orders/"+orderID+"/strong", "", "", token)
		assert.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, "PAYMENT_PENDING", data(out)["status"])
		resp, out = e.do(t, "GET", "/v1/orders/nope", "", "", token)
		assert.Equal(t, 404, resp.StatusCode)
		assert.Equal(t, "ORDER_NOT_FOUND", out["error"].(map[string]any)["code"])
		assert.NotEmpty(t, out["error"].(map[string]any)["request_id"])
	})

	t.Run("gRPC create is idempotent and replays the RESPONSE type", func(t *testing.T) {
		conn, err := grpcclient.Dial(e.grpcAddr, grpcclient.Options{Name: "self", Insecure: true})
		require.NoError(t, err)
		defer conn.Close()
		cl := orderv1.NewOrderServiceClient(conn)
		md := metadata.Pairs("authorization", "Bearer "+token, "idempotency-key", "grpc-key-1")
		cctx := metadata.NewOutgoingContext(ctx, md)
		req := &orderv1.CreateOrderRequest{CustomerId: "c-2", AmountMinor: 500, Currency: "USD"}

		var hdr metadata.MD
		first, err := cl.CreateOrder(cctx, req)
		require.NoError(t, err)
		second, err := cl.CreateOrder(cctx, req, grpcHeader(&hdr))
		require.NoError(t, err)
		assert.Equal(t, first.Id, second.Id)
		assert.Equal(t, []string{"true"}, hdr.Get("idempotent-replayed"))

		_, err = cl.CreateOrder(cctx, &orderv1.CreateOrderRequest{CustomerId: "c-2", AmountMinor: 999, Currency: "USD"})
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "same key, different payload")

		_, err = cl.GetOrderStrong(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), &orderv1.GetOrderRequest{Id: "missing"})
		assert.Equal(t, codes.NotFound, status.Code(err))

		_, err = cl.GetOrder(ctx, &orderv1.GetOrderRequest{Id: first.Id})
		assert.Equal(t, codes.Unauthenticated, status.Code(err))

		hc := grpc_health_v1.NewHealthClient(conn)
		res, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		require.NoError(t, err)
		assert.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, res.Status, "health needs no credentials")
	})

	t.Run("payment event is consumed once through the inbox", func(t *testing.T) {
		add := func(id string) {
			require.NoError(t, e.rdb.XAdd(ctx, &redis.XAddArgs{Stream: "t:payment", Values: map[string]any{
				"id": id, "type": "payment.succeeded", "key": orderID, "headers": "{}",
				"payload": fmt.Sprintf(`{"order_id":%q}`, orderID),
			}}).Err())
		}
		add("pay-1")
		require.Eventually(t, func() bool {
			var st string
			_ = e.db.QueryRow(`SELECT status FROM example_orders WHERE id=$1`, orderID).Scan(&st)
			return st == "PAID"
		}, 5*time.Second, 50*time.Millisecond)

		add("pay-1") // broker redelivery / duplicate publish: same business event ID
		add("pay-2") // a different event for an already-paid order is also harmless
		time.Sleep(600 * time.Millisecond)

		var paidEvents, inbox int
		require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM outbox_events WHERE event_type='order.paid' AND aggregate_id=$1`, orderID).Scan(&paidEvents))
		require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM inbox_messages`).Scan(&inbox))
		assert.Equal(t, 1, paidEvents, "order.paid emitted once")
		assert.Equal(t, 2, inbox)
		pend, _ := e.rdb.XPending(ctx, "t:payment", "e2e").Result()
		assert.EqualValues(t, 0, pend.Count, "everything acked after commit")

		// Poison message -> DLQ, not an endless retry loop.
		require.NoError(t, e.rdb.XAdd(ctx, &redis.XAddArgs{Stream: "t:payment", Values: map[string]any{
			"id": "pay-bad", "type": "payment.succeeded", "key": "x", "headers": "{}", "payload": `not-json`}}).Err())
		require.Eventually(t, func() bool { n, _ := e.rdb.XLen(ctx, "t:payment.dlq").Result(); return n == 1 }, 5*time.Second, 50*time.Millisecond)
	})

	t.Run("admin port exposes probes and metrics, public port does not", func(t *testing.T) {
		r, err := http.Get("http://" + e.adminURL + "/metrics")
		require.NoError(t, err)
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		assert.Contains(t, string(b), "idempotency_requests_total")
		assert.Contains(t, string(b), `build_info{commit="unknown",service="e2e"`)

		resp, _ := e.do(t, "GET", "/metrics", "", "", token)
		assert.Equal(t, 404, resp.StatusCode)
	})

	e.stop()
}
