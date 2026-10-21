package shell

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	contentcmscompose "gogo/internal/features/contentcms/compose"
	homeui "gogo/internal/features/home/ui"
	"gogo/internal/shared/web/navigation"
	"gogo/internal/shared/web/site"
)

// publicSitePage composes the two public surfaces that share the root: the
// manual storefront and the native CMS. Keeping the dispatcher here, at the
// gateway boundary, is what allows a published CMS page to own "/" without
// either vertical importing the other or losing its existing routes.
type publicSitePage struct {
	content *contentcmscompose.PageHandler
	home    *homeui.Page
}

func newPublicSitePage(content *contentcmscompose.PageHandler, home *homeui.Page) *publicSitePage {
	return &publicSitePage{content: content, home: home}
}

func (p *publicSitePage) RegisterRoutes(r chi.Router) {
	p.content.RegisterRoutes(r)
	// The home page keeps its catalog, product detail, and search endpoints.
	// Only "/" is arbitrated here.
	p.home.RegisterContentRoutes(r)
	r.Get("/", p.handleHome)
}

func (p *publicSitePage) Nav() []navigation.Item {
	nav := make([]navigation.Item, 0, len(p.home.Nav())+len(p.content.Nav()))
	nav = append(nav, p.home.Nav()...)
	nav = append(nav, p.content.Nav()...)
	return nav
}

// SiteSettings makes the published CMS shell available to the gateway's
// per-request layout middleware.
func (p *publicSitePage) SiteSettings(ctx context.Context) (site.Settings, error) {
	return p.content.SiteSettings(ctx)
}

func (p *publicSitePage) handleHome(w http.ResponseWriter, r *http.Request) {
	if p.content.ServeHome(w, r) {
		return
	}
	p.home.HandleHome(w, r)
}
