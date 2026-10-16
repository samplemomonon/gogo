package render

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"golang.org/x/sync/singleflight"

	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/ui"
)

// Composer is the page composer: it orchestrates every island according
// to its decided strategy and assembles the parts into one document.
type Composer struct {
	planner Planner
	cache   Cache

	// flight coalesces concurrent renders of the same island key, so a cache
	// miss triggers one render rather than one per concurrent request (the
	// cache-stampede / thundering-herd failure mode).
	flight singleflight.Group

	// genMu guards prefixGen, the per-island-family invalidation generation.
	// A coalesced render captures the generation before rendering and only
	// publishes its bytes if the generation is unchanged, so an invalidation that
	// races a render cannot leave stale HTML cached.
	genMu     sync.Mutex
	prefixGen map[string]uint64
}

// NewComposer returns a composer with the ContractPlanner policy and the
// given cache (pass nil to disable caching).
func NewComposer(cache Cache) *Composer {
	return &Composer{
		planner:   ContractPlanner{},
		cache:     cache,
		prefixGen: make(map[string]uint64),
	}
}

// islandKey qualifies an island's cache key with the tenant the render is for.
//
// The tenant is a trailing segment so that an island name remains a prefix of
// its qualified keys, which is what lets family invalidation keep working
// (see Invalidate) while the qualification makes it impossible for one tenant
// to read another's cached HTML. Only islands whose name embeds a product id
// would be safe unqualified — ids are globally unique — but a page-level
// island (a product grid, a seller table) renders tenant-specific rows under a
// single name, and those are exactly the entries this protects.
func (c *Composer) islandKey(ctx context.Context, name string) string {
	return name + "." + string(tenant.FromContextOrDefault(ctx))
}

// WithPlanner overrides the rendering policy used by the composer.
func (c *Composer) WithPlanner(planner Planner) *Composer {
	c.planner = planner
	return c
}

// RenderPage renders every island of the page with its strategy, composes
// the results and writes the final document to w.
func (c *Composer) RenderPage(
	ctx context.Context,
	w io.Writer,
	page *Page,
	rctx RenderingContext,
) error {
	regions := make([]templ.Component, 0, len(page.Components))
	for _, component := range page.Components {
		html, err := c.renderComponent(ctx, component, rctx)
		if err != nil {
			return err
		}
		regions = append(regions, rawHTML(html))
	}

	var out templ.Component
	if page.Layout != nil {
		out = page.Layout(join(regions))
	} else {
		out = join(regions)
	}
	return out.Render(ctx, w)
}

// RenderNavigation serves a KindNavigation use case: the complete page for
// plain requests, and only the page's islands bare (no shell) for HTMX
// requests — the region htmx swaps into the existing document. Handlers
// declare the page once and state their intent; the renderer picks the
// representation from the request.
//
// The caller's Page is never mutated: for HTMX requests a shallow copy with
// a nil Layout is rendered, so a Page reused or cached across requests keeps
// its layout.
func (c *Composer) RenderNavigation(
	ctx context.Context,
	w io.Writer,
	page *Page,
	rctx RenderingContext,
) error {
	if rctx.IsHTMX {
		bare := *page
		bare.Layout = nil
		return c.RenderPage(ctx, w, &bare, rctx)
	}
	return c.RenderPage(ctx, w, page, rctx)
}

// RenderComponent renders a single island — used by endpoints that serve
// Client-mode island fragments (e.g. GET /catalog/{id}/stock).
func (c *Composer) RenderComponent(
	ctx context.Context,
	w io.Writer,
	component *Component,
	rctx RenderingContext,
) error {
	html, err := c.renderComponent(ctx, component, rctx)
	if err != nil {
		return err
	}
	_, err = w.Write(html)
	return err
}

// Invalidate drops the named islands from the cache so the next request
// re-renders them. Call it after mutations that change the island data.
//
// A name identifies an island *family*, not a single cache entry: keys are
// tenant-qualified (see islandKey), so this clears every tenant's copy. That
// is deliberate. An event subscriber usually cannot tell which tenant a
// mutation belonged to, and the two mistakes are not symmetric — dropping
// another tenant's warm entry costs one re-render, while failing to drop the
// mutated tenant's entry serves stale HTML. Invalidation is therefore
// deliberately broader than strictly necessary rather than ever too narrow.
func (c *Composer) Invalidate(names ...string) {
	for _, name := range names {
		c.InvalidatePrefix(name)
	}
}

// InvalidatePrefix drops every island whose key begins with prefix and
// advances the prefix's generation, so a family of related islands (e.g. one
// cache entry per paginated page of a listing) is re-rendered together after
// a mutation. The prefix generation is folded into every key's effective
// generation, so an in-flight render of any affected page — even one that
// started before the prefix was first invalidated — cannot publish
// pre-mutation bytes.
func (c *Composer) InvalidatePrefix(prefix string) {
	c.genMu.Lock()
	c.prefixGen[prefix]++
	c.genMu.Unlock()

	if c.cache != nil {
		c.cache.InvalidatePrefix(prefix)
	}
}

func (c *Composer) renderComponent(
	ctx context.Context,
	component *Component,
	rctx RenderingContext,
) ([]byte, error) {
	decision := c.planner.Decide(component.Contract, rctx)

	switch decision.Mode {
	case ModeClient:
		return c.renderClient(ctx, component)
	case ModeStatic:
		return c.renderCached(ctx, component, 0)
	case ModeRevalidate:
		return c.renderCached(ctx, component, decision.CacheTTL)
	default: // ModeDynamic
		return renderTempl(ctx, component.Body)
	}
}

// renderCached returns the cached HTML of the island when present and
// fresh, otherwise renders and stores it. Only Public, non-personalized
// islands are cached, so cached content can never leak across viewers. The
// same predicate gates both the read and the write, so an island is never
// read from a cache it would not have been written to.
//
// Cache misses are coalesced per island key: when N requests miss the same
// island at once, exactly one render runs and the other N-1 wait for its
// bytes. This is what keeps a cold cache (a deploy, an invalidation storm, a
// hot key's TTL expiry) from stampeding the backend with N identical
// expensive renders. Each caller still honors its own context: a caller that
// gives up returns a context error without aborting the shared render for
// the others.
func (c *Composer) renderCached(
	ctx context.Context,
	component *Component,
	ttl time.Duration,
) ([]byte, error) {
	cachable := c.cache != nil && component.Contract.Public && !component.Contract.Personalized

	if cachable {
		if data, ok := c.cache.Get(c.islandKey(ctx, component.Name)); ok {
			return data, nil
		}
		return c.renderCoalesced(ctx, component, ttl)
	}

	return renderTempl(ctx, component.Body)
}

// renderCoalesced renders a cacheable island exactly once per key across
// concurrent callers and stores the result. The render runs on a context
// detached from any single caller, so one cancelled request cannot abort the
// shared work; waiters still select on their own context to return early.
//
// The write is guarded by the island's invalidation generation: if the island
// was invalidated while the render was in flight, the freshly rendered bytes
// are stale and are NOT stored, so a concurrent mutation cannot leave
// pre-mutation HTML in the cache until the next event.
func (c *Composer) renderCoalesced(
	ctx context.Context,
	component *Component,
	ttl time.Duration,
) ([]byte, error) {
	key := c.islandKey(ctx, component.Name)
	startGen := c.generation(key)

	resultCh := c.flight.DoChan(key, func() (any, error) {
		// Re-check the cache: a render that started just before this one may
		// already have populated it (the key could have been invalidated and
		// refreshed between the caller's Get and this call).
		if data, ok := c.cache.Get(key); ok {
			return data, nil
		}

		// Detach from the triggering request's cancellation: the shared
		// render must complete for every waiter even if the first caller's
		// request is aborted.
		renderCtx := context.WithoutCancel(ctx)

		data, err := renderTempl(renderCtx, component.Body)
		if err != nil {
			return nil, err
		}

		// Only publish the result if the island was not invalidated while it
		// rendered; otherwise the bytes describe pre-mutation state.
		if c.generation(key) == startGen {
			c.cache.Set(key, data, ttl)
		}
		return data, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		data, ok := result.Val.([]byte)
		if !ok {
			return nil, errors.New("render: unexpected coalesced result type")
		}
		return data, nil
	}
}

// cachedFragment returns the already-rendered HTML of a cacheable island when
// it is present and fresh, so a caller can serve a fragment without building
// (and fetching the data for) the component. It applies the same gate as
// renderCached — only Public, non-personalized islands are ever read — so a
// cached island is never served to a viewer it was not writable for. A miss
// returns ok=false, leaving the full render path (with its coalescing and
// stampede protection) to do the work.
//
// This is the fast path for HTMX fragment requests: after the first render,
// a fragment swap can be answered straight from the cache with no data fetch
// on the backing service.
func (c *Composer) CachedFragment(ctx context.Context, component *Component) (data []byte, ok bool) {
	cachable := c.cache != nil && component.Contract.Public && !component.Contract.Personalized
	if !cachable {
		return nil, false
	}
	return c.cache.Get(c.islandKey(ctx, component.Name))
}

// generation returns the current effective invalidation generation of an
// island key: the sum of the generations of every registered prefix it falls
// under. Invalidate bumps a family's prefix, which raises the generation of
// every tenant-qualified key it matches.
func (c *Composer) generation(key string) uint64 {
	c.genMu.Lock()
	defer c.genMu.Unlock()

	var g uint64
	for prefix := range c.prefixGen {
		if strings.HasPrefix(key, prefix) {
			g += c.prefixGen[prefix]
		}
	}
	return g
}

// renderClient produces the placeholder markup: a scoped island element
// that lazy-loads its content from FetchURL (htmx lazy-load pattern).
func (c *Composer) renderClient(ctx context.Context, component *Component) ([]byte, error) {
	var b bytes.Buffer

	b.WriteString(`<div id="island-`)
	b.WriteString(html.EscapeString(component.Name))
	b.WriteString(`" data-island="`)
	b.WriteString(html.EscapeString(component.Name))
	b.WriteString(`" hx-get="`)
	b.WriteString(html.EscapeString(component.FetchURL))
	b.WriteString(`" hx-trigger="load" hx-swap="innerHTML" class="island">`)

	if component.Fallback != nil {
		if err := component.Fallback.Render(ctx, &b); err != nil {
			return nil, err
		}
	} else {
		// The design system's Spinner, not a hand-written loading class: the
		// stock island placeholder was the one loader in the app that bypassed
		// the primitive, so it was free to drift from the loader the catalog
		// stock placeholder renders. render -> ui is a legal edge; ui never
		// depends on render.
		if err := ui.Spinner("sm", "").Render(ctx, &b); err != nil {
			return nil, err
		}
	}

	b.WriteString(`</div>`)
	return b.Bytes(), nil
}
