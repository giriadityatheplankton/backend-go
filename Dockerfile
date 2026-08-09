# Stage 1: Build stage
FROM golang:1.26.1-alpine AS builder

# Install certificates and git
RUN apk --no-cache add ca-certificates git

WORKDIR /app

# Copy dependency files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go application binary
# -w -s flags reduce binary size by stripping debug symbols
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o app cmd/api/main.go

# Stage 2: Runtime stage
FROM alpine:3.19

# Add a non-root user for security
RUN adduser -D -g '' appuser

WORKDIR /app

# Copy built binary and certificates from the builder stage
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/app .

# Set container ownership to the non-root user
RUN chown -R appuser:appuser /app

# Switch to the non-root user
USER appuser

# Expose the application port
EXPOSE 8080

# Run the application
ENTRYPOINT ["./app"]
