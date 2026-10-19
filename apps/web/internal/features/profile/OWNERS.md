# profile — team-owned vertical (storefront account read-side)

- **Team:** T4 (Identity).
- **Boundary:** one vertical. This half is the storefront account page
  (`/profile`), a top-level gateway page — NOT a federated module: no manifest,
  no lifecycle. Its read-side lives under `ui/`: ports in `application/`, the
  auth client adapter in `infrastructure/authclient`, and the account views.
- **Closed to other verticals:** packages under `internal/features/profile` may
  import only their own subtree, `internal/shared`, or `gogo/contracts` — this
  is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Wire contracts:** the page consumes the auth vertical over `auth.v1`
  through the shared `internal/shared/authconsumer` kit adapted by its own
  client; it owns no identity state and never imports auth runtime code.
  Cross-team UI, when a real one exists, is the declared `public/` surface
  (D-021) — none exists yet.

Re-homed from `internal/pages/profile` (D-023 Phase 5).
