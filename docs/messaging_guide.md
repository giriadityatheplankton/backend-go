# Messaging (Redis Streams)

Pengiriman **at-least-once**. Konsumen harus idempotent; paket `inbox` menyediakannya.

## Produsen: outbox → stream

```text
use case ── tx ──► tabel bisnis + outbox_events        (satu transaksi)
outbox.Worker ──► messaging.Publisher (redisstream, dibungkus circuit breaker) ──► XADD <prefix><aggregate_type> MAXLEN ~ N
```

- Nama stream = `STREAM_PREFIX` + `aggregate_type` (satu stream per tipe aggregate; `event_type` ada di field `type`).
- Field: `id` (ID event deterministik — **kunci dedupe**, bukan ID stream), `type`, `key` (aggregate ID), `payload`, `headers` (JSON: `X-Request-ID`, `traceparent`).
- `STREAM_MAXLEN` membatasi memori (trim perkiraan).
- Postgres tetap sumber kebenaran: bila stream hilang, outbox/reconciler bisa mem-publish ulang.

## Konsumen

Daftarkan lewat `Module.Subscriptions()`:

```go
func (m *Module) Subscriptions() []messaging.Subscription {
    return []messaging.Subscription{{Stream: "payment", Name: "order/payment-events", Handler: m.pay.Handle}}
}
```

`app/worker.go` membungkus handler dengan `inbox.Wrap` dan menjalankan `redisstream.Consumer`. Perilaku konsumen:

| Hal | Perilaku |
| --- | --- |
| Group | satu per service (`CONSUMER_GROUP`, default `SERVICE_NAME`), nama consumer = hostname pod |
| Ack | **setelah COMMIT** transaksi inbox + efek bisnis |
| Pod mati | `XAUTOCLAIM` mengambil alih pesan idle > `CONSUMER_CLAIM_IDLE` |
| Retry | handler error → tidak di-ack → diklaim ulang setelah `CONSUMER_CLAIM_IDLE` (jarak antar-retry) |
| DLQ | lewat `CONSUMER_MAX_DELIVERIES` atau error `messaging.Permanent` → `XADD <stream>.dlq` lalu `XACK`; gagal menulis DLQ → tidak di-ack (tidak pernah hilang) |
| Poison | payload tak terbaca / tanpa ID → langsung DLQ |
| Backpressure | `CONSUMER_WORKERS` (semaphore) + `COUNT` = `CONSUMER_BATCH_SIZE` |
| Urutan | `CONSUMER_WORKERS=1` untuk urutan ketat per stream; atau terima eventual order dan gunakan versi di payload |
| Shutdown | berhenti membaca, menunggu handler yang berjalan, sisanya diklaim pod lain |
| Panic handler | ditangkap, dihitung sebagai kegagalan |

Klasifikasi error di handler:

```go
case errors.Is(err, domain.ErrOrderNotFound), errors.Is(err, domain.ErrInvalidStatus):
    return messaging.Permanent(err) // retry tidak mengubah hasil → DLQ
default:
    return err                      // sementara → redelivery
```

## Inbox

```sql
inbox_messages (consumer, message_id) PRIMARY KEY, received_at
```

`consumer` harus unik per subscriber (`Subscription.Name`). Retensi `INBOX_RETENTION` (default 14 hari) harus lebih panjang dari retensi stream dan jendela redelivery; pembersihan berjalan tiap jam di worker.

## Operasi

- Redis stream: `appendonly yes`, `appendfsync everysec`, `maxmemory-policy noeviction`. Failover bisa kehilangan ~1 detik tulisan; itu sebabnya outbox bukan Redis.
- Metrik: `consumer_messages_total{stream,result}`, `consumer_pending_messages{stream}`, `outbox_publish_total{result}`, `outbox_oldest_pending_age_seconds`, `circuit_breaker_state{name}`.
- Alert yang dianjurkan: umur outbox tertua > 1 menit; `consumer_pending_messages` terus naik; `consumer_messages_total{result="dlq"}` naik; breaker `open` > 1 menit.
- Memproses ulang DLQ: baca entri `XRANGE <stream>.dlq`, perbaiki penyebab, `XADD` ulang ke stream asli dengan `id` yang sama (inbox memastikan efek tidak ganda bila sebelumnya sebagian berhasil).
