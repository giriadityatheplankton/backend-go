# Contoh alur order (modul `order`, build tag `examples`)

Satu alur yang memakai semua mekanisme skeleton. Jalankan: `make run-examples` (butuh Postgres + Redis).

```text
POST /v1/orders  (Authorization, Idempotency-Key)
 └ auth → scope orders:write → rate limit → idempotency L1 (Redis) → handler
    └ CreateOrder.Execute
       ├ replay? FindByIdempotencyKey (primary)  → hasil lama, atau ErrIdempotencyKeyReused bila fingerprint beda
       └ WithinTx: INSERT example_orders ON CONFLICT(idempotency_key) DO NOTHING   (L2)
                   outbox.Save(order.created, id=order-created-<id>)               (transaksi yang sama)
                   MarkOutboxEnqueued
outbox.Worker ── claim lease ──► XADD <prefix>order  {id,type,key,payload,headers}
(service lain) XADD <prefix>payment {type: payment.succeeded, payload: {order_id}}
 └ redisstream.Consumer ─► inbox.Wrap ─► PaymentEvents.Handle ─► MarkOrderPaid ─► order.paid (outbox lagi)
```

## Endpoint

| Method | Path | Scope | Catatan |
| --- | --- | --- | --- |
| POST | `/v1/orders` | `orders:write` | `Idempotency-Key` wajib; `201` baru, `200` replay DB, replay edge mengulang status asli |
| GET | `/v1/orders/:id` | `orders:read` | boleh dari cache/replica |
| GET | `/v1/orders/:id/strong` | `orders:read` | selalu primary, tanpa cache — untuk keputusan |
| gRPC | `order.v1.OrderService/{CreateOrder,GetOrder,GetOrderStrong}` | metadata `authorization`, `idempotency-key` | use case yang sama |
| GET | `/v1/users/:id` | `users:read` | modul `user`; contoh decorator cache |

## Konsistensi baca

`port.Consistency`: `EventuallyConsistent` (cache → replica sehat → primary), `Strong` (primary, tanpa cache; read-after-write dan keputusan), `ForUpdate` (primary + `FOR UPDATE` di dalam transaksi). `CachedOrderRepository` hanya memotong jalur `EventuallyConsistent`.

## Skema event

`order.created` dan `order.paid`, versi 1, JSON: `event_id, event_type, version, occurred_at, order_id, [customer_id, amount_minor, currency, status]`. Skema dan serialisasi hidup di `adapter/outbound/postgres/outbox.go`; use case hanya menghasilkan domain event.

## Yang dibuktikan `tests/integration/app_e2e_test.go`

Empat request identik → satu order; Redis di-flush di tengah → tetap satu order (L2); replay gRPC bertipe respons benar; key sama payload beda → 422 / `INVALID_ARGUMENT`; event keluar ke stream tepat sekali; event pembayaran ganda → `order.paid` sekali; payload rusak → DLQ; `/metrics` hanya di port admin; shutdown bersih.
