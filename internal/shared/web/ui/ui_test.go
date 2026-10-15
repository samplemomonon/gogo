package ui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestClassHelpers(t *testing.T) {
	if got, want := BadgeClass("success", "sm"), "badge badge-success badge-sm"; got != want {
		t.Errorf("BadgeClass = %q, want %q", got, want)
	}
	if got, want := BadgeClass("", ""), "badge"; got != want {
		t.Errorf("BadgeClass(empty) = %q, want %q", got, want)
	}
	if got := EyebrowClass("primary"); !strings.Contains(got, "text-primary") {
		t.Errorf("EyebrowClass(primary) = %q, want text-primary", got)
	}
	if got := ChipToneClass("secondary"); !strings.Contains(got, "bg-secondary/10") {
		t.Errorf("ChipToneClass(secondary) = %q, want bg-secondary/10", got)
	}
	if !strings.Contains(HeroClass, "bg-hero-gradient") {
		t.Errorf("HeroClass = %q, want bg-hero-gradient", HeroClass)
	}
	// Both steps are asserted so an edit that drops the responsive pair fails.
	for _, want := range []string{"text-2xl", "sm:text-3xl"} {
		if !strings.Contains(TitleClass, want) {
			t.Errorf("TitleClass = %q, want it to contain %q", TitleClass, want)
		}
	}
	// Button variants name their intent once, so every action button shares one
	// palette instead of drifting per call site.
	for _, tc := range []struct{ got, want string }{
		{BtnPrimaryClass, "btn-primary"},
		{BtnDangerClass, "btn-error"},
		{BtnGhostClass, "btn-ghost"},
	} {
		if tc.got != tc.want {
			t.Errorf("button variant = %q, want %q", tc.got, tc.want)
		}
	}
	// A <select> must not wear an <input>'s chrome: daisyUI's select component
	// is what supplies the dropdown affordance.
	if strings.Contains(SelectClass, "input") {
		t.Errorf("SelectClass must not reuse InputClass, got %q", SelectClass)
	}
	if !strings.Contains(SelectClass, "select") {
		t.Errorf("SelectClass = %q, want the daisyUI select chrome", SelectClass)
	}
}

// TestActionClassNamesIntent is the guard on the bug this vocabulary exists to
// prevent: every delete button in the seller console was assembled as
// "btn-sm " + BtnPrimaryClass, so "Hapus" and "Simpan" rendered identically and
// BtnDangerClass was never called.
func TestActionClassNamesIntent(t *testing.T) {
	tests := []struct {
		intent Intent
		size   string
		want   string
	}{
		{IntentDanger, "sm", "btn-sm btn-error gap-1.5"},
		{IntentPrimary, "", "btn-primary gap-1.5"},
		{IntentNeutral, "xs", "btn-xs btn-neutral gap-1.5"},
		{IntentGhost, "lg", "btn-lg btn-ghost gap-1.5"},
		// An unknown intent falls back to the low-emphasis treatment rather
		// than silently promoting itself to primary.
		{Intent("nonsense"), "sm", "btn-sm btn-ghost gap-1.5"},
	}
	for _, tc := range tests {
		if got := ActionClass(tc.intent, tc.size); got != tc.want {
			t.Errorf("ActionClass(%q, %q) = %q, want %q", tc.intent, tc.size, got, tc.want)
		}
	}
	// The tap target is a separate, named decision so "is this control big
	// enough" is visible in the code rather than drifting per call site.
	tap := TapActionClass(IntentPrimary, "sm")
	if !strings.Contains(tap, TapTargetClass) {
		t.Errorf("TapActionClass must include the tap target, got %q", tap)
	}
	if strings.Contains(ActionClass(IntentPrimary, "sm"), TapTargetClass) {
		t.Errorf("the dense ActionClass must not inflate the target inside a table row: %q", ActionClass(IntentPrimary, "sm"))
	}
}

func TestPriceBlockSizesFollowOneScale(t *testing.T) {
	sm := render(t, PriceBlock("Rp", "125.000", PriceSm))
	lg := render(t, PriceBlock("Rp", "125.000", PriceLg))
	for _, want := range []string{"Rp", "125.000", PriceCurrencyClass, PriceValueClass, "text-primary"} {
		if !strings.Contains(sm, want) {
			t.Errorf("PriceBlock(sm) missing %q in:\n%s", want, sm)
		}
	}
	// The same price on the card and on the detail page must be the same
	// treatment at two scales, not two unrelated ones.
	if !strings.Contains(sm, "text-2xl") || !strings.Contains(lg, "text-4xl") {
		t.Errorf("price scale steps missing:\nsm=%s\nlg=%s", sm, lg)
	}
	if strings.Contains(sm, "text-4xl") {
		t.Errorf("the card price must not use the detail figure size:\n%s", sm)
	}
}

func TestBrandLink(t *testing.T) {
	brand := render(t, BrandLink("/seller", "Seller", "Seller console"))
	for _, want := range []string{`href="/seller"`, `aria-label="Seller console"`, BrandMarkClass, "Seller", "min-h-11"} {
		if !strings.Contains(brand, want) {
			t.Errorf("BrandLink missing %q in:\n%s", want, brand)
		}
	}

}

func TestStatCardLink(t *testing.T) {
	out := render(t, StatCardLink("/seller/produk", "Produk", "12", "primary"))
	for _, want := range []string{`href="/seller/produk"`, CardHoverClass, "Produk", "12", EyebrowClass("primary")} {
		if !strings.Contains(out, want) {
			t.Errorf("StatCardLink missing %q in:\n%s", want, out)
		}
	}
}

func TestFactListAndCodePill(t *testing.T) {
	facts := renderWithChildren(t, FactList(), render(t, FactRow("Email", "a@b.c")))
	for _, want := range []string{"<dl", FactListClass, "<dt", "Email", "<dd", "a@b.c"} {
		if !strings.Contains(facts, want) {
			t.Errorf("FactList missing %q in:\n%s", want, facts)
		}
	}
	if pill := render(t, CodePill("SKU-1")); !strings.Contains(pill, CodePillClass) || !strings.Contains(pill, "SKU-1") {
		t.Errorf("CodePill = %q, want the shared chip chrome and its text", pill)
	}
}

// A destructive action has to be both visually distinct and confirmed; the
// button's own attributes are the place the confirmation is declared.
func TestSubmitAttrsConfirmDestructiveActions(t *testing.T) {
	attrs := SubmitAttrs("Hapus produk?")
	if attrs["data-confirm"] != "Hapus produk?" {
		t.Errorf("SubmitAttrs must carry the confirmation, got %v", attrs)
	}
	if attrs["data-submitting"] != "true" {
		t.Errorf("SubmitAttrs must mark the control for in-flight feedback, got %v", attrs)
	}
	// Set the name implies. Button serves submitting and in-page roles, so
	// SubmitAttrs states the type rather than leaning on HTML's default.
	if attrs["type"] != "submit" {
		t.Errorf("SubmitAttrs must set type=submit, got %q", attrs["type"])
	}
	// A safe action asks for no confirmation but still gets the flight mark.
	safe := SubmitAttrs("")
	if _, ok := safe["data-confirm"]; ok {
		t.Errorf("an empty confirmation must not emit the attribute, got %v", safe)
	}
	if safe["data-submitting"] != "true" {
		t.Errorf("even a safe submit needs in-flight feedback, got %v", safe)
	}
	if safe["type"] != "submit" {
		t.Errorf("a safe submit is still a submit, got %q", safe["type"])
	}
}

func TestFormActionsAndCheckbox(t *testing.T) {
	actions := renderWithChildren(t, FormActions(""), `<button>Simpan</button>`)
	if !strings.Contains(actions, FormActionsClass) || !strings.Contains(actions, "Simpan") {
		t.Errorf("FormActions = %q, want the full-bleed row and its children", actions)
	}

	box := render(t, FormCheckbox("active", "Aktif", true))
	for _, want := range []string{`name="active"`, "checkbox checkbox-primary", LabelTextClass, "Aktif", "checked"} {
		if !strings.Contains(box, want) {
			t.Errorf("FormCheckbox missing %q in:\n%s", want, box)
		}
	}
	if unchecked := render(t, FormCheckbox("active", "Aktif", false)); strings.Contains(unchecked, "checked") {
		t.Errorf("an unchecked checkbox must not render the attribute:\n%s", unchecked)
	}
}

func TestSubmitActionUsesIntentVocabulary(t *testing.T) {
	out := renderWithChildren(t, SubmitAction(IntentDanger, "sm", "", SubmitAttrs("Void?")), "Batal")
	for _, want := range []string{`type="submit"`, "btn-error", `data-confirm="Void?"`} {
		if !strings.Contains(out, want) {
			t.Errorf("SubmitAction missing %q in:\n%s", want, out)
		}
	}
	// SubmitButton stays the shorthand for the common primary case.
	if out := renderWithChildren(t, SubmitButton("w-full"), "Simpan"); !strings.Contains(out, "btn-primary") {
		t.Errorf("SubmitButton should default to the primary intent:\n%s", out)
	}
}

func TestColumnAlignClass(t *testing.T) {
	tests := []struct {
		align string
		want  string
	}{
		{"right", "text-right"},
		{"center", "text-center"},
		{"", ""},
		{"nonsense", ""},
	}
	for _, tc := range tests {
		if got := columnAlignClass(tc.align); got != tc.want {
			t.Errorf("columnAlignClass(%q) = %q, want %q", tc.align, got, tc.want)
		}
	}
}

func TestCountBadgeText(t *testing.T) {
	tests := []struct {
		name string
		spec CountBadgeOptions
		want string
	}{
		{"count and suffix", CountBadgeOptions{Count: 5, Suffix: "in stock"}, "5 in stock"},
		{"bare count", CountBadgeOptions{Count: 12}, "12"},
		{"zero label", CountBadgeOptions{ZeroLabel: "Out of stock"}, "Out of stock"},
		{"bare zero", CountBadgeOptions{}, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countBadgeText(tc.spec); got != tc.want {
				t.Errorf("countBadgeText = %q, want %q", got, tc.want)
			}
		})
	}
}

// The card spells out "Out of stock" while the table shows a bare zero; both
// must still read the same way, so the tone rule is asserted once here.
func TestCountBadgeToneFollowsCount(t *testing.T) {
	positive := render(t, CountBadge(CountBadgeOptions{Count: 3}))
	if !strings.Contains(positive, "badge-success") {
		t.Errorf("positive count should use the success tone, got %q", positive)
	}
	zero := render(t, CountBadge(CountBadgeOptions{}))
	if !strings.Contains(zero, "badge-error") {
		t.Errorf("zero count should use the error tone, got %q", zero)
	}
}

func TestPaginationOmitsAbsentControls(t *testing.T) {
	full := render(t, Pagination(PaginationOptions{
		Label:    "Product pages",
		PrevHref: "/?page=1",
		NextHref: "/?page=3",
		Links: []PageLink{
			{Number: 1, Href: "/?page=1"},
			{Number: 2, Href: "/?page=2", Current: true},
			{Number: 3, Href: "/?page=3"},
		},
	}))
	for _, want := range []string{
		`aria-label="Product pages"`, `href="/?page=1"`, `rel="prev"`,
		`rel="next"`, `aria-current="page"`, PaginationCurrentClass,
	} {
		if !strings.Contains(full, want) {
			t.Errorf("pagination missing %q in:\n%s", want, full)
		}
	}

	first := render(t, Pagination(PaginationOptions{
		Links: []PageLink{{Number: 1, Href: "/?page=1", Current: true}},
	}))
	if strings.Contains(first, "rel=\"prev\"") {
		t.Errorf("a listing with no previous page must not render a prev control:\n%s", first)
	}
	if !strings.Contains(first, `aria-label="Penomoran halaman"`) {
		t.Errorf("an unlabelled bar should fall back to a generic name:\n%s", first)
	}
}

func TestPaginationWithoutLinksRendersNothing(t *testing.T) {
	if out := render(t, Pagination(PaginationOptions{NextHref: "/?page=2"})); strings.TrimSpace(out) != "" {
		t.Errorf("a bar with no numbered links should render nothing, got %q", out)
	}
}

// The step controls are named by the surface, not by the primitive: a shared
// design-system package should not decide what language the interface is in.
// The name is used for the visible text and the accessible name together, so
// an assistive reader announces the words the reader can see.
func TestPaginationStepLabelsComeFromTheCaller(t *testing.T) {
	named := render(t, Pagination(PaginationOptions{
		PrevHref:  "/?page=1",
		NextHref:  "/?page=3",
		PrevLabel: "Sebelumnya",
		NextLabel: "Berikutnya",
		Links:     []PageLink{{Number: 2, Href: "/?page=2", Current: true}},
	}))
	for _, want := range []string{`aria-label="Sebelumnya"`, "Sebelumnya", `aria-label="Berikutnya"`, "Berikutnya"} {
		if !strings.Contains(named, want) {
			t.Errorf("pagination missing %q in:\n%s", want, named)
		}
	}

	// An empty label must not leave an icon-only control with no name.
	fallback := render(t, Pagination(PaginationOptions{
		PrevHref: "/?page=1",
		NextHref: "/?page=3",
		Links:    []PageLink{{Number: 2, Href: "/?page=2", Current: true}},
	}))
	for _, want := range []string{`aria-label="Sebelumnya"`, `aria-label="Berikutnya"`} {
		if !strings.Contains(fallback, want) {
			t.Errorf("pagination must fall back to a generic name %q in:\n%s", want, fallback)
		}
	}
}

func TestBreadcrumbRendersTrail(t *testing.T) {
	out := render(t, Breadcrumb([]Crumb{
		{Label: "Products", Href: "/admin"},
		{Label: "Kopi Susu", Current: true},
	}))
	for _, want := range []string{
		`aria-label="Remah roti"`, "<ol", `href="/admin"`, "Products",
		`aria-current="page"`, "Kopi Susu", BreadcrumbDividerClass, `aria-hidden="true"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("breadcrumb missing %q in:\n%s", want, out)
		}
	}
}

func TestBreadcrumbWithoutCrumbsRendersNothing(t *testing.T) {
	if out := render(t, Breadcrumb(nil)); strings.TrimSpace(out) != "" {
		t.Errorf("an empty trail should render nothing, got %q", out)
	}
}

func TestDataTableRendersHeaderAndRows(t *testing.T) {
	ctx := templ.WithChildren(context.Background(), templ.Raw(`<tr class="hover:bg-base-200/40 transition-colors"><td>Kopi</td></tr>`))
	var buf bytes.Buffer
	comp := DataTable(DataTableOptions{
		Columns:  []Column{{Label: "Name"}, {Label: "Actions", Align: "right"}},
		MinWidth: "min-w-[40rem]",
		Caption:  "Products",
	})
	if err := comp.Render(ctx, &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		TableScrollClass, TableClass, TableHeadClass, "min-w-[40rem]",
		`scope="col"`, "Name", "Actions", "text-right", "Kopi", "Products", "sr-only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("data table missing %q in:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "<th scope"); got != 2 {
		t.Errorf("expected one header cell per column, got %d:\n%s", got, out)
	}
}

// A column whose header is decorative still needs a name, or the row's actions
// sit under a gap in the header row that assistive technology announces as an
// empty column.
func TestDataTableNamesUnlabelledColumns(t *testing.T) {
	out := renderWithChildren(t, DataTable(DataTableOptions{
		Columns: []Column{{Label: "Nama"}, {Label: "", Align: "right", AriaLabel: "Aksi"}},
	}), `<tr></tr>`)
	if !strings.Contains(out, `aria-label="Aksi"`) {
		t.Errorf("an unlabelled action column must still be named:\n%s", out)
	}
}

// Numeric columns are announced as numbers, and a row's subject is carried
// across its values by a real row header rather than a bolded cell.
func TestDataTableMarksNumericColumnsAndRowHeaders(t *testing.T) {
	out := renderWithChildren(t, DataTable(DataTableOptions{
		Columns: []Column{{Label: "Harga", Align: "right", Numeric: true}},
	}), `<tr class="hover:bg-base-200/50"><th scope="row" class="`+TableRowHeadClass+`">Kopi</th></tr>`)
	if !strings.Contains(out, `aria-sort="none"`) {
		t.Errorf("a numeric column should be marked as one:\n%s", out)
	}
	if !strings.Contains(out, `scope="row"`) || !strings.Contains(out, "Kopi") {
		t.Errorf("the row's first cell should be a row header:\n%s", out)
	}
}

// TablePanel is the whole point of the primitive: the caller's rows when there
// are some, an answer when there are not. Every record table in the app used to
// render bare headers over nothing.
func TestTablePanelShowsRowsOrEmptyState(t *testing.T) {
	populated := renderWithChildren(t, TablePanel(TablePanelOptions{
		Title:      "Daftar produk",
		Count:      2,
		Columns:    []Column{{Label: "Nama"}, {Label: "", Align: "right", AriaLabel: "Aksi"}},
		MinWidth:   "min-w-[46rem]",
		EmptyTitle: "Belum ada produk",
	}), `<tr><td>Kopi</td></tr>`)
	for _, want := range []string{"Daftar produk", "Kopi", "min-w-[46rem]", `aria-label="Aksi"`} {
		if !strings.Contains(populated, want) {
			t.Errorf("populated TablePanel missing %q in:\n%s", want, populated)
		}
	}
	if strings.Contains(populated, "Belum ada produk") {
		t.Errorf("a table with rows must not render the empty state:\n%s", populated)
	}

	empty := renderWithChildren(t, TablePanel(TablePanelOptions{
		Title:            "Daftar produk",
		Count:            0,
		Columns:          []Column{{Label: "Nama"}, {Label: "SKU"}},
		EmptyTitle:       "Belum ada produk",
		EmptyDescription: "Tambahkan produk pertama.",
	}), `<tr><td>Kopi</td></tr>`)
	for _, want := range []string{"Belum ada produk", "Tambahkan produk pertama.", `colspan="2"`} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty TablePanel missing %q in:\n%s", want, empty)
		}
	}
	if strings.Contains(empty, "Kopi") {
		t.Errorf("an empty table must not render its rows:\n%s", empty)
	}
}

func TestPrimitivesRenderExpectedChrome(t *testing.T) {
	tests := []struct {
		name string
		comp templ.Component
		want []string
	}{
		{"Badge", Badge("success", "sm", ""), []string{"badge badge-success badge-sm"}},
		{"Button", Button("btn-sm btn-ghost", nil), []string{"btn btn-sm btn-ghost"}},
		{"ButtonLink", ButtonLink("/x", "btn-primary", nil), []string{`href="/x"`, "btn btn-primary"}},
		{"AlertError", Alert("error", "boom"), []string{"alert-error", "boom", `role="alert"`}},
		{"AlertSuccess", Alert("success", "yay"), []string{"alert-success", `role="status"`}},
		{"AlertEmpty", Alert("error", ""), []string{}},
		{"EmptyState", EmptyState("Empty", "Nothing here", IconCart("size-8")), []string{"Empty", "Nothing here"}},
		{"PageHero", PageHero("Home", "primary", "Browse", "desc", IconDocuments("size-4")), []string{"bg-hero-gradient", "text-primary", "Browse"}},
		{"HeroBannerFull", HeroBanner(HeroOptions{
			Eyebrow: "billing module", Tone: "primary", Title: "Billing",
			Description: "Invoices", FooterText: "3 events",
		}), []string{"bg-hero-gradient", "billing module", "Billing", "Invoices", "3 events", HeroPadClass}},
		{"FormField", FormField("Name"), []string{FieldClass, LabelClass, LabelTextClass, "Name"}},
		{"Spinner", Spinner("xs", "htmx-indicator"), []string{"loading loading-spinner loading-xs htmx-indicator"}},
		{"StatTile", StatTile("Products", "12"), []string{StatTileClass, StatTileLabelClass, StatTileValueClass, "Products", "12"}},
		{"CountBadge", CountBadge(CountBadgeOptions{Count: 5, Suffix: "in stock", Class: "shrink-0"}), []string{"badge-success", "5 in stock", "shrink-0"}},
		{"IconPlus", IconPlus("size-4"), []string{"size-4", "<svg"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := render(t, tc.comp)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q in:\n%s", w, out)
				}
			}
			if tc.name == "AlertEmpty" && strings.TrimSpace(out) != "" {
				t.Errorf("Alert with empty message should render nothing, got %q", out)
			}
		})
	}
}

// TestHeroBannerOmitsAbsentFields pins that every HeroBanner field but Title is
// genuinely optional, so a caller with only a title does not render an empty
// eyebrow row, chip or paragraph.
func TestHeroBannerOmitsAbsentFields(t *testing.T) {
	out := render(t, HeroBanner(HeroOptions{Title: "Module registry"}))
	for _, want := range []string{"Module registry", "<h1", HeroClass, HeroPadClass} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<p") {
		t.Errorf("HeroBanner with only a title should render no paragraphs, got:\n%s", out)
	}
}

func TestFormFieldRendersChildren(t *testing.T) {
	ctx := templ.WithChildren(context.Background(), templ.Raw(`<input class="`+InputClass+`"/>`))
	var buf bytes.Buffer
	if err := FormField("Name").Render(ctx, &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), InputClass) {
		t.Errorf("FormField did not render its children:\n%s", buf.String())
	}
}

func renderWithChildren(t *testing.T, c templ.Component, children string) string {
	t.Helper()
	ctx := templ.WithChildren(context.Background(), templ.Raw(children))
	var buf bytes.Buffer
	if err := c.Render(ctx, &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestCardPrimitives(t *testing.T) {
	card := renderWithChildren(t, Card("gap-4", templ.Attributes{"id": "panel"}), `<p>body</p>`)
	for _, want := range []string{CardClass, `id="panel"`, "card-body gap-4", "body"} {
		if !strings.Contains(card, want) {
			t.Errorf("Card missing %q in:\n%s", want, card)
		}
	}

	if body := renderWithChildren(t, CardBody("p-0"), `<span>x</span>`); !strings.Contains(body, "card-body p-0") {
		t.Errorf("CardBody = %q, want card-body p-0", body)
	}

	header := render(t, CardHeader("primary", "Add product", IconPlus("size-3.5 text-primary")))
	for _, want := range []string{cardHeaderChipClass("primary"), "card-title text-base", "Add product"} {
		if !strings.Contains(header, want) {
			t.Errorf("CardHeader missing %q in:\n%s", want, header)
		}
	}
	if bare := render(t, CardHeader("primary", "No icon", nil)); strings.Contains(bare, "<div class=\"size-7") {
		t.Errorf("CardHeader with a nil icon must not render a chip:\n%s", bare)
	}

	if actions := renderWithChildren(t, CardActions("justify-end pt-1"), `<button>go</button>`); !strings.Contains(actions, "card-actions justify-end pt-1") {
		t.Errorf("CardActions = %q, want card-actions justify-end pt-1", actions)
	}
}

func TestStatPanel(t *testing.T) {
	panel := renderWithChildren(t, StatPanel("Price", IconTag("size-3.5 text-base-content/40")), `<span>Rp</span>`)
	for _, want := range []string{CardClass, "card-body gap-3", EyebrowClass(""), "Price", "Rp"} {
		if !strings.Contains(panel, want) {
			t.Errorf("StatPanel missing %q in:\n%s", want, panel)
		}
	}

	bare := renderWithChildren(t, StatPanel("Stock", nil), `<span>0</span>`)
	if strings.Contains(bare, "size-7") {
		t.Errorf("StatPanel with a nil icon must not render a chip:\n%s", bare)
	}
}

func TestPanelRendersTitleAndBody(t *testing.T) {
	out := renderWithChildren(t, Panel("Daftar produk"), `<p>rows</p>`)
	for _, want := range []string{CardClass, "card-title text-base", "Daftar produk", "rows"} {
		if !strings.Contains(out, want) {
			t.Errorf("Panel missing %q in:\n%s", want, out)
		}
	}
}

func TestStatusBadgeRendersToneDot(t *testing.T) {
	ok := render(t, StatusBadge(StatusBadgeOptions{Tone: "success", Label: "Available", Size: "sm", Pulse: true}))
	for _, want := range []string{"badge-success", "badge-soft", "bg-success", "status-pulse", "Available"} {
		if !strings.Contains(ok, want) {
			t.Errorf("StatusBadge(success) missing %q in:\n%s", want, ok)
		}
	}

	zero := render(t, StatusBadge(StatusBadgeOptions{Tone: "error", Label: "Out of stock", Size: "lg"}))
	for _, want := range []string{"badge-error", "bg-error", "size-2", "Out of stock"} {
		if !strings.Contains(zero, want) {
			t.Errorf("StatusBadge(error, lg) missing %q in:\n%s", want, zero)
		}
	}
	if strings.Contains(zero, "status-pulse") {
		t.Errorf("a non-pulsing StatusBadge must not animate its dot:\n%s", zero)
	}
}

func TestMetaPillRendersIconAndText(t *testing.T) {
	out := render(t, MetaPill(IconCalendar("size-3.5"), "Created 01 Jan 2026"))
	for _, want := range []string{"Created 01 Jan 2026", "size-3.5", "rounded-lg"} {
		if !strings.Contains(out, want) {
			t.Errorf("MetaPill missing %q in:\n%s", want, out)
		}
	}

	if bare := render(t, MetaPill(nil, "Updated")); strings.Contains(bare, "<svg") {
		t.Errorf("MetaPill with a nil icon must not render an icon:\n%s", bare)
	}
}

func TestPageShellInjectsLayout(t *testing.T) {
	shell := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, `<main data-shell>`); err != nil {
			return err
		}
		return templ.GetChildren(ctx).Render(ctx, w)
	})

	got := renderWithChildren(t, PageShell(PageShellOptions{Layout: shell}), `<p>body</p>`)
	for _, want := range []string{`<main data-shell>`, PageSpaceClass, "body"} {
		if !strings.Contains(got, want) {
			t.Errorf("PageShell missing %q in:\n%s", want, got)
		}
	}

	narrow := renderWithChildren(t, PageShell(PageShellOptions{Narrow: true}), `<p>body</p>`)
	for _, want := range []string{"max-w-4xl", "mx-auto"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("PageShell(Narrow) missing %q in:\n%s", want, narrow)
		}
	}

	bare := renderWithChildren(t, PageShell(PageShellOptions{Class: "space-y-2"}), `<p>body</p>`)
	if !strings.Contains(bare, "space-y-2") || strings.Contains(bare, PageSpaceClass) {
		t.Errorf("PageShell(Class) should let the caller override the rhythm:\n%s", bare)
	}
}

func TestFormPrimitives(t *testing.T) {
	form := renderWithChildren(t, Form(FormOptions{Action: "/seller/produk", Layout: FormFieldset}), `<input name="name"/>`)
	for _, want := range []string{`method="post"`, `action="/seller/produk"`, FieldsetClass, `name="name"`} {
		if !strings.Contains(form, want) {
			t.Errorf("Form missing %q in:\n%s", want, form)
		}
	}

	put := renderWithChildren(t, Form(FormOptions{Action: "/x", Method: "put"}), `<input/>`)
	if !strings.Contains(put, `method="put"`) {
		t.Errorf("Form(Method) should honor the caller's method:\n%s", put)
	}

	submit := renderWithChildren(t, SubmitButton("w-full"), "Simpan")
	for _, want := range []string{`type="submit"`, "btn btn-primary gap-1.5 w-full", "Simpan"} {
		if !strings.Contains(submit, want) {
			t.Errorf("SubmitButton missing %q in:\n%s", want, submit)
		}
	}
}

func TestMediaAndIconTile(t *testing.T) {
	media := renderWithChildren(t, MediaTile("overflow-hidden"), `<span>glyph</span>`)
	for _, want := range []string{MediaTileClass, "overflow-hidden", "glyph", "blur-xl"} {
		if !strings.Contains(media, want) {
			t.Errorf("MediaTile missing %q in:\n%s", want, media)
		}
	}

	icon := renderWithChildren(t, IconTile("bg-primary/10"), `<svg></svg>`)
	for _, want := range []string{IconTileClass, "bg-primary/10", "<svg>"} {
		if !strings.Contains(icon, want) {
			t.Errorf("IconTile missing %q in:\n%s", want, icon)
		}
	}
}

func TestIsActiveNav(t *testing.T) {
	tests := []struct {
		href, path string
		want       bool
	}{
		{"/", "/", true},
		{"/", "/seller", false},
		{"/seller", "/seller", true},
		{"/seller", "/seller/produk", true},
		{"/seller", "/sellers", false},
		{"/seller/produk", "/seller/kategori", false},
		{"/seller/kasir", "/seller/kasir", true},
	}
	for _, tc := range tests {
		if got := IsActiveNav(tc.href, tc.path); got != tc.want {
			t.Errorf("IsActiveNav(%q, %q) = %v, want %v", tc.href, tc.path, got, tc.want)
		}
	}
}

func TestNavLinksButtonChrome(t *testing.T) {
	out := render(t, NavLinks(NavLinksOptions{
		Chrome: NavChromeButton,
		Class:  "btn-sm font-medium",
		Path:   "/catalog/42",
		Items: []NavItem{
			{Href: "/", Label: "Home"},
			{Href: "/catalog", Label: "Store"},
		},
	}))
	store := "btn btn-sm font-medium btn-active btn-primary"
	if !strings.Contains(out, store) {
		t.Errorf("active top-bar link should carry %q, got:\n%s", store, out)
	}
	if !strings.Contains(out, `aria-current="page"`) {
		t.Errorf("active link should be aria-current:\n%s", out)
	}
	if got := strings.Count(out, `aria-current="page"`); got != 1 {
		t.Errorf("expected exactly one active link, got %d:\n%s", got, out)
	}
	if got := strings.Count(out, "btn-ghost"); got != 1 {
		t.Errorf("expected exactly one ghost (inactive) link, got %d:\n%s", got, out)
	}
}

func TestNavLinksBlockChrome(t *testing.T) {
	out := render(t, NavLinks(NavLinksOptions{
		Chrome: NavChromeButtonBlock,
		Path:   "/seller",
		Items:  []NavItem{{Href: "/seller", Label: "Seller"}},
	}))
	if !strings.Contains(out, "btn btn-block justify-start") {
		t.Errorf("block chrome should lay the link out full width:\n%s", out)
	}
	if !strings.Contains(out, "<li>") {
		t.Errorf("block chrome should wrap each link in an li for the dropdown menu:\n%s", out)
	}
}

func TestNavLinksPrefersSpecificMatch(t *testing.T) {
	out := render(t, NavLinks(NavLinksOptions{
		Path: "/seller/produk",
		Items: []NavItem{
			{Href: "/seller", Label: "Dashboard"},
			{Href: "/seller/produk", Label: "Produk"},
		},
	}))
	if got := strings.Count(out, `aria-current="page"`); got != 1 {
		t.Errorf("only the most specific link should be active, got %d:\n%s", got, out)
	}
}

func TestBottomNavMarksActiveTab(t *testing.T) {
	out := render(t, BottomNav(BottomNavOptions{
		Path: "/seller",
		Items: []BottomNavItem{
			{Href: "/", Label: "Home", Icon: IconHome("size-5")},
			{Href: "/seller", Label: "Seller", Icon: IconStore("size-5")},
		},
	}))
	for _, want := range []string{
		`aria-label="Navigasi bawah"`, "md:hidden", "Home", "Seller",
		`href="/seller"`, `aria-current="page"`, "<svg",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bottom nav missing %q in:\n%s", want, out)
		}
	}
	if got := strings.Count(out, `aria-current="page"`); got != 1 {
		t.Errorf("exactly one tab should be active, got %d:\n%s", got, out)
	}
}

func TestBottomNavWithoutItemsRendersNothing(t *testing.T) {
	if out := render(t, BottomNav(BottomNavOptions{})); strings.TrimSpace(out) != "" {
		t.Errorf("an empty bottom nav should render nothing, got %q", out)
	}
}

// The clear control is an icon with no text, so its name is the only thing
// that makes it usable; it comes from the surface like the field's does.
func TestSearchFormClearLabelComesFromTheCaller(t *testing.T) {
	spec := SearchFormOptions{Action: "/", Value: "kopi", Label: "Cari produk", ClearLabel: "Bersihkan pencarian"}
	if out := render(t, SearchForm(spec)); !strings.Contains(out, `aria-label="Bersihkan pencarian"`) {
		t.Errorf("clear control missing the caller's name in:\n%s", out)
	}
	spec.ClearLabel = ""
	if out := render(t, SearchForm(spec)); !strings.Contains(out, `aria-label="Hapus pencarian"`) {
		t.Errorf("clear control must fall back to a generic name in:\n%s", out)
	}
	// No term, no clear control to name.
	spec.Value = ""
	if out := render(t, SearchForm(spec)); strings.Contains(out, "Hapus pencarian") {
		t.Errorf("an unfiltered search should not render a clear control:\n%s", out)
	}
}

func TestSearchFormRendersBox(t *testing.T) {
	out := render(t, SearchForm(SearchFormOptions{
		Action:      "/",
		Value:       "kopi",
		Placeholder: "Search products…",
		Label:       "Search products",
	}))
	for _, want := range []string{
		`role="search"`, `action="/"`, `method="get"`, `name="q"`,
		`value="kopi"`, "Hapus pencarian", `aria-label="Search products"`, "<svg",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("search form missing %q in:\n%s", want, out)
		}
	}

	bare := render(t, SearchForm(SearchFormOptions{Action: "/", Label: "Search"}))
	if strings.Contains(bare, "Hapus pencarian") {
		t.Errorf("an empty search form must not render a clear control:\n%s", bare)
	}
}

func TestSidebarProviderDrawer(t *testing.T) {
	sidebar := Sidebar(SidebarOptions{Content: templ.Raw("<p>nav</p>")})
	inset := renderWithChildren(t, SidebarInset(SidebarInsetOptions{ID: "console", Mode: SidebarDrawer}), "<p>body</p>")
	out := render(t, SidebarProvider(SidebarProviderOptions{
		ID: "console", Mode: SidebarDrawer, Sidebar: sidebar, Inset: templ.Raw(inset),
	}))
	for _, want := range []string{
		"drawer lg:drawer-open", `id="console"`, "drawer-toggle", "drawer-content",
		"drawer-side", "drawer-overlay", `for="console"`, "body", "nav",
		// The trigger is a <label>, which is not focusable on its own; it must
		// carry tabindex + role so a keyboard user can reach and open the drawer.
		`tabindex="0"`, `role="button"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SidebarProvider(drawer) missing %q in:\n%s", want, out)
		}
	}
}

// The console column and the shell's top bar must not both pin to the top of
// the viewport. daisyUI leaves the base `top: 0` on the panel when
// `lg:drawer-open` turns it into a sticky in-flow column, so without an offset
// the two stick to the same line and the sidebar — later in the document, and
// originally at the same z-index — paints over the bar as soon as the page
// scrolls. Pin the offset, the bound, and the desktop z-order.
func TestDrawerSidebarSitsBelowTheShellHeader(t *testing.T) {
	t.Parallel()

	sidebar := Sidebar(SidebarOptions{Content: templ.Raw("<p>nav</p>")})
	inset := renderWithChildren(t, SidebarInset(SidebarInsetOptions{ID: "console", Mode: SidebarDrawer}), "<p>body</p>")
	out := render(t, SidebarProvider(SidebarProviderOptions{
		ID: "console", Mode: SidebarDrawer, Sidebar: sidebar, Inset: templ.Raw(inset),
	}))

	for _, want := range []string{
		// Parked under the header rather than at the top of the viewport...
		"lg:top-16",
		// ...and bounded to what is left of the viewport, which is what lets the
		// column's own `flex-1 min-h-0 overflow-y-auto` list scroll in place
		// instead of growing the page.
		"lg:h-[calc(100dvh-4rem)]",
		// Below the header's z-40 on desktop. The base z-40 is deliberately kept
		// so the mobile drawer still covers the bar like a full-screen panel.
		"lg:z-30",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("drawer sidebar missing %q; on desktop it would cover the shell header in:\n%s", want, out)
		}
	}
}

func TestSidebarProviderStatic(t *testing.T) {
	sidebar := Sidebar(SidebarOptions{Content: templ.Raw("<p>nav</p>")})
	inset := renderWithChildren(t, SidebarInset(SidebarInsetOptions{Mode: SidebarStatic}), "<p>body</p>")
	out := render(t, SidebarProvider(SidebarProviderOptions{
		Mode: SidebarStatic, Sidebar: sidebar, Inset: templ.Raw(inset),
	}))
	for _, want := range []string{"flex min-h-dvh", "body", "nav"} {
		if !strings.Contains(out, want) {
			t.Errorf("SidebarProvider(static) missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "drawer-toggle") {
		t.Errorf("static mode must not render the drawer checkbox:\n%s", out)
	}
}

func TestSidebarRegions(t *testing.T) {
	out := render(t, Sidebar(SidebarOptions{
		Header:  SidebarHeader(),
		Content: templ.Raw(`<p>groups</p>`),
		Footer:  SidebarFooter(),
		Rail:    true,
	}))
	for _, want := range []string{"<aside", SidebarHeaderClass, SidebarContentClass, SidebarFooterClass, SidebarRailClass, "groups"} {
		if !strings.Contains(out, want) {
			t.Errorf("Sidebar missing %q in:\n%s", want, out)
		}
	}
}

func TestSidebarGroupAndMenu(t *testing.T) {
	group := SidebarGroup()
	label := SidebarGroupLabel("Konsol")
	sub := SidebarMenuSub()
	_ = sub

	out := renderWithChildren(t, group, `<p>inner</p>`)
	for _, want := range []string{SidebarGroupClass, "inner"} {
		if !strings.Contains(out, want) {
			t.Errorf("SidebarGroup missing %q in:\n%s", want, out)
		}
	}
	if l := render(t, label); !strings.Contains(l, SidebarGroupLabelClass) || !strings.Contains(l, "Konsol") {
		t.Errorf("SidebarGroupLabel missing chrome or label:\n%s", l)
	}
	if a := render(t, SidebarGroupAction(templ.Attributes{"hx-post": "/x"})); !strings.Contains(a, `hx-post="/x"`) {
		t.Errorf("SidebarGroupAction missing attrs:\n%s", a)
	}
}

func TestSidebarMenuDataDrivenActive(t *testing.T) {
	out := render(t, SidebarMenu(SidebarMenuOptions{
		Path: "/seller/produk",
		Items: []NavItem{
			{Href: "/seller", Label: "Dashboard"},
			{Href: "/seller/produk", Label: "Produk"},
		},
	}))
	if got := strings.Count(out, `aria-current="page"`); got != 1 {
		t.Errorf("side menu should mark exactly the most specific item active, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "Produk") || !strings.Contains(out, "Dashboard") {
		t.Errorf("side menu should render every item:\n%s", out)
	}
}

func TestSidebarMenuButtonActive(t *testing.T) {
	menu := SidebarMenu(SidebarMenuOptions{})
	item := SidebarMenuItem()
	btn := SidebarMenuButton(SidebarMenuButtonOptions{Href: "/seller/produk", Label: "Produk", Path: "/seller/produk"})

	out := renderWithChildren(t, menu, renderWithChildren(t, item, render(t, btn)))
	for _, want := range []string{SidebarMenuClass, "<ul", "<li>", "Produk", `aria-current="page"`, `class="active"`} {
		if !strings.Contains(out, want) {
			t.Errorf("SidebarMenu/Item/Button missing %q in:\n%s", want, out)
		}
	}
}

func TestSidebarMenuButtonInactive(t *testing.T) {
	btn := SidebarMenuButton(SidebarMenuButtonOptions{Href: "/billing", Label: "Billing", Path: "/seller/produk"})
	out := render(t, btn)
	if strings.Contains(out, `aria-current="page"`) {
		t.Errorf("non-matching item must not be active:\n%s", out)
	}
}

func TestSidebarMenuButtonPinnedActive(t *testing.T) {
	pinned := true
	out := render(t, SidebarMenuButton(SidebarMenuButtonOptions{Href: "/x", Label: "X", Active: &pinned}))
	if !strings.Contains(out, `aria-current="page"`) {
		t.Errorf("Active=true should pin the item active even without a path match:\n%s", out)
	}
}

func TestSidebarMenuSubAndBadge(t *testing.T) {
	out := renderWithChildren(t, SidebarMenuSub(), `<li>child</li>`)
	for _, want := range []string{SidebarMenuSubClass, "child"} {
		if !strings.Contains(out, want) {
			t.Errorf("SidebarMenuSub missing %q in:\n%s", want, out)
		}
	}
	if b := render(t, SidebarMenuBadge("3")); !strings.Contains(b, SidebarMenuBadgeClass) || !strings.Contains(b, "3") {
		t.Errorf("SidebarMenuBadge missing chrome or label:\n%s", b)
	}
	if it := renderWithChildren(t, SidebarMenuSubItem(), "<span>x</span>"); !strings.Contains(it, "<li>") {
		t.Errorf("SidebarMenuSubItem should render an li:\n%s", it)
	}
}
