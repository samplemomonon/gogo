# web/ui — design-system boundary

This package is the **web component adapter**, not the owner of the cross-
platform contract. The token source, governance, release rules and adapter map
live in the repository-root [`design-system`](../../README.md). The Go/Templ
primitives live here so they can be imported independently from the web
application's private `internal` tree. Native clients consume the neutral token
contract and build platform-native components.

The lowest visual layer of the web surface: reusable, presentational primitives
that every read-side, module and shell partial composes. Nothing here knows about
a feature, a domain, or a request.

## Dependency contract (one-way)

```
apps/web/internal/features/**   ─┐
apps/web/internal/shell/**      ─┼─▶  gogo.design/system/web/ui
apps/web/internal/shared/web/** ─┘
```

`ui` may import only the standard library and `github.com/a-h/templ`. It must
never import a vertical, `shell`,
`pagekit`, `render`, or a domain package — otherwise a primitive would couple
two surfaces together.

## What lives here

- `classes.go` — the single source of truth for the daisyUI/Tailwind class
  strings that used to be copy-pasted across views (`CardClass`, `FieldClass`,
  `InputClass`, `EyebrowClass`, `BadgeClass`, `Btn`, `ChipToneClass`,
  `TableClass`, `StatTileClass`, `PaginationClass`, `LinkClass`, …).
- `icons.templ` — the inline icon set (`IconPlus`, `IconCart`, `IconEye`, …).
- Structural primitives — `Badge`, `Button` / `ButtonLink`, `Alert`,
  `EmptyState`, `PageHero`, `FormField`, `Spinner`.
- Card chrome — `Card` (outer panel + body), `Panel` (the standard card with a
  single card-title heading) and `CardBody` / `CardHeader` / `CardActions` for
  panels whose outer element is not a `<div>` (a `<form>`, `<section>` or the
  hover card). `CardClass` / `CardHoverClass` stay as the raw strings so a panel
  that composes its own outer element can still use them.
- List and navigation primitives — `Pagination` (+ `PageLink` /
  `PaginationOptions`), `Breadcrumb` (+ `Crumb`), `DataTable` (+ `Column`,
  `TableRow`, `TableCell`), and `NavLinks` (+ `NavItem` / `NavLinksOptions` /
  `NavChrome`, the one place the top-bar active rule, `aria-current` and link
  chrome live). `IsActiveNav` and `ActiveNavHref` expose the active rule so no
  surface re-derives it.
- Sidebar composition (shadcn-style hierarchy over daisyUI `drawer` + `menu`):
  `SidebarProvider` (+ `SidebarProviderOptions`) is the layout context that
  places a `Sidebar` beside a `SidebarInset`, in `SidebarStatic` or
  `SidebarDrawer` mode; `Sidebar` (+ `SidebarOptions`) is the column with
  `SidebarHeader` / `SidebarContent` / `SidebarFooter` / `SidebarRail`;
  `SidebarGroup` (+ `SidebarGroupLabel` / `SidebarGroupAction` /
  `SidebarGroupContent`) groups items; `SidebarMenu` (+ `SidebarMenuOptions`,
  which renders items and marks the most specific active one), `SidebarMenuItem`,
  `SidebarMenuButton` (+ `SidebarMenuButtonOptions`), `SidebarMenuAction`,
  `SidebarMenuBadge`, `SidebarMenuSub` and `SidebarMenuSubItem` form the menu
  tree; `SidebarTrigger` is a standalone toggle.
- Page skeleton and forms — `PageShell` (+ `PageShellOptions`, the injected
  shell wrapper around a page's body), `Form` (+ `FormOptions` / `FormLayout`),
  `SubmitButton`, `Toolbar`, `MediaTile`, `IconTile`.
- Reporting primitives — `StatTile` (a hero's action slot), `StatPanel` (a
  detail-page card with an icon-eyebrow header), `CountBadge` (+
  `CountBadgeOptions`), `StatusBadge` (+ `StatusBadgeOptions`, the tone-dot
  state pill), `MetaPill` (an icon + label fact chip).

## Where the line falls

A primitive owns **chrome**, never **meaning**. When a pattern needs a decision
the design system has no business making, the caller keeps the decision and
passes the result in:

- `Pagination` renders the controls, but the caller decides which pages exist
  and what each `href` is — `pagekit.Pagination` is where that decision is
  made, so a paged listing's URL shape stays with the listing.
- `DataTable` renders the header row, the zebra styling and the scroll wrapper;
  the caller supplies the columns' labels and its own body rows.
- `Column.Align` / `TableCell` take `"right"` and map it to a utility, so views
  state intent instead of Tailwind classes.

## Naming convention

One rule per kind of declaration, so the package reads predictably:

| Kind | Rule | Examples |
| ---- | ---- | -------- |
| Component | singular PascalCase noun for the element it renders; a variant suffixes the base it extends | `Card`, `Badge`, `Breadcrumb`; `Button`→`ButtonLink`, `Card`→`CardHeader`/`CardBody`/`CardActions` |
| Class string | `<Thing>Class` as a constant; `<Thing>Class(args)` as a helper mapping semantic args to utilities | `CardClass`; `BadgeClass(kind, size)`, `EyebrowClass(tone)` |
| Structured input | `<Component>Options` | `CountBadgeOptions`, `PaginationOptions` |
| List-element descriptor | shortest name tied to its primitive | `Column`, `Crumb`, `PageLink` |
| Attribute passthrough | a container whose caller may need its own `id`/`hx-*` takes `attrs templ.Attributes` last | `Card(bodyClass string, attrs templ.Attributes)` |

The separate Go module is the dependency boundary: it cannot import the web
application's private Go packages. The module's direct dependencies stay
limited to Templ and the standard library.

## Local workflow

From the repository root, `make templ` regenerates this module and its web-app
consumers. `go -C design-system/web/ui build ./...` builds the standalone
component package; `make css` regenerates the token adapter and embedded web
stylesheet. Run component changes through the web module's existing render
assertions and review their consuming page at narrow and wide widths.

## Adding a primitive

When a visual pattern repeats in more than one view, extract it here rather
than copying markup. Keep it flat: no interfaces, no factories, no config for a
constant. Add a render assertion to `ui_test.go`.

Two rules that keep this package from drifting into a framework:

- A primitive earns its place by removing duplication that already exists.
  A component nothing composes yet is speculative — leave it out until a
  second view needs it.
- Prefer a named options struct over a long positional parameter list once a
  primitive has more than two or three optional inputs, so call sites read as
  `CountBadgeOptions{Count: 5, Suffix: "in stock"}` instead of
  `CountBadge(5, "in stock", "", "")`.

Tailwind note: literal utility classes must appear verbatim in a scanned source
file (see `@source "../../../web"` in `assets/css/app.css`) for the class to be
emitted — do not build a color utility purely from a dynamic prefix.
