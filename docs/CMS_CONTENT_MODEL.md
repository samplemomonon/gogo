# Headless editorial CMS contract

The gateway reads published public content from Builder.io through `contentcms.Reader`. The first adapter is for platform-wide pages such as FAQ; it does not invent a tenant-specific content model. It does not contain an authoring API, preview path, or write credential. Platform editorial administrators manage the CMS project and editor roles in Builder.io. Seller/merchant roles in the gateway do not grant CMS access. Ownership, the business rules per content surface, and the build order are recorded in [`DECISION_CONTENT_SURFACES.md`](./DECISION_CONTENT_SURFACES.md).

## Runtime configuration

Set both `BUILDER_PUBLIC_API_KEY` and `BUILDER_CONTENT_MODEL` to enable the read adapter. Leave both unset to keep the gateway independent of Builder.io; `/pages/{slug}` then returns 404. The model name accepts lowercase letters, digits, and hyphens. The API key is a public read key and is never sent to the browser. Do not configure a private write key.

The gateway uses the published Content API only, requests one page by slug, limits response bodies to 1 MiB, times out each request after three seconds, caches at most 64 slugs for 60 seconds, and uses the existing circuit breaker. These are defensive implementation defaults, not measured capacity targets; recalibrate them against the baseline before tuning. CMS failure returns a generic 503 and is logged server-side. A failed fetch never turns into an empty success page. The current Kubernetes egress policy does not permit general internet access; production must route the request through the organization's approved egress path and allowlist the Builder.io content host before enabling the two variables.

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

Create the model in the target Builder.io project, verify the exact API response against a published fixture, assign platform-only editor roles, and capture the approved key scope and rotation owner. Establish the approved outbound network path and prove redirects remain on the Builder.io host. The source does not include project credentials or egress policy, so integration remains opt-in until deployment configuration is supplied.
