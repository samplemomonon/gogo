#!/bin/bash
# Scaffold a team-owned vertical under apps/web/internal/features/<name>.
#
# "One team, one project, one image" (see docs/DECISION_TEAM_OWNED_VERTICALS.md
# and ARCHITECTURE.md D-023). A vertical is the single home for a feature's
# backend AND its UI, so a team has exactly one directory to own. A vertical is
# a closed module to every other vertical by Go's internal/ visibility, except
# for its /public surface — declared only when a real cross-team consumer exists
# (D-021).
#
# Usage:
#   scripts/feature-vertical.sh <name> [--team "Team Name"] [--owned-by email]
#
# | --team "Name"    write it into OWNERS.md (default: current git user name)
# | --owned-by email CODEOWNERS line (default: current git user email)
#
# This is pure shell; it does not touch the Go toolchain. After scaffolding,
# run `make arch-graph` (with go installed) and commit import_graph.txt.
set -euo pipefail

name="${1:-}"
if [ -z "$name" ]; then
  echo "usage: $0 <vertical-name> [--team \"Team Name\"]" >&2
  exit 1
fi

# Normalise to a lowercase underscored Go package-safe name.
slug="${name,,}"             # lowercase
slug="${slug//-/_}"          # dashes -> underscore
slug="${slug// /_}"          # spaces -> underscore

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base="$root/apps/web/internal/features"
dir="$base/$slug"
rel="apps/web/internal/features/$slug"   # repo-relative, for CODEOWNERS

if [ -e "$dir" ]; then
  echo "error: $dir already exists" >&2
  exit 1
fi

team=""
owned_by=""
while [ $# -gt 0 ]; do
  case "$1" in
    --team) team="$2"; shift 2 ;;
    --owned-by) owned_by="$2"; shift 2 ;;
    *) shift ;;
  esac
done

git_name="$(git config user.name 2>/dev/null || echo 'Unnamed Team')"
git_email="$(git config user.email 2>/dev/null || echo 'team@example.com')"
team="${team:-$git_name}"
owned_by="${owned_by:-$git_email}"

mkdir -p \
  "$dir/domain" \
  "$dir/application/ports" \
  "$dir/application/use-cases" \
  "$dir/infrastructure" \
  "$dir/presentation/grpcservice" \
  "$dir/ui" \
  "$dir/public"

# OWNERS.md interpolates the team/owner and has no shell metacharacters.
cat > "$dir/OWNERS.md" <<EOF
# $name — team-owned vertical

- **Team:** $team
- **Owned by:** $owned_by
- **Boundary:** closed to other verticals except through \`/public\`.

Nothing in \`domain/\`, \`application/\`, \`infrastructure/\` or \`presentation/\`
is importable by another vertical. Cross-team UI goes through \`/public\` only,
and that package is declared only when a real consumer exists.
EOF

# Go skeletons use a quoted heredoc (no shell expansion) plus sed, so package
# names are the only thing substituted and backticks/`$` stay literal.
emit_go() {
  local path="$1"; shift
  cat > "$path"
  sed -i "s/__SLUG__/$slug/g" "$path"
}

emit_go "$dir/vertical.go" <<'GO'
package __SLUG__

import "net/http"

// Vertical is the owned root of the __SLUG__ bounded context. It carries its
// own HTTP mux (route ownership), its own graph (compose), its own lifecycle.
// Nothing else in the feature tree is importable by another vertical.
type Vertical struct {
	mux  *http.ServeMux
	stop func()
}

func New() *Vertical { return &Vertical{mux: http.NewServeMux()} }
GO

emit_go "$dir/routes.go" <<'GO'
package __SLUG__

import "net/http"

// Routes registers this vertical's HTTP routes. A route has exactly one owning
// team, and this vertical is it: the UI and the backend are the same unit.
// The composition root (internal/shell) calls this per vertical; there is no
// separate page tree to reach into.
func (v *Vertical) Routes() http.Handler { return v.mux }
GO

emit_go "$dir/events.go" <<'GO'
package __SLUG__

// This vertical talks to its peers only through the shared bus, in its own
// vocabulary. It never imports another vertical; module-to-module contact is
// event-based (import rule 3).
GO

# Record the target ownership at the repo root for CODEOWNERS attribution.
codeowners="$root/CODEOWNERS"
if [ ! -f "$codeowners" ]; then
  printf '# CODEOWNERS — the single ownership source (D-023).\n' > "$codeowners"
fi
if ! grep -qxF "$rel/** $owned_by" "$codeowners"; then
  printf '%s/** %s\n' "$rel" "$owned_by" >> "$codeowners"
fi

echo "scaffolded vertical: $rel"
echo "  - add the ownership guard (docs/DECISION_TEAM_OWNED_VERTICALS.md §5)"
echo "  - run 'make arch-graph' (with go installed) and commit import_graph.txt"
