# Product Requirements Document — Team Topologies & Architecture Boundaries

**Repository:** `gogo`
**Revision:** 2 — restructures Rev 1 around **boundaries** rather than around team types.
**Supersedes:** Rev 1 of this document. *Complements* [`ARCHITECTURE.md`](../ARCHITECTURE.md), which remains the canonical statement of the seven import rules and the `D-0xx` decision log.
**Decision IDs:** `TT-0xx` (single namespace). Rev 1 deliberately used a separate `TT-nnn` range to avoid colliding with `ARCHITECTURE.md`'s `D-0xx` log; Rev 2 consolidates, and §0.4 records the one migration hazard.
**Author role:** Software Architect.

---

## 0. Scope, maturity, and what this PRD does not decide

### 0.1 What changed in Rev 2

Rev 1 was organised team-first. Rev 2 is organised **boundary-first**, because a boundary is the thing that can be tested; a team is a consequence of boundaries.

| # | Correction | Where it landed |
|---|---|---|
| 1 | **Duplication → delete one, do not reconcile.** | §3, promoted to a first-class register with a deletion order |
| 2 | **Monorepo is the selected distribution model.** Separate Go modules still provide service build and release boundaries. | §2.6 records the decision and the repository layout |
| 3 | **Git and CI/CD governance is not implemented.** Rev 1 asserted CODEOWNERS generation and CI wiring as if they existed. | §1 maturity ladder; every rule in §9–§10 now carries a rung label |
| 4 | **One feature end-to-end first.** Rev 1's plan was breadth-first. | §12 Phase 0 is a single named vertical slice; everything else follows its template |
| 5 | **Hybrid is the priority: modular monolith → microservice.** Rev 1 treated services as given. | §4 — a five-rung escalation ladder is now the spine of the architecture |
| 6 | **Focus on ownership, deployment, and other boundaries.** | §2 — six boundaries, each with a definition, a test, and current state |

### 0.2 The one-line architecture position

> **Every unit starts as a module inside one process. A service is something a module becomes when a named trigger fires. The seam that makes that promotion a transport change rather than a rewrite already exists in the codebase — it is documented in exactly two places and is currently invisible to the architecture docs.**

### 0.3 What this PRD does not decide

Stated up front, because Rev 1 overreached on two of these.

| Not decided here | Why | Where it is parked |
|---|---|---|
| **Future service extraction from the monorepo** | The current implementation stays in one repository. Extraction is revisited only when an independent owner, access policy, or release lifecycle requires it. | §2.6 |
| **Team names, headcount, reporting lines** | The repository contains zero organisational information: no `CODEOWNERS`, no `OWNERS`, no team directory. Inventing teams would be fiction. §6 uses **team ids** (`T1`…`T5`) as placeholders with a substitution procedure. | §6.1, §12 Phase 3 |
| **Whether the product catalogue leaves this repository** | It stays in this monorepo; service modules remain independently deployable. Revisit only on the extraction triggers in §2.6. | §2.6 |
| **Whether product types and attributes are admin-managed** | This PRD records the target workflow and ownership; implementation details belong to the product metadata feature plan in §12.6 and `IMPLEMENTATION_PLAN.md`. | §8.6, §12.6 |
| **CI/CD pipeline design** | Not implemented; out of scope until the boundaries are declared. | §1, §12 |

### 0.4 One migration hazard

`ARCHITECTURE.md`'s decision log skips `D-010` (self-reported as `docs/FOLLOW_UP.md` item 7). Rev 1 used `TT-nnn` partly to avoid making that gap worse. Rev 2 uses `TT-0xx` and requires that **`D-010` be either written or explicitly retired** before the first `TT-` entry lands, so the log is verifiably complete.

---

## 1. Maturity ladder — read every rule with its rung

Correction 3: git and CI governance are not implemented. Any PRD that lists unbuilt enforcement as if it existed is not a requirement document. Every check in §9 carries a rung.

| Rung | Meaning | Enforcement mechanism | Requires CI? |
|---|---|---|---|
| **A · Declared** | Exists as metadata or prose. Nothing checks it. | `ARCHITECTURE.md`, `team_catalog.yaml` | no |
| **B · Locally enforced** | A test exists and `make check` runs it. | `apps/web/internal/architecture/architecture_test.go` | no |
| **C · Merge-enforced** | CI runs it; a PR cannot merge without it. | `.github/workflows/ci.yml` → `make check` | yes |
| **D · Drift-proof** | Generated *from* metadata, and CI verifies the generated artefact is current. | `make arch-graph`, `make CODEOWNERS` | yes |

### 1.1 Current state, honestly assessed

| Capability | Rung | Evidence |
|---|---|---|
| The seven import rules | **C** | `make check` runs `go -C apps/web test ./internal/architecture/`, which runs `TestImportBoundaries`; `ci.yml` runs `make check` |
| Import-graph freshness | **D** | `make arch-graph` regenerates `import_graph.txt` from `BUILD.bazel`; `make check` fails on a stale copy |
| Parser honesty (`TestParserIsHonest`) | **C** | floors on package counts and required edges — "a guard on the guard" |
| Per-module hexagon rules | **A** | `ARCHITECTURE.md` states them in prose and says so explicitly |
| Team ownership (`team.Descriptor.Team`) | **A** | does not exist |
| `team_catalog.yaml` | **A** | does not exist |
| `CODEOWNERS` | **A** | does not exist |
| `devenv.nix` + lock per unit | **A**, 2 of 6 units | `product-service`, `product-listing-service`; both still `devenv init` scaffolds |
| GraphQL schema breaking gate | **A** | gqlgen has no equivalent of `buf breaking` |
| Git branch / review policy | **A** | no policy document exists |

**Two consequences.**

1. Rev 1's acceptance criteria were mostly unbuilt items stated as commitments. Here they are split by rung (§13), and the PRD's *Phase 0* (§12) is explicitly a rung-A-to-B exercise on one feature before any of it generalises.
2. Rung **D** is the only rung that is genuinely drift-proof, because it is the only one where the artefact is generated rather than written. The metadata-driven literature's central warning is *drift* — *"when the real-world state differs from the state defined in your configuration"* ([Terraform drift](https://developer.hashicorp.com/terraform/tutorials/state/resource-drift)) — and the fix is always the same: derive, don't duplicate.

---

## 2. The six boundaries

Correction 6. Each boundary gets a definition, a machine-checkable test, and current state. **A boundary with no test is a diagram.**

| ID | Boundary | Question it answers | Primary test | Current |
|---|---|---|---|---|
| **B1** | Ownership | *Which team is answerable for this?* | `TT-034` | **A** (absent) |
| **B2** | Deployment | *Which process does this run in?* | `TT-048` | **B** (implicit) |
| **B3** | Contract | *What wire format separates them?* | import rule 6 + `buf breaking` | **C** |
| **B4** | Data / aggregate | *Who is the sole writer of this row?* | import rule 7 + `TT-047` | **C**, with a live hole (§2.4) |
| **B5** | Composition vs domain | *May this code know a business noun?* | import rules 1–4 | **C** |
| **B6** | Distribution | *Which repository?* | `go.work` + root build/CI + module paths | **monorepo selected** (§2.6) |

### 2.1 B1 · Ownership boundary

**Definition.** Every file, package, module, page, service and contract maps to **exactly one** team. Not "the teams that touch it" — exactly one, because code owned by three teams is owned by no one.

**Why it is the primary boundary.** Conway's law guarantees that a system's structure mirrors its communication structure: *"Any organization that designs a system (defined broadly) will produce a design whose structure is a copy of the organization's communication structure"* ([Conway 1967](https://melconway.com/Home/Conways%5FLaw.html)). If ownership is ambiguous, coupling is guaranteed and will be discovered later, in production, as a dependency nobody admits to.

**Test.** `TT-034` — every unit resolves to exactly one team id; every team id referenced exists; a team id may not own two units that declare contradictory `tier`.

**Current state.** Absent. `module.Descriptor` carries `Name`, `Version`, `Entry`, `Description`, `Routes`, `Nav` and nothing else, so `GET /module-registry` — the one artefact a new engineer would consult — **cannot answer "who owns this?"**

### 2.2 B2 · Deployment boundary

**Definition.** The set of units that execute inside one process. In this repository it is currently: **one process** containing seven modules and four pages, plus four out-of-process services.

**Two levels exist and must not be confused:**

| Level | Question | Decided by |
|---|---|---|
| **Deployment boundary** (B2) | Which OS process? | The import graph — importable ⇒ same process |
| **Distribution boundary** (B6) | Which repository? | One monorepo with multiple Go modules. §2.6 |

**Why B2 is a *real* boundary here even though everything is in one process.** `internal/modules/auth/presentation/grpcservice` and `internal/pages/seller` communicate over **in-process gRPC** through `grpcx.InProcess`. That is a deliberate seam, and the code says so:

> "New wraps the platform connections. Pass connections built by `grpcx`: **`Dial` for deployed services, or `InProcess` for a same-process pairing.**"
> — `internal/pages/seller/infrastructure/catalogclient/client.go:22-23`, and verbatim again at `internal/pages/home/infrastructure/listingclient/client.go`

**This is the hybrid mechanism, already built and undocumented.** Two adapters — one on the write path, one on the read path, in different zones of the codebase — independently converge on the same abstraction: *the connection type is the only thing that changes when a module becomes a service.* It appears in no architecture document, no decision log entry, and no ADR. **§4 formalises it.**

**Test.** `TT-048` — every module and service is reachable from `internal/bootstrap`; no unit is mounted nowhere.

**Current state.** B. Reachable-from-bootstrap is *not* tested, which is why three units (§3) have sat unmounted indefinitely.

### 2.3 B3 · Contract boundary

**Definition.** A contract is the *only* thing that crosses a B2 boundary, and it has exactly one provider.

**The repository is already rigorous here and this PRD does not relax it.** `module.Descriptor` is the HTML federation contract; `contracts/proto/**` is the RPC contract; `internal/shared/grpcconsumer` defines the calling convention once (tenant threading, breaker, *"only a transport failure may count against the breaker"*, error vocabulary) so that six hand-rolled copies cannot diverge — D-016's rationale, still correct.

**Test.** import rule 6 (the four product-platform contracts are imported by exactly one package, `internal/shared/productconsumer`) + `buf breaking` per §9.3.

**Current state.** C.

**One recorded strength, verified rather than assumed.** Rev 1 suspected client duplication. It does not exist:

| Adapter | Verdict |
|---|---|
| `internal/pages/seller/infrastructure/catalogclient` | *embeds* `productconsumer.Client`, maps cores into `results.Product` |
| `internal/pages/home/infrastructure/listingclient` | *embeds* `productconsumer.Client`, keeps only term-routing and core→read-model mapping |
| `internal/pages/auth/infrastructure/authclient` | `New(auth *authconsumer.Client)` — thin adapter |
| `internal/pages/profile/infrastructure/authclient` | `New(auth *authconsumer.Client)` — thin adapter |

This is the ports-and-adapters rule applied correctly: the consumer declares its own port, the shared kit owns the wire types, the adapter owns only the mapping ([Cockburn 2005](https://alistair.cockburn.us/hexagonal-architecture/)). **Do not "deduplicate" these** — they are the pattern the rest of the codebase should copy.

### 2.4 B4 · Data / aggregate boundary

**Definition.** Exactly one component is the sole writer of a given row. A second writer of the same row is a defect, not an integration.

**The canonical statement is D-018.** gogo once owned `internal/modules/catalog/product` while four standalone services implemented the same aggregate elsewhere; *"Two authorities for one row"* is the failure D-011 was written to prevent, *"reached by a different road."* D-018 deleted gogo's copy and added import rule 7.

**Test.** import rule 7 + `TT-047`.

**Current state.** C for product — with **two live holes** (§3).

#### Hole 1 · media is a second authority

`internal/modules/media/**` exists in full: `domain/`, `application/ports/`, `application/service/`, `application/results/`, `infrastructure/filesystem/`, `infrastructure/postgres/` with its own migration `001_create_media_table`, and a doc comment saying it is *"deliberately unwired rather than half-wired."*

Meanwhile `media.v1` is served by an **external** `media-service`, and `aggregator-service/project.json` declares `implicitDependencies: ["media-service"]`.

Same disease as D-018, different aggregate, uncured. **`contracts/proto/media/` does not exist**, so nothing external depends on the module — the deletion is free.

#### Hole 2 · reporting is a second authority by derivation

`internal/modules/reporting/infrastructure/postgres/migrations/0001_reporting.sql` creates:

```sql
CREATE TABLE IF NOT EXISTS dashboard_stats (tenant_id, period, total_products,
    total_revenue, total_orders, low_stock_count, generated_at, ...);
CREATE TABLE IF NOT EXISTS low_stock_items (tenant_id, product_id, name,
    current, min_stock, unit, ...);
CREATE TABLE IF NOT EXISTS top_products (tenant_id, period, product_id, name, quantity, ...);
```

Every column is **derived from state that already exists elsewhere**: `total_products` from the product platform, `low_stock_count` from Inventory, `total_revenue`/`total_orders`/`quantity` from Sales. So these tables are a second copy of three other contexts' facts, in a fourth context's database, written by nobody and read by nobody.

It is worse than a dormant duplicate because its in-memory adapter `internal/modules/reporting/infrastructure/repository.go` returns **confident zeros** rather than an error, so a future caller cannot distinguish "no data" from "not implemented".

It is also un-ownable: a read model spanning Inventory, Sales and Catalogue **cannot be stream-owned by any single team** without cutting the architecture along the wrong plane (§7.4).

### 2.5 B5 · Composition vs domain boundary

This is Rev 1 §2.1, condensed and kept, because it remains the distinction most likely to be got wrong.

**The repository has four zones, not two.** "Modules and pages" under-specifies it, and the under-specification is why ownership has never been assignable.

| Zone | Path | Owns business rules? | May import |
|---|---|---|---|
| **Z-C · Application composition** | `internal/bootstrap`, `shared/{httpserver,module,web/shell,web/render,foundation/di}`, `cmd/server` | **No. Zero.** | all four zones |
| **Z-D · Domain business core** | `internal/modules/<ctx>/**` | **Yes — sole authority** | own module, Z-P, contracts |
| **Z-U · UI business domain** | `internal/pages/<page>/**` | **Yes — own read side only** | own page, Z-P, contracts |
| **Z-P · Extractable platform** | `internal/shared/**` (rest) | **No** | itself, stdlib, templ |

**The distinguishing test, since both Z-C and Z-U emit HTML.** It is not *who writes the response*; it is *what they are allowed to know*:

- **Z-C** knows every context's **route, manifest and breaker**, and **no** context's **vocabulary**.
- **Z-U** may reach **several** contexts' **declared interfaces**, and **no** context's **internals**.

> **Corrected by [`DECISION_UI_OWNERSHIP.md`](./DECISION_UI_OWNERSHIP.md).** This bullet
> originally read "knows **one** context's vocabulary in depth and **no** other context's
> routes". That described a constraint the code happened to have — there was no cross-unit
> composition mechanism at all — and presented it as a design principle. A page may
> legitimately compose features owned by several teams. The rule that matters is not "one
> context per page" but that a page reaches another team's **declared interface**, never its
> internals.

A page is not a view layer. `internal/pages/seller` consumes four contracts, shapes `results.Transaction`, and owns the console's HTTP error vocabulary (`internal/pages/seller/httperr/`) — it has more business surface than `internal/modules/ops`. Under this model it is a UI business domain: *Merchant Console*, owning the POS console end to end and composing features from other teams only through their declared interfaces.

**Test.** import rules 1–4 (C) + `TT-040` (A).

### 2.6 B6 · Distribution boundary — monorepo selected

Correction 2. **Keep one Git repository with multiple Go modules.** The repository
contains an application, independently buildable services, and shared wire
contracts. A Go module boundary is not a repository boundary, and neither one
automatically decides team ownership or deployment.

**The directory layout makes those boundaries visible:**

| Path | Responsibility |
|---|---|
| `apps/web/` | Gateway Go module, SSR, pages, and in-process modules |
| `apps/mobile/` | Mobile client boundary; native navigation and presentation |
| `apps/desktop/` | Desktop client boundary; desktop navigation and presentation |
| `services/<name>/` | One Go module and service deployment boundary per service |
| `contracts/` | Protobuf source and generated Go contract module |
| `go.work` | Explicit local workspace containing all modules |
| Repository root | Compose, Make, CI, deployment manifests, and docs |

The directory and build guidance is documented in
[`MONOREPO_STRUCTURE.md`](./MONOREPO_STRUCTURE.md). Service checks use
`GOWORK=off` to prove each module builds independently; the root workspace is
for local development and does not merge service releases.

**Keep ownership and distribution separate.** Teams own capabilities, not
directories merely because they share a repository. Services communicate
through versioned contracts and events, not imports of one another's private
packages. The monorepo gives reviewers one change set when an intentional
contract rollout touches several consumers.

**Extraction trigger.** Revisit polyrepo only when a service has a distinct
owner and independent release process, repository access needs differ, or a
shared contract needs its own published lifecycle. An extraction must preserve
module independence, compatibility checks, contract versioning, and service
ownership. Until then, `services/` is the canonical location for platform
services; `internal/` is reserved for packages private to their owning Go
module. The former TT-062 freeze on moving or adding services is retired by
this decision.

The mobile and desktop directories are architecture boundaries, not runnable
clients yet. Their framework, client generation, release packaging, and CI are
chosen when each gets its first end-to-end workflow. Web `templ.Component`
primitives stay web-specific; native clients own their platform primitives.
Cross-platform design tokens should be extracted only after a second real app
consumes them and the source format is agreed.

---

## 3. Deletion register

Correction 1: **when two things are duplicated, delete one.** Do not reconcile, do not abstract, do not add a flag. A duplicate is a second authority (§2.4) or a second source of truth; both are defects with a maintenance cost that is invisible until someone picks the wrong one.

**Deletion order matters.** Each row lists what must be true before it can be deleted, so the sequence is safe.

| ID | Delete | Duplicates / why it must go | Precondition | Owner |
|---|---|---|---|---|
| **DEL-1** | `internal/modules/media/**` (8 files + migration) | External `media-service` is the sole authority for media (D-018's disease, uncured — §2.4 hole 1). `contracts/proto/media/` does not exist, so nothing external depends on it | Confirm `media-service` serves every method the module exposes. Nothing in `internal/` imports the module. Both already true. | T1 |
| **DEL-2** | `contracts/proto/reporting/v1/**` | A published contract whose only provider is DEL-3. A contract with no provider is a promise no one keeps | DEL-3 | T1 |
| **DEL-3** | `internal/modules/reporting/**` (incl. `0001_reporting.sql` and the zero-returning `Store`) | Derives `dashboard_stats` / `low_stock_items` / `top_products` from Inventory + Sales + Catalogue state, written by nobody, read by nobody (§2.4 hole 2). Un-ownable by any stream team | Confirm no query depends on those three tables. `grep -r dashboard_stats` returns only this module. | T1 |
| **DEL-4** | The `metrics` module in the `README.md:104` ASCII diagram | Does not exist. `/metrics` is served by `httpserver` + `ops` | none | T5 |
| **DEL-5** | `billing/v1`, `media/v1` rows in `ARCHITECTURE.md` §1 contract-family table | Neither exists under `contracts/` (§1.3 G6) | none | T5 |
| **DEL-6** | The `make extract-check` step in `.github/workflows/ci.yml:56-59` | Targets `make extract-check` and `scripts/extract-catalog-repo.sh`; neither exists. D-018 removed what they referenced | Replace with the check it was gesturing at: does `internal/shared/**` still pass the extractability test? (TT-052) | T5 |

### 3.1 What must **not** be deleted

Recorded explicitly, because "delete all duplication" is a rule that needs a counter-rule.

| Keep | Why |
|---|---|
| `internal/pages/{seller,home,auth,profile}/**/infrastructure/*client` | Not duplication — thin adapters *embedding* the shared kits (§2.3). Deleting them would delete the ports-and-adapters pattern |
| `internal/shared/productconsumer`'s four separate breakers and four separate `Caller`s | Deliberate: *"separate on purpose: the storefront must keep serving browse while search is down"* |
| `grpcx.InProcess` **and** `grpcx.Dial` | Both are needed. One is today's modular-monolith rung, the other is tomorrow's service rung (§4) |
| `internal/modules/billing` | **Keep, reclassify.** It is *not* a bounded context — it owns no invoice rule; it renders an activity projection fed by `product.changed.*`. But under the hybrid doctrine (§4) it is a correctly-shaped **rung-1 module**: manifest + routes + event subscription + render strategy, flat layout because it has no domain complexity (§6.3). Its defect is its *name* and its ubiquitous language, not its shape. Fix by renaming, not deleting. |

---

## 4. The hybrid doctrine: modular monolith first, service on trigger

Correction 5. This is now the spine of the architecture.

### 4.1 The position

> **The default unit is a module inside one process. A service is what a module becomes when a named trigger fires. Never start at rung 3.**

"Simpler than microservices while maintaining modularity… easy refactoring to microservices if needed" is the modular-monolith position, and it is the correct default for a team of this size.

### 4.2 The escalation ladder

Five rungs. Each has a promotion trigger and a stated test.

| Rung | Unit | Separate process? | Owns data | Full hexagon? | Promotion trigger to next rung |
|---|---|---|---|---|---|
| **0** | **Platform package** — `internal/shared/**` | no | no | no | **never** — if it owns business rules it is not rung 0, it is a domain and must leave |
| **1** | **Module** — manifest + routes + ports, no domain | no | no | no | It acquires a rule that cannot be expressed as a port precondition |
| **2** | **Domain module** — `domain/` + `application/` + `ports/` + `infrastructure/` + `presentation/` + `compose/` | no | yes | yes | It needs an independent scaling profile, release cadence, data authority, runtime, or blast-radius isolation (§4.3) |
| **3** | **Service** — own process, own database | **yes** | yes | yes | It needs an independent release cycle *and* a separate team |
| **4** | **Externally-owned service** — independently owned and deployed; may later use a separate repo and versioned contract | yes | yes | yes | terminal unless the ownership/deployment trigger changes |

**Where every unit currently sits:**

| Unit | Rung | Note |
|---|---|---|
| `internal/shared/**` | 0 | extractability test passes |
| `internal/modules/billing`, `internal/modules/ops` | 1 | no domain rules |
| `internal/modules/auth` | 2 | PIN hashing, session TTL, role rules |
| `internal/modules/inventory` | 2 — **mis-shaped** | see §4.4 |
| `internal/modules/sales` | 2 | checkout saga + compensation |
| `services/product-service` | 3 | sole write authority + outbox |
| `services/product-listing-service` | 3 | independent scaling (cache + projection) — trigger fired |
| `services/product-search-service` | 3 | independent scaling + separate ranking logic |
| `services/aggregator-service` | **0 at rung 3** | a composition layer that happens to run as a process. See §4.5 |
| `internal/pages/**` | **Z-U** | not on this ladder; governed by §2.5 |

### 4.3 Promotion triggers, enumerated

A module may be promoted to rung 3 **only** when a trigger is named in `team_catalog.yaml`. No trigger, no promotion.

| Trigger | Example in this repo | Status |
|---|---|---|
| **T-scaling** — independent scaling profile | `product-listing-service`: read-heavy, cache-backed, must scale separately from writes | **fired** |
| **T-cadence** — independent release cadence | *not fired for any module today* | — |
| **T-data** — owns data others must not write | `product-service`: the product aggregate, D-018 | **fired** |
| **T-runtime** — needs a different runtime or scale unit | *not fired* | — |
| **T-blast-radius** — a failure must not take down the caller | *arguably fired for listing/search*; defensible either way | **contested** |
| **T-contract-consumer** — consumed by a team in a different repository | *not fired; modules currently share a monorepo* (§2.6) | — |

**T-cadence is the one people reach for first and it is the weakest**, because cadence is an observable, not a constraint: two contexts can have different release days inside one process with no cost whatsoever. Team Topologies' fracture-plane guidance reaches the same conclusion from the other side — technology-driven splits should cut *deployables*, not *teams*.

### 4.4 A rung-2 module that is not actually shaped like one

**Finding (F1).** `internal/modules/inventory`:

- `var _ ports.StockService = (*StockRepository)(nil)` in `infrastructure/repository.go` **and** `infrastructure/postgres/repository.go`
- there is **no `application/service.go`** and **no `domain/`**
- the interface is named `…Service` but is satisfied by persistence adapters

So the context's invariant — *"what makes the adjustment atomic and what refuses to oversell"* — lives in SQL:

```sql
UPDATE inventory_stock SET quantity = quantity + $3, updated_at = now()
 WHERE tenant_id = $1 AND product_id = $2 AND quantity + $3 >= 0
 RETURNING quantity
```

The rule is correct, and the comment explains why it is correct *at that layer*.

> **Corrected, verified 2026-10-15.** An earlier draft of this section concluded that
> "an in-memory adapter is a different rule". That was asserted without reading the
> adapter, and it is wrong: `infrastructure/repository.go` refuses the adjustment the
> same way. So this is **duplication, not divergence** — one invariant written twice,
> in two persistence adapters, rather than defined once in `domain/`. That is a
> maintenance risk, not a present correctness defect, and it is tracked as such rather
> than described as a live violation.

**Per Khononov**, tactical pattern choice must be driven by the complexity of the business logic, not developer preference — a ubiquitous language is not a glossary ([Learning DDD](https://www.oreilly.com/library/view/learning-domain-driven-design/9781098100124)). Inventory has real rules, so it earns a `domain/` and an `application/service.go`; the conditional UPDATE remains as the *enforcement mechanism*, defined by a domain rule it satisfies.

**This is the first thing the Phase 0 pilot (§12) touches.**

### 4.5 The aggregator is rung 0 wearing a rung-3 costume

Correction 5 interacts with §2.5 here. `aggregator-service` runs as a separate process, which makes it look like rung 3. It is not:

| Rung-3 criterion | aggregator | |
|---|---|---|
| Owns an aggregate with identity | no persisted entity | ❌ |
| Owns invariants it alone enforces | all behind `product.v1`, `category.v1` | ❌ |
| Ubiquitous language, not transport vocabulary | `Query`/`Mutation`/`Page`/`Resolver` | ❌ |
| Domain model | **no `domain/` package at all** | ❌ |
| Owns persistence | no DB driver, no ORM, no migrations | ❌ |
| Sole write authority | delegates to `product.v1` | ❌ |
| Publishes domain events | no producer | ❌ |

Seven fails. Its own source says so twice: *"the composition layer in front of the five platform services. **It owns no data and no database.**"* (`README.md:1-5`, `cmd/server/main.go:1-6`).

**It is Z-C that happens to be a separate process.** That is a legitimate and common shape — a backend-for-frontend is composition, not domain — but it must be *declared* as such, because the process boundary will otherwise be read as a domain boundary. The six platform services it dials, and the two plain-HTTP status endpoints it reads, are the proof: it delegates everything.

**What it does own: a published contract.** The 434-line GraphQL SDL with 10 queries and 10 mutations is a consumer-facing API with a hard breaking-change surface. So it is a **provider of `graphql.aggregator/v1`** and a **consumer of five gRPC contracts** — two X-as-a-Service relations, and the reason it needs a schema gate (§9.3) despite owning no domain.

**Tripwire (TT-046).** The aggregator's architecture test asserts its transitive import closure contains no `database/sql`, no `pgx`/`sqlx`, no ORM, and no `migrations/` path. If someone adds a database, it has become a sixth authority for the catalogue — the exact outcome its own comment names.

---

## 5. Frontend architecture and render strategy

Correction 1 makes these sections *supporting* rather than central: the UI boundary is already the best-governed part of this repository, and the PRD's job is to stop it regressing.

### 5.1 Where the federation mechanism actually sits

gogo federates **HTML fragments**, not JavaScript remotes. The lineage matters because the repository's own doc comment places it in the wrong one.

> `internal/shared/module/module.go`: `Entry` is *"the module's `remoteEntry.js` equivalent — the HTML fragment contract everything else loads from"*

It is **not** `remoteEntry.js`. `Entry` is a path that `httpserver.App` mounts in the same process. Nothing fetches it at runtime.

| Approach | Composition time | gogo |
|---|---|---|
| Server-side template composition | request | **closest** |
| Build-time integration | build | ✗ forces coordinated rebuild |
| Runtime via `<iframe>` | load | ✗ |
| Runtime via JavaScript (Module Federation, single-spa) | load | ✗ |
| Runtime via Web Components | load | ✗ |
| **Hypermedia federation (htmx / Hotwire)** | request | ✅ **this is gogo** |

Sources: [Jackson's taxonomy](https://martinfowler.com/articles/micro-frontends.html) · [microfrontend.io patterns](https://microfrontend.io/patterns/) · [ThoughtWorks Radar 2016](https://www.thoughtworks.com/radar/techniques/micro-frontends) · htmx's contract, *"Htmx expects responses to the AJAX requests it makes to be HTML, typically HTML fragments"* ([htmx](https://four.htmx.org/docs/core-concepts/requests-and-responses.md)).

**What the hypermedia position buys, per the literature:** no shared-JS singleton problem (Module Federation requires `shared` singletons or the two-copies-of-React bug appears); no CSS namespace collision, the failure Jackson names for runtime JS integration; and no JSON-then-client-template step — Hotwire's position is *"you don't need to serialize the new message as JSON, receive it in JavaScript, render a client-side template. It's just the standard server-side templates reused"* ([Turbo](https://turbo.hotwired.dev/handbook/streams)).

**What it costs, stated honestly:** a module cannot be deployed independently of the gateway. gogo is a *federated modular monolith*, not independently deployable frontends. Correct at this size; must be written down rather than implied. `TT-029` requires the `module.go` comment be corrected.

**TT-063 (fragment import rules).** A module's fragment may reference only its own `views`, `shared/web/ui`, and `shared/web/navigation`. It may **not** reference another module's views, and may **not** reference a page's views. This is the no-cross-remote-imports rule from Module Federation, expressed for hypermedia.

### 5.2 Render strategy is HTTP content negotiation

A page's composition layer owns **arrangement, not content**: the placement of features
on a page is the page-owning team's decision, and no named enterprise system gives that
decision to a feature team. Per-feature render strategy is declared per island and is owned
by the feature team. See [`DECISION_UI_OWNERSHIP.md`](./DECISION_UI_OWNERSHIP.md).

`internal/shared/web/render/strategy.go` implements RFC 9110 §12, in which a *representation* is *"information that is intended to reflect a past, current, or desired state of a given resource, in a format that can be readily communicated via the protocol"* ([RFC 9110](https://www.rfc-editor.org/rfc/rfc9110)). A partial HTML page is a representation.

```
KindNavigation:  IsHTMX(r) ? Fragment : Page
KindMutation:    IsHTMX(r) ? Fragment : 303 → Redirect      (Post/Redirect/Get)
default:         error — "unknown strategy kind"
```

Matches three independent precedents: htmx's documented `if HX-Request: render partial else: render full` ([htmx](https://four.htmx.org/reference/headers/HX-Request)); Hotwire injecting `text/vnd.turbo-stream.html` into `Accept` so `format.turbo_stream` is literally negotiated ([Turbo](https://turbo.hotwired.dev/handbook/streams)); Rails `respond_to` ([Rails](https://api.rubyonrails.org/v7.2/classes/ActionController/MimeResponds.html)). `a-h/templ` ships it natively as `templ.Fragment(id)` ([templ](https://pkg.go.dev/github.com/a-h/templ@v0.3.1020)).

**The `render` package is a render graph, and it should be named as one.** `RenderMode` (`ModeStatic`=SSG, `ModeRevalidate`=ISR, `ModeDynamic`=SSR, `ModeClient`=CSR) × `Freshness` × `RenderingContext` → `ContractPlanner` → `Composer` with `singleflight` (cache-stampede defence) and `prefixGen` (generation invalidation that cannot race a render). The literature's names for this shape: Unity's render pipelines ([Unity](https://docs.unity.com/en-us/engine/7000.0/manual/render-pipelines/overview)), Frostbite's FrameGraph ([GDC](https://gdcvault.com/play/1024612/FrameGraph-Extensible-Rendering-Architecture-in)), CDK's deterministic cloud-assembly synthesis ([CDK](https://docs.aws.amazon.com/cdk/v2/guide/configure-synth.html)).

**Determinism requirements.** `TT-043` — `render.Respond` is the **only** permitted response path; a handler may not call `component.Render`, `html/template`, or write a status code itself. `TT-044` — a `KindNavigation` strategy must supply both `Page` and `Fragment`; a `KindMutation` must supply `Fragment` and `Redirect`; a missing field fails at construction. `TT-045` — `ContractPlanner` is a **pure function** of `RenderingContext` plus registered island declarations: no clock, no network, no map-iteration order. This is the CDK deterministic-hash rule applied to HTML, and it is what makes a cached fragment reproducible in review.

### 5.3 The primitives layer already has a written contract

`internal/shared/web/ui/doc.go` states a one-way dependency, an import allowlist (stdlib, `templ`, `internal/shared/money`), a naming convention (`<Thing>Class`, `<Component>Options`, `attrs` last), and names its enforcer. That is a design-system boundary in the Radix/Storybook sense, where the public entry is one file and accessibility is tested at the component level — because *"a broken button style isn't one bug — it's hundreds of affected screens"* ([visual regression](https://www.visual-regression-testing.dev/storybook-visual-regression-testing)).

The four-zone map in §2.5 is [Feature-Sliced Design](https://feature-sliced.design/docs) with the naming fixed. FSD's definition of `shared` is the load-bearing part: it contains *no business logic at all*, which is why it has no slices — identical to gogo's `internal/shared/` and identical to the extractability test already implemented as `platform must not import app code`.

FSD has no equivalent for the **server-side** composition plane, which is why §2.2 and §4.5 use a different test for the aggregator. FSD is a frontend architecture; the server needs the seven-criterion aggregate test.

**TT-064:** make the `ui` naming convention lint-enforced, and require every new primitive to have ≥1 real consumer. A primitive with no consumer is dead vocabulary.

---

## 6. Target topology

### 6.1 Team ids are placeholders with a substitution procedure

The repository contains **zero** organisational information. §5 of `docs/FOLLOW_UP.md` and the CI config show a one-team reality. Naming real teams here would be fiction.

So: **team ids** `T1`…`T5`, a **substitution procedure**, and a hard rule.

**Substitution procedure.** Every unit in `team_catalog.yaml` carries a `team: Tn` field. When real teams exist, replace `Tn` with the team id; the validator does not care. The topology's *shape* — which units are together, which are apart, which edge has which interaction mode — is what this PRD asserts; the *labels* are placeholders.

**Hard rule (TT-065):** the number of teams is a **consequence** of the boundaries, never an input. If the org has three teams, the boundaries merge and `team_catalog.yaml` says so. If a boundary cannot be staffed, the boundary is wrong.

### 6.2 T1 · Catalogue

**Type:** stream-aligned with a platform interface. Team Topologies' 2nd edition makes a platform *"a grouping of other team types"* and highlights *"the fractal nature of successful organizational design"* ([TT](https://teamtopologies.com/book)) — a team owning a domain and serving it to internal consumers via a versioned contract is a legitimate hybrid.

| Owns | Rung |
|---|---|
| `services/product-service/**` | 3 — sole write authority for the product aggregate |
| `services/product-listing-service/**` | 3 — browse read model |
| `services/product-search-service/**` | 3 — search read model; owns the `search.Engine` seam |
| `services/aggregator-service/**` | 0 at rung 3 (§4.5) |
| `contracts/proto/{product,productlisting,productsearch,category,media}/**` | content |

**Why one team for four deployables.** Three reasons, all structural:
1. **Fracture plane.** They are four deployables of one business domain. Business domain is the *default* plane; technology (the CQRS read/write split) justifies separate *deployables*, not separate *teams* — cutting teams by technology guarantees every catalogue change crosses a boundary.
2. **B4.** One team, one aggregate, one vocabulary. This is the structural guarantee D-018 relied on and did not fully deliver.
3. **Cognitive load.** Four deployables of one *complicated* domain is already at heuristic 3's limit — a team owning a complex domain *"should not have any more domains assigned to them, not even a simple one"* ([heuristics](https://itrevolution.com/articles/minimize-cognitive-load-of-teams/)). Splitting into two teams would give each two complicated domains, which *"in practice… the team will behave as two subteams."*

**Split trigger (TT-018), all three required:** the aggregator needs its own release cadence for ≥2 consecutive quarters, **and** its own on-call rotation, **and** the other three release independently of it.

The service modules may be extracted into separate repositories later, but catalogue ownership remains a single-team responsibility unless an explicit domain boundary changes that decision.

### 6.3 T2 · Merchant Console

**Type:** stream-aligned. Owns the seller's job end to end: prepare catalogue → ring up a sale → adjust stock → read history.

| Owns | Rung |
|---|---|
| `internal/pages/seller/**` | Z-U |
| `internal/modules/inventory/**` | 2 — **mis-shaped, §4.4** |
| `internal/modules/sales/**` | 2 — checkout saga + compensation |
| `contracts/proto/{inventory,sales}/**` | content |

**Declared heuristic-4 violation.** T2 holds two *complicated* domains. Declared openly rather than breached silently:

- They share a change cadence — both move on merchant-feature days, not independent schedules.
- They share a user and a screen flow; splitting them puts a handoff *inside one interaction*, which is the handoff-dominates-lead-time anti-pattern.
- The saga is the reason they are coupled; the alternative is a distributed workflow across two teams, strictly worse at this size.

**Split trigger (TT-019), any two of:** inventory takes a weekly release while sales takes monthly; a second merchant surface appears (purchasing/replenishment), giving Inventory a UI independent of the POS; the team exceeds 9 people.

### 6.4 T3 · Storefront, T4 · Identity, T5 · Gateway Platform

**T3 · Storefront** — stream-aligned, simple. Owns `internal/pages/home/**` including `islandkeys/` (a page-owned cache namespace, explicitly *"lifted out of the catalog module along with the page, so catalog must not know about it"*). Consumes `productlisting.v1` + `productsearch.v1`.

**T4 · Identity** — stream-aligned, simple. Owns `internal/modules/auth/**`, `internal/pages/auth/**`, `internal/pages/profile/**`, `internal/shared/authconsumer/**` (its own kit), `contracts/proto/auth/**`. One domain, two zones (Z-D + Z-U), one team.

**T5 · Gateway Platform** — platform. *"A grouping of other team types that provide a compelling internal product to accelerate delivery by Stream-aligned teams"* ([teamtopologies.com](https://teamtopologies.com/key-concepts)). Owns `internal/bootstrap`, all of `internal/shared/**`, `cmd/**`, `contracts/` plumbing, `Dockerfile`, `docker-compose.yml`, `deploy/**`, `.github/**`, `Makefile`, root `devenv.nix`.

**Platform fitness (TT-020):** judged by **extraneous load removed from T1–T4**, not by output. Fails if adopting it is harder than self-hosting — *"if adopting it is harder than self-hosting, it's a liability, not a platform."*

**Not a shared-services queue:** `internal/shared/**` is governed by a hard extractability test plus a **two-consumer minimum**. That is the difference between a paved road and a bottleneck.

### 6.5 Enabling routines

T5 owns software, so these are **routines**, not a separate team; enabling teams *"should not own any software components"* and the mode is *"temporary… generally last for no more than a few weeks"* ([interaction modelling](https://teamtopologies.com/key-concepts-content/team-interaction-modeling-with-team-topologies)).

| Routine | Mode | Exit condition |
|---|---|---|
| **E1** module onboarding: `modulegen` → `Register(b)` → manifest → `/module-registry` | facilitating | team ships a module without opening `internal/bootstrap` |
| **E2** consumer-kit adoption: replace a hand-rolled client with `grpcconsumer.Caller` + `Vocabulary` | facilitating | passes breaker, tenant-threading, error-classification tests |
| **E3** devenv adoption | facilitating | `devenv shell` reproduces the toolchain on a clean machine |
| **E4** architecture-rule authoring | facilitating | the rule runs in `make check` |

**TT-021:** any routine that has run three times without its exit condition met is reclassified as an X-as-a-Service obligation (T5 builds it) or cancelled.

---

## 7. Ownership matrix — consumer, provider, interaction mode

**Every row must be enforceable.** Rows marked *declared* are A-rung until §12 delivers the validator.

### 7.1 In-process module contracts

gRPC over `grpcx.InProcess`, so the deployment boundary is same-process while the contract boundary is real.

| ID | Contract | Provider | Consumer(s) | Mode |
|---|---|---|---|---|
| C-01 | `inventory.v1/InventoryService` | T2 (`modules/inventory`) | T2: `modules/sales` via `infrastructure/inventoryclient`; T2: `pages/seller` via `infrastructure/inventoryclient` | X-as-a-Service (intra) |
| C-02 | `sales.v1/SalesService` | T2 (`modules/sales`) | T2: `pages/seller` | X-as-a-Service (intra) |
| C-03 | `auth.v1/AuthService` | T4 (`modules/auth`) | T4: `pages/auth`, T4: `pages/profile` — both via `shared/authconsumer` | X-as-a-Service (intra) |
| C-04 | `event/product.changed.*` | **T1** (`product-service` → Kafka `product.events.v1` → `shared/productevents` bridge) | T3 (`pages/home` — cache invalidation), T5 (`modules/billing`) | X-as-a-Service |

**C-04 is one-way and strict.** *"The events it consumes are published by the gateway's productevents feed, not by anything it imports"*; *"a page never publishes, it only reacts to keep its own cache fresh"* (`islandkeys.go`, `pagekit.go`). **TT-022:** no `events.Publish` outside `internal/shared/productevents` may use a `productevents` type constant.

### 7.2 The product platform — the cross-boundary seam

| ID | Contract | Provider | Consumer | Tripwire |
|---|---|---|---|---|
| C-05 | `product.v1/ProductService` | T1 | T2 `pages/seller` (writes); T5 `bootstrap` → `productconsumer` | breaker `upstreamProduct` |
| C-06 | `productlisting.v1/ProductListingService` | T1 | T3 `pages/home`; T5 `bootstrap` | breaker `upstreamProductListing` |
| C-07 | `productsearch.v1/ProductSearchService` | T1 | T3 `pages/home`; T5 `bootstrap` | breaker `upstreamProductSearch` |
| C-08 | `category.v1/CategoryService` | External | T1 `product-service` (write-time `CategoryLookup.Exists`); T2 `pages/seller`; T1 `aggregator` | breaker `upstreamCategory` |
| C-09 | `media.v1/MediaService` | External | T1 `product-service`; T1 `aggregator` | **no breaker configured — R-07** |
| C-10 | `GET /api/v1/listing/status` | T1 | T1 `aggregator` `ProjectionStore` | — |
| C-11 | `GET /api/v1/search/status` | T1 | T1 `aggregator` `ProjectionStore` | — |

**These rows are correct if a service is extracted later**, because ownership follows the team and contract rather than the directory or repository.

### 7.3 Platform capabilities (T5 as provider)

| ID | Capability | Consumers | Tripwire |
|---|---|---|---|
| C-12 | `shared/module` — `Descriptor`, `Registry`, lifecycle | T5 `bootstrap`; T2/T4 modules | import rule 4 |
| C-13 | `shared/web/render` — `Strategy`, `Respond`, `Composer`, `Planner` | every handler in Z-C/Z-D/Z-U | TT-043/044 |
| C-14 | `shared/web/ui` — primitives | every view | `ui primitives depend on no app code` |
| C-15 | `shared/web/pagekit` | Z-U pages | `platform must not import app code` |
| C-16 | `shared/grpcconsumer` — `Caller`, `Do[T]`, `Vocabulary` | T1, T2, T4, T5 | imports no wire types, no app code |
| C-17 | `shared/productconsumer` | T2 `pages/seller`, T5 `bootstrap` | import rule 6 |
| C-18 | `shared/authconsumer` | T4 | — |
| C-19 | `shared/foundation/di` — `Container` | T5 `bootstrap`, T2/T4 `Register(b)` | — |
| C-20 | `shared/events` | T2 `billing`, T3 `pagekit`, T5 | — |
| C-21 | `shared/{breaker,tenant,session}` | all | — |
| C-22 | `cmd/modulegen` | T2, T4 | TT-051 |
| C-23 | root `devenv.nix` | T1–T4 | TT-053 |

### 7.4 Interaction modes per pair

| Pair | Mode | Note |
|---|---|---|
| T1 → T2, T3 | X-as-a-Service | the product platform is the clearest internal product in the repo |
| T2, T3 → T5 | X-as-a-Service | primitives, render strategies, DI, kits |
| T1 ↔ T5 | **collaboration, time-boxed** | T5 authors the client kits, T1 authors the servers; must converge to X-as-a-Service |
| T5 → any | **facilitating** | the six enabling routines |
| T4 → T5 | collaboration, short | session identity is threaded through the shell |

*"Collaboration is for discovery and is expensive — it blurs boundaries and raises both teams' cognitive load; time-box it."* A well-designed org needs **less** inter-team communication, not more.

### 7.5 Cognitive load, applied honestly

Difficulty rule: *simple* = clear path of action; *complicated* = needs analysis, may take iterations; *complex* = needs experimentation ([heuristics](https://itrevolution.com/articles/minimize-cognitive-load-of-teams/)).

| Context | Difficulty |
|---|---|
| Catalogue (product aggregate) | complicated |
| Search projection | complicated (ranking; `word_similarity` returns 1.0 for every hit and so cannot rank) |
| Aggregator (composition) | complicated — but **no domain** |
| Inventory | complicated (atomic non-negative invariant) |
| Sales / POS | complicated (saga + compensation) |
| Identity, browse projection, storefront | simple |
| Billing | **not a context** — a rung-1 module |

One confirmed violation (T2, §6.3), declared with a trigger.

**The controlling principle is architectural:** *"organizations should not allow a software subsystem to grow beyond the cognitive load of the team responsible for the software"* ([TT](http://arafatm.com/mba/team-topologies-organizing-business-and-technology-teams-for-fast-flow)).

---

## 8. The metadata contract

> **Scope note:** this section's catalog is the *architecture catalog* (units,
> owners, contracts, and topology). It is distinct from the runtime product
> catalog described in §8.6. Both use metadata, but they have different owners,
> storage, change controls, and validators.

### 8.1 One catalog, derived not duplicated

The repository is already metadata-driven in four disconnected places:

| Metadata | File | Validated by | Rung |
|---|---|---|---|
| Protobuf schema | `contracts/proto/**`, `buf.yaml`, `buf.gen.yaml` | `buf lint`, `buf breaking`, codegen | C |
| Nx graph + tags | `project.json` × 6 | nothing — **Nx is not in CI** | A |
| Bazel `deps` → graph | `BUILD.bazel` → `import_graph.txt` | `make arch-graph`, `make check` | **D** |
| Runtime manifests | `module.Descriptor` → `/module-registry` | nothing | A |

Four sources, three rungs, one of them drift-proof. The literature's central warning applies exactly: **two sources of truth → drift** ([Terraform](https://developer.hashicorp.com/terraform/tutorials/state/resource-drift)); the design-tokens community describes the identical disease in the UI layer ([DTCG announcement](https://lists.w3.org/Archives/Public/public-design-tokens/2025Oct/0003.html)).

**The fix is Backstage's generalised rule: when two things describe the same fact, designate one authoritative** — and for derived relations, *the producer wins* ([descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/)).

**Therefore: `team_catalog.yaml` does not restate code edges as hand-written truth.** It declares *intent*; the validator asserts **declared == derived** against `import_graph.txt`, the bootstrap wiring and the contracts tree. Exactly the discipline `make check` already applies to import-graph staleness, and the ratchet used by dependency-cruiser's `--baseline-mode shrink-only` ([cli.md](https://github.com/sverweij/dependency-cruiser/blob/main/doc/cli.md)) and ArchUnit's freezing ([§8.6](https://www.archunit.org/userguide/html/000_Index.html)).

### 8.2 Schema

`internal/architecture/team_catalog.yaml`, next to `import_graph.txt` so a change to either is a diff in the same directory.

```yaml
apiVersion: gogo.dev/v1
kind: TeamCatalog

teams:
  - id: T2
    name: Merchant Console
    type: stream-aligned          # stream-aligned | platform | enabling | complicated-subsystem
    charter: Owns the seller's job: prepare catalogue, ring up a sale, adjust stock.
    contact: { channel: "#team-merchant" }
    declared_violations:
      - rule: cognitive-load-4
        detail: "Inventory and Sales are both complicated."
        until: TT-019-trigger-met
        owner: T2

units:
  - path: internal/modules/inventory
    kind: module
    team: T2
    rung: 2                       # 0..4 per §4.2
    tier: business                # composition | business | platform | foundation
    context: inventory            # null for composition and platform
    complexity: complicated
    provides: [inventory.v1/InventoryService]
    consumes: []
    promotion_triggers: []        # rung 2 -> 3 requires a named trigger

  - path: internal/bootstrap
    kind: composition
    team: T5
    rung: 0
    tier: composition
    context: null                 # a composition layer has no bounded context
    provides: []
    consumes: [product.v1/ProductService, productlisting.v1/ProductListingService,
               productsearch.v1/ProductSearchService, category.v1/CategoryService]

contracts:
  - id: inventory.v1/InventoryService
    kind: grpc
    proto: contracts/proto/inventory/v1/inventory.proto
    provider: T2
    consumers: [T2]
    breaking: PACKAGE             # FILE | PACKAGE | WIRE_JSON | WIRE
    interaction: x-as-a-service

  - id: event/product.changed.*
    kind: event
    topic: product.events.v1
    provider: T1
    consumers: [T3, T5]
    breaking: BACKWARD            # Confluent: non-transitive so consumers can rewind
    direction: provider-to-consumer-only
```

### 8.3 Additive extension to `module.Descriptor`

```go
// --- added by TT-028; all optional, derived from team_catalog.yaml ---
Team     string   `json:"team,omitempty"`
Tier     Tier     `json:"tier,omitempty"`
Rung     int      `json:"rung,omitempty"`
Context  string   `json:"bounded_context,omitempty"`
Provides []string `json:"provides,omitempty"`
Consumes []string `json:"consumes,omitempty"`
```

- **TT-028** — every added field must equal the catalog entry. Divergence fails `make check`.
- **TT-029** — stop calling `Entry` a `remoteEntry.js` equivalent. It is a mounted route; nothing fetches it.
- `Nav` is deliberately untouched: navigation is Z-C's concern, and putting team metadata there would couple presentation to organisation.

### 8.4 Nx tags — completed

| Tag | Values |
|---|---|
| `tier:*` | `composition` · `business` · `platform` · `foundation` — **`tier:business` does not exist today** |
| `scope:*` | `gateway` · `catalogue` |
| `type:*` | `app` · `lib` |
| `lang:*` | `go` |
| `team:*` | `T1`…`T5` |
| `context:*` | bounded-context slug; absent for `tier:composition`/`platform` |
| `rung:*` | `0`…`4` |

**TT-030:** every path under `internal/modules/**` and `internal/pages/**` gets a `project.json`. Today none does — which is *why* the taxonomy is unenforced: most units are not in the graph. Nx supports multi-dimensional tags and enforces `depConstraints` over the graph ([Nx](https://nx.dev/docs/features/enforce-module-boundaries)).

### 8.5 Contract IDs

`<pkg>.<ver>/<Service>` · `event/<topic>.<verb>` · `http+<METHOD> <path>` · `graphql.<svc>/v1`. One string everywhere. **TT-031:** a contract ID used in code but absent from the catalog fails `make check`.

---


### 8.6 Runtime product metadata — admin-managed schema, code-owned invariants

**Decision.** Product types and attribute definitions are editable by an
authorized administrator through the product platform. Product Service stores
drafts and immutable published schema versions in its own database. It remains
the authority that validates product writes. This is runtime business metadata;
it does not belong in the architecture catalog or in seller UI code.

**Boundary.** Metadata describes the permitted shape of a product form: type,
attribute key and label, value kind, required status, options, and applicable
constraints. Product Service code enforces universal invariants and interprets
that definition. Admins cannot upload executable validation logic or arbitrary
code. Variant dimensions such as color and size remain variant data, not free
form descriptive attributes.

**Lifecycle.** An administrator edits a draft, validates and previews it, then
publishes a new monotonically versioned schema. A published version is
immutable. Seller forms fetch the published schema and submit its version with
the product. Product Service validates against that version and stores the
version alongside the values. Schema changes do not silently rewrite or
invalidate existing products; incompatible changes require an explicit product
migration or a compatibility policy.

**Consumers.** Product Service emits product snapshots with type, schema
version, and values through the existing event path. Listing and Search update
their own projections; SSR reads those projections and does not call Product
Service for each page render. Attribute facets are a later capability and need
an agreed query/index strategy.

**Editorial content is a separate capability.** Use a headless CMS for banners
and editorial page content, with an allowlisted set of renderable blocks. The
gateway owns SSR composition and renders those blocks using platform UI
primitives. Do not use the CMS as the source of truth for product validation.
Use Rspress for developer documentation authored and reviewed with the code;
publish its static build independently. Neither a CMS nor Rspress requires a
new product-service repository.

**Repository decision.** Keep the admin UI/API and schema persistence in the
existing product platform boundary. A new repository or service is justified
only when a distinct team/lifecycle needs independent ownership, or multiple
independent systems need to author and consume a shared catalog schema. A
repository split by itself does not make the feature dynamic.

**Acceptance checks for this capability:**

1. An authorized administrator can create and publish a product type without a
   code change or Product Service deployment.
2. Seller UI discovers the published type and renders its attributes from the
   schema contract; it contains no hardcoded type registry.
3. Product Service rejects unknown, stale-version, and invalid attribute
   values, and records the schema version used.
4. Listing and Search project product values from events; storefront SSR reads
   those projections without a per-request Product Service call.
5. Draft content is not publicly visible; published schema versions are
   immutable and auditable.

## 9. Validation

**Every row carries its current rung and the rung Phase 0 targets.**

| ID | Check | Rung now | Rung after Phase 0 |
|---|---|---|---|
| TT-034 | every unit has exactly one owning team | A | **B** |
| TT-035 | catalog edges == derived edges | A | **B** |
| TT-036 | every contract has exactly one provider | A | B |
| TT-037 | every edge declares an interaction mode | A | B |
| TT-038 | no team owns two complicated domains without a declared violation | A | B |
| TT-039 | a stream team holds ≤3 simple domains (heuristic 2) | A | B |
| TT-040 | composition layer contains no business rules | A | B |
| TT-041 | per-module hexagon rules — **closes the prose-only gap** | A | B |
| TT-042 | port names carry no technology token | A | B |
| TT-043 | `render.Respond` is the only response path | A | B |
| TT-044 | render strategies are complete | A | B |
| TT-045 | `ContractPlanner` is pure | A | B |
| TT-046 | the aggregator owns no persistence — §4.5 tripwire | A | **B** |
| TT-047 | no product **or media** aggregate is owned in-process — **closes DEL-1's hole** | A | B |
| TT-048 | every module and service is reachable from bootstrap — **closes DEL-3's hole** | A | **B** |
| TT-049 | event direction is provider-only | A | B |
| TT-050 | `Descriptor` metadata matches the catalog | A | B |
| TT-051 | the golden path compiles into the composition root | A | **B** |
| TT-052 | `internal/shared/**` passes the extractability test standalone | A | B |
| TT-053 | every unit has `devenv.nix` + a committed lock | A (2/6) | A |

Existing and already **C**: the seven import rules, import-graph freshness (**D**), `TestParserIsHonest`.

### 9.1 Ordering, and why it matters

```
1. YAML parses
2. schema validates            (additionalProperties: false)
3. referential integrity       (every team id / contract id resolves)
4. catalog semantics           (TT-034, TT-036..039)
5. catalog vs derived graph    (TT-035, TT-050)
6. code-level rules            (TT-040..049, TT-051, TT-052)
```

A schema failure must not be reported as a graph failure. Every published metadata format closes itself this way: `unevaluatedProperties: false` in the Serverless Workflow schema, `additionalProperties: false` in dependency-cruiser's own config, structural-schema reporting in Kubernetes CRDs. **Metadata without a validator is prose.**

**TT-033:** if a baseline is ever needed it must be **shrink-only** (violations may be removed, never added) and every baselined entry needs an owner and an expiry.

### 9.2 Make targets — *planned, not implemented*

Correction 3: these do not exist yet.

```make
arch-team-catalog:            ## regenerate the derived half and report diffs
	go -C apps/web test ./internal/architecture/ -run 'TeamCatalog|Descriptor' -update

check: vet test
	… existing gofmt / templ / buf / arch-graph checks …
	go -C apps/web test ./internal/architecture/ -run 'TeamCatalog|Descriptor'
	@echo "team catalog current and consistent with the import graph"
```

Promoting a check from **B** to **C** requires editing `.github/workflows/ci.yml`, which already runs `make check` — so B→C is a Makefile change alone, and that is the cheapest rung promotion available.

### 9.3 Breaking-change categories, assigned per contract

Buf's four categories are strictest-first and *"Passing a stricter category implies passing every looser one"* ([buf](https://buf.build/docs/breaking/rules/)).

| Contract | Category | Rationale |
|---|---|---|
| `product.v1`, `category.v1`, `media.v1` | `WIRE_JSON` | cross-repo; a field change with an uncoordinated consumer is exactly the D-018 scenario |
| `productlisting.v1`, `productsearch.v1` | `WIRE_JSON` | a field removal breaks `productconsumer.Core`'s union immediately |
| `inventory.v1`, `sales.v1`, `auth.v1` | `PACKAGE` | in-process; stricter would be noise |
| `event/product.changed.*` | `BACKWARD`, **non-transitive** | Confluent's rationale: *"that allows you to rewind consumers to the beginning of the topic"* ([Confluent](https://docs.confluent.io/platform/current/schema-registry/fundamentals/schema-evolution.html)) |
| `graphql.aggregator/v1` | **custom (TT-023)** | no `buf breaking` equivalent. Required: an SDL snapshot test + a "no new required input field" check, modelled on [AIP-180](https://google.aip.dev/180) |

**Field-number discipline (TT-024).** Protobuf's own rules: numbers `19000–19999` are implementation-reserved; numbers *"should never be reused"*; deletion requires `reserved`; and *"any wire-safe changes may be a breaking change to application code in a given language"* ([proto3](https://protobuf.dev/programming-guides/proto3/), [best practices](https://protobuf.dev/best-practices/dos-donts/)). Breaking changes require expand → migrate → contract ([Fowler](https://martinfowler.com/bliki/ParallelChange.html)); ThoughtWorks rates *API expand-contract* **Adopt** and notes the failure mode is *"complex versioning schemes and breaking changes used in scenarios where a simple expand and then contract would suffice"* ([Radar](https://www.thoughtworks.com/radar/techniques/api-expand-contract)).

---

## 10. Dev environment as metadata

**Current state:** 2 of 6 units have `devenv.nix` (`product-service`, `product-listing-service`); **no `devenv.lock` anywhere**; both existing files are still the `devenv init` scaffold (`env.GREET = "devenv"`, `scripts.hello`, no-op `enterShell`). The gateway, `contracts/`, `product-search-service` and `aggregator-service` have none.

**What devenv provides:** *"Fast, Declarative, Reproducible, and Composable Developer Environments using Nix"* ([README](https://github.com/cachix/devenv/)) — `env`, `packages`, `languages`, `services`, `processes`, `scripts`, `tasks`, `enterShell`, `enterTest`, `git-hooks`, `files`, `containers`, `integrations`, `secretspec`, `devcontainer` ([options](https://devenv.sh/reference/options/)). Two operational properties matter:

- **Hermeticity is the default; relaxing it is explicit.** `--impure` relaxes it, `--no-impure` forces it.
- **Purity is enforced at evaluation.** *"The result of a flake evaluation is fully determined by declared inputs—locked in `flake.lock`—not by ambient filesystem state, environment variables, or `NIX_PATH`"* ([Nix pure-eval](https://zemdregon.github.io/nix-docs/07-flakes/pure-eval-and-impure/)). `devenv.lock` is the analogue.

| ID | Requirement |
|---|---|
| TT-053 | every unit has `devenv.nix` **and a committed `devenv.lock`** |
| TT-054 | root devenv provides the shared baseline: `go 1.27.1`, `buf`, `templ`, `bun`, `goimports`, and tasks for `arch-graph`, `check`, `dev` |
| TT-055 | root `services` provide the local stack, so `devenv up` replaces `docker compose up` for the dev loop; compose remains the CI/integration artefact |
| TT-056 | per-service devenvs `imports: [ ../.. ]` so the toolchain version is declared once (flake-input deduplication, applied to devenv modules) |
| TT-057 | root `git-hooks`: `gofmt`, `templ`, `buf lint`, `go -C apps/web test ./internal/architecture/` |
| TT-058 | rewrite the two scaffolds — remove `env.GREET`, `scripts.hello`, the no-op `enterShell` |
| TT-059 | `devcontainer.enable = true` so `.devcontainer/devcontainer.json` is generated from the same metadata ([spec supporting tools](https://github.com/devcontainers/spec/blob/main/docs/specs/supporting-tools.md)) |

**The 12-factor tension, named.** `.env.example` is 4.8 KB — well past the point where env vars stop working: *"the chances of variable cross contamination is high… If you need [to know which variables] are actually missing, tracking them is a pain,"* and it only holds *"less than 10 variables"* ([HitchDev](https://hitchdev.com/strictyaml/why-not/environment-variables-as-config/)). Factor III is explicit that config lives in env vars *"each fully orthogonal to other env vars"* ([12factor](https://12factor.net/config)), though upstream converges on *"environment variables specifically feel like an implementation detail"* ([issue #4](https://github.com/twelve-factor/twelve-factor/issues/4)).

**TT-060 — composition, not replacement:** declare *structure* in version control, inject *secrets* from env — *"a YAML file marked up with environment variables"* ([Pilosus](https://blog.pilosus.org/posts/2019/06/07/application-configs-files-or-environment-variables-actually-both/)). Already devenv's own `env.*` + `secretspec` split.

**Reference point:** Nix's sandbox isolates builds *"from the normal file system hierarchy"* in *"private PID, mount, network, IPC and UTS namespaces"* ([nix.conf](https://nix.dev/manual/nix/latest/command-ref/conf-file.html)); Bazel's practical four-property checklist is no undeclared deps, no timestamps/UIDs in generated files, no network, no randomness ([FAQ](https://bazel.build/about/faq)). gogo's `import_graph.txt` already implements the same discipline at the architecture layer — **read a committed snapshot instead of shelling out** — which is why it is rung **D**.

---

## 11. Determinism — the definition used here

| # | Property | Enforced by |
|---|---|---|
| **D-1** | Total ownership: every unit → exactly one team | `team_catalog.yaml`, TT-034 |
| **D-2** | Declared == derived: no drift is representable | TT-035, staleness gate |
| **D-3** | Single provider per contract | TT-036 |
| **D-4** | Every edge declares exactly one interaction mode | TT-037 |
| **D-5** | No unmounted unit | TT-048 |
| **D-6** | Pure planning: no clock, no network, no map-iteration order | TT-045 |
| **D-7** | Reproducible environment: declared and locked | TT-053, TT-056 |
| **D-8** | **No duplicates**: every duplicated authority is deleted, not reconciled | §3, TT-047 |

**What determinism does not mean:** that the topology is static. It means *change to the topology cannot happen without a reviewable diff and a green build*.

---

## 12. Phased plan — one feature end-to-end first

> **Decisions deliberately left open** are tracked in
> [`OPEN_DECISIONS.md`](./OPEN_DECISIONS.md), not here. Three surfaced during
> implementation and none was resolved by inference: the enforcement mechanism
> (OD-1), whether the aggregator may hold a cache or read model (OD-2), and
> whether `type Mutation` is part of the permanent aggregator contract (OD-3).
> Where an undecided question touches code, the architecture test says which way
> it currently falls — so that behaviour is never mistaken for a ratification.

Correction 4. Rev 1's plan was breadth-first: fix all the docs, then build all the metadata. That is the plan that produces a catalog nobody trusts and a validator nobody runs.

**Phase 0 proves the whole chain on one feature, produces the template, and only then generalises.**

### 12.1 Phase 0 · Pilot — Seller stock adjustment (tracer bullet)

**Feature:** a seller adjusts stock for a product in the seller console. `POST` → `sales`-free path: `inventory.v1/AdjustStock` → stock row updated + append-only movement recorded → `product.changed.stock_adjusted` published → Storefront island invalidated → console redirected (PRG).

**Why this feature and not another.** It exercises the maximum number of boundaries per unit of work:

| Boundary | How the pilot touches it |
|---|---|
| **B1 ownership** | T2 must be declared for `modules/inventory`, `modules/sales`, `pages/seller` — one team, three units, one feature |
| **B2 deployment** | all in-process; `grpcx.InProcess` seam exercised without changing |
| **B3 contract** | `inventory.v1` in-process gRPC; `product.changed.*` cross-service via Kafka |
| **B4 data** | inventory is the **sole writer** of stock; `pages/seller` must not write it directly. Forces the authority question to be answered explicitly |
| **B5 composition vs domain** | `bootstrap` wires it; `modules/inventory` must not know `pages/seller` exists (import rule 2) |
| **§4 rung** | inventory is rung-2-mis-shaped; the pilot goes straight through `domain/` + `application/service.go`, resolving **F1** |
| **§5 render** | `KindMutation` + `Fragment` + `Redirect` — exercises TT-043/044 on a real path |
| **Event direction** | exercises TT-022 |

**Runner-up, if the render/event side is wanted more heavily:** the Storefront grid (`pages/home` → `productlisting.v1` + `productsearch.v1` → island cache + `product.changed.*` invalidation). It exercises `RenderMode`/`Freshness`/`ContractPlanner`/singleflight/`prefixGen` instead of the domain. *Swapping the pilot is cheap in Phase 0 and expensive afterwards — pick deliberately.*

**Deliverables — each one is the template for every later feature:**

1. **Deletions DEL-1 … DEL-3 first.** They are prerequisites: DEL-1 and DEL-3 close two B4 holes, and doing them before the pilot means the pilot's `team_catalog.yaml` never has to describe a duplicate authority.
2. `team_catalog.schema.json` with `additionalProperties: false` (TT-032).
3. `team_catalog.yaml` entries **for the pilot's units only** — `internal/modules/inventory`, `internal/modules/sales`, `internal/pages/seller`, `internal/bootstrap`, and the contracts they touch. Nothing else yet.
4. Validator implementing TT-034, TT-035, TT-036, TT-037, TT-048 + the staleness gate. **B rung.**
5. `Descriptor` extension (TT-028) for the pilot's modules.
6. **F1 resolved:** `internal/modules/inventory/domain/` + `application/service.go`; the oversell invariant defined in the domain, enforced by the conditional UPDATE; `ports.StockService` satisfied by the application service, not the repository.
7. **TT-041 enabled for the pilot's modules only** — deliberately scoped, so the first hexagon rule lands on the one module whose violation is already understood.
8. `make arch-team-catalog` + `make check` wiring.

**Exit condition:** the pilot feature works end to end; `make check` fails if any of its five units' ownership is changed wrongly; and **`team_catalog.yaml` + the validator are demonstrably reusable without modification for the next feature.** That last clause is the real test of Phase 0.

**What Phase 0 deliberately does not do:** touch the other 6 units, change the team topology, change the selected monorepo distribution, or write any CI.

### 12.2 Phase 1 · Generalise the pattern

Repeat Phase 0 for: Storefront grid (T3), Identity (T4), then the four services (T1) + aggregator. Add `project.json` per unit (TT-030). Enable TT-035 across the whole graph — the point at which a *wrong* declaration outside the pilot starts failing. Promote the golden path (TT-051): fix `cmd/modulegen`'s completion message and templates so a scaffolded module compiles into `bootstrap` with zero manual edits (**G12**).

### 12.3 Phase 2 · Close the correctness holes

| Item | Closes |
|---|---|
| Delete the three stale catalogue-era docs; rewrite the two Indonesian READMEs | G7 |
| Fix the `ARCHITECTURE.md` contract-family table | G6 |
| Remove the dead `extract-check` CI step; add TT-052 | G5, DEL-6 |
| Fix `product-listing-service`'s `+event.Topic+` usage-string artefact | G7 |
| Add `bazel test //...` to CI so the hermetic-snapshot benefit is actually used | rung C→D for the existing rules |

### 12.4 Phase 3 · Bind organisation and repository governance

The monorepo decision is recorded in §2.6 and implemented by the layout in `MONOREPO_STRUCTURE.md`. Bind real team identities to the declared ownership model. If an extraction trigger in §2.6 fires later, publish the affected consumer kits as versioned modules and give contract generation a separately governed artifact before splitting repositories.

**Organisation binding:** substitute real team ids into `team_catalog.yaml`; generate `CODEOWNERS` **from** it (never hand-maintained — that would be a second source of truth, §8.1) and CI-verify freshness (rung D). Name a contact channel and on-call rotation per team. Write or retire `D-010` (§0.4).

**Git and CI/CD policy** — currently A-rung and entirely unimplemented — is written here, once the boundaries are declared and the generator exists, rather than specified now against a shape that will have changed.

### 12.5 Risk register

| ID | Risk | L | Mitigation |
|---|---|---|---|
| R-01 | Catalog becomes a second source of truth; someone hand-edits it to silence a check | H | TT-035 compares declared to *derived*; a hand-edit that does not match reality fails. Shrink-only baselines with owners + expiry (TT-033) |
| R-02 | Five teams is too many for real headcount, so the topology is decorative | H | §5 states today's reality; §6.1 makes team count a *consequence*; TT-065 forbids headcount-driven boundaries |
| R-03 | Phase 0's template does not generalise and gets rewritten per feature | M | Phase 0's exit condition *is* reusability; the pilot is chosen to touch the most boundaries |
| R-04 | The aggregator accretes state and becomes a sixth catalogue authority | M | TT-046; the seven-criterion table (§4.5) sits in its own architecture test file as documentation |
| R-05 | `internal/shared/**` becomes a shared-services ticket queue | M | two-consumer minimum + extractability test; TT-064 kills unconsumed primitives |
| R-06 | Deleting `reporting` destroys a capability someone was about to build | L | DEL-3's precondition is a grep; and reporting is un-ownable as a stream context (§2.4) — it would have to be rebuilt inside T1 or T2 regardless |
| R-07 | `media.v1` has no breaker in the gateway, unlike the other four upstreams | M | add `upstreamMedia` to the seven `contributeBreaker` calls, or record why exempt. Currently unexplained |
| R-08 | A future service extraction happens before its contracts and release ownership are ready | M | Keep the monorepo as the default; require an extraction plan covering contract publication, module independence, CI, and team ownership |

### 12.6 Product capability track — admin-managed product types

This is an end-to-end product capability, separate from the team-catalog
governance phases above. It uses the existing product platform boundary and
does not require a repository split.

**User outcome:** an authorized admin can publish a product type and its
attribute definitions; the seller form discovers the published schema and
renders matching fields; Product Service validates and stores submissions;
Listing and Search receive product snapshots and project the values; storefront
SSR renders from those read models.

**Implementation order:**

1. Persist product types, attribute definitions, schema versions, publication
status, and audit metadata in Product Service's database. Seed the existing
`general` and `apparel` types as the migration starting state.
2. Add an admin-authorized draft/edit/validate/preview/publish workflow. Keep
published schema versions immutable and make publication atomic.
3. Replace the code-owned type registry with a schema repository. Keep
validation mechanics in Product Service code and validate writes against the
requested published version.
4. Expose the published schema through the existing product contract. Remove
the seller form's dependency on any code-owned registry and keep its controls
limited to supported field kinds.
5. Preserve the schema version and attribute values in product snapshots and
Listing/Search projections. Handle schema-publication events or provide an
explicit projection rebuild path where a schema change affects search facets.
6. Verify the full workflow with admin authorization, stale-version and invalid
value rejection, seller form discovery, read-model propagation, and SSR.

**Explicitly out of scope for this track:** creating one service per metadata
concept; arbitrary executable rules from admin input; using the editorial CMS
as the validation authority; and moving Listing/Search to synchronous calls on
the SSR request path.

**Separate content tools:** use a headless CMS for banners and editorial page
content with allowlisted content blocks; use Rspress for developer
documentation. The gateway remains the SSR composition owner and uses platform
UI primitives to render content. These tools have separate deployment and
content lifecycles from product schema management.

---

## 13. Acceptance criteria, by phase and rung

**Phase 0** — the pilot is complete and the pattern is proven:
1. DEL-1…DEL-3 deleted; no duplicated authority remains. *(D-8)*
2. `team_catalog.schema.json` validates with `additionalProperties: false`. *(A→B)*
3. The five pilot units each resolve to exactly one team; a deliberate mis-assignment fails `make check`. *(B1, D-1)*
4. Declared edges equal derived edges for the pilot; a fabricated edge fails. *(D-2)*
5. `inventory` has `domain/` + `application/service.go`; the oversell invariant is defined in the domain. *(F1 closed)*
6. TT-041 passes for the pilot's modules. *(A→B)*
7. `Descriptor.Team` matches the catalog for the pilot's manifests. *(A→B)*
8. `team_catalog.yaml` + validator are reusable for the next feature with no modification. **This is the real gate.**

**Phase 1** — generalised:
9. Every unit in the repository appears in the catalog with one team. *(D-1)*
10. TT-041 enforced for all modules. *(prose-only gap closed)*
11. Every contract in `contracts/proto/**` has one provider and ≥1 consumer or a rationale. *(D-3)*
12. Every module and page has `project.json` with `tier:*`, `team:*`, `rung:*`. *(G3)*
13. `cmd/modulegen` output compiles and wires into `bootstrap` with no manual edits. *(G12)*

**Phase 2** — correctness:
14. TT-046, TT-047, TT-048, TT-049 enforced.
15. Zero occurrences of deleted catalogue identifiers in any document. *(G7)*
16. Dead CI step removed; TT-052 wired. *(G5)*

**Phase 3** — decided and bound:
17. The monorepo layout matches `MONOREPO_STRUCTURE.md`; Go workspace entries, Compose contexts, CI, and service paths point to the same module locations. *(G11 resolved)*
18. `CODEOWNERS` generated from the catalog, CI-verified for freshness. *(rung D)*
19. Every team has a named contact channel and on-call rotation. *(G14)*
20. `D-010` written or retired; the decision log is verifiably complete. *(§0.4)*
21. Git and CI/CD policy written. *(§12.4)*

**Product capability track (§12.6)** — admin-managed schema is live end to end:
22. An authorized admin can draft, validate, preview, publish, and audit product
   type schema changes without a code change or Product Service deployment.
23. Published schema versions are immutable; product writes validate against
   their submitted published version and persist that version.
24. Seller forms discover published schema through the product contract; no
   product-type registry is hardcoded in seller or Product Service source.
25. Product snapshots reach Listing and Search, and the storefront renders
   projected values without a synchronous Product Service read.
26. Banner/editorial CMS content and Rspress documentation are documented and
   deployed separately from product-schema administration.

---

## 14. Evolution — sensing

| Signal | Reading | Action |
|---|---|---|
| Growing wait time across one boundary | Wrong boundary, or an X-as-a-Service being negotiated as a collaboration | Widen the contract rather than adding meetings |
| Recurring friction in one place | Either the boundary is wrong (→ collaboration, time-boxed) or the platform needs product work (→ T5 builds it) | Choose one. Not both |
| On-call pain concentrated in one team | That team owns a complicated-subsystem's load | Apply heuristic 4; split |
| T5 is asked for the same X-as-a-Service twice by one team | T5 is the wrong owner; it is a stream concern | Move it to the requesting team |
| A promotion trigger fires and nobody notices | The trigger list is decorative | `team_catalog.yaml.promotion_triggers` is checked quarterly, not on demand |
| `make check` pass rate drops below 95% on T1–T4 PRs | The paved road has become a mandate | Cut or fix a gate. A routinely bypassed gate is worse than none |
| A context outgrows one team's cognitive load | *"organizations should not allow a software subsystem to grow beyond the cognitive load of the team responsible"* | Split the **domain** first, then the team (heuristic 1) |

**Cadence:** quarterly, 90 minutes. Output is a diff to `team_catalog.yaml` or an explicit "no change" with reasons. If the only output is sentiment, D-1 has failed.

---

## 15. References

### Team Topologies
[Skelton & Pais, *Team Topologies*](https://teamtopologies.com/book) · [key concepts](https://teamtopologies.com/key-concepts) · [interaction modelling](https://teamtopologies.com/key-concepts-content/team-interaction-modeling-with-team-topologies) · [cognitive-load heuristics](https://itrevolution.com/articles/minimize-cognitive-load-of-teams/) · [TT text excerpt](http://arafatm.com/mba/team-topologies-organizing-business-and-technology-teams-for-fast-flow) · [Fowler on TT](https://martinfowler.com/bliki/TeamTopologies.html) · [Conway 1967](https://melconway.com/Home/Conways%5FLaw.html) ([PDF](https://www.melconway.com/Home/pdf/committees.pdf)) · [Tune, Architecture Ownership Patterns Pt 2](https://nick-tune.me/blog/2020-12-29-architecture-ownership-patterns-for-team-topologies-part-2-s/) — a *team-ownership* taxonomy, **not** a frontend macro-architecture argument. A series titled "vertical slices & macro-architecture" attributed to Tune could not be found in either of his archives (nick-tune.me/blog, 211 posts; nicktune.substack.com, 11 posts) and must not be cited. "Vertical slices" is Dudycz/Bogard's · [Dudycz, vertical slices and ownership](https://www.architecture-weekly.com/p/vertical-slices-their-ownership-and) · [golden path vs paved road](https://www.internal-developer-portal.com/developer-experience-self-service-platforms/) · [paved paths](https://platformengineering.org/blog/how-to-pave-golden-paths-that-actually-go-somewhere) · [onboarding as a platform metric](https://dxclouditive.com/en/blog/developer-onboarding-time-metric/) · in-repo: `.agents/skills/team-topologies/references/`

### SOLID, Clean Architecture, hexagonal
[Cockburn, Hexagonal Architecture (2005)](https://alistair.cockburn.us/hexagonal-architecture/) · [Martin, The Clean Architecture (2012)](https://blog.cleancoder.com/uncle-bob/2012/08/13/the-clean-architecture.html) · [book ch. 22](https://www.informit.com/articles/article.aspx?p=2832399) / [ch. 30](https://www.oreilly.com/library/view/clean-architecture-a/9780134494272/ch30.xhtml) · [Solid Relevance (2020)](https://blog.cleancoder.com/uncle-bob/2020/10/18/Solid-Relevance.html) · [cleancoder.com](https://www.cleancoder.com/) · [Khononov, Learning DDD](https://www.oreilly.com/library/view/learning-domain-driven-design/9781098100124) · [North, CUPID](https://dannorth.net/2021/03/16/cupid-the-back-story/) · [Miller, Putting SOLID into Perspective](https://jeremydmiller.com/2022/08/10/putting-solid-into-perspective) · [Marston, Not-so-SOLID](https://tonymarston.net/php-mysql/not-so-solid-oo-principles.html) · [Bogard, Vertical Slices](https://github.com/SSWConsulting/SSW.VerticalSliceArchitecture.Documentation/blob/main/learn/cookbook/history.md) · [Ozkaya, The Problem with Clean Architecture](https://medium.com/design-microservices-architecture-with-patterns/the-problem-with-clean-architecture-vertical-slices-111537c0ffcb) · [sairyss/domain-driven-hexagon](https://github.com/sairyss/domain-driven-hexagon) · [Go FAQ, accept interfaces return structs](https://www.gofaq.org/en/accept-interfaces-return-structs) · [consumer-side interfaces](https://www.nazarboyko.com/articles/go-interfaces-accept-interfaces-return-structs)

### Frontend & microfrontends
[Jackson, Micro Frontends](https://martinfowler.com/articles/micro-frontends.html) · [microfrontend.io patterns](https://microfrontend.io/patterns/) · [ThoughtWorks Radar](https://www.thoughtworks.com/radar/techniques/micro-frontends) · [single-spa: import maps over Module Federation](https://fr.single-spa.js/docs/recommended-setup) · [Feature-Sliced Design](https://feature-sliced.design/docs) ([layers](https://feature-sliced.design/docs/reference/layers), [slices & segments](https://feature-sliced.design/docs/reference/slices-segments)) · [Atomic Design](https://atomicdesign.bradfrost.com/) · [Islands Architecture](https://docs.astro.build/en/concepts/islands/) ([Miller](https://jasonformat.com/islands-architecture/), [Qwik's counter-argument](https://qwik.dev/docs/faq)) · [component-level visual regression](https://www.visual-regression-testing.dev/storybook-visual-regression-testing) · [Design Tokens 2025.10](https://www.designtokens.org/tr/2025.10/) ([announcement](https://www.w3.org/community/design-tokens/2025/10/28/design-tokens-specification-reaches-first-stable-version/), [Style Dictionary](https://github.com/style-dictionary/style-dictionary/blob/main/docs/src/content/docs/info/tokens.md), [Tailwind v4 `@theme`](https://tailwindcss.com/docs/theme))

### Render strategies & content negotiation
[RFC 9110 §12](https://www.rfc-editor.org/rfc/rfc9110) · [GoF Strategy](https://courses.cs.duke.edu/spring12/cps108/classwork/class10/Strategy_Pattern.pdf) · [htmx headers](https://four.htmx.org/reference/headers) / [requests & responses](https://four.htmx.org/docs/core-concepts/requests-and-responses.md) · [Hotwire Turbo Streams](https://turbo.hotwired.dev/handbook/streams) · [Rails `respond_to`](https://api.rubyonrails.org/v7.2/classes/ActionController/MimeResponds.html) · [a-h/templ `Fragment`](https://pkg.go.dev/github.com/a-h/templ@v0.3.1020) · render pipelines: [Unity](https://docs.unity.com/en-us/engine/7000.0/manual/render-pipelines/overview), [Godot](https://github.com/godotengine/godot-docs/blob/master/tutorials/rendering/renderers.rst), [Frostbite FrameGraph](https://gdcvault.com/play/1024612/FrameGraph-Extensible-Rendering-Architecture-in) · [CDK determinism](https://docs.aws.amazon.com/cdk/v2/guide/configure-synth.html)

### Metadata-driven & determinism
[Brown, Software Architecture for Developers](https://leanpub.com/software-architecture-for-developers/read) · [C4](https://c4model.com/) · [model-code gap](https://static.simonbrown.je/pth2024-c4.pdf) · [Smithy 2.0](https://smithy.io/2.0/spec/index.html) · [k8s CRDs](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/) · [operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator) · [JSON Forms](https://jsonforms.io/docs) · [ProseMirror schema](https://prosemirror.net/docs/guide/) · [Serverless Workflow schema](https://github.com/serverlessworkflow/specification/blob/main/schema/workflow.yaml) · [Airflow's counter-argument](https://airflow.apache.org/docs/stable/index.html) · [ArchUnit](https://www.archunit.org/) ([freezing §8.6](https://www.archunit.org/userguide/html/000_Index.html)) · [dependency-cruiser](https://github.com/sverweij/dependency-cruiser/blob/main/doc/cli.md) · [Deptrac](https://deptrac.github.io/deptrac/concepts) · [jQAssistant](https://jqassistant.github.io/jqassistant/current/) · [Nx module boundaries](https://nx.dev/docs/features/enforce-module-boundaries) · [OPA/Rego](https://www.openpolicyagent.org/docs/latest/policy-language) · [Backstage descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/) · [Reproducible Builds](https://reproducible-builds.org/) / [`SOURCE_DATE_EPOCH`](https://reproducible-builds.org/specs/source-date-epoch/) · [Terraform drift](https://developer.hashicorp.com/terraform/tutorials/state/resource-drift) · [metadata sprawl](https://lists.w3.org/Archives/Public/public-design-tokens/2025Oct/0003.html)

### Contracts & schema evolution
[Buf](https://buf.build/docs/) ([lint](https://buf.build/docs/lint/rules/), [breaking](https://buf.build/docs/breaking/rules/), [`buf.yaml` v2](https://buf.build/docs/configuration/v2/buf-yaml/)) · [protobuf language guide](https://protobuf.dev/programming-guides/proto3/) / [best practices](https://protobuf.dev/best-practices/dos-donts/) · [Confluent compatibility](https://docs.confluent.io/platform/current/schema-registry/fundamentals/schema-evolution.html) · [AIP-180](https://google.aip.dev/180) · [Pact](https://docs.pact.io/pact_broker/overview) · [Spring Cloud Contract](https://docs.spring.io/spring-cloud-contract/reference/project-features-contract.html) · [Fowler, Parallel Change](https://martinfowler.com/bliki/ParallelChange.html) · [MinimumCD expand-and-contract](https://beyond.minimumcd.org/docs/foundations/evolutionary-coding/expand-and-contract/) · [Radar: API expand-contract](https://www.thoughtworks.com/radar/techniques/api-expand-contract)

### Dev environments
[devenv](https://devenv.sh/) ([options](https://devenv.sh/reference/options/), [repo](https://github.com/cachix/devenv/), [processes](https://devenv.sh/processes/)) · [Nix pure-eval](https://zemdregon.github.io/nix-docs/07-flakes/pure-eval-and-impure/) · [builders & sandboxes](https://zemdregon.github.io/nix-docs/04-store-and-build/builders-and-sandboxes/) · [`nix.conf`](https://nix.dev/manual/nix/latest/command-ref/conf-file.html) · [Devbox](https://www.jetify.com/docs/devbox/configuration) · [Dev Containers spec](https://containers.dev/implementors/spec/) ([supporting tools](https://github.com/devcontainers/spec/blob/main/docs/specs/supporting-tools.md)) · [nix-direnv](https://github.com/nix-community/nix-direnv/) · [Bazel hermeticity](https://bazel.build/basics/hermeticity) / [FAQ](https://bazel.build/about/faq) / [sandboxing](https://bazel.build/docs/sandboxing) · [Tilt](https://docs.tilt.dev/tutorial/2-tilt-up.html) · [mise](https://mise.jdx.dev/) · [12-factor config](https://12factor.net/config) ([tension](https://github.com/twelve-factor/twelve-factor/issues/4), [counter-case](https://hitchdev.com/strictyaml/why-not/environment-variables-as-config/), [compose both](https://blog.pilosus.org/posts/2019/06/07/application-configs-files-or-environment-variables-actually-both/))

### Repository-local
`ARCHITECTURE.md` (four zones in §1, seven rules in §2, `D-0xx` log — **`D-010` absent**) · `apps/web/internal/architecture/architecture_test.go` · `apps/web/internal/architecture/import_graph.txt` · `apps/web/internal/shared/module/{module,lifecycle}.go` · `apps/web/internal/shared/web/render/{strategy,planner,composer,htmx,render}.go` · `apps/web/internal/shared/web/ui/doc.go` · `apps/web/internal/shared/{grpcconsumer,productconsumer,authconsumer,productevents}/` · `apps/web/internal/bootstrap/*.go` · `apps/web/internal/modules/*/{module.go,compose/}` · `services/aggregator-service/{README.md,cmd/server/main.go,graph/}` · `contracts/` · `Makefile` · module `project.json` descriptors · `go.work` · `deploy/k8s/networkpolicy.yaml`

---

## Appendix A · Glossary

**Aggregator service** — a composition layer (Z-C) running as a separate process. Owns a published GraphQL schema; no domain, no persistence (§4.5). **Tripwire TT-046.**
**Application composition layer (Z-C)** — code deciding what runs, in what order, and how a response is shaped. Owns zero business rules (§2.5).
**Bounded context** — a semantic boundary with a ubiquitous language and sole authority over its aggregates.
**B1…B6** — the six boundaries: ownership, deployment, contract, data/aggregate, composition-vs-domain, distribution (§2).
**Breaking-change category** — Buf's layered strictness: `FILE` > `PACKAGE` > `WIRE_JSON` > `WIRE`.
**Declared == derived** — D-2: catalog edges must equal edges extracted from the real import graph.
**Deployment boundary (B2)** — which OS process. *Distinct from* distribution boundary (B6).
**Distribution boundary (B6)** — which repository. **One monorepo, with separate Go modules** (§2.6).
**Escalation ladder** — rungs 0–4, modular monolith first, service on a named trigger (§4.2).
**Hybrid doctrine** — start in-process; a service is what a module becomes when a trigger fires (§4).
**Interaction mode** — collaboration / X-as-a-Service / facilitating; exactly one per edge.
**Island** — a page region under its own `RenderMode` and `Freshness`, cached under a page-owned key.
**Maturity rung** — A declared · B locally enforced · C merge-enforced · D drift-proof (§1).
**Platform package** — rung 0: ≥2 consumers, no business vocabulary, extractable.
**Promotion trigger** — `T-scaling`, `T-cadence`, `T-data`, `T-runtime`, `T-blast-radius`, `T-contract-consumer` (§4.3).
**Rung 1 module** — manifest + routes + ports, no domain rules.
**Rung 2 domain module** — full hexagon; owns data.
**Rung 3 service** — own process, own database.
**Sole authority** — exactly one writer of a row. A second writer is a defect (§2.4).
**UI business domain (Z-U)** — a page owning a read-side bounded context, its consumer-side ports, result shapes and island cache namespace.
