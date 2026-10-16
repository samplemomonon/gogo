// Package partials_test covers the seller console's own view helpers.
package partials_test

import (
	"testing"

	"gogo/internal/pages/seller/views/partials"
)

// A stock movement is stored as a stable wire value and shown as a word the
// seller reads. The stock form and the movement ledger must agree on that
// mapping: if either showed the raw value, the seller would see "adjustment" in
// the table next to a dropdown that calls the same thing "Penyesuaian", with no
// way to tell which one the ledger is actually recording.
func TestStockKindLabelNamesEveryKind(t *testing.T) {
	t.Parallel()

	for value, label := range map[string]string{
		"restock":    "Restok",
		"adjustment": "Penyesuaian",
		"opname":     "Opname",
	} {
		if got := partials.StockKindLabel(value); got != label {
			t.Errorf("StockKindLabel(%q) = %q, want %q", value, got, label)
		}
	}
}

// An unrecognised kind is stored data, not a value to discard. Showing it raw
// tells a seller something a blank would not.
func TestStockKindLabelPassesUnknownKindsThrough(t *testing.T) {
	t.Parallel()

	const future = "whatever-a-future-kind-is-called"
	if got := partials.StockKindLabel(future); got != future {
		t.Errorf("StockKindLabel(%q) = %q, want it shown as-is", future, got)
	}
}
