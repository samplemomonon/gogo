# Headless editorial CMS contract

The gateway supports two separate editorial paths. The existing `contentcms.Reader` reads published platform-wide content from Builder.io for pages such as FAQ and the storefront banner slot (`home-banner`). Native pages are tenant-scoped and authored through the gateway; their draft state, revisions, and published snapshots are stored in PostgreSQL. Neither path accepts executable HTML or database-stored code. Ownership and business rules per content surface are recorded in [`DECISION_CONTENT_SURFACES.md`](./DECISION_CONTENT_SURFACES.md).

Native authoring routes are `/admin/content/pages`, `/admin/content/pages/{slug}`, `/admin/content/sections`, the draft/revision/publish JSON endpoints under `/admin/content/pages/{slug}`, and the protected draft preview at `/preview/pages/{slug}`. The tenant-wide public shell is edited at `/admin/content/site` and previewed at `/preview/site`; its draft/publish lifecycle is separate from page revisions. Authoring and preview require a live session with the existing `authz.role.manage` permission; this MVP therefore limits page and site management to policy administrators. A separate editor permission and role mapping are not yet implemented. State-changing requests require a matching same-origin `Origin` header. Public native pages are served at `/pages/{slug}` and use the same registered component renderers as draft preview. The older Builder.io editorial path keeps its block renderer but is wrapped in the same shared shell, so site brand, menu, footer, and theme apply there too.

The public root `/` is arbitrated by the gateway. If the tenant's published site settings name a native page slug (`home` by default) and that page is published, the CMS page is served at `/`; otherwise the existing manual storefront keeps serving. A draft or missing homepage never takes over `/`, and an invalid published document fails closed instead of falling back to stale content.

## Runtime configuration

Set both `BUILDER_PUBLIC_API_KEY` and `BUILDER_CONTENT_MODEL` to enable the Builder.io read adapter. Leave both unset to keep the gateway independent of Builder.io; when no matching native page exists, `/pages/{slug}` returns 404. The model name accepts lowercase letters, digits, and hyphens. The API key is a public read key and is never sent to the browser. Do not configure a private write key.

Native authoring is enabled when `DATABASE_URL` is configured. The gateway opens a pgx pool and applies the feature's embedded migration at startup; a configured but unavailable database fails startup rather than silently serving an in-memory editor. The page table is tenant-scoped, and each save/publish transaction appends an immutable revision row.

The gateway uses the published Content API only, requests one page by slug, limits response bodies to 1 MiB, times out each request after three seconds, caches at most 64 slugs for 60 seconds, and uses the existing circuit breaker. These are defensive implementation defaults, not measured capacity targets; recalibrate them against the baseline before tuning. CMS failure returns a generic 503 and is logged server-side. A failed fetch never turns into an empty success page. The current Kubernetes egress policy does not permit general internet access; production must route the request through the organization's approved egress path and allowlist the Builder.io content host before enabling the two variables.

## Native page schema

Native pages store a versioned JSON document with `schemaVersion: 2` and a `components` tree. Component IDs are unique within a page; nesting is limited to six levels and 200 components, and a document is capped at 256 KB. The registry is closed to `container`, `heading`, `text`, `image`, `button`, `spacer`, `navbar`, `form`, `data-text`, and `ad-slot`. Only `container` may hold children. Each component has a closed property allow-list and no raw HTML or script field. The editor and server share the same Go definitions; the server revalidates every save and publish, so client-side validation is never an authorization or integrity boundary.

Properties support strings, text, numbers, booleans, enums, colors, images, URLs, spacing tokens, typography, repeatable arrays, and approved data references. Responsive overrides are stored per component under `responsive.tablet` and `responsive.mobile`; mobile inherits tablet, tablet inherits desktop, and removing an override restores inheritance. Generated breakpoint rules explicitly reset declarations that an override removes (for example full width, automatic aspect ratio, non-sticky navigation, or a cleared color), so a base value cannot leak through the cascade. Unknown fields from migrated v1 documents are preserved under `extensions`/`legacyProps` rather than silently discarded. Button destinations and image sources accept safe HTTPS URLs or same-origin paths. Bound text resolves only server-provided values from the declared data sources. The ad slot is a labelled placeholder and never executes a third-party ad script.

### Requested section vocabulary

The page-builder vocabulary is deliberately expressed through the existing registry rather than parallel "hero", "FAQ", or "gallery" component types. The built-in presets provide starting points for each requested section, and every preset validates against the same publish rules as a hand-built page:

| Requested section | Existing composition |
|---|---|
| Hero | `hero-split` preset: a two-column `container` with `heading`, `text`, `button`, and `image` children. |
| Rich text | `rich-text` preset: semantic `heading` plus escaped `text` blocks, one per paragraph. The native registry intentionally does not accept Markdown or HTML; the legacy Builder.io `markdown` block remains the only formatted-copy path. |
| Image and text | `image-text` preset: a two-column `container` pairing an `image` with a stack of `heading`, `text`, and `button`. |
| Features | `feature-grid` preset: a three-column `container` of composed feature cards. |
| Gallery | `gallery` preset: a responsive `container` grid with `image` children. The container supports up to four columns. |
| FAQ | `faq` preset: a stacked `container` of question `heading` and answer `text` blocks. It is static server-rendered content, not a scripted accordion. |
| CTA | `cta-band` preset: a styled `container` with `heading`, `text`, and `button`. |

None of these presets adds a renderer or a storage shape. They are ordinary validated component trees and can be edited, reordered, nested, or deleted with the same builder controls.

The editor is a server-rendered Templ application with a dedicated ES module for canvas state. It offers a searchable, category-grouped component library, nested canvas outliner, drag-and-drop with drop indicators, keyboard reordering/nesting, schema-generated inspector controls, desktop/tablet/mobile canvases, deterministic undo/redo, debounced autosave, save-failure and revision-conflict recovery, reusable tenant-scoped sections and built-in presets, revision restore, and protected draft preview. Draft preview and public pages render through the same registered Go renderers and generated responsive stylesheet; the canvas mirrors the same closed registry and inheritance rules in the editor.

Persistence is tenant-scoped. `content_pages` holds the mutable draft pointer plus the last published snapshot; every save or publication appends an immutable `content_page_revisions` row. JSON draft saves require `If-Match` or `expectedRevision`, return `409` with the current revision on conflict, and return structured `application/problem+json` validation errors. Publication revalidates the draft in the server transaction and rejects incomplete documents. Public pages at `/pages/{slug}` render only the published snapshot; draft preview requires the same session and `authz.role.manage` permission as authoring. A publish request does not make a draft public until validation succeeds.

Still out of scope: media uploads, scheduled publication, custom content types, revision comparison/diffs, and editor-specific permissions (authoring currently uses `authz.role.manage`). The schema, migration, HTTP workflow, rendering, accessibility-oriented state operations, and optimistic-concurrency paths are covered by unit and in-memory HTTP tests. The opt-in `TestLiveSiteStoreDraftPublishIsolation` test exercises the site table against a real PostgreSQL instance when `CONTENTCMS_TEST_DATABASE_URL` is set; it is skipped otherwise.

## Native site shell

`content_site_settings` is a tenant-scoped row with a mutable draft shell and the last published shell. `Save` changes only the draft; `Publish` validates the draft and copies it to the public pointer in one transaction. The editor uses the same expected-revision conflict contract as page drafts. The published shell is resolved once per request by the gateway and attached to the request context, so the shared layout renders the same brand, header menu, footer, and theme on CMS pages, the manual storefront fallback, and the other gateway pages.

The shell is deliberately a closed, presentation-only document: site name, optional tagline, optional logo URL/alt text, homepage slug, up to eight same-origin header links, footer text and up to eight same-origin footer links, and optional `#RGB`/`#RRGGBB` primary/secondary/accent colors. There is no raw HTML, CSS, or JavaScript field. Empty custom menu/footer links preserve the existing shell behavior; an empty logo falls back to the wordmark. A missing row or an unavailable database falls back to the built-in `Gogo` shell rather than taking the public site down. The custom menu replaces the federated menu on public pages; operator surfaces (`/admin`, `/seller`, `/billing`, `/profile`) deliberately keep the federated menu so publishing a public menu cannot hide dashboard navigation. Published site settings are cached in-process for 30 seconds and invalidated immediately after a publish.

## Builder model fields

Create one model for editorial pages with these data fields. Block objects must use the exact field names below; unknown block types are rejected at runtime.

| Field | Type | Constraints |
|---|---|---|
| `slug` | text | lowercase `a-z`, digits, hyphen; 1–80 characters |
| `title` | text | required; at most 240 characters |
| `blocks` | list of objects | at most 50 |

Allowed block objects:

| `type` | Fields | Rendering |
|---|---|---|
| `hero` / `banner` | optional `title`, `imageUrl`, `imageAlt` | Server-rendered heading and image; image URL must be HTTPS or same-origin path |
| `markdown` | optional `title`, `body` | Markdown converted on the server; raw HTML is not enabled |
| `faq` | optional `title`, `items[]` with `question`, `answer` | Server-rendered definition list; answers use the same restricted Markdown renderer |
| `cta` | optional `title`, required `label`, `href` | Link must be HTTPS or a same-origin path |

CMS content is presentation data. Product names, prices, stock, categories, and validation remain owned by the Product Service and its read models; editorial blocks cannot write or override them. No arbitrary HTML, JavaScript, iframe, or business rule is accepted by this contract.

## Acceptance before enabling production

Create the model in the target Builder.io project, verify the exact API response against a published fixture, assign platform-only editor roles, and capture the approved key scope and rotation owner. Establish the approved outbound network path and prove redirects remain on the Builder.io host. On the repository side the read path is already pinned by tests in `internal/features/contentcms/presentation` (documented request shape, documented-shape fixture parse, 404-vs-503 taxonomy, cache bounds, and the rule that the public read key never appears in an error); replace the fixture with a captured published response when the project exists. The source does not include project credentials or egress policy, so integration remains opt-in until deployment configuration is supplied.
