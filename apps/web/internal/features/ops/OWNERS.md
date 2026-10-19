# ops — team-owned vertical

- **Boundary:** closed to every other app/vertical by Go's `internal/` visibility.
  It imports `internal/shared` only; it never imports another feature's internals.
- **Home:** this is the whole vertical. There is nowhere else to look for ops:
  the module, its routes, its manifest, and its tests live here together.
- **Why a `features/` dir:** a team owns one thing end to end (its backend *and*
  its UI) instead of scattering across `internal/modules`, `internal/pages`,
  `internal/bootstrap`, and an import-rule test. `ops` is the reference vertical —
  it shows how small a federated profile can be.

Every unit re-homed the same way: the migration completed in D-023 Phases 4–7,
and the architecture tests fail if `internal/modules` or `internal/pages` is
recreated. Scaffold a new vertical with `make feature`.
