# syntax=docker/dockerfile:1

# ---------- Stage 1: frontend assets (Tailwind CSS) -------------------------
FROM oven/bun:1 AS assets
WORKDIR /src

# Lockfile-pinned install so image rebuilds are reproducible.
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile --production

# Tailwind scans .templ/.go for the classes it must emit, so the whole source
# tree must be present before css:build — copying only app.css produced a CSS
# with none of the module/page utilities (output.css is .dockerignore'd and
# regenerated here).
COPY . .
RUN bun run css:build

# ---------- Stage 2: templ generation + Go build ---------------------------
FROM golang:1.27-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src

# The contracts module is a separate go.mod in the same repository and the
# gateway imports it, so it must be on disk before `go mod download` resolves
# anything. There is no replace directive; the workspace resolves it.
COPY apps/web/go.mod apps/web/go.sum ./apps/web/
COPY contracts/go.mod contracts/go.sum ./contracts/
RUN cd apps/web && go mod download

COPY . .
COPY --from=assets /src/apps/web/internal/shared/web/assets/css/output.css ./apps/web/internal/shared/web/assets/css/output.css

# *_templ.go files are gitignored, so generate them in the image.
# templ is a Go tool dependency, so the module download layer above caches its
# complete command dependency graph as part of the normal, checksum-pinned build.
RUN cd apps/web && go tool templ generate

ARG VERSION=dev
ARG LDFLAGS=
RUN cd apps/web && CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X gogo/internal/shared/version.Version=${VERSION} ${LDFLAGS}" \
    -o /out/gogo ./cmd/server

# ---------- Stage 3: minimal non-root runtime ---------------------------------
# Alpine base with a static Go binary, a dedicated non-root user (UID 65532,
# the same account distroless uses, so a later distroless swap is drop-in) and
# the CA bundle for outgoing TLS. No shell is available to the app user.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
 && addgroup -S -g 65532 app \
 && adduser -S -u 65532 -G app app
COPY --from=build /out/gogo /usr/local/bin/gogo
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/gogo"]
