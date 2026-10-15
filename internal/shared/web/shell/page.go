// Package shell provides the shared document shell: header, navigation,
// footer and the htmx script that every full page in gogo shares. Pages
// compose their own regions; the shell wraps them.
//
// Navigation is federated: the gateway compiles the top-bar links from its
// pages (page Nav) and the module registry (registry.Nav) and injects them
// into the request context, so a newly registered page or module appears in
// the shell's nav without any shell-side edit.
package shell

import (
	"context"
	"io"

	"github.com/a-h/templ"

	"gogo/internal/shared/web/navigation"
	"gogo/internal/shared/web/shell/layouts"
)

// Page wraps inner content in the shared base document, rendering the
// federated navigation the gateway injected for this request. Pages that
// do not need their own templ layout can build a complete document in Go:
//
//	render.HTML(w, r, shell.Page("Products", inner))
func Page(title string, inner templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return layouts.Base(title, navigation.NavFromContext(ctx), navigation.PathFromContext(ctx), "").Render(
			templ.WithChildren(ctx, inner),
			w,
		)
	})
}
