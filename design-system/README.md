# Gogo platform design system

The platform team owns the shared visual language and the foundations that
make Gogo clients feel related. Product teams consume this system; they do not
fork its tokens or redefine shared component behavior in feature views.

## Package map

```
design-system/
├── tokens/tokens.json        canonical, platform-neutral token contract
├── web/                      web token adapter and usage contract
│   └── ui/                   web components (Templ + Go, standalone module)
├── CONTRIBUTING.md           ownership, review, release and contribution rules
└── CHANGELOG.md              consumer-visible token and component changes
apps/web/internal/shared/web/ web shell, navigation, rendering and asset serving
apps/mobile/, apps/desktop/   future native adapters; no runnable clients yet
```

The design system is a platform capability, rather than a package owned by a
single application. Tokens describe shared intent. Adapters translate that
intent into each platform's native rendering and interaction model. Shared
visual language does not require shared markup, navigation, or component code.

## Layer model

1. **Foundations** define color palettes, spacing, type, shape, elevation,
   motion, layout and interaction targets.
2. **Semantic tokens** name roles such as canvas, surface, text, border,
   action, and status. Themes resolve these roles for light and dark contexts.
3. **Component tokens** set shared defaults for controls, cards, fields,
   navigation, and feedback. Components may use these and semantic tokens;
   they should not depend directly on raw palette values.
4. **Platform adapters** map tokens into CSS/Tailwind/DaisyUI, or native
   platform APIs. Adapters may add platform-specific behavior while retaining
   the shared semantic roles.
5. **Components and patterns** provide tested, accessible building blocks.
   Product-specific workflows remain with their feature teams.

## Consumer guarantees

- Stable semantic names; implementation values can evolve by theme.
- Light and dark themes expose the same semantic roles.
- Responsive layout intent and interaction minimums are documented without
  pretending that CSS breakpoints are native device classes.
- Keyboard, pointer, touch, reduced-motion and safe-area behavior are part of
  the platform contract where applicable.
- Breaking changes are versioned and announced in the changelog.

## Current support

The web adapter is the only implementation today. Mobile and desktop are
architecture boundaries, not runnable apps. When those clients are started,
their teams should implement native adapters from this token contract and
propose platform-specific component conventions through the same review path.

See [contribution and governance](CONTRIBUTING.md), the [web adapter](web/README.md),
the [token contract](tokens/tokens.json), and the existing [web component
catalog](web/ui/README.md).
