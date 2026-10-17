package ui

import "strings"

// The canonical surface chrome. Every panel in the app used to repeat these
// strings verbatim; they now live here once.
const (
	CardClass = "card bg-base-100 shadow-sm border border-base-300/60 card-sheen transition-colors"
	// The transition names the three properties the hover actually changes.
	// `transition-all` made the browser test every animatable property on every
	// frame of every hover, on every card in the grid, and it also let
	// layout-affecting properties (width, padding, margin) transition if a
	// caller ever hovered into one — each of which forces layout rather than
	// just paint. transform and box-shadow are compositor/paint work; the rest
	// of the box is none of the browser's business.
	CardHoverClass = "card bg-base-100 shadow-sm border border-base-300/60 card-sheen hover:border-primary/40 hover:shadow-lg hover:-translate-y-1 transition-[border-color,box-shadow,transform] duration-200 ease-out group"
	FieldClass     = "form-control w-full"
	LabelClass     = "label pb-1.5"
	LabelTextClass = "label-text font-medium text-base-content/80 text-sm"
	InputClass     = "input w-full focus:border-primary focus:outline-none transition-colors"

	// SelectClass is deliberately NOT InputClass. daisyUI styles a <select>
	// through its own `select` component, which is what supplies
	// `appearance: none` and the chevron background; an <input> class on a
	// <select> leaves the native arrow in place and makes the control
	// indistinguishable from a text field. Every select in the app was
	// carrying InputClass, so the whole console offered dropdowns that looked
	// like text inputs.
	// No "select-bordered": daisyUI 5 dropped the *-bordered modifiers, and
	// shipping a class that compiles to nothing in a token meant to be
	// authoritative is how the next person concludes the select has no border.
	// `.input` supplies its own border, so InputClass no longer names
	// input-bordered either — it had been inert for the same reason.
	SelectClass = "select w-full focus:border-primary focus:outline-none transition-colors"

	// TapTargetClass raises a control to the 44px thumb target (Apple HIG,
	// WCAG 2.5.5). Needed because daisyUI's scale derives height from
	// --size-field (0.25rem) — btn is 40px, btn-sm 32px — so a tapped control
	// must pair its size modifier with this. min-w keeps a btn-circle round.
	TapTargetClass = "min-h-11 min-w-11"

	TableScrollClass = "overflow-x-auto"
	TableClass       = "table table-zebra w-full"
	TableHeadClass   = "text-xs uppercase font-semibold tracking-wider text-base-content/60 bg-base-200/80 border-b border-base-300/60"
	TableRowClass    = "hover:bg-base-200/50 transition-colors border-b border-base-300/30"
	// A row header is the cell naming the row. It is a <th scope="row">, not a
	// bolded <td>, so a screen reader announces the row's subject together with
	// each of its values instead of reading a column of orphaned cells.
	TableRowHeadClass = "font-medium text-left"

	// The stat tile is meant for the page hero's action slot, where it shows a
	// single headline number beside the page's own actions.
	StatTileClass      = "stat bg-base-100/90 rounded-xl py-3 px-5 shadow-sm border border-base-300/60 card-sheen"
	StatTileLabelClass = "stat-title text-xs font-semibold uppercase tracking-wider text-base-content/60"
	StatTileValueClass = "stat-value text-2xl"

	// StatCardLinkClass is the navigable stat: the hoverable card chrome, sized
	// as a grid cell, with the eyebrow + figure pair stacked inside.
	StatCardLinkClass = CardHoverClass + " block p-5"
	// The figure is one step larger than StatTileValueClass because a linked
	// count is the card's whole reason to exist; the tiles sit beside a heading
	// and must not compete with it.
	StatCardValueClass = "text-3xl font-black pt-1 " + NumClass

	BreadcrumbListClass = "flex items-center gap-2 text-sm min-w-0"
	// inline-flex, not the bare link: min-height is ignored on a non-replaced
	// inline box, which left these crumbs at 16px tall — the one control in
	// the app that failed WCAG 2.5.8 (24px) outright.
	BreadcrumbLinkClass    = "link link-hover inline-flex items-center text-base-content/60 hover:text-primary transition-colors " + TapTargetClass
	BreadcrumbCurrentClass = "font-medium text-base-content/90 truncate min-w-0"
	BreadcrumbDividerClass = "text-base-content/30"

	// Every pagination control is a real anchor so a listing stays crawlable
	// and usable without JavaScript. The bar wraps rather than scrolling, so a
	// deep window stays reachable on a narrow viewport.
	PaginationClass        = "flex flex-wrap items-center justify-center gap-2 mt-8"
	PaginationLinkClass    = "btn btn-sm btn-ghost hover:bg-base-300/50 transition-colors"
	PaginationCurrentClass = "btn btn-sm btn-primary btn-active shadow-xs no-animation"

	// Page-title and hero chrome, shared by PageHero, HeroBanner and the plain
	// banners in billing and the shell registry. Padding, stack rhythm and
	// description styling are centralized below so the variants cannot drift.
	TitleClass = "text-2xl sm:text-3xl font-bold tracking-tight text-base-content"
	HeroClass  = "rounded-2xl bg-hero-gradient border border-base-300/60 shadow-sm relative overflow-hidden card-sheen"

	// The padding steps down below sm: a fixed px-8 leaves a narrow phone
	// almost no content width.
	HeroPadClass         = "px-5 py-6 sm:px-8 sm:py-8"
	HeroStackClass       = "space-y-2"
	HeroDescriptionClass = "text-base-content/75 max-w-lg text-sm sm:text-base leading-relaxed"

	// MutedClass carries supporting copy; MetaClass is tertiary chrome such as a
	// status line under a hero.
	MutedClass = "text-base-content/70"
	MetaClass  = "text-sm text-base-content/50"

	// The two muted *sizes* were spelled out by hand at seven call sites, which
	// is how the same sentence ended up at three different opacities.
	SmallMutedClass = "text-sm text-base-content/70"
	TinyMutedClass  = "text-xs text-base-content/60"

	// NumClass is the figure treatment: lining, tabular digits so a column of
	// numbers lines up on the decimal. Eight call sites spelled it out.
	NumClass = "tabular-nums tracking-tight"

	// MonoClass is an identifier — a SKU, a document number, a product id.
	// Identifiers are read character by character, so they get a monospaced
	// face and a size that keeps them from competing with the value beside them.
	MonoClass = "font-mono text-xs text-base-content/80"

	// LinkClass is the inline text-link chrome used inside prose and panels.
	LinkClass = "link link-primary font-medium"

	PageSpaceClass  = "space-y-8"
	StackSpaceClass = "space-y-6"

	// ToolbarClass wraps. Its one caller puts a breadcrumb trail against a back
	// link, and on a 320px viewport the trail's truncation never engaged
	// (the flex child had no min-w-0) so the two collided instead of stacking.
	ToolbarClass = "flex flex-wrap items-center justify-between gap-4"

	// Button variants. The app once spelled these out at every call site, which
	// is how a "Simpan" (ghost) and a "Hapus" (ghost + text-error) drifted into
	// two different looks for the same kind of action. Every action button now
	// names its intent through one of these, so the palette stays consistent.
	BtnPrimaryClass = "btn-primary"
	BtnDangerClass  = "btn-error"
	BtnGhostClass   = "btn-ghost"
	BtnNeutralClass = "btn-neutral"

	// BrandLinkClass is the wordmark lockup: the glowing brand tile plus the
	// product name. It was copied into the shell header and both console
	// sidebars, so the tile's rounded tile size and the name's tracking were
	// free to drift between the three places a reader navigates from.
	BrandLinkClass = "flex min-h-11 shrink-0 items-center gap-2 rounded-lg"

	// The brand tile behind the wordmark (see .brand-mark in app.css).
	BrandMarkClass = "brand-mark"

	// FormActionsClass is a form's action row: full-bleed inside a fieldset
	// grid so the submit button aligns with the panel's edge instead of
	// floating in the first column.
	FormActionsClass = "sm:col-span-2 pt-1"

	// FactListClass is the definition-list grid behind a read-only detail
	// panel; FactRowClass is one label/value pair within it.
	FactListClass  = "grid grid-cols-1 gap-3 sm:grid-cols-2"
	FactRowClass   = "flex flex-col gap-0.5 min-w-0"
	FactValueClass = "font-medium text-sm break-all"

	// PriceBlockClass is the currency prefix + figure treatment. The two price
	// surfaces (product card, detail price panel) each spelled this out with
	// three different sizes for the same two elements.
	PriceCurrencyClass = "text-primary/70 uppercase font-bold"
	PriceValueClass    = "font-black text-primary " + NumClass

	// CodePillClass is an inline identifier chip. Three views each rolled their
	// own mono chip; a SKU and a product id now read the same.
	CodePillClass = "font-mono text-xs bg-base-200 text-base-content/80 px-2 py-0.5 rounded-md border border-base-300/60"

	// IconTileClass / MediaTileClass are the decorative card visuals: an icon
	// tile and a gradient media header. Colours stay with the caller.
	IconTileClass  = "size-14 rounded-2xl flex items-center justify-center shrink-0 shadow-xs border"
	MediaTileClass = "aspect-[16/9] w-full bg-gradient-to-br from-primary/10 via-base-200 to-secondary/10 relative overflow-hidden flex items-center justify-center border-b border-base-300/40"

	FieldsetClass   = "grid grid-cols-1 sm:grid-cols-2 gap-3"
	InlineFormClass = "flex items-end gap-3"

	// GridClass is the responsive card grid a listing uses. The storefront grid
	// spelled its own column steps; the shared one keeps a list of cards
	// consistent wherever it appears.
	GridClass = "grid grid-cols-2 gap-4 sm:grid-cols-2 sm:gap-5 lg:grid-cols-3 xl:grid-cols-5 2xl:grid-cols-5"

	// Sidebar chrome, grouped so the column's sections read the same way.
	SidebarPanelClass        = "flex min-h-full w-full flex-col bg-base-100 border-r border-base-300/60"
	SidebarHeaderClass       = "flex items-center gap-2 px-3 h-16 border-b border-base-300/60 shrink-0"
	SidebarContentClass      = "flex-1 min-h-0 overflow-y-auto px-2 py-3"
	SidebarFooterClass       = "border-t border-base-300/60 px-2 py-3 shrink-0"
	SidebarGroupClass        = "pb-2"
	SidebarGroupLabelClass   = "px-2 py-1.5 text-xs font-semibold uppercase tracking-wider text-base-content/50"
	SidebarGroupContentClass = "space-y-0.5"
	SidebarMenuClass         = "menu w-full gap-0.5 p-0"
	SidebarMenuSubClass      = "ms-3.5 border-s border-base-300/60 ps-1"
	SidebarMenuBadgeClass    = "badge badge-sm ms-auto"
	SidebarTriggerClass      = IconTriggerClass
	SidebarRailClass         = "hidden lg:block absolute inset-y-0 -end-3 w-3 cursor-ew-resize hover:bg-base-300/40 transition-colors"
)

// IconTriggerClass is the chrome for a round, glyph-only trigger — the theme
// toggle, the drawer's toggle, the header's mobile account entry. Three
// controls spelled this string out identically, and the drawer's copy had
// already begun to diverge from the other two.
const IconTriggerClass = "btn btn-ghost btn-circle btn-sm " + TapTargetClass

// Intent names what an action control is *for*, not how it looks. Naming the
// intent at the call site is what stops a destructive action from being spelled
// as the primary one: every delete button in the seller console was assembled
// as "btn-sm " + BtnPrimaryClass, so "Hapus" (delete) and "Simpan" (save) were
// rendered in the same brand blue and only the word on the button distinguished
// them. BtnDangerClass existed for exactly this and was never called.
type Intent string

const (
	// IntentPrimary is the page's main forward action: save, submit, continue.
	IntentPrimary Intent = "primary"

	// IntentDanger is irreversible or destructive: delete, void, revoke. It
	// always pairs with a confirmation, never sits alone as the page's main
	// action, and never carries the primary emphasis.
	IntentDanger Intent = "danger"

	// IntentNeutral is a committed-but-not-primary action inside a dense row:
	// save a renamed category, adjust a quantity.
	IntentNeutral Intent = "neutral"

	// IntentGhost is a low-emphasis navigation or dismissal action: back,
	// cancel, clear.
	IntentGhost Intent = "ghost"
)

// ActionClass composes the chrome for an action control from its intent and a
// daisyUI size modifier ("sm", "xs", "lg", or "" for the default height).
//
// The icon gap lives here rather than at the call site because a button is a
// flex container: on a control whose only child is a label the gap has no
// effect, and on one that pairs a glyph with a label it is the difference
// between a tidy control and a joined blob. Putting it here means "an action
// button" is one rule rather than two spellings.
//
// This is the dense variant, for a control sitting inside a table row or a card
// footer where a 44px target would distort the surrounding layout. Use
// TapActionClass for a standalone control.
func ActionClass(intent Intent, size string, extra ...string) string {
	return joinClasses(buttonSizeClass(size), intentClass(intent), "gap-1.5", joinClasses(extra...))
}

// TapActionClass is ActionClass raised to the 44px thumb target (Apple HIG,
// WCAG 2.5.5), for a standalone control — a hero action, a full-width panel
// action. The two variants exist so that "is this control big enough" is a
// decision visible in the code rather than one that drifts per call site: the
// tap target was applied in eleven places and missing in thirteen.
func TapActionClass(intent Intent, size string, extra ...string) string {
	return joinClasses(ActionClass(intent, size, extra...), TapTargetClass)
}

// The daisyUI button classes are component classes emitted by the plugin, not
// utilities Tailwind has to discover in the source text, so composing the size
// modifier from a variable is safe. Tone is the other way round — see
// intentClass.
func buttonSizeClass(size string) string {
	if size == "" {
		return ""
	}
	return "btn-" + size
}

func intentClass(intent Intent) string {
	switch intent {
	case IntentPrimary:
		return BtnPrimaryClass
	case IntentDanger:
		return BtnDangerClass
	case IntentNeutral:
		return BtnNeutralClass
	default:
		return BtnGhostClass
	}
}

// EyebrowClass is the small uppercase section label. The tone literals are
// written out (rather than concatenated from a prefix) so Tailwind's scanner
// sees the exact utility classes it must emit.
func EyebrowClass(tone string) string {
	const base = "text-xs font-semibold uppercase tracking-widest "
	switch tone {
	case "primary":
		return base + "text-primary"
	case "secondary":
		return base + "text-secondary"
	default:
		return base + "text-base-content/50"
	}
}

// ChipToneClass is the rounded icon chip that sits beside an eyebrow. Literals
// are explicit for the same Tailwind-visibility reason as EyebrowClass.
func ChipToneClass(tone string) string {
	switch tone {
	case "primary":
		return "size-8 rounded-lg flex items-center justify-center bg-primary/10 text-primary"
	case "secondary":
		return "size-8 rounded-lg flex items-center justify-center bg-secondary/10 text-secondary"
	default:
		return "size-8 rounded-lg flex items-center justify-center bg-base-200 text-base-content/60"
	}
}

// cardHeaderChipClass is the compact icon chip beside a card title — one step
// smaller than ChipToneClass, which belongs to the page hero. The icon inside
// carries its own colour; the chip supplies the tinted background only.
func cardHeaderChipClass(tone string) string {
	const base = "size-7 rounded-md flex items-center justify-center "
	switch tone {
	case "primary":
		return base + "bg-primary/10"
	case "secondary":
		return base + "bg-secondary/10"
	default:
		return base + "bg-base-200"
	}
}

// BadgeClass assembles a daisyUI badge. kind and size are daisyUI modifiers
// (e.g. "ghost", "sm"); either may be empty.
func BadgeClass(kind, size string) string {
	parts := []string{"badge"}
	if kind != "" {
		parts = append(parts, "badge-"+kind)
	}
	if size != "" {
		parts = append(parts, "badge-"+size)
	}
	return strings.Join(parts, " ")
}

// columnAlignClass maps a column's semantic alignment onto its text-align
// utility. The literals are written out for the same Tailwind-visibility
// reason as EyebrowClass.
func columnAlignClass(align string) string {
	switch align {
	case "right":
		return "text-right"
	case "center":
		return "text-center"
	default:
		return ""
	}
}

func joinClasses(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// AlertClass is the daisyUI alert chrome for a kind ("success" | "error").
func AlertClass(kind string) string {
	return "alert alert-" + kind + " shadow-sm"
}

// PriceSize scales a PriceBlock. The product card and the detail price panel
// each hand-rolled this pair at a different size for the same two elements, so
// "a price" was two unrelated type treatments.
type PriceSize string

const (
	// PriceSm is the card-level figure.
	PriceSm PriceSize = "sm"

	// PriceLg is the detail-page figure.
	PriceLg PriceSize = "lg"
)

func priceCurrencySizeClass(size PriceSize) string {
	if size == PriceLg {
		return "text-sm"
	}
	return "text-xs"
}

func priceValueSizeClass(size PriceSize) string {
	if size == PriceLg {
		return "text-4xl"
	}
	return "text-2xl"
}

func spinnerClass(size, class string) string {
	s := "loading loading-spinner loading-" + size
	if class != "" {
		s += " " + class
	}
	return s
}
