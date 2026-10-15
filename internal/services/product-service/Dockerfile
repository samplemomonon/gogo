# Multi-stage Dockerfile that uses a Nix-built binary when available,
# but falls back to building with Go if Nix is not available.
#
# Preferred workflow:
#   nix build .#product-service
#   docker build --build-arg NIX_BINARY=./result/bin/product-service -t product-service .
#
# Fallback (no Nix):
#   docker build -t product-service .

# --- build stage (fallback only) ---
FROM golang:1.27-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY gen ./gen

ENV CGO_ENABLED=0
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN go build -trimpath \
      -ldflags="-s -w \
        -X product-service/internal/version.Version=$VERSION \
        -X product-service/internal/version.Commit=$COMMIT \
        -X product-service/internal/version.Date=$DATE" \
      -o /out/product-service ./cmd/server

# --- Nix binary stage ---
# This stage copies a pre-built binary from Nix (if provided via build arg).
# Always creates /out/product-service (empty if no Nix binary) so the COPY below succeeds.
FROM alpine:3.20 AS nix-binary
ARG NIX_BINARY
RUN mkdir -p /out && \
    if [ -n "$NIX_BINARY" ] && [ -f "$NIX_BINARY" ]; then \
      cp "$NIX_BINARY" /out/product-service; \
    else \
      touch /out/product-service; \
    fi

# --- runtime stage ---
FROM alpine:3.20

# ca-certificates for outbound TLS; tzdata so TIMESTAMPTZ renders in local time.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 65532 -H app

WORKDIR /app

# Copy binary: prefer Nix build if it produced a non-empty file, else use Go builder
COPY --from=nix-binary /out/product-service /tmp/nix-binary
COPY --from=builder /out/product-service /tmp/go-binary
RUN if [ -s /tmp/nix-binary ]; then \
      cp /tmp/nix-binary /bin/product-service; \
    else \
      cp /tmp/go-binary /bin/product-service; \
    fi

ENV APP_PORT=8082 \
    GRPC_PORT=9092 \
    LOG_FORMAT=json \
    LOG_LEVEL=info

USER 65532:65532
EXPOSE 8082 9092

ENTRYPOINT ["/bin/product-service"]