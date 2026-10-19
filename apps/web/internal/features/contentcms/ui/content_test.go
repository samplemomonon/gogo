package ui

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"gogo/internal/shared/breaker"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPageValidateAllowsOnlyDeclaredBlocksAndSafeLinks(t *testing.T) {
	t.Parallel()

	valid := Page{
		Slug:  "faq",
		Title: "Pertanyaan Umum",
		Blocks: []Block{
			{Type: "hero", ImageURL: "/assets/faq.webp"},
			{Type: "markdown", Body: "## Cara menggunakan layanan"},
			{Type: "faq", Items: []FAQItem{{Question: "Bagaimana?", Answer: "Baca panduan."}}},
			{Type: "cta", Label: "Bantuan", Href: "https://example.test/help"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for name, page := range map[string]Page{
		"unknown block":     {Slug: "faq", Title: "FAQ", Blocks: []Block{{Type: "script"}}},
		"unsafe CTA":        {Slug: "faq", Title: "FAQ", Blocks: []Block{{Type: "cta", Label: "Buka", Href: "javascript:alert(1)"}}},
		"protocol relative": {Slug: "faq", Title: "FAQ", Blocks: []Block{{Type: "cta", Label: "Buka", Href: "//attacker.test"}}},
		"invalid slug":      {Slug: "../faq", Title: "FAQ"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := page.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want rejection")
			}
		})
	}
}

func TestMarkdownRenderingEscapesRawHTML(t *testing.T) {
	t.Parallel()

	page := Page{
		Slug: "faq", Title: "FAQ",
		Blocks: []Block{{Type: "markdown", Body: "<script>alert(1)</script>\n\n**aman**"}},
	}
	view, err := renderPage(page)
	if err != nil {
		t.Fatalf("renderPage() error = %v", err)
	}
	body := string(view.Blocks[0].Body)
	if strings.Contains(body, "<script>") {
		t.Fatalf("raw script was rendered: %q", body)
	}
	if !strings.Contains(body, "raw HTML omitted") || !strings.Contains(body, "<strong>aman</strong>") {
		t.Fatalf("Markdown escaping/formatting missing: %q", body)
	}
}

func TestMarkdownRenderingRejectsDangerousLinkSchemes(t *testing.T) {
	t.Parallel()
	page := Page{Slug: "faq", Title: "FAQ", Blocks: []Block{{Type: "markdown", Body: "[tautan](javascript:alert(1))"}}}
	view, err := renderPage(page)
	if err != nil {
		t.Fatalf("renderPage() error = %v", err)
	}
	if strings.Contains(strings.ToLower(string(view.Blocks[0].Body)), "href=\"javascript:") {
		t.Fatalf("dangerous URL rendered: %q", view.Blocks[0].Body)
	}
}

func TestBuilderReaderFetchesPublishedPageAndCachesBoundedEntry(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Query().Get("apiKey") != "public-test-key" {
			t.Errorf("apiKey = %q, want public key", r.URL.Query().Get("apiKey"))
		}
		if r.URL.Query().Has("preview") || r.URL.Query().Has("includeUnpublished") {
			t.Errorf("draft preview parameters must not be sent: %v", r.URL.Query())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`[{"data":{"slug":"faq","title":"FAQ","blocks":[{"type":"markdown","body":"Jawaban **resmi**"}]}}]`)),
			Request:    r,
		}, nil
	})

	reader := &BuilderReader{
		model: "editorial-page", apiKey: "public-test-key", endpoint: "https://cms.example/api/v3/content/",
		client: &http.Client{Transport: transport}, breaker: breaker.NewDefault(), cache: make(map[string]cacheEntry),
	}
	for range 2 {
		page, err := reader.ReadPublished(context.Background(), "faq")
		if err != nil {
			t.Fatalf("ReadPublished() error = %v", err)
		}
		if page.Title != "FAQ" || len(page.Blocks) != 1 {
			t.Fatalf("ReadPublished() = %#v", page)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1 cached request", got)
	}
}

func TestBuilderReaderRejectsOversizedResponse(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))),
			Request:    r,
		}, nil
	})
	reader := &BuilderReader{
		model: "editorial-page", apiKey: "public-test-key", endpoint: "https://cms.example/",
		client: &http.Client{Transport: transport}, breaker: breaker.NewDefault(), cache: make(map[string]cacheEntry),
	}
	if _, err := reader.ReadPublished(context.Background(), "faq"); err == nil {
		t.Fatal("ReadPublished() error = nil, want oversized-body rejection")
	}
}

func TestBuilderReaderDoesNotForwardPublicKeyAcrossRedirectHosts(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://attacker.example/collect"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		}),
		CheckRedirect: checkBuilderRedirect,
	}
	reader := &BuilderReader{
		model: "editorial-page", apiKey: "public-test-key", endpoint: "https://cdn.builder.io/api/v3/content/",
		client: client, breaker: breaker.NewDefault(), cache: make(map[string]cacheEntry),
	}
	if _, err := reader.ReadPublished(context.Background(), "faq"); err == nil {
		t.Fatal("ReadPublished() error = nil, want redirect rejection")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want no cross-host redirect request", got)
	}
}
