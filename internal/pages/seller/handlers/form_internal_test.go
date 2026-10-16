package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogo/internal/pages/seller/application/results"
)

func formRequest(t *testing.T, form string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/seller/produk", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// TestParseProductReadsTheWritableFields pins the form reader field by field:
// values are trimmed, the numeric fields are parsed, and the category id is carried
// as the console's string. A field the platform does not carry is never read.
func TestParseProductReadsTheWritableFields(t *testing.T) {
	t.Parallel()

	input, ok := parseProduct(formRequest(t,
		"name=++Kopi+Susu++&slug=+kopi-susu+&description=+Enak+&brand=+Gogo+&price=+25000+&stock=+3+&status=draft&categoryId=+10+"))
	if !ok {
		t.Fatal("parseProduct rejected a well-formed form")
	}

	want := results.ProductInput{
		Name: "Kopi Susu", Slug: "kopi-susu", Description: "Enak", Brand: "Gogo",
		Price: 25000, Stock: 3, Status: "draft", CategoryID: "10",
	}
	if input != want {
		t.Errorf("input = %+v, want %+v", input, want)
	}
}

// TestParseProductDefaultsBlankStatusToActive pins the one default the form has:
// an omitted status would be stored as the platform's UNSPECIFIED, which no read
// model renders as sellable, so the handler supplies active for a create form
// submitted without one.
func TestParseProductDefaultsBlankStatusToActive(t *testing.T) {
	t.Parallel()

	for _, form := range []string{"name=Kopi", "name=Kopi&status=", "name=Kopi&status=%20%20"} {
		t.Run(form, func(t *testing.T) {
			t.Parallel()

			input, ok := parseProduct(formRequest(t, form))
			if !ok {
				t.Fatal("parseProduct rejected the form")
			}
			if input.Status != "active" {
				t.Errorf("status = %q, want active", input.Status)
			}
		})
	}
}

// TestParseProductTreatsBlankNumbersAsZero pins that an empty optional number is
// "not set yet", not a parse failure: only a value that fails to parse rejects the
// form.
func TestParseProductTreatsBlankNumbersAsZero(t *testing.T) {
	t.Parallel()

	for _, form := range []string{"name=Kopi", "name=Kopi&price=&stock=", "name=Kopi&price=%20&stock=%20"} {
		t.Run(form, func(t *testing.T) {
			t.Parallel()

			input, ok := parseProduct(formRequest(t, form))
			if !ok {
				t.Fatal("parseProduct rejected a form with blank numbers")
			}
			if input.Price != 0 || input.Stock != 0 {
				t.Errorf("price/stock = %d/%d, want 0/0", input.Price, input.Stock)
			}
		})
	}
}

// TestParseProductRejectsUnreadableNumbers pins the rejection: the parse error
// names a field and echoes the offending value, so it must stop the form here
// rather than travel to the view.
func TestParseProductRejectsUnreadableNumbers(t *testing.T) {
	t.Parallel()

	for _, form := range []string{"name=Kopi&price=not-a-number", "name=Kopi&stock=many"} {
		t.Run(form, func(t *testing.T) {
			t.Parallel()

			if _, ok := parseProduct(formRequest(t, form)); ok {
				t.Error("parseProduct accepted an unreadable number")
			}
		})
	}
}

// TestParseIntHelpers pins the shared number readers: blank is zero, surrounding
// whitespace is ignored, and anything else is an error the caller decides about.
func TestParseIntHelpers(t *testing.T) {
	t.Parallel()

	ints := map[string]struct {
		want  int
		valid bool
	}{
		"":    {0, true},
		"   ": {0, true},
		" 5 ": {5, true},
		"-3":  {-3, true},
		"5x":  {0, false},
		"1.5": {0, false},
	}
	for in, want := range ints {
		got, err := parseInt(in)
		if (err == nil) != want.valid {
			t.Errorf("parseInt(%q) error = %v, want valid=%v", in, err, want.valid)
		}
		if err == nil && got != want.want {
			t.Errorf("parseInt(%q) = %d, want %d", in, got, want.want)
		}
	}

	int64s := map[string]struct {
		want  int64
		valid bool
	}{
		"":                    {0, true},
		" 25000 ":             {25000, true},
		"9223372036854775807": {9223372036854775807, true},
		"9223372036854775808": {0, false},
		"2.5":                 {0, false},
	}
	for in, want := range int64s {
		got, err := parseInt64(in)
		if (err == nil) != want.valid {
			t.Errorf("parseInt64(%q) error = %v, want valid=%v", in, err, want.valid)
		}
		if err == nil && got != want.want {
			t.Errorf("parseInt64(%q) = %d, want %d", in, got, want.want)
		}
	}
}

// TestDefaultKind pins the adjustment kind's fallback: only an absent or
// whitespace-only kind becomes "adjustment"; a named kind is passed through
// verbatim because it is a ledger term of art, not free text to normalize.
func TestDefaultKind(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":        "adjustment",
		"   ":     "adjustment",
		"restock": "restock",
		"opname":  "opname",
	}
	for in, want := range cases {
		if got := defaultKind(in); got != want {
			t.Errorf("defaultKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStockOfDistinguishesNeverStockedFromZero pins the console's most subtle join:
// a product with a zero quantity has a stock row (it is stocked, empty), while a
// product with no row has never been stocked. The bool is what tells them apart,
// and the dashboard's out-of-stock count depends on it.
func TestStockOfDistinguishesNeverStockedFromZero(t *testing.T) {
	t.Parallel()

	stocks := []*results.Stock{
		{ProductID: "1", Quantity: 0},
		{ProductID: "2", Quantity: 4},
	}

	if quantity, found := stockOf(stocks, "1"); quantity != 0 || !found {
		t.Errorf("stockOf(zero row) = (%d, %v), want (0, true)", quantity, found)
	}
	if quantity, found := stockOf(stocks, "2"); quantity != 4 || !found {
		t.Errorf("stockOf(stocked) = (%d, %v), want (4, true)", quantity, found)
	}
	if quantity, found := stockOf(stocks, "3"); quantity != 0 || found {
		t.Errorf("stockOf(no row) = (%d, %v), want (0, false)", quantity, found)
	}
	if quantity, found := stockOf(nil, "1"); quantity != 0 || found {
		t.Errorf("stockOf(no rows at all) = (%d, %v), want (0, false)", quantity, found)
	}
}

// TestSellableKeepsOnlyActiveProductsInOrder pins the till's filter: draft and
// archived products exist in the catalogue but are not offered, and the surviving
// products keep the catalogue's order so the till is stable between loads.
func TestSellableKeepsOnlyActiveProductsInOrder(t *testing.T) {
	t.Parallel()

	products := []*results.Product{
		{ID: "1", Name: "Aktif", Status: "active"},
		{ID: "2", Name: "Draf", Status: "draft"},
		{ID: "3", Name: "Arsip", Status: "archived"},
		{ID: "4", Name: "Aktif Lagi", Status: "active"},
	}

	kept := sellable(products)
	if len(kept) != 2 || kept[0].ID != "1" || kept[1].ID != "4" {
		t.Errorf("sellable = %+v, want products 1 and 4 in order", kept)
	}
	if got := sellable(nil); len(got) != 0 {
		t.Errorf("sellable(nil) = %v, want empty", got)
	}
}

// TestTrimmedNormalizesWhitespace pins the form reader's normalization: a value
// that was only ever spaces reads as absent rather than as a one-character name.
func TestTrimmedNormalizesWhitespace(t *testing.T) {
	t.Parallel()

	req := formRequest(t, "name=++Kopi+Susu++&blank=%20%20%20&empty=")

	if got := trimmed(req, "name"); got != "Kopi Susu" {
		t.Errorf("trimmed(name) = %q, want Kopi Susu", got)
	}
	if got := trimmed(req, "blank"); got != "" {
		t.Errorf("trimmed(blank) = %q, want empty", got)
	}
	if got := trimmed(req, "empty"); got != "" {
		t.Errorf("trimmed(empty) = %q, want empty", got)
	}
	if got := trimmed(req, "missing"); got != "" {
		t.Errorf("trimmed(missing) = %q, want empty", got)
	}
}
