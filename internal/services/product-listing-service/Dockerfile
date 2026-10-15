# Standalone build for machines without Nix.
# The Nix flake (flake.nix) is the primary build system; this file exists so
# `docker compose up --build` keeps working for anyone who only has Docker.

# --- build stage ---
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Dependencies are cached separately from the source, so editing Go code does
# not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY gen ./gen

ENV CGO_ENABLED=0
# Build metadata is stamped at link time. Without it every image reports
# "dev (commit unknown)", which makes it impossible to tell which revision a
# running container is serving. The linker ignores an -X that names no symbol,
# so the import path below must be the module path from go.mod, not the
# directory name.
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN go build -trimpath \
      -ldflags="-s -w \
        -X github.com/ruangbercerita182-tech/product-listing-service/internal/version.Version=$VERSION \
        -X github.com/ruangbercerita182-tech/product-listing-service/internal/version.Commit=$COMMIT \
        -X github.com/ruangbercerita182-tech/product-listing-service/internal/version.Date=$DATE" \
      -o /out/product-listing-service ./cmd/server

# --- runtime stage ---
FROM alpine:3.20

# ca-certificates for outbound TLS; tzdata so TIMESTAMPTZ renders in local time.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 65532 -H app

WORKDIR /app
COPY --from=builder /out/product-listing-service /bin/product-listing-service

ENV APP_PORT=8083 \
    GRPC_PORT=9093 \
    LOG_FORMAT=json \
    LOG_LEVEL=info

USER 65532:65532
EXPOSE 8083 9093

ENTRYPOINT ["/bin/product-listing-service"]
