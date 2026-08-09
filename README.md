# Go Backend Clean Architecture Template (Gin)

A modular, testable, and clean Go backend template using the Gin HTTP framework, designed to be easily extensible and ready for CI/CD pipelines.

## Project Structure

```plaintext
backend-go/
├── .github/
│   └── workflows/
│       └── ci.yml             # GitHub Actions CI/CD pipeline configuration
├── cmd/
│   └── api/
│       └── main.go            # Application entry point (dependency injection & server startup)
├── internal/
│   ├── config/                # Environment variable loader (.env)
│   ├── domain/                # Data structures and interface definitions (Core Domain)
│   ├── handler/               # HTTP Delivery Layer (REST API Controllers)
│   ├── repository/            # Data Access Layer (Mock / DB Implementation)
│   └── usecase/               # Business Logic Layer
├── Makefile                   # Automation commands for building, running, and testing
├── go.mod                     # Go module definition
└── README.md                  # Project documentation
```

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
