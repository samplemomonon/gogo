package render

import (
	"fmt"
	"strings"
	"testing"
)

// TestValidateIslandName pins the rule docs/DECISION_UI_OWNERSHIP.md §5
// decided: every island key carries its owner namespace. The valid cases are
// the shapes the codebase actually produces (declared names, paged keys with
// a hashed term, variant coordinates); the invalid ones are each failure mode
// the rule exists to stop — a bare name that could collide with another unit,
// and an owner segment that is not an identifier.
func TestValidateIslandName(t *testing.T) {
	t.Parallel()

	valid := []string{
		"home.product-grid",
		"home.product-grid:p2",
		"home.product-grid:t1a2b3c:p3",
		"home/product-grid",
		"billing.invoice-list",
		"detail.x.stock",
	}
	for _, name := range valid {
		name := name
		t.Run("valid/"+name, func(t *testing.T) {
			t.Parallel()

			if err := ValidateIslandName(name); err != nil {
				t.Errorf("ValidateIslandName(%q) = %v, want nil", name, err)
			}
		})
	}

	invalid := []struct {
		name string
		want string
	}{
		{name: "", want: "no owner namespace"},
		{name: "product-grid", want: "no owner namespace"},
		{name: `evil"><script>`, want: "no owner namespace"},
		{name: "Home.grid", want: "invalid owner segment"},
		{name: "-home.grid", want: "invalid owner segment"},
		{name: "9lives.grid", want: "invalid owner segment"},
		{name: "home_grid.grid", want: "invalid owner segment"},
		{name: ".grid", want: "invalid owner segment"},
		{name: "home.", want: "no island after the separator"},
		{name: "home/", want: "no island after the separator"},
		{name: "home.product grid", want: "whitespace"},
		{name: "home.product\tgrid", want: "whitespace"},
	}
	for _, tt := range invalid {
		tt := tt
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateIslandName(tt.name)
			if err == nil {
				t.Fatalf("ValidateIslandName(%q) = nil, want an error explaining %q", tt.name, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ValidateIslandName(%q) = %v, want it to explain %q", tt.name, err, tt.want)
			}
		})
	}
}

// TestIslandConstructorsRejectUnownedNames pins the enforcement point: the
// constructors are the single place an island enters the page composition, so
// an unowned key fails there — at wiring or in CI — rather than silently
// sharing another unit's cache namespace at runtime.
func TestIslandConstructorsRejectUnownedNames(t *testing.T) {
	t.Parallel()

	t.Run("Island", func(t *testing.T) {
		t.Parallel()

		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("Island with an unowned name must panic")
			}
			if msg := fmt.Sprint(r); !strings.Contains(msg, "render: island name") {
				t.Errorf("panic = %q, want the render: island name error", msg)
			}
		}()
		Island("product-grid", nil, RenderingContract{})
	})

	t.Run("ClientIsland", func(t *testing.T) {
		t.Parallel()

		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("ClientIsland with an unowned name must panic")
			}
		}()
		ClientIsland("product-grid", nil, "/grid", nil, RenderingContract{})
	})

	t.Run("owned names pass", func(t *testing.T) {
		t.Parallel()

		Island("home.product-grid", nil, RenderingContract{})
		ClientIsland("seller.stock", nil, "/seller/x/stock", nil, RenderingContract{})
	})
}
