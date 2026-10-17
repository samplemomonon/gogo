# Go monorepo structure

This repository is one Git monorepo containing several independently buildable
Go modules. A monorepo does not require one `go.mod`, one process, or one
deployment. `go.work` connects the modules for local development; each app or
service keeps its own `go.mod` as its dependency and release boundary.

## Repository map

```text
gogo/
├── apps/
│   ├── web/                  # Web gateway and SSR application; Go module: gogo
│   │   ├── cmd/               # Process entry points
│   │   └── internal/          # Web modules, pages, web UI primitives
│   ├── mobile/               # Mobile app boundary
│   └── desktop/              # Desktop app boundary
├── services/                 # Independently buildable platform services
│   ├── product-service/
│   ├── product-listing-service/
│   ├── product-search-service/
│   ├── category-service/
│   ├── media-service/
│   └── aggregator-service/
├── contracts/                 # Protobuf source and generated Go contract module
├── deploy/                    # Local and cluster deployment configuration
├── docs/                      # Architecture decisions, PRDs, and runbooks
├── go.work                    # Explicit local workspace module list
├── docker-compose.dev.yml     # Whole-system development stack
└── Makefile                   # Monorepo build and verification entry points
```

## What the boundaries mean

| Path | Boundary | Rule |
|---|---|---|
| `apps/web` | Web Go module and deployment | Owns page composition, SSR, seller experience, and web-specific UI primitives. The Go module remains `gogo`. |
| `apps/mobile` | Mobile application boundary | Owns native navigation, lifecycle, and mobile presentation. It consumes service APIs/contracts and does not import Go `internal` packages. Choose its framework when implementation starts. |
| `apps/desktop` | Desktop application boundary | Owns desktop navigation, lifecycle, and desktop presentation. It consumes service APIs/contracts and does not import Go `internal` packages. Choose its framework when implementation starts. |
| `services/<name>` | One Go module and service deployment per directory | Owns its domain, database, migrations, runtime, and service tests. Services do not import each other's `internal` packages. |
| `contracts` | Shared wire-contract module | Owns versioned protobuf definitions and generated Go types. Each client platform generates or maintains language-specific clients from these contracts; generated Go types are not a cross-platform UI library. |
| `go.work` | Local developer workspace | Lists every module explicitly. It does not combine deployment or release boundaries. |
| repository root | Coordination and tooling | Owns whole-stack Compose, Make targets, CI, deployment manifests, and docs. |

Go's `internal/` directory has a different job from the repository layout: it
restricts which Go import paths can use a package. `apps/web/internal` is
private to the web gateway module. Each service has its own `services/<name>/internal`
and therefore its own private implementation packages. Do not put independent
services or other client apps under the web gateway's `internal/` tree.

## Cross-platform ownership

Treat web, mobile, and desktop as separate application shells. Product, listing,
search, identity, and merchant workflows remain domain capabilities exposed by
service APIs and versioned contracts. Each app owns navigation, screen
composition, accessibility behavior, and platform-specific interactions.

The web primitives in the repository-root `design-system/web/ui` Go module
are HTML/Templ components consumed by the web app. Platform-neutral tokens,
governance and release policy live in the root `design-system/`; native apps
map those tokens to native controls instead of importing web components.

The mobile and desktop directories currently contain architecture notes rather
than runnable projects. Add their toolchains, dependency manifests, CI targets,
and deployment packaging with the first vertical slice for each platform. This
keeps the repository ready for multiple platforms without committing to
frameworks or scaffolds before those decisions are made.

## Build and test from the repository root

Use the root targets to work across modules:

```sh
make build
make check
make up
```

The web targets run from `apps/web`; service checks run inside each
`services/<name>` module with `GOWORK=off`. That verifies the same module graph
each service uses when built and released independently. To work on one service
directly, run its Go command from that service directory.

## When to split a repository

Keep this monorepo while one repository makes contract changes, coordinated
Compose runs, code review, and cross-module refactors easier. Consider a split
when a service has an independent owning team and release process, repository
access needs differ, or shared contracts need a separately governed published
artifact. A folder or `go.mod` boundary alone is not a reason to create another
Git repository.
