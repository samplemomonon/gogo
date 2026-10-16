package render

import (
	"fmt"
	"strings"
	"unicode"
)

// ValidateIslandName reports whether name carries the owner namespace every
// island key must have: "<owner>.<island>" or "<owner>/<island>", where the
// owner names the page or module the island belongs to.
//
// An island name is a cross-team contract string: it is the cache key of the
// rendered HTML, and the prefix callers pass to Composer.InvalidatePrefix. A
// bare name like "product-grid" would silently share a namespace with any
// other unit that picks the same string, and the collision would surface as
// another team's invalidation dropping this team's cache — or worse, another
// team's cached HTML — with nothing in the build to explain it.
//
// docs/DECISION_UI_OWNERSHIP.md §5 decided the convention and the enforcement:
// an island key's owner prefix is validated, not documented. The single
// construction site is Island/ClientIsland, so the check runs there (and
// eagerly in pagekit.NewPagedListPage, so a bad key fails at wiring rather
// than on the first request).
//
// The owner must be a lowercase identifier ([a-z][a-z0-9-]*). Everything after
// the separator is deliberately left permissive — paged islands append their
// own coordinates (":p2", ":t<hash>:p3") to the declared name — so the rule
// constrains exactly the part that carries ownership, and nothing else.
func ValidateIslandName(name string) error {
	sep := strings.IndexAny(name, "./")
	if sep < 0 {
		return fmt.Errorf(
			"island name %q has no owner namespace; use \"<owner>.<island>\" naming the page or module that owns the island (docs/DECISION_UI_OWNERSHIP.md §5)",
			name)
	}
	owner, rest := name[:sep], name[sep+1:]
	if !isOwnerSegment(owner) {
		return fmt.Errorf(
			"island name %q has an invalid owner segment %q; the owner is the lowercase identifier before the first \".\" or \"/\", as in \"home.product-grid\"",
			name, owner)
	}
	if rest == "" {
		return fmt.Errorf(
			"island name %q has an owner namespace but no island after the separator", name)
	}
	for _, r := range rest {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf(
				"island name %q contains whitespace or control characters; island names are cache keys and element ids", name)
		}
	}
	return nil
}

// isOwnerSegment reports whether s is a valid owner segment: a lowercase
// identifier starting with a letter, as in "home", "billing" or "home-grid".
func isOwnerSegment(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9', c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
