// Package ui is the design-system boundary: the lowest visual layer of the
// web surface.
//
// Dependency contract (one-way):
//
//	internal/pages/**      ─┐
//	internal/modules/**    ─┼─▶ web/ui     (this package)
//	internal/shared/web/** ─┘
//
// This package may import only the standard library, github.com/a-h/templ and
// internal/shared/money. It must never import pages, modules, shell, pagekit,
// render or any domain package, so a primitive can be composed by any surface
// without coupling that surface to another.
//
// The class constants in classes.go are the single source of truth for the
// daisyUI/Tailwind strings that were previously copy-pasted across views. When
// a new repeated visual pattern appears, add a primitive here instead of
// copying markup into a view.
//
// A primitive owns chrome, never meaning: where a pattern needs a decision this
// package has no business making — which pages exist, what a link points at,
// what a count means — the caller makes it and passes the result in through the
// primitive's parameters. See README.md for the worked examples.
//
// # Naming convention
//
// One rule per kind of declaration, so the package reads predictably:
//
//   - A component is a singular PascalCase noun naming the element it renders
//     (Card, Badge, Breadcrumb). A variant suffixes the base it extends:
//     Button → ButtonLink, Card → CardHeader/CardBody/CardActions.
//   - A class string is <Thing>Class when constant (CardClass) and
//     <Thing>Class(args) when a helper maps semantic arguments to utilities
//     (BadgeClass(kind, size), EyebrowClass(tone)).
//   - A structured input is <Component>Options (CountBadgeOptions,
//     PaginationOptions). List-element descriptors keep the shortest name tied
//     to their primitive (Column, Crumb, PageLink).
//   - A container whose caller may need its own id / hx-* attributes takes
//     attrs templ.Attributes after its other parameters.
//
// The boundary above is enforced by the "ui primitives depend on no app code"
// test in internal/architecture.
package ui
