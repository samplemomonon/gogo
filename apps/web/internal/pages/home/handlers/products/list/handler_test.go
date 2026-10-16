// Package homelist covers what the home listing handler owns rather than what
// pagekit already owns: the mapping from a listing failure to the storefront's
// HTTP answer (an outage is a 503, everything else a 500), and the translation
// of the pagekit query (page number, limit, search term) into the page's own
// port query. Paging keys, caching and fragment negotiation are covered in
// internal/shared/web/pagekit and internal/pages/home/page_test.go; this file
// does not repeat them.
package homelist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogo/internal/pages/home/application/ports"
	"gogo/internal/pages/home/application/results"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/web/pagekit"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubListing is an in-memory ports.Listing that records the query it was
// asked for, so the handler's translation can be asserted without gRPC.
type stubListing struct {
	result results.Listing
	err    error

	calls int
	got   ports.Query
}

var _ ports.Listing = (*stubListing)(nil)

func (s *stubListing) ListPage(_ context.Context, q ports.Query) (results.Listing, error) {
	s.calls++
	s.got = q
	if s.err != nil {
		return results.Listing{}, s.err
	}
	return s.result, nil
}

func (*stubListing) GetProduct(context.Context, string) (*results.Product, error) {
	return nil, nil
}

func newTestHandler(t *testing.T, listing ports.Listing) *Handler {
	t.Helper()
	return NewHandler(listing, pagekit.NewComposer(), discardLogger())
}

func get(t *testing.T, handler *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.Handle(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestClassifyFetchError pins the status mapping that keeps an outage
// distinguishable from an empty catalogue: the platform's outage sentinel —
// bare or wrapped, since errors.Is is the contract — answers 503, while a
// business rejection or an unclassifiable error is a 500 because the page
// cannot tell the client anything true about it.
func TestClassifyFetchError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "platform outage",
			err:  productconsumer.ErrProductPlatformUnavailable,
			want: http.StatusServiceUnavailable,
		},
		{
			name: "wrapped platform outage",
			err:  fmt.Errorf("list page: %w", productconsumer.ErrProductPlatformUnavailable),
			want: http.StatusServiceUnavailable,
		},
		{
			name: "business rejection",
			err:  productconsumer.ErrNotFound,
			want: http.StatusInternalServerError,
		},
		{
			name: "unclassified failure",
			err:  errors.New("boom"),
			want: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := classifyFetchError(tt.err); got != tt.want {
				t.Errorf("classifyFetchError(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestTheHandlerTranslatesPageAndTermIntoThePortQuery pins the handler's half
// of the paging contract: the pagekit page number becomes an offset at the
// page's own limit, and the active search term is forwarded unchanged.
func TestTheHandlerTranslatesPageAndTermIntoThePortQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     string
		wantTerm   string
		wantOffset int
		wantLimit  int
	}{
		{name: "page and term", target: "/?page=3&q=kopi", wantTerm: "kopi", wantOffset: 40, wantLimit: 20},
		{name: "first page", target: "/", wantTerm: "", wantOffset: 0, wantLimit: 20},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listing := &stubListing{result: results.Listing{
				Products: []*results.Product{{ID: "1", Name: "Kopi", Price: 1000, Stock: 1}},
				Total:    45,
			}}
			rec := get(t, newTestHandler(t, listing), tt.target)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if listing.calls != 1 {
				t.Fatalf("port calls = %d, want 1", listing.calls)
			}
			if listing.got.Term != tt.wantTerm {
				t.Errorf("Term = %q, want %q", listing.got.Term, tt.wantTerm)
			}
			if listing.got.Offset != tt.wantOffset {
				t.Errorf("Offset = %d, want %d", listing.got.Offset, tt.wantOffset)
			}
			if listing.got.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d (the handler's page size)", listing.got.Limit, tt.wantLimit)
			}
		})
	}
}

// TestAnOutageAnswers503WithTheRetryMessage pins the classifier's installation:
// the storefront must answer a platform outage with 503 and the client-facing
// message, not render an empty grid that reads like a real answer.
func TestAnOutageAnswers503WithTheRetryMessage(t *testing.T) {
	t.Parallel()

	listing := &stubListing{err: fmt.Errorf("browse: %w", productconsumer.ErrProductPlatformUnavailable)}
	rec := get(t, newTestHandler(t, listing), "/")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Gagal memuat produk.") {
		t.Error("an outage should carry the client-facing retry message")
	}
	if strings.Contains(rec.Body.String(), "Belum ada produk") {
		t.Error("an outage rendered the empty catalogue, which reads as a real answer")
	}
}

// TestAnUnclassifiedFailureAnswers500 pins the other half of the classifier:
// a failure the page cannot name is a 500, because the request was well-formed
// and the page has no honest explanation to give the client.
func TestAnUnclassifiedFailureAnswers500(t *testing.T) {
	t.Parallel()

	listing := &stubListing{err: errors.New("boom")}
	rec := get(t, newTestHandler(t, listing), "/")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Gagal memuat produk.") {
		t.Error("a failed fetch should carry the client-facing message")
	}
}
