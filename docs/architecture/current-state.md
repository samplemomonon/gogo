# Current State Architecture

**Status:** Phase A assessment  
**Date:** 2026-10-20  
**Repository revision:** `499473a` plus the uncommitted working tree  
**Scope:** discovery, verified baseline, and risks. No refactoring has started.

**Full assessment:** [Architecture, Coupling, Ownership, and Team Topologies Audit](audit/README.md) contains the A–J findings, coupling matrix, Team Topologies review, target alternatives, and proposed migration sequence.

This document describes what the repository actually contains today. The
working tree is part of the baseline because it contains substantial,
uncommitted work that is not in `HEAD`. Where the two differ, that difference is
called out.

## 1. Assessment in one page

Gogo is already a **federated modular monolith with team-owned verticals**, not a
blank application. The target architecture should **tighten the existing
boundaries and retire legacy exceptions**, not replace the stack or re-home the
whole application.

The main facts that shape the next increment:

- The application is Go + templ + htmx + Tailwind/DaisyUI. There is no SPA, no
  React runtime, and no global JavaScript bundler.
- The gateway is the composition root and the web shell. It mounts pages
  directly and federates modules through manifests. Six platform services are
  separately deployable; the gateway reaches five of them over gRPC, while the
  aggregator is a separate GraphQL/BFF service.
- Public pages and the seller/account consoles already render through typed
  presentation models. General pages do not receive database entities.
- Architectural boundaries are enforced by Go tests: closed verticals,
  hexagonal layer direction, no retired roots, a committed import graph, and a
  team catalog.
- **A native visual page builder already exists in the working tree.** It is
  absent from `HEAD`, where the old content read-side lived under
  `features/contentcms/ui`. The premise that the repository has no visual page
  builder is stale.
- There is no customer cart, customer checkout, customer order, or payment
  flow. The implemented checkout is the seller POS; `/profile/pesanan` is
  currently an empty-state placeholder.
- The strongest first code increment is narrow: replace the legacy Builder.io
  `html/template` renderer with an equivalent typed templ component. It removes
  the second rendering engine without touching routes, persistence, pricing,
  inventory, authentication, or CSS.

## 2. Verified baseline

All commands below were executed on the working tree in this assessment.
`make check` was run from the repository root; the fresh Go test run used
`-count=1` so cached results were not reused.

| Command | Result | Evidence |
| --- | --- | --- |
| `make check` | **passed (exit 0)** | gateway vet + race tests, six service modules, gofmt, templ/contracts/import-graph freshness, `buf lint`/`buf breaking`, 23 page-builder Node tests, 7 documentation-publication tests, team catalog |
| Fresh Go tests | **passed (exit 0)** | `go -C apps/web test ./... -race -count=1` plus each service with `GOWORK=off`; 194 `ok` packages, 28 packages with no test files, 0 failures |
| `make build` | **passed (exit 0)** | produced `bin/gogo`; templ generation and CSS build completed |
| `make docker-build` | **passed (exit 0)** | produced image `gogo:latest` from the multi-stage Dockerfile |
| `bun run css:build` | **passed (exit 0)** | generated a 274,038-byte `output.css`; all 83 `builder-*` classes are present |
| Compose smoke | **expected statuses** | `/` 200, `/healthz` 200, `/readyz` 200, `/module-registry` 200, `/admin/content/pages` 303 to login, `/preview/pages/faq` 303 to login, `/catalog/does-not-exist` 404 |

Environment limits and skipped coverage:

- Live database tests that require `CONTENTCMS_TEST_DATABASE_URL` were skipped,
  as were the product service SQL tests requiring `POSTGRES_TEST_DSN` and the
  Kafka wire test requiring `KAFKA_TEST_BROKERS`.
- `kubectl` and `kustomize` are not installed in this environment. The
  Kubernetes manifests were inspected but not applied or client-validated.
  The Docker image build is covered by CI.
- There is no permanent browser, visual-regression, or accessibility test in the
  repository. The page-builder DOM controller and responsive editor behavior
  are not covered by the Node test suite; only the pure state module is.

No command failed in this pass, and no pre-existing failures were observed. The
pre-existing condition is a dirty tree, not a red baseline.

### Working-tree state

Before this document was added:

- 50 modified tracked files
- 6 deleted tracked files (all six old `contentcms/ui` files)
- 20 untracked status entries, representing 64 untracked files, including the
  native CMS domain, store, components, handlers, migrations, and page-builder
  JS

The tree includes unrelated changes as well as the CMS work. No application
source file was edited or deleted during Phase A. The verification commands did
regenerate tracked build artifacts in place (`tokens.css` and `output.css`);
`make check` separately proved that generated templ and contract files are
current. This document is the only file added by the assessment.

## 3. System shape

```text
Browser
  |
  v
apps/web gateway  (one Go binary, one image)
  |-- pages mounted directly
  |     /, /catalog/{id}, /search/suggest
  |     /login, /logout, /profile*
  |     /seller*
  |     /pages/{slug}, /admin/content/*, /preview/*
  |-- modules federated through manifests
  |     auth, inventory, sales, billing, ops
  |-- remote platform clients
  |     product.v1, productlisting.v1, productsearch.v1,
  |     category.v1, media.v1
  |
  +--> PostgreSQL (auth, inventory, sales, contentcms)
  +--> Kafka product events -> in-process event bus -> billing
  +--> six separate platform service deployments
```

Deployable units:

| Unit | Location | Notes |
| --- | --- | --- |
| Gateway / web app | `apps/web` | Owns HTTP routes, HTML rendering, composition, and in-process verticals |
| Product platform | `services/product-service` | Product aggregate and write API |
| Listing projection | `services/product-listing-service` | Browse read model fed from product events |
| Search projection | `services/product-search-service` | Search read model |
| Category service | `services/category-service` | Category hierarchy |
| Media service | `services/media-service` | Object metadata; media bytes in object storage |
| Aggregator | `services/aggregator-service` | GraphQL/BFF composition over platform services |
| Shared contracts | `contracts/` | Protobuf definitions and generated Go packages |
| Design system | `design-system/` | Tokens and templ UI primitives |

`auth`, `inventory`, `sales`, `contentcms`, `billing`, and `ops` remain in the
gateway process. Auth, inventory, and sales still communicate with their
consumers over real gRPC contracts on `bufconn`, which keeps the lift-out seam
available without a second deployment today.

## 4. Data authority and contracts

| Domain data | Authority | Access path |
| --- | --- | --- |
| Product, category, media | Platform services | gRPC clients built in `internal/shell/di.go` |
| Browse/search projections | Platform services | gRPC; short-TTL server-side cache |
| Stock and movement ledger | `features/inventory` | `inventory.v1` over in-process gRPC |
| POS transactions | `features/sales` | `sales.v1` over in-process gRPC; owns stock effect through inventory |
| Employees, sessions, roles, merchant relationship | `features/auth` | `auth.v1`; shared `authconsumer` client |
| Editorial pages, revisions, site shell | `features/contentcms` | PostgreSQL store; public reads use published snapshots |
| Billing activity | `features/billing` | Bounded in-memory projection of product events; no billing ledger |
| Operational state | `features/ops` | Registry readiness plus process health |

Multi-tenancy is request-scoped by host (or an explicitly trusted header), with
tenant IDs carried through storage and events. The content store, inventory,
sales, and auth schemas are tenant-scoped; public content reads resolve the
published site shell before rendering.

Product changes arrive from `product.events.v1` through Kafka, are republished
on the in-process bus, and are consumed by billing. There is no synchronous
HTTP call between verticals.

**Not present today:** customer cart, customer checkout, customer orders,
shipping, customer payment, and a public product-write path. Preserve what
exists; do not describe those flows as implemented.

## 5. Gateway composition and request lifecycle

The composition root is `apps/web/internal/shell`:

- `cmd/server/main.go` loads configuration, builds the logger, constructs the
  container, and runs the HTTP server.
- `shell/di.go` registers platform connections, breakers, auth/inventory/sales
  in-process gRPC connections, UI ports, and shell pages.
- `shell/units.go` is the declared table of pages and federated modules.
- `shared/module` defines manifests, route registration, lifecycle, health, and
  the registry served at `/module-registry`.
- `shell/site_page.go` composes the two public surfaces that contend for `/`:
  native CMS homepage selection and the manual storefront.

The actual module registry in the running stack lists `auth`, `inventory`,
`sales`, `billing`, and `ops`. Billing and Ops are registered explicitly in the
unit table. Auth, inventory, and sales backend modules are registered by their
in-process connection builders; their UI read-sides are mounted as pages.

Middleware order in `shared/httpserver/app.go`:

1. metrics
2. request ID
3. trusted-proxy-aware real IP
4. tenant resolution
5. structured request logging
6. panic recovery
7. security headers (including CSP)
8. request body limit
9. compression
10. request timeout
11. federated navigation injection
12. published site-settings resolution (skipped for static/ops endpoints)

Representative request path:

```text
GET /catalog/{id}
  -> home detail handler
  -> application port
  -> listingclient adapter (productlisting.v1 + media)
  -> typed results.Product
  -> pages.ProductDetail / ProductDetailContent
  -> ui.PageShell
  -> layouts.Base
  -> embedded output.css
```

`GET /pages/{slug}` first asks the native published store, then falls back to
the legacy Builder.io read path. `/` is owned by a shell-level dispatcher that
asks the CMS for a published homepage first and otherwise calls the manual home
page. That is composition, not a cross-vertical import.

## 6. Presentation and frontend architecture

### Rendering model

- Server-rendered HTML through templ; htmx handles fragment swaps.
- `shared/web/render` chooses a full document or a fragment for the same
  handler based on the request.
- `layouts.Base` owns the document head, skip link, header, main landmark,
  footer, status region, and scripts.
- `ui.PageShell` owns page spacing and the optional narrow reading column.
  Home, product detail, legacy CMS, native CMS, and site settings use it.
- Seller and profile use `layouts.Base` directly and pass a sidebar layout as
  their body. They intentionally do not add a second `ds-container`.

### Typed presentation models

General pages are typed and narrow:

- `results.Product` for product detail and cards.
- `results.Account` for profile.
- `results.Transaction` and related models for the seller console.
- `pages.EditorBootstrap` for the CMS editor.
- `pages.PublicNativePage`/`DraftPreview` for CMS output.

The exception is the CMS document model: `domain.Component.Props` is
`map[string]any` because component properties are schema-driven. The domain
normalizes and validates the map at the boundary, and render helpers perform
typed assertions. This is defensible for a page builder, but it should not be
copied into ordinary pages.

### Design system and CSS

- Design tokens are generated from `design-system/tokens/tokens.json` into
  `design-system/web/tokens.css`.
- `apps/web/internal/shared/web/assets/css/app.css` is the Tailwind v4 entry
  point and imports the generated token file.
- Shared primitives live in `design-system/web/ui`; class constants and finite
  variant functions live in `classes.go`.
- Runtime Tailwind class generation is not used for CMS content. Responsive
  component CSS is generated as inline custom CSS from validated enum maps,
  bounded numbers, and validated colors.
- The shared shell's tenant theme override is also generated from validated
  `#RGB`/`#RRGGBB` values before it is injected into the document head.
- The production CSS build succeeds with all builder classes present.

### JavaScript

There is no bundler and no shared JS build. Embedded scripts are:

- vendored htmx
- web-vitals
- the CMS page-builder controller and pure state core

`page-builder-core.js` is a pure ES module with 23 Node tests for document
operations, validation, history, responsive inheritance, and autosave
scheduling. `page-builder.js` is a 2,828-line DOM controller with no browser
test suite in the repository. The controller builds DOM with `textContent` and
DOM APIs rather than injecting authored HTML.

### SEO and accessibility

Current strengths: `lang="id"`, skip link, landmarks, `aria-live` status,
accessible labels on editor controls, `noindex` on the editor, safe URL/color
handling, and escaped content.

Current limits: the shared layout supports only a document title plus one
site-wide description. Product detail does not add a per-product meta
description, canonical URL, or social metadata. There is no permanent
accessibility or visual-regression test.

## 7. Content CMS and page builder

There are two editorial paths in the working tree.

### Legacy read path

`contentcms.Reader` reads published Builder.io content for pages such as FAQ and
the storefront banner. It is enabled only when the Builder public key is
configured. Its public rendering lives in
`presentation/handler/page.go`:

- `html/template` renders a body fragment at runtime.
- The fragment is wrapped by a templ page using `templ.Raw`.
- Goldmark renders markdown with raw HTML disabled.
- The template is safe, but it is a second rendering engine and a raw-HTML
  handoff.

### Native path

The working tree adds a tenant-scoped native CMS:

- `features/contentcms/domain`: page document schema, components, presets,
  bindings, site settings, validation, limits.
- `infrastructure/postgres`: pages, revisions, published snapshots, sections,
  form submissions, site shell, migrations.
- `presentation/components`: the single Go component registry and generated
  responsive CSS.
- `presentation/pages`: editor shell, library, canvas, inspector, outline,
  site settings, public and preview pages.
- `presentation/handler`: public pages, editor APIs, revisions, publish,
  sections, site endpoints, protected previews, and form submission.

Authoring and preview require an authenticated session with the existing
`authz.role.manage` permission. Drafts, revisions, published snapshots, and the
site shell have separate lifecycles. Public rendering uses only published
documents. Page documents are validated before save and again before publish
and render. Safe URL and color validation prevents arbitrary HTML, CSS, or
JavaScript from being authored. The native path still uses `templ.Raw` for its
generated component stylesheet; unlike the legacy body, that CSS is produced
only from validated IDs, fixed enum maps, bounded numbers, and validated colors.
It is a deliberate, narrow exception, not a general HTML slot.

The builder supports more than the six originally requested primitives
(heading, text, image, button, spacer, container). It also provides navbar,
form, data-text, and ad-slot components, responsive overrides, nested nodes,
sections/presets, bindings, autosave, revision history, and breakpoint modes.

**Important:** this native path exists only in the dirty working tree. The six
old tracked files under `features/contentcms/ui` are deleted, and their
replacement implementation is untracked. The builder cannot be treated as a
released baseline until that work is committed.

## 8. Build, assets, and deployment

- `make assets` runs templ generation, token generation, and Tailwind.
- Generated `*_templ.go` files and `output.css` are build artifacts; the CSS is
  tracked so a checkout can compile before generation.
- Go embeds `output.css`, htmx, web-vitals, page-builder JS/core, and a
  placeholder image from `shared/web/assets`.
- The Dockerfile builds CSS in a Bun stage, generates templ in a Go stage, and
  copies the compiled static binary into an Alpine runtime image.
- CI runs `make check`, tests the six service modules again, and builds the
  container image. Tagged pushes publish to GHCR.
- Kubernetes manifests use Kustomize, secrets/config maps, health probes,
  network policies, and no published host ports.

The build gate is strong on Go and contract correctness. Two asset gaps remain:

1. `app.css` still declares `@source` paths for the retired
   `internal/modules` and `internal/pages` roots. Tailwind silently ignores the
   missing directories, so the build is green but the source list is wrong.
2. `output.css` is tracked, but `make check` regenerates it and only checks
   templ/contracts/import-graph drift. It does not fail when the committed CSS
   differs from a fresh build.

Static assets are served without `Cache-Control`, `ETag`, or `Last-Modified`.
They are compressed, but repeated page loads cannot rely on a validator.

## 9. Ownership today

The `team_catalog.json` declares placeholder teams T1–T5 and gateway units:

| Team | Declared ownership | Evidence |
| --- | --- | --- |
| T1 Catalogue | Product platform services and contracts | Declared in team docs; no gateway unit; no `OWNERS.md` under `services/` |
| T2 Merchant Console | inventory, sales backend, seller UI | `features/inventory`, `features/sales`; `OWNERS.md` present |
| T3 Storefront | home UI | `features/home`; `OWNERS.md` present |
| T4 Identity | auth backend + login UI, profile UI | `features/auth`, `features/profile`; `OWNERS.md` present |
| T5 Gateway Platform | shell, shared web, design system, contentcms, billing, ops | `OWNERS.md` present for verticals; no `OWNERS.md` for shared/shell/design-system |

Ownership gaps:

- There is **no root `CODEOWNERS`**. Some documents say one exists or should
  exist; others explicitly say none is generated because the repository has no
  real organizational data. The placeholder team ids are not real reviewers.
- `contentcms/OWNERS.md` is updated to cover the native vertical, but
  `team_catalog.json` still lists only `contentcms/presentation` as its unit.
  A generated ownership map would under-cover the new domain, store, compose,
  and component packages.
- Platform services have READMEs, not `OWNERS.md` files.

This is a documentation/governance gap, not a reason to invent names or
authorities. `team-ownership.md` should use T1–T5 as placeholders until real
owners exist.

## 10. Risks and findings

| Severity | Finding | Evidence | Impact |
| --- | --- | --- | --- |
| High | The native builder is uncommitted | 6 deleted old files, 64 untracked files; `HEAD` has only the old `contentcms/ui` read-side | Any refactor or branch cleanup can lose the only visual builder; reviewers cannot see a stable baseline |
| High | Two CMS rendering engines | When Builder.io is configured: `html/template` + `templ.Raw` in `presentation/handler/page.go`; native pages render through templ | Source-of-truth split, raw-HTML handoff, duplicated markup and escaping rules |
| High | Feature-specific assets live in shared | `page-builder.js` and its core in `shared/web/assets/js`; roughly 1,000 of 1,585 `app.css` lines are builder styles | Violates feature asset ownership and makes shared assets a dumping ground; changes to CMS affect the global asset bundle |
| Medium | Documentation drift | README route table omits `/admin/content/*`, `/preview/*`, site, forms; describes Builder.io as the only editorial path; `app.css` names retired roots | Misleads maintainers and future agents about what exists |
| Medium | No browser-level UI gate | Node suite covers only core state; no Playwright/Cypress/visual/a11y tests | Responsive editor, keyboard behavior, focus management, and real page regressions are only proven manually |
| Medium | Default gate skips live PostgreSQL/Kafka tests | `CONTENTCMS_TEST_DATABASE_URL`, `POSTGRES_TEST_DSN`, `KAFKA_TEST_BROKERS` not set | A green suite is not proof of database or event-wire behavior; earlier defects were only found against live Postgres |
| Medium | Generated CSS can drift | `output.css` is tracked; `make check` does not compare it after regeneration | A stale committed asset can ship while the build stays green |
| Medium | Page metadata is thin | `layouts.Base` takes only title; description is site-wide | Product and editorial pages cannot express page-specific description/canonical/social metadata |
| Low | Presentation composition has two conventions | Seller/profile use `layouts.Base` directly; home/detail/CMS use `PageShell` | Not a defect today, but the next page may copy the wrong convention |
| Low | CMS handler is broad | The handler package spans a 649-line `page.go` plus `editor_api.go`, covering public HTTP, editor APIs, forms, revisions, preview, and legacy rendering | Harder to test and review; split along use cases only when touching it |
| Low | Static assets lack validators | `/assets/*` responses have no cache headers | Repeated CSS/JS downloads and no safe immutable caching strategy |

The largest risk is not a design flaw in the Go architecture. It is that the
most recent and complex feature exists as an uncommitted working-tree change,
while the repository's front door (`README.md`) still describes the previous
world.

## 11. Safe incremental path (proposed; not started)

### Increment 1 — migrate the legacy CMS body to templ

Replace `html/template` and `templ.Raw` in the legacy read path with an
equivalent typed templ body component:

- define an `EditorialPageView` / `EditorialBlockView` presentation model;
- render the body with templ, preserving the current markup, classes, escaping,
  and empty states;
- keep the native CMS path untouched;
- add tests for markdown safety, FAQ items, CTA links, images, and empty lists;
- remove `html/template` from the content handler.

Why first: narrow blast radius, no route/database/auth/pricing/inventory/order
change, and it retires the clearest violation of the templ-only and raw-HTML
rules. It is code-only and reversible.

### Later, in order, only after approval

2. **Stabilize the baseline:** commit the native builder as-is in a focused
   change, then update `README.md`, the CMS docs, and the dead Tailwind source
   list to match the code.
3. **Move feature assets under their feature:** contentcms JS, builder CSS, and
   their tests under `features/contentcms`; expose them through an asset mount
   while preserving `/assets/js/...` URLs or providing redirects.
4. **Add generated-asset and browser gates:** CSS drift check, Playwright
   smoke/visual/a11y coverage for the editor and representative public pages.
5. **Introduce a typed page metadata contract:** title, description, robots,
   canonical, and optional social metadata, with a safe default.
6. **Split the CMS handler along use cases** when those use cases are next
   changed; do not do a speculative wholesale split now.

## 12. Decisions needed before implementation

1. Treat the uncommitted native page builder as the baseline to preserve, and
   commit it before refactoring? Recommended: yes, in a separate focused change.
2. Approve Increment 1 (legacy CMS templ migration) as the first code change?
3. Keep T1–T5 as documented placeholders, or provide real team/reviewer
   identities for `team-ownership.md` and an eventual `CODEOWNERS`?
4. Is browser E2E coverage in scope for the target state, or is the current
   unit/integration gate sufficient for this phase?
