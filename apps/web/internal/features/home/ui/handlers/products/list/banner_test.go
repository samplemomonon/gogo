// This file covers the home page's editorial banner slot: the handler resolves
// it before rendering, and the page degrades to no banner when the slot is
// empty or the editorial source fails — the storefront keeps serving its
// products either way (docs/DECISION_CONTENT_SURFACES.md §2 and §5 step 2).
package homelist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
)

// stubBannerSlot is an in-memory ports.BannerSlot that records the slot it was
// asked for, so the handler's slot contract can be asserted without a CMS.
type stubBannerSlot struct {
	banner results.Banner
	err    error

	calls int
	got   string
}

var _ ports.BannerSlot = (*stubBannerSlot)(nil)

func (s *stubBannerSlot) ReadBanner(_ context.Context, slot string) (results.Banner, error) {
	s.calls++
	s.got = slot
	if s.err != nil {
		return results.Banner{}, s.err
	}
	return s.banner, nil
}

// emptySlot is the banner port every non-banner test wires: a page whose slot
// has nothing published.
func emptySlot() *stubBannerSlot { return &stubBannerSlot{err: ports.ErrNoBanner} }

// TestAPublishedBannerRendersOnTheHomePage pins the slot's happy path: a
// published banner reaches the rendered document — the section marker, its
// title and its image — and the handler asked for the page's own slot name.
func TestAPublishedBannerRendersOnTheHomePage(t *testing.T) {
	t.Parallel()

	slot := &stubBannerSlot{banner: results.Banner{
		Title:    "Promo Kemerdekaan",
		ImageURL: "/assets/img/promo-kemerdekaan.webp",
		ImageAlt: "Bendera dan diskon",
	}}
	listing := &stubListing{result: results.Listing{
		Products: []*results.Product{{ID: "1", Name: "Kopi", Price: 1000, Stock: 1}},
		Total:    1,
	}}

	rec := get(t, newTestHandler(t, listing, slot), "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-slot="home-banner"`,
		"Promo Kemerdekaan",
		"/assets/img/promo-kemerdekaan.webp",
		"Bendera dan diskon",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home document missing %q", want)
		}
	}
	if slot.got != BannerSlot {
		t.Errorf("slot = %q, want %q (the page's declared slot)", slot.got, BannerSlot)
	}
	if slot.calls != 1 {
		t.Errorf("banner slot read %d times, want 1", slot.calls)
	}
}

// TestAnEmptySlotRendersNoBanner pins the empty state: ErrNoBanner is not a
// failure — the page serves normally, with no banner markup and no empty frame.
func TestAnEmptySlotRendersNoBanner(t *testing.T) {
	t.Parallel()

	listing := &stubListing{result: results.Listing{
		Products: []*results.Product{{ID: "1", Name: "Kopi", Price: 1000, Stock: 1}},
		Total:    1,
	}}
	rec := get(t, newTestHandler(t, listing, emptySlot()), "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-slot="home-banner"`) {
		t.Error("an empty slot rendered a banner section")
	}
	if !strings.Contains(body, "Kopi") {
		t.Error("an empty slot must not stop the storefront serving its products")
	}
}

// TestABannerFailureDegradesToNoBanner pins the failure path: an editorial
// source failure is logged and rendered as no banner — the storefront keeps
// serving — because products must not depend on the editorial source being up
// (docs/DECISION_CONTENT_SURFACES.md §2).
func TestABannerFailureDegradesToNoBanner(t *testing.T) {
	t.Parallel()

	listing := &stubListing{result: results.Listing{
		Products: []*results.Product{{ID: "1", Name: "Kopi", Price: 1000, Stock: 1}},
		Total:    1,
	}}
	slot := &stubBannerSlot{err: errors.New("editorial source unreachable")}

	rec := get(t, newTestHandler(t, listing, slot), "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a banner failure must not fail the page)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-slot="home-banner"`) {
		t.Error("a failed banner read rendered a banner section")
	}
	if !strings.Contains(body, "Kopi") {
		t.Error("a failed banner read blanked the storefront, which reads through its listing")
	}
}

// TestTheBannerIsNotInTheGridFragment pins the slot's placement: the banner is
// part of the full document, so an HTMX fragment — the grid island htmx swaps
// into the listing — stays the bare listing and never re-renders the banner.
func TestTheBannerIsNotInTheGridFragment(t *testing.T) {
	t.Parallel()

	slot := &stubBannerSlot{banner: results.Banner{Title: "Promo Kemerdekaan"}}
	listing := &stubListing{result: results.Listing{
		Products: []*results.Product{{ID: "1", Name: "Kopi", Price: 1000, Stock: 1}},
		Total:    1,
	}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	newTestHandler(t, listing, slot).Handle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("fragment status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `data-slot="home-banner"`) {
		t.Error("the grid fragment re-rendered the banner; the slot belongs to the full document")
	}
}
