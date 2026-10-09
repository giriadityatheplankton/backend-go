# Panduan Migrasi: Konfigurasi Lokal (.env) ke Centralized Config Platform (Consul / Vault / Cloud KMS)

Dokumen ini menjelaskan arsitektur, strategi, dan langkah-langkah teknis untuk memindahkan pengelolaan konfigurasi dari file lokal (`.env`) ke platform terpusat seperti **HashiCorp Consul KV**, **HashiCorp Vault**, atau **AWS SSM / Secrets Manager**.

---

## 1. Mengapa Pindah ke Centralized Configuration?

| Masalah pada `.env` / Repo Config | Solusi Centralized Config (Consul / Vault / ESO) |
| :--- | :--- |
| Secrets (password DB, API key) rawan bocor ke source code. | Secrets dienkripsi saat *rest* dan *transit* dengan audit access log. |
| Setiap perubahan config mengharuskan *redeploy* atau *restart pod*. | Dynamic Reload: Nilai config/feature-flag dapat diperbarui saat runtime. |
| Inkonsistensi nilai konfigurasi antar replika pod di K8s. | Satu sumber kebenaran terpusat (*Single Source of Truth*). |
| Sulit melakukan rotasi credentials database secara berkala. | Mendukung auto-rotation credentials (misal via Vault dynamic secrets). |

---

## 2. Pilihan Pola Arsitektur (Architectural Approaches)

Terdapat 2 pola utama untuk integrasi konfigurasi terpusat:

```text
Pola 1: Kubernetes External Secrets Operator (ESO) [DIREKOMENDASIKAN]
┌────────────────┐      ┌─────────────────────────┐      ┌──────────────────┐
│ HashiCorp      │ ───> │ External Secrets        │ ───> │ K8s Secret /     │ ───> Pod Envs
│ Consul / Vault │      │ Operator (ESO Daemon)   │      │ ConfigMap        │     (Tanpa ubah kode Go)
└────────────────┘      └─────────────────────────┘      └──────────────────┘

Pola 2: Direct Go Application SDK Integration (Jika butuh Live In-Memory Reload)
┌────────────────┐      gRPC / HTTP
│ HashiCorp      │ ─────────────────────────────────> Go Backend Config Engine
│ Consul KV      │ <── Consul Watcher (Long-polling)    (atomic.Pointer[Config])
└────────────────┘
```

---

## 3. Pendekatan 1: Cloud-Native K8s External Secrets Operator (Zero Code Change)

Pendekatan ini merupakan *best practice* di Kubernetes karena **tidak memerlukan perubahan kode Go sama sekali** dan menjaga prinsip *12-Factor App*.

### A. Buat `SecretStore` di Kubernetes
Menghubungkan cluster K8s ke HashiCorp Consul / Vault:

```yaml
# deployments/k8s/secret-store.yaml
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: consul-backend-store
  namespace: default
spec:
  provider:
    vault:
      server: "https://vault.internal.company.com"
      path: "secret"
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "backend-go-role"
```

### B. Buat `ExternalSecret`
Otomatis mengambil data dari Consul/Vault dan membuat K8s Secret:

```yaml
# deployments/k8s/external-secret.yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: backend-go-external-secrets
  namespace: default
spec:
  refreshInterval: "1h"
  secretStoreRef:
    name: consul-backend-store
    kind: SecretStore
  target:
    name: backend-go-secrets
    creationPolicy: Owner
  data:
    - secretKey: DB_PRIMARY_DSN
      remoteRef:
        key: production/backend-go/database
        property: primary_dsn
    - secretKey: REDIS_PASSWORD
      remoteRef:
        key: production/backend-go/redis
        property: password
```

---

## 4. Pendekatan 2: Direct Go Integration (Dynamic Runtime Reload)

Jika aplikasi membutuhkan kemampuan reload konfigurasi secara *real-time* tanpa restart container (contoh: mengubah nilai `RATE_LIMIT_REQUESTS` atau `CACHE_TTL` saat traffic spike).

### A. Desain Interface `ConfigProvider`
Tambahkan interface di `internal/config/provider.go`:

```go
package config

import (
	"context"
)

// Provider defines the contract for fetching configuration dynamically.
type Provider interface {
	Load(ctx context.Context) (*Config, error)
	Watch(ctx context.Context, onChange func(*Config)) error
}
```

### B. Implementasi Consul KV Provider
Gunakan SDK resmi `github.com/hashicorp/consul/api`:

```bash
go get github.com/hashicorp/consul/api
```

Contoh implementasi adapter:

```go
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hashicorp/consul/api"
)

type ConsulProvider struct {
	client  *api.Client
	kvPath  string
	current atomic.Pointer[Config]
}

func NewConsulProvider(consulAddr, kvPath string) (*ConsulProvider, error) {
	cfg := api.DefaultConfig()
	if consulAddr != "" {
		cfg.Address = consulAddr
	}

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to consul: %w", err)
	}

	return &ConsulProvider{
		client: client,
		kvPath: kvPath,
	}, nil
}

func (p *ConsulProvider) Load(ctx context.Context) (*Config, error) {
	kv := p.client.KV()
	pair, _, err := kv.Get(p.kvPath, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to read consul key %s: %w", p.kvPath, err)
	}
	if pair == nil {
		return nil, fmt.Errorf("consul key %s not found", p.kvPath)
	}

	var cfg Config
	if err := json.Unmarshal(pair.Value, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config json: %w", err)
	}

	p.current.Store(&cfg)
	return &cfg, nil
}

// Watch listens for real-time changes in Consul KV via long-polling (Blocking Queries).
func (p *ConsulProvider) Watch(ctx context.Context, onChange func(*Config)) error {
	var lastIndex uint64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		opts := &api.QueryOptions{
			WaitIndex: lastIndex,
			WaitTime:  10 * time.Minute,
		}

		pair, meta, err := p.client.KV().Get(p.kvPath, opts.WithContext(ctx))
		if err != nil {
			slog.Warn("Consul watch error, retrying in 5s", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if meta.LastIndex > lastIndex && pair != nil {
			lastIndex = meta.LastIndex
			var newCfg Config
			if err := json.Unmarshal(pair.Value, &newCfg); err == nil {
				p.current.Store(&newCfg)
				slog.Info("Configuration dynamically reloaded from Consul", "kv_path", p.kvPath)
				if onChange != nil {
					onChange(&newCfg)
				}
			}
		}
	}
}

func (p *ConsulProvider) Get() *Config {
	return p.current.Load()
}
```

---

## 5. Struktur Payload Konfigurasi di Consul KV

Simpan konfigurasi pada key path: `config/backend-go/production`

```json
{
  "server_address": ":8080",
  "grpc_address": ":50051",
  "app_env": "production",
  "read_timeout": "10s",
  "write_timeout": "10s",
  "shutdown_timeout": "15s",
  "db_primary_dsn": "postgres://user:pass@primary-db:5432/appdb?sslmode=require",
  "db_replica_dsn": "postgres://user:pass@replica-db:5433/appdb?sslmode=require",
  "db_max_open_conns": 50,
  "db_max_idle_conns": 20,
  "db_conn_max_lifetime": "15m",
  "redis_address": "redis-cluster:6379",
  "redis_db": 0,
  "cache_ttl": "5m",
  "nats_address": "nats://nats-cluster:4222",
  "outbox_partitions": 16,
  "outbox_batch_size": 200,
  "outbox_poll_interval": "100ms",
  "outbox_max_retries": 5,
  "idempotency_ttl": "24h",
  "rate_limit_requests": 2000,
  "rate_limit_window": "1m"
}
```

---

## 6. Tahapan Eksekusi Migrasi

1. **Setup Remote Store**: Siapkan path KV di Consul / secret engine di Vault.
2. **Setup Kubernetes ESO**: Pasang `SecretStore` dan `ExternalSecret` di cluster Kubernetes target.
3. **Validasi K8s Secret**: Pastikan K8s secret `backend-go-secrets` terisi otomatis dari remote store.
4. **Deploy Aplikasi**: Jalankan rolling update; aplikasi akan otomatis membaca environment variables dari K8s secret tanpa perlu perubahan kode.
5. **Hapus File Lokal `.env`**: Hapus file `.env` di server staging/production (hanya pertahankan `.env.example` sebagai referensi developer).
