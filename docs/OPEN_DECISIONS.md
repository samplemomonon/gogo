# Open Decisions Register

**Date:** 2026-10-15
**Purpose:** every question this repository has deliberately *not* answered, with its owner, its closure options, and — critically — **which way the code currently falls so the reader knows that is not the same thing as a decision.**

**Rule this register exists to enforce:**

> A test may enforce a contract that has been decided. It may not quietly decide a question by making one answer build-failing.
>
> Where an item below is *undecided*, any behaviour in the code that appears to enforce an answer is an artefact of the undecided state, and is recorded as such rather than presented as an invariant.

Three items are open. Two are structural (they need a repository decision). One belongs to a contract owner outside this codebase's architecture.

| ID | Decision | Owner | Blocks | Rung today |
|---|---|---|---|---|
| **OD-1** | Fail-closed allowlist and `forbidigo` as the enforcement mechanism | project owner | nothing — it is a strengthening | not started |
| **OD-2** | May the aggregator hold a cache or read model? | project owner | the scope of the aggregator's architecture rule | **test currently permits it — unratified** |
| **OD-3** | Must `type Mutation` be part of the permanent aggregator contract? | product / API contract owner | closing the schema TODO in the aggregator's architecture test | **test currently requires it — temporary, marked** |

---

## OD-1 · Enforcement mechanism: allowlist and `forbidigo`

**Status:** decided — **not adopted**. Recorded here so the recommendation is not silently lost, and so a later reader does not assume it was considered and rejected.

**The recommendation.** Research against published composition layers concluded that a *denylist of database libraries* is the wrong shape — fail-open, must be maintained forever, and cannot match standard library. The recommended replacement is two fail-closed rules:

1. A `depguard` strict allowlist scoped to production files. `depguard`'s own documentation describes strict mode as *"everything is denied unless in allowed"*, and golangci-lint ships that as its default: when no rules are written, it allows only the standard library.
2. A `forbidigo` rule on `^sql\.(Open|OpenDB)$`. `database/sql` is standard library, so no third-party denylist can match it — and every pure-Go SQL path, present and future, funnels through those two functions.

**Why it was not adopted.** The repository enforces its architecture with plain `go test` — five architecture test packages, no linter dependency, no lint configuration. Adding `golangci-lint` with `depguard` and `forbidigo` changes the enforcement *mechanism* and adds a maintenance burden across every future change. That is a decision for the project owner, not something to slip in alongside a boundary test.

**What exists today instead.** `internal/services/aggregator-service/internal/architecture/architecture_test.go` denies exactly one import — `database/sql` — derived from `go list -deps`. That is a **denylist of one**, which is the weakest form of the idea OD-1 proposes to replace.

**Closure options.**

| # | Option | Consequence |
|---|---|---|
| 1 | Adopt `depguard` strict + `forbidigo`; retire the `go list` walk | Strongest, fail-closed. Costs a lint dependency and a new configuration surface |
| 2 | Keep `go test`, extend to a small allowlist | Stays dependency-free. Fail-closed, but the allowlist is hand-maintained in Go |
| 3 | Keep as-is | The rule stays a one-item denylist and a new driver arrives unchallenged |

**Note for whichever is chosen.** An earlier draft of the aggregator's architecture test denied nineteen module roots — `pgx`, `lib/pq`, `go-sql-driver/mysql`, `gorm`, `ent`, `badger`, `buntdb`, `goleveldb`, `bbolt`, `etcd`, `go-redis`, `goose`, `golang-migrate`, `sqlite` and others. That list was written from memory and then audited: **none of those items could be corroborated as present in any published composition layer**, and no Go-language composition layer was found at all. The one capability with real support behind it — Redis — appears in Tokopedia's GraphQL gateway as *legitimate caching*. That list was deleted rather than repaired.

---

## OD-2 · May the aggregator hold a cache or read model?

**Status:** undecided. **This is the question that determines how much the aggregator's architecture rule can say.**

**Why it is open, and why neither answer can be assumed.** The aggregator's `README.md` and `cmd/server/main.go` both state *"It owns no data and no database"*, and both explain that the reason is that everything is read from another service at request time. That is a statement about **today**. It is not a decision about whether the service may ever hold derived state, and treating it as one would be reading more into the prose than it carries.

The published evidence does not settle it either, and it cuts both ways:

| Direction | Evidence |
|---|---|
| **Caches are legitimate** | Microsoft's CQRS guidance calls a materialized read model *"in effect a durable read-only cache of the data"* and states that because the event store is the source of information, *"it is possible to delete the materialized views and replay all past events"*. Kleppmann: *"if you lose your cache, you can rebuild it from the underlying database; thus, the contents of the cache are derived from the database."* Redis on eviction: *"since cache entries are copies of persistently-stored data, it is usually safe to evict them."* **Tokopedia's GraphQL gateway — the closest published analogue to this service — caches upstream data in Redis and calls it caching.** |
| **In-memory read models are a database** | Netflix Tudum replaced a Cassandra query database and a key-value store with RAW Hollow, which Netflix itself describes as *"an in-memory, co-located, compressed object database"*. Netflix named the old Cassandra layer *"a database"*. |

So the industry does not draw this line consistently, which is exactly why it is a project decision rather than a research output.

**What the code does today, and why that is not a decision.** The aggregator's architecture test forbids `database/sql` only. It therefore **permits** an in-memory cache, a read model, and a Redis dependency. That permissiveness is the *accident* of a narrow rule, not a ratified position. If OD-2 closes as "no derived state either", the rule has to be extended and the test changed; if it closes as "derived state allowed", the current behaviour becomes correct and should be documented as intended rather than incidental.

**Closure options.**

| # | Option | What the rule then has to express |
|---|---|---|
| 1 | **Derived state is allowed** | The rule must distinguish *authoritative* state from *derived disposable* state, and say which is which. It must also define what makes a read model "derived" — rebuildable from an upstream service or an event stream — because that is the testable part |
| 2 | **Derived state is forbidden too** | The prohibition must be stated explicitly with a defined scope: which packages may not be imported, and how that is checked. It cannot remain the absence of a rule |
| 3 | **Deferred** | Name the decision owner and a review date. The rule stays as it is, explicitly marked provisional, so it is not mistaken for a settled position |

**A related risk worth recording, whichever way it closes.** Airbnb describes the documented failure mode of a shared composition layer as *"the slow creep toward monolith"* — a growing schema accumulated across teams — and Zalando constrains its GraphQL layer with a *"No Business Logic"* principle. Neither failure is a datastore. A datastore would be loud. **Logics accretion is silent**, and OD-2 does not cover it; the aggregator's `TestAggregatorHoldsNoDomainModel` covers one visible symptom of it, and one symptom is not the disease.

---

## OD-3 · Must `type Mutation` be part of the permanent aggregator contract?

**Status:** undecided — and it belongs to a contract owner outside this codebase's architecture.

**Why it cannot be settled from the code.** Three category mutations — `createCategory`, `updateCategory`, `deleteCategory` — always answer `DEPENDENCY_UNAVAILABLE`, because `category.v1` exposes no write RPCs. So the `Mutation` root currently advertises surface that cannot succeed.

That fact alone does not determine the answer. It is equally consistent with three different intents:

- the mutations are a **bug** and should be removed;
- they are a **placeholder** for a contract that is coming, and should stay;
- they are a **deliberate pointer** — a mutation that answers "not available here" rather than a silent omission, which is a defensible thing for a gateway to do.

Which of those is true is a question about the product and the API contract, not about the repository.

**What the code does today.** `TestAggregatorOwnsItsSchemaContract` asserts the `Mutation` root exists, and the assertion carries a `TODO(architecture-decision)` marking it as a **temporary guard, not an architecture property**. It was kept, rather than removed, because removing it would also remove the signal that the contract is under question — but keeping it unmarked would turn a product question into a build-failing rule.

**Closure options.**

| # | Option | Consequence |
|---|---|---|
| 1 | The `Mutation` root must exist | Keep the assertion; record why, and which mutations are permanently unavailable |
| 2 | The `Mutation` root must not exist | Remove the assertion *and* the test that exists only to enforce its presence |
| 3 | Deferred | Name the decision owner and a review date, so the TODO does not become permanent by being left alone |

---

## How this register is used

- An architecture test may cite an OD id in its failure message when the behaviour it enforces is one of these.
- No item may be closed by an agent, an automated tool, or an architecture test. Closing an item means a human owner recorded a choice.
- When an item closes, the change goes here first, with the decision, its date, and its owner — and only then to the code.

## References

**Ownership and interaction**
[Team Topologies — key concepts](https://teamtopologies.com/key-concepts) · [interaction modelling](https://teamtopologies.com/key-concepts-content/team-interaction-modeling-with-team-topologies) · [Fowler on TT](https://martinfowler.com/bliki/TeamTopologies.html) · [Conway 1967](https://melconway.com/Home/Conways%5FLaw.html)

**Composition layer shape and dependency**
[Tokopedia — a GraphQL gateway that caches upstream data in Redis](https://ihsansatriawan.github.io/blog/2019/06/how-tokopedia-lite-use-graphql/) · [Netflix Tudum — from CQRS with Kafka to CQRS with RAW Hollow](https://netflixtechblog.com/netflix-tudum-architecture-from-cqrs-with-kafka-to-cqrs-with-raw-hollow-86d141b72e52) · [InfoQ on Tudum](https://www.infoq.com/news/2025/08/netflix-tudum-cqrs-raw-hollow) · [Airbnb — server-driven UI and the shared data layer](https://medium.com/airbnb-engineering/a-deep-dive-into-airbnb-s-server-driven-ui-system-842244c5f5) · [Zalando — "No Business Logic" in the GraphQL layer](https://engineering.zalando.com/posts/2021/03/how-we-use-graphql-at-europes-largest-fashion-e-commerce-company.html) · [Booking.com — page-owning rendering services](https://medium.com/booking-com-development/node-js-at-scale-rebalanced-how-we-cut-cost-by-38-62ce247a3002) · [Agoda — WebGate](https://medium.com/agoda-engineering/breaking-the-monolith-f3538d9c3ad6)

**Cache vs datastore**
[Microsoft CQRS pattern — "a durable read-only cache of the data"](https://learn.microsoft.com/en-us/azure/architecture/patterns/cqrs) · [Microsoft CQRS guide (archived)](https://msdn.microsoft.com/en-us/library/jj591577) · [Kleppmann — derived data](https://martin.kleppmann.com/2015/03/04/turning-the-database-inside-out.html) · [Redis — eviction is safe because entries are copies](https://redis.io/docs/latest/develop/reference/eviction/) · [Fowler on CQRS](https://martinfowler.com/bliki/CQRS.html)

**Enforcement mechanisms**
[depguard — strict, lax, and the original mode](https://github.com/OpenPeeDeeP/depguard) · [golangci-lint depguard configuration](https://golangci-lint.run/docs/linters/configuration/#depguard) · [forbidigo](https://github.com/ashanbrown/forbidigo) · [golangci-lint forbidigo configuration](https://golangci-lint.run/docs/linters/configuration/#forbidigo) · [containerd `.golangci.yml` — a denylist used for one specific dependency](https://cos.googlesource.com/third_party/containerd/+/refs/tags/v2.0.0-rc.0/.golangci.yml) · [`cmd/vet` analyzer list — there is no banned-imports analyzer](https://pkg.go.dev/cmd/vet) · [x/tools vet suite](https://pkg.go.dev/golang.org/x/tools/go/analysis/suite/vet) · [go-arch-lint](https://github.com/fe3dback/go-arch-lint)
