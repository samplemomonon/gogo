# home — team-owned vertical (storefront read-side)

- **Team:** T3 (Storefront).
- **Boundary:** one vertical. This half is the storefront's public surface —
  top-level gateway pages (`/` with the product grid, `/catalog/{id}` for
  product detail), NOT a federated module: no manifest, no lifecycle. Its
  read-side lives under `ui/`: ports in `application/`, the remote adapter in
  `infrastructure/listingclient`, handlers, and the views the pages render.
- **Closed to other verticals:** packages under `internal/features/home` may
  import only their own subtree, `internal/shared`, or `gogo/contracts` — this
  is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Wire contracts:** the grid and detail pages consume the product platform
  remotely, through `productlisting.v1` and `productsearch.v1` (via
  `internal/shared/productconsumer`); the page imports no product-domain runtime
  code. Cross-team UI, when a real one exists, is the declared `public/` surface
  (D-021) — none exists yet.
- **Freshness:** the grid and the detail region are revalidated islands keyed by
  `ui/islandkeys`; the page subscribes to no product event stream, so staleness
  is bounded by the rendering contract's TTL (the write side lives in another
  process — ARCHITECTURE.md).

Re-homed from `internal/pages/home` (D-023 Phase 5).
