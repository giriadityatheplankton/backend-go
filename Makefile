.PHONY: help run run-examples run-worker migrate migrate-down test test-race test-examples test-integration test-cover arch lint vuln fmt tidy build clean remove-examples proto proto-lint docker-build docker-up docker-down k8s-render new

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
TAGS    ?=

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  %-18s %s\n", $$1, $$2}'

# -----------------------------------------------------------------------------
# Run
# -----------------------------------------------------------------------------
run: ## API + embedded worker (APP_ENV=development)
	APP_ENV=development go run -tags "$(TAGS)" ./cmd/api

run-examples: ## Same, with the order/user example modules
	$(MAKE) run TAGS=examples

run-worker: ## Worker only (outbox relay, consumers, jobs)
	APP_ENV=development EMBED_WORKER=false go run -tags "$(TAGS)" ./cmd/worker

migrate: ## Apply pending migrations (TAGS=examples includes example schemas)
	go run -tags "$(TAGS)" ./cmd/migrate

migrate-down: ## Revert last migration of a source: make migrate-down SOURCE=platform STEPS=1
	go run -tags "$(TAGS)" ./cmd/migrate down $(SOURCE) $(or $(STEPS),1)

# -----------------------------------------------------------------------------
# Test & quality
# -----------------------------------------------------------------------------
test: ## Unit + architecture tests
	go test -race -count=1 ./...

test-examples: ## Unit tests including the example modules
	go test -race -count=1 -tags=examples ./...

test-integration: ## Needs Postgres + Redis: TEST_POSTGRES_DSN and TEST_REDIS_ADDR, or a container runtime
	go test -race -count=1 -tags=integration,examples ./tests/integration/...

test-cover:
	go test -race -coverprofile=coverage.out -tags=examples ./...
	go tool cover -html=coverage.out -o coverage.html

arch: ## Dependency Rule check only
	go test -count=1 ./tests/arch/...

lint: ## golangci-lint (includes depguard layer rules)
	golangci-lint run

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

fmt:
	gofmt -w $$(git ls-files '*.go' | grep -v '^gen/')

tidy:
	go mod tidy

# -----------------------------------------------------------------------------
# Protobuf (buf). Plugins: protoc-gen-go and protoc-gen-go-grpc must be on PATH.
# -----------------------------------------------------------------------------
proto: ## Generate Go code from api/proto into gen/
	buf generate

proto-lint:
	buf lint

# -----------------------------------------------------------------------------
# Build & containers
# -----------------------------------------------------------------------------
build: ## Static binaries in bin/
	CGO_ENABLED=0 go build -trimpath -tags "$(TAGS)" -ldflags="-s -w" -o bin/api ./cmd/api
	CGO_ENABLED=0 go build -trimpath -tags "$(TAGS)" -ldflags="-s -w" -o bin/worker ./cmd/worker
	CGO_ENABLED=0 go build -trimpath -tags "$(TAGS)" -ldflags="-s -w" -o bin/migrate ./cmd/migrate

clean:
	rm -rf bin/ coverage.out coverage.html

docker-build: ## Multi-arch capable image (single arch locally)
	docker build -f deployments/docker/Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg GO_TAGS="$(TAGS)" -t backend-go:$(VERSION) .

docker-up:
	GO_TAGS="$(TAGS)" docker compose up --build

docker-down:
	docker compose down

k8s-render: ## Render an overlay: make k8s-render ENV=prod
	kubectl kustomize deployments/k8s/overlays/$(or $(ENV),dev)

# -----------------------------------------------------------------------------
# Remove the example modules to start a real service from this skeleton.
# -----------------------------------------------------------------------------
remove-examples:
	rm -rf internal/modules/order internal/modules/user \
	       migrations/order migrations/user migrations/modules_examples.go \
	       api/proto/order api/proto/user gen/proto/order gen/proto/user \
	       internal/app/modules_examples.go tests/integration/app_e2e_test.go tests/integration/grpcopts_test.go \
	       docs/example_order_workflow_guide.md docs/example_proto_migration_guide.md
	sed -i '/^\/\/go:build !examples$$/,+1d' internal/app/modules_none.go migrations/modules_none.go
	@echo "Examples removed. Add your modules under internal/modules/<name> and register them in internal/app/modules_none.go (buildModules) and migrations/modules_none.go (moduleSources)."

# -----------------------------------------------------------------------------
# Scaffold a new service from this template.
# -----------------------------------------------------------------------------
new: ## make new path=../my-service module=github.com/acme/my-service
	@if [ -z "$(path)" ] || [ -z "$(module)" ]; then \
		echo "Usage: make new path=../my-new-app module=my-new-app"; exit 1; fi
	@mkdir -p "$(path)" && rsync -a --exclude='bin' --exclude='.git' ./ "$(path)"/
	@perl -pi -e 's|module backend-go|module $(module)|g' "$(path)/go.mod"
	@find "$(path)" -type f \( -name '*.go' -o -name '*.proto' -o -name '*.yaml' -o -name '*.yml' -o -name 'Makefile' -o -name 'Dockerfile' \) -not -path '*/gen/*' \
		-exec perl -pi -e 's|backend-go/|$(module)/|g; s|"backend-go"|"$(module)"|g' {} +
	@cd "$(path)" && (command -v buf >/dev/null && buf generate || true) && go mod tidy && go test ./...
	@echo "Created $(path)"
