# Operasi & infrastruktur

## Port

| Port | Fungsi | Terbuka ke |
| --- | --- | --- |
| 8080 | HTTP publik (`/v1/...`) | Ingress |
| 50051 | gRPC (+ `grpc.health.v1`, reflection hanya non-prod) | service lain |
| 9090 | **Admin**: `/healthz/live`, `/healthz/ready`, `/metrics`, `/debug/pprof` (bila `PPROF_ENABLED`) | Prometheus, kubelet — jangan lewat Ingress |

`/metrics` dan pprof tidak ada di port publik. Body readiness hanya `UP/DEGRADED/DOWN` per komponen; alasan kegagalan hanya muncul di port admin non-produksi (`Verbose`).

## Probe & readiness

- **Liveness** tidak bergantung pada apa pun di luar proses.
- **Readiness/startup** memeriksa: PostgreSQL primary dan Redis state (*critical* → pod unready bila mati); replica PostgreSQL dan Redis cache (*degraded* → tetap ready, dilaporkan `DEGRADED`).
- Replica yang gagal dikeluarkan dari rotasi baca otomatis (`DBGroup.MonitorReplicas`); bila semua mati, baca jatuh ke primary.
- gRPC health mengikuti readiness (`SetServing`), jadi probe `grpc` Kubernetes dan LB sisi-klien melihat status yang sama.

## Startup yang gagal-cepat

Konfigurasi tidak valid, PostgreSQL/Redis wajib yang tidak terjangkau, atau port yang sudah dipakai → proses keluar dengan kode ≠ 0 dan pesan jelas. Listener yang mati saat berjalan menurunkan seluruh proses (tidak ada pod "hidup tapi tidak melayani"). `REDIS_REQUIRED=false` (hanya dev) mengizinkan mode degraded.

## Shutdown

```text
SIGTERM → readiness NOT_READY → DRAIN_DELAY → stop HTTP/gRPC/admin → stop worker → stop tugas latar → tutup pool
```

Anggaran: `DRAIN_DELAY (2s) + SHUTDOWN_TIMEOUT (15s) + preStop (3s) = 20s ≤ terminationGracePeriodSeconds (30s)`; divalidasi saat start lewat `TERMINATION_GRACE`. `GracefulStop` gRPC dibatasi `SHUTDOWN_TIMEOUT` lalu `Stop()`. Sinyal kedua → exit 1 segera. `preStop` memakai aksi `sleep` native Kubernetes (image distroless tidak punya shell).

## Konfigurasi

Referensi lengkap dan komentar: [`.env.example`](../.env.example). Template produksi: `.env.production.example` (tidak dibaca saat `APP_ENV=production`; berikan lewat ConfigMap/Secret). Aturan yang ditegakkan `Config.Validate()`:

- nilai salah (`DB_MAX_OPEN_CONNS=abc`, `READ_TIMEOUT=10` tanpa satuan) → gagal start, bukan default diam-diam;
- `WRITE_TIMEOUT ≥ HTTP_REQUEST_TIMEOUT`; `IDEMPOTENCY_INFLIGHT_TTL ≥ timeout + 5s`;
- `REDIS_ADDRESSES` banyak + `REDIS_MODE=single` ditolak (salah konfigurasi cluster klasik);
- produksi: `AUTH_MODE≠none`, `DB_PRIMARY_DSN` wajib, `REDIS_REQUIRED=true`, reflection mati, tanpa `CORS *` + credentials.

Koneksi PostgreSQL: total koneksi = `replika pod × DB_MAX_OPEN_CONNS` (+ worker) harus di bawah `max_connections`, atau gunakan PgBouncer. `statement_timeout` dan `idle_in_transaction_session_timeout` diset per koneksi.

## Kubernetes

`deployments/k8s/base` + `overlays/{dev,staging,prod}` (kustomize; `make k8s-render ENV=prod`):

- dua Deployment: `backend-go` (API) dan `backend-go-worker` (outbox, consumer, job) — skala terpisah; worker aman dijalankan multi-replika;
- `runAsNonRoot`, `readOnlyRootFilesystem` + `emptyDir` untuk `/tmp`, `drop ALL`, seccomp, `automountServiceAccountToken: false`;
- `topologySpreadConstraints` (zona + node), PDB, HPA (CPU/memori), NetworkPolicy (admin hanya dari `monitoring`);
- `GOMEMLIMIT` ≈ 90% batas memori; `startupProbe` memakai readiness;
- migrasi sebagai `Job` terpisah (`/app/migrate`), dilindungi advisory lock bila tumpang tindih;
- image dipin per overlay (`kustomize edit set image backend-go=...@sha256:...` untuk prod).

## Migrasi

`cmd/migrate` (`up` default, `down <source> [steps]`). Satu advisory lock global; checksum SHA-256 mendeteksi file yang diubah setelah diterapkan (buat migrasi baru, jangan edit); baris pertama `-- +migrate notx` menjalankan di luar transaksi (`CREATE INDEX CONCURRENTLY`). Versi dipisah per sumber: `migrations/platform` (outbox, dlq, audit, inbox) dan `migrations/<modul>`.

## Runbook singkat

| Gejala | Periksa | Tindakan |
| --- | --- | --- |
| Pod unready | admin `/healthz/ready` (port admin) | Komponen `DOWN` = primary/Redis state; `DEGRADED` bukan penyebab |
| `idempotency_requests_total{result="store_error"}` naik | Redis state | Mode `closed` menolak tulis dengan 503 — pulihkan Redis; jangan ganti ke `open` untuk endpoint uang/stok |
| `...{result="lease_lost"}` > 0 | latensi handler vs `IDEMPOTENCY_INFLIGHT_TTL` | Naikkan TTL atau percepat handler |
| `outbox_oldest_pending_age_seconds` naik | worker hidup? breaker `open`? | Cek Redis stream, `dlq_events`, log `Outbox publish failed` |
| `consumer_pending_messages` naik | handler gagal/lambat | Cek DLQ stream `<stream>.dlq`, `CONSUMER_WORKERS` |
| Breaker `circuit_breaker_state=2` | dependensi hilir | Menutup otomatis lewat half-open setelah `BREAKER_OPEN_TIMEOUT` |
| Deploy macet 30 detik saat rollout | anggaran shutdown | Pastikan `terminationGracePeriodSeconds` ≥ `MinTerminationGrace` |

## Yang belum ada (pekerjaan lanjutan)

OpenTelemetry SDK + OTLP exporter (tracing saat ini buatan sendiri, W3C `traceparent`), Problem Details RFC 9457, `pgxpool`/`sqlc`, LISTEN/NOTIFY untuk outbox. Lihat [review_resolution.md](review_resolution.md).
