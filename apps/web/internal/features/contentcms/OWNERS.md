# contentcms — team-owned vertical (editorial content read-side)

- **Team:** T5 (Gateway Platform).
- **Boundary:** one vertical. This half serves the read-only editorial content
  contract at `/pages/{slug}`: a top-level gateway page — NOT a federated
  module: no manifest, no lifecycle, and no `Register` (the shell wires the
  `Reader` and the `PageHandler` directly, so the unit table does not list it).
- **Closed to other verticals:** packages under `internal/features/contentcms`
  may import only their own subtree, `internal/shared`, or `gogo/contracts` —
  this is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Untrusted input:** CMS data is treated as untrusted: only the block types
  declared in `ui/content.go` render, and executable HTML/browser-side code is
  never accepted. The Builder.io reader reads published content only; authoring
  and preview credentials never belong here.

Re-homed from `internal/pages/contentcms` (D-023 Phase 5).
