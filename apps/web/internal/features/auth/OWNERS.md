# auth — team-owned vertical (identity backend + login page)

- **Team:** T4 (owner of the auth backend and the account surfaces).
- **Boundary:** one vertical. The backend hexagon lives at the feature root
  (`domain`, `application`, `infrastructure`, `presentation`, `compose`); the
  login page's read-side lives under `ui/`.
- **Closed to other verticals:** packages under `internal/features/auth` may
  import only their own subtree, `internal/shared`, or `gogo/contracts` — this
  is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Hexagon still checked:** the layer matrix from ARCHITECTURE.md applies here
  via `TestFeatureVerticalHexagonDependencyDirection`.
- **Wire contracts:** `auth.v1` is provided by `presentation/grpcservice`; its
  only client is the shared `internal/shared/authconsumer` kit, which the
  seller console, the login page and the profile page adapt to their own read
  models. Cross-team UI, when a real one exists, is the declared `public/`
  surface (D-021) — none exists yet.
- **Domain ownership:** the merchant relationship (`Merchant`, `Membership`,
  `MerchantInvitation`) and the RBAC surface are owned here (D-022); the login
  page reads sessions over `auth.v1` and owns no identity state.

Re-homed from `internal/modules/auth` + `internal/pages/auth` (D-023 Phase 4).
