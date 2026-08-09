.PHONY: run test mock lint build clean new docker-build docker-up docker-down

run:
	go run cmd/api/main.go

test:
	go test -v -cover ./...

# Command to generate mocks from interfaces using mockery
mock:
	mockery --all --keeptree

lint:
	golangci-lint run

build:
	go build -o bin/api cmd/api/main.go

clean:
	rm -rf bin/

new:
	@if [ -z "$(path)" ] || [ -z "$(module)" ]; then \
		echo "Error: path and module must be specified."; \
		echo "Usage example: make new path=../my-new-app module=my-new-app"; \
		exit 1; \
	fi
	@echo "Copying template to $(path)..."
	@mkdir -p $(path)
	@rsync -av --exclude='bin' --exclude='.git' ./ $(path)/
	@echo "Changing module name to $(module)..."
	@sed -i 's|module backend-go|module $(module)|g' $(path)/go.mod
	@echo "Updating imports in Go files..."
	@find $(path) -type f -name "*.go" -exec sed -i 's|"backend-go/|"$(module)/|g' {} +
	@echo "Running go mod tidy and tests in new project..."
	@cd $(path) && go mod tidy && go test -v ./...
	@echo "New project successfully created at $(path)!"

docker-build:
	docker build -t backend-go:latest .

docker-up:
	docker compose up --build

docker-down:
	docker compose down


