# sales — team-owned vertical (backend `sales` + read-side `seller`)

- **Team:** T2 (owner of the POS/seller console and the sales backend).
- **Boundary:** one vertical. The backend hexagon lives at the feature root
  (`domain`, `application`, `infrastructure`, `presentation`, `compose`); the
  seller console's read-side lives under `ui/`.
- **Closed to other verticals:** packages under `internal/features/sales` may
  import only their own subtree, `internal/shared`, or `gogo/contracts` — this
  is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Hexagon still checked:** the layer matrix from ARCHITECTURE.md applies here
  via `TestFeatureVerticalHexagonDependencyDirection`.
- **Wire contracts:** `sales.v1` is provided by `presentation/grpcservice` and
  consumed by `ui/infrastructure/salesclient`; the seller console also reads
  `inventory.v1` and the product-listing/search contracts through
  `internal/shared/productconsumer`. Cross-team UI, when a real one exists, is
  the declared `public/` surface (D-021) — none exists yet.

Re-homed from `internal/modules/sales` + `internal/pages/seller` (D-023 Phase 3).