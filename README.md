# Go Backend Clean Architecture Template (Gin & gRPC)

Production-grade Go backend architecture featuring Gin HTTP, gRPC delivery, transactional Outbox worker publishing to Redis Streams, three-layer idempotency engine, W3C distributed tracing, resilient rate limiting & circuit breakers, and multi-tenant read/write database routing.

## Project Structure

```plaintext
backend-go/
├── cmd/
│   ├── api/main.go                 # HTTP/gRPC server entry point (wiring lives in internal/app)
│   ├── worker/main.go              # Outbox relay, stream consumers, background jobs
│   └── migrate/main.go             # Schema migrator
├── api/
│   └── proto/                      # Protobuf contracts (buf); sample services are optional
├── gen/
│   └── proto/                      # Generated Go code (do not edit)
├── deployments/
│   ├── docker/                     # Multi-stage hardened multi-arch Dockerfile
│   └── k8s/                        # Kustomize: base/ (Deployments, Job, NetPol, HPA, PDB) + overlays/
├── docs/
│   ├── architecture.md                    # Layers, dependency rule, how to add a module
│   ├── idempotency_guide.md               # Three-layer idempotency design
│   ├── messaging_guide.md                 # Redis Streams producers/consumers, inbox, DLQ
│   ├── proto_guide.md                     # buf workflow and gRPC conventions
│   ├── review_resolution.md               # Status of every finding in review.md
│   ├── infra_operations_guide.md          # Panduan deployment Kubernetes, observability, dan runbook
│   ├── postgres_replication_guide.md      # Panduan PostgreSQL critical reads dan replication lag
│   ├── redis_replication_guide.md         # Panduan Redis routing, critical reads, dan replication lag
│   ├── contributing_guide.md               # Panduan developer untuk berkontribusi
│   ├── audit_guide.md                       # Pilihan audit log-only atau persistence PostgreSQL
│   ├── outbox_guide.md                      # PostgreSQL persistence, transactions, worker setup, and adapter extensibility
│   ├── reconciliation_guide.md              # Implementing and operating reconciliation jobs
│   ├── configuration_management_migration_guide.md # Centralized Config (Consul/Vault/ESO) migration guide
├── internal/
│   ├── app/                        # Composition root (infra, HTTP, gRPC, workers, shutdown)
│   ├── config/                     # Configuration loader and Validate()
│   ├── kernel/                     # Shared error taxonomy (apperr), clock, ids
│   ├── modules/
│   │   └── <module>/               # One bounded context (samples: order, user; build tag `examples`)
│   │       ├── domain/             # Entities, value objects, domain events, errors
│   │       ├── usecase/            # Use cases; usecase/port holds the interfaces they need
│   │       ├── adapter/inbound/    # http, grpc, consumer, job
│   │       ├── adapter/outbound/   # postgres, redis (data access, caching)
│   │       └── module.go
│   └── platform/
│       ├── audit/                  # Immutable Audit Trail logging
│       ├── auth/                   # Principal, JWT / static-token auth (HTTP & gRPC)
│       ├── cache/                  # Singleflight Stampede-Protected Cache Manager
│       ├── database/               # Read/Write Splitting, TxManager, Dynamic Multi-Tenant DB Router, migrate/
│       ├── grpcclient/             # Outbound gRPC client (deadline, retry, breaker)
│       ├── grpcserver/             # gRPC server (health, reflection, keepalive)
│       ├── health/                 # Non-blocking K8s Liveness & Readiness Probes
│       ├── httpserver/             # Router, server, middleware/ (Timeout, Security Headers, CORS, Recovery), respond/
│       ├── idempotency/            # Universal Idempotency Engine (HTTP & gRPC middleware)
│       ├── messaging/              # Publisher/Consumer contracts, redisstream/, inbox/
│       ├── observability/          # metrics/ (Prometheus), telemetry/ (W3C TraceContext & slog TraceHandler)
│       ├── outbox/                 # Transactional Outbox Worker, Distributed Lock, DLQ, Cleaner
│       ├── reconciliation/         # Background Self-Healing & Transaction Reconciliation
│       ├── redisclient/            # single / sentinel / cluster Redis client
│       ├── resilience/             # Distributed Rate Limiting (Sliding Window) & Circuit Breaker
│       └── shutdown/               # Phased Graceful Shutdown Engine
├── migrations/                     # SQL per owner: platform/ and one folder per module
├── tests/
│   ├── arch/                       # Dependency Rule tests
│   └── integration/                # End-to-End tests (PostgreSQL + Redis)
├── Makefile
├── go.mod
└── README.md
```

Panduan memulai kontribusi tersedia di
[docs/contributing_guide.md](docs/contributing_guide.md).

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE)
for the full license text.

---

## Enterprise Modules & Features

### 1. Advanced Outbox Worker & Redis Streams (`internal/platform/outbox`, `internal/platform/messaging`)
- **Row Leasing (`worker.go`)**: Events are claimed in PostgreSQL with `FOR UPDATE SKIP LOCKED` and a lease, so multi-pod safety no longer depends on Redis. Expired leases are reclaimed after a crash.
- **Distributed Lock (`lock.go`)**: Redis `SET NX PX` with atomic Lua release/extension, kept for singleton jobs (cleaner, reconciliation).
- **Partitioned Batching (`worker.go`)**: Multi-partition worker pool for high-throughput event processing, with optional per-aggregate ordering.
- **Dead Letter Queue (`DLQ`)**: Automatic routing to DLQ when event retry limit is reached with exponential backoff and jitter.
- **Redis Streams (`messaging/redisstream`)**: Publisher plus consumer groups with `XAUTOCLAIM`, retries, a DLQ stream, and trimming. The `inbox` package de-duplicates messages on the consumer side.
- **Trace Context Propagation**: Preserves W3C `traceparent` and `X-Request-ID` from request headers to message broker payload headers.
- **Persistence**: `memory_repository.go` provides the in-memory test adapter and `postgres_repository.go` provides PostgreSQL persistence backed by `database/sql`. The PostgreSQL repository can share the business transaction when saving events. See the [Outbox Package Guide](docs/outbox_guide.md) and [Messaging Guide](docs/messaging_guide.md).

### 2. Idempotency Engine (`internal/platform/idempotency`)
- **Three layers**: edge (Redis), database (`UNIQUE` key + fingerprint, source of truth), and consumer (inbox). See the [Idempotency Guide](docs/idempotency_guide.md).
- **HTTP Middleware (`HTTPMiddleware`)**: Intercepts requests with the `Idempotency-Key` header (`X-Idempotency-Key` still accepted), scoped per authenticated principal, holds a fenced lease for in-flight processing, caches response payloads, and replays completed responses on duplicate calls.
- **gRPC Interceptor (`UnaryServerInterceptor`)**: Intercepts gRPC calls carrying `idempotency-key` metadata, preventing duplicate execution and replaying the cached response with its correct Protobuf type.

### 3. Distributed Tracing & Telemetry (`internal/platform/observability/telemetry`)
- **W3C TraceContext Propagator (`trace.go`)**: Seamless context injection/extraction across HTTP headers, gRPC metadata, broker headers, and Go `context.Context`.
- **Structured Logger Handler (`slog_handler.go`)**: Decorates `slog.Handler` to automatically attach `trace_id`, `span_id`, and `request_id` to every structured log entry.

### 4. Resiliency & Rate Limiting (`internal/platform/resilience`)
- **Distributed Sliding Window Rate Limiter (`ratelimit.go`)**: Enforces rate limits per authenticated principal (tenant/user) or per-IP using Redis Sorted Sets and atomic Lua scripts. Client-supplied `X-Tenant-ID` / `X-User-ID` headers are not trusted.
- **Circuit Breaker (`circuitbreaker.go`)**: Powered by `github.com/sony/gobreaker/v2` with state machine (`CLOSED`, `OPEN`, `HALF_OPEN`) protecting the outbox publisher and downstream HTTP and gRPC service integrations with automatic fallback execution.
- **gRPC Resiliency Interceptors (`grpc.go`)**: Native `UnaryServerRateLimitInterceptor` and `UnaryClientCircuitBreakerInterceptor`.

### 5. Database Optimization & Multi-Tenancy (`internal/platform/database`)
- **Read/Write Splitting (`DBGroup`)**: Menyediakan `Write()` untuk mutasi, `ReadPrimary()` untuk critical reads, dan `Read()` untuk query toleran-stale yang dibagi round-robin ke read replicas yang sehat.
- **Transactions (`tx.go`)**: `TxManager.WithinTx` with automatic retry of serialization failures and deadlocks.
- **Critical Read Guidance**: Gunakan PostgreSQL primary untuk read-after-write dan keputusan critical; lewati cache Redis untuk data authoritative. Lihat [Panduan PostgreSQL Replication Lag](docs/postgres_replication_guide.md) dan [Panduan Redis Replication Lag](docs/redis_replication_guide.md) untuk panduan spesifik masing-masing datastore.
- **Optional examples**: sample user/order modules, workflows, migrations, and guides are excluded from the default build. Run with `make run-examples`, test with `make test-examples`, or remove all example code in one step with `make remove-examples`.
- **Dynamic Tenant Router (`DynamicTenantRouter`)**: Resolves tenant-specific database connection pools dynamically based on `TenantID` in context. Unknown tenants return an error unless the default fallback is enabled explicitly.
- **Embedded Migration Engine (`internal/platform/database/migrate`, `migrations/`)**: Programmatic DDL execution using `go:embed` SQL files with transaction safety, advisory locking, checksums, and version tracking (`schema_history`).

### 6. Kubernetes Probes, Observability & Graceful Shutdown
- **Health Probes (`internal/platform/health`)**: Non-blocking asynchronous checks for Primary/Replica DB and Redis at `/healthz/live` and `/healthz/ready` on the admin port (`:9090`). Replica and cache failures are reported as degraded, not unready.
- **Prometheus Metrics (`internal/platform/observability/metrics`)**: Exposes HTTP/gRPC RED metrics, idempotency, outbox, consumer, circuit breaker, and DB connection pool stats at `/metrics` on the admin port.
- **Phased Graceful Shutdown (`internal/platform/shutdown`)**: Multi-phase termination handling SIGTERM/SIGINT (Readiness flip -> Ingress drain -> Listener shutdown -> Worker drain -> Client cleanup).
- **Context Deadlines & Timeouts (`internal/platform/httpserver/middleware/timeout.go`)**: Global request timeout injection with downstream deadline propagation.
- **Go Runtime & Concurrency Hardening**: `automaxprocs` integration for Kubernetes CPU quota alignment and pprof served only from the admin server when `PPROF_ENABLED=true`.

### 7. Security, Auditing & Panic Recovery
- **Panic Recovery Middleware (`internal/platform/httpserver/middleware/recovery.go`)**: Recovers runtime panics across Gin and gRPC with structured JSON logs preserving contextual trace identifiers; panic details are never returned to clients.
- **Security Headers & Strict CORS (`internal/platform/httpserver/middleware/security.go`)**: OWASP-aligned response headers (`X-Frame-Options`, `X-Content-Type-Options`, `HSTS`, `CSP`) and deny-by-default CORS.
- **Authentication (`internal/platform/auth`)**: JWT or static-token auth that places a `Principal` in the context for HTTP and gRPC.
- **Audit Trail (`internal/platform/audit`)**: Catat aksi penting ke structured log saja atau simpan secara persisten di PostgreSQL; gunakan transaksi yang sama dengan perubahan bisnis jika audit wajib atomik. Lihat [Panduan Paket Audit](docs/audit_guide.md).
- **Outbox Retention Cleaner (`internal/platform/outbox/cleaner.go`)**: Distributed locked background worker that purges processed records to prevent table bloat.

### 8. High Performance & Testability Patterns
- **Singleflight Cache Stampede Protection (`internal/platform/cache`)**: Utilizes `golang.org/x/sync/singleflight` to suppress redundant database queries during concurrent cache misses on high-traffic keys.
- **Self-Healing Reconciliation Engine (`internal/platform/reconciliation`)**: Background distributed-locked reconciliation daemon to resolve stuck/pending transactions with external providers. See the [Reconciliation Guide](docs/reconciliation_guide.md) for implementing and registering jobs.
- **Architecture Tests (`tests/arch`)**: Fail the build when a layer imports outward (for example a use case importing `database/sql`).
- **Integration Testing (`tests/integration`)**: Automated integration tests verifying PostgreSQL, Redis, DDL migrations, Outbox claiming, and the end-to-end HTTP/gRPC flow (`make test-integration`).

---

## Deployment & Kubernetes Manifests

Ready-to-use production assets located under `deployments/`:
- **Docker (`deployments/docker/Dockerfile`)**: Multi-stage multi-arch build with Go module caching, distroless non-root runtime, and the `api`, `worker`, and `migrate` binaries.
- **Kubernetes (`deployments/k8s/`)**: Kustomize `base/` plus `overlays/{dev,staging,prod}` (`make k8s-render ENV=prod`):
  - `api-deployment.yaml` / `worker-deployment.yaml`: Zero-downtime rolling update, security context, liveness/readiness/startup probes, and `preStop` hook.
  - `job-migration.yaml`: Pre-deployment DDL database migration Job.
  - `networkpolicy.yaml`: Microsegmentation restricting ingress/egress network traffic.
  - `hpa.yaml`: Horizontal Pod Autoscaler targeting CPU (70%) and Memory (80%).
  - `pdb.yaml`: Pod Disruption Budget guaranteeing minimum pod availability.
  - `service.yaml` & `configmap.yaml`: Networking and environment configurations.

---

## Module Organization

- **HTTP adapters (`internal/modules/<module>/adapter/inbound/http`)**:
  - Contains Gin controllers and routes; sample routes are opt-in with the `examples` build tag.
- **gRPC adapters (`internal/modules/<module>/adapter/inbound/grpc`)**:
  - Contains gRPC service implementations; sample handlers are opt-in with the `examples` build tag.
- **Server infrastructure** lives in `internal/platform/httpserver` and `internal/platform/grpcserver`.

---

## Configuration

Configuration values are loaded from environment-specific files based on `APP_ENV` (`.env.development`), falling back to `.env` and system environment variables. In production `.env` files are ignored; use real environment variables. Invalid values stop startup instead of silently using defaults.

### Environment Files:
- **`.env.development`**: Configured for local debugging with verbose `LOG_LEVEL=debug`, relaxed timeouts (`60s`) for IDE breakpoint inspection, local addresses, and lightweight Outbox concurrency.
- **`.env.production.example`**: Production template with `LOG_LEVEL=info`, strict timeouts, high-throughput Outbox worker partitioning, and tight rate limits. Do not commit real secrets.
- **`.env.example`**: Comprehensive reference listing all available configuration variables.

### Available Variables:

| Variable | Description | Development Default | Production Default |
|----------|-------------|---------------------|--------------------|
| `APP_ENV` | Application environment (`development` / `production`). | `development` | `production` |
| `LOG_LEVEL` | Structured log severity (`debug`, `info`, `warn`, `error`). | `debug` | `info` |
| `SERVER_ADDRESS` | HTTP server host and port. | `127.0.0.1:8080` | `:8080` |
| `GRPC_ADDRESS` | gRPC server host and port. | `127.0.0.1:50051` | `:50051` |
| `ADMIN_ADDRESS` | Probes, `/metrics`, and pprof server address. | `127.0.0.1:9090` | `:9090` (cluster-internal) |
| `PPROF_ENABLED` | Serve pprof on the admin server. | `true` | `false` |
| `READ_TIMEOUT` | HTTP server read timeout. | `60s` | `10s` |
| `WRITE_TIMEOUT` | HTTP server write timeout. | `60s` | `15s` |
| `SHUTDOWN_TIMEOUT` | Graceful shutdown timeout before forced kill. | `5s` | `15s` |
| `AUTH_MODE` | `none` (dev only), `static`, or `jwt`. | `static` | `jwt` |
| `DB_PRIMARY_DSN` | Primary PostgreSQL connection string (Write). | `postgres://.../appdb_dev` | `postgres://.../appdb` |
| `DB_REPLICA_DSNS` | Comma-separated read-replica connection strings (Read). | empty | `postgres://.../appdb` |
| `DB_MAX_OPEN_CONNS`| Max open connections in DB pool. | `10` | `20` |
| `DB_MAX_IDLE_CONNS`| Max idle connections in DB pool. | `5` | `10` |
| `DB_CONN_MAX_LIFETIME`| Max connection lifetime in pool. | `30m` | `30m` |
| `REDIS_MODE` | Redis mode (`single` / `sentinel` / `cluster`). | `single` | `cluster` |
| `REDIS_ADDRESSES` | Redis host and port list for idempotency, locks, and streams. | `127.0.0.1:6379` | `redis-cluster:6379` |
| `REDIS_CACHE_ADDRESSES` | Optional separate Redis for caching. | unset | separate instance |
| `CACHE_TTL` | Entity caching duration. | `1m` | `15m` |
| `STREAM_PREFIX` | Redis Streams namespace. | empty | service name |
| `OUTBOX_PARTITIONS`| Outbox processing partition count. | `2` | `16` |
| `OUTBOX_BATCH_SIZE`| Batch fetch size per partition. | `20` | `200` |
| `OUTBOX_POLL_INTERVAL`| Polling interval for pending events. | `500ms` | `100ms` |
| `IDEMPOTENCY_TTL` | Deduplication response cache duration. | `10m` | `24h` |
| `IDEMPOTENCY_FAILURE_MODE` | Behavior when Redis is down (`closed` / `open`). | `closed` | `closed` |
| `RATE_LIMIT_REQUESTS`| Sliding-window rate limit request count. | `1000` | `100` |
| `RATE_LIMIT_WINDOW`| Sliding-window duration. | `1m` | `1m` |

---

## Getting Started

### 1. Run the Server Locally (Development / Debug Mode)
```bash
docker compose up -d postgres redis
make migrate
make run
```
The server will run on `http://127.0.0.1:8080` with probes, metrics, and pprof at `http://127.0.0.1:9090`.

### 2. Run the Server in Production Mode
```bash
APP_ENV=production go run ./cmd/api
APP_ENV=production go run ./cmd/worker
```

### 3. Run the Optional Examples
```bash
make migrate TAGS=examples
make run-examples
```
The examples use the Go `examples` build tag and are not part of the default
application build. Use `make remove-examples` to delete their source, tests,
migrations, and example-only guides from a fork.

### 4. Run Unit & Integration Tests
```bash
# Run unit tests
make test

# Run integration tests (set TEST_POSTGRES_DSN and TEST_REDIS_ADDR, or use a Docker-compatible runtime)
make test-integration
```

### 5. Build the Binary
```bash
go build -o bin/api ./cmd/api
./bin/api

# Build the worker and the schema migrator used by deployments/k8s/base/job-migration.yaml
go build -o bin/worker ./cmd/worker
go build -o bin/migrate ./cmd/migrate
```

---

## Creating a New Project from this Template

You can easily instantiate a brand new project with a customized module path and name using the automated instructions below.

### Method A: Using Make (Recommended)
If `make` is installed on your system:
```bash
make new path=../my-new-project module=my-new-project
```

### Method B: Using Shell Commands
If `make` is not available, execute this command directly in your shell (adjust the path and module values as needed):
```bash
NEW_PATH="../my-new-project"
NEW_MODULE="my-new-project"

mkdir -p "$NEW_PATH"
rsync -av --exclude='bin' --exclude='.git' ./ "$NEW_PATH"/
perl -pi -e "s|module backend-go|module $NEW_MODULE|g" "$NEW_PATH/go.mod"
find "$NEW_PATH" -type f \( -name "*.go" -o -name "*.proto" -o -name "*.yaml" -o -name "*.yml" -o -name "Makefile" -o -name "Dockerfile" \) -not -path '*/gen/*' -exec perl -pi -e "s|backend-go|$NEW_MODULE|g" {} +
cd "$NEW_PATH" && go mod tidy && go test ./...
```
