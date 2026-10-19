package views

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"gogo/internal/features/billing/activity"
)

func TestBadgeTone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		action activity.Action
		want   string
	}{
		{activity.ActionCreated, "success"},
		{activity.ActionUpdated, "warning"},
		{activity.ActionDeleted, "error"},
		{activity.ActionStockAdjusted, "neutral"},
		// An action this view does not know must fall back to a tone rather
		// than render an unstyled badge; the projection's action set and the
		// view's are allowed to drift by design.
		{activity.Action("something_new"), "neutral"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.action), func(t *testing.T) {
			t.Parallel()
			if got := badgeTone(tt.action); got != tt.want {
				t.Errorf("badgeTone(%q) = %q, want %q", tt.action, got, tt.want)
			}
		})
	}
}

func TestStatusText(t *testing.T) {
	t.Parallel()

	got := statusText(3)
	if !strings.Contains(got, "3") {
		t.Errorf("statusText(3) = %q, want it to name the count", got)
	}
	if !strings.Contains(statusText(0), "0") {
		t.Errorf("statusText(0) = %q, want it to name the zero count", statusText(0))
	}
}

func TestFeedRendersEntries(t *testing.T) {
	t.Parallel()

	entries := []activity.Entry{
		{ProductID: "42", Action: activity.ActionCreated, When: time.Now()},
		{ProductID: "43", Action: activity.ActionDeleted, When: time.Now()},
	}

	var buf bytes.Buffer
	if err := Feed(entries).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Feed.Render returned error: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		`id="billing-fragment"`,
		"42",
		"43",
		"created",
		"deleted",
		"2 total",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("feed missing %q", want)
		}
	}
	if strings.Contains(html, "Belum ada event katalog") {
		t.Error("a non-empty feed rendered the empty state")
	}
}

func TestFeedRendersEmptyState(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := Feed(nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Feed.Render returned error: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Belum ada event katalog") {
		t.Error("an empty feed did not render the empty state")
	}
	if !strings.Contains(html, "0 total") {
		t.Error("an empty feed should still show the count badge at 0")
	}
}

func TestLandingRendersHeroAndStatus(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := Landing(nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Landing.Render returned error: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Tagihan") {
		t.Error("landing hero missing its title")
	}
	if !strings.Contains(html, strconv.Itoa(0)) {
		t.Error("landing hero missing the activity count")
	}
}
