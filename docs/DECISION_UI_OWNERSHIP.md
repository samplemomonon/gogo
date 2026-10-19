# Decision Record — UI Ownership Boundary Between the Composition Layer and Feature Bounded Contexts

**Date:** 2026-10-15
**Status:** Decided
**Supersedes:** the four-zone model in [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md) §2.5, which was wrong.
**Method:** every decision below cites named systems with published evidence. Where no evidence exists, that is said explicitly rather than filled with an opinion.

---

## 0. Why this record exists

The PRD's four-zone model stated that a page "knows **one** context's vocabulary in depth and **no** other context's routes." That sentence described a constraint the code happens to have, and presented it as a design principle. It is not a principle — it is an accident of there being no cross-unit composition mechanism at all.

The PRD also cited "Nick Tune — vertical slices & macro-architecture." **That series does not exist.** Two of Tune's archives were enumerated (nick-tune.me/blog, 211 posts; nicktune.substack.com, 11 posts); no such post is present. The real, citable Tune piece is *Architecture Ownership Patterns for Team Topologies Pt 2* (Dec 2020), which is a **team-ownership taxonomy**, not a frontend macro-architecture argument. "Vertical slices" is Dudycz/Bogard's term. The citation has been removed.

---

## 1. What the evidence actually says

Across every named system with a published frontend architecture, four findings are consistent and none is a minority position.

### 1.1 The page — not the component — is the unit of ownership

> "Each major page has its own Node rendering service, **owned by the team responsible for that page** … **The page itself is assembled from sections owned by different teams.**"
> — Booking.com, 70+ rendering services. [STRONG — first-party Medium on their engineering blog](https://medium.com/booking-com-development/node-js-at-scale-rebalanced-how-we-cut-cost-by-38-62ce247a3002)

Agoda, verbatim: *"Module belongs one team-owner"*; *"Module don't have dependency on any another Module"* — [STRONG, first-party](https://medium.com/agoda-engineering/breaking-the-monolith-f3538d9c3ad6).

Blibli — the closest Indonesian analogue, in their own words: *"sebagai orkestrator kami membuat Main Layout yang bertugas **menentukan aplikasi mana yang harus dimuat ketika berada di halaman tertentu**"* ("as orchestrator we built a Main Layout whose job is to decide which application is loaded on which page") — [STRONG, first-party](https://medium.com/bliblidotcom-techblog/micro-frontends-arsitektur-di-blibli-com-4063f639055d).

### 1.2 The composition layer owns **arrangement**, never **content**

Booking (the page team), Agoda (WebGate + a shared Bootstrap Razor Layout), Blibli (Main Layout), Atlassian (the host page passes props to 50+ "apps": *"we made each of those Apps or features a separate, entirely separate application with just props as the API between them"* — [STRONG](https://conffab.com/presentation/scaling-frontend-development/)), Shopify (*"App Bridge and the Shopify admin **own those integrations**"* — [STRONG](https://shopify.dev/docs/apps/build/app-home/migrate-from-polaris-react/frame)).

**No named system gives the feature team the placement decision.** Four different instantiation styles were found — page-owning team, dedicated platform team, declarative configuration, the server — and the answer is the same in all four.

### 1.3 Cross-team composition crosses a narrow, declared interface

- **Airbnb (iOS):** *"Feature modules are **not visible to other feature modules** (i.e. a feature module **cannot depend on a feature module**); however, they share lightweight types via a sibling module type called a **feature interface**."* Enforced by Buck visibility rules. [STRONG — named company engineering blog](https://medium.com/airbnb-engineering/designing-for-productivity-in-a-large-scale-ios-application-9376a430a0bf)
- **Salesforce FlexiPage:** regions with widths, components gated by `isExposed` and `targets`. [STRONG](https://developer.salesforce.com/docs/atlas.en-us.api_meta.meta/api_meta/meta_flexipage.htm)
- **Shopify surfaces:** *"**Shopify defines where an app can appear**, and each surface comes with its own extension types, APIs, and design rules."* [STRONG](https://shopify.dev/docs/apps/build/app-surfaces)

### 1.4 Slot/widget composition pays off **only at a scale this repository is not at**

Booking needed 70+ rendering services plus module federation. Atlassian needed 50+ apps, 400k LOC, ~200 committers, a merge queue, and *Stricter* (a project-level linter, because *"ESLint rules can see the file by file basis, Stricter rules can see the whole sort of project structure"*). Agoda needed a CDN bundle plus Consul. Blibli needed an nginx proxy plus one Vue instance per repo plus a shared Vuex store from the orchestrator.

The most useful published failure is a frontend platform team's own account of what broke at 30+ apps consuming one design system: no contribution model, no visual tests, and components *"went through review, but each one grew a prop per consumer variant."* Their conclusion: *"At 30+ apps, I'd take it with these flaws over no DS"* — and that was about a **design system**, a far cheaper dependency than cross-team composition. [MEDIUM-STRONG](https://sordyl.dev/blog/design-system-pitfalls/)

**gogo today:** 7 modules, 4 pages, 104k lines, one team.

---

## 2. Decision 1 — Composition model: route ownership, with import rule 1 narrowed

**Decision.** A route has one owning team. That team composes the page. Cross-team UI on that page is permitted only through the other team's **public entry point**, never through its internals. The exact shape of that entry point is specified in [`ADR_CROSS_TEAM_UI_CONTRACT.md`](./ADR_CROSS_TEAM_UI_CONTRACT.md) §4.1 — an ordinary Go package, not a new interface type.

**Usecase satisfied.** A page is one user job. `home` = *browse a shop*, `seller` = *ring up a sale*, `profile` = *manage an account*. None of them is "everything on the storefront", and pretending otherwise is how a page becomes a dumping ground.

**Trade-off accepted.** Adding a feature to an existing page requires a PR to the page-owning team. That is a coordination cost, and it is the cost Booking.com's model is built on.

**Consequence.** A new feature team's first release is cheap (build the feature and its own route). A second team's feature on that page is a negotiated change. That ordering is correct: the expensive case should not be the common one.

**Explicitly not built.** Slot composition, widget layers, island registry, `runtime-distributed` components, or a plugin/extension marketplace. The evidence above puts the break-even for those well above this repository's size, and the two published enterprise post-mortems both point the same way: the composition problem gets *wider* as you add teams, not easier.

**Correction to this record's own framing.** An earlier draft described the needed rule change as requiring "a new shared type". That was an assumption, not a finding — the review it went through established that `templ.Component` already *is* the composition unit and that no imperative lifecycle exists to model. See §8 and the ADR.

**Why the store-front example in the brief does not force a redesign.** The brief's example — a storefront home page consuming product-listing, banner and recommendation features — is exactly Booking.com's property page. It is a **real** pattern, and it is reached by *page ownership plus narrow interfaces*, not by a slot registry. The difference from today is only that gogo's pages cannot yet import another team's renderable interface, which is Decision 1's one rule change.

---

## 3. Decision 2 — Who decides which features appear on a page: the page-owning team

**Decision.** The page-owning team decides. Feature teams do not get this decision.

**Evidence.** Unanimous across all four instantiation styles in §1.2. Note the Shopify nuance in full: the deciding party is the *merchant*, not the app team — *"Merchants have the option to add your app block to a page, and arrange it in the page layout."* Even there, the feature team is not the decider.

**Trade-off accepted.** Adding a feature to a page owned by another team needs their agreement.

**Consequence.** Change to page composition is attributable — there is exactly one team to ask. This is what the Team Topologies interaction-mode table should say for the page↔feature pair: **collaboration to agree the placement, then X-as-a-Service for the interface.**

**Deliberately deferred.** The declarative-configuration variant (Salesforce FlexiPage, Shopify admin) and the flag-driven variant (LaunchDarkly, Unleash) are both enterprise answers to a problem gogo does not have yet: pages whose content varies by tenant, experiment, or plan. Unleash explicitly advises *against* using flags for static configuration — *"Don't use feature flags with configuration that is static or rarely changes."* Revisit when a page must vary by tenant.

---

## 4. Decision 3 — Render strategy: per-feature declaration, with a **structural** ceiling

**Decision.** A feature declares its own rendering contract per island. The platform supplies the mechanism and a ceiling. **The ceiling must be enforced structurally, not documented.**

**What the code already does — correctly.** `render.Island(name, body, contract)` already takes a per-island `RenderingContract` (`Mode`, `CacheTTL`, `Public`, `Personalized`, `RequiresSEO`, `Freshness`), and `ContractPlanner` maps that contract to a decision. This is **per-feature render strategy selection, and it already works.** The PRD was wrong to treat render strategy as a platform-level policy.

This matches Next.js Cache Components, which states the same model: *"Data fetching is dynamic by default, and you choose what to cache at the page, component, or function level."* [STRONG](https://nextjs.org/docs/app/api-reference/config/next-config-js/cacheComponents)

**What the evidence says the ceiling must be.** Astro's `routeRules` model is centrally owned with precedence-ordered patterns and **no per-component opt-out** — a team cannot unilaterally change a page another team owns. That is too strict for gogo, because it contradicts the brief's requirement that a feature picks its strategy per use case.

Next.js's model is the better fit, and its enforcement mechanism is the part to copy. Next.js makes caching safety **impossible to violate** rather than forbidden:

> A `'use cache'` scope *"must not"* read `cookies()` or `headers()` — *"To use cookies or headers, read them outside cached scopes and pass values as arguments"*, or opt into `'use cache: private'*.
> [STRONG](https://nextjs.org/docs/app/api-reference/directives/use-cache)

**Verified current state in this repository.** `Composer` already enforces the equivalent as a condition:

```go
cachable := c.cache != nil && component.Contract.Public && !component.Contract.Personalized
```

and `islandKey` is **tenant-qualified**, so a `Public` island is cached per tenant and a `Personalized` island is never cached at all. **There is no cross-user data leak today.** — [STRONG, verified in `internal/shared/web/render/composer.go:203,285`]

**The gap.** It is a *cooperative* condition at each call site, not a structural impossibility. A future render path that skips `renderCached` would silently cache personalized HTML. Next.js closed that hole by making the unsafe operation unavailable inside a cached scope.

**Two-tier enforcement, after Vercel Conformance's published pattern** — hard `Prevent` for things with no legitimate use, `review and approve` for things sometimes necessary. [MEDIUM — vendor docs](https://vercel.com/docs/conformance/rules)

| Tier | Rule | Enforcement |
|---|---|---|
| **Prevent** | A cached island must not read session, tenant override, or any `Personalized` input | Structural — make the cached render path unable to reach the request context |
| **Prevent** | A cached island must declare `Public: true` | Already the condition above; keep it and add a test per island |
| **Review** | A feature declaring `ModeStatic` for data that changes | CODEOWNERS on the declaration site — "review and approve", not a ban |
| **Review** | A feature widening its own cache TTL | CODEOWNERS on the declaration site |

**Trade-off accepted.** A team cannot unilaterally make a page fast at the cost of correctness, but it also cannot be blocked from doing so — it gets a review, which is a conversation rather than a wall.

**Consequence.** The ceiling becomes checkable in CI, and the "review" tier needs `CODEOWNERS` to exist at all. That is a dependency on PRD §12 Phase 3, and it is the concrete reason to do it.

---

## 5. Decision 4 — Cache keys: structural namespace, feature declares its own key

**Decision.** A feature owns the cache key of any island it contributes. A page may not invent another team's key. The composer namespaces the key under the page/tenant it renders in.

**Evidence — and an honest negative result.** **No frontend framework publishes a cache-key-per-team convention.** Next.js `cacheTag()` is a flat global string with no namespace, no owner, and no collision detection — `revalidateTag('x')` invalidates *every* entry carrying that string, from any team. [STRONG](https://nextjs.org/docs/app/api-reference/functions/cacheTag) · The documented Next.js bugs in this class are all **over**-invalidation, e.g. *"revalidateTag and revalidatePath clears cache of all tags located at the same path"* [#63509, team-labelled](https://github.com/vercel/next.js/issues/63509).

So the convention must come from elsewhere. Three transferable models, in descending order of strength:

| Model | Mechanism | Source |
|---|---|---|
| **Structural namespace** | Keys are namespaced by a *pool*, not by a string convention — *"prefixing keys with a namespace that's generated by hashing the name of the pool"* | [STRONG — Symfony](https://symfony.com/doc/current/cache.html) |
| **Two-tier keys** | Granular per-entity keys *plus* a roll-up key — *"Prefer granular tags (`product-${id}`) plus a roll-up tag"*; *"A coarse tag attached to thousands of paths has a large blast radius"* | [MEDIUM — Vercel](https://github.com/vercel/vercel-plugin/blob/main/skills/cdn-caching/SKILL.md) |
| **Enforced naming at creation** | Project-level regex, non-compliant names **rejected**, keys immutable — `^(red|blue|green).[a-z-]+.[0-9]+$`, shape `<team>.<feature>.<ticket>` | [STRONG — Unleash](https://docs.getunleash.io/guides/organize-feature-flags) |

The transferable lesson is the same in all three: **a string convention decays; a structural namespace does not, and an enforced pattern beats a documented one.** LaunchDarkly's published collision story is two teams each believing one flag was theirs — *"imagine having a room with two light switches that control the same light"* — and their fix was a *project-enforced prefix applied to API writes*, not a wiki page. [STRONG](https://launchdarkly.com/docs/home/flags/proj-flag-settings)

**Verified current state.** `Composer` already holds its own cache and its keys are tenant-qualified via `islandKey`. Structurally the namespace already exists. What is missing is the **rule**: `internal/features/home/ui/islandkeys/islandkeys.go` hardcodes `HomeGrid = "home.product-grid"`, which is correct today because home owns the only island on the home page, and wrong the moment another team contributes one.

**Trade-off accepted.** An island key becomes a cross-team contract string and cannot be renamed casually.

**Consequence.** Follow Unleash, not a wiki page: the key's prefix is **validated in CI**, not documented. Concretely — the island name must be `owner/island` or carry an explicit owner segment, and the test fails on an unnamespaced key. That is decidable automatically because `render.Island(name, …)` is the single construction site.

---

## 6. Correctness defects found while researching, fixed independently

These are htmx specification violations, not design decisions. They are fixed here because they are unambiguous, primary-source-backed, and independent of every decision above.

### 6.1 `Vary: HX-Request` was never set

htmx is explicit: *"if your server renders the full HTML when the `HX-Request` header is missing or `false`, and it renders a fragment of that HTML when `HX-Request: true`, you need to add `Vary: HX-Request`. That causes the cache to be keyed based on a composite of the response URL and the `HX-Request` request header."* [STRONG — htmx docs source](https://github.com/bigskysoftware/htmx/blob/master/www/content/docs.md)

Verified: no `Vary` header is set anywhere in gogo's own code. With a shared cache in front of the gateway, one response variant can be served for the other. RFC 9110 §12.4.1 makes the absence of a field meaningful, so `Vary` is correct even when the header is missing — there is a documented objection on ergonomics grounds in [htmx #1229](https://github.com/bigskysoftware/htmx/issues/1229), and htmx 4's new `Accept: text/html` on core requests is a move toward the other camp. Mitigation if it ever matters: echo a sentinel and vary on that.

### 6.2 `HX-Request-Type` and `hx-boost` were not handled

`IsHTMX` checked only `HX-Request == "true"`. htmx 4 documents: *"**`HX-Request-Type`** is new and carries `"partial"` or `"full"`"*, and — critically — *"`hx-boost` implicitly sets `hx-target="body"`, so boosted navigation arrives as `HX-Request-Type: full`"*, which must be answered with a full document. [STRONG](https://four.htmx.org/docs/whats-new-in-htmx-4) · [STRONG](https://four.htmx.org/reference/headers/HX-Request-Type)

Consequence before the fix: an `hx-boost`-ed link would have had a **fragment injected into `<body>`** — a visibly broken page.

Related, documented footgun: a history-restore request arrives with `HX-History-Restore-Request: true` and *"expects the HTML needed for the entire page"*, so history restore is a navigation even though it is technically an htmx request.

### 6.3 Not a security issue, verified anyway

`ModeStatic` on a `Personalized` island cannot leak across users: the read path is gated on `Public && !Personalized`, and the key is tenant-qualified. Recorded so that a later change to the render path does not assume this is safe by accident.

---

## 7. What was ruled out, and the trigger to revisit

| Ruled out | Trigger to revisit |
|---|---|
| Slot / widget composition | More than ~15 teams, or a page needing features from 3+ teams **on a stable cadence** |
| Runtime-distributed components (module federation / CDN) | Cross-team chrome that must update without a redeploy — Booking's header/footer case |
| Declarative page configuration (FlexiPage-style) | Pages that must vary by tenant, experiment, or subscription plan |
| A server-driven UI payload | Netflix's own scoping warning applies: *"Don't try to leak it into everywhere because server-driven UI is cool."* Nothing here argues for it in a seller console |
| A per-component cache policy *enforced by lint alone* | Never. Vercel Conformance's two-tier design exists because a single tier does not work |

---

## 8. The rule change this record implied — resolved separately

**Resolved in [`ADR_CROSS_TEAM_UI_CONTRACT.md`](./ADR_CROSS_TEAM_UI_CONTRACT.md).** Not bundled here, because a rule change must be reviewed on its own rather than as a rider on a feature.

That record also **withdraws a suggestion in this file's earlier draft**: the idea that the rule change "requires a new shared type (`render.Mountable` or equivalent)" was wrong on two counts, both verifiable from the codebase rather than arguable:

- **`templ.Component` is already the composition unit** — an interface with a single `Render(ctx, io.Writer) error` — so the public contract is an ordinary Go package, not a new interface type.
- **There is no lifecycle for `Mountable` to model.** `mount`/`unmount`/`createRoot`/`hydrate` appear nowhere in runtime code; the only lifecycle interface is `module.Lifer{Start, Stop}`, which is server-side and Go-context-based.

An abstraction introduced for uniformity, over contracts that are not uniform and a lifecycle that does not exist, adds coupling and removes nothing. The ADR compares all three shapes against what is actually present, and does not adopt any of them.

---

## 9. Sources

**Composition and ownership**
[Booking.com — Node.js at scale](https://medium.com/booking-com-development/node-js-at-scale-rebalanced-how-we-cut-cost-by-38-62ce247a3002) · [Booking — modernising from the inside out](https://medium.com/booking-product/how-we-managed-to-modernize-the-booking-com-app-from-the-inside-and-out-ed24f0e6df0a) · [Booking — the necessity of imperfection](https://medium.com/booking-com-development/the-necessity-of-imperfection-21c289c87515) · [Agoda — breaking the monolith](https://medium.com/agoda-engineering/breaking-the-monolith-f3538d9c3ad6) · [Blibli — micro-frontends](https://medium.com/bliblidotcom-techblog/micro-frontends-arsitektur-di-blibli-com-4063f639055d) · [Atlassian — scaling frontend development](https://conffab.com/presentation/scaling-frontend-development/) · [Atlassian cloud overview](https://www.atlassian.com/blog/how-we-build/cloud-overview) · [Atlassian Compass component ownership](https://support.atlassian.com/compass/docs/assign-a-component-owner/) · [Shopify app surfaces](https://shopify.dev/docs/apps/build/app-surfaces) · [Shopify app structure](https://shopify.dev/docs/apps/design/app-structure) · [Shopify shell ownership](https://shopify.dev/docs/apps/build/app-home/migrate-from-polaris-react/frame) · [Airbnb — large-scale iOS module visibility](https://medium.com/airbnb-engineering/designing-for-productivity-in-a-large-scale-ios-application-9376a430a0bf) · [Airbnb — Ghost Platform SDUI](https://medium.com/airbnb-engineering/a-deep-dive-into-airbnb-s-server-driven-ui-system-842244c5f5) · [Airbnb — frontend rearchitecture](https://medium.com/airbnb-engineering/rearchitecting-airbnb-s-frontend-5e213efc24d2) · [Wayfair — applications instead of libraries](https://www.aboutwayfair.com/careers/tech-blog/applications-instead-of-libraries-part-1) · [HubSpot frontend platform charter](https://product.hubspot.com/blog/frontend-platform) · [Gojek — why we stayed multi-repo](https://medium.com/gojekengineering/stuck-with-a-multi-repo-here-are-tips-to-re-evaluate-codebase-structure-ba82ae80b84e) · [Tokopedia — GraphQL for a multi-tribe page](https://ihsansatriawan.github.io/blog/2019/06/how-tokopedia-lite-use-graphql/) · [Traveloka — three-tier UI ownership, no shared](https://medium.com/traveloka-engineering/the-engineering-of-a-design-system-cdede29c58b6) · [Netflix — server-driven UI scoping](https://www.infoq.com/presentations/server-ui-mobile/) · [Netflix Tudum architecture](https://netflixtechblog.com/netflix-tudum-architecture-from-cqrs-with-kafka-to-cqrs-with-raw-hollow-86d141b72e52) · [What broke at 30+ apps on one design system](https://sordyl.dev/blog/design-system-pitfalls/) · [Why federated design systems keep failing](https://53degrees.studio/posts/why-federated-design-systems-keep-failing/)

**Spec and mechanism**
[RFC 9110 §12 Content Negotiation](https://www.rfc-editor.org/rfc/rfc9110.html) · [RFC 5861 stale-while-revalidate](https://www.rfc-editor.org/rfc/rfc5861.html) · [htmx HX-Request](https://four.htmx.org/reference/headers/HX-Request) · [htmx HX-Request-Type](https://four.htmx.org/reference/headers/HX-Request-Type) · [htmx 4 changes](https://four.htmx.org/docs/whats-new-in-htmx-4) · [htmx docs source (`Vary`)](https://github.com/bigskysoftware/htmx/blob/master/www/content/docs.md) · [htmx #1229 — contested `Vary`](https://github.com/bigskysoftware/htmx/issues/1229) · [Hotwire Turbo Streams](https://turbo.hotwired.dev/handbook/streams) · [templ Fragment](https://github.com/a-h/templ/blob/main/fragment.go)

**Render governance and cache ownership**
[Next.js Cache Components](https://nextjs.org/docs/app/api-reference/config/next-config-js/cacheComponents) · [`use cache`](https://nextjs.org/docs/app/api-reference/directives/use-cache) · [how revalidation works](https://nextjs.org/docs/app/guides/how-revalidation-works) · [`cacheTag`](https://nextjs.org/docs/app/api-reference/functions/cacheTag) · [Next.js #63509 — over-invalidation](https://github.com/vercel/next.js/issues/63509) · [Astro route caching and precedence](https://docs.astro.build/en/guides/caching/) · [Astro server islands](https://docs.astro.build/en/guides/server-islands/) · [Astro route caching proposal non-goals](https://github.com/withastro/roadmap/blob/main/proposals/0056-route-caching.md) · [Vercel Conformance rules](https://vercel.com/docs/conformance/rules) · [Vercel CDN-caching guidance](https://github.com/vercel/vercel-plugin/blob/main/skills/cdn-caching/SKILL.md) · [Symfony namespaced cache pools](https://symfony.com/doc/current/cache.html) · [Symfony HTTP cache / ESI](https://symfony.com/doc/current/http_cache.html) · [Piranha — reducing feature flag debt at Uber (ICSE-SEIP 2020)](https://www.computer.org/csdl/proceedings-article/icse-seip/2020/712300a221/1pov0yPiIPm) · [Unleash — enforced flag naming](https://docs.getunleash.io/guides/organize-feature-flags) · [LaunchDarkly — project flag settings](https://launchdarkly.com/docs/home/flags/proj-flag-settings) · [Nx code ownership](https://nx.dev/docs/kb/code-ownership) · [Nx module boundaries](https://nx.dev/docs/features/enforce-module-boundaries)
