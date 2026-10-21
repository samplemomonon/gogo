# contentcms — team-owned vertical (editorial read path + native page authoring)

- **Team:** T5 (Gateway Platform).
- **Boundary:** one vertical. The UI serves published editorial content at
  `/pages/{slug}`, can own the public `/` homepage selected by the tenant's
  published site settings, and serves native authoring routes under
  `/admin/content/pages` and `/admin/content/site`. Native page drafts,
  revisions, published snapshots, and the global shell (brand, menu, footer,
  homepage slug, and brand colors) are owned by this feature's PostgreSQL
  repository. It remains a top-level gateway page — NOT a federated module: no
  manifest, no lifecycle, and no `Register` (the shell wires its `Reader`,
  `PageStore`, `SiteStore`, and `PageHandler` directly).
- **Closed to other verticals:** packages under `internal/features/contentcms`
  may import only their own subtree, `internal/shared`, or `gogo/contracts` —
  this is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Untrusted input:** CMS data is treated as untrusted. Builder.io blocks are
  limited to `presentation/builder.go` and the routes in
  `presentation/handler/page.go`; native page trees are versioned and validated
  against the closed `domain` component schema before persistence and render.
  Neither path accepts executable HTML or code from the database. Draft,
  publish, section, revision, site-settings, and preview operations require a
  live session and the `authz.role.manage` permission; page preview and site
  preview use the same registered renderers and validated shell data as the
  public site. Site links are same-origin paths and brand colors are a closed
  hex format, so the global shell cannot become an open redirect or a CSS/script
  injection surface.

Re-homed from `internal/pages/contentcms` (D-023 Phase 5).
