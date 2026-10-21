package presentation

import (
	"gogo/internal/features/contentcms/application/ports"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gogo/internal/shared/breaker"
)

const (
	builderContentURL = "https://cdn.builder.io/api/v3/content/"
	maxResponseBytes  = 1 << 20
	cacheTTL          = 60 * time.Second
	cacheCapacity     = 64
)

// errBuilderTransportFailed replaces transport errors, which may embed the full
// request URL — and the public read key is a query parameter — so they must
// never reach a log. Every other failure detail names only this package's own
// finds (a status, a document shape) and is kept for enablement debugging.
var errBuilderTransportFailed = errors.New("Builder.io content transport failed")

// BuilderReader consumes only published Builder.io content. The public API
// key has read access; authoring and preview credentials never belong here.
type BuilderReader struct {
	model    string
	apiKey   string
	endpoint string
	client   *http.Client
	breaker  *breaker.Breaker
	mu       sync.Mutex
	cache    map[string]cacheEntry
}

type cacheEntry struct {
	page      ports.EditorialPage
	expiresAt time.Time
}

func NewBuilderReader(model, publicAPIKey string) (*BuilderReader, error) {
	model = strings.TrimSpace(model)
	publicAPIKey = strings.TrimSpace(publicAPIKey)
	if model == "" || publicAPIKey == "" {
		return nil, ports.ErrUnavailable
	}
	if !ports.ValidSlug(model) {
		return nil, errors.New("invalid Builder.io model name")
	}
	return &BuilderReader{
		model:    model,
		apiKey:   publicAPIKey,
		endpoint: builderContentURL,
		client: &http.Client{
			Timeout:       3 * time.Second,
			CheckRedirect: checkBuilderRedirect,
		},
		breaker: breaker.NewDefault(),
		cache:   make(map[string]cacheEntry),
	}, nil
}

func checkBuilderRedirect(request *http.Request, via []*http.Request) error {
	if len(via) == 0 || request.URL.Scheme != "https" || request.URL.Host != via[0].URL.Host || len(via) > 3 {
		return http.ErrUseLastResponse
	}
	return nil
}

// ReadPublished reads a single published page by its stable slug. It never
// asks Builder for drafts or accepts client-selected model names.
func (r *BuilderReader) ReadPublished(ctx context.Context, slug string) (ports.EditorialPage, error) {
	if !ports.ValidSlug(slug) {
		return ports.EditorialPage{}, ports.ErrNotFound
	}
	now := time.Now()
	r.mu.Lock()
	if entry, ok := r.cache[slug]; ok {
		if now.Before(entry.expiresAt) {
			r.mu.Unlock()
			return cloneEditorialPage(entry.page), nil
		}
		delete(r.cache, slug)
	}
	r.mu.Unlock()

	page, err := r.fetch(ctx, slug)
	if err != nil {
		return ports.EditorialPage{}, err
	}
	r.mu.Lock()
	if len(r.cache) >= cacheCapacity {
		// Expired entries are cleared before declining a new cache entry. The
		// cache has a hard memory bound even if callers request many valid slugs.
		for key, entry := range r.cache {
			if !time.Now().Before(entry.expiresAt) {
				delete(r.cache, key)
			}
		}
	}
	if len(r.cache) < cacheCapacity {
		r.cache[slug] = cacheEntry{page: page, expiresAt: time.Now().Add(cacheTTL)}
	}
	r.mu.Unlock()
	return cloneEditorialPage(page), nil
}

func cloneEditorialPage(page ports.EditorialPage) ports.EditorialPage {
	page.EditorialBlocks = append([]ports.EditorialBlock(nil), page.EditorialBlocks...)
	for index := range page.EditorialBlocks {
		page.EditorialBlocks[index].Items = append([]ports.EditorialFAQItem(nil), page.EditorialBlocks[index].Items...)
	}
	return page
}

func (r *BuilderReader) fetch(ctx context.Context, slug string) (ports.EditorialPage, error) {
	var page ports.EditorialPage
	var resultErr error
	err := r.breaker.Execute(func() error {
		endpoint, err := url.Parse(r.endpoint + url.PathEscape(r.model))
		if err != nil {
			return err
		}
		query := endpoint.Query()
		query.Set("apiKey", r.apiKey)
		query.Set("limit", "1")
		query.Set("fields", "data")
		query.Set("query", fmt.Sprintf(`{"data.slug":%q}`, slug))
		endpoint.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return err
		}
		resp, err := r.client.Do(req)
		if err != nil {
			// Do not surface errors that may include the full request URL:
			// Builder's public key is a query parameter and must not leak into
			// logs.
			resultErr = errBuilderTransportFailed
			return resultErr
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			resultErr = ports.ErrNotFound
			return nil // a clean miss is not an upstream availability failure
		}
		if resp.StatusCode != http.StatusOK {
			resultErr = fmt.Errorf("Builder.io content request returned HTTP %d", resp.StatusCode)
			return resultErr
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			resultErr = fmt.Errorf("read Builder.io content: %w", err)
			return resultErr
		}
		if len(body) > maxResponseBytes {
			resultErr = errors.New("Builder.io content exceeds response size limit")
			return resultErr
		}
		var response []struct {
			Data ports.EditorialPage `json:"data"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			resultErr = fmt.Errorf("decode Builder.io content: %w", err)
			return resultErr
		}
		if len(response) == 0 {
			resultErr = ports.ErrNotFound
			return nil
		}
		page = response[0].Data
		if page.Slug != slug {
			resultErr = errors.New("Builder.io returned a mismatched editorial slug")
			return resultErr
		}
		if err := page.Validate(); err != nil {
			resultErr = fmt.Errorf("invalid Builder.io editorial content: %w", err)
			return resultErr
		}
		return nil
	})
	if err != nil {
		// A failure this package constructed (a status, a document problem, a
		// slug mismatch) keeps its detail: enablement must be able to tell bad
		// content from a bad wire. Transport errors are already replaced by the
		// sentinel above, and an opened circuit has no detail, so both stay
		// generic.
		if resultErr != nil && !errors.Is(resultErr, errBuilderTransportFailed) {
			return ports.EditorialPage{}, fmt.Errorf("%w: %w", ports.ErrUnavailable, resultErr)
		}
		return ports.EditorialPage{}, fmt.Errorf("%w: upstream read failed", ports.ErrUnavailable)
	}
	if resultErr != nil {
		return ports.EditorialPage{}, resultErr
	}
	return page, nil
}
