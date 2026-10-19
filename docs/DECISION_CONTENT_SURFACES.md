# Content surfaces: ownership, business rules, and the build order

**Status:** decided (2026-10-19); §5 step 2 is implemented and verified, and
§5 steps 1 and 3 are verified offline — their external halves (the Builder.io
project; legal content and the publication host) await their owners.
**Scope:** editorial CMS pages, banners, engineering documentation, and the
customer legal pages (terms & conditions, privacy policy).
**Companions:** [`CMS_CONTENT_MODEL.md`](./CMS_CONTENT_MODEL.md) (the read
contract), [`DECISION_PRODUCT_METADATA.md`](./DECISION_PRODUCT_METADATA.md)
(headless CMS for banners and editorial content; Rspress for developer docs),
[`DECISION_UI_OWNERSHIP.md`](./DECISION_UI_OWNERSHIP.md) (arrangement belongs to
the page, content to the content owner),
[`DECISION_TEAM_OWNED_VERTICALS.md`](./DECISION_TEAM_OWNED_VERTICALS.md) (the
vertical model), [`ADR_FRAGMENT_DELIVERY_AND_MEASUREMENT.md`](./ADR_FRAGMENT_DELIVERY_AND_MEASUREMENT.md)
§6 (the enforced unit catalogue, `team_catalog.json`), and
[`../ARCHITECTURE.md`](../ARCHITECTURE.md) (D-023, D-024).

This record answers one question: when someone says "make a CMS / banner / docs /
terms feature — with a team and business rules" — what does that mean in this
repository? Every content surface gets an owner, a rule set, and a build order,
so the answer stops being re-derived per feature.

## 0. How ownership is expressed here

Not an org chart. A **vertical** is the ownership unit (D-023): one directory
`apps/web/internal/features/<f>` holding the backend and the UI that ship
together, an `OWNERS.md` naming who answers for it, an entry in the ownership
catalogue (`apps/web/internal/architecture/team_catalog.json`), and an
architecture test that keeps it closed to other verticals. A team without a
vertical is a review habit; a vertical without a team is an orphan. New verticals
come from `make feature <name>` — and only when a capability has a genuinely
independent owner, lifecycle, or consumer ("extract with consumer"). The
repository deliberately carries no organisational data, so a `CODEOWNERS` is
created only when a real owner is declared.

Two rules from `DECISION_UI_OWNERSHIP.md` apply to every row below:

- **Content belongs to the content owner; arrangement belongs to the page
  team.** Wiring a banner slot into the storefront home is `home`'s change; what
  the banner says is `contentcms`' data.
- **No slot registry.** A page composes what it needs, through a narrow
  interface.

## 1. The map

| Surface | Owner | Where it lives | Runtime? |
| --- | --- | --- | --- |
| Editorial pages (`/pages/{slug}`) | `contentcms` vertical | `internal/features/contentcms/`; source: Builder.io; contract: `CMS_CONTENT_MODEL.md` | Yes — the gateway reads published content |
| Banners (editorial) | Content: `contentcms`. Placement: the page that renders the slot (`home`) | block type `hero`/`banner`; `design-system` `HeroBanner` | Yes |
| Engineering documentation | The repository — reviewed in Git | `docs/**`, `README.md` | No — build-time |
| Customer legal pages (terms, privacy) | Facts: the service owner + legal (outside the repository). Build: the repository | `docs/site/` (Rspress) + `check-publication.mjs` gate | No — build-time |
| Developer documentation site / demos (if ever added) | The repository | Rspress, build-time only (D-024) | No |

One deliberate absence: **there is no runtime vertical for legal pages.** Legal
text is versioned, approved, and diff-reviewable in Git; moving it behind an
external editor would remove the diff and the gate. The editorial CMS and the
legal site do not merge.

## 2. Business rules (locked today)

**Editorial CMS (read contract).** All of `CMS_CONTENT_MODEL.md` is the rule
set. In summary: published content only; a closed block list (`hero`/`banner`,
`markdown`, `faq`, `cta`); no raw HTML, JavaScript, or iframes; image and link
URLs must be HTTPS or same-origin; per-request timeout, 1 MiB body cap, a 60 s
cache of at most 64 slugs, and the circuit breaker apply; a fetch failure is a
logged 503 and **never an empty success page**; with no CMS configured the route
404s (kill switch). CMS content is presentation data — it can never write or
override product data.

**Authoring (who may edit).** Platform editorial administrators manage the CMS
project and editor roles in Builder.io. Roles in the gateway — seller, merchant,
buyer — do not grant CMS access. The gateway holds a public read key only; no
write credential exists in this repository or its deploy configuration.

**Banners — today's scope: editorial only.** A banner is a published CMS block,
placed by the page that owns the slot. There is no scheduling, ordering,
audience or tenant targeting, or impression/click tracking. If any of that is
needed, it is new work under §4 — not a quiet widening of this one. The one live
slot today is the storefront home's `home-banner` slug (§5 step 2).

**Banner failure semantics.** The home page keeps serving when the CMS is down
or a slot is absent: the slot degrades to rendering nothing (no empty frame, no
error page), and the failure is logged. This mirrors the storefront rule that
browse keeps serving while a read upstream is down. (Contrast: an editorial
page's own URL returns 503 on failure — its content *is* the page.)

**Legal pages.** Publication is gated, not editorial: `publicationStatus:
approved` plus `documentOwner`, `version`, `effectiveDate`, and
`approvalReference`; the draft instructions removed; the values real. The
repository cannot invent the legal entity, the data flows, or contractual
policy — those arrive from the service owner and legal, and the build blocks
until they do.

**Engineering documentation.** Authored and reviewed in Git, built separately
from the customer legal pages and from any editorial CMS. No application code
imports the docs site (D-024).

## 3. Why not more "teams"

- A *banner team* today would own one block type and nothing else: no
  independent lifecycle, no deployment boundary, no second consumer. That is a
  review convention, not a vertical. When the merchandising triggers in §4
  fire, the vertical comes with them.
- A *docs team* has no runtime artifact to own; the Git review is the
  governance.
- `contentcms` already owns the editorial read path end to end — contract,
  adapter, renderer, routes. Splitting it now would put one page behind two
  owners.

## 4. Deferred: merchandising triggers (do not build speculatively)

If banners ever need any of the following, that is the trigger to scaffold a
merchandising capability — a new vertical with its own rules — not to grow
scheduling logic inside `contentcms`:

- scheduled visibility (start/end, auto-expire) and ordering/priority;
- tenant or audience targeting (and therefore per-tenant content storage,
  isolation, and RBAC);
- impression/click tracking (an event contract and its consumers).

Its rule set will need at least: conflict/priority resolution, time-zone and
clock semantics, empty-slot fallback, and the isolation model. None of that is
decided here because none of it has a consumer yet.

## 5. Build order (each step has an exit condition)

1. **Enable and verify the read path.** Create the Builder.io model per
   `CMS_CONTENT_MODEL.md`; set `BUILDER_PUBLIC_API_KEY` and
   `BUILDER_CONTENT_MODEL`; obtain the approved egress path; publish one real
   page. *Exit:* the published page renders at `/pages/{slug}`; unsetting the
   two variables returns 404; a forced CMS failure returns 503, not an empty
   page. **Verified offline (2026-10-19):** the 404 and 503 exits, the
   documented request shape, the response parse, the error taxonomy, and the
   cache bounds are pinned by tests in `internal/features/contentcms/ui`; the
   live published page still requires the project and the approved egress path.
2. **Banner slot in the storefront home.** `home` declares a content port for a
   named slot; the composition root adapts the `contentcms` reader into it (the
   same shape as the existing cross-vertical wiring in
   `internal/shell/*_connection.go` — no vertical imports another, so the
   closed-module rule stays intact). If the slot ever needs to be a *renderable*
   cross-team surface — `contentcms` shipping its own banner component — that is
   the D-021 `public/` grant's trigger, decided then. *Exit:* a published banner
   renders on home; absence or CMS failure degrades to no banner while home
   still serves; tests pin all three paths; `import_graph.txt` regenerated
   (`make arch-graph`) and `make check` green. **Done (2026-10-19):** the port is
   `home/ui/application/ports.BannerSlot`, the slot is `home-banner`;
   `internal/shell/content_connection.go` adapts `contentui.Reader`; handler
   tests pin the published, empty and failed paths; `make check` is green.
3. **Customer legal publication.** Content owners complete the two documents
   and their metadata; the deployment path and host are supplied and wired.
   *Exit:* `npm run build` in `docs/site` passes the gate only with approved
   documents, and the built site is served at the agreed path. **Verified
   offline (2026-10-19):** the gate is pinned by its own tests
   (`docs/site/scripts/check-publication.test.mjs`, run by `make check`) —
   approved documents pass while drafts, missing metadata, malformed dates, and
   leftover draft instructions each block the build — and the production
   command (`npm run build`, gate included) was exercised end to end: it blocks
   the checked-in drafts and completes against approved fixtures on a throwaway
   copy, emitting the expected static pages. The document content, its
   approval, and the host remain owner-supplied.

Step 4 (merchandising) exists only when §4's trigger fires.

## 6. What this record does not decide (owner-supplied)

- The Builder.io project, keys, editor roster, and the approved egress path.
- The legal entity, data flows, effective dates, and the publication host and
  path.
- Any per-tenant content policy — today the model is explicitly platform-wide.

## 7. References

- [`CMS_CONTENT_MODEL.md`](./CMS_CONTENT_MODEL.md) — the read contract, model
  fields, and the acceptance checklist before enabling production.
- [`DECISION_PRODUCT_METADATA.md`](./DECISION_PRODUCT_METADATA.md) — headless
  CMS for banners and editorial content; Rspress for developer documentation.
- [`DECISION_UI_OWNERSHIP.md`](./DECISION_UI_OWNERSHIP.md) — arrangement vs
  content; why there is no slot registry.
- [`DECISION_TEAM_OWNED_VERTICALS.md`](./DECISION_TEAM_OWNED_VERTICALS.md) — the
  vertical model and how to scaffold one.
- [`../ARCHITECTURE.md`](../ARCHITECTURE.md) — [D-023](../ARCHITECTURE.md#d-023)
  (team-owned verticals), [D-024](../ARCHITECTURE.md#d-024) (server-rendered
  first; React only as islands).
