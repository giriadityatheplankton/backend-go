.PHONY: run run-dev run-prod dev prod test test-integration test-cover mock lint build clean proto pprof-heap pprof-profile docker-build docker-up docker-down new

# -----------------------------------------------------------------------------
# Running Applications
# -----------------------------------------------------------------------------
run: run-dev

run-dev dev:
	APP_ENV=development go run cmd/api/main.go cmd/api/pprof.go

run-prod prod:
	APP_ENV=production go run cmd/api/main.go cmd/api/pprof.go

# -----------------------------------------------------------------------------
# Testing & Code Quality
# -----------------------------------------------------------------------------
test:
	go test -v -race -cover ./...

test-integration:
	go test -v -tags=integration ./tests/integration/...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated at coverage.html"

# Command to generate mocks from interfaces using mockery
mock:
	mockery --all --keeptree

lint:
	golangci-lint run --timeout=5m

# -----------------------------------------------------------------------------
# Diagnostics & Profiling
# -----------------------------------------------------------------------------
pprof-heap:
	go tool pprof http://127.0.0.1:6060/debug/pprof/heap

pprof-profile:
	go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30

# -----------------------------------------------------------------------------
# Build & Docker
# -----------------------------------------------------------------------------
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-w -s" -o bin/api cmd/api/main.go cmd/api/pprof.go

clean:
	rm -rf bin/ coverage.out coverage.html

docker-build:
	docker build -f deployments/docker/Dockerfile -t backend-go:latest .

docker-up:
	docker compose up --build

docker-down:
	docker compose down

# -----------------------------------------------------------------------------
# Scaffolding: Create New Project from Template
# -----------------------------------------------------------------------------
new:
	@if [ -z "$(path)" ] || [ -z "$(module)" ]; then \
		echo "Error: path and module must be specified."; \
		echo "Usage example: make new path=../my-new-app module=my-new-app"; \
		exit 1; \
	fi
	@echo "Copying template to $(path)..."
	@mkdir -p "$(path)"
	@rsync -av --exclude='bin' --exclude='.git' ./ "$(path)"/
	@echo "Changing module name to $(module)..."
	@if command -v perl >/dev/null 2>&1; then \
		perl -pi -e 's|module backend-go|module $(module)|g' "$(path)/go.mod"; \
		find "$(path)" -type f \( -name "*.go" -o -name "*.proto" -o -name "*.yaml" -o -name "*.yml" -o -name "Makefile" -o -name "Dockerfile" \) -exec perl -pi -e 's|backend-go|$(module)|g' {} +; \
	elif [ "$$(uname)" = "Darwin" ]; then \
		sed -i '' 's|module backend-go|module $(module)|g' "$(path)/go.mod"; \
		find "$(path)" -type f -name "*.go" -exec sed -i '' 's|"backend-go/|"$(module)/|g' {} +; \
	else \
		sed -i 's|module backend-go|module $(module)|g' "$(path)/go.mod"; \
		find "$(path)" -type f -name "*.go" -exec sed -i 's|"backend-go/|"$(module)/|g' {} +; \
	fi
	@echo "Running go mod tidy and tests in new project..."
	@cd "$(path)" && go mod tidy && go test ./...
	@echo "New project successfully created at $(path)!"



