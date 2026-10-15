// Package projection reports how far the read models have caught up.
//
// This is the one collaborator in the aggregator that speaks HTTP rather than
// gRPC, and that is not an oversight. The two read services expose their status
// endpoints on REST only: there is no status RPC in either the listing or the
// search contract. Adding an interface here, rather than extending the gRPC
// clients, keeps that asymmetry visible in one place instead of scattering it.
//
// Surfacing the status matters because of how CQRS fails. A stalled projection
// does not error: reads keep succeeding and keep answering with stale data. A
// browse that omits a product that was created ten minutes ago looks identical to
// a browse of an empty catalogue. This is the difference between an operator who
// knows the projection is stuck and one who concludes the write failed.
package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aggregator-service/internal/graph/model"
)

// maxResponseBytes caps the status response. The endpoints return a small object;
// anything larger means something is wrong that is not worth parsing.
const maxResponseBytes = 64 << 10

// statusResponse is the shape both read services return.
//
// The field names differ between the two services: the listing endpoint reports
// "products" and the search endpoint reports "documents". They are decoded into
// separate structs rather than one struct with both fields, so a change to either
// contract fails loudly at decode time instead of silently reading a zero.
type listingStatusResponse struct {
	Products      int64 `json:"products"`
	AppliedEvents int64 `json:"applied_events"`
	Healthy       bool  `json:"healthy"`
}

type searchStatusResponse struct {
	Documents     int64 `json:"documents"`
	AppliedEvents int64 `json:"applied_events"`
	Healthy       bool  `json:"healthy"`
}

// Reporter queries both read services' status endpoints.
type Reporter struct {
	listingBase string
	searchBase  string
	client      *http.Client
	// transport is the instrumented round tripper. Injected so this package does not
	// depend on the tracing library: the composition root supplies either an
	// otelhttp transport or the plain default, and a no-op one is the zero value.
	transport http.RoundTripper
}

// Options configures a Reporter.
type Options struct {
	// ListingBase is the listing service's REST origin.
	ListingBase string
	// SearchBase is the search service's REST origin.
	SearchBase string
	// Timeout bounds one status request. It is deliberately shorter than the gRPC
	// dependency timeout: this call is on the path of a write response and of
	// projectionHealth, and neither is worth a long wait for a status number.
	Timeout time.Duration
	// Transport instruments the outbound request. Nil uses http.DefaultTransport.
	Transport http.RoundTripper
}

// New builds a Reporter.
func New(opts Options) *Reporter {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	transport := opts.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &Reporter{
		listingBase: strings.TrimRight(opts.ListingBase, "/"),
		searchBase:  strings.TrimRight(opts.SearchBase, "/"),
		transport:   transport,
		client: &http.Client{
			Timeout: timeout,
			// Redirects are not followed. A status endpoint that redirects is
			// misconfigured, and following it could send a request to an address
			// nobody reviewed.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: transport,
		},
	}
}

// ListingStatus reports the browse projection's progress.
func (r *Reporter) ListingStatus(ctx context.Context) (model.ProjectionStatus, error) {
	var payload listingStatusResponse
	if err := r.get(ctx, r.listingBase+"/api/v1/listing/status", &payload); err != nil {
		return model.ProjectionStatus{}, fmt.Errorf("listing projection status: %w", err)
	}
	return model.ProjectionStatus{
		Records:       payload.Products,
		AppliedEvents: payload.AppliedEvents,
		Healthy:       payload.Healthy,
		Source:        "product-listing-service",
	}, nil
}

// SearchStatus reports the search projection's progress.
func (r *Reporter) SearchStatus(ctx context.Context) (model.ProjectionStatus, error) {
	var payload searchStatusResponse
	if err := r.get(ctx, r.searchBase+"/api/v1/search/status", &payload); err != nil {
		return model.ProjectionStatus{}, fmt.Errorf("search projection status: %w", err)
	}
	return model.ProjectionStatus{
		Records:       payload.Documents,
		AppliedEvents: payload.AppliedEvents,
		Healthy:       payload.Healthy,
		Source:        "product-search-service",
	}, nil
}

// get performs one status request.
func (r *Reporter) get(ctx context.Context, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	// Declared explicitly so an intermediary does not answer with a cached status
	// and report a stalled projection as healthy.
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		// Draining before closing lets the connection return to the pool. Without
		// it every status query leaks a connection until the timeout closes it,
		// which under load looks like an exhaustible pool for no visible reason.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
