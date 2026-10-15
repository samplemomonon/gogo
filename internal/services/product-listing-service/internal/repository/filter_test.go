package repository

import (
	"strings"
	"testing"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// buildFilters has to keep every caller-supplied value in a bound parameter. The
// SQL text may vary in shape, but a value must never be pasted into it, or the
// shape becomes attacker-controlled.
func TestBuildFiltersBindsEveryValue(t *testing.T) {
	t.Parallel()

	cases := map[string]*model.ListProductsQuery{
		"category only": {CategoryID: 3},
		"status only":   {Status: statusPointer(model.StatusActive)},
		"price band":    {MinPriceCents: pricePointer(100), MaxPriceCents: pricePointer(200)},
		"everything": {
			CategoryID:    3,
			Status:        statusPointer(model.StatusDraft),
			MinPriceCents: pricePointer(100),
			MaxPriceCents: pricePointer(200),
		},
	}

	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			conditions, args := buildFilters(query)
			joined := strings.Join(conditions, " AND ")

			// One placeholder per argument, numbered in order, so a condition
			// cannot reference a slot that belongs to another one.
			if strings.Count(joined, "$1") < 1 && len(args) > 0 {
				t.Errorf("conditions reference no placeholder: %s", joined)
			}
			for i := range args {
				want := "$" + itoa(i+1)
				if !strings.Contains(joined, want) {
					t.Errorf("argument %d (%v) has no %s in the conditions: %s", i, args[i], want, joined)
				}
			}
			// A number that has no argument behind it would make PostgreSQL reject
			// the query at run time.
			for n := len(args) + 1; n <= len(args)+2; n++ {
				if strings.Contains(joined, "$"+itoa(n)) {
					t.Errorf("conditions reference $%d but only %d arguments were bound", n, len(args))
				}
			}
		})
	}
}

// The filters that reach the read model have to be able to express an absent
// value distinctly from a real one, or a search for "no status" silently becomes a
// search for drafts.
func TestStatusFilterIsOmittedWhenAbsent(t *testing.T) {
	t.Parallel()

	conditions, _ := buildFilters(&model.ListProductsQuery{})
	if len(conditions) != 0 {
		t.Errorf("conditions = %v, want none for an empty query", conditions)
	}

	conditions, _ = buildFilters(&model.ListProductsQuery{Status: statusPointer(model.StatusDraft)})
	if len(conditions) != 1 {
		t.Fatalf("conditions = %v, want one status condition", conditions)
	}
	if !strings.Contains(conditions[0], "status = $1") {
		t.Errorf("condition = %q, want an equality on status", conditions[0])
	}
}

// An absent bound is omitted. The bounds are pointers precisely so that absence
// and zero are different: a caller asking for products that cost nothing must not
// have the filter dropped and the whole catalogue returned instead.
func TestPriceBoundsAreOmittedWhenAbsent(t *testing.T) {
	t.Parallel()

	conditions, args := buildFilters(&model.ListProductsQuery{})
	if len(conditions) != 0 {
		t.Errorf("conditions = %v, want none when both bounds are absent", conditions)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want nothing bound for absent bounds", args)
	}

	conditions, args = buildFilters(&model.ListProductsQuery{
		MinPriceCents: pricePointer(100),
		MaxPriceCents: pricePointer(200),
	})
	if len(conditions) != 2 {
		t.Fatalf("conditions = %v, want both bounds", conditions)
	}
	if len(args) != 2 {
		t.Errorf("args = %v, want both bounds bound", args)
	}
}

// The regression: a zero bound used to be treated as absent, so a caller asking
// for products under IDR 0 was silently given the unfiltered catalogue. More rows
// than were asked for, and no error to explain it.
func TestAZeroPriceBoundIsAFilterNotAnAbsentFilter(t *testing.T) {
	t.Parallel()

	conditions, args := buildFilters(&model.ListProductsQuery{MaxPriceCents: pricePointer(0)})
	if len(conditions) != 1 {
		t.Fatalf("conditions = %v, want the zero bound to produce a condition", conditions)
	}
	if !strings.Contains(conditions[0], "price_cents <=") {
		t.Errorf("condition = %q, want an upper bound on price", conditions[0])
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want the zero bound bound as a parameter", args)
	}
	if args[0] != int64(0) {
		t.Errorf("arg = %v, want 0", args[0])
	}

	// The mirror case: a lower bound of zero is also a real filter, and it must
	// not be dropped either.
	conditions, args = buildFilters(&model.ListProductsQuery{MinPriceCents: pricePointer(0)})
	if len(conditions) != 1 || len(args) != 1 {
		t.Errorf("conditions = %v, args = %v, want a lower bound of zero to bind", conditions, args)
	}
}

func statusPointer(s model.Status) *model.Status { return &s }

func pricePointer(v int64) *int64 { return &v }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
