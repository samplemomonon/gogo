package render

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
)

// flushSpy is an http.ResponseWriter that records the body content at every
// Flush, so a test can prove delivery was progressive — island N's bytes out
// before island N+1 rendered — rather than observe only the final document.
type flushSpy struct {
	header  http.Header
	body    bytes.Buffer
	flushed []string
}

func newFlushSpy() *flushSpy {
	return &flushSpy{header: make(http.Header)}
}

func (s *flushSpy) Header() http.Header         { return s.header }
func (s *flushSpy) Write(p []byte) (int, error) { return s.body.Write(p) }
func (s *flushSpy) WriteHeader(int)             {}
func (s *flushSpy) Flush() {
	s.flushed = append(s.flushed, s.body.String())
}

// textComponent is a templ component that writes one literal string.
func textComponent(text string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

// shellLayout writes a head and a tail around the composed islands, the shape
// the real Base layout has.
func shellLayout(inner templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, "<html><body>"); err != nil {
			return err
		}
		if err := inner.Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "</body></html>")
		return err
	})
}

func TestStreamingDeliversProgressively(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	page := &Page{
		Streaming: true,
		Layout:    shellLayout,
		Components: []*Component{
			Island("render.stream-fast", textComponent("fast-island"), RenderingContract{Freshness: FreshnessRealtime}),
			Island("render.stream-slow", textComponent("slow-island"), RenderingContract{Freshness: FreshnessRealtime}),
		},
	}

	spy := newFlushSpy()
	if err := composer.RenderPage(context.Background(), spy, page, RenderingContext{}); err != nil {
		t.Fatalf("RenderPage returned error: %v", err)
	}

	body := spy.body.String()
	fastAt := strings.Index(body, "fast-island")
	slowAt := strings.Index(body, "slow-island")
	if fastAt < 0 || slowAt < 0 || fastAt > slowAt {
		t.Fatalf("body = %q, want fast island before slow island", body)
	}
	if !strings.HasPrefix(body, "<html><body>") || !strings.HasSuffix(body, "</body></html>") {
		t.Errorf("body = %q, want the layout around the islands", body)
	}

	// The point of streaming: the fast island reached the client (it was
	// flushed) while the slow island had not yet rendered.
	progressive := false
	for _, snapshot := range spy.flushed {
		if strings.Contains(snapshot, "fast-island") && !strings.Contains(snapshot, "slow-island") {
			progressive = true
			break
		}
	}
	if !progressive {
		t.Errorf("no flush delivered the fast island before the slow one; flushes = %q", spy.flushed)
	}

	if got := spy.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	if got := spy.Header().Get("Vary"); !strings.Contains(got, "HX-Request") {
		t.Errorf("Vary = %q, want HX-Request", got)
	}
	if got := spy.Header().Get("Trailer"); got != "Server-Timing" {
		t.Errorf("Trailer = %q, want Server-Timing announced before the first write", got)
	}
	timing := spy.Header().Get("Server-Timing")
	for _, want := range []string{"render.stream-fast", "render.stream-slow"} {
		if !strings.Contains(timing, want) {
			t.Errorf("Server-Timing = %q, want entry for %q", timing, want)
		}
	}
}

func TestStreamingIsIgnoredForFragments(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	page := &Page{
		Streaming:  true,
		Layout:     shellLayout,
		Components: []*Component{Island("render.frag-swap", textComponent("swapped"), RenderingContract{Freshness: FreshnessRealtime})},
	}

	spy := newFlushSpy()
	if err := composer.RenderPage(context.Background(), spy, page, RenderingContext{IsHTMX: true}); err != nil {
		t.Fatalf("RenderPage returned error: %v", err)
	}

	if len(spy.flushed) != 0 {
		t.Errorf("fragment responses must stay buffered; got %d flushes", len(spy.flushed))
	}
	if !strings.Contains(spy.body.String(), "swapped") {
		t.Errorf("body = %q, want the island content", spy.body.String())
	}
}

func TestBufferedResponseIsAtomicOnIslandError(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	page := &Page{
		Layout: shellLayout,
		Components: []*Component{
			Island("render.atomic", templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
				if _, err := io.WriteString(w, "<half-an-island>"); err != nil {
					return err
				}
				return errors.New("render failed mid-island")
			}), RenderingContract{Freshness: FreshnessRealtime}),
		},
	}

	spy := newFlushSpy()
	err := composer.RenderPage(context.Background(), spy, page, RenderingContext{})
	if err == nil {
		t.Fatal("RenderPage should return the island error")
	}
	if spy.body.Len() != 0 {
		t.Errorf("buffered response leaked partial output: %q", spy.body.String())
	}
}

func TestIslandLoadErrorRendersFallback(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	comp := Island("render.broken-load", nil, RenderingContract{Freshness: FreshnessRealtime})
	comp.Load = func(context.Context) (templ.Component, error) {
		return nil, errors.New("catalog unreachable")
	}
	comp.Fallback = textComponent("fallback-content")

	html := renderPage(t, composer, &Page{Layout: shellLayout, Components: []*Component{comp}})

	if !strings.Contains(html, "fallback-content") {
		t.Errorf("page = %q, want the fallback content", html)
	}
	if !strings.Contains(html, `data-island="render.broken-load"`) {
		t.Error("fallback should stay inside the island's marker element")
	}

	stat := IslandCounters()["render.broken-load"]
	if stat.Renders != 1 || stat.Fallbacks != 1 || stat.Errors != 1 {
		t.Errorf("stat = %+v, want 1 render, 1 fallback, 1 error", stat)
	}
}

func TestIslandTimeoutRendersFallback(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	comp := Island("render.timed-out", nil, RenderingContract{Freshness: FreshnessRealtime})
	comp.Timeout = 10 * time.Millisecond
	comp.Load = func(ctx context.Context) (templ.Component, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	comp.Fallback = textComponent("timeout-fallback")

	html := renderPage(t, composer, &Page{Layout: shellLayout, Components: []*Component{comp}})

	if !strings.Contains(html, "timeout-fallback") {
		t.Errorf("page = %q, want the timeout fallback", html)
	}

	stat := IslandCounters()["render.timed-out"]
	if stat.Timeouts != 1 {
		t.Errorf("stat = %+v, want 1 timeout", stat)
	}
}

func TestIslandWithoutFallbackFailsTheRender(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	comp := Island("render.no-fallback", nil, RenderingContract{Freshness: FreshnessRealtime})
	comp.Load = func(context.Context) (templ.Component, error) {
		return nil, errors.New("no substitute declared")
	}

	var buf bytes.Buffer
	err := composer.RenderPage(context.Background(), &buf, &Page{Layout: shellLayout, Components: []*Component{comp}}, RenderingContext{})
	if err == nil {
		t.Fatal("RenderPage should surface the island error when no fallback is declared")
	}
}

func TestLoadIsSkippedOnCacheHit(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	comp := Island("render.cached-load", nil, RenderingContract{Public: true, CacheTTL: time.Minute})
	loadCalls := 0
	comp.Load = func(context.Context) (templ.Component, error) {
		loadCalls++
		return textComponent("loaded-once"), nil
	}

	for i := 0; i < 3; i++ {
		html := renderPage(t, composer, &Page{Layout: shellLayout, Components: []*Component{comp}})
		if !strings.Contains(html, "loaded-once") {
			t.Fatalf("render %d: page = %q, want the loaded content", i, html)
		}
	}
	if loadCalls != 1 {
		t.Errorf("Load ran %d times across three requests, want 1 (cache hits must not re-fetch)", loadCalls)
	}
}

func TestServerTimingHeaderSanitizesIslandNames(t *testing.T) {
	t.Parallel()

	header := serverTimingHeader([]islandTiming{
		{name: "home.product-grid:p2", duration: 12300 * time.Microsecond},
		{name: "render.fallback-case", duration: 4000 * time.Microsecond, fallback: true},
	})

	if !strings.Contains(header, "island-home.product-grid_p2;dur=12.3") {
		t.Errorf("header = %q, want the paged name sanitized to the token charset", header)
	}
	if !strings.Contains(header, `;desc="fallback"`) {
		t.Errorf("header = %q, want the fallback marker", header)
	}
}
