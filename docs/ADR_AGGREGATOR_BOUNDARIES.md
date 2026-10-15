# ADR: The aggregator's boundaries — OD-1, OD-2, OD-3

**Date:** 2026-10-15
**Status:** Accepted. Closes OD-1, OD-2 and OD-3 in `docs/OPEN_DECISIONS.md`.
**Owner:** project owner.

Three questions had been left open, and the register that tracked them carried a
rule worth restating, because it is the reason this document exists:

> A test may enforce a contract that has been decided. It may not quietly decide
> a question by making one answer build-failing.

All three are now decided. None of them was decided by a test.

---

## OD-1 · The enforcement mechanism

**Decision: adopt a two-way ratchet. Do not adopt `depguard` strict or
`forbidigo`.**

`forbiddenImports` in the aggregator's architecture test remains exactly as it
was — one entry, `database/sql` — and is no longer the weakest thing in the
package. `ratchet_test.go` adds a committed mirror of the module's first-party
import edges that fails on additions **and** on stale entries.

### Why not the allowlist

The proposal was sound. `depguard`'s strict mode is genuinely fail-closed, and
golangci-lint does ship it. It was declined for a reason this repository can
demonstrate rather than assert, because it has three recorded failures of the
same kind:

| List | What happened |
|---|---|
| ~19 forbidden module roots in a draft architecture test | Written from memory. On audit, **none** could be corroborated as present in any published composition layer. |
| 44 `BUILD.bazel` files | Outlived the build system that read them. Nothing invokes bazel; there is no `WORKSPACE` or `MODULE.bazel`. |
| `import_graph.txt`, derived from those files | Rotted to 43 of 119 packages **while its own staleness gate was failing**. |

One cause, three times: **a list that is not derived from something cannot stay
true.** A `depguard` allowlist is not derived from anything — someone edits YAML
when a dependency is added.

### What the ratchet buys instead

Craig Johnston's formulation: an allowlist entry has to expire, and equality must
run in both directions — "an allowlist that only grows is how a coupling you
deliberately removed gets pre-approved the next time an agent reaches for it."

Measured against the status quo it replaces. Adding `internal/retry →
internal/graph/model` — an inward-pointing edge, exactly the shape a hexagonal
boundary rule exists to catch — produces:

```
TestAggregatorOwnsNoDatastore        PASS
TestAggregatorHoldsNoDomainModel     PASS
TestImportRatchetIsCurrent          FAIL  new first-party import edge(s)
```

The pre-existing rules are blind to it. The ratchet is not.

### Why the `database/sql` rule stays as it is

OD-1 was raised about a new database driver arriving unchallenged. That risk is
closed by construction rather than by enumeration: every pure-Go SQL path passes
through `sql.Open`, so a driver that does not exist yet still has nowhere to
enter. `forbidigo` on `^sql\.(Open|OpenDB)$` would reimplement, with a new
toolchain dependency, a check one import line already performs.

---

## OD-2 · May the aggregator hold a cache or a read model?

**Decision: derived, non-authoritative state is allowed. Authoritative state is
not. Upstream-derived data may be held for one request and not beyond, until the
owning service declares freshness.**

### The distinction

Derived state is a copy. Authoritative state is something this service would be
the only holder of, which would then have to be kept in sync with the service
that owns the truth. The catalogue has exactly one writer, `product-service`, and
D-018 exists because two authorities for one row drift.

The failure mode is asymmetric, which is the whole reason this matters. A cache
makes no failure. A datastore makes a silent second authority: the API keeps
working, the schema keeps working, the tests pass, and the two disagree.

### Why the question is decidable now

When OD-2 was opened the honest position was that the evidence cut both ways —
Microsoft's CQRS guidance and Kleppmann on rebuildable derived data, against
Netflix's RAW Hollow being called "an in-memory, co-located, compressed object
database" while the Cassandra layer it replaced was called a database. The
industry does not draw this line consistently.

What settles it is not a better argument but a fact about composition layers,
taken from the closest published analogue available: **Apollo's GraphOS Router
caches upstream subgraph responses in Redis, and it is the reference
implementation of doing this well.** Its TTL is not invented by the router. The
router reads `Cache-Control: max-age` from the origin — derived from the origin's
own `@cacheControl` directive — and where a write must be visible immediately it
requires `@cacheTag` plus an authenticated invalidation endpoint that the owning
service calls.

**Freshness is declared by the party that owns the data, never by the layer that
caches it.** A composition layer cannot derive a TTL for data it does not own.
This repository's own `RequestCache` comment said so before any of this was
written: a longer-lived cache "would need invalidation this service has no way to
do correctly — the hierarchy belongs to category-service, not to the aggregator."

### What that means today, concretely

Measured, not assumed: `Cache-Control`, `max-age` and cache-tag directives appear
**zero times** across all five upstream services. Nobody has been asked to declare
freshness.

So the precondition is unmet, and the constraint is active rather than theoretical.

### What is enforced, and what is not

Honest accounting, because a decision that overstates its enforcement is worse
than an open question:

| Property | Enforced by |
|---|---|
| No authoritative state | `database/sql` forbidden in `architecture_test.go` — the single choke point every pure-Go SQL path passes through |
| No cache that nobody decided on | `caches_test.go` — every cache constructor in the module must be a declared one, and every declared entry must still exist |
| No upstream-derived data beyond one request | `caches_test.go` — fails if a declared cache is upstream-derived and outlives the request |

It cannot tell a cache from a read model by looking, and it does not try to.

---

## OD-3 · Must `type Mutation` be part of the permanent contract?

**Decision: yes — and the three category mutations are deprecated in the schema,
each with a reason naming the alternative.**

### What was rejected

The three options on the table were keep, remove, or defer. The first two fail
for the same reason, and it is worth being precise about why.

*Remove the category mutations.* graphql.org's own governance guidance classes
removing a field as a breaking change — clients requesting it get a validation
error and the whole operation fails. Introducing a breaking change to fix a
problem caused by the field's presence inverts the cost.

*Remove the Mutation root.* Not available. Six mutations work: `createProduct`,
`updateProduct`, `deleteProduct`, `registerMedia`, `completeMediaUpload`,
`deleteMedia`. A GraphQL service with working writes has a Mutation root. The
original test asserted the root's existence with no stated reason, which is how a
root cause that was never a question came to look like one.

### What was adopted

graphql.guide's answer to the underlying question — *build expected errors into
the schema, so clients know what to handle* — combined with graphql.org's
mechanism for a field that should not be used: `@deprecated` carrying a reason.

```graphql
createCategory(input: CreateCategoryInput!): Category!
  @deprecated(reason: "Cannot succeed: category.v1 exposes no write RPC, so this
   always answers DEPENDENCY_UNAVAILABLE. Write categories through the category
   service's own REST API, which is the only writer of that table.")
```

The fields stay, the root stays, and the schema itself now says which of its ten
mutations cannot succeed and where to go instead. That is discoverable through
ordinary introspection — where a client author looks — rather than only in a test
file or a README.

The test moved from a temporary guard to a decided contract: it asserts the
Mutation root exists *and names the six mutations that justify it*, and it
requires every capability-missing mutation to carry a `@deprecated` with a
non-empty reason.

---

## What this does not change

- The aggregator still owns no data and no database.
- `AUTHITECTURE.md` D-018 is untouched: one writer per row.
- Nothing here adds a dependency. The enforcement mechanism is still `go test`.
- The next category write RPC, when it exists, removes the deprecation. That is a
  schema change, not an architecture change.

## If one of these turns out to be wrong

Each decision names the observation that would reopen it:

| Decision | Reopens if |
|---|---|
| Ratchet, not allowlist | A hand-maintained allowlist appears somewhere that is *not* derived from the graph. |
| No upstream cache beyond a request | An owning service starts emitting `Cache-Control` or a cache-tag channel. |
| Category mutations deprecated | `category.v1` grows a write RPC, or the REST split is abandoned. |
