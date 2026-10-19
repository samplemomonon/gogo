// Package typetest is a fixture for the di type-key tests: one of two
// packages that deliberately share this package *name* from different import
// paths — the shape every team-owned vertical produces (each vertical has its
// own `ui` and `ports` packages, D-023). Nothing here imports anything.
package typetest

// Page is a fixture type. The sibling package two.Page is a distinct Go type
// with the same short name, which is exactly what the container's type key
// must distinguish.
type Page struct{ Tag string }
