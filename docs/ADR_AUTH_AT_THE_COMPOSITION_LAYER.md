# ADR: Authentication at the composition layer

**Date:** 2026-10-15
**Status:** Accepted.
**Owner:** project owner.
**Supersedes:** the "Belum ada authentication" entry in
`internal/services/aggregator-service/README.md`.

## The problem

The aggregator had **no authentication of any kind**. Measured before this change:
zero occurrences of `Authorization`, `Bearer`, `JWT` or `OAuth` in any production
`.go` file across the repository.

That is not a gap in one service. It is a gap in the service that fronts the other
five, publishing port `8085:8085`, holding ten write mutations:

| Mutation | Capability |
|---|---|
| `createProduct`, `updateProduct`, `deleteProduct` | write the catalogue's primary entity |
| `createCategory`, `updateCategory`, `deleteCategory` | answer `DEPENDENCY_UNAVAILABLE` |
| `registerMedia` | **returns a presigned URL that lets the caller PUT arbitrary bytes into object storage** |
| `completeMediaUpload`, `deleteMedia` | flip media state and remove stored objects |

`registerMedia` is the sharpest one: it is an unauthenticated path to writing
objects into the store.

The service's own README had already identified the right place and the right
question, and left both open:

> composition layer adalah tempat yang paling logis untuk menambahkannya, karena
> satu titik menutup semua API. Mekanisme tokennya belum diputuskan.

So the first half was decided by the codebase and the second half was not. This
ADR decides it.

## The decision

**Reads are public. Writes require a bearer token. A configuration with no token
does not start.**

### Reads stay public

This is a product catalogue. Browsing, searching and reading a category tree are
what the service exists to make fast, and a credential on those paths imposes a
cost on every anonymous visitor to defend nothing.

The asymmetry is deliberate and it is tested: `TestQueriesRemainPublic` and
`TestIntrospectionRemainsPublic` fail if the gate is ever widened to reads.

### Writes require a token

Configured as `MUTATION_TOKENS`: a comma-separated list, each at least 16
characters, duplicates rejected.

A list rather than a single value because rotation is an operational requirement,
not a nicety — one token means rotating it cancels every caller at once.

### No token means no startup

This is the part worth arguing for, because it is the unusual choice.

The natural reading of an absent `MUTATION_TOKENS` is "no authentication
required". That reading is how an endpoint with ten write mutations becomes public
because someone removed an environment variable, rather than because anyone chose
it. `config.Load` therefore refuses to return a configuration without a token, and
names the variable in the error.

An operator who genuinely wants writes open must configure a token — which reads
differently in a deployment manifest from an absent variable.

## Where the check sits, and why not in middleware

"Does this request write?" cannot be answered from the HTTP method, because
`POST /query` carries both queries and mutations. It should also not be answered
by re-parsing the document in middleware: that parses every request twice, and the
parse is precisely the cost Automatic Persisted Queries exists to remove.

So the check runs at the operation layer, via gqlgen's `AroundOperations`. By then
gqlgen has parsed the document, resolved persisted queries, and selected the
operation by name — the operation type is known exactly rather than guessed. A
query that never reaches a write costs one integer comparison.

This placement also closes a hole that a body-inspecting middleware would leave
open. A client cannot register a mutation once as a persisted query and then
execute it by hash forever: `TestAMutationSentAsAPersistedHashStillRequiresAToken`
covers exactly that, and it passes only because the check runs after persisted-query
resolution.

## What the tests actually prove

Every rejection test asserts that **the write did not happen** — a counter in the
stub store — not merely that an error came back.

An error that looks authentic but arrives after the write has already landed would
pass any status-code check. Removing the `AroundOperations` registration turns
`TestMutationWithoutATokenIsRefusedAndDoesNotWrite` red with:

```
the write reached the domain layer 1 time(s); authentication is a control that
must stop the write, not merely report on it afterwards
```

Also covered: absent and invalid tokens are indistinguishable from outside (an
absent credential and a wrong one produce byte-identical client-visible errors, so
an attacker cannot learn which guesses are worth continuing); the token never
appears in a response body; the `Basic` scheme, a bare token, a token with a
trailing space and a lowercased scheme are all refused rather than silently treated
as anonymous; and the mutation root stays discoverable through introspection so a
client can learn what it needs a token for.

## Two things this is not

**It is not an identity system.** It answers one question — may this caller write —
and claims nothing more. Callers are identified by which token they present, not by
who they are. There is no per-caller authorisation and no audit of who changed
what. A real identity system belongs above this service and is deliberately not
faked here; the five services below this one have no authentication either, so
there is no identity here to delegate to.

**The constant-time comparison is not covered by a test, on purpose.** Timing a
comparison loop reliably needs either a statistical test with a large iteration
count or an AST check on `auth.go`'s own source, and both are worse than the gap
they close: a timing test that passes by luck teaches a reader the property is
verified when it is not. The residual risk — an attacker learning a token prefix
from response latency — is recorded in the source rather than papered over.

## Consequences

- **Every deployment must set `MUTATION_TOKENS`.** This is a breaking change for
  any existing deployment, and it is the intended effect: a deployment that has
  never chosen a credential should not start.
- **Callers must send `Authorization: Bearer <token>` on every write.** One header,
  at one place, which is the property the README claimed was missing.
- **`docker-compose.yml` sets a local development token in the clear.** It is
  labelled as not-a-secret so nobody mistakes it for a real one.
- **The five upstream services are still unauthenticated.** This closes the
  composition layer only. Anything that can reach them directly is still
  unauthenticated, which is the next problem, not this one.
