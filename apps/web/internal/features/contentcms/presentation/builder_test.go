// This file pins the read path that step 1 of DECISION_CONTENT_SURFACES.md
// depends on: the documented request shape, the response parse, the error
// taxonomy behind the handler's 404-vs-503 split, the cache bounds, and the
// rule that the public key never reaches an error. content_test.go covers the
// happy path and the oversized-response and cross-host-redirect guards; this
// file covers the rest.
package presentation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gogo/internal/shared/breaker"

	"gogo/internal/features/contentcms/application/ports"
)

// testAPIKey is the public read key every test reader carries; one of the tests
// below pins that it never appears in a returned error.
const testAPIKey = "public-test-key"

// publishedPageFixture is a response in the shape the published Content API
// returns, covering every block type of the documented model. It stands in for
// a captured response until the target Builder.io project exists (acceptance
// checklist, docs/CMS_CONTENT_MODEL.md).
const publishedPageFixture = `[{"data":{"slug":"faq","title":"Pertanyaan Umum","blocks":[` +
	`{"type":"hero","title":"Bantuan","imageUrl":"/assets/faq.webp","imageAlt":"Ilustrasi bantuan"},` +
	`{"type":"markdown","title":"Panduan","body":"## Memulai\n\nBaca **panduan**."},` +
	`{"type":"faq","items":[{"question":"Bagaimana?","answer":"Baca panduan."}]},` +
	`{"type":"cta","label":"Hubungi kami","href":"https://example.test/help"}]}}]`

// testReader builds a BuilderReader whose wire is the test's transport; every
// other field is the production shape.
func testReader(transport http.RoundTripper) *BuilderReader {
	return &BuilderReader{
		model:    "editorial-page",
		apiKey:   testAPIKey,
		endpoint: "https://cms.example/api/v3/content/",
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: checkBuilderRedirect,
		},
		breaker: breaker.NewDefault(),
		cache:   make(map[string]cacheEntry),
	}
}

// answer returns a transport that answers every request with status and body,
// counting requests so caching and breaker behavior can be asserted.
func answer(status int, body string) (roundTripFunc, *atomic.Int32) {
	var requests atomic.Int32
	return func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	}, &requests
}

func TestBuilderReaderSendsTheDocumentedReadRequest(t *testing.T) {
	t.Parallel()

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v3/content/editorial-page" {
			t.Errorf("path = %q, want the configured model in the path", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("apiKey") != testAPIKey {
			t.Errorf("apiKey = %q, want the configured public key", query.Get("apiKey"))
		}
		if query.Get("limit") != "1" {
			t.Errorf("limit = %q, want 1", query.Get("limit"))
		}
		if query.Get("fields") != "data" {
			t.Errorf("fields = %q, want data", query.Get("fields"))
		}
		if query.Get("query") != `{"data.slug":"faq"}` {
			t.Errorf(`query = %q, want {"data.slug":"faq"}`, query.Get("query"))
		}
		if query.Has("preview") || query.Has("includeUnpublished") {
			t.Errorf("draft parameters must never be sent: %v", query)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(publishedPageFixture)),
			Request:    r,
		}, nil
	})

	page, err := testReader(transport).ReadPublished(context.Background(), "faq")
	if err != nil {
		t.Fatalf("ReadPublished() error = %v", err)
	}
	if page.Title != "Pertanyaan Umum" {
		t.Fatalf("title = %q, want the parsed fixture", page.Title)
	}
}

func TestBuilderReaderParsesTheDocumentedFixture(t *testing.T) {
	t.Parallel()

	transport, _ := answer(http.StatusOK, publishedPageFixture)
	page, err := testReader(transport).ReadPublished(context.Background(), "faq")
	if err != nil {
		t.Fatalf("ReadPublished() error = %v", err)
	}
	if page.Slug != "faq" || page.Title != "Pertanyaan Umum" || len(page.EditorialBlocks) != 4 {
		t.Fatalf("page = %+v, want the full fixture", page)
	}
	if hero := page.EditorialBlocks[0]; hero.Type != "hero" || hero.ImageURL != "/assets/faq.webp" || hero.ImageAlt != "Ilustrasi bantuan" {
		t.Fatalf("hero block = %+v", hero)
	}
	if faq := page.EditorialBlocks[2]; len(faq.Items) != 1 || faq.Items[0].Question != "Bagaimana?" || faq.Items[0].Answer != "Baca panduan." {
		t.Fatalf("faq block = %+v", faq)
	}
	if cta := page.EditorialBlocks[3]; cta.Label != "Hubungi kami" || cta.Href != "https://example.test/help" {
		t.Fatalf("cta block = %+v", cta)
	}
}

func TestBuilderReaderAnswersNotFoundForCleanMisses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http 404", status: http.StatusNotFound, body: ""},
		{name: "empty result set", status: http.StatusOK, body: "[]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			transport, _ := answer(tt.status, tt.body)
			_, err := testReader(transport).ReadPublished(context.Background(), "faq")
			if !errors.Is(err, ports.ErrNotFound) {
				t.Fatalf("err = %v, want ports.ErrNotFound", err)
			}
			if errors.Is(err, ports.ErrUnavailable) {
				t.Fatalf("a clean miss is not an availability failure: %v", err)
			}
		})
	}
}

func TestBuilderReaderTreatsCleanMissesAsNonFailures(t *testing.T) {
	t.Parallel()

	transport, requests := answer(http.StatusNotFound, "")
	reader := testReader(transport)
	// More misses than the breaker's failure threshold: if a clean miss counted
	// as a failure, the circuit would open and later reads would fail as
	// unavailable instead of the honest ports.ErrNotFound.
	for i := range 8 {
		if _, err := reader.ReadPublished(context.Background(), fmt.Sprintf("miss-%d", i)); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("miss %d: err = %v, want ports.ErrNotFound", i, err)
		}
	}
	if got := requests.Load(); got != 8 {
		t.Fatalf("HTTP requests = %d, want 8 (an opened circuit would fail fast before the wire)", got)
	}
}

func TestBuilderReaderMapsFailureDetailsToUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       string
		wantDetail string
	}{
		{name: "http error", status: http.StatusInternalServerError, body: "boom", wantDetail: "HTTP 500"},
		{name: "malformed json", status: http.StatusOK, body: "{not json", wantDetail: "decode Builder.io content"},
		{
			name:       "unsupported document",
			status:     http.StatusOK,
			body:       `[{"data":{"slug":"faq","title":"FAQ","blocks":[{"type":"script"}]}}]`,
			wantDetail: "invalid Builder.io editorial content",
		},
		{
			name:       "mismatched slug",
			status:     http.StatusOK,
			body:       `[{"data":{"slug":"other","title":"FAQ"}}]`,
			wantDetail: "mismatched editorial slug",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			transport, _ := answer(tt.status, tt.body)
			_, err := testReader(transport).ReadPublished(context.Background(), "faq")
			if !errors.Is(err, ports.ErrUnavailable) {
				t.Fatalf("err = %v, want ports.ErrUnavailable", err)
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("err = %q, want the %q detail: enablement must tell a content problem from a wire problem", err, tt.wantDetail)
			}
		})
	}
}

func TestBuilderReaderNeverLeaksThePublicKey(t *testing.T) {
	t.Parallel()

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		// A realistic transport error embeds the full request URL, key included.
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: errors.New("dial tcp: connection refused")}
	})
	_, err := testReader(transport).ReadPublished(context.Background(), "faq")
	if !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("err = %v, want ports.ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("err = %q leaks the public key", err)
	}
	if strings.Contains(err.Error(), "cms.example") {
		t.Fatalf("err = %q leaks the request URL", err)
	}
}

func TestBuilderReaderRefetchesAfterTheCacheExpires(t *testing.T) {
	t.Parallel()

	transport, requests := answer(http.StatusOK, `[{"data":{"slug":"faq","title":"Baru","blocks":[{"type":"markdown","body":"x"}]}}]`)
	reader := testReader(transport)
	reader.cache["faq"] = cacheEntry{page: Page{Slug: "faq", Title: "Lama"}, expiresAt: time.Now().Add(-time.Second)}

	page, err := reader.ReadPublished(context.Background(), "faq")
	if err != nil {
		t.Fatalf("ReadPublished() error = %v", err)
	}
	if page.Title != "Baru" {
		t.Fatalf("title = %q, want the fresh fetch", page.Title)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1", got)
	}

	reader.mu.Lock()
	entry, cached := reader.cache["faq"]
	reader.mu.Unlock()
	if !cached || !entry.expiresAt.After(time.Now()) {
		t.Fatal("the fresh page was not cached with a live expiry")
	}
}

func TestBuilderReaderDeclinesNewEntriesWhenFullOfLiveEntries(t *testing.T) {
	t.Parallel()

	transport, requests := answer(http.StatusOK, `[{"data":{"slug":"extra","title":"Extra"}}]`)
	reader := testReader(transport)
	for i := range cacheCapacity {
		reader.cache[fmt.Sprintf("old-%d", i)] = cacheEntry{page: Page{Slug: "old"}, expiresAt: time.Now().Add(time.Minute)}
	}

	read := func() {
		t.Helper()
		if _, err := reader.ReadPublished(context.Background(), "extra"); err != nil {
			t.Fatalf("ReadPublished() error = %v", err)
		}
	}
	read()
	read() // the declined entry is not cached, so this refetches

	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP requests = %d, want 2 (a full cache of live entries declines new ones)", got)
	}
	reader.mu.Lock()
	_, cached := reader.cache["extra"]
	size := len(reader.cache)
	reader.mu.Unlock()
	if cached || size != cacheCapacity {
		t.Fatalf("cache grew past its bound: cached=%v size=%d", cached, size)
	}
}

func TestBuilderReaderEvictsExpiredEntriesToAdmitNewOnes(t *testing.T) {
	t.Parallel()

	transport, requests := answer(http.StatusOK, `[{"data":{"slug":"extra","title":"Extra"}}]`)
	reader := testReader(transport)
	for i := range cacheCapacity {
		reader.cache[fmt.Sprintf("stale-%d", i)] = cacheEntry{page: Page{Slug: "stale"}, expiresAt: time.Now().Add(-time.Second)}
	}

	for range 2 {
		if _, err := reader.ReadPublished(context.Background(), "extra"); err != nil {
			t.Fatalf("ReadPublished() error = %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (expired entries make room)", got)
	}
	reader.mu.Lock()
	size := len(reader.cache)
	reader.mu.Unlock()
	if size != 1 {
		t.Fatalf("cache size = %d, want 1 (only the fresh entry survives eviction)", size)
	}
}

func TestBuilderReaderRejectsAnInvalidSlugWithoutAFetch(t *testing.T) {
	t.Parallel()

	transport, requests := answer(http.StatusOK, publishedPageFixture)
	_, err := testReader(transport).ReadPublished(context.Background(), "Faq/../secrets")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("err = %v, want ports.ErrNotFound", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0 (invalid slugs never reach the wire)", got)
	}
}

func TestBuilderReaderReturnsIsolatedCopies(t *testing.T) {
	t.Parallel()

	transport, _ := answer(http.StatusOK, publishedPageFixture)
	reader := testReader(transport)

	first, err := reader.ReadPublished(context.Background(), "faq")
	if err != nil {
		t.Fatalf("ReadPublished() error = %v", err)
	}
	first.EditorialBlocks[2].Items[0].Question = "MUTATED"

	second, err := reader.ReadPublished(context.Background(), "faq")
	if err != nil {
		t.Fatalf("ReadPublished() error = %v", err)
	}
	if second.EditorialBlocks[2].Items[0].Question == "MUTATED" {
		t.Fatal("a caller's mutation reached the cache")
	}
}

func TestBuilderReaderFollowsOnlySameHostHTTPSRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		location     string
		wantRequests int32
		wantErr      bool
	}{
		{name: "same host https", location: "https://cms.example/api/v3/content/editorial-page-moved", wantRequests: 2},
		{name: "insecure downgrade", location: "http://cms.example/api/v3/content/editorial-page-moved", wantRequests: 1, wantErr: true},
		{name: "cross host", location: "https://attacker.example/collect", wantRequests: 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				if r.URL.Path == "/api/v3/content/editorial-page" {
					return &http.Response{
						StatusCode: http.StatusFound,
						Header:     http.Header{"Location": []string{tt.location}},
						Body:       io.NopCloser(strings.NewReader("")),
						Request:    r,
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(publishedPageFixture)),
					Request:    r,
				}, nil
			})

			_, err := testReader(transport).ReadPublished(context.Background(), "faq")
			if tt.wantErr && err == nil {
				t.Fatal("ReadPublished() error = nil, want a refusal")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ReadPublished() error = %v", err)
			}
			if got := requests.Load(); got != tt.wantRequests {
				t.Fatalf("HTTP requests = %d, want %d", got, tt.wantRequests)
			}
		})
	}
}

func TestBuilderReaderServesConcurrentReads(t *testing.T) {
	t.Parallel()

	transport, requests := answer(http.StatusOK, publishedPageFixture)
	reader := testReader(transport)

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := reader.ReadPublished(context.Background(), "faq"); err != nil {
				t.Errorf("ReadPublished() error = %v", err)
			}
		}()
	}
	wg.Wait()

	if got := requests.Load(); got < 1 || got > 32 {
		t.Fatalf("HTTP requests = %d, want within [1, 32]", got)
	}
}

func TestNewBuilderReaderValidatesConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewBuilderReader("", "key"); !errors.Is(err, ports.ErrUnavailable) {
		t.Errorf("empty model: err = %v, want ports.ErrUnavailable", err)
	}
	if _, err := NewBuilderReader("editorial-page", ""); !errors.Is(err, ports.ErrUnavailable) {
		t.Errorf("empty key: err = %v, want ports.ErrUnavailable", err)
	}
	if _, err := NewBuilderReader("Editorial_Page", "key"); err == nil {
		t.Error("an invalid model name was accepted")
	}
	reader, err := NewBuilderReader("editorial-page", "key")
	if err != nil {
		t.Fatalf("NewBuilderReader() error = %v", err)
	}
	if reader.endpoint != builderContentURL {
		t.Fatalf("endpoint = %q, want the production content URL", reader.endpoint)
	}
}
