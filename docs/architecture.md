# Arsitektur

Dependensi kode sumber hanya boleh mengarah ke dalam (Dependency Rule, Clean Architecture).

```text
 cmd/*  ──►  internal/app  ──►  modules/<m> (module.go)
                                   │
              adapter/inbound ─────┤   http, grpc, consumer, job   (controller + presenter)
              adapter/outbound ────┤   postgres, redis              (implementasi port)
                                   ▼
                              usecase (+ usecase/port)             interactor, DTO, interface milik pemakai
                                   ▼
                                domain                             entity, value object, domain event, error
                                   ▼
                          internal/kernel                          apperr, clock, ids — hanya stdlib

 internal/pkg  ◄── dipakai adapter dan app; TIDAK PERNAH meng-import modules, app, atau config
```

| Lapisan | Boleh meng-import | Dilarang |
| --- | --- | --- |
| `domain` | stdlib, `kernel`, domain sendiri | semua framework, `usecase`, `pkg` |
| `usecase` | `kernel`, `domain`, `usecase/port` | `database/sql`, gin, grpc, redis, `pkg`, `gen`, modul lain |
| `adapter/inbound` | `usecase`, `domain`, `pkg`, `gen`, gin, grpc | `adapter/outbound`, `app`, `config`, modul lain |
| `adapter/outbound` | `usecase/port`, `domain`, `pkg`, driver | `adapter/inbound`, `app`, `config`, modul lain |
| `pkg` | stdlib, `kernel`, library, sesama `pkg` | `modules`, `app`, `config` |

Aturan ini dijalankan di setiap `go test` oleh `tests/arch/arch_test.go` (membaca import, termasuk file ber-build-tag) dan di lint oleh `depguard` (`.golangci.yml`).

## Di mana setiap hal diletakkan

| Concern | Lokasi |
| --- | --- |
| Invariant bisnis, state machine | `domain` |
| Alur + batas transaksi | `usecase/*.go` lewat `port.TxManager` |
| Idempotency bisnis (key + fingerprint → hasil sama) | use case + `UNIQUE` di repository Postgres (lapisan 2) |
| Idempotency edge (retry/concurrent) | `pkg/idempotency`, dipasang di `app/` |
| Idempotency consumer | `pkg/messaging/inbox`, membungkus handler di `app/` |
| Serialisasi event ke outbox | `adapter/outbound/postgres/outbox.go` |
| Cache | decorator di `adapter/outbound/redis` (tanpa mengubah use case) |
| Error → HTTP status / gRPC code | `kernel/apperr` dipetakan di `respond.Fail` dan `grpcserver.Error` |
| Auth, rate limit, tracing, metrics | `pkg/*`, dirangkai di `app/` |

## Alur proses

```text
cmd/api      HTTP :8080  gRPC :50051  admin :9090 (+ worker bila EMBED_WORKER=true)
cmd/worker   outbox relay → Redis Streams, consumer (via inbox), cleaner, reconciliation, admin :9090
cmd/migrate  up | down <source> [steps]
```

Urutan shutdown (`internal/app/app.go`): flip readiness → `DRAIN_DELAY` → hentikan server → hentikan worker → hentikan tugas latar → tutup pool. Worker berhenti setelah server agar request yang masih berjalan masih bisa menulis outbox.

## Menambah modul

1. `internal/modules/<nama>/domain` — entity dan error (`apperr`).
2. `usecase/port/*.go` — interface yang dibutuhkan; `usecase/<aksi>.go` — satu file per use case, terima dependensi lewat konstruktor (`clock.Clock`, `port.IDGenerator`, bukan `time.Now()`).
3. `adapter/inbound/{http,grpc,consumer,job}` — humble object: parse → panggil use case → presentasi.
4. `adapter/outbound/{postgres,redis,...}` — implementasi port; tulis lewat `database.Executor(ctx, db)` agar ikut transaksi use case.
5. `module.go` — `New(Deps) *Module` yang memenuhi `app.Module`.
6. Daftarkan di `internal/app/modules_none.go`; migrasi di `migrations/<nama>/`.
7. Tulis test use case dengan fake in-memory (tanpa Docker), lalu satu integration test.

Contoh lengkap: `internal/modules/order` (build tag `examples`).

## Keputusan yang diambil

- **Redis dipisah menjadi dua peran**: `state` (idempotency, lock, stream; `noeviction` + AOF) dan `cache` (boleh `allkeys-lru`). Bila `REDIS_CACHE_*` kosong, cache memakai state — cukup untuk dev, tidak disarankan di produksi.
- **Outbox tidak bergantung pada Redis lock**: eksklusivitas dari lease baris Postgres. Redis lock hanya untuk job singleton (cleaner, reconciliation).
- **NATS dihapus**. Target broker adalah Redis Streams; kontrak `messaging.Publisher`/`Consumer` memungkinkan adapter lain ditambahkan tanpa menyentuh modul.
- **Tracing masih buatan sendiri** (W3C `traceparent`, span anak sudah dibuat dengan benar). Migrasi ke OpenTelemetry SDK + OTLP dicatat sebagai pekerjaan lanjutan di [review_resolution.md](review_resolution.md).
- **Format error tetap envelope** `{success, data|error}` ditambah `request_id` dan `details` validasi; RFC 9457 belum diadopsi.
