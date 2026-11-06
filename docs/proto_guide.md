# Protobuf & gRPC

Kontrak ada di `api/proto/<service>/v<major>/*.proto`; kode hasil generate di `gen/proto/...` (**jangan diedit**). `gen/` hanya boleh di-import oleh `adapter/inbound/grpc` dan `app`.

```bash
# sekali: protoc-gen-go dan protoc-gen-go-grpc harus ada di PATH
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
make proto-lint   # buf lint
make proto        # buf generate
```

CI memeriksa `buf lint`, `buf breaking` terhadap `main`, dan bahwa `gen/` sudah mutakhir.

## Menambah service

1. Tulis `api/proto/<nama>/v1/<nama>.proto`; `option go_package = "backend-go/gen/proto/<nama>/v1;<nama>v1"`.
2. `make proto`.
3. Implementasikan `<Nama>ServiceServer` di `adapter/inbound/grpc/server.go`: panggil use case yang sama dengan HTTP, petakan error lewat `grpcserver.Error(err)`.
4. Daftarkan di `Module.RegisterGRPC`.

## Konvensi

- Versi di path dan package (`order.v1`); perubahan breaking = `v2` baru.
- Idempotency-key dikirim di **metadata** (`idempotency-key`), bukan di pesan; interceptor di `app/grpc.go` menanganinya.
- Auth lewat metadata `authorization: Bearer ...`; health dan reflection dikecualikan.
- Reflection hanya aktif bila `GRPC_REFLECTION=true` (ditolak validasi di produksi).
- Client keluar: `grpcclient.Dial` (deadline default, retry `UNAVAILABLE` opsional, circuit breaker, propagasi `traceparent`/`x-request-id`/key turunan). Aktifkan `Retry` hanya untuk RPC idempotent.
