# billing — team-owned vertical (billing backend)

- **Team:** T5 (Gateway Platform).
- **Boundary:** one vertical. The module is flat by design — no `domain` or
  `application` package, because a vertical earns a layer once it owns a rule
  (ARCHITECTURE.md §4); it satisfies the gateway's module contract (manifest +
  routes) and contributes itself to the container via `Register`, so the shell
  lists it in the unit table and federates it in `/module-registry`.
- **Closed to other verticals:** packages under `internal/features/billing`
  may import only their own subtree, `internal/shared`, or `gogo/contracts` —
  this is enforced by `internal/architecture/vertical_boundary_test.go`.
- **Wire contracts:** none: billing consumes product change events from the
  shared module bus (published by the shell's productevents feed, not imported),
  which keeps its activity projection coherent with the catalogue.

Re-homed from `internal/modules/billing` (D-023 Phase 5).
