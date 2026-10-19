// Package typetest is a fixture for the di type-key tests: the sibling of the
// package under one/, sharing this package *name* from a different import
// path. See ../one/one.go for why.
package typetest

// Page is a fixture type, distinct from one.Page but spelled the same.
type Page struct{ Tag string }
