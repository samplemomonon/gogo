# Implementation Log — P0 to P4 executed

**Date:** 2026-10-15
**Companion to:** [`IMPLEMENTATION_PLAN.md`](./IMPLEMENTATION_PLAN.md) · [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md) · [`ARCHITECTURE.md`](../ARCHITECTURE.md)

This records what was actually done, and — more usefully — what the plan got
wrong. Two of its claims did not survive contact with the code, and both are
written up below rather than quietly dropped.

**Note (2026-10-19):** this log is preserved as written. Paths in it reflect the
layout at the time (`internal/modules`, `internal/pages`, `internal/bootstrap`);
the [D-023](../ARCHITECTURE.md#d-023) re-home has since moved every unit under
`internal/features/**` and renamed `internal/bootstrap` to `internal/shell`.
For the current map see [`ARCHITECTURE.md`](../ARCHITECTURE.md) §1 and
[`DECISION_TEAM_OWNED_VERTICALS.md`](./DECISION_TEAM_OWNED_VERTICALS.md).

---

## 1. Result

| Phase | Planned | Done | Outcome |
|---|---|---|---|
| **P0** resource hygiene | ~30 min | ✅ | 675 MB of tooling state excluded from git and from Docker build context |
| **P0b** repair the drift gate | *not in the plan* | ✅ | the staleness gate was failing and had been masking future drift |
| **P1** deletions | ~2 h | ✅ | 1,347 lines and two duplicate authorities removed |
| **P5** aggregator tripwire | *not in the plan* | ✅ with one retraction | a load-bearing property of a second module is now enforced; the first attempt was reverted (§6a) |
| **P6** render enforcement | *planned as TT-043/044* | ✅, one rule replaced | the render decision is enforced rather than declared; `TT-043` as written was not implementable and was replaced (§6b) |
| **P2** golden path | ~3 h | ✅ | scaffold → wire → build → test verified with zero manual edits |
| **P3** declarative composition | ~1 day | ✅ but **redesigned** — see §3 | unit table + a gate that catches unwired units |
| **P4** rule table | ~1 day | ✅ partially — see §4 | 4 new enforced rules; the prose-only gap is closed |

`go build ./...`, `go vet ./...` and `go test ./...` all pass. The architecture
package runs 17 top-level tests, 0 failures. `buf` is not installed in this
environment, so `make check` stops at the buf step — that is the Makefile's
deliberate "a missing tool is not a passing check" behaviour, not a regression.

---

## 2. P0 — resource hygiene

`.devenv/state` was **675 MB** of Go module cache sitting inside the repository
tree across two service directories, and nothing excluded it.

| Change | Effect |
|---|---|
| `.gitignore`: added `.devenv/`, `.nix-devenv` | no longer enters version control |
| `.dockerignore`: added `.devenv/`, `.nix-devenv` | **the Dockerfile runs `COPY . .` in two stages**, so this was shipping 675 MB to the Docker daemon as build context, twice, on every build |
| `Makefile`: `gofmt` now runs over `go list`'s package directories instead of `./cmd ./internal` | the check ran in **0.3 s** instead of walking the module cache |
| `architecture_test.go`: added `.devenv`, `.nix-devenv`, `vendor` to the directory skip-list | see §2.1 |

### 2.1 The skip-list was a correctness bug, not a tidiness issue

`.devenv` was absent from the architecture test's walk skip-list. That meant the
guard was walking vendored dependency source looking for `BUILD.bazel` files —
and `parseBuildFile` would have merged any third-party rules it found into the
graph that every boundary rule is decided on.

No dependency in the cache actually ships a `BUILD.bazel` today (verified: zero
matches), so nothing was corrupted. But the failure mode was live: one dependency
gaining a BUILD file would have silently altered the import graph.

**The `.devenv` directories were deliberately not deleted.** They are devenv
state, so `devenv shell` regenerates them — but they also contain
`state/postgres`, a live Postgres data directory, and `state/go`, a module cache
that took real time to populate. Removing 675 MB of someone's working state is
not a call to make silently. They are excluded from git and from Docker; delete
them yourself when convenient.

---

## 3. P0b — the plan was wrong about the drift gate

**The plan assumed `make check` was green and that P0 was a tidiness fix. It was
not.**

`TestImportGraphIsCurrent` was **failing**, and the reason was structural:

- The graph was derived by parsing `deps` from Bazel `BUILD.bazel` files.
- Bazel is not installed. There is no `MODULE.bazel` and no `WORKSPACE`.
- Only **44 BUILD files survived**, describing **43 packages**.
- The committed `import_graph.txt` held **119 packages**.
- `internal/bootstrap`, all of `internal/shared/**`, `internal/modules/{billing,inventory,ops,sales}` and `internal/pages/auth` had **no BUILD file at all** — precisely the packages the rules care most about.

So the regeneration half could never reproduce the committed file, which meant
the gate could never pass, which meant the snapshot had been free to drift with
nothing to notice. The eight boundary rules still passed — they read the
committed 119-package snapshot — but nothing guaranteed that snapshot matched
reality.

**Fix.** The derivation now reads `go list -json`, from the root module and from
`contracts` separately (because `./...` stops at a nested module boundary).
`go list` is what Gazelle itself consumed, so this is the upstream source rather
than a substitute for it. The ~200 lines of hand-written Starlark parser are
deleted — a parser that silently stopped understanding its format is exactly
what had rotted.

**Validation.** The new derivation reproduced the committed snapshot's edges
**exactly** — `diff` over all 119 packages, zero differences. So the old
snapshot was accurate; only its ability to detect future inaccuracy was broken.
That is the strongest available evidence the swap is correct.

The graph now sits at **103 packages** after the P1 deletions.

### 3.1 A floor fired, correctly

`TestParserIsHonest` failed with *"only 7 packages under `gogo/contracts/,
expected at least 8"* when the reporting contract was deleted. That is the guard
working: it is a floor on the shape of the derivation, not a headcount, and a
deliberate deletion should lower it. Both affected floors were lowered with a
note saying what went, rather than padded with slack.

---

## 4. P3 — redesigned, because the plan over-engineered it

**The plan specified** a YAML file, a code generator, and a byte-comparison
staleness gate, so that adding a unit would be "a YAML edit plus regeneration".

**What was built** is a typed Go table in `internal/bootstrap/units.go` with
invariant tests.

The change of approach happened for three concrete reasons, found while building
it:

1. **The generator needed a YAML parser, and the module cache could not supply
   one offline.** Adding `gopkg.in/yaml.v3` touched `go.sum` and had to be
   reverted.
2. **Three artefacts where one suffices.** A 6-entry unit list and a 7-entry
   upstream list did not need a schema, a renderer and a drift gate.
3. **The hand-rolled parser is the thing that rotted last time.** Adding a
   bespoke parser to guard a bespoke renderer repeats the failure mode that
   P0b just spent effort removing.

The Go table is one source of truth, reviewable as a diff, with no new
dependency and no generator. Adding a unit is a three-line edit in one place
instead of a literal in the middle of a 400-line function.

### 4.1 What the change actually bought

The plan's stated win was "adding a unit is no longer a Go edit in the
composition root". That is half-achieved: it is still a Go edit, but it is a
table edit, in one place, and `internal/bootstrap/di.go` no longer contains the
list.

The bigger win was unplanned. `TestEveryRegisteringUnitIsDeclared` walks the tree
with `go/parser`, finds every package offering `Register(*fdi.Builder)`, and
fails if any is absent from the table. **Verified** by scaffolding a throwaway
`shadow` module:

```
internal/modules/shadow offers Register(*di.Builder) but is not in registeredUnits;
it would never be wired
```

That failure mode — a module that compiles, passes every import rule, and simply
never runs — had no guard at all before. It is the "declared == actual" property
of the PRD's determinism definition, and it is now enforced in both directions
(the table cannot name a path that does not exist; the tree cannot contain an
undeclared unit).

---

## 5. P4 — four new rules; two of the six did not apply

The plan listed six new rules. Four were implemented; two were withdrawn with
reasons.

| Rule | Status |
|---|---|
| Per-module hexagon dependency direction | ✅ `TestHexagonDependencyDirection` |
| Composition layer reaches no module internals | ✅ `TestCompositionLayerReachesNoModuleInternals` |
| No platform aggregate owned in-process | ✅ `TestNoPlatformAggregateIsOwnedInProcess` — extended from product to media, category and search, closing the hole that let `internal/modules/media` sit in the tree |
| Port names carry no technology | ✅ `TestPortNamesCarryNoTechnology` |
| The aggregator owns no persistence | ❌ **not applicable here** — `aggregator-service` is a separate Go module with its own `internal/architecture`. Its package is not in this graph. The tripwire belongs in that service's own architecture test |
| Every module reachable from bootstrap | ⚠️ **superseded** — `TestEveryRegisteringUnitIsDeclared` covers the wiring half. Full reachability would additionally have to cover modules wired through `compose.Build`, which the register-walk does not see |

### 5.1 The hexagon rules were correct but unverified

`ARCHITECTURE.md` states them in prose and admits nothing enforces them. Measured
before writing the rule, the real layer shape is:

```
domain               -> (nothing)
application          -> domain, application/ports
application/ports    -> domain
infrastructure/**    -> domain, application/ports
presentation/**      -> domain, application/ports
compose              -> everything
```

All clean. So the architecture was right and merely unchecked — which is worth
recording, because it means the rule table documents an existing truth rather
than imposing a new one. It is written as data (`layerRules`) because it is a
matrix, which is how dependency-cruiser, Deptrac and Nx all express the same
thing.

The two rows doing the real work forbid `infrastructure` and `presentation` from
reaching `application` — the use-case layer. An adapter satisfies a port; it does
not call a use case.

### 5.2 One genuine violation found, recorded as a shrink-only exception

The composition-layer rule found that `internal/bootstrap` imports
`gogo/internal/modules/sales/infrastructure/inventoryclient`
(`sales_connection.go:30`), where `auth/compose` and `inventory/compose` build
their own adapters.

**It was not fixed**, and the reason is worth stating: fixing it means moving
`inventoryclient.New` into `sales/compose`, which changes that module's public
API and its integration tests. That is a design change to a module nobody asked
to change, taken on the side.

Instead it is in `compositionExceptions` — a map with the reason, the retirement
condition and an owner (T2, Merchant Console). The rule stays strict; the one
edge it is wrong about is named, so the next person can tell which edges were
checked. A rule with a quiet exception is worse than no rule.

### 5.3 P2 verified end to end, including the revert

`make module name=probe` → wire the three lines the tool printed → `go build`
→ `make check` → `make arch-graph` → revert. All green with no manual fixups. The
staleness gate fired on its own when the new package appeared, which is the gate
working as intended.

---

## 6. Findings still open

| # | Finding | Where | Severity |
|---|---|---|---|
| 1 | ~~`inventory` has no application service and duplicates the oversell policy~~ | `internal/modules/inventory` | **CLOSED by Phase 0 pilot work** — see §6.1 |
| 2 | ~~`bootstrap` reaches `sales/infrastructure`~~ | `sales_connection.go:30` | **CLOSED.** `inventoryclient.New` moved into `sales/compose` as `compose.NewInventoryAdjuster`; `compositionExceptions` is now empty — see §31 |
| 3 | ~~`aggregator-service` owns no persistence, and nothing checks it~~ | — | **CLOSED.** Tripwire written at `services/aggregator-service/internal/architecture/`. See §8 |
| 4 | ~~`media.v1` has no breaker in the gateway~~ | — | **NOT A FINDING — verified and withdrawn.** `productconsumer.Connections` has exactly four upstreams: Write, Browse, Search, Category. The gateway never calls `media.v1`; `media-service` is reached by `aggregator-service`, a different binary. A breaker would guard a dependency that does not exist. The `units.yaml` comment already had this right; the log entry was wrong |
| 5 | ~~`product-listing-service`'s usage string contains a `+event.Topic+` template artefact~~ | — | **CLOSED.** The `event` import existed only to support the dead placeholder — with the placeholder removed the import was unused and the package stopped compiling. `config.DefaultKafkaTopic` / `DefaultKafkaDeadLetterTopic` are now named constants, and `usage` uses `Fprintf` so the placeholders actually expand |
| 6 | ~~B6 (monorepo vs polyrepo) undecided; services were nested under the gateway's `internal/`~~ | PRD §2.6 | **CLOSED.** Selected a monorepo with separate Go modules; gateway moved to `apps/web`, services to `services/`. See [`MONOREPO_STRUCTURE.md`](./MONOREPO_STRUCTURE.md) |
| 7 | `devenv.lock` exists for 2 of 6 units; the two `devenv.nix` files are still `devenv init` scaffolds | `services/*/devenv.nix` | small |
| 8 | 675 MB of `.devenv` state still on disk (git- and docker-ignored) | two service directories | cosmetic — yours to delete |

### 6.1 Phase 0 pilot progress — inventory application boundary

The inventory path now has an application service between gRPC and persistence.
`application.Service` implements the delivery-facing `StockService`, validates
adjustment scope through the domain, and delegates the atomic write to a
`StockRepository`. The in-memory adapter uses `domain.AdjustedQuantity` for the
oversell rule; Postgres retains its conditional `UPDATE ... quantity + delta >= 0`
as the concurrency-safe enforcement mechanism. The module constructor always
wraps its injected repository with the application service.

This closes finding 1's missing application layer and defines the invariant in
domain code while preserving the database's atomic guard. The seller flow's
existing gRPC connection and event path remain the vertical slice to verify as
the broader PRD pilot is completed.

---

## 6a. Closing finding 3 — the aggregator tripwire, and a retracted list

`aggregator-service` had **no** `internal/architecture` package at all, so the property its own README and `main.go` both assert — *"It owns no data and no database"* — was enforced by nothing. It sits in front of five catalogue services with ten write mutations, so that property is load-bearing: acquire a datastore and it becomes a sixth place where the catalogue has to be kept, and nothing about that failure is loud.

### The first attempt was wrong and was reverted

The initial version denied nineteen module roots — `pgx`, `lib/pq`, `go-sql-driver/mysql`, `gorm`, `ent`, `badger`, `buntdb`, `goleveldb`, `bbolt`, `etcd`, `go-redis`, `goose`, `golang-migrate`, `sqlite` and others — written from memory rather than from evidence about this repository.

An audit against published composition layers found that **none of those items could be corroborated as present in any real composition layer, and no Go-language composition layer was found at all.** The one capability with genuine support behind it, Redis, appears in Tokopedia's GraphQL gateway as *legitimate caching* of upstream data. The list was deleted rather than repaired.

Two more assumptions were retracted in the same pass:

| Retracted | Why |
|---|---|
| "no migration directory" | A migration tool is a function call; it does not need a directory. The check is bypassable and only proves nobody made a folder with a conventional name |
| Checking three guessed paths for a domain package (`internal/domain`, `domain`, `graph/domain`) | A package named `model/` or `types/` would have passed silently. Replaced with a name match against the real import graph, which cannot be placed wrongly |

### What ships instead

One forbidden import: `database/sql`, exact match. It is the only choke point for SQL in Go — every pure-Go path funnels through `sql.Open`/`sql.DB`, present and future — and it is **standard library**, so no denylist of third-party modules can match it. That was the gap in the original list.

Exact rather than prefix, because `database/sql/driver` is interface-only and `github.com/google/uuid` imports it solely to assert that UUID implements `driver.Valuer`. OpenTelemetry pulls uuid in, so the service reaches `database/sql/driver` today; a prefix rule fired on a service with no database. **That was the third failure of this guard during development, and it is why the rule now carries the reasoning in its comment** — a boundary rule that cries wolf does not get fixed, it gets deleted, and the rule it protected goes with it.

### Testing the guard, not just running it

Every rule was verified in both directions before being accepted:

| Case | Expected | Result |
|---|---|---|
| a package importing `database/sql` | fail | fail, with the import chain named |
| a package importing `github.com/google/uuid` | pass | pass |
| a `domain/` package | fail | fail |
| clean tree | pass | pass |

Two bugs were found only by doing this. The walk was skipping the standard library, which made it blind to the single most important import. And `go list -deps` emits a **test variant** of each dependency under the same `ImportPath` with the test files' imports included, so a naive map let the test variant overwrite the real one — and OpenTelemetry's own test dependencies reach `database/sql`. The walk now keeps the non-test variant deliberately, and says so.

### Two things are deliberately left open

`TestAggregatorOwnsItsSchemaContract` asserts the `Mutation` root exists, and carries a `TODO(architecture-decision)` marking it as a temporary guard rather than an architecture property. The test also **permits** caches and read models, because it forbids only `database/sql` — and that permissiveness is the accident of a narrow rule, not a ratified position.

Both are recorded as **OD-2** and **OD-3** in [`OPEN_DECISIONS.md`](./OPEN_DECISIONS.md), with owners and closure options. Neither is decided here.

---

## 6b. Closing the render decision's enforcement gap

`DECISION_UI_OWNERSHIP.md` §5.2 records the render decision as settled, and the PRD lists `TT-043` ("`render.Respond` is the only response path") and `TT-044` ("render strategies are complete") at rung **A** — declared, not enforced. That is the same prose-only gap P4 closed for the hexagon rules, so it is closed here too.

**Verified first, because the premise turned out to be narrower than the plan assumed.**

| Check | Result |
|---|---|
| Does any handler write a response outside `render`? | Yes — `ops` writes probe status codes, `auth` and `seller` call `http.Redirect`. A blanket "only `render.Respond`" rule would be wrong |
| Is `hx-boost` used anywhere? | **No.** The `HX-Request-Type` fix is therefore **latent, not a live bug** — recorded as such rather than as a rescue |
| Did the `Vary` fix leave a hole? | No live one. `Respond` sets `Vary`; the other call sites (`profile`, `seller`, `auth`) serve single-representation URLs, where `Vary` is not required. `/billing/fragment` is only ever fetched by `hx-get` |

So `TT-043` as written was not implementable without inventing a rule. Two checks were written instead, each at the level where the property is actually knowable:

**1. `TestRenderStrategiesAreComplete` (static, AST).** `render.Respond` fails closed on an unknown `Kind` but cannot fail on a **missing field** — a `KindNavigation` with no `Fragment` renders a full document for an htmx request, with status 200. The symptom is a page whose swapped region replaced itself with an entire page, which is not an error anyone would trace back here. Verified in both directions: removing `Fragment` and switching to `KindMutation` without `Redirect` each produce a named failure. Currently 1 strategy, complete.

**2. `TestNegotiatedResponsesDeclareVary` (integration, real HTTP).** Which URLs negotiate is a property of the running router, so it is asked of the running gateway rather than inferred by a static pass. It asserts both that `/billing` declares `Vary: HX-Request` and `Vary: HX-Request-Type`, **and** that the route really does answer two distinct shapes — a document for a navigation and a fragment for an htmx request. That second half matters: without it, `Vary` could be declaring a distinction that does not exist, which would make the header noise rather than a contract.

**A finding corrected in the same pass.** The oversell-invariant finding claimed an in-memory adapter "implements a *different* rule". That was asserted without reading the adapter and is **wrong** — `internal/modules/inventory/infrastructure/repository.go:101-103` refuses the adjustment the same way the Postgres adapter does. The real finding is **duplication, not divergence**: one invariant written twice, in two persistence adapters, instead of defined once in `domain/`. That is a maintenance risk rather than a present defect, and both `IMPLEMENTATION_PLAN.md` and the PRD now say so. The shape of the fix is a design question — how a domain expresses a predicate that must also run atomically inside SQL — so it is not taken here.

---

## 7. What this log corrects in the plan

| Plan said | Reality |
|---|---|
| "`make check` runtime returns to baseline" | it was not returning to baseline — `TestImportGraphIsCurrent` was already failing |
| "P0 is ~30 minutes, zero risk" | P0 was hiding a broken gate that needed a structural fix |
| "the aggregator tripwire is a rule we can add" | correct in principle; the first implementation denied a 19-item library list written from memory, of which none could be corroborated. Also: the aggregator had no `internal/architecture` package at all, so there was nothing to add a rule *to* |
| "the aggregator owns no persistence — nothing checks it" | still true, and now checked. But a second recorded finding — a missing `media.v1` breaker — turned out not to be a finding: the gateway never calls `media.v1` |
| "P3: `modules.yaml` + generator + staleness gate" | three artefacts was too many; a typed Go table plus a real drift gate is both simpler and stronger |
| "P4: rewrite the eight existing subtests as data" | not done. They pass, they are individually named, and converting bespoke logic to a table risks regression for no gain. The new rules are data because they were a matrix |
| "the aggregator tripwire is a rule we can add" | it lives in a different Go module and cannot be reached from this graph |
| "W-2: `.dockerignore` omits `.devenv`" | understated. Two `COPY . .` stages made it a per-build 675 MB transfer |

---

## 8. The gate covered 97 of 172 packages

Recorded here rather than in a commit message because both defects were
invisible to the tool that was supposed to detect them.

`make check` was the required CI status. It ran `go vet ./...` and `go test
./...`, which resolve **inside the root module only**. The four platform services
are separate Go modules, so `make check` never built and never tested them. The
aggregator's composition-layer tripwire — the rule keeping it a composition layer
rather than a sixth catalogue authority — had never executed.

`make check`'s own comment claimed to be *"everything CI cares about … the full
test suite"*, and the README described `make test` as running "all tests". Both
were wrong, and both were load-bearing: a check that describes itself as complete
stops anyone from looking for the part it is missing.

| | packages covered |
|---|---|
| before | 97 (root module) |
| after | **172** (6 modules) |

aggregator 20, product-listing 20, product-search 16, product 19.

`go test -race` also re-enabled on the services, and `gofmt-check` was widened to
them. Its first run found 15 files in `product-service` that `gofmt` had never
been asked about — 14 whitespace-only, one trailing blank line. They were never
checked because nothing had ever checked them.

### `GOWORK=off` is load-bearing, not hygiene

A directory not listed in `go.work` but living inside the workspace tree does not
resolve to itself. `go list ./...` inside `services/aggregator-service`
silently enumerates the **gateway's** packages. Measured: aggregator, product and
product-search all returned an *identical* list under workspace mode.

That is the mechanism by which three services could look covered by a check that
never touched them — a false pass is worse than a crash, because nothing looks
wrong. `make services-vet` and `make services-test` therefore set `GOWORK=off`
and build each service on its own `go.mod`, the way it is deployed. The build
graph is shared; the dependency graph is deliberately not. A service that could
only build inside this workspace could not be released on its own.

### `go work use -r .` is destructive in this tree

The obvious fix for an incomplete `go.work`, and the one independent research
recommended. The tree contains two devenv environments under
`services/*/.devenv` whose `state/go/pkg/mod` is a Go module cache. A
recursive walk adopts every cached module into the workspace and the build fails
with `module google.golang.org/protobuf appears multiple times in workspace`.

`go.work` now lists all eight modules by hand and carries a comment saying so. The
same hazard that motivated ignoring `.devenv` in Git and Docker has a third
appearance here; a directory can be invisible to Git and still be discovered by
any tool that walks the tree.

### `buf breaking` could never have passed on a pull request

Found by the new coverage, not by inspection. `--against '.git#branch=main'`
needs `refs/heads/main`. A `pull_request` checkout is a detached merge commit:
`refs/heads/main` does not exist there, only `refs/remotes/origin/main`.

```
buf breaking --against '.git#branch=main'
Failure: could not clone file://…/.git: exit status 128
fatal: couldn't find remote ref main
```

It never surfaced because the only green run was a **push** to main, where
`refs/heads/main` does exist — the check passed on push and was structurally
incapable of passing on a PR. Two related facts: the gate is the same check that
was broken, so fixes needed `enforce_admins: false` to land and then the flag
back; and `container image` had never once run, because its `if:` condition had
only ever been reached after a step that always failed.

Fixing it needed the actual buf binary, not the docs. Every candidate syntax was
run against a real PR-shaped checkout (detached HEAD, local `main` deleted):

| `--against` | result |
|---|---|
| `.git#branch=main` | FAIL — the original bug |
| `.git#branch=origin/main` | pass |
| `.git#ref=refs/remotes/origin/main` | pass |
| `.git#ref=<sha>` | pass |
| `<sha>` bare | FAIL — read as a module path: *"Module … had no .proto files"* |

The base is resolved to a SHA and passed as `.git#ref=<sha>`, which behaves
identically on push and PR and pins the comparison to one commit. `make
buf-breaking` was then run in that same PR-shaped checkout both ways: clean tree
exits 0, and deleting `string description = 3;` without reserving it exits
non-zero naming the field and the number.

### 39 `BUILD.bazel` files

No `WORKSPACE`, no `MODULE.bazel`, no bazel in the Makefile or CI, and the
`go list` import-graph derivation replaced the only reader when its coverage had
rotted to 43 of 119 packages. Deleted.

---

## 9. Corrections to this log

| Claim | Outcome |
|---|---|
| "CI is green on main" | green on `push` only. On `pull_request` it failed at `buf breaking`, so no PR could ever have passed |
| "`make check` is everything CI cares about" | 97 of 172 packages |
| "39 orphan `BUILD.bazel` files" | 44 existed; the count was taken after 5 had already been deleted in this work |
| "the aggregator tripwire is a rule we can add" | it now runs, and passes. It had never executed before |


---

## 10. Closing OD-1, OD-2 and OD-3

The three open decisions in `docs/OPEN_DECISIONS.md` are closed. Reasoning and
evidence: `docs/ADR_AGGREGATOR_BOUNDARIES.md`. What follows is what happened while
closing them, including three bugs of my own that the tests caught.

### OD-1 — the ratchet, and why it is not an allowlist

`internal/architecture/ratchet_test.go` mirrors the module's 39 first-party import
edges and fails on additions **and** stale entries.

The argument against `depguard` strict is this repository's own record rather than
a general position: three hand-maintained lists here have decayed — nineteen
module roots of which *none* existed, forty-four BUILD files for an uninstalled
build system, and an import graph that had rotted to a third of the repository
while its own staleness gate was failing. A `depguard` allowlist is not derived
from anything; someone edits YAML when a dependency is added.

What the ratchet buys, measured rather than asserted. Adding
`internal/retry → internal/graph/model`:

```
TestAggregatorOwnsNoDatastore     PASS
TestAggregatorHoldsNoDomainModel  PASS
TestImportRatchetIsCurrent       FAIL
```

The existing rules are blind to a first-party edge — which is precisely the gap
OD-1 said existed. The mirror also surfaced, on its first run, two inward edges
nothing had been tracking: `internal/telemetry → graph` and
`internal/grpcclient → graph`.

`database/sql` stays as the single forbidden import. The risk OD-1 was raised
about — a new driver arriving unchallenged — is already closed by construction,
because every pure-Go SQL path passes through `sql.Open`.

### OD-2 — derived state allowed, and the fact that settles it

Derived state is allowed; authoritative state is not; upstream-derived data may
be held for one request and not beyond.

The settling evidence is Apollo's GraphOS Router, the closest published analogue
and the reference implementation of a *correctly* caching composition layer. It
caches upstream subgraph responses in Redis — so caching at this layer is
mainstream, not exotic. Its TTL is not invented by the router: it reads
`Cache-Control: max-age` from the origin, derived from the origin's own
`@cacheControl`, and requires an authenticated invalidation endpoint from the
owning service for writes.

**Freshness is declared by the party that owns the data.** Measured here: zero
occurrences of `Cache-Control`, `max-age` or cache-tag across all five upstream
services. The precondition is unmet, so the constraint is active rather than
theoretical.

`internal/architecture/caches_test.go` enforces two things: every cache
constructor in the module is a declared one, and no declared cache holds
upstream-derived data past the request. It deliberately does *not* try to
distinguish a cache from a read model by inspection.

### OD-3 — the Mutation root is permanent

Six of the ten mutations work, so the root is not in question. The three category
mutations cannot succeed and now carry `@deprecated` with a reason naming the
category service's REST API — discoverable through ordinary introspection.

Removing them was rejected: graphql.org's governance guidance classes field
removal as a breaking change, which would mean introducing a breaking change to
fix a problem caused by the field's presence.

### Three bugs found by testing in both directions

Recording these because each was a rule that **passed while being wrong**, which is
the exact failure this repository has been closing all along.

1. `recorded := make(map[string]bool)` was declared and never populated from
   `committed`. Every edge reported as new; the test failed on a clean tree. It
   could only have been found by running it.
2. `mutationFieldDeclaration` cut at the first `"\n  "`, which matches the first
   two spaces of a four-space continuation line — so every `@deprecated` directive
   was truncated away before being checked, and all three OD-3 assertions passed
   vacuously.
3. The AST walk skipped the module root, because `filepath.Rel(root, root)` is
   `"."` and `strings.HasPrefix(".", ".")` is true. The cache test found zero
   constructors and tripped its own guard-on-the-guard — which is why that guard
   exists.

A fourth assertion was also wrong before it was fixed: the deprecation check
looked for `reason: "` to be present, which is also true of `@deprecated(reason:
"")` — the empty reason it existed to catch.

---

## 11. Authentication at the composition layer

**The gap.** Measured before changing anything: zero occurrences of `Authorization`,
`Bearer`, `JWT` or `OAuth` in any production `.go` file in the repository. The
service holding ten write mutations — including `registerMedia`, which returns a
presigned URL letting the caller PUT arbitrary bytes into object storage — was
publishing port `8085:8085` with no credential of any kind.

This was the highest-priority item available, and not a close call: it is the only
one that is a security hole rather than an internal improvement.

**Decided** in `docs/ADR_AUTH_AT_THE_COMPOSITION_LAYER.md`: reads public, writes
require a bearer token, and a configuration with no token does not start.

The last clause is the part worth defending. The natural reading of an absent
`MUTATION_TOKENS` is "no authentication required", which is how an endpoint with
ten write mutations becomes public because someone removed an environment variable
rather than because anyone chose it. `config.Load` refuses instead, and names the
variable.

**Placed at the operation layer**, via gqlgen's `AroundOperations`. The question
"is this a write?" cannot be answered from the HTTP method — `POST /query` carries
both — and re-parsing in middleware would parse every request twice, which is the
cost Automatic Persisted Queries exists to remove. At that point gqlgen has already
parsed, resolved persisted queries and selected the operation by name.

This placement also closes a hole a body-inspecting middleware would leave: a client
cannot register a mutation once as a persisted query and execute it by hash
forever.

### The tests prove the write did not happen

Every rejection test asserts on a counter in the stub store, not on a status code.
Removing the registration turns the main test red with:

    the write reached the domain layer 1 time(s); authentication is a control
    that must stop the write, not merely report on it afterwards

Two more negative checks were run deliberately:

| Sabotage | Caught by |
|---|---|
| Remove the `AroundOperations` registration | `TestMutationWithoutATokenIsRefusedAndDoesNotWrite` — write count 1 |
| Gate every operation instead of only writes | `TestQueriesRemainPublic`, `TestIntrospectionRemainPublic` |

A third sabotage was **not** caught, and is recorded rather than papered over:
replacing the constant-time comparison with an early-returning one passes every
test. Timing a comparison loop reliably needs a statistical test or an AST check on
`auth.go`'s own source, and a timing test that passes by luck teaches a reader the
property is verified when it is not.

### One test bug worth recording

The persisted-query test initially asserted the right thing for the wrong reason.
Automatic Persisted Queries only populates its cache when the hash **and** the
document text arrive together, so the test's "register" step — which sent the text
alone — cached nothing, and the replay returned `PERSISTED_QUERY_NOT_FOUND` rather
than `UNAUTHENTICATED`.

A test that passes for the wrong reason is worse than one that fails, because it
reports the property as covered. Fixed, and the helper now marshals its JSON
instead of formatting it: the hand-built body had four nested braces and was missing
one, which surfaced as "unexpected EOF" — an error about the test's transport
rather than about the rule it was meant to test.

### Blast radius

Making `MUTATION_TOKENS` mandatory at `config.Load` correctly refused to start for
every existing test harness in the service. Six call sites in `hardening_test.go`,
one `config.Load` helper in `graph/integration_test.go`, and eleven across the
config package all now configure a token. Two graph integration tests were genuinely
performing writes and had to start presenting a credential — which is the rule
working, not the rule breaking something.

---

## 12. One place where a port is assigned

**The question asked:** are ports hardcoded or environment variables, and do
they collide with what a developer is already running?

**Answer:** environment variables, but the defaults live in five separate config
files, four Dockerfiles, five compose files and the aggregator's dependency
targets — seven places, no index, and nothing that noticed when they disagreed.
Compose maps `host:container` with identical numbers, so on a laptop the port is
really taken.

**The collision, verified rather than remembered.** The gRPC range 9090–9094 sits
on Prometheus's allocated range and, at 9092, Kafka's:

| Port | Repo | Belongs to |
|---|---|---|
| 9090 | aggregator default `CATEGORY_GRPC_TARGET` | Prometheus server |
| 9091 | aggregator default `MEDIA_GRPC_TARGET` | Prometheus Pushgateway |
| 9092 | `GRPC_PORT` product-service | **Kafka broker** |
| 9093 | `GRPC_PORT` product-listing | Prometheus Alertmanager |
| 9094 | `GRPC_PORT` product-search | Prometheus Alertmanager clustering |

Prometheus's own port-allocation wiki marks 9092 `UNALLOCATED (to avoid
collision with Kafka)`. Prometheus avoided 9092 to leave room for Kafka; this
repository allocated it.

**The sharpest failure mode is not a bind conflict.** `category-service` and
`media-service` do not exist in this repository at all, yet the aggregator's
defaults point at 9090 and 9091. Run the aggregator with defaults beside a
running Prometheus and it sends gRPC to Prometheus's HTTP port — failing with a
message that points nowhere useful.

**Decided (D-019):** `ports.txt` at the repository root is the single place a
port is assigned. `ports_test.go` walks out of it into every artefact that has to
agree — Go defaults, `EXPOSE`, compose mappings, and the aggregator's gRPC
targets, which previously had no link at all to what the peers bind.

**Staging and production publish no host ports.** Services reach each other over
the container network by service name. That is the load-bearing half: it makes
most of the collision problem disappear, and it is why the registry has a dev
column and no deployment columns. `TestNoDeploymentPublishesHostPorts` passes
vacuously today — there are no deployment manifests yet — and exists so that
adding one which publishes host ports fails with an explanation.

**Renumbering was deliberately not done.** It changes the default every client,
deployment manifest and client-side fallback depends on. Instead the gate refuses
a *new* undocumented collision and accepts the four recorded ones. Same trade as
the aggregator ratchet: a rule that blocks a known problem is a rule people route
around.

**Three negative tests, all confirmed to fail when they should:**

| Sabotage | Caught by |
|---|---|
| Claim 9090 as `gogo` when Prometheus owns it | `TestNoPortCollidesSilentlyWithWellKnownDefaults` |
| Change a service's `GRPC_PORT` without the registry | `TestPortRegistryMatchesReality`, `TestAggregatorTargetsMatchTheRegistry` |
| Point two services at one dev host port | `TestNoPortIsClaimedTwice` |

The third took two attempts: the first edit used an exact string that did not
match the file's column spacing, so it silently changed nothing and the test
passed. A negative test that passes because the sabotage did not apply is worse
than no negative test, because it reports the rule as covered.

**Not enforced, and recorded as such:** whether the developer's own machine
already has something on a port. That is a runtime question. The dev column makes
collisions rare, not impossible.

---

## 13. The control from the last ADR was bypassable

**What was found.** Authenticating the composition layer does nothing if the
services behind it are directly reachable — and they were. Measured before
changing anything:

```
$ grep -rl 'Authorization|Bearer|JWT|OAuth' --include=*.go   (production)
0
```

`product-service` holds the ten routes that write the catalogue, listens on host
`8082` and `9092`, and builds its handler from a bare `http.NewServeMux()`:

```
POST   /api/v1/products          CreateProduct
PUT    /api/v1/products/{id}     UpdateProduct
DELETE /api/v1/products/{id}     DeleteProduct
```

So `curl localhost:8082/api/v1/products -d '{...}'` wrote to the catalogue with
no token, while `MUTATION_TOKENS` sat unused a few ports away. The previous ADR's
premise — "one point closes all APIs" — was true only of the paths that point
went through. This was not the next problem; it was a hole in the control just
shipped.

**Decided** in `docs/ADR_SERVICE_TO_SERVICE_AUTH.md`: every platform service
refuses unauthenticated callers on both transports, and none starts without a
credential. The aggregator presents `DOWNSTREAM_SERVICE_TOKEN`.

**The read models were guarded too.** Only product-service has writes, so it was
the only one where the bypass corrupted data. Leaving two of four services open
produces a posture nobody can reason about, and the next service would have no
precedent.

### The finding worth more than the fix

Every guard is installed in a composition root, and **the composition root was not
covered by any test.** Verified by sabotage — all four passed with the guard gone:

| Sabotage | Suite result before |
|---|---|
| Remove the gRPC interceptor from product-service | 0 — unguarded |
| Remove the HTTP guard from product-service | 0 — unguarded |
| Remove `/health` from the exempt list | 0 — probes would answer 401 |
| Neutralise the guard in listing / search | 0 — unguarded |

The `serviceauth` tests all passed throughout: they test the mechanism, and
deleting its *installation* is invisible to them. Three extractions closed it, each
re-verified by re-running the same sabotage — `grpcServerOptions`, `wrapHandler` /
`wrapChain`, and `probePaths` as a package value the test reads rather than
restates. All four sabotages are now caught.

The probe exemption is the one that would have hurt most: an orchestrator getting
401 from a liveness probe restarts the container, and restarting the process
answering the probe turns a partial outage into a total one.

### Recorded rather than claimed

The bearer token travels over an insecure gRPC connection, in the clear. This
protects against reachability, not against an on-path attacker; closing that is
TLS or mTLS, a separate decision. Written down in the source and the ADR so it is
discovered there rather than in an incident.

gRPC health is also not covered by a test — it is answered before interceptors run,
so the chain cannot affect it, and covering it meant extracting the health
registration out of `buildGRPCServer` as well.

### Three test expectations inverted

`TestLoadWithoutAnyEnvironment` asserted that a service boots with nothing
configured, so it can come up before its dependencies. Still true of every other
setting; no longer true of the credential. Inverted in all three services, with
the reason written into the test rather than left implicit.

Six existing tests across the three services broke on the new requirement and were
updated rather than worked around — including `TestPanicIsRecoveredAndIdentified`,
which would have passed for the wrong reason (asserting 500 against a 401 the
guard produced) had the credential not been supplied.

## 14. The island ownership rule is enforced, not documented

`docs/DECISION_UI_OWNERSHIP.md` §5 decided that an island key carries its owner
namespace and that "the test fails on an unnamespaced key". Until now it was the
only item of that decision with no code behind it: `render.Island` accepted any
string, and §4's "add a test per island" had no per-island test either.

**The rule.** `render.ValidateIslandName` accepts `"<owner>.<island>"` (or `/`),
with the owner a lowercase identifier. Everything after the first separator is
deliberately unconstrained — paged islands append their own coordinates
(`":t<hash>:p3"`) — so the rule constrains exactly the part that carries
ownership. The enforcement point is the constructors: `Island` and
`ClientIsland` are the single place an island enters a composition, so an
unnamespaced key panics there — and `pagekit.NewPagedListPage` checks eagerly,
so the failure lands at wiring, not on the first request. A bare key would
otherwise alias another unit's cache namespace, and the collision would surface
as another team's invalidation dropping this team's cache with nothing in the
build to explain it.

**What the tests pin.** `islandname_test.go` table-tests the rule, including
each failure shape (no separator, non-identifier owner, empty island,
whitespace, the hostile `evil"><script>`). The composer fixtures now use
namespaced names (`render.static`, …), which is what the rule requires of every
real island. The home key is covered where it is declared: `islandkeys_test.go`
asserts `home.product-grid` validates and is namespaced to the page that owns
it — an unowned key fails at the declaration site rather than at the first
render.

**The XSS test is the one worth noting.** The old attribute-escaping test built
a hostile island through `ClientIsland("evil\"><script>…")`. That name can no
longer be constructed through the owner-enforcing constructor, and the test now
builds the `Component` literally. The two layers stay distinguishable: the
constructor refuses to create a hostile name; the renderer still escapes one if
it ever arrives another way.

**§4's per-island test.** `TestHomeGridIsPublicAndCacheable` primes the grid
with a full-page request and then serves an HTMX fragment, asserting the
platform is not fetched twice: the island is declared `Public` and not
`Personalized`, which is what makes its tenant-qualified cache entry safe to
serve.

## 15. Four defects the coverage pass surfaced, fixed

Taking the remaining 41 packages from "[no test files]" to covered turned up four
defects that had survived review because nothing exercised the paths. Each fix
writes the contract into a test.

**The auth error table shadowed itself.** `ErrInvalidPIN` and `ErrExpired` wrap
`ErrInvalidEmployee` and `ErrInvalidSession` (`fmt.Errorf("%w: …")`), and
`Classify`/`Message` checked the buckets first, so every specific answer — "PIN
must be 6 digits", "session has expired" — was demoted to its bucket's code and
message: `KindFailedPrecondition` and `KindUnauthenticated` were unreachable.
The table now checks specifics before the buckets they wrap, and login's
credential mismatch is its own sentinel (`ErrInvalidCredentials`, answered
`KindUnauthenticated`) instead of reusing the wrong-shape `ErrInvalidPIN`.
`errors_test.go` pins every sentinel's kind and message, bare and wrapped. The
grpcservice classification test, which had pinned the shadowed answers as "what
the adapter actually answers", now pins the real ones: FailedPrecondition for a
malformed PIN and an expired session, Unauthenticated for a credential mismatch.

**The seller's transaction table mislabelled its columns.** Headers
"Bayar"/"Metode" sat over the payment-method and cashier cells. The cells match
the rest of the console — a `Petugas`-style person column, `Kasir` in the nav —
so the headers were corrected to "Metode" and "Kasir". The reverse reading (a
money "Bayar" column for `Paid`) was considered and rejected: nothing else in
the page renders `Paid`, and the cells carry no numeric styling.

**The browse mapper dropped Description.** `coreFromBrowse` never copied
`Description` from the listing contract while the search and write mappers did;
`TestListPageMapsWireToCore` now asserts it.

**Two comments described behaviour that does not exist.** The home read model
claimed the grid renders a barcode line (no contract carries a barcode and no
view renders it) and that the pagination component is written against
`NextCursor` (the UI pages by number, and pagekit's handler discards the cursor
slot its Fetch contract declares). The comments now say what the code does.

## 13. Audit foundation safety pass

The general audit logger was not wired to a durable store. Its in-memory adapter
retained caller-owned pointers, returned pointers callers could mutate, used
timestamp-only IDs, and could panic while diffing nested JSON values. The
middleware also discarded persistence errors. The safety pass now snapshots
entries on write/read, uses UUID IDs and UTC timestamps, handles nil filters,
respects context cancellation, captures added/changed/removed JSON fields, and
surfaces audit logging errors alongside operation errors. Event publication and
fallback storage share the same prepared event identity.

This does **not** close the durable audit control: middleware remains unused and
in-memory, and post-operation logging is not transactionally coupled to domain
writes. The inventory movement ledger is durable for stock adjustments only.
General audit adoption still requires the event scope, actor source, transaction
boundary, access policy, and retention to be decided by their owners.

## 14. Seller session boundary

The seller console routes now validate the session cookie through the auth
contract before serving the console. Inventory adjustments, checkout and void
records take their actor identity from that validated employee; form fields no
longer choose the stock actor, cashier, or voiding employee. Auth dependency
failures return an unavailable response instead of treating the caller as
anonymous. This establishes authentication and attribution, not authorization
coverage. The static role-matrix question was superseded by the owner's decision
that roles and grants must be dynamic and users may hold several roles; see
`docs/ADR_IDENTITY_AUTHORIZATION.md` and OD-4 in `docs/OPEN_DECISIONS.md`.

## 15. Dynamic identity and authorization baseline

The owner selected dynamic, multi-role access. The architecture baseline now
separates PEP, PDP, PIP, and PAP; uses tenant-scoped RBAC for composable grants
and code-enforced ABAC for active identity, tenant equality, and domain
invariants; and leaves relationship authorization behind a PIP boundary until
an authoritative store-membership relationship exists. Legacy role names grant
no implicit permissions. The first policy-administrator bootstrap is OD-5 and
must be ratified before provisioning the first production policy administrator
or enabling authorization in production. The PAP endpoints remain default-deny
without that provisioned authority.

## 16. One-time policy-administrator bootstrap

Added `apps/web/cmd/authzbootstrap` and an auth Postgres bootstrap operation.
The operator supplies a separate database URL, tenant, active employee, and
change reference. The operation serializes attempts by tenant, atomically
creates the reserved policy-admin role, grants only the three PAP capabilities,
assigns the role, and writes an event containing PostgreSQL `session_user`.
Legacy roles remain unprivileged. OD-5 stays open: the deployment/security
owner must ratify operator identity and production procedure before this command
is used in production. A shared database login cannot attribute the action to
an individual human.

## 17. Deny-by-default seller authorization

Added `CheckPermission` to the auth contract. The auth application resolves the
principal from a live session, then the Postgres PDP joins current active
employee, tenant role assignments, roles, and permission grants with their
time bounds. Unknown permission keys and the in-memory auth adapter deny. The
seller route PEP now maps each read and write route to a code-owned permission
key; an explicit deny returns 403 and an auth/PDP outage returns 503. The
permission catalog migration introduces seller dashboard, catalog, category,
inventory, checkout, and void capabilities.

This is route-level enforcement only. Durable decision evidence and checks in
service-owned mutation use cases remain required before production
authorization coverage can be claimed.

## 18. Dynamic policy administration APIs

Added session-authorized auth RPCs to create tenant roles, grant/revoke known
permissions, and assign/revoke roles for active employees. The tenant is always
resolved from the session; requests cannot nominate a tenant or actor. Temporal
grants and assignments, mandatory reason/change reference, a tenant advisory
lock, and transactional audit events preserve a reviewable history. Expired
rows are closed before reissue, and the last active holder of each PAP
capability cannot be removed. Authorized reads expose active roles with grants
and per-employee assignments with optional history. `AUTHZ_DATABASE_URL`
bootstrap remains a deployment procedure under OD-5 review; these PAP APIs are
intended for the bootstrapped authority.

## 19. Sales and inventory service-boundary authorization

Internal gRPC now carries the seller's opaque session reference across the
private in-process transport. Sales reads, checkout, and void resolve the live
session and check their existing permission keys at the sales contract
boundary. Cashier and void actor fields come from that resolved identity;
request actor fields are ignored. Inventory reads and direct stock adjustments
enforce inventory permissions at its contract boundary. The sales-owned stock
adapter marks stock effects so inventory checks checkout/void permission while
still recording the employee as the movement actor.

The sales adapter presents a random process-local capability over private
`bufconn`; this is not network service authentication. Inventory movements now
persist employee subject and service actor separately. Before network
separation, replace the in-process capability with authenticated service
identity and a scoped delegated credential. Sales/inventory permission denials
also produce structured logs with tenant, subject, permission, and module; they
exclude session identifiers. Central durable log collection, tamper protection,
retention, and review remain deployment controls to implement.

## 20. Close legacy employee administration RPCs

The auth contract still contains employee CRUD RPCs whose requests have no
session or permission fields and have no application consumers. The server now
returns `Unimplemented` for those legacy methods instead of allowing an
unauthenticated bypass around session-bound policy administration. Login,
session validation, and PAP methods remain available. A future employee
lifecycle API must define explicit capabilities and derive tenant/actor from a
validated session.

## 21. Session-bound employee roster read

Added `authz.employee.read` to the permission catalog and bound
`ListEmployees` to a live session. The auth service resolves the tenant from
that session and ignores the request's legacy tenant field. The shared auth
consumer can request the roster without importing protobuf types. The endpoint
supports access-review tooling; employee create/update/PIN lifecycle remains
disabled until a separately authorized lifecycle contract is designed.

## 22. Tenant-wide role assignment review

Added `ListTenantRoleAssignments`, a session-bound read API requiring the
separate `authz.assignment.review` permission. It returns active and historical
assignments in bounded pages using a stable `(assigned_at, assignment_id)`
keyset cursor, with a tenant-leading index. The existing per-employee query
continues to require assignment-management authority. This provides data for a
review; it does not itself schedule, attest, or retain periodic review evidence.

## 23. Assignment review provenance

Both per-employee and tenant-wide assignment reads now return the recorded
assigner identifier and assignment source alongside reason and validity dates.
The fields are projected from the existing tenant-scoped history table, and
the protobuf contract exposes them to review clients. This improves reviewer
context without creating a separate audit authority or asserting that the
recorded identifier always maps to a human operator.

## 24. Read-only policy administration audit history

Added a paginated auth RPC for durable `authorization_admin_events`, guarded by
the separate `authz.audit.read` capability. It derives tenant scope from the
validated session and returns the recorded actor, database principal, action,
subject/role, change reference, and timestamp in stable keyset order. It covers
policy-administration events only; it is not a general authorization-decision
log or an integrity, retention, or review-evidence system.

## 25. PAP audit event reason

Policy grant, revoke, and assignment events now persist the reason already
required by their commands, and the audit read API returns it. Automatic closure
of expired grants/assignments records a fixed factual reason. Role creation
continues to have an empty reason because its existing contract asks only for a
change reference. Existing event rows migrate with an empty reason; they are
not backfilled from mutable grant/assignment rows.

## 26. Prevent ordinary PAP audit event mutation

Migration `0009_authorization_audit_append_only.sql` adds database triggers
that reject `UPDATE`, `DELETE`, and `TRUNCATE` against policy administration
events. This guards ordinary DML paths without assuming a deployment role
name. A database owner or superuser can still alter the table or disable its
triggers; external immutable storage and verified retention remain operational
controls, so this is not tamper-proof or WORM evidence.

## 27. Fragment delivery: streaming and per-island fault tolerance

The render composer now supports whole-document streaming and per-island
failure handling, per `docs/ADR_FRAGMENT_DELIVERY_AND_MEASUREMENT.md` (owner
ratified 2026-10-17). Streaming is opt-in per page (`Page.Streaming`) and only
applies to full documents — HTMX fragments stay buffered so a swap stays
atomic. A streamed page holds the layout shell and flushes each island as it
completes, with the status line and `Trailer: Server-Timing` declared before
the first flush; after the first flush an island error can only truncate the
document, which is why every fallible island is expected to degrade, not fail.

Fault tolerance is per island: `Component.Load` (server-side fetch, never
called on a cache hit), `Component.Timeout` (explicit; streamed islands get a
3 s default so one slow upstream cannot stall a document), and
`Component.Fallback` (rendered with the island context's values but no
deadline, since the deadline that fired is why it runs; a fallback-served
island is counted under its failure cause *and* as a fallback, so substitution
is visible and never masquerades as a healthy render). An island without a
fallback fails the render: buffered responses return a clean error before
anything is written; a streamed response is truncated. The home page streams;
the product-detail page is deliberately buffered because its honest statuses
(404/503) must be decided before the response begins — recorded in the ADR.

Measurement rides along: every island render reports `Server-Timing`
(sanitized name, duration, fallback description), and the expvar
`gogo_render_islands` aggregates renders, fallbacks, timeouts, errors and
duration per island family, with Prometheus rows at `/metrics`. Client-mode
placeholders are never counted — they are not renders. The pagekit
cached-fragment fast path now also sets `Vary` and `Content-Type` on cache
hits, closing a header gap where the fast path bypassed the composer's header
assembly.

## 28. Field measurement: web-vitals beacons at POST /rum

The gateway now receives its first field data. A vendored web-vitals v4.2.4
attribution build (Apache-2.0, license header preserved, `assets/js/`) observes
LCP/INP/CLS/FCP/TTFB in real browsers and beacons each metric to `POST /rum`,
attributing it to the island that owned the element (`[data-island]`), or to
`page` for document-level metrics. The receiver is deliberately dumb: POST
only, 4 KiB body cap, strict metric and rating sets, no auth (it stores
bounded aggregates and nothing else), 204 on acceptance.

Island labels are normalized before aggregation so client input cannot mint
cardinality: names out of grammar become `unknown`, valid names aggregate
under their family (part before `:`, which drops paged coordinates and cache
keys), digit-only dotted segments collapse to `{id}`, and a 64-family cap
bounds the map. The path is accepted for wire compatibility and deliberately
neither stored nor logged. Aggregates land in the expvar
`gogo_rum_web_vitals` (`metric|island|rating → count, value_sum`) and surface
beside the island server timings at `/metrics` — server and client halves of
the same per-fragment measurement, per the ADR's measurement decision. Every
island is now wrapped in a `ds-island` marker element so client attribution
has an anchor.

## 29. Ownership catalog completed, scope widened to the gateway

`team_catalog.json` now describes the whole gateway release unit (scope
`gateway`, replacing the single-flow `seller-stock-adjustment`), per ADR
decision 4: 14 units across T2–T5 with kind, tier, bounded context, complexity
and ladder rung — including `internal/pages/home` (T3), the identity module
and surfaces (T4), and platform-foundation units T5 (shared, bootstrap, cmd,
design-system/web/ui, billing/ops/contentcms as derived-but-recorded). The
`auth.v1/AuthService` contract row was added because the validator accepted it
from the existing import edges (provider `internal/modules/auth`, consumer
`internal/shared`), which is the ADR's stated condition; `authconsumer` stays
a shared-module consumer kit, not a T4 contract. No CODEOWNERS is emitted —
the repository has no organisational data, per the PRD. `make
arch-team-catalog` passes; `import_graph.txt` was regenerated, since the graph
is derived from the working tree and both the auth-boundary edges and the
page-composition edges existed in it uncommitted. The regen also surfaced one
open layer violation from the in-flight auth work: `internal/bootstrap`
imports `modules/inventory/presentation/grpcservice` directly instead of going
through the module's compose or root package — owner T5/T2, not fixed here.

## 30. Naming ambiguity pass — what changed and what deliberately did not

Per ADR decision 5: `render/templutil.go` → `render/html.go` (no `util` file
names), `Composer.CachedFragment` → `Composer.CachedIsland` (an island's bytes;
“fragment” stays reserved for HTMX response fragments),
`renderClient` → `renderClientPlaceholder` (names what it renders). The
`Load`/`FetchURL` pair keeps its names and now documents its contrast on the
struct — `Load` runs server-side inside composition, `FetchURL` is fetched by
the browser for Client-mode islands — so neither verb is ambiguous alone.
Deliberately unchanged: the two `products/{list,detail}` packages (aliased at
import sites; trigger: a third `detail` package) and `views/pages` directory
names (load-bearing in generated-code paths). A repository-wide search
confirms no references to the old names remain.

## 31. Follow-up pass — ledger emptied, kernel trimmed, guide trued up

The `docs/FOLLOW_UP.md` backlog was worked end to end. The result, in the
file's own order:

- **Composition exception retired (finding #2, §5.2).** `inventoryclient.New`
  is called from `sales/compose` now (`compose.NewInventoryAdjuster`); the
  composition root composes the module with the raw connection, breaker and
  capability and never names the module's infrastructure package.
  `compositionExceptions` in `layers_test.go` is empty — the ledger stays as
  the place a future exception is written down, not as debt.
- **Kernel trimmed.** `internal/shared/kernel/interfaces.go` keeps only the six
  interfaces with a live consumer (`EventBus`, `Event`, `EventHandler`,
  `Subscription`, `Logger`, `Validator`); the unreferenced fourteen are
  deleted, `EventPublisher`/`EventSubscriber` fold into `EventBus`, and the
  unused `ServiceManager` lifecycle helper went with them. An interface
  without a named consumer is a diagram, not a contract.
- **ID generation consolidated.** `shared/idgen` deleted — it existed only for
  the never-mounted `media` module, and no committed revision imported it — and
  auth's hex `generateID` replaced with `uuid.NewString()`. The recorded
  key-space split stays: product IDs are the platform's `int64` as decimal
  strings, inventory and sales mint UUIDs — a seller never mints a product ID.
- **`D-010` tombstoned.** No revision of `ARCHITECTURE.md` ever contained a
  D-010 entry (`git log --all -S "D-010" -- ARCHITECTURE.md` returns nothing);
  the number is now retired explicitly instead of silently skipped, satisfying
  the prerequisite `TEAM_TOPOLOGIES_PRD.md` §0.4 records.
- **`MIGRATION_GUIDE.md` trued up** against the working tree (real `di.go`
  wiring, billing's subscription pattern, the island-name rule, `apps/web/...`
  paths), with the one forward-looking snippet labelled as a shape. The guide
  and `foundation/README.md` remain the pair; if they drift again, the guide
  should be cut down to the README.

`make arch-graph` was regenerated (the graph lost the `idgen` node and the
bootstrap→sales/infrastructure edge), and the full `make check` gate passes.
