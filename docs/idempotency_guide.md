# Idempotency

Tiga lapisan, masing-masing menutup kegagalan yang tidak bisa ditutup lapisan lain.

```text
Client retry ─► L1 Edge (Redis)         dedupe cepat, replay respons, tahan request konkuren
                L2 Domain (Postgres)    UNIQUE(idempotency_key) + fingerprint — SUMBER KEBENARAN
                  └ outbox ─► broker ─► L3 Consumer (tabel inbox)  dedupe pesan duplikat
```

| Lapisan | Mencegah | Gagal jika dilewati |
| --- | --- | --- |
| L1 `pkg/idempotency` | Eksekusi ganda dari retry/konkuren, beban DB | Redis hilang (evict, failover, `FLUSHALL`) → hanya L2 yang menyelamatkan |
| L2 use case + repository | Data ganda | — (ini yang menentukan) |
| L3 `pkg/messaging/inbox` | Efek ganda dari pesan at-least-once | Redelivery menjalankan efek dua kali |

**Aturan:** setiap operasi yang menulis state wajib punya L2; setiap consumer wajib lewat L3; L1 bersifat opsional/optimasi.

## L1: cara kerja

1. Key klien (`Idempotency-Key`, atau `X-Idempotency-Key` untuk kompatibilitas; 1–128 karakter ASCII tanpa spasi) di-scope: `hash(principal, method, route, key)`. Principal diambil dari **auth context**, bukan header klien, jadi dua user dengan key sama tidak pernah saling menerima respons.
2. `Acquire` mengembalikan **lease** berisi token acak (fencing token). Hanya satu pemanggil yang menang.
3. Handler berjalan. Heartbeat memperpanjang lease tiap `TTL/3` selama handler hidup.
4. Selesai: `Complete` (Lua, hanya jika token cocok) menyimpan respons; atau `Release` bila hasilnya tidak boleh di-cache.
5. Penyimpanan memakai `context.WithoutCancel` + timeout 2 detik, sehingga hasil tetap tersimpan walau klien putus. Retry berikutnya mendapat replay, bukan `409`.

### Hasil per kondisi

| Kondisi | HTTP | gRPC |
| --- | --- | --- |
| Key baru | handler jalan | handler jalan |
| Key sama, payload sama, selesai | replay status + body + `Location`/`ETag` + `Idempotent-Replayed: true` | replay **tipe respons yang benar** (`anypb`) + header `idempotent-replayed` |
| Key sama, payload beda | `422 IDEMPOTENCY_KEY_REUSED` | `INVALID_ARGUMENT` |
| Key sama, masih berjalan | `409 REQUEST_IN_PROGRESS` + `Retry-After` | `ABORTED` |
| Redis mati | sesuai `IDEMPOTENCY_FAILURE_MODE` | sama |
| Key salah format | `400 INVALID_IDEMPOTENCY_KEY` | `INVALID_ARGUMENT` |

Yang di-cache: `2xx` dan 4xx deterministik (400, 404, 409, 422); gRPC: `InvalidArgument`, `NotFound`, `FailedPrecondition`, `AlreadyExists`, `OutOfRange`. Yang **tidak** di-cache (lease dilepas agar bisa dicoba ulang): `401/403/408/429`, semua `5xx`, dan error gRPC sementara. Panic juga melepas lease.

### `IDEMPOTENCY_FAILURE_MODE`

- `closed` (default): Redis mati → `503` / `UNAVAILABLE`. Wajib untuk uang dan stok.
- `open`: handler tetap jalan tanpa dedupe edge; **hanya aman bila L2 melindungi tulisan**.

Perilaku ini sama untuk HTTP dan gRPC, dan tidak pernah panic.

### Konfigurasi yang divalidasi saat start

- `IDEMPOTENCY_INFLIGHT_TTL ≥ timeout request + 5s` (lease tidak boleh habis sebelum handler selesai).
- `IDEMPOTENCY_TTL > IDEMPOTENCY_INFLIGHT_TTL`.
- Metrik `idempotency_requests_total{result="lease_lost"}` > 0 berarti TTL terlalu pendek.

## L2: pola di use case

```go
fp := fingerprint(customerID, amount)
if out, found, err := uc.replay(ctx, key, fp); err != nil || found { return out, err } // jalur cepat

err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
    if err := uc.repo.Create(ctx, order, key, fp); err != nil { return err } // ON CONFLICT DO NOTHING → port.ErrDuplicateKey
    if err := uc.outbox.Enqueue(ctx, events...); err != nil { return err }   // transaksi yang sama
    return uc.repo.MarkOutboxEnqueued(ctx, order.ID())
})
if errors.Is(err, port.ErrDuplicateKey) { /* kalah balapan → ambil yang sudah ada */ }
```

Kunci pola: event dibuat **sekali di luar closure** (closure bisa diulang oleh retry `40001/40P01`); `ID` event deterministik (`order-created-<orderID>`); fingerprint disimpan di baris yang sama dengan key.

## L3: consumer

Lihat [messaging_guide.md](messaging_guide.md). Ringkasnya: `BEGIN → INSERT inbox ON CONFLICT DO NOTHING → (bila baru) efek bisnis → COMMIT → XACK`.

## Panggilan keluar

`idempotency.DerivedKey(ctx, "payment")` menghasilkan `<key-klien>:payment`. `grpcclient` meneruskannya otomatis (`PropagateIdempotency: true`) sehingga retry berantai tetap aman.

## Test yang menjaga perilaku ini

`pkg/idempotency/storetest` (kontrak yang sama dijalankan untuk memory dan Redis): satu pemenang dari 32 goroutine, lease kedaluwarsa tidak bisa menimpa/menghapus lease baru, record tidak ter-alias. `middleware_test.go` / `grpc_test.go`: eksekusi tepat sekali, klien putus, panic, scope antar principal, tipe respons replay. `tests/integration/app_e2e_test.go`: Redis di-flush di tengah jalan, L2 tetap mencegah duplikasi.
