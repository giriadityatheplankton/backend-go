# syntax=docker/dockerfile:1.7
# Single hardened, multi-arch Dockerfile (the root Dockerfile was removed: it built
# cmd/api/main.go as a single file and no longer compiled).

# ---- build -------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
RUN apk add --no-cache git ca-certificates tzdata
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
# Set GO_TAGS=examples to include the order/user example modules.
ARG GO_TAGS=""
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for b in api worker migrate; do \
      go build -trimpath -tags "$GO_TAGS" -ldflags="-s -w" -o /out/$b ./cmd/$b || exit 1; \
    done

# ---- runtime -----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="backend-go" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT"
ENV SERVICE_VERSION=$VERSION SERVICE_COMMIT=$COMMIT
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/api /out/worker /out/migrate /app/

USER 65532:65532
WORKDIR /app
# 8080 public HTTP, 50051 gRPC, 9090 admin (probes, metrics, pprof): keep 9090 cluster-internal.
EXPOSE 8080 50051 9090
ENTRYPOINT ["/app/api"]
