# Panduan kontribusi

Skeleton ini adalah template: hanya sertakan kemampuan yang berguna bagi hampir semua service. Kode yang spesifik domain hidup di `internal/modules/<nama>`.

## Aturan emas

1. **Dependency Rule** — dependensi mengarah ke dalam; ditegakkan oleh `tests/arch` dan `depguard`. Lihat [architecture.md](architecture.md).
2. **Use case tidak tahu infrastruktur** — tidak ada `gin`, `database/sql`, `redis`, `proto`, `time.Now()`, `rand`. Pakai port, `clock.Clock`, `port.IDGenerator`.
3. **Semua penulisan state idempotent di DB** (lapisan 2), semua consumer lewat inbox (lapisan 3). Lihat [idempotency_guide.md](idempotency_guide.md).
4. **Konfigurasi masuk lewat `internal/config` dan hanya dibaca `app/`/`cmd/`**; paket lain menerima nilai lewat parameter.
5. **Jangan telan error** dan jangan bocorkan detail internal ke klien: gunakan `apperr` + `respond.Fail` / `grpcserver.Error`.
6. **Interface didefinisikan di sisi pemakai** dan sekecil mungkin (outbox: `Writer`, `Claimer`, `Purger`, `StatsReader`).
7. **Implementasi dari satu port harus lulus kontrak yang sama** (lihat `idempotency/storetest`).

## Tata letak

| Path | Isi |
| --- | --- |
| `cmd/{api,worker,migrate}` | Entrypoint tipis; semua perakitan di `internal/app` |
| `internal/app` | Composition root: infra, HTTP, gRPC, worker, shutdown |
| `internal/kernel` | `apperr`, `clock`, `ids` (hanya stdlib) |
| `internal/modules/<m>` | `domain`, `usecase/{port}`, `adapter/{inbound,outbound}`, `module.go` |
| `internal/platform` | Infrastruktur reusable (tidak boleh meng-import `modules`/`app`/`config`) |
| `api/proto`, `gen/proto` | Kontrak gRPC dan kode generate |
| `migrations/{platform,<m>}` | SQL embedded per pemilik |
| `tests/arch`, `tests/integration` | Aturan arsitektur; Postgres+Redis sungguhan |

## Modul contoh

`order` dan `user` memakai build tag `examples` agar skeleton default kosong. `make remove-examples` menghapusnya (diverifikasi: skeleton tetap build dan lulus tes tanpa mereka). Saat menambah contoh baru, taruh di dalam modul contoh, bukan di `platform`.

## Alur kerja

```bash
make test            # cepat, tanpa Docker
make test-examples   # termasuk modul contoh
make lint            # golangci-lint + depguard
make test-integration  # Postgres + Redis (env TEST_* atau container)
```

- Tes use case memakai **fake in-memory** (tanpa Docker). Jika sebuah use case butuh Docker untuk dites, ada detail infrastruktur yang bocor.
- Tes repository/consumer/migrasi → integration test dengan build tag `integration`.
- Selalu jalankan `go test -race`.
- Migrasi: file baru `NNNNNN_nama.up.sql` (+ `.down.sql`), jangan ubah file yang sudah diterapkan (checksum akan menolak).
- Perubahan `.proto` → `make proto` dan commit hasil `gen/`.
- Tambahkan metrik untuk perilaku baru yang bisa gagal diam-diam, dan dokumentasikan variabel env baru di `.env.example` + `Validate()` bila ada hubungan antar nilai.

## Checklist PR

- [ ] `make test test-examples lint` hijau
- [ ] Tidak ada import yang melanggar lapisan
- [ ] Error baru memakai `apperr` dengan `Kind` yang tepat
- [ ] Penulisan state: ada `UNIQUE`/fingerprint atau alasan tertulis mengapa tidak perlu
- [ ] Handler consumer membedakan error sementara vs `messaging.Permanent`
- [ ] Env baru: `.env.example`, ConfigMap, dan `Validate()`
