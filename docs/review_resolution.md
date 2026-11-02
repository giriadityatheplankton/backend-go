# Status temuan review

Pemetaan [review.md](../review.md) → perubahan. Struktur folder mengikuti [recommend-folder-structure.md](../recommend-folder-structure.md).
✅ selesai dan diuji · 🟡 sebagian · ⏭️ sengaja ditunda.

## Bug P0

| # | Temuan | Status | Perubahan | Bukti |
| --- | --- | --- | --- | --- |
| 1 | `TimeoutMiddleware` memakai goroutine → data race | ✅ | `middleware.Timeout` hanya memasang deadline di context; hard limit = `http.Server.WriteTimeout` | `go test -race ./...` hijau (sebelumnya gagal) |
| 2 | Replay gRPC memakai tipe request | ✅ | respons disimpan sebagai `anypb.Any`, replay via `UnmarshalNew`; `CheckGRPCIdempotency` dan tipe tak terpakai dihapus | `TestGRPC_ReplaysResponseTypeNotRequestType`, e2e |
| 3 | Lock idempotency tanpa fencing token | ✅ | `Store` berbasis lease: `Acquire/Complete/Release/Extend`, Lua CAS terhadap token; heartbeat | kontrak store (memory + Redis), `lease expire…` |
| 4 | Outbox: lock 5 dtk, tanpa `SKIP LOCKED`, `PROCESSING` menggantung | ✅ | klaim lease di Postgres + `SKIP LOCKED` + reclaim; update di-fence `locked_by`; lease diperpanjang/dilepas saat shutdown | integration: klaim konkuren tanpa overlap, reclaim pod mati |
| 5 | CI & Dockerfile root rusak (`cmd/api/main.go`, Go 1.22) | ✅ | CI baru (`go-version-file`, `./cmd/...`, `-race`, tag `examples`, integration, buf, govulncheck, Trivy, SBOM); satu Dockerfile multi-arch distroless; root `Dockerfile` dihapus | image dibangun dan dijalankan lokal; **workflow CI belum dijalankan di GitHub** |
| 6 | gRPC tanpa service, port hardcode, interceptor tak terpasang | ✅ | proto + `buf` + kode generate; `OrderService`/`UserService`; `cfg.GRPC_ADDRESS`; health, reflection, keepalive; interceptor lengkap | e2e gRPC |
| 7 | Tidak ada consumer/inbox, tidak ada Redis Streams | ✅ | `messaging`, `redisstream` (publisher + consumer), `inbox`; NATS dihapus | miniredis + Postgres + e2e |

## Idempotency (§3)

| Item | Status |
| --- | --- |
| A–B gRPC replay + `CheckGRPCIdempotency` | ✅ |
| C fingerprint gRPC (`FullMethod` + marshal deterministik) | ✅ |
| D fencing token | ✅ |
| E `context.WithoutCancel` saat simpan | ✅ (`TestHTTP_ClientDisconnectStillSavesResult`) |
| F TTL in-flight vs timeout | ✅ divalidasi di `Config.Validate()` + heartbeat |
| G kebijakan cache status | ✅ hanya 2xx + 400/404/409/422; 401/403/408/429/5xx melepas lease |
| H replay setia (Content-Type, Location, ETag) + `Idempotent-Replayed` | ✅ |
| I perilaku Redis mati | ✅ `IDEMPOTENCY_FAILURE_MODE=closed|open`, sama di HTTP/gRPC, tanpa panic; Redis wajib di produksi |
| J scoping per principal, validasi key, `Idempotency-Key` standar, batas body | ✅ |
| Desain 3 lapis (L1/L2/L3) | ✅ [idempotency_guide.md](idempotency_guide.md) |
| Daftar test §3.4 | ✅ semuanya ada, termasuk "Redis di-flush di tengah" (e2e) |

## Concurrency & HTTP (§5)

| Item | Status |
| --- | --- |
| `ReadHeaderTimeout`/`IdleTimeout`/`MaxHeaderBytes` | ✅ |
| Listener gagal harus menghentikan proses; gRPC memakai config | ✅ listener dibind saat startup, error menurunkan proses |
| Redis/NATS gagal start → jalan diam-diam | ✅ fail-fast (`REDIS_REQUIRED=false` hanya dev) |
| `main()` 300+ baris → `internal/app` | ✅ `main` ±40 baris |
| Urutan shutdown, double signal, `GracefulStop` bertimeout, exit code | ✅ |
| CORS default deny, `Vary: Origin`, `*`+credentials ditolak | ✅ |
| Recovery gRPC tidak membocorkan panic | ✅ (+ stream) |
| RequestID tervalidasi, masuk context, log, outbox, gRPC metadata | ✅ |
| Logger memakai `*Context`, query string tidak dicatat | ✅ |
| `X-XSS-Protection: 0`, HSTS tanpa `preload` | ✅ |
| Metrics `unmatched` untuk 404 | ✅ |
| Envelope + `request_id`/`details`; `DisallowUnknownFields`; batas body global | ✅ |
| RFC 9457 Problem Details | ⏭️ envelope dipertahankan, ditambah `request_id` dan `details` |
| Auth + `Principal`; rate limit & scoping dari principal | ✅ `platform/auth` (JWT HS/RS, static token) |

## gRPC (§6)

Semua baris tabel ✅ kecuali: **mTLS** 🟡 (server TLS tersedia lewat `GRPC_TLS_*`; verifikasi sertifikat klien diserahkan ke service mesh) dan **Logging interceptor** 🟡 (request ID + trace + metrics ada; log per-RPC belum). Client factory: `grpcclient.Dial` (retry, breaker yang hanya menghitung kegagalan infrastruktur, deadline, trace/request-id, key turunan). Taksonomi error terpadu: `kernel/apperr` → `respond.Fail` / `grpcserver.Error`.

## Database & Outbox (§7)

| Item | Status |
| --- | --- |
| Lease + `SKIP LOCKED`, reclaim, fencing | ✅ |
| `max_retries` per event, backoff berjitter + cap | ✅ |
| Urutan per aggregate | ✅ `OUTBOX_PRESERVE_ORDER` |
| `Save` tidak menimpa (`DO NOTHING`) | ✅ |
| Topic mapper | ✅ `ByAggregateType` |
| Metrik outbox terisi + umur event tertua | ✅ |
| Index cleaner / parsial | ✅ migrasi 000004 |
| Helper `Enqueue`/`PartitionFor` | ✅ `Save`, `PartitionFor`, `Enrich` |
| `database.WithTx` + retry 40001/40P01 | ✅ `TxManager` (diuji dengan konflik serializable sungguhan) |
| `statement_timeout` / `idle_in_transaction_session_timeout` | ✅ |
| Replica tidak membuat semua pod unready; `Read()` lewati replica mati | ✅ |
| Tenant router tidak jatuh ke DB bersama | ✅ fallback opt-in |
| Advisory lock, checksum, `CONCURRENTLY`, down, versi terpisah | ✅ engine migrasi diperkuat (bukan diganti goose/golang-migrate) |
| Dokumentasi rumus `max_connections` | ✅ [infra_operations_guide.md](infra_operations_guide.md) |
| LISTEN/NOTIFY, `pgxpool`, `sqlc` | ⏭️ |

## Redis (§8)

| Item | Status |
| --- | --- |
| Timeout, pool, TLS, ACL | ✅ |
| Sentinel | ✅ |
| `.env.production` tanpa `REDIS_MODE=cluster` | ✅ template baru + validasi single/multi-alamat |
| Prefix key per service | ✅ idempotency, rate limit, lock, cache |
| Pisah Redis cache vs state | ✅ (`REDIS_CACHE_*`) |
| `redis.NewScript` (EVALSHA) | ✅ semua script |
| Cache: `WithoutCancel`, jitter TTL, negative cache | ✅ |
| `redisotel`, `PoolStats()` Redis | ⏭️ |

## Observability, Config, Deployment (§9)

| Item | Status |
| --- | --- |
| Metrik idempotency, rate limit, breaker, outbox lag, consumer lag/DLQ, build info | ✅ |
| `/metrics` & pprof di port admin | ✅ |
| Readiness tidak membocorkan error | ✅ |
| Log: `service/version/env/pod`, trace_id, request_id | ✅ |
| Span anak yang benar | ✅ (tree tidak lagi rusak) |
| **OpenTelemetry SDK + OTLP** | ⏭️ tracing masih buatan sendiri; jalur migrasi: ganti `telemetry.HTTPMiddleware`/interceptor di `app/` |
| `Config.Validate()`, parse ketat, `.env` tidak dibaca di produksi | ✅ |
| `DB_REPLICA_DSN` duplikat dihapus; `.env.production` → `.env.production.example` | ✅ |
| CI: pin versi, golangci, govulncheck, buf, Trivy, SBOM, integration | ✅ ditulis; belum dijalankan di GitHub |
| Dockerfile: `TARGETARCH`, distroless, versi | ✅ |
| docker-compose: postgres, redis (AOF), migrate, worker | ✅ (jaeger/otel dikomentari) |
| K8s: digest/tag per overlay, probe, `GOMEMLIMIT`, topology spread, `emptyDir`, kustomize | ✅ |
| Makefile `proto` | ✅ |

## Struktur (recommend-folder-structure.md)

✅ `cmd/{api,worker,migrate}`, `internal/{app,config,kernel,modules,platform}`, `api/`, `gen/`, `migrations/`, `tests/{arch,integration}`, `deployments/{docker,k8s/{base,overlays}}`. Pelanggaran §8.1 diperbaiki: interface tidak lagi di `domain`; use case tidak meng-import infrastruktur; transaksi ditentukan use case lewat `port.TxManager`; waktu dan ID di-inject; cache menjadi decorator; DTO gRPC buatan tangan diganti kode generate; tag `json` tidak ada di entity. `go-arch-lint` (opsional) digantikan test Go `tests/arch` yang berjalan di setiap `go test`.

## Hasil verifikasi lokal

| Pemeriksaan | Hasil |
| --- | --- |
| `go test -race ./...` (tanpa dan dengan `-tags examples`) | hijau; race pada `TestTimeoutMiddleware` hilang |
| `go test -race -tags integration,examples ./tests/...` terhadap Postgres 16 + Redis 7 sungguhan | hijau (migrasi konkuren, `SKIP LOCKED`, reclaim lease, inbox, retry serializable, e2e HTTP+gRPC+stream) |
| `tests/arch` (Dependency Rule) | hijau; aturan diuji juga dengan kasus yang harus ditolak |
| `golangci-lint` v2.15.0 (termasuk `depguard`, `gosec`, `errorlint`, `contextcheck`) | 0 issue |
| `govulncheck` v1.9.0 | 0 kerentanan terpakai (`x/net` → v0.60.0 dan `quic-go` → v0.59.1 dinaikkan) |
| `docker build` + menjalankan image terhadap Postgres/Redis | migrasi, readiness, replay idempotent, metrik, shutdown exit 0 |
| `kubectl kustomize` dev/staging/prod | render OK |
| `make remove-examples` pada salinan | skeleton tetap build, vet, dan lulus tes |

Belum bisa diverifikasi di lingkungan ini: workflow GitHub Actions itu sendiri, `buf` (kode di `gen/` dibuat dengan `protoc` + plugin yang sama), Trivy/SBOM, dan penerapan manifest ke cluster sungguhan.

## Cara memverifikasi

```bash
make test test-examples          # unit, race, arsitektur
make test-integration            # Postgres+Redis sungguhan, termasuk e2e HTTP+gRPC+stream
docker build -f deployments/docker/Dockerfile -t backend-go .
make k8s-render ENV=prod
```
