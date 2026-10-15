# Standalone build. The generated GraphQL code and the protobuf stubs are both
# committed, so the build needs nothing but Go: no protoc, no gqlgen, no network
# access to a code generator. That is what keeps `docker compose up --build` working
# for anyone with only Docker.
#
# The trade is that a schema change requires running ./hack/gen.sh and committing the
# result. See graph/gqlgen.yml for why generation is a host step rather than a build
# step.

# --- build stage ---
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Dependencies are cached separately from the source, so editing Go code does not
# re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY gen ./gen
COPY graph ./graph

# CGO off produces a static binary that runs on the scratch-like runtime stage.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/aggregator-service ./cmd/server

# --- runtime stage ---
FROM alpine:3.20

# ca-certificates for outbound TLS to object storage; tzdata so timestamps render in
# local time rather than UTC-only.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 65532 -H app

WORKDIR /app
COPY --from=builder /out/aggregator-service /bin/aggregator-service

# The defaults here target a compose network. Running the binary outside compose means
# setting the gRPC targets explicitly, since they are the only thing it depends on.
ENV APP_PORT=8085 \
    LOG_FORMAT=json \
    LOG_LEVEL=info \
    # Off in production by default. Introspection hands a caller a machine-readable
    # inventory of the whole published schema.
    ENABLE_INTROSPECTION=false \
    ENABLE_PLAYGROUND=false

USER 65532:65532
EXPOSE 8085

ENTRYPOINT ["/bin/aggregator-service"]
