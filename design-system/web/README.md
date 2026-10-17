# Web adapter

The adapter maps the neutral token contract into CSS custom properties,
Tailwind v4 theme values and DaisyUI theme roles. Its generated file is
`design-system/web/tokens.css`; the source of truth is `../tokens/tokens.json`.

Regenerate after editing tokens:

```sh
bun design-system/web/build.mjs
```

The adapter is intentionally limited to web mechanics. Templ primitives and
their dependency contract are documented in
[`ui primitives`](ui/README.md).

The app stylesheet imports this generated adapter and compiles it with the
app's utility usage into the embedded runtime asset. Use `--ds-*` semantic
variables in shared CSS and component styles. Keep
DaisyUI/Tailwind utilities inside the web adapter and web application; native
clients consume semantic token names through their own mapping.
