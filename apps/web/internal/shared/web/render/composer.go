package render

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	templruntime "github.com/a-h/templ/runtime"
	"golang.org/x/sync/singleflight"

	"gogo.design/system/web/ui"
	"gogo/internal/shared/tenant"
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
//
// Two delivery shapes, chosen here so handlers never have to think about them:
//
//   - Buffered (the default): every island and the layout render before the
//     first byte is written; a late error is a clean error response, never a
//     truncated page.
//   - Streaming (page.Streaming): the shell and each island are flushed as
//     soon as they render. The first flush locks status and headers, so the
//     per-island timings ship as a trailer instead of a header.
//
// Either way every server-rendered island is timed and its outcome counted
// (metrics.go), and a failing island degrades to its Fallback when one is
// declared — one bad region no longer takes the page down with it.
func (c *Composer) RenderPage(
	ctx context.Context,
	w io.Writer,
	page *Page,
	rctx RenderingContext,
) error {
	if streamingRequested(page, w, rctx) {
		return c.renderStreaming(ctx, w, page, rctx)
	}
	return c.renderBuffered(ctx, w, page, rctx)
}

// streamingRequested reports whether this response takes the streaming path.
// Streaming is a full-document affair: it needs a Layout to deliver the shell
// before the islands, an HTMX fragment swap is atomic by contract, and a
// writer without flush support (a plain buffer) has nowhere to deliver to.
func streamingRequested(page *Page, w io.Writer, rctx RenderingContext) bool {
	if !page.Streaming || rctx.IsHTMX || page.Layout == nil {
		return false
	}
	if _, ok := w.(*templruntime.Buffer); ok {
		return true
	}
	_, ok := w.(http.Flusher)
	return ok
}

// renderBuffered composes the page in memory and writes it in one piece:
// islands in order, layout around them, then headers, status and bytes. The
// pooled buffer is the same trick templ's own buffered handler uses, and it
// is what keeps a layout error from leaving a half-written document on the
// wire.
func (c *Composer) renderBuffered(
	ctx context.Context,
	w io.Writer,
	page *Page,
	rctx RenderingContext,
) error {
	regions, timings, err := c.composeIslands(ctx, page.Components, rctx, false)
	if err != nil {
		return err
	}

	buf := templ.GetBuffer()
	defer templ.ReleaseBuffer(buf)

	out := pageComposition(page, regions)
	if err := out.Render(ctx, buf); err != nil {
		return err
	}

	c.writeResponseHeaders(w, timings, false)
	return writeBody(w, page.StatusCode, buf.Bytes())
}

// composeIslands renders islands in document order for a buffered response.
// Each island renders to its own bytes first — per-island atomicity — so no
// delivery boundary can ever split an island's markup.
func (c *Composer) composeIslands(
	ctx context.Context,
	components []*Component,
	rctx RenderingContext,
	streaming bool,
) ([]templ.Component, []islandTiming, error) {
	regions := make([]templ.Component, 0, len(components))
	timings := make([]islandTiming, 0, len(components))

	for _, component := range components {
		region, timing, err := c.renderIslandRegion(ctx, component, rctx, streaming)
		if err != nil {
			return nil, nil, err
		}
		regions = append(regions, rawHTML(region))
		timings = appendTiming(timings, timing)
	}

	return regions, timings, nil
}

// pageComposition wraps the island regions in the page's layout, or leaves
// them bare for fragment responses.
func pageComposition(page *Page, regions []templ.Component) templ.Component {
	if page.Layout != nil {
		return page.Layout(join(regions))
	}
	return join(regions)
}

// renderStreaming delivers the document progressively. Headers and status go
// out before the first flush; per-island timings cannot be known that early,
// so they are announced as a trailer and set after the last island. The
// buffer lifecycle mirrors templ's generated code: a caller-owned buffer is
// reused untouched, a composer-owned one is returned to the pool — flushed on
// success, reset first on error so a failed composition never leaks a partial
// document the composer still owns.
func (c *Composer) renderStreaming(
	ctx context.Context,
	w io.Writer,
	page *Page,
	rctx RenderingContext,
) error {
	hw, _ := w.(http.ResponseWriter)
	if hw != nil {
		c.writeResponseHeaders(w, nil, true)
		if page.StatusCode != 0 {
			hw.WriteHeader(page.StatusCode)
		}
	}

	buf, existing := templruntime.GetBuffer(w)

	timings := make([]islandTiming, 0, len(page.Components))
	inner := &streamingIslands{
		composer:   c,
		components: page.Components,
		rctx:       rctx,
		timings:    &timings,
	}

	if err := page.Layout(inner).Render(ctx, buf); err != nil {
		if !existing {
			buf.Reset(io.Discard)
			_ = templruntime.ReleaseBuffer(buf)
		}
		return err
	}

	if hw != nil && len(timings) > 0 {
		// Announced in the Trailer header before WriteHeader, so this
		// lands as a trailer: the point of a streamed response is that
		// the timings are not known when the headers lock.
		hw.Header().Set("Server-Timing", serverTimingHeader(timings))
	}
	if !existing {
		return templruntime.ReleaseBuffer(buf)
	}
	return nil
}

// streamingIslands is the composed region of a streamed page. The layout
// renders it with the composer's runtime buffer as its writer; it renders
// each island to bytes (per-island atomicity), writes them, and flushes — so
// the shell and every earlier island are already on the wire while later
// islands render.
type streamingIslands struct {
	composer   *Composer
	components []*Component
	rctx       RenderingContext
	timings    *[]islandTiming
}

func (s *streamingIslands) Render(ctx context.Context, w io.Writer) error {
	for _, component := range s.components {
		region, timing, err := s.composer.renderIslandRegion(ctx, component, s.rctx, true)
		if err != nil {
			return err
		}
		*s.timings = appendTiming(*s.timings, timing)

		if _, err := w.Write(region); err != nil {
			return err
		}
		if f, ok := w.(interface{ Flush() error }); ok {
			if err := f.Flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeResponseHeaders sets the headers a composed response carries:
// Content-Type only when the caller has not set one (render.Respond sets it
// before its path, and that value must win); Vary, because the same URL
// negotiates a full document versus a fragment; and Server-Timing, either as
// a header (buffered: timings are known before the first write) or announced
// as a trailer (streaming: they are not).
func (c *Composer) writeResponseHeaders(w io.Writer, timings []islandTiming, trailer bool) {
	hw, ok := w.(http.ResponseWriter)
	if !ok {
		return
	}
	if hw.Header().Get("Content-Type") == "" {
		hw.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	SetVary(hw)
	if trailer {
		hw.Header().Set("Trailer", "Server-Timing")
		return
	}
	if len(timings) > 0 {
		hw.Header().Set("Server-Timing", serverTimingHeader(timings))
	}
}

// writeBody writes status and bytes for a buffered response. The status is
// written only when the page asked for a specific one, keeping the implicit
// 200 the default for every ordinary page.
func writeBody(w io.Writer, status int, data []byte) error {
	hw, ok := w.(http.ResponseWriter)
	if !ok {
		_, err := w.Write(data)
		return err
	}
	if status != 0 {
		hw.WriteHeader(status)
	}
	_, err := hw.Write(data)
	return err
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
// Client-mode island fragments (e.g. GET /catalog/{id}/stock). The response
// is the bare island: no marker wrapper, because the fragment is the whole
// body the browser asked for.
func (c *Composer) RenderComponent(
	ctx context.Context,
	w io.Writer,
	component *Component,
	rctx RenderingContext,
) error {
	html, _, _, _, err := c.renderIsland(ctx, component, rctx, false)
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

// renderIslandRegion renders one island and returns the region as it appears
// in the document — server islands wrapped in their ds-island marker, Client
// placeholders already carrying their own — plus the island's timing and any
// fatal error.
//
// Client placeholders are deliberately not timed or counted: their server
// work is trivial and constant, and their names may embed ids
// (detail.<id>.stock), which would put an unbounded label set into the
// metrics. A zero-value timing (empty name) means "nothing to report".
func (c *Composer) renderIslandRegion(
	ctx context.Context,
	component *Component,
	rctx RenderingContext,
	streaming bool,
) ([]byte, islandTiming, error) {
	start := time.Now()
	html, mode, fallback, cause, err := c.renderIsland(ctx, component, rctx, streaming)

	if mode == ModeClient {
		return html, islandTiming{}, err
	}

	timing := islandTiming{
		name:     component.Name,
		duration: time.Since(start),
		fallback: fallback,
	}

	switch {
	case err != nil:
		// A cancelled parent is the client leaving, not an island
		// defect: recording it would poison the island's error rate.
		if ctx.Err() == nil {
			recordIsland(component.Name, timing.duration, islandResult(err), false)
		}
		return nil, timing, err
	case cause != nil:
		// The fallback shipped, but the underlying failure is counted
		// under its own cause: a fallback rate an operator cannot
		// decompose into errors and timeouts hides the incident.
		recordIsland(component.Name, timing.duration, islandResult(cause), true)
	default:
		recordIsland(component.Name, timing.duration, resultOK, false)
	}

	return wrapIsland(component.Name, html), timing, nil
}

// renderIsland renders one island with its decided strategy, its Load, its
// timeout and its fallback substitution. The returned flags separate three
// outcomes an operator needs to tell apart: a clean render (fallback false,
// cause nil), a substituted render (fallback true, cause set to the failure
// the fallback covers), and a fatal one (err set; no bytes may be used).
func (c *Composer) renderIsland(
	ctx context.Context,
	component *Component,
	rctx RenderingContext,
	streaming bool,
) (html []byte, mode RenderMode, fallback bool, cause error, err error) {
	decision := c.planner.Decide(component.Contract, rctx)
	mode = decision.Mode

	if mode == ModeClient {
		html, err = c.renderClientPlaceholder(ctx, component)
		return html, mode, false, nil, err
	}

	timeout := component.Timeout
	if timeout == 0 && streaming {
		timeout = DefaultIslandTimeout
	}
	islandCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		islandCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	html, err = c.renderServerModes(islandCtx, component, decision, timeout)
	if err == nil {
		return html, mode, false, nil, nil
	}

	// Substitute the fallback unless the request itself is over: a gone
	// client gets no rendering at all, fallback or not. The fallback
	// renders on a context that keeps the request's values (tenant,
	// identity) but not the island's expired deadline — a deadline that
	// already fired must not abort the very component that exists to
	// degrade gracefully.
	if ctx.Err() != nil || component.Fallback == nil {
		return nil, mode, false, err, err
	}
	html, fbErr := renderTempl(context.WithoutCancel(islandCtx), component.Fallback)
	if fbErr != nil {
		// The fallback itself failing is a wiring bug; the original
		// cause is the honest error to surface.
		return nil, mode, false, err, err
	}
	return html, mode, true, err, nil
}

// renderServerModes renders the island according to its decided mode,
// threading the data loader through so a cache hit never pays for Load.
func (c *Composer) renderServerModes(
	ctx context.Context,
	component *Component,
	decision RenderDecision,
	timeout time.Duration,
) ([]byte, error) {
	load := islandLoader(component)

	switch decision.Mode {
	case ModeStatic:
		return c.renderCached(ctx, component, load, 0, timeout)
	case ModeRevalidate:
		return c.renderCached(ctx, component, load, decision.CacheTTL, timeout)
	default: // ModeDynamic
		return renderLoaded(ctx, load)
	}
}

// islandLoaderFn produces the component to render for one island render.
type islandLoaderFn func(ctx context.Context) (templ.Component, error)

// islandLoader returns the island's data loader: its Load when it declares
// one, otherwise a loader yielding its static Body. Everything downstream
// calls the loader and never touches Body directly, so “is this island's data
// fetched here?” has exactly one answer.
func islandLoader(component *Component) islandLoaderFn {
	if component.Load != nil {
		return component.Load
	}
	return func(context.Context) (templ.Component, error) {
		return component.Body, nil
	}
}

// renderLoaded loads the island's data and renders it. Used by ModeDynamic
// and by cached modes on a miss — after the cache already said no.
func renderLoaded(ctx context.Context, load islandLoaderFn) ([]byte, error) {
	body, err := load(ctx)
	if err != nil {
		return nil, err
	}
	return renderTempl(ctx, body)
}

// renderCached returns the cached HTML of the island when present and
// fresh, otherwise renders and stores it. Only Public, non-personalized
// islands are cached, so cached content can never leak across viewers. The
// same predicate gates both the read and the write, so an island is never
// read from a cache it would not have been written to.
//
// Cache hits return before Load runs: the cached bytes are the island, and
// re-fetching the data that produced them would defeat the cache wholesale.
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
	load islandLoaderFn,
	ttl time.Duration,
	timeout time.Duration,
) ([]byte, error) {
	cachable := c.cache != nil && component.Contract.Public && !component.Contract.Personalized

	if cachable {
		if data, ok := c.cache.Get(c.islandKey(ctx, component.Name)); ok {
			return data, nil
		}
		return c.renderCoalesced(ctx, component, load, ttl, timeout)
	}

	return renderLoaded(ctx, load)
}

// renderCoalesced renders a cacheable island exactly once per key across
// concurrent callers and stores the result. The render runs on a context
// detached from any single caller, so one cancelled request cannot abort the
// shared work; waiters still select on their own context to return early.
// The island's timeout is re-attached to the detached context, so a shared
// render is bounded too — an unbounded one would keep the singleflight slot
// (and any goroutine it spawned) alive indefinitely.
//
// The write is guarded by the island's invalidation generation: if the island
// was invalidated while the render was in flight, the freshly rendered bytes
// are stale and are NOT stored, so a concurrent mutation cannot leave
// pre-mutation HTML in the cache until the next event.
func (c *Composer) renderCoalesced(
	ctx context.Context,
	component *Component,
	load islandLoaderFn,
	ttl time.Duration,
	timeout time.Duration,
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
		if timeout > 0 {
			var cancel context.CancelFunc
			renderCtx, cancel = context.WithTimeout(renderCtx, timeout)
			defer cancel()
		}

		data, err := renderLoaded(renderCtx, load)
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

// CachedIsland returns the already-rendered HTML of a cacheable island when
// it is present and fresh, so a caller can serve an island without building
// (and fetching the data for) the component. It applies the same gate as
// renderCached — only Public, non-personalized islands are ever read — so a
// cached island is never served to a viewer it was not writable for. A miss
// returns ok=false, leaving the full render path (with its coalescing and
// stampede protection) to do the work.
//
// This is the fast path for HTMX fragment requests: after the first render,
// a fragment swap can be answered straight from the cache with no data fetch
// on the backing service.
func (c *Composer) CachedIsland(ctx context.Context, component *Component) (data []byte, ok bool) {
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

// renderClientPlaceholder produces the placeholder markup: a scoped island
// element that lazy-loads its content from FetchURL (htmx lazy-load pattern).
func (c *Composer) renderClientPlaceholder(ctx context.Context, component *Component) ([]byte, error) {
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
