# inventory — team-owned vertical (stock backend)

- **Team:** T2 (Merchant Console).
- **Boundary:** one vertical. The backend hexagon lives at the feature root:
  `domain` (the sellable quantity and the movement ledger), `application`
  (ports + service), `infrastructure/postgres`, `presentation/grpcservice`
  (the `inventory.v1` server plus health), and `compose` — the vertical's own
  composition root. It carries no HTTP routes: the seller console consumes it
  as a peer over `inventory.v1`.
- **Closed to other verticals:** packages under `internal/features/inventory`
  may import only their own subtree, `internal/shared`, or `gogo/contracts` —
  this is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Hexagon checked:** the layer matrix from ARCHITECTURE.md applies here via
  `TestFeatureVerticalHexagonDependencyDirection`.
- **Wiring:** the shell mounts it over the in-process gRPC connection
  (`internal/shell/inventory_connection.go`); the authorization config
  (`Security`) is supplied by the composition root. The seller console is its
  only client today (`sales/ui/infrastructure/inventoryclient`). Cross-team UI,
  when a real one exists, is the declared `public/` surface (D-021) — none
  exists yet.

Re-homed from `internal/modules/inventory` (D-023 Phase 5).
