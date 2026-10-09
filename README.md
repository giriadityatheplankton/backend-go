# Go Backend Clean Architecture Template (Gin & gRPC)

Production-grade Go backend architecture featuring Gin HTTP, gRPC delivery, transactional Outbox worker with distributed locking, idempotency engine, W3C distributed tracing, resilient rate limiting & circuit breakers, and multi-tenant read/write database routing.

## Project Structure

```plaintext
backend-go/
├── cmd/
│   └── api/
│       └── main.go                 # App entry point (DI & HTTP/gRPC server startup)
├── internal/
│   ├── config/                     # Configuration loader (.env & system environment)
│   ├── domain/                     # Domain entities, error types, repository/usecase interfaces
│   ├── events/                     # Message broker publishers (NATS / Kafka)
│   ├── handler/
│   │   ├── http/                   # REST API Delivery Layer (Gin controllers & routes)
│   │   └── grpc/                   # gRPC Delivery Layer (Service server adapters)
│   ├── pkg/
│   │   ├── database/               # Read/Write Splitting & Dynamic Multi-Tenant DB Router
│   │   ├── idempotency/            # Universal Idempotency Engine (HTTP & gRPC middleware)
│   │   ├── outbox/                 # Transactional Outbox Worker, Distributed Lock, DLQ
│   │   ├── resilience/             # Distributed Rate Limiting (Sliding Window) & Circuit Breaker
│   │   ├── response/               # Standardized JSON response helpers
│   │   └── telemetry/              # W3C TraceContext propagator & slog TraceHandler
│   ├── repository/                 # Data Access Layer with caching & DB persistence
│   └── usecase/                    # Business Logic Layer
├── proto/
│   └── user/v1/user.proto          # Protobuf service definitions
├── Makefile
├── go.mod
└── README.md
```

---

## Enterprise Modules & Features

### 1. Advanced Outbox Worker & Distributed Locking (`internal/pkg/outbox`)
- **Distributed Lock (`lock.go`)**: Multi-pod safe distributed locking using Redis `SET NX PX` and atomic Lua release/extension scripts.
- **Partitioned Batching (`worker.go`)**: Multi-partition worker pool for high-throughput event processing.
- **Dead Letter Queue (`DLQ`)**: Automatic routing to DLQ when event retry limit is reached with exponential backoff.
- **Trace Context Propagation**: Preserves W3C `traceparent` from request headers to message broker payload headers.

### 2. Idempotency Engine (`internal/pkg/idempotency`)
- **HTTP Middleware (`HTTPMiddleware`)**: Intercepts requests with `X-Idempotency-Key` header, locks in-flight processing, caches response payloads, and replays completed responses on duplicate calls.
- **gRPC Interceptor (`UnaryServerInterceptor`)**: Intercepts gRPC calls carrying `x-idempotency-key` metadata, preventing duplicate execution and replaying cached Protobuf/JSON responses.

### 3. Distributed Tracing & Telemetry (`internal/pkg/telemetry`)
- **W3C TraceContext Propagator (`trace.go`)**: Seamless context injection/extraction across HTTP headers, gRPC metadata, NATS/Kafka broker headers, and Go `context.Context`.
- **Structured Logger Handler (`slog_handler.go`)**: Decorates `slog.Handler` to automatically attach `trace_id` and `span_id` to every structured log entry.

### 4. Resiliency & Rate Limiting (`internal/pkg/resilience`)
- **Distributed Sliding Window Rate Limiter (`ratelimit.go`)**: Enforces rate limits per-tenant (`X-Tenant-ID`), per-user (`X-User-ID`), or per-IP using Redis Sorted Sets and atomic Lua scripts.
- **Circuit Breaker (`circuitbreaker.go`)**: Powered by `github.com/sony/gobreaker/v2` with state machine (`CLOSED`, `OPEN`, `HALF_OPEN`) protecting downstream HTTP and gRPC service integrations with automatic fallback execution.
- **gRPC Resiliency Interceptors (`grpc.go`)**: Native `UnaryServerRateLimitInterceptor` and `UnaryClientCircuitBreakerInterceptor`.

### 5. Database Optimization & Multi-Tenancy (`internal/pkg/database`)
- **Read/Write Splitting (`DBGroup`)**: Automatically routes mutation queries (`Write()`) to Primary DB and query operations (`Read()`) across read-replica pools using round-robin distribution.
- **Dynamic Tenant Router (`DynamicTenantRouter`)**: Resolves tenant-specific database connection pools dynamically based on `TenantID` in context.
- **Embedded Migration Engine (`internal/pkg/database/migration`)**: Programmatic DDL execution using `go:embed` SQL files with transaction safety and version tracking (`schema_migrations`).

### 6. Kubernetes Probes, Observability & Graceful Shutdown
- **Health Probes (`internal/pkg/health`)**: Non-blocking asynchronous checks for Primary/Replica DB, Redis, and NATS at `/healthz/live` and `/healthz/ready`.
- **Prometheus Metrics (`internal/pkg/metrics`)**: Exposes HTTP/gRPC RED metrics, outbox queue metrics, and DB connection pool stats at `/metrics`.
- **Phased Graceful Shutdown (`internal/pkg/shutdown`)**: Multi-phase termination handling SIGTERM/SIGINT (Readiness flip -> Ingress drain -> Listener shutdown -> Worker drain -> Client cleanup).
- **Context Deadlines & Timeouts (`internal/pkg/middleware/timeout.go`)**: Global request timeout injection with downstream deadline propagation.
- **Go Runtime & Concurrency Hardening**: `automaxprocs` integration for Kubernetes CPU quota alignment and isolated internal diagnostic server (`cmd/api/pprof.go` on `:6060`).

### 7. Security, Auditing & Panic Recovery
- **Panic Recovery Middleware (`internal/pkg/middleware/recovery.go`)**: Recovers runtime panics across Gin and gRPC with structured JSON logs preserving contextual trace identifiers.
- **Security Headers & Strict CORS (`internal/pkg/middleware/security.go`)**: OWASP-aligned response headers (`X-Frame-Options`, `X-Content-Type-Options`, `HSTS`, `CSP`) and fine-grained CORS.
- **Immutable Audit Trail (`internal/pkg/audit`)**: Captures mutation events with actor, before/after states, IP, user-agent, and trace IDs.
- **Outbox Retention Cleaner (`internal/pkg/outbox/cleaner.go`)**: Distributed locked background worker that purges processed records to prevent table bloat.

---

## Deployment & Kubernetes Manifests

Ready-to-use production assets located under `deployments/`:
- **Docker (`deployments/docker/Dockerfile`)**: Multi-stage build with Go module caching, non-root user `appuser:10001`, and minimal runtime image.
- **Kubernetes (`deployments/k8s/`)**:
  - `deployment.yaml`: Zero-downtime rolling update, security context, liveness/readiness/startup probes, and `preStop` hook.
  - `job-migration.yaml`: Pre-deployment DDL database migration Job.
  - `networkpolicy.yaml`: Microsegmentation restricting ingress/egress network traffic.
  - `hpa.yaml`: Horizontal Pod Autoscaler targeting CPU (70%), Memory (80%), and Prometheus metrics.
  - `pdb.yaml`: Pod Disruption Budget guaranteeing minimum pod availability.
  - `service.yaml` & `configmap.yaml`: Networking and environment configurations.

---

## Handler Layer Organization

- **HTTP Handlers (`internal/handler/http`)**:
  - Contains Gin REST controllers and route registration (e.g. `RegisterUserRoutes`).
- **gRPC Handlers (`internal/handler/grpc`)**:
  - Contains gRPC server implementations exposing usecases over Protobuf/gRPC (e.g. `UserGRPCHandler`).

---

## Prerequisites

- **Go**: Version 1.22 or higher.
- **Rsync** (optional, for template cloning): Usually pre-installed on Linux/WSL/macOS.

---

## Getting Started

### 1. Run the Server Locally
To start the HTTP server in development mode:
```bash
go run cmd/api/main.go
```
The server will run on `http://127.0.0.1:8080` by default.

### 2. Run Unit Tests
To run all unit tests and verify correctness:
```bash
go test -v -cover ./...
```

### 3. Build the Binary
To compile the application into a single executable binary:
```bash
go build -o bin/api cmd/api/main.go
```
Run the compiled binary:
```bash
./bin/api
```

---

## Configuration

Configuration values are loaded from environment variables or an optional `.env` file in the root directory.

| Variable | Description | Default Value |
|----------|-------------|---------------|
| `SERVER_ADDRESS` | The host and port where the HTTP server listens. | `127.0.0.1:8080` |
| `APP_ENV` | Application runtime environment (`development` / `production`). | `development` |

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
sed -i "s|module backend-go|module $NEW_MODULE|g" "$NEW_PATH/go.mod"
find "$NEW_PATH" -type f -name "*.go" -exec sed -i "s|\"backend-go/|\"$NEW_MODULE/|g" {} +
cd "$NEW_PATH" && go mod tidy && go test -v ./...
```
