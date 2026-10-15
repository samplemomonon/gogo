BINARY       := bin/gogo
PKG          := ./cmd/server

IMAGE        ?= gogo
IMAGE_TAG    ?= latest
K8S_DIR      := deploy/k8s

# Build-time identity, injected into internal/shared/version and surfaced in
# the module registry and /metrics.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X gogo/internal/shared/version.Version=$(VERSION) \
               -X gogo/internal/shared/version.Commit=$(COMMIT) \
               -X gogo/internal/shared/version.BuildDate=$(BUILD_DATE)

.PHONY: all build run dev assets test vet check lint fmt tidy templ css css-watch \
	module buf buf-lint buf-generate buf-check buf-breaking arch-graph \
	docker-build docker-push k8s-build k8s-apply k8s-secrets k8s-delete clean

all: build

# Generated sources the Go build embeds: templ components and the compiled
# Tailwind stylesheet (both gitignored). Everything that compiles the module
# depends on them, so a fresh checkout never fails on the embed.
assets: templ css

build: assets
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

# Development: watch templ + CSS changes and run the server.
dev: templ css
	@templ generate --watch & \
	bun run css:watch & \
	go run $(PKG)

test: assets
	go test ./... -race

vet: assets
	go vet ./...

# Everything CI cares about in one shot: gofmt cleanliness, generated templ
# and proto contracts up to date, static analysis and the full (race-enabled)
# test suite.
#
# The gofmt check asks `go list` for the directories that actually build, rather
# than walking ./cmd and ./internal. Two reasons: a stray directory holding Go
# files that no module claims would otherwise be formatted and then ignored by
# the compiler, and a directory walk descends into tooling state that lives in
# the tree — two devenv environments were 675 MB of Go module cache each, and
# `gofmt -l ./internal` walked all of it on every run.
check: vet test
	@dirs="$$(go list -f '{{.Dir}}' ./... | sort -u)"; \
	test -z "$$(gofmt -l $$dirs)" || \
		(echo "gofmt needed on:"; gofmt -l $$dirs; exit 1)
	@before="$$(git status --porcelain -- internal cmd)"; \
	templ generate >/dev/null 2>&1; \
	after="$$(git status --porcelain -- internal cmd)"; \
	if [ "$$before" = "$$after" ]; then \
		echo "templates generated, up to date"; \
	else \
		echo "check failed: 'templ generate' changed files — commit or regenerate them first."; \
		git diff --stat; exit 1; \
	fi
	@if ! command -v buf >/dev/null; then \
		echo "check failed: 'buf' is not on PATH."; \
		echo "  The contract gate (buf lint / buf generate / buf breaking) cannot run without it,"; \
		echo "  and must not be skipped silently — a missing tool is not a passing check."; \
		echo "  Install: https://buf.build/docs/installation"; \
		exit 1; \
	fi; \
	before="$$(git status --porcelain -- contracts)"; \
	buf lint >/dev/null 2>&1 && buf generate >/dev/null 2>&1; \
	after="$$(git status --porcelain -- contracts)"; \
	if [ "$$before" = "$$after" ]; then \
		echo "buf lint passing, generated contracts, up to date"; \
	else \
		echo "check failed: 'buf generate' changed contracts — commit or regenerate them first."; \
		git diff --stat -- contracts; exit 1; \
	fi
	@$(MAKE) --no-print-directory buf-breaking
	@before="$$(cat internal/architecture/import_graph.txt 2>/dev/null)"; \
	go test ./internal/architecture/ -run ImportGraph >/dev/null 2>&1 || \
		(echo "check failed: the committed import graph is stale — run 'make arch-graph' and commit the result."; \
		 go test ./internal/architecture/ -run ImportGraph 2>&1 | sed -n '2,20p'; exit 1); \
	echo "import graph current"
	@echo "check passed: gofmt clean, templates and contracts generated, import graph current, vet and tests green"

# Static analysis beyond go vet (currently go vet; swap in staticcheck).
lint: vet buf-lint

# ---- Architecture rules ---------------------------------------------------

# Regenerate the committed import graph that internal/architecture reads.
#
# The architecture rules read this committed snapshot rather than shelling out
# to the toolchain on every run, so a boundary change shows up in review as a
# diff. Regeneration is the only half that needs a checkout: `make check`
# re-derives the graph and fails if the committed copy has drifted.
#
# It previously derived the graph by parsing the Bazel BUILD files. That no
# longer worked — Bazel is not installed here, has no MODULE.bazel, and only 44
# BUILD files survived, covering 43 of the repo's 119 packages — so the
# staleness gate had been failing unnoticed. It now derives from `go list`,
# which is what Gazelle itself consumed. See the package comment on
# internal/architecture/architecture_test.go.
#
# Run after adding a Go package or changing an import. `make check` fails if the
# committed copy is stale, so it cannot drift unnoticed.
arch-graph:
	go test ./internal/architecture/ -run ImportGraph -update

# ---- Contract (buf/protobuf) ----------------------------------------------

# Fail on an incompatible contract change against main (the base branch). A
# field may be deleted only when its number and name are reserved; every other
# FILE-level break fails. Needs the base ref present, so CI checks out fully.
buf-breaking:
	buf breaking --against '.git#branch=main'

# Lint the proto contract schemas (buf STANDARD) — no generation.
buf-lint:
	buf lint

# Regenerate the Go code in contracts/ from the proto schemas. Generated
# files are committed and reviewed in PRs like any source.
buf-generate: buf-lint
	buf generate

# Convenience target for the two-step contract workflow.
buf: buf-generate

fmt:
	gofmt -w $$(go list -f '{{.Dir}}' ./... | sort -u)

tidy:
	go mod tidy

# Regenerate Templ *_templ.go files from .templ sources.
templ:
	templ generate

# Build the Tailwind stylesheet once or in watch mode.
css:
	bun run css:build

css-watch:
	bun run css:watch

# Scaffold a new contract-compliant feature module:
#   make module name=billing desc="Subscription billing and invoices"
module:
	go run ./cmd/modulegen $(name) "$(desc)"

# ---- Container -----------------------------------------------------------

# Build the production image (multi-stage: Tailwind -> templ -> Go ->
# distroless). Defaults to local tag `gogo:latest`.
docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg LDFLAGS="$(LDFLAGS)" \
		-t $(IMAGE):$(IMAGE_TAG) .

docker-push: docker-build
	docker push $(IMAGE):$(IMAGE_TAG)

# ---- Kubernetes ----------------------------------------------------------

# Render the final manifest set (kustomize) without applying.
k8s-build:
	kubectl kustomize $(K8S_DIR)

# Apply the whole stack. Requires the gogo-secrets secret (see
# deploy/k8s/secret.example.yaml):  make k8s-secrets CHANGEME  first.
k8s-apply: k8s-build
	kubectl apply --kustomize $(K8S_DIR)

# Create the runtime secret from the example (replace CHANGEME values first).
k8s-secrets:
	@test -n "$$CHANGEME" || (echo "set CHANGEME with your DB password, e.g. make k8s-secrets CHANGEME=supersecret"; exit 1)
	kubectl -n gogo create secret generic gogo-secrets \
		--from-literal=POSTGRES_PASSWORD=$$CHANGEME \
		--from-literal=DATABASE_URL='postgres://gogo:'"$$CHANGEME"'@gogo-postgres.gogo.svc.cluster.local:5432/gogo?sslmode=disable' \
		--dry-run=client -o yaml | kubectl apply -f -

k8s-delete:
	kubectl delete --kustomize $(K8S_DIR)

clean:
	rm -rf $(BINARY) coverage
