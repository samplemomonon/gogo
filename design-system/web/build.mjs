import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const tokenFile = resolve(root, "design-system/tokens/tokens.json");
const outputFile = resolve(root, "design-system/web/tokens.css");
const tokens = JSON.parse(await readFile(tokenFile, "utf8"));

const kebab = (value) => value.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`);
const resolveReference = (value) => value.replace(/\{([^}]+)\}/g, (_, path) => {
  const [group, name] = path.split(".");
  return `var(--${group === "radius" ? "radius" : `ds-${group}`}-${kebab(name)})`;
});
const declarations = (entries, prefix) => Object.entries(entries)
  .filter(([, token]) => token && typeof token.value === "string")
  .map(([name, token]) => `  --${prefix}-${kebab(name)}: ${resolveReference(token.value)};`);

const semantic = (theme) => Object.entries(tokens.color.theme[theme])
  .map(([name, token]) => `  --ds-color-${kebab(name)}: ${token.value};`);
const darkTheme = semantic("dark").join("\n");

const neutral = declarations(tokens.color.neutral, "ds-color-neutral");
const brand = declarations(tokens.color.brand, "ds-color-brand");
const spacing = Object.entries(tokens.space)
  .map(([name, token]) => `  --spacing-${name}: ${token.value};`);
const type = Object.entries(tokens.size)
  .filter(([name]) => name.startsWith("text"))
  .map(([name, token]) => {
    const sizeName = name === "textMd" ? "base" : kebab(name.slice(4)).replace(/^-/, "");
    return `  --text-${sizeName}: ${token.value};`;
  });
const radius = declarations(tokens.radius, "radius");
const shadows = declarations(tokens.elevation, "shadow");
const motion = Object.entries(tokens.motion).map(([name, token]) => {
  const family = token.type === "cubicBezier" ? "ease" : "duration";
  const scale = name.replace(/^ease|^duration/, "");
  return `  --${family}-${scale.charAt(0).toLowerCase()}${scale.slice(1)}: ${token.value};`;
});
const typography = declarations(tokens.typography, "ds-type");
const fonts = Object.entries(tokens.typography)
  .filter(([name]) => name.startsWith("family"))
  .map(([name, token]) => `  --font-${name.slice(6).toLowerCase()}: ${token.value};`);
const fontWeights = Object.entries(tokens.typography)
  .filter(([name]) => name.startsWith("weight"))
  .map(([name, token]) => `  --font-weight-${name.slice(6).toLowerCase()}: ${token.value};`);
const leading = Object.entries(tokens.typography)
  .filter(([name]) => name.startsWith("leading"))
  .map(([name, token]) => `  --leading-${name.slice(7).toLowerCase()}: ${token.value};`);
const component = declarations(tokens.component, "ds-component");
const breakpoints = [
  `  --breakpoint-compact: ${tokens.layout.breakpointCompact.value};`,
  `  --breakpoint-expanded: ${tokens.layout.breakpointExpanded.value};`,
  `  --breakpoint-wide: ${tokens.layout.breakpointWide.value};`,
];
const layout = [
  `  --ds-content-max: ${tokens.layout.contentMax.value};`,
  `  --ds-reading-max: ${tokens.layout.readingMax.value};`,
  `  --ds-page-gutter-min: ${tokens.layout.pageGutterMin.value};`,
  `  --ds-page-gutter-max: ${tokens.layout.pageGutterMax.value};`,
  `  --ds-page-gutter: clamp(var(--ds-page-gutter-min), 4vw, var(--ds-page-gutter-max));`,
  `  --ds-target-min: ${tokens.size.targetMin.value};`,
  `  --ds-breakpoint-compact: ${tokens.layout.breakpointCompact.value};`,
  `  --ds-breakpoint-expanded: ${tokens.layout.breakpointExpanded.value};`,
  `  --ds-breakpoint-wide: ${tokens.layout.breakpointWide.value};`,
  "  --ds-safe-bottom: env(safe-area-inset-bottom, 0px);",
];

const css = `/* Generated from design-system/tokens/tokens.json. Do not edit by hand. */
:root,
:root[data-theme="gogo"] {
${[...neutral, ...brand, ...semantic("light"), ...layout].join("\n")}
${[...typography, ...component].join("\n")}
}

:root[data-theme="gogo-dark"] {
${darkTheme}
}

@media (prefers-color-scheme: dark) {
  :root:not([data-theme]) {
${darkTheme}
  }
}

@theme {
${[...spacing, ...type, ...radius, ...shadows, ...motion, ...fonts, ...fontWeights, ...leading, ...breakpoints].join("\n")}
}
`;

await writeFile(outputFile, css);
console.log(`Generated ${outputFile}`);
