# Design system contribution and governance

## Ownership

The platform team is the maintainer and final reviewer for shared foundations,
semantic token names, cross-platform component contracts, accessibility
defaults, and adapter compatibility. Feature teams own product-specific
composition and may propose changes with concrete usage evidence. An adapter
maintainer reviews changes that affect their platform. The platform team keeps
the contract coherent across adapters and records decisions here or in the
changelog.

## Contribution path

1. Identify the repeated user need and at least two real consumers, or explain
   why a foundational accessibility or platform requirement needs an exception.
2. Propose the semantic intent and consumer API before choosing implementation
   classes. Do not expose utility-class strings as cross-platform contracts.
3. Update canonical tokens and all currently supported adapters in one change.
   Include light/dark behavior, responsive behavior, interactive states and
   accessibility implications.
4. Add or update component render assertions and platform-specific checks when
   implementation changes. Review narrow and wide layouts and keyboard/touch
   interaction.
5. Record consumer-visible changes in `CHANGELOG.md`. Breaking semantic/API
   changes require a migration note and a major version; additive roles and
   variants are minor; value corrections and fixes are patch changes.

## Token rules

- Use lowercase dotted semantic names in documentation and adapter APIs.
- Keep raw palettes private to adapters and exceptional data visualization.
- Alias intent to theme values; do not make a product feature the name of a
  token (for example, use `action.primary`, not `seller.saveButton`).
- Add a token only when it represents a repeated decision or a platform
  contract. Avoid single-use component styling tokens.
- Color roles must preserve readable foreground/background pairs. State roles
  need text, icon or shape cues; color alone cannot carry meaning.
- Motion must be purposeful and honor reduced-motion settings. Interaction
  targets must account for touch and keyboard users.
- Responsive rules describe available layout space and content behavior, not
  device brands or assumed hardware.

## Component rules

- Components own presentation, interaction feedback and accessibility
  mechanics; product teams own labels, domain meaning and workflow decisions.
- A shared component needs real repeated consumers or a clear cross-platform
  foundation role. Keep the API small and prefer explicit variants over
  arbitrary style passthroughs.
- Preserve semantic HTML/native controls, focus visibility, disabled/pending
  states, and graceful operation without optional JavaScript.
- Do not bring framework-specific abstractions into the neutral token
  contract.

## Review checklist

- Do names express intent and work across web, mobile and desktop?
- Are all themes and adapters updated from the canonical source?
- Are contrast, focus, touch targets, reduced motion, safe areas and text
  scaling considered?
- Does the change remove drift for existing consumers without forcing
  unrelated product redesign?
- Are migration notes and ownership clear?
