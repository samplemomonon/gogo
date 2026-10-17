// Package ui is the reusable web component adapter, consumed by the web app
// from the repository-root design-system module.
//
// Dependency contract:
//
//	apps/web/internal/pages/**       ─┐
//	apps/web/internal/modules/**     ─┼─▶ gogo.design/system/web/ui
//	apps/web/internal/shared/web/**  ─┘
//
// This standalone module may import only the standard library and
// github.com/a-h/templ. Its location outside apps/web/internal makes that
// dependency boundary structural: components cannot reach into private web
// application packages.
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
// Cross-platform tokens and governance live beside this package in the root
// design-system. This package owns web markup and behavior; native clients map
// the same semantic tokens to native components.
package ui
