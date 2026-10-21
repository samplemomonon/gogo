BINARY       := $(CURDIR)/bin/gogo
APP_DIR      := apps/web
GO           := go -C $(APP_DIR)
PKG          := ./cmd/server

IMAGE        ?= gogo
IMAGE_TAG    ?= latest
K8S_DIR      := deploy/k8s

# The platform services. Each is a separate Go module with its own go.mod,
# deployed on its own release train, so each is built and tested on its own
# module graph rather than on the gateway's.
#
# GOWORK=off is load-bearing, not hygiene. This repository ships a go.work, and
# a directory that is not listed in it but lives inside the workspace tree does
# not resolve to itself: a `go list ./...` run inside services/… for
# an unlisted service silently enumerates the *gateway's* packages. Measured:
# aggregator, product and product-search all returned an identical package list
# under workspace mode — the gateway's — which is how three services can look
# covered by a check that never touched them.
# The six platform services. Keep in step with ports.txt, which the port gate
# checks against every go.mod default and Dockerfile EXPOSE in here.
SERVICES     := aggregator-service category-service media-service product-listing-service product-search-service product-service
SERVICES_DIR := services

.PHONY: services-vet services-test gofmt-check page-builder-test

# Build-time identity, injected into internal/shared/version and surfaced in
# the module registry and /metrics.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X gogo/internal/shared/version.Version=$(VERSION) \
               -X gogo/internal/shared/version.Commit=$(COMMIT) \
               -X gogo/internal/shared/version.BuildDate=$(BUILD_DATE)

.PHONY: all build run dev assets test vet check lint fmt tidy templ css css-watch faker-products seed docs-site-build docs-site-preview docs-site-gate-test \
	buf buf-lint buf-generate buf-check buf-breaking arch-graph \
	feature arch-team-catalog \
	docker-build docker-push k8s-build k8s-apply k8s-secrets k8s-delete clean \
	up up-platform down restart logs ps reset smoke smoke-platform dev-pull

all: build

# Generated sources the Go build embeds: templ components and the compiled
# Tailwind stylesheet (both gitignored). Everything that compiles the module
# depends on them, so a fresh checkout never fails on the embed.
assets: templ css

build: assets
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

run: build
	$(BINARY)

# Development: watch templ + CSS changes and run the server.
#
# The server runs from the repository root (plain `go run`, not `go -C
# apps/web`): the config loader reads `.env` from the working directory, and the
# root is where `.env` sits, next to `.env.example` — the same file `make run`
# and the quick start use.
dev: templ css
	@bun --watch design-system/web/build.mjs & \
	go -C design-system/web/ui tool templ generate --watch & \
	cd $(APP_DIR) && templ generate --watch & \
	bun run css:watch & \
	go run ./apps/web/cmd/server

test: assets
	$(GO) test ./... -race

# The editor's document operations, undo/redo, validation, and autosave
# coordinator are pure ES modules, so they run under Node's built-in test
# runner without a browser or extra dependencies.
page-builder-test:
	node --test apps/web/internal/shared/web/assets/js/page-builder-core.test.js

vet: assets
	$(GO) vet ./...

# Static analysis for the platform services, on their own module graphs.
services-vet:
	@set -e; for svc in $(SERVICES); do \
		echo "==> vet $$svc"; \
		(cd "$(SERVICES_DIR)/$$svc" && GOWORK=off go vet ./...); \
	done

# The platform test suites, including each service's own architecture tests.
# aggregator-service carries the tripwire that keeps it a composition layer, and
# this target is the only thing that ever runs it.
services-test:
	@set -e; for svc in $(SERVICES); do \
		echo "==> test $$svc"; \
		(cd "$(SERVICES_DIR)/$$svc" && GOWORK=off go test ./... -race); \
	done

# Everything CI cares about in one shot, across every module in the repository:
# gofmt cleanliness, generated templ and proto contracts up to date, static
# analysis, and the race-enabled test suite.
#
# "Every module" is the part this comment used to get wrong. It said "the full
# test suite" while depending on `go test ./...`, which resolves within the root
# module only — 97 of the repository's 172 packages. The four platform services
# were never built and never tested, so a green CI run said nothing about them
# and the aggregator's composition-layer tripwire never executed. services-vet
# and services-test are now prerequisites for the same reason.
#
# The gofmt check asks `go list` for the directories that actually build, rather
# than walking ./cmd and ./internal. Two reasons: a stray directory holding Go
# files that no module claims would otherwise be formatted and then ignored by
# the compiler, and a directory walk descends into tooling state that lives in
# the tree — two devenv environments were 675 MB of Go module cache each, and
# `gofmt -l ./internal` walked all of it on every run.
gofmt-check:
	@dirs="$$($(GO) list -f '{{.Dir}}' ./... | sort -u)"; \
	 dirs="$$dirs $$(go -C design-system/web/ui list -f '{{.Dir}}' ./... | sort -u)"; \
	for svc in $(SERVICES); do \
		dirs="$$dirs $$(cd "$(SERVICES_DIR)/$$svc" && GOWORK=off go list -f '{{.Dir}}' ./... | sort -u)"; \
	done; \
	test -z "$$(gofmt -l $$dirs)" || \
		(echo "gofmt needed on:"; gofmt -l $$dirs; exit 1)

arch-team-catalog:
	$(GO) run ./cmd/teamcatalog

# Customer terms and privacy documents are built separately from engineering
# documentation. The site script blocks production builds until both pages are
# approved by their content owners.
docs-site-build:
	npm --prefix docs/site run build

docs-site-preview:
	npm --prefix docs/site run preview

# The publication gate itself must stay covered: approved documents pass while
# drafts, missing metadata, malformed dates and leftover draft instructions
# block the build. Pure Node, no dependencies to install first.
docs-site-gate-test:
	npm --prefix docs/site test --silent

check: vet test services-vet services-test gofmt-check page-builder-test arch-team-catalog docs-site-gate-test
	@echo "check: gateway + $(words $(SERVICES)) service modules vetted and tested, gofmt clean"
	@before="$$(git status --porcelain -- apps/web design-system/web/ui)"; \
	go -C design-system/web/ui tool templ generate >/dev/null 2>&1; \
	(cd $(APP_DIR) && templ generate) >/dev/null 2>&1; \
	after="$$(git status --porcelain -- apps/web design-system/web/ui)"; \
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
	@before="$$(cat apps/web/internal/architecture/import_graph.txt 2>/dev/null)"; \
	$(GO) test ./internal/architecture/ -run ImportGraph >/dev/null 2>&1 || \
		(echo "check failed: the committed import graph is stale — run 'make arch-graph' and commit the result."; \
		 $(GO) test ./internal/architecture/ -run ImportGraph 2>&1 | sed -n '2,20p'; exit 1); \
	echo "import graph current"
	@echo "check passed: gofmt clean, templates and contracts generated, import graph current, vet and tests green, publication gate covered"

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
	$(GO) test ./internal/architecture/ -run ImportGraph -update

# Scaffold a team-owned vertical (D-023): one team, one project, one image.
# A vertical owns its backend AND its UI under apps/web/internal/features/<name>,
# so a team has one directory instead of six scattered homes.
#
#	git config user.name/email	=> written into OWNERS.md + CODEOWNERS
#	--team "Name" --owned-by email	=> override
feature:
	@scripts/feature-vertical.sh $(filter-out $@,$(MAKECMDGOALS))

# `make feature <name>` passes the vertical's name as a goal — make has no
# other positional channel. Swallow that goal, but only when `feature` is on
# the command line, so a typo anywhere else still fails loudly instead of
# silently doing nothing.
ifneq (,$(filter feature,$(MAKECMDGOALS)))
%:
	@true
endif

# ---- Contract (buf/protobuf) ----------------------------------------------

# Fail on an incompatible contract change against the base branch. A field may be
# deleted only when its number and name are reserved; every other FILE-level
# break fails.
#
# The base is resolved to a commit SHA rather than named as `.git#branch=main`,
# because "main" only exists locally in some of the places this runs:
#
#   - a push to main: refs/heads/main exists
#   - a pull_request checkout: HEAD is a detached merge commit and there is no
#     local main at all; only refs/remotes/origin/main
#
# `--against '.git#branch=main'` therefore fails on every pull request with
# "couldn't find remote ref main". That is exactly what CI did: its one green run
# was a push to main, where refs/heads/main does exist, so the check passed there
# and could never have passed on a pull request.
#
# The resolved SHA is passed as `.git#ref=<sha>` — a bare SHA is not accepted,
# buf reads it as a module path and answers "Module ... had no .proto files".
# `.git#ref=` works identically on push and pull request, and naming the commit
# makes the comparison reproducible rather than dependent on what main points at.
#
# If neither ref exists — a repository whose first push has no base yet — the
# check reports that and passes, because there is genuinely nothing to compare
# against. It says so on stdout rather than going quiet.
buf-breaking:
	@base=""; \
	for ref in refs/remotes/origin/main refs/heads/main; do \
		if sha="$$(git rev-parse --verify --quiet "$$ref" 2>/dev/null)"; then \
			base="$$sha"; break; \
		fi; \
	done; \
	if [ -z "$$base" ]; then \
		echo "buf-breaking: no base ref found (tried refs/remotes/origin/main, refs/heads/main)"; \
		echo "buf-breaking: nothing to compare against — skipping"; \
		exit 0; \
	fi; \
	echo "buf breaking --against $$base"; \
	buf breaking --against ".git#ref=$$base"

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
	gofmt -w $$($(GO) list -f '{{.Dir}}' ./... | sort -u)
	gofmt -w $$(go -C design-system/web/ui list -f '{{.Dir}}' ./... | sort -u)

tidy:
	$(GO) mod tidy

# Regenerate Templ *_templ.go files from .templ sources.
templ:
	go -C design-system/web/ui tool templ generate
	cd $(APP_DIR) && templ generate

# Build the Tailwind stylesheet once or in watch mode.
css:
	bun run css:build

css-watch:
	bun run css:watch

# Generate development categories and products through their owning services.
faker-products:
	bun run tools/product-faker/index.ts

# Seed the two dummy identities the role model demo needs (a pembeli and a
# pembeli+penjual who owns a merchant). Idempotent; targets the shared Postgres
# store the gateway uses. Like `dev`, it runs from the repository root so the
# `.env` there is the one read; an exported DATABASE_URL (or AUTHZ_DATABASE_URL)
# wins over it.
seed:
	go run ./apps/web/cmd/authseed

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

# ---------------------------------------------------------------- dev stack

# The whole system in one command: six platform services, the composition layer,
# the gateway, and the three backing stores they need.
#
# docker-compose.dev.yml is the authoritative definition of the system as one
# thing. The per-service compose files still exist for working on a service in
# isolation; this file is what runs everything, and it is the only place that
# decides what the services are called to each other.
#
# Note the two token settings the aggregator takes. MUTATION_TOKENS is what a
# *client* presents; DOWNSTREAM_SERVICE_TOKEN is what the aggregator presents to
# the services behind it. Two settings because they are two trust boundaries — a
# caller is not a peer — and sharing one value would mean a client credential
# would be accepted by a platform service.
DEV_COMPOSE := docker compose -f docker-compose.dev.yml

# How long the smoke targets wait for every endpoint before giving up, in
# seconds. The default covers a cold start on a slow machine; a warm stack
# settles in seconds.
SMOKE_TIMEOUT ?= 90

# The endpoints the smoke targets poll. `smoke` checks the whole system;
# `smoke-platform` checks everything `make up-platform` starts — the same list
# without the gateway, which runs on the host in that flow (`make dev`) rather
# than in a container.
GATEWAY_HEALTH  := "gateway|http://localhost:8080/healthz"
PLATFORM_HEALTH := \
	"category-service|http://localhost:8086/healthz" \
	"media-service|http://localhost:8081/healthz" \
	"product-service|http://localhost:8082/healthz" \
	"product-listing|http://localhost:8083/healthz" \
	"product-search|http://localhost:8084/healthz" \
	"aggregator|http://localhost:8085/healthz"

# --wait holds the command until every container's healthcheck passes, so a
# failure here names the unhealthy service instead of surfacing as seven
# mysterious connection refusals in the smoke output.
up:
	$(DEV_COMPOSE) up -d --build --wait --wait-timeout 300
	@$(MAKE) --no-print-directory smoke

# The whole system except the gateway. `make dev` runs the gateway on the host
# so the templ and CSS watchers see the source tree, and it cannot share port
# 8080 with the gateway container. Scaling the gateway to zero is what keeps
# "everything else" true as services are added to docker-compose.dev.yml.
up-platform:
	$(DEV_COMPOSE) up -d --build --wait --scale gateway=0 --wait-timeout 300
	@$(MAKE) --no-print-directory smoke-platform

down:
	$(DEV_COMPOSE) down

# Stop and delete the data. The only way to re-apply deploy/dev-init.sql, since
# postgres runs its init scripts once per data directory.
reset:
	$(DEV_COMPOSE) down -v

restart:
	$(DEV_COMPOSE) restart

logs:
	$(DEV_COMPOSE) logs -f --tail=100

ps:
	$(DEV_COMPOSE) ps

# Health endpoints, on the published development ports.
#
# This is what makes `make up` worth something: without it, "up" means "compose
# accepted the file", which is true long before anything serves a request.
#
# Every service answers its probes without a credential — that exemption is what
# stops an orchestrator restarting a healthy container — so this is a real
# reachability check rather than a second copy of the auth test.
#
# It reports every failure rather than stopping at the first, because the usual
# reason a dependency is down is that something earlier in the chain is.
#
# The endpoints are polled until they all answer or $(SMOKE_TIMEOUT) elapses.
# A single pass made `up` a race: services accept connections seconds after
# their container starts, and the loser was whichever dependency chain was
# longest that day. Output is printed once, at the end, so a slow start does
# not look like a loop of failures.
#
# `smoke` checks the whole system; `smoke-platform` checks everything
# `up-platform` starts. One recipe, two endpoint lists.
smoke: HEALTH_TARGETS = $(GATEWAY_HEALTH) $(PLATFORM_HEALTH)
smoke-platform: HEALTH_TARGETS = $(PLATFORM_HEALTH)

smoke smoke-platform:
	@deadline=$$(($$(date +%s) + $(SMOKE_TIMEOUT))); \
	while :; do \
		fail=0; report=""; \
		for target in $(HEALTH_TARGETS); do \
			name=$${target%%|*}; url=$${target#*|}; \
			code=$$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 $$url 2>/dev/null); \
			if [ "$$code" = "200" ]; then \
				report="$$report  ok    $$name\n"; \
			else \
				report="$$report  FAIL  $$name  ($$url -> $${code:-no response})\n"; \
				fail=1; \
			fi; \
		done; \
		if [ $$fail -eq 0 ] || [ $$(date +%s) -ge $$deadline ]; then \
			printf '%b' "$$report"; \
			break; \
		fi; \
		sleep 2; \
	done; \
	if [ $$fail -ne 0 ]; then \
		echo; \
		echo "not fully up after $(SMOKE_TIMEOUT)s — 'make logs' shows why. The first run builds"; \
		echo "six images and takes several minutes; later runs start in under a minute."; \
	fi; \
	exit $$fail

dev-pull:
	$(DEV_COMPOSE) pull --ignore-buildable

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
