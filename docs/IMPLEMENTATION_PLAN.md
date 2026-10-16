# Implementation Plan — gogo

**Companion to:** [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md) (boundaries & topology) and [`ARCHITECTURE.md`](../ARCHITECTURE.md) (canonical rules).
**Purpose:** distinguish architecture metadata from runtime product metadata and domain rules, audit the repository boundaries, and sequence the implementation work.
**Status:** actionable. Every phase has an exit condition and a file list.

---

## 0. TL;DR

**The key distinction (§1):** metadata and domain rules work together. Metadata describes a shape that admins or platform owners need to change; domain code enforces the invariants that must remain correct for every input. For the architecture-governance work below, the metadata opportunity is mainly in plumbing. Product Type and Attribute definitions are a separate runtime business-metadata use case: admins need to change them without a code deployment, while Product Service code remains responsible for validating values and enforcing invariants.

**Do these first (§4, P0–P1, roughly one day):**

| # | Action | Why it is first |
|---|---|---|
| 1 | Add `.devenv/` to `.gitignore` and `.dockerignore` | **675 MB of Go module cache is currently inside the repo tree and is being shipped to the Docker daemon on every build** |
| 2 | Add `.devenv/` to the architecture test's directory skip-list | The architecture guard is currently walking vendored dependency source looking for `BUILD.bazel` files |
| 3 | Delete `internal/modules/media` and `internal/modules/reporting` (+ `contracts/proto/reporting/v1`) | 1,347 lines and a migration that duplicate authority that already exists elsewhere |
| 4 | Fix the `cmd/modulegen` templates so the golden path compiles | The scaffolder currently teaches a shape that does not build |

The remaining architecture-governance work follows the ordering in §4. P7 is a separate product capability track and does not depend on completing that governance work.

---

## 1. Architecture metadata, runtime product metadata, and domain rules

These concepts are often presented as alternatives, but they answer different questions. A healthy system can use runtime metadata to describe editable product schemas while keeping validation mechanics and invariants in Product Service code.

### 1.1 What each one actually answers

| | **Domain business** | **Metadata-driven** |
|---|---|---|
| Question it answers | *What must be true? Who decides?* | *What is the shape? What changes when it changes?* |
| Substance | vocabulary, invariants, authority | contracts, topology, composition, configuration |
| Why it must be **code** | validation mechanics and universal invariants must be exhaustive and testable | — |
| Why it must be **metadata** | — | schemas that admins need to edit at runtime must be versioned and checked for compatibility |
| Artifact | Go types + validation in Product Service | `.proto` contracts; versioned product schemas in Product Service's database; architecture YAML/JSON with validators |
| Failure when misused | rules become strings; a runtime interpreter nobody can refactor | shapes become hand-edited code; every change is a coordinated edit across every implementation |

**The shortest useful formulation:**

> **The domain code enforces the rules; metadata describes supported shapes.**
> Product Service code validates a value as a non-negative number. An admin-managed product schema declares whether `weight` exists, its label, and whether it is required. Both are necessary; the schema does not replace the validator.

### 1.2 The decision rule

Ask which of these it is:

| If the thing… | Then it is |
|---|---|
| must be exhaustive — every case handled, compiler-enforced | **code / domain** |
| must be provable correct for all inputs | **code / domain** |
| has a vocabulary whose words also appear in the code that calls it | **code / domain** |
| must be reviewable as a diff and versioned compatibly | **metadata** |
| an authorized admin must change it without a code change or a redeploy | **runtime metadata** |
| is a contract between two things that deploy independently | **metadata** |
| is discovered by an IDE, a linter, or a graph tool | **metadata** |

### 1.3 The third category: plumbing

Most of a repository is neither domain nor metadata. It is **infrastructure** — DI, HTTP plumbing, process lifecycle, circuit breakers.

| | Plumbing should be |
|---|---|
| its **structure** (which units exist, how they connect) | **metadata** |
| its **behaviour** (retry logic, breaker state machine) | **code** |

`internal/shared/**` is entirely this third category, and it is already governed correctly: `platform must not import app code` is a structural rule expressed as a test.

### 1.4 The four quadrants

This is the mental model to carry. Two axes, four quadrants:

| | **Structure in code** | **Structure as metadata** |
|---|---|---|
| **Has business rules** | **Q1 · the domain** — `domain.Stock`, the oversell invariant, `sales`' checkout saga, `auth`'s PIN rules | **Q2 · contracts + runtime schemas** — `inventory.v1/inventory.proto`, `module.Descriptor`, and versioned Product Type schemas. The schema describes supported fields; Product Service code validates their values. **Correct and normal.** |
| **No business rules** | **Q4 · hardcoded plumbing** — literal call lists, string constants repeated in Go | **Q3 · declarative plumbing** — `buf.yaml`, `project.json`, `devenv.nix`, `docker-compose.yml`, k8s manifests, `import_graph.txt`. **The target.** |

**The finding that answers your question directly:**

> **The architecture-governance metadata opportunity in this plan is mainly Q4.** Product schemas are a distinct Q2 runtime-metadata capability because administrators need to change the supported product shape without redeploying. Neither case moves domain invariants out of code.

This distinction keeps "make it metadata-driven" from turning into an untyped rule interpreter. Use runtime metadata for the editable product schema; keep supported value kinds and validation behavior in code. Use declarative metadata for architecture topology and wiring.

### 1.5 Three confusions worth naming explicitly

| Confusion | Correction |
|---|---|
| *"The rules should be configurable, so put them in YAML."* | Then they are no longer rules. A rule the compiler cannot check is a rule nobody maintains. `inventory`'s non-negative invariant belongs in `domain/`, not in a config map. |
| *"The `Descriptor` makes this metadata-driven."* | `Descriptor` makes the **module list** metadata-driven. It says nothing about how the module decides anything. Those are different claims. |
| *"`product-service` is metadata-driven because it uses protobuf."* | It is **contract**-metadata-driven. Its usecases, outbox relay and validation are ordinary code — which is correct. |
| *"Product types are dynamic, so the CMS should own validation."* | Product Service owns the versioned schema and validation. An editorial CMS may own banners and page copy, but it is not the product write authority. |

### 1.6 Runtime product schema — admin-managed, Product Service-owned

Product Type and Attribute definitions change less often than product values,
but an admin must be able to add or edit them without a code change or service
deployment. Store drafts and immutable published versions in Product Service's
database. Expose published schemas through the product contract so Seller
renders its form from the same definition Product Service validates.

Product Service still owns validation mechanics: supported kinds, required
field checks, numeric parsing, option membership, and safe limits. Admin input
chooses among those supported capabilities; it does not provide executable
validation code. A product records the published schema version used at write
time. Existing products are not silently invalidated by later schema edits.

Product metadata must not be confused with three other things:

| Need | Owner/tool | Runtime relationship |
|---|---|---|
| Product types and attributes | Product Service database + admin workflow | Seller reads published schema; Product Service validates writes |
| Banners and editorial page blocks | Headless CMS such as Builder.io | Gateway SSR consumes published content through an allowlisted block contract |
| Developer docs, ADRs, API guides | Rspress site with source in Git | Static documentation build; not a runtime content API |

Keep these capabilities in the current repository/deployment topology initially.
Create a separate repository or service only when an independent team or
lifecycle needs that boundary, or several independent systems need to author
and consume a shared schema. A new repository alone does not make product
metadata dynamic.

---

## 2. Audit — what in this repository conflicts

### 2.1 Unit matrix

`D` = domain present · `S` = published shape is metadata · `H` = plumbing structure hardcoded in Go.

| Unit | Quadrant | D | S | H | Verdict |
|---|---|---|---|---|---|
| `internal/modules/inventory` | Q1 | ✅ | ✅ | – | **conflict A1** — rule escaped to SQL |
| `internal/modules/sales` | Q1 | ✅ | ✅ | – | correct |
| `internal/modules/auth` | Q1 | ✅ | ✅ | – | correct |
| `internal/modules/billing` | – | ❌ | ✅ | – | correct shape, wrong name (§2.3 C5) |
| `internal/modules/ops` | – | ❌ | ✅ | – | correct |
| `internal/modules/media` | – | – | – | – | **delete — C2** |
| `internal/modules/reporting` | – | – | – | – | **delete — C3** |
| `internal/pages/{home,seller,auth,profile}` | Z-U | ✅ | ✅ | ✅ | A3, A4 |
| `internal/bootstrap` | – | ❌ | ❌ | ✅✅ | **conflict A2 — the biggest one** |
| `internal/shared/**` | Q3 | ❌ | ✅ | ✅ | mostly correct; A5 |
| `internal/shared/module` | Q2 | ❌ | ✅ | – | correct |
| `services/product-service` | Q1+Q2 | ✅ | ✅ | – | correct |
| `services/product-listing-service` | Q1+Q2 | ✅ | ✅ | – | correct (see §3.3 on cost) |
| `services/product-search-service` | Q1+Q2 | ✅ | ✅ | – | correct (see §3.3) |
| `services/aggregator-service` | Q3 at rung 3 | ❌ | ✅ | ✅ | correct in kind; A6; **no consumer** (§3.3) |

### 2.2 Confirmed conflicts

**A1 · `inventory`'s invariant escaped into SQL.** *Domain conflict, not a metadata problem.*

`internal/modules/inventory/domain/stock.go` exists and holds `domain.Stock`, `domain.Movement` and `domain.NewAdjustment` — but the rule that refuses overselling lives in the persistence adapter:

```sql
-- infrastructure/postgres/repository.go
UPDATE inventory_stock SET quantity = quantity + $3, updated_at = now()
 WHERE tenant_id = $1 AND product_id = $2 AND quantity + $3 >= 0
RETURNING quantity
```

and `var _ ports.StockService = (*StockRepository)(nil)` is asserted in **both** adapters, with no `application/service.go` in the module.

> **Correction, verified 2026-10-15.** This originally claimed the in-memory adapter
> "implements a *different* rule". That is wrong, and it was asserted without reading
> the adapter. `internal/modules/inventory/infrastructure/repository.go:101-103` does
> refuse it: `after := before + delta; if after < 0 { return nil, nil,
> domain.ErrInsufficientStock }`.
>
> So the real finding is **duplication, not divergence**. Both adapters enforce the
> invariant, and both enforce it correctly. What is wrong is that it is written twice
> — once in Go in the in-memory adapter, once as a conditional `UPDATE` in the
> Postgres adapter — instead of being defined once in `domain/`. Two definitions of
> one rule can drift apart in the next change, which is a maintenance risk rather
> than a present correctness defect. A duplicate is not a second authority, but it is
> the same failure mode one edit away.

Consequences: (a) the rule has two definitions instead of one, so a future edit can make them diverge — the divergence is a maintenance risk, not a current bug; (b) `ports.StockService` is misnamed — it is a repository contract wearing a service name, and there is no `application/service.go`; (c) the interface lives in `application/` but is satisfied from `infrastructure/`, which inverts the Dependency Rule at exactly one place in the codebase.

Fix: move the invariant into `domain/`, keep the conditional `UPDATE` as the enforcement mechanism, make the application service satisfy the port.

**A2 · The composition root is a literal call list.** *The largest Q4 opportunity in the repo.*

`internal/bootstrap/di.go:333-338`:

```go
home.Register(b)
seller.Register(b)
auth.Register(b)
profile.Register(b)
billing.Register(b)
ops.Register(b)
```

and `di.go:212-218`:

```go
contributeBreaker(b, upstreamProduct)
contributeBreaker(b, upstreamProductListing)
contributeBreaker(b, upstreamProductSearch)
contributeBreaker(b, upstreamCategory)
contributeBreaker(b, upstreamInventory)
contributeBreaker(b, upstreamSales)
contributeBreaker(b, upstreamAuth)
```

`contributeBreaker`'s own comment claims *"so the two stay in lockstep by construction"* — but they are only in lockstep for the seven names someone remembered to type. Adding an upstream means editing Go in the composition root. `internal/shared/web/navigation` hardcodes three nav items as Go vars for the same reason. `httpserver/app.go:138-140` hardcodes four mounts.

This is the textbook Q4 → Q3 move, and it is exactly what a `team_catalog.yaml` + a declarative module table would fix.

**A3 · The architecture rules are Go, not a rule table.** `internal/architecture/architecture_test.go` implements eight rules as eight subtests, each re-implementing graph traversal (`withPrefix(closure(g, roots...))`). Every published tool in this space expresses rules as data — dependency-cruiser's `forbidden`/`allowed`/`required` with `extends` and a JSON Schema for its own config, Deptrac's collectors + ruleset with **all cross-layer dependencies forbidden by default**, Nx's `depConstraints`, ArchUnit's freezing. None of them hardcode the policy in the test body.

**A4 · `cmd/modulegen` emits a shape that does not compile into the composition root.** Templates produce the flat `billing` shape, emit no `Register(b *fdi.Builder)`, and `main.go:98` prints `httpserver.New(cfg, logger, catalog.NewModule(), …)` — referencing the module deleted in D-018 and a superseded constructor.

**A5 · Config is correctly centralised; the surrounding concern is not.** `internal/shared/config/loader.go` has one `LoadValidated()`, typed helpers (`envString`/`envInt`/`envBool`/`envDuration`) and validation. **This is right and should not be touched.** It is listed only because `.env.example` is 4.8 KB, which is large — but the code has a single typed boundary, which is the correct answer to that problem. No action.

**A6 · The aggregator declares three mutations that always fail.** `category.v1` has no write RPCs, so `createCategory`/`updateCategory`/`deleteCategory` answer `DEPENDENCY_UNAVAILABLE` and point at a REST API. A published mutation that cannot succeed is a contract lie.

### 2.3 Confirmed non-conflicts — do not change these

Verified, because "make it metadata-driven" is exactly the kind of instruction that damages good code.

| Keep | Evidence |
|---|---|
| The three page adapters wrapping the shared kits | `seller/catalogclient` and `home/listingclient` both *embed* `productconsumer.Client`; `auth`/`profile` authclients both take `New(auth *authconsumer.Client)`. This is ports-and-adapters working. |
| Four separate breakers in `productconsumer` | *"separate on purpose: the storefront must keep serving browse while search is down"* |
| `grpcx.InProcess` **and** `grpcx.Dial` | Both are needed — one is today's rung, the other is tomorrow's. Two adapters already document this identically. |
| Domain rules as Go code, everywhere | §1.4 — Q1 is already correct |
| `contracts/proto/**` as metadata | Q2 is already correct |
| `internal/shared/config` typed loader | A6/A5 — one boundary, validated |

---

## 3. Resource profile

Measured, not estimated. Hand-written lines exclude `_test.go`, `_templ.go`, `gen/`, `proto/`, and `.devenv/`.

### 3.1 Where the code is

| Unit | hand-written | tests | ratio |
|---|---|---|---|
| **gateway** (`internal` + `cmd`) | **104,631** | — | — |
| aggregator-service | 19,020 | 9,200 | 2.1× |
| product-service | 6,400 | 2,512 | 2.5× |
| product-listing-service | 4,912 | 4,771 | 1.0× |
| product-search-service | 3,703 | 3,254 | 1.1× |
| **services total** | **34,035** | **19,737** | |

**The gateway is 75% of all hand-written code.** The four services together are 25%.

### 3.2 Where the disk is

| Path | Size |
|---|---|
| **`.devenv/state` (2 dirs)** | **675 MB** |
| `node_modules` | 38 MB |
| `.agents` | 11 MB |
| `internal` (excluding `.devenv`) | 6.9 MB |
| **repository total** | **731 MB** |

**92% of the repository's disk footprint is a Go module cache** that `.gitignore` does not exclude.

### 3.3 Cost per unit of capability

| Component | Cost | Buys | Assessment |
|---|---|---|---|
| `aggregator-service` | 19,020 + 9,200 test; 5 gRPC connections, 2 HTTP, full OpenTelemetry stack | **zero consumers in this repository** | **Highest cost, lowest return.** |
| `product-listing-service` | 4,912 + 4,771; own Postgres, own migrations, own consumer group, own DLQ topic, cache, metrics, rate limiter | one read model: the home page product grid | Infrastructure per unit of capability is high |
| `product-search-service` | 3,703 + 3,254; own Postgres, own migrations, own consumer group | `pg_trgm` search on the same grid | Could share a database with listing under a schema boundary; the split buys independent scaling this workload may not need |
| gateway | 104,631 | 7 modules, 4 pages, 3 consumer kits, the only traffic-serving component | The workhorse |

**Total deployment surface:** 5 in-repo processes + 2 external gRPC services, **4 databases** (gateway, product, listing, search), **2 Kafka consumer groups**, 1 DLQ topic, 1 broker, 2 extra HTTP status endpoints.

**The resource finding, stated plainly:** cost is concentrated in the components serving the least, and the component serving the most is the cheapest per capability. This is not automatically wrong — `product-service` earning rung 3 is defensible under `T-data` (§4.3 of the PRD), because it is the sole write authority and has an outbox. **`listing` and `search` earned rung 3 on `T-scaling`, which is the weakest trigger**: cadence is observable rather than constraining, and scaling independence is only worth paying for if the load profile actually diverges.

**Decision rule, not a verdict.** Traffic and workload evidence still determine whether separate deployments earn their cost. The monorepo decision does not settle this question. So:

> If the home page grid is the only consumer of `listing` and `search`, and its load does not require independent scaling from writes, then their rung-3 promotion was not earned, and consolidating them into `product-service`'s deployment behind a schema boundary removes **2 processes, 2 databases, 2 consumer groups, 1 DLQ topic** — and costs one schema boundary, which is cheap and reversible.
>
> If traffic already diverges (a busy public storefront plus a quiet POS), keep them. The rule is: **rung 3 must be earned by a named trigger, and the trigger must still be true.**

Record the answer in `team_catalog.yaml` as `promotion_triggers` either way. An unrecorded trigger is indistinguishable from a default.

### 3.4 The three waste items — quantified

| # | Finding | Cost |
|---|---|---|
| **W-1** | `.devenv/state` (675 MB) is not in `.gitignore` | repo is 731 MB instead of ~56 MB; every clone, every `find`/`grep`, every IDE index pays for it |
| **W-2** | `.dockerignore` omits `.devenv`, and `Dockerfile:15` and `:30` both `COPY . .` | **675 MB of build context shipped to the Docker daemon, twice per build**, on every `docker build` and every `docker compose up --build` |
| **W-3** | `internal/architecture/architecture_test.go:173` skip-list is `node_modules`, `.git`, `bazel-bin`, `bazel-out`, `bazel-testlogs` — **`.devenv` is absent** | the architecture guard walks 675 MB of vendored dependency source. **Worse than slow:** any dependency shipping a `BUILD.bazel` gets parsed by `parseBuildFile` and injected into the import graph, which would corrupt the very rules it exists to enforce |

W-3 is a correctness risk in the guard, not just a performance one. It is first in P0 for that reason.

---

## 4. Implementation plan

The architecture work has phases P0–P6. P7 is a separate end-to-end product
capability track. Each phase states what it does **not** do, so scope cannot
creep forward.

Effort assumes one developer familiar with the codebase. All commands verified against this repository.

### P0 · Resource hygiene — *~30 minutes, zero risk*

**Files:** `.gitignore`, `.dockerignore`, `internal/architecture/architecture_test.go`

| # | Change |
|---|---|
| 1 | `.gitignore`: add `.devenv/` and `.nix-devenv` alongside the existing `node_modules/` |
| 2 | `.dockerignore`: add `.devenv/` and `.devenv` under "Node toolchain artifacts (dev only)", renamed to "Toolchain artifacts" |
| 3 | `architecture_test.go:173`: add `".devenv"` to the `switch` case list |

**Exit:** `du -sh .` drops to ~56 MB; `docker build` context drops by the same; `make check` runtime returns to baseline.

**Does not do:** delete anything, change any rule, touch `devenv.nix`.

**Note:** adding to `.gitignore` does not remove already-tracked files. Run `git rm -r --cached` on those paths if they are tracked.

### P1 · Deletions — *~2 hours, low risk*

**Order matters.** Each row's precondition is already true.

| # | Delete | LOC | Precondition (verified) |
|---|---|---|---|
| 1 | `internal/modules/media/**` | 740 | No file under `internal/` imports it. `contracts/proto/media/` does not exist, so nothing external depends on it. The external `media-service` is the sole authority — D-018's disease, uncured. |
| 2 | `contracts/proto/reporting/v1/**` | — | Its only provider is #3. |
| 3 | `internal/modules/reporting/**` | 607 | `grep -r dashboard_stats internal/` returns only this module. Creates three tables derived from Inventory + Sales + Catalogue, written and read by nobody. Un-ownable by any stream team. |
| 4 | The `metrics` module row in `README.md:104` | — | Does not exist; `/metrics` is served by `httpserver` + `ops`. |
| 5 | `billing/v1` + `media/v1` rows in `ARCHITECTURE.md` §1 | — | Neither exists under `contracts/`. |
| 6 | The `make extract-check` step in `.github/workflows/ci.yml:56-59` | — | `make extract-check` and `scripts/extract-catalog-repo.sh` both absent; D-018 removed what they referenced. |

**Exit:** `make check` passes; `grep -rn "internal/modules/media\|internal/modules/reporting" --include=*.go internal/` returns nothing.

**Does not do:** touch `internal/modules/billing` — keep it, reclassify it as a rung-1 reference module. Its defect is its name, not its shape (§2.3).

### P2 · Fix the golden path — *~3 hours, low risk*

**Files:** `cmd/modulegen/main.go`, `cmd/modulegen/templates/*.tmpl`

The scaffolder is how every future unit gets built. It currently produces code that does not compile into `internal/bootstrap`, which means every module is hand-fixed after generation — that hand-fix is the untemplated part of the architecture.

1. Replace the completion message at `main.go:98` (references the deleted `catalog` module and the old variadic `httpserver.New`).
2. Add a `Register(b *fdi.Builder)` function to the generated `module.go`, matching `internal/modules/billing/module.go`.
3. Decide the generated shape from one rule — *no domain rules → flat; has domain rules → hexagon* — and encode it. Today the scaffolder hardcodes "flat"; make the choice explicit and documented in the generated file's header comment.
4. Update `module_test.go.tmpl` so the smoke test asserts the new invariants.

**Exit:** `make module name=probe desc="…" && make check` passes **with zero manual edits** — verified by scaffolding, building, then removing `probe`.

**Does not do:** not migrate existing modules to the new shape.

### P3 · Make the composition root declarative (Q4 → Q3) — *~1 day*

**This is the highest-value metadata-driven change available, and it touches no business logic.**

**Files:** `internal/bootstrap/di.go`, new `internal/architecture/modules.yaml`

1. Move the six `Register(b)` calls and the seven `contributeBreaker` calls into a declarative table:

```yaml
# internal/architecture/modules.yaml
units:
  - path: internal/pages/home
    register: home.Register
    nav: true
  - path: internal/modules/billing
    register: billing.Register
    nav: true
upstreams:
  - { name: product,       kind: product,   breaker: true }
  - { name: product-listing, kind: productlisting, breaker: true }
  - { name: category,      kind: category,  breaker: true }
  # media has no breaker — see §3.4 R-07; either add one or record why not
```

2. Generate the registration calls from it, in the same style as `make arch-graph` — generated code committed, validated for freshness. **Follow the pattern the repo already uses; do not add a second mechanism.**
3. Move `navigation.Home/Billing/Profile` into the same table as data.
4. Leave `httpserver`'s four mounts alone — they are genuinely fixed infrastructure, not a variable set.

**Exit:** adding a module or an upstream is a YAML edit plus regeneration, with no Go edit in `internal/bootstrap`. `make check` fails if the YAML is stale.

**Does not do:** not a DI framework change, not a schema-validator change. If this needs a new dependency, it is the wrong design.

### P4 · Rule table for the architecture tests — *~1 day*

**Files:** `internal/architecture/architecture_test.go`

Rewrite the eight subtests as data + one generic evaluator, in the shape every comparable tool uses (dependency-cruiser's `forbidden`/`allowed`/`required` with `extends`; Deptrac's collectors + ruleset; Nx's `depConstraints`). The eight existing rules become eight YAML entries and the test body shrinks to traversal.

Then add the **six rules that currently have no test** — the ones `ARCHITECTURE.md` states in prose and admits are unenforced:

| Rule | Closes |
|---|---|
| composition layer contains no business rules | the Z-C test predicate |
| per-module hexagon rules | the stated prose-only gap |
| port names carry no technology token | Cockburn's "by purpose, not by technology" |
| the aggregator owns no persistence | keeps it a composition layer |
| no media aggregate owned in-process | the DEL-1 hole, permanently |
| every module reachable from bootstrap | the DEL-3 hole, permanently |

**Exit:** the six new rules run in `make check`; the eight existing rules are unchanged in behaviour (verified by the existing tests still passing unmodified).

**Does not do:** not a general graph tool, not a new language.

### P5 · `team_catalog.yaml` + validator — *~2 days, start only after P4*

Per PRD §8 and §12 Phase 1. One metadata file declaring team, tier, rung, context, contracts, interaction mode; validated against `import_graph.txt` so declared edges cannot drift from real ones.

**This is the phase to start only after P4**, because the rule table is the mechanism it plugs into. Building the catalog before the validator would produce a second source of truth — the exact failure PRD §8.1 exists to prevent.

### P6 · Revisit rung-3 promotions — *on trigger, not on schedule*

Per §3.3. For `listing` and `search`, record whether the `T-scaling` trigger is still true. If it is not, consolidating them behind a schema boundary in `product-service`'s deployment removes 2 processes, 2 databases, 2 consumer groups and a DLQ topic.

**Blocked on evidence and ownership.** Revisit only if measured scaling needs no longer justify independent Listing/Search deployments and their owners agree. The modules can remain in the monorepo either way.

### P7 · Admin-managed Product Type schemas — *product capability track*

This is an end-to-end runtime feature, not a request to make domain invariants
configurable. The existing product metadata slice carries Product Type,
schema-version, and attribute values through product creation and projections,
but the type registry is still hardcoded in Product Service. This phase replaces
that registry with admin-managed, database-backed schemas.

**Boundary decision:** Product Service owns schema storage, publication,
validation, and product values. Keep the admin interface in the current
application/repository boundary initially; authorize writes using the existing
admin identity flow. The service's internal gRPC credential is not, by itself,
proof that an end user is an administrator. Do not add a new repository or
service until an independent owner/lifecycle justifies the added contract and
operational work.

**Implementation sequence:**

1. **Model and migrate.** Add tables for product types, schema versions, and
attribute definitions (or a normalized schema document with equivalent
constraints). Seed the existing `general` and `apparel` definitions. Preserve
product `product_type_id`, `schema_version`, and attribute values.
2. **Add the admin workflow.** Provide list, draft create/edit, validation,
preview, publish, and audit history. Published versions are immutable;
publication assigns a monotonic version atomically. Validate keys, supported
types, limits, enum options, and incompatible edits before publish.
3. **Replace the hardcoded registry.** Add a repository port and database
adapter. Product creation/update validates against the submitted published
version. Seller reads the published schema from the product contract. Unknown,
unpublished, or stale versions fail with a field-level validation response.
4. **Propagate changes.** Keep product type, schema version, and attribute
values in product snapshots. Listing and Search project values independently.
Define the event/rebuild behavior for schema changes before implementing
attribute facets; SSR continues to read projections, not Product Service.
5. **Complete user feedback.** Admin sees publish success/failure and the
published version. Seller sees fields from the published schema and actionable
validation errors. Existing products remain readable if a newer schema changes.
6. **Verify the vertical slice.** Verify admin authorization, draft isolation,
publish/version behavior, seller discovery, valid and invalid product writes,
event projection, and storefront rendering. Add the checks to the existing
module test/build gates used by this repository.

**Not included:** one service per metadata concept, arbitrary code/expressions
from admins, changing Listing/Search into synchronous service calls, or
integrating a CMS as the product validation authority.

**Editorial and documentation tooling:** use a headless CMS (Builder.io is one
option) for banners and allowlisted editorial page blocks. The Go gateway owns
SSR composition and renders supported blocks with platform UI primitives. Use
Rspress for developer documentation authored and reviewed in Git, then publish
the generated static site independently. Neither tool replaces the admin
product-schema workflow.

**Exit:** an admin publishes a new product type without code changes or a
Product Service deployment; a seller creates a product using that type; the
write service validates and persists the published schema version; Listing and
Search project the event; and storefront SSR displays the projected values.

---

## 5. Sequencing

| Phase | Effort | Risk | Blocks | Why here |
|---|---|---|---|---|
| **P0** resource hygiene | 30 min | none | nothing | 675 MB, and W-3 is a correctness risk in the guard |
| **P1** deletions | 2 h | low | P2 (catalog must not describe a duplicate) | removes 1,347 lines and two duplicate authorities |
| **P2** golden path | 3 h | low | P3 (bootstrap consumes generated shape) | every future unit is built this way |
| **P3** declarative bootstrap | 1 d | med | P5 | the biggest Q4 win; touches no business logic |
| **P4** rule table | 1 d | med | P5 | provides the mechanism P5 plugs into |
| **P5** team catalog | 2 d | med | — | needs P4 |
| **P6** rung-3 review | — | — | measured scaling and service ownership | repository layout does not determine deployment boundaries |
| **P7** admin-managed Product Type schemas | sized after admin-auth review | medium | product admin authorization + schema migration design | end-to-end business metadata; independent of P0–P6 topology tooling |

**Total P0–P4: about five days.** P7 has a separate estimate because the admin authorization and schema lifecycle need to be designed against the existing product platform; do not fold it into this infrastructure estimate. A1 remains scoped to the Phase 0 pilot in PRD §12.1 because it needs a vertical slice, not a standalone refactor.

### 5.1 What to stop doing

| Stop | Because |
|---|---|
| Declaring domain rules as configuration | §1.4 — the entire metadata-driven opportunity is in Q4, which contains no rules |
| Adding a unit to `services/` | Give it its own `go.mod`, deployment config, project descriptor, and explicit `go.work` entry |
| Editing `internal/bootstrap` by hand | P3 exists so this stops being possible |
| Treating `team_catalog.yaml` as documentation | it is validated input to a test; if it can drift, it must not exist yet |
| Promoting a unit to rung 3 without recording a trigger | an unrecorded trigger is indistinguishable from a default |

---

## 6. Appendix — commands

```bash
# P0 — resource hygiene
printf '\n# devenv (Nix dev-environment state; a module cache, not source)\n.devenv/\n' >> .gitignore

# .dockerignore: add under the toolchain-artifacts section
#   .devenv/
# Dockerfile does `COPY . .` at lines 15 and 30, so build context carries it twice.

# internal/architecture/architecture_test.go:173
#   case "node_modules", ".git", ".devenv", "bazel-bin", "bazel-out", "bazel-testlogs":

du -sh .                     # expect ~56 MB after .devenv is excluded
du -sh .devenv 2>/dev/null   # still on disk until manually removed

# P1 — deletions
rm -rf internal/modules/media internal/modules/reporting contracts/proto/reporting
grep -rn "internal/modules/media\|internal/modules/reporting" --include=*.go internal/   # expect nothing

# P2 — golden path
make module name=probe desc="probe"
make check
rm -rf internal/modules/probe

# Verify the current state at any time
make check                                   # the whole gate
make arch-graph                               # regenerate the committed import graph
go -C apps/web test ./internal/architecture/ -v # the eight boundary rules, by name
```

---

## 6a. Decisions this plan does not make

Three questions surfaced during implementation and were deliberately left open rather than resolved by inference. They are tracked in [`OPEN_DECISIONS.md`](./OPEN_DECISIONS.md), each with an owner and closure options:

- **OD-1** · whether to adopt a fail-closed allowlist (`depguard` strict + `forbidigo`) as the enforcement mechanism. Recommended by research; **not adopted**, because it changes the enforcement mechanism for the whole repository.
- **OD-2** · whether the aggregator may hold a cache or read model. **Undecided** — and the current architecture test permits it as an accident of a narrow rule, not as a ratified position.
- **OD-3** · whether `type Mutation` is part of the permanent aggregator contract. **Undecided**; the test asserts it as a marked temporary guard.

P7 does not depend on changing the monorepo layout. It does depend on
confirming which existing identity claim/role the gateway uses to authorize
administrator actions. Do not infer admin authorization from the
service-to-service token.

## 7. References

[Skelton & Pais, *Team Topologies*](https://teamtopologies.com/book) · [cognitive-load heuristics](https://itrevolution.com/articles/minimize-cognitive-load-of-teams/) · [Conway 1967](https://melconway.com/Home/Conways%5FLaw.html) · [Cockburn, Hexagonal Architecture](https://alistair.cockburn.us/hexagonal-architecture/) · [Martin, The Clean Architecture](https://blog.cleancoder.com/uncle-bob/2012/08/13/the-clean-architecture.html) · [Khononov, Learning DDD](https://www.oreilly.com/library/view/learning-domain-driven-design/9781098100124) · [Feature-Sliced Design](https://feature-sliced.design/docs) · [dependency-cruiser rules](https://github.com/sverweij/dependency-cruiser/blob/main/doc/rules-reference.md) · [Deptrac concepts](https://deptrac.github.io/deptrac/concepts) · [Nx module boundaries](https://nx.dev/docs/features/enforce-module-boundaries) · [Backstage descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/) · [Terraform drift](https://developer.hashicorp.com/terraform/tutorials/state/resource-drift) · [devenv](https://devenv.sh/) · [Bazel hermeticity](https://bazel.build/basics/hermeticity) · [12-factor config](https://12factor.net/config)

Product content tools: [Rspress introduction](https://www.rspress.dev/guide/start/introduction) · [Rspress static generation](https://www.rspress.dev/guide/basic/ssg) · [Builder.io Visual Editor](https://www.builder.io/c/docs/visual-editor) · [Payload drafts and publishing](https://payloadcms.com/docs/versions/drafts)

Repository-local: [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md) · [`MONOREPO_STRUCTURE.md`](./MONOREPO_STRUCTURE.md) · [`ARCHITECTURE.md`](../ARCHITECTURE.md) · `apps/web/internal/architecture/architecture_test.go` · `apps/web/internal/bootstrap/di.go` · `apps/web/cmd/modulegen/` · `services/` · `Dockerfile` · `.gitignore` · `.dockerignore`
