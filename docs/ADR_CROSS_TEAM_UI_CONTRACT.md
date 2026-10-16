# ADR — Cross-Team UI Composition Contract

**Status:** Accepted — implemented 2026-10-16
**Date:** 2026-10-15
**Decision owner:** architecture review, not the author
**Evidence base:** [`DECISION_UI_OWNERSHIP.md`](./DECISION_UI_OWNERSHIP.md)
**Supersedes:** nothing yet; narrows [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md) §2.5

---

## 1. Context

The app composition layer assembles features from several domain teams into one page — banner, recommendation and listing on the storefront home page. Three questions have to be separated before any rule can be written, because they have different owners and different failure modes:

1. **UI ownership** — the team owning a feature is responsible for that feature's UI *and its behaviour*.
2. **Page composition** — the app composition layer is responsible for which features appear on a page, in what order, and under what page context.
3. **Rendering contract** — whether the composition layer may import a *renderable* contract from another team, and whether that contract is stable enough to serve as an architecture boundary.

The third is the one that carries the risk, in both directions:

- Allowing free import of a feature's implementation makes its internals an implicit public API. The owning team can never refactor them, and the coupling is invisible because no contract was ever declared.
- Banning all cross-team UI contracts pushes the composition layer to reimplement feature behaviour locally — which silently transfers ownership of the feature to the layer that is only supposed to arrange it. That is the same failure as two authorities for one row, at the presentation layer.

So the ADR has to permit a *narrow* cross-team contract and forbid the rest, and it has to be able to tell "public contract" from "type that happened to be importable".

### 1.1 The distinction this ADR rests on

- **Public contract** — an API the owning team deliberately exposes for cross-team consumption, and whose compatibility it maintains.
- **Implementation interface** — an internal type or component that happens to be reachable, with no compatibility guarantee.

A consumer must depend on the first. In Go the two are already mechanically distinguishable, which is stronger than a naming convention elsewhere: an importable package path is not an API, and `internal/` is enforced by the compiler rather than by review.

---

## 2. The three open questions — answered from the codebase

These were flagged as undecidable without evidence. All three are answerable from this repository, so this ADR does not rest on assumption.

### 2.1 Framework and runtime — one, and it is server-side

| Check | Result |
|---|---|
| Rendering frameworks in `go.mod` | `github.com/a-h/templ` only |
| React / Vue / Preact / Svelte / Angular in `go.mod` or `package.json` | **none** |
| Client-side runtime | htmx — an attribute library over server-rendered HTML. No SPA runtime, no client component tree, no virtual DOM |

**Consequence.** single-spa's own guidance is that when all parts use the same framework, prefer ordinary components over parcels with explicit `mount`/`unmount` lifecycle. That condition holds, and it holds more strongly than single-spa's case: there is no client framework at all for a parcel to mount.

### 2.2 Deployment model — the feature teams are not independently deployable

| Check | Result |
|---|---|
| Go modules in the gateway | one (`module gogo`) |
| Binaries produced by `Dockerfile` | one (`ENTRYPOINT ["/local/bin/gogo"]`) |
| App containers in `docker-compose.yml` | one (`gateway`), plus `postgres` |
| `grpcx.Dial` call sites in runtime code | **exactly one** — `bootstrap/product_connection.go:57`, reaching the external product platform, which is a different repository and deployment |

**Consequence.** Within this repository a "feature team" owns a bounded context *inside one binary released together*. Independent deployment is not available to them, so a contract designed for independent deployment is solving a problem that does not exist here — and would be paid for in complexity with no return.

### 2.3 Lifecycle — there is no imperative DOM lifecycle to model

| Check | Result |
|---|---|
| `mount` / `unmount` / `createRoot` / `hydrate` in runtime code | **none** — the only matches are chi router mounts and unrelated comments |
| The only lifecycle interface | `module.Lifer{ Start(ctx) error; Stop(ctx) error }` — Go context-based, server-side, driven by the registry in registration order |
| Client-rendered content | `render.ClientIsland` ships a placeholder and a `FetchURL`; the browser fetches HTML, it does not mount a component |

**Consequence.** The lifecycle `Mountable` exists to model has no instance in this system. Its stated use case — a separate lifecycle, cross-framework interop, explicit mount and unmount — is absent on all three counts.

---

## 3. Options considered

| Parameter | A · Public component entry point | B · `render.Renderable` | C · `render.Mountable` |
|---|---|---|---|
| Ownership of the feature | Stays with the feature team | Stays, if the contract leaks nothing | Stays, if lifecycle and implementation belong to the provider |
| Simplicity | High | Medium | Low |
| Page composition control | Declarative | Declarative through a wrapper | Imperative |
| Special lifecycle needed | Follows the framework | Follows the framework | Must be specified explicitly |
| Coupling surface | Framework + props | Framework + props + an extra abstraction | Lifecycle + container + props + runtime |
| Governance load | Public component API | Public API plus abstraction rules | Public API plus lifecycle and cleanup |
| **Use case present in this repo?** | **Yes — §2.1, §2.2** | **No — it wraps `templ.Component` without removing a need** | **No — §2.3** |

### 3.1 Why Option A, not merely "start with A"

The usual reason to prefer A is simplicity. Here the stronger reason is that **B and C have no use case in this system**:

- **Option C** requires a lifecycle that does not exist, is aimed at cross-framework interop that does not exist, and is aimed at independent deployment that does not exist. Adding it would create an abstraction whose only purpose is uniformity — and uniform abstractions over unequal contracts add coupling without removing any.
- **Option B** would wrap `templ.Component` in a struct. `templ.Component` is *"an interface with a single function"* — `Render(ctx, io.Writer) error` — and is already the composition unit templ is built around, with `templ.Fragment(id)` for partial output and `shell.Page(title, inner)` for composition. A struct wrapping it adds a type and a construction site while the props problem stays exactly as it was, since each feature needs different props.

If a later fact changes this — a second runtime, independent deployment, or a real mount/unmount lifecycle — this ADR is the thing to revisit, and §7 records the triggers.

---

## 4. Decision

**1.** The app composition layer may import a feature's UI **only through a public entry point declared and maintained by the owning team**. Free import of internals is forbidden.

**2.** The owning team keeps full responsibility for its feature's UI implementation, behaviour, and evolution. Publishing a contract does not transfer ownership away from it.

**3.** The platform team owns the design-system primitives and the agreed platform contracts. Primitives are not a composition authority and never decide what appears on a page.

**4.** Page composition — which features appear, in what order, with what page context — is the app composition layer's decision. It is the only such decision, and no feature team gets it.

**5.** `Renderable` and `Mountable` are **not** adopted. Neither has a use case in this system today. If a need for either appears, it is a separate design decision with its own evidence, per §7.

**6.** A public contract carries its own obligations: integration tests, and a stated compatibility policy. Publishing something that is not tested makes the coupling permanent and invisible.

**7.** Any change to import rules, cross-team contracts, or lifecycle is a separate ADR reviewed on its own. It is never a rider on a feature.

### 4.1 Shape of the contract in this codebase

The platform unit is `templ.Component`. The public entry point should therefore be **an ordinary Go package that exposes components**, not a new interface type.

```go
// internal/modules/recommendation/public/public.go

// Package public is the cross-team surface of the recommendation feature.
// The recommendation team owns everything here and its compatibility; other
// units may import this package and nothing else under internal/modules/recommendation.
package public

// Component renders the recommendation section for a page.
func Component(ctx context.Context, w io.Writer) error
```

Enforcement follows from Go's own rules rather than from review:

| Zone | A page may import? | Enforced by |
|---|---|---|
| `<unit>/public` | yes | the compiler |
| `<unit>/domain`, `/application`, `/infrastructure`, `/presentation`, `/compose` | **no** | a test asserting the page's closure contains no such path |
| another page's `public` | no — pages do not compose pages | the existing rule 1 test, narrowed |

Because `internal/` is already a compiler-enforced boundary, the only thing that needs a test is the negative: *a page must not reach another unit's internals*. `internal/architecture/layers_test.go` already has the traversal; this is one more rule beside the existing ones.

**Naming.** The package is `public`, not `api` and not `contract` — because in Go the thing being published is a package others import, and `api` would invite request-shaped types into a package whose only job is to return components.

---

## 5. Consequences

**Positive**

- Feature ownership and page ownership become distinguishable, which is the point of the four-zone model and the thing the previous wording got wrong.
- Consumers depend on a declared surface; the owning team can refactor anything behind it.
- Cross-team change becomes an explicit review: changing a `public` package is a visible, diffable act, unlike changing a type that happened to be reachable.
- Composition stays a single decision in one place, which is what makes a page's layout attributable.

**Negative**

- A public API has to be maintained and tested, and someone has to own that cost.
- Contract changes need coordination, because the rule 1 change now permits a new edge between units that previously could not exist.
- Enforcement needs tooling. Without the test in §4.1, the rule is prose.
- A page can no longer reach a feature quickly, which is the friction that stops people adding "just one" import and silently creating a second authority.

---

## 6. What is explicitly out of scope

| Out of scope | Trigger to revisit |
|---|---|
| Slot / widget composition | More than ~15 teams, or a page needing 3+ teams' features on a stable cadence |
| Runtime-distributed components (module federation, CDN bundles) | Cross-team chrome that must update without a redeploy |
| Declarative page configuration (FlexiPage-style) or feature flags driving layout | Pages that must vary by tenant, experiment, or plan |
| Server-driven UI payload | Netflix's own scoping warning applies: *"Don't try to leak it into everywhere because server-driven UI is cool."* Nothing argues for it in a seller console |
| Independent deployment of features | A second runtime, or a decision that features release separately |

---

## 7. Open questions — re-opened only by a fact change

These were flagged as blocking. §2 answers them for today's codebase. They are recorded here as triggers, because a decision that names its own invalidating facts is durable in a way that one which pretends to be unconditional is not.

| Question | Answered | Fact that would reopen it |
|---|---|---|
| Do all features use the same runtime? | Yes — one, `templ`, server-side (§2.1) | A second rendering runtime, or any SPA framework introduced |
| Do features deploy independently? | No — one binary, one container (§2.2) | A decision that features release separately, or a second service host |
| Is there a lifecycle beyond the framework's? | No — none in runtime code (§2.3) | A real mount/unmount requirement, such as surviving a hot swap |
| Does the contract need to be a new interface type? | No — `templ.Component` is already it (§3.1) | A need to render features without the shared shell, or to bind props generically |

---

## 8. Enforcement — implemented

The rule change and its enforcement landed together, in the shape this section specified:

1. `internal/architecture/layers_test.go` — `TestCompositionLayerReachesNoModuleInternals` ("a page reaches no module internals") now forbids `domain`, `application`, `infrastructure`, `presentation` **and `compose`** in a page's closure. `public` is the one layer it does not forbid, so the grant is the absence of a prohibition rather than a special case.
2. `internal/architecture/architecture_test.go` — the old `pages must not import modules` rule was **narrowed, not deleted**. `pages import modules only through a module's public surface` (`moduleReachOffenses`) permits `<module>/public` and anything under it, and flags every other module package — the module root of a flat module included. `pages must not import other pages` (`pageCompositionOffenses`) covers the last row of the §4.1 table. Both failure messages name this ADR.
3. Module-to-module `public` was **not granted**: `modules must not import other modules` (`crossModuleOffenses`) is unchanged in scope, and its failure message now says so explicitly. Rule 3 and its event-based reasoning stand.
4. `ARCHITECTURE.md` rule 1 states the narrowed rule and the `public` exception; the decision log carries [D-021](../ARCHITECTURE.md#d-021), which records the ownership split, the pages-only scope, and the implementation.

Because no `public` package exists yet — none is added speculatively, and the first is extracted with its first real consumer — the real-graph tests exercise only the negative half. The predicates are therefore additionally pinned on synthetic graphs in `internal/architecture/ui_contract_test.go`: the `public` grant, each forbidden layer, the transitive-taint rule (a `public` package that reaches its own module's internals fails every page that imports it), the `publicity`-style prefix trap, and module-to-module `public` refusal in both directions.

**The trade, restated for the record:** item 2 replaced one enforced invariant ("no module import at all") with a narrower one ("no module import except `public`"). The reduction is real. It is the price of a cross-team UI contract that keeps feature ownership with the feature team, and the fixtures are what make the narrowed invariant as verifiable as the old one.

---

## 9. References

**Ownership and interaction**
[Team Topologies — key concepts](https://teamtopologies.com/key-concepts) · [interaction modelling](https://teamtopologies.com/key-concepts-content/team-interaction-modeling-with-team-topologies) · [cognitive-load heuristics](https://itrevolution.com/articles/minimize-cognitive-load-of-teams/) · [Fowler on TT](https://martinfowler.com/bliki/TeamTopologies.html) · [Conway 1967](https://melconway.com/Home/Conways%5FLaw.html)

**Composition and coupling cost**
[Jackson, Micro Frontends](https://martinfowler.com/articles/micro-frontends.html) · [microfrontend.io patterns](https://microfrontend.io/patterns/) · [Booking.com — page ownership and sections from other teams](https://medium.com/booking-com-development/node-js-at-scale-rebalanced-how-we-cut-cost-by-38-62ce247a3002) · [Agoda — module ownership, no module→module dependency](https://medium.com/agoda-engineering/breaking-the-monolith-f3538d9c3ad6) · [Airbnb — feature modules not visible to each other, feature-interface sibling](https://medium.com/airbnb-engineering/designing-for-productivity-in-a-large-scale-ios-application-9376a430a0bf) · [Blibli — Main Layout decides what loads on which page](https://medium.com/bliblidotcom-techblog/micro-frontends-arsitektur-di-blibli-com-4063f639055d) · [Shopify — the platform defines where an app can appear](https://shopify.dev/docs/apps/build/app-surfaces) · [What broke at 30+ apps on one design system](https://sordyl.dev/blog/design-system-pitfalls/)

**Contract shape and lifecycle**
[single-spa — recommended setup](https://fr.single-spa.js.org/docs/recommended-setup/) (prefer import maps; ordinary components over parcels when the framework is shared) · [single-spa — parcels overview](https://single-spa.js.org/docs/parcels-overview/) · [a-h/templ — `templ.Component` and `templ.Fragment(id)`](https://pkg.go.dev/github.com/a-h/templ@v0.3.1020) · [htmx — responses are HTML fragments](https://four.htmx.org/docs/core-concepts/requests-and-responses.md)

**Go enforcement**
Go `internal/` package visibility is specified in the [Go modules reference](https://go.dev/ref/mod#visibility) and enforced by the compiler; [Go FAQ — accept interfaces, return structs](https://www.gofaq.org/en/accept-interfaces-return-structs) for the consumer-declares-interface rule this codebase already follows.
