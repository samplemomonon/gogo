# Implementation Log — P0 to P4 executed

**Date:** 2026-10-15
**Companion to:** [`IMPLEMENTATION_PLAN.md`](./IMPLEMENTATION_PLAN.md) · [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md) · [`ARCHITECTURE.md`](../ARCHITECTURE.md)

This records what was actually done, and — more usefully — what the plan got
wrong. Two of its claims did not survive contact with the code, and both are
written up below rather than quietly dropped.

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
| 1 | `inventory`'s oversell invariant lives in SQL, not in `domain/`; `ports.StockService` is satisfied by repositories, and there is no `application/service.go` | `internal/modules/inventory` | **design** — needs the Phase 0 vertical slice, not a refactor |
| 2 | `bootstrap` reaches `sales/infrastructure` | `sales_connection.go:30` | design — §5.2 |
| 3 | ~~`aggregator-service` owns no persistence, and nothing checks it~~ | — | **CLOSED.** Tripwire written at `internal/services/aggregator-service/internal/architecture/`. See §8 |
| 4 | ~~`media.v1` has no breaker in the gateway~~ | — | **NOT A FINDING — verified and withdrawn.** `productconsumer.Connections` has exactly four upstreams: Write, Browse, Search, Category. The gateway never calls `media.v1`; `media-service` is reached by `aggregator-service`, a different binary. A breaker would guard a dependency that does not exist. The `units.yaml` comment already had this right; the log entry was wrong |
| 5 | ~~`product-listing-service`'s usage string contains a `+event.Topic+` template artefact~~ | — | **CLOSED.** The `event` import existed only to support the dead placeholder — with the placeholder removed the import was unused and the package stopped compiling. `config.DefaultKafkaTopic` / `DefaultKafkaDeadLetterTopic` are now named constants, and `usage` uses `Fprintf` so the placeholders actually expand |
| 6 | B6 (monorepo vs polyrepo) undecided; the repository still contradicts itself about whether `internal/services/` is in-tree | see PRD §2.6 | needs a decision, not code |
| 7 | `devenv.lock` exists for 2 of 6 units; the two `devenv.nix` files are still `devenv init` scaffolds | `internal/services/*/devenv.nix` | small |
| 8 | 675 MB of `.devenv` state still on disk (git- and docker-ignored) | two service directories | cosmetic — yours to delete |

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
resolve to itself. `go list ./...` inside `internal/services/aggregator-service`
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
`internal/services/*/.devenv` whose `state/go/pkg/mod` is a Go module cache. A
recursive walk adopts every cached module into the workspace and the build fails
with `module google.golang.org/protobuf appears multiple times in workspace`.

`go.work` now lists all six modules by hand and carries a comment saying so. The
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
