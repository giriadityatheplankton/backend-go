# Transactional outbox

`internal/platform/outbox` memindahkan event yang ditulis dalam transaksi bisnis ke broker tanpa dual-write.

## Menulis event (di modul)

Adapter outbound memanggil `outbox.NewPostgresRepository(database.Executor(ctx, db)).Save(ctx, event)` di dalam transaksi use case. `Save` memakai `ON CONFLICT (id) DO NOTHING`: memanggilnya lagi (reconciliation, retry transaksi) **tidak pernah** mengembalikan event yang sudah `PUBLISHED` menjadi `PENDING`. Header `X-Request-ID` dan `traceparent` disalin dari context saat enqueue.

- `ID` harus deterministik dari aggregate, mis. `order-created-<orderID>`.
- `Partition = outbox.PartitionFor(aggregateID, OUTBOX_PARTITIONS)`.

## Relay (`outbox.Worker`)

Tiap partisi di-poll oleh semua pod; eksklusivitas dari Postgres, bukan Redis:

```sql
WITH claimable AS (
  SELECT id FROM outbox_events WHERE partition = $1 AND (
        (status IN ('PENDING','FAILED') AND next_retry_at <= now())
     OR (status = 'PROCESSING' AND locked_until < now()))      -- reclaim pod yang mati
  AND NOT EXISTS (<event lebih lama untuk aggregate yang sama masih terbuka>)  -- urutan per aggregate
  ORDER BY created_at, id LIMIT $2 FOR UPDATE SKIP LOCKED)
UPDATE outbox_events SET status='PROCESSING', locked_by=$3, locked_until=now()+$4 ...
```

| Perilaku | Detail |
| --- | --- |
| Fencing | `MarkPublished/MarkFailed/MoveToDLQ` memakai `WHERE locked_by = $owner AND status='PROCESSING'`; pemilik lama mendapat `ErrLeaseLost` |
| Lease | `OUTBOX_LEASE` (default 30 dtk); diperpanjang selama batch panjang; event yang belum diproses dilepas saat shutdown |
| Retry | `max_retries` per event; backoff `base·2^n` dibatasi `MaxBackoff` dengan jitter ±20% |
| Poison | `messaging.Permanent` dari publisher → langsung DLQ |
| Urutan | `OUTBOX_PRESERVE_ORDER=true`: event N+1 menunggu event N untuk aggregate yang sama |
| Backlog | batch penuh → langsung ambil lagi tanpa tidur |
| Shutdown | publish yang sudah mulai diselesaikan (dengan timeout), loop berhenti |

Dampak: bila Redis mati, relay tetap claim dari DB; circuit breaker pada publisher membuka dan event dijadwalkan ulang dengan backoff.

## Housekeeping

`Cleaner` menghapus `PUBLISHED` lebih tua dari `OUTBOX_RETENTION` (default 7 hari) per batch dengan `SKIP LOCKED`, di bawah Redis lock best-effort. Index parsial (`idx_outbox_claim`, `idx_outbox_open_by_aggregate`, `idx_outbox_published_updated`) ada di `migrations/platform/000004_outbox_lease.up.sql`.

## Reconciliation

Job modul (`Module.Jobs()`) memperbaiki catatan bisnis yang tidak punya event. Perbaikan idempotent karena ID event deterministik. Lihat [reconciliation_guide.md](reconciliation_guide.md).

## Monitoring

`outbox_oldest_pending_age_seconds` (alert terpenting), `outbox_publish_total{result=ok|retry|dlq}`, tabel `dlq_events`. Mem-publish ulang event DLQ setelah penyebabnya diperbaiki: `UPDATE outbox_events SET status='PENDING', retry_count=0, next_retry_at=now(), last_error=NULL WHERE id=$1 AND status='DLQ'`. ID event tetap sama, jadi inbox konsumen tetap mencegah efek ganda.
