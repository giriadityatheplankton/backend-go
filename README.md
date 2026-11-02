# backend-go

Skeleton microservice Go yang siap produksi: **HTTP + gRPC**, **PostgreSQL**, **Redis** (cache, state, dan message queue via **Redis Streams**), **transactional outbox**, dan **idempotency tiga lapis**. Strukturnya mengikuti Clean Architecture (lihat [docs/architecture.md](docs/architecture.md)).

## Yang sudah ada

| Kebutuhan | Implementasi |
| --- | --- |
| Idempotency | Edge (Redis, lease + fencing token, replay gRPC via `anypb`) → DB (`UNIQUE` + fingerprint) → Consumer (tabel inbox). [Panduan](docs/idempotency_guide.md) |
| Message queue | Redis Streams: consumer group, `XAUTOCLAIM`, retry, DLQ stream, trimming. [Panduan](docs/messaging_guide.md) |
| Outbox | Klaim lease di Postgres (`FOR UPDATE SKIP LOCKED`), urutan per aggregate, backoff + jitter, DLQ. [Panduan](docs/outbox_guide.md) |
| Circuit breaker | `platform/resilience`; dipakai di publisher outbox dan client gRPC (hanya kegagalan infrastruktur yang dihitung) |
| Rate limit | Sliding window Redis (Lua), key dari **principal** hasil autentikasi, bukan header klien |
| Auth | JWT / static token, `Principal` di context, scope per route, interceptor gRPC |
| gRPC | Health, reflection (non-prod), keepalive, batas pesan, TLS opsional, interceptor stream, client factory dengan retry + breaker + deadline |
| Shutdown | Readiness dibalik dulu → drain → stop server → stop worker → tutup pool; sinyal kedua memaksa keluar |
| Observability | Log JSON (`request_id`, `trace_id`), Prometheus (idempotency, outbox lag, consumer, breaker, pool), port admin terpisah |
| Konfigurasi | Divalidasi saat start; nilai salah = gagal start. Aturan produksi ditegakkan |
| Migrasi | Advisory lock, checksum, `-- +migrate notx`, `.down.sql`, versi terpisah per modul |
| Arsitektur | Dependency Rule ditegakkan oleh test (`tests/arch`) dan `depguard` |

## Struktur

```text
cmd/{api,worker,migrate}      entrypoint tipis
internal/app                  composition root (satu-satunya tempat semua lapisan bertemu)
internal/config               load + Validate()
internal/kernel               apperr, clock, ids (tanpa dependensi eksternal)
internal/modules/<m>          domain → usecase(+port) → adapter(inbound|outbound) → module.go
internal/platform             infrastruktur reusable, tidak tahu domain
api/proto  gen/proto          kontrak gRPC dan kode hasil generate (buf)
migrations/{platform,<m>}     SQL per pemilik
deployments/{docker,k8s}      Dockerfile multi-arch distroless; kustomize base + overlay
tests/{arch,integration}      aturan arsitektur; Postgres + Redis sungguhan
```

Skeleton kosong secara default. Modul contoh `order` dan `user` hanya dikompilasi dengan `-tags examples`.

## Mulai cepat

```bash
docker compose up -d postgres redis
make migrate TAGS=examples
make run-examples            # API + worker tertanam, port 8080 / 50051 / admin 9090

curl -s localhost:8080/v1/orders -H 'Authorization: Bearer dev-token-0123456789abcdefghij' \
  -H 'Idempotency-Key: demo-1' -H 'Content-Type: application/json' \
  -d '{"customer_id":"c-1","amount_minor":15000,"currency":"IDR"}'
# kirim ulang perintah yang sama: respons identik + header Idempotent-Replayed: true

curl -s localhost:9090/healthz/ready ; curl -s localhost:9090/metrics | head
```

Semua dalam satu stack: `docker compose up --build` (tambahkan `--profile split` untuk worker terpisah).

## Perintah

```bash
make test               # unit + arsitektur, -race
make test-examples      # termasuk modul contoh
make test-integration   # butuh Postgres+Redis (TEST_POSTGRES_DSN, TEST_REDIS_ADDR) atau runtime container
make lint               # golangci-lint + depguard
make proto              # buf generate
make k8s-render ENV=prod
make remove-examples    # mulai service sungguhan dari skeleton ini
make new path=../svc module=github.com/acme/svc
```

## Membuat service dari skeleton

1. `make new path=../my-service module=github.com/acme/my-service`
2. `make remove-examples`
3. Buat modul di `internal/modules/<nama>` (lihat [docs/architecture.md](docs/architecture.md#menambah-modul)) dan daftarkan di `internal/app/modules_none.go`.
4. Tambahkan migrasi di `migrations/<nama>/` dan daftarkan di `migrations/modules_none.go`.

## Dokumentasi

[architecture](docs/architecture.md) · [idempotency](docs/idempotency_guide.md) · [messaging](docs/messaging_guide.md) · [outbox](docs/outbox_guide.md) · [operasi](docs/infra_operations_guide.md) · [kontribusi](docs/contributing_guide.md) · [proto](docs/proto_guide.md) · [hasil review](docs/review_resolution.md)

Latar belakang keputusan: [review.md](review.md) dan [recommend-folder-structure.md](recommend-folder-structure.md).
