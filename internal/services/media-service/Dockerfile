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
RUN go build -trimpath -ldflags="-s -w" -o /out/media-service ./cmd/server

# --- runtime stage ---
FROM alpine:3.20

# ca-certificates for outbound TLS; tzdata so TIMESTAMPTZ renders in local time.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 65532 -H app

WORKDIR /app
COPY --from=builder /out/media-service /bin/media-service

ENV APP_PORT=8081 \
    GRPC_PORT=9091 \
    LOG_FORMAT=json \
    LOG_LEVEL=info

USER 65532:65532
EXPOSE 8081 9091

ENTRYPOINT ["/bin/media-service"]
