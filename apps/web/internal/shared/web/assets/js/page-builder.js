// Visual page builder.
//
// The server owns the document schema, validation, persistence, and public
// rendering. This module owns only editor state and the canvas. Every document
// mutation goes through page-builder-core.js, and every preview element is
// built with DOM APIs (textContent, never innerHTML) so component text can
// never become markup.

import {
  History,
  SaveCoordinator,
  canContain,
  clone,
  componentSummary,
  definitionsMap,
  duplicateNode,
  findNode,
  hasOverride,
  indentNode,
  insertNode,
  isDescendant,
  moveNode,
  newComponent,
  outdentNode,
  remapIds,
  removeNode,
  resetResponsive,
  resolveProps,
  safeURL,
  setHidden,
  shiftSibling,
  updateProp,
  validateDocument,
} from "./page-builder-core.js";

const SPACING = {
  none: "0",
  xs: "4px",
  sm: "8px",
  md: "16px",
  lg: "24px",
  xl: "40px",
};

const FONT_SIZES = {
  sm: "0.875rem",
  md: "1rem",
  lg: "1.125rem",
  xl: "1.5rem",
  "2xl": "2.25rem",
};

const FONT_WEIGHTS = {
  normal: "400",
  medium: "500",
  semibold: "600",
  bold: "700",
};

const COLOR_PATTERN = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/;
const HEADING_TAGS = { h1: "h1", h2: "h2", h3: "h3", h4: "h4" };
const HEADING_CLASSES = {
  h1: "text-3xl font-bold tracking-tight",
  h2: "text-2xl font-bold tracking-tight",
  h3: "text-xl font-semibold tracking-tight",
  h4: "text-lg font-semibold",
};
const ICONS = {
  container: "▦",
  heading: "H",
  text: "¶",
  image: "▧",
  button: "▭",
  spacer: "↕",
  navbar: "☰",
  form: "⌑",
  "data-text": "{}",
  "ad-slot": "Ad",
};
const IMMEDIATE_KINDS = new Set(["boolean", "enum", "spacing"]);
// URL and color inputs are only committed on blur/Enter. Committing partial
// values on every keystroke would make autosave send invalid URLs and colors
// that the server correctly rejects.
const DEFERRED_KINDS = new Set(["color", "image", "url", "same-origin-url"]);

const root = document.querySelector("[data-page-builder]");

if (root) {
  try {
    init(root);
  } catch (error) {
    console.error("Page builder failed to start", error);
    showFatal(root, "The page builder could not start. Reload the page to try again.");
  }
}

function showFatal(target, message) {
  target.replaceChildren(
    h("div", { class: "grid min-h-dvh place-items-center p-8" }, [
      h("div", { class: "max-w-md rounded-2xl border border-error/40 bg-base-100 p-6" }, [
        h("h1", { class: "text-lg font-semibold", text: "Page builder unavailable" }),
        h("p", { class: "mt-2 text-sm text-base-content/70", text: message }),
      ]),
    ]),
  );
}

function h(tag, options = {}, children = []) {
  const node = document.createElement(tag);
  if (options.class) node.className = options.class;
  if (options.text !== undefined) node.textContent = String(options.text);
  if (options.attrs) {
    for (const [key, value] of Object.entries(options.attrs)) {
      if (value !== undefined && value !== null && value !== false) node.setAttribute(key, String(value));
    }
  }
  if (options.style) {
    for (const [key, value] of Object.entries(options.style)) {
      if (value !== undefined && value !== null && value !== "") node.style.setProperty(key, String(value));
    }
  }
  for (const child of children.flat()) {
    if (child !== null && child !== undefined) node.append(child);
  }
  return node;
}

function readBootstrap() {
  const element = document.getElementById("page-builder-data");
  if (!element) throw new Error("Missing page-builder-data bootstrap script");
  const payload = JSON.parse(element.textContent || "{}");
  if (!payload.page || !payload.document || !Array.isArray(payload.document.components)) {
    throw new Error("Incomplete page-builder bootstrap payload");
  }
  return payload;
}

function init(builderRoot) {
  const bootstrap = readBootstrap();
  const refs = {
    root: builderRoot,
    canvas: builderRoot.querySelector("[data-canvas]"),
    canvasBreakpointLabel: builderRoot.querySelector("[data-canvas-breakpoint-label]"),
    canvasWidthLabel: builderRoot.querySelector("[data-canvas-width-label]"),
    inspector: builderRoot.querySelector("[data-inspector]"),
    outline: builderRoot.querySelector("[data-outline]"),
    sections: builderRoot.querySelector("[data-sections]"),
    library: builderRoot.querySelector("[data-library-results]"),
    librarySearch: builderRoot.querySelector("[data-library-search]"),
    toolbarTitle: builderRoot.querySelector("[data-toolbar-title]"),
    undo: builderRoot.querySelector('[data-action="undo"]'),
    redo: builderRoot.querySelector('[data-action="redo"]'),
    retrySave: builderRoot.querySelector('[data-action="retry-save"]'),
    saveStatus: builderRoot.querySelector("[data-save-status]"),
    saveLabel: builderRoot.querySelector("[data-save-label]"),
    announcer: builderRoot.querySelector("[data-announcer]"),
    banner: builderRoot.querySelector("[data-builder-banner]"),
    bannerTitle: builderRoot.querySelector("[data-banner-title]"),
    bannerMessage: builderRoot.querySelector("[data-banner-message]"),
    bannerActions: builderRoot.querySelector("[data-banner-actions]"),
    publishBody: document.querySelector('[data-dialog="publish"] [data-publish-body]'),
    publishTitle: document.querySelector('[data-dialog="publish"] [data-publish-title]'),
    revisionsBody: document.querySelector('[data-dialog="revisions"] [data-revisions-body]'),
    conflictRevision: document.querySelector("[data-conflict-revision]"),
    diagnosticsBody: document.querySelector('[data-dialog="diagnostics"] [data-diagnostics-body]'),
    sectionForm: document.querySelector("[data-section-form]"),
    sectionName: document.querySelector("[data-section-name]"),
    sectionPreview: document.querySelector("[data-section-preview]"),
  };
  if (!refs.canvas || !refs.inspector || !refs.library || !refs.outline || !refs.sections) {
    throw new Error("Required builder shell is missing");
  }
  refs.canvas.tabIndex = -1;

  const state = {
    document: bootstrap.document,
    title: bootstrap.page.title || "",
    revision: bootstrap.page.revision || 0,
    status: bootstrap.page.status || "draft",
    breakpoint: "desktop",
    selectedId: "",
    activePanel: "library",
    search: "",
    definitions: Array.isArray(bootstrap.definitions) ? bootstrap.definitions : [],
    categories: Array.isArray(bootstrap.categories) ? bootstrap.categories : [],
    presets: Array.isArray(bootstrap.presets) ? bootstrap.presets : [],
    sections: Array.isArray(bootstrap.sections) ? bootstrap.sections : [],
    sectionsError: "",
    dataSources: Array.isArray(bootstrap.dataSources) ? bootstrap.dataSources : [],
    values: bootstrap.values || {},
    endpoints: bootstrap.endpoints || {},
    breakpoints: Array.isArray(bootstrap.breakpoints) && bootstrap.breakpoints.length > 0
      ? bootstrap.breakpoints
      : [
          { id: "desktop", label: "Desktop", width: 1024 },
          { id: "tablet", label: "Tablet", width: 768 },
          { id: "mobile", label: "Mobile", width: 390 },
        ],
    diagnostics: Array.isArray(bootstrap.diagnostics) ? bootstrap.diagnostics : [],
    dirty: false,
    saveError: "",
    conflict: false,
    conflictCurrent: 0,
    success: null,
    savedDocument: "",
    savedTitle: "",
    bindingDrafts: new Map(),
    sectionTargetId: "",
    actionSequence: 0,
    dropTarget: null,
    dragPayload: null,
  };

  state.savedDocument = JSON.stringify(state.document);
  state.savedTitle = state.title;
  const definitions = definitionsMap(state.definitions);
  const undoHistory = new History(state.document);
  const saveCoordinator = new SaveCoordinator({ delay: 800 });

  if (state.categories.length === 0) {
    const seen = new Set();
    state.categories = state.definitions
      .map((definition) => definition.category)
      .filter((category) => category && !seen.has(category) && seen.add(category))
      .map((category) => ({ id: category, label: category }));
  }

  // --------------------------------------------------------------------------
  // Small shared helpers

  function definitionFor(type) {
    return definitions.get(type) || null;
  }

  function selectedNode() {
    if (!state.selectedId) return null;
    return findNode(state.document.components, state.selectedId);
  }

  function announce(message) {
    refs.announcer.textContent = "";
    window.setTimeout(() => {
      refs.announcer.textContent = message;
    }, 20);
  }

  function documentJSON() {
    return JSON.stringify(state.document);
  }

  function isDirty() {
    return documentJSON() !== state.savedDocument || state.title.trim() !== state.savedTitle;
  }

  function structuralKey(action, id) {
    state.actionSequence += 1;
    return `structure:${action}:${id}:${state.actionSequence}`;
  }

  function setDropTarget(target) {
    clearDropIndicators();
    state.dropTarget = target;
    if (!target || !target.element) return;
    if (target.root) target.element.classList.add("is-drop-root");
    else if (target.inside) target.element.classList.add("is-drop-inside");
    else if (target.after) target.element.classList.add("is-drop-after");
    else target.element.classList.add("is-drop-before");
  }

  function clearDropIndicators() {
    root.querySelectorAll(".is-over, .is-drop-before, .is-drop-after, .is-drop-inside, .is-drop-root").forEach((element) => {
      element.classList.remove("is-over", "is-drop-before", "is-drop-after", "is-drop-inside", "is-drop-root");
    });
    state.dropTarget = null;
  }

  // --------------------------------------------------------------------------
  // Component library

  function matches(text, query) {
    return text.toLowerCase().includes(query);
  }

  function definitionMatches(definition, query) {
    if (!query) return true;
    const haystack = [
      definition.type,
      definition.label,
      definition.description,
      definition.category,
      ...(definition.properties || []).map((property) => `${property.key} ${property.label}`),
    ].join(" ").toLowerCase();
    return haystack.includes(query);
  }

  function presetMatches(preset, query) {
    if (!query) return true;
    return `${preset.name} ${preset.description} ${preset.category}`.toLowerCase().includes(query);
  }

  function libraryItem({ label, description, icon, badge, attrs }) {
    const body = h("span", { class: "builder-library-copy" }, [
      h("strong", { text: label }),
      h("small", { text: description }),
    ]);
    const button = h(
      "button",
      {
        class: "builder-library-item",
        attrs: { type: "button", draggable: "true", ...attrs },
      },
      [h("span", { class: "builder-library-icon", attrs: { "aria-hidden": "true" }, text: icon }), body],
    );
    if (badge) button.append(h("span", { class: "builder-library-badge", text: badge }));
    button.draggable = true;
    button.dataset.search = `${label} ${description}`.toLowerCase();
    return button;
  }

  function renderLibrary() {
    const query = state.search.trim().toLowerCase();
    const fragment = document.createDocumentFragment();
    let count = 0;

    for (const category of state.categories) {
      const definitionsInCategory = state.definitions.filter(
        (definition) => definition.category === category.id && definitionMatches(definition, query),
      );
      const presetsInCategory = state.presets.filter(
        (preset) => preset.category === category.id && presetMatches(preset, query),
      );
      if (definitionsInCategory.length === 0 && presetsInCategory.length === 0) continue;

      const group = h("section", { class: "builder-library-group", attrs: { "aria-label": category.label } }, [
        h("h3", { class: "builder-library-heading", text: category.label }),
      ]);
      for (const definition of definitionsInCategory) {
        group.append(
          libraryItem({
            label: definition.label,
            description: definition.description,
            icon: ICONS[definition.type] || "＋",
            attrs: { "data-library-type": definition.type, title: `Add ${definition.label}` },
          }),
        );
        count += 1;
      }
      for (const preset of presetsInCategory) {
        group.append(
          libraryItem({
            label: preset.name,
            description: preset.description,
            icon: "◇",
            badge: "Preset",
            attrs: { "data-preset-id": preset.id, title: `Insert ${preset.name}` },
          }),
        );
        count += 1;
      }
      fragment.append(group);
    }

    if (count === 0) {
      fragment.append(h("p", { class: "builder-muted", text: "No components match that search." }));
    }
    refs.library.replaceChildren(fragment);
  }

  // --------------------------------------------------------------------------
  // Canvas rendering

  function dropzone(parentId, index) {
    return h("div", {
      class: "builder-dropzone",
      attrs: {
        "data-drop-parent": parentId || "",
        "data-drop-index": index,
        "aria-hidden": "true",
      },
    });
  }

  function colorValue(value) {
    return typeof value === "string" && COLOR_PATTERN.test(value) ? value : "";
  }

  function applyComponentStyle(node, type, props) {
    const add = (property, value) => {
      if (value !== "" && value !== undefined && value !== null) node.style.setProperty(property, value);
    };
    const spacing = (key) => SPACING[props[key]] || "";
    const color = (key) => colorValue(props[key]);
    const align = ["left", "center", "right"].includes(props.align) ? props.align : "";

    if (type === "container") {
      if (props.layout === "grid") {
        add("display", "grid");
        const columns = Number.isFinite(Number(props.columns)) ? Math.min(4, Math.max(1, Number(props.columns))) : 2;
        add("grid-template-columns", `repeat(${columns},minmax(0,1fr))`);
      } else if (props.layout === "stack") {
        add("display", "flex");
        add("flex-direction", "column");
      }
      add("gap", spacing("gap"));
      add("padding", spacing("padding"));
      add("background-color", color("background"));
      if (props.maxWidth === "wide") {
        add("max-width", "72rem");
        add("margin-inline", "auto");
      } else if (props.maxWidth === "narrow") {
        add("max-width", "48rem");
        add("margin-inline", "auto");
      }
      if (props.rounded === true) add("border-radius", "1rem");
      if (props.rounded === false) add("border-radius", "0");
    } else if (type === "heading" || type === "text" || type === "data-text") {
      add("font-size", FONT_SIZES[props.size] || "");
      add("font-weight", FONT_WEIGHTS[props.weight] || "");
      add("text-align", align);
      add("color", color("color"));
    } else if (type === "image") {
      if (props.aspect === "square") add("aspect-ratio", "1 / 1");
      if (props.aspect === "video") add("aspect-ratio", "16 / 9");
      if (props.fit === "cover" || props.fit === "contain") add("object-fit", props.fit);
      if (props.width === "wide") add("max-width", "72rem");
      if (props.width === "narrow") add("max-width", "48rem");
      if (props.rounded === true) add("border-radius", "0.75rem");
      if (props.rounded === false) add("border-radius", "0");
    } else if (type === "button") {
      if (align === "center") add("justify-content", "center");
      else if (align === "right") add("justify-content", "flex-end");
      if (props.fullWidth === true) {
        add("display", "flex");
        add("width", "100%");
      }
    } else if (type === "spacer") {
      add("height", spacing("height"));
    } else if (type === "navbar") {
      if (props.sticky === true) {
        add("position", "sticky");
        add("top", "0");
      }
      add("background-color", color("background"));
      add("color", color("color"));
    } else if (type === "form") {
      add("text-align", align);
      add("background-color", color("background"));
      add("color", color("color"));
    } else if (type === "ad-slot") {
      if (props.format === "rectangle") add("min-height", "250px");
      else if (props.format === "inline") add("min-height", "120px");
      else add("min-height", "90px");
    }
  }

  function buttonClasses(props) {
    let classes = "btn";
    const variant = ["primary", "secondary", "outline", "ghost"].includes(props.variant) ? props.variant : "primary";
    classes += ` btn-${variant}`;
    if (props.size === "sm") classes += " btn-sm";
    if (props.size === "lg") classes += " btn-lg";
    if (props.fullWidth === true) classes += " w-full";
    return classes;
  }

  function renderFormFields(fields) {
    const fragment = document.createDocumentFragment();
    for (const field of Array.isArray(fields) ? fields : []) {
      if (!field || typeof field !== "object") continue;
      const label = h("label", { class: "form-control" }, [
        h("span", { class: "label-text mb-1", text: `${field.label || field.name || "Field"}${field.required ? " *" : ""}` }),
      ]);
      if (field.type === "textarea") {
        label.append(h("textarea", {
          class: "textarea textarea-bordered w-full",
          attrs: { name: field.name || "", rows: 4, disabled: "disabled", placeholder: field.label || "" },
        }));
      } else {
        const type = field.type === "email" || field.type === "tel" ? field.type : "text";
        label.append(h("input", {
          class: "input input-bordered w-full",
          attrs: { type, name: field.name || "", disabled: "disabled", placeholder: field.label || "" },
        }));
      }
      fragment.append(label);
    }
    return fragment;
  }

  function createComponentElement(component, breakpoint) {
    const definition = definitionFor(component.type);
    const props = resolveProps(component, breakpoint);
    const common = { "data-cms-component": component.type, "data-cms-id": component.id };
    let node;

    switch (component.type) {
      case "container":
        node = h("div", {
          class: `cms-${component.id} rounded-2xl border border-base-300 bg-base-100 p-6`,
          attrs: common,
        });
        break;
      case "heading": {
        const level = HEADING_TAGS[props.level] || "h2";
        node = h(level, {
          class: `cms-${component.id} ${HEADING_CLASSES[level]}`,
          attrs: common,
          text: props.text || "Heading text",
        });
        break;
      }
      case "text":
        node = h("p", {
          class: `cms-${component.id} leading-7`,
          attrs: common,
          text: props.text || "Text content",
        });
        break;
      case "image":
        if (props.src && safeURL(props.src)) {
          node = h("img", {
            class: `cms-${component.id} max-h-[32rem] w-full rounded-xl object-cover`,
            attrs: { ...common, src: props.src, alt: props.alt || "", loading: "lazy" },
          });
          node.style.width = "100%";
        } else {
          node = h("div", {
            class: `cms-${component.id} flex min-h-32 items-center justify-center rounded-xl border border-dashed border-base-300 bg-base-200 p-6 text-sm text-base-content/60`,
            attrs: common,
            text: "Image URL required",
          });
        }
        break;
      case "button":
        node = h("div", {
          class: `cms-${component.id} flex`,
          attrs: common,
        }, [
          h("span", {
            class: buttonClasses(props),
            text: props.label || "Button",
            attrs: { role: "presentation", title: props.href || "" },
          }),
        ]);
        break;
      case "spacer":
        node = h("div", {
          class: `cms-${component.id} h-4 w-full`,
          attrs: { ...common, "aria-hidden": "true" },
        });
        break;
      case "navbar":
        node = h("nav", {
          class: `cms-${component.id} flex flex-wrap items-center justify-between gap-4 rounded-xl border border-base-300 bg-base-100 px-5 py-3`,
          attrs: { ...common, "aria-label": "Page navigation" },
        }, [
          h("span", { class: "font-semibold", text: props.brand || "Brand" }),
          h("div", { class: "flex flex-wrap items-center gap-4" },
            (Array.isArray(props.links) ? props.links : []).map((link) =>
              h("span", { class: "text-sm font-medium", text: link?.label || "Link", attrs: { title: link?.href || "" } }),
            ),
          ),
        ]);
        break;
      case "form":
        node = h("div", {
          id: `form-${component.id}`,
          class: `cms-${component.id} space-y-4 rounded-2xl border border-base-300 bg-base-100 p-6`,
          attrs: common,
        }, [
          h("h2", { class: "text-xl font-semibold", text: props.title || "Form" }),
          renderFormFields(props.fields),
          h("span", { class: "btn btn-primary", text: props.submitLabel || "Submit" }),
        ]);
        break;
      case "data-text": {
        const binding = component.binding;
        const bound = binding ? state.values[`${binding.source}.${binding.field}`] : "";
        node = h("p", {
          class: `cms-${component.id} leading-7`,
          attrs: common,
          text: bound || props.fallback || "Bound text",
        });
        break;
      }
      case "ad-slot":
        node = h("aside", {
          class: `cms-${component.id} flex min-h-24 flex-col items-center justify-center gap-1 rounded-xl border border-dashed border-base-300 bg-base-200/60 p-4 text-center${props.active === false ? " opacity-60" : ""}`,
          attrs: { ...common, "data-ad-slot": props.slot || "", "aria-label": props.label || "Advertisement" },
        }, [
          h("span", { class: "text-[11px] font-semibold uppercase tracking-[0.18em] text-base-content/50", text: props.label || "Advertisement" }),
          h("span", { class: "text-xs text-base-content/40", text: props.active === false ? `Inactive placement · ${props.slot || "unset"}` : `Reserved placement · ${props.slot || "unset"}` }),
        ]);
        break;
      default:
        node = h("div", {
          class: "rounded border border-warning bg-warning/10 p-3 text-sm",
          attrs: { "data-cms-diagnostic": component.type },
          text: `Unsupported component: ${component.type}`,
        });
    }

    if (definition) applyComponentStyle(node, component.type, props);
    node.dataset.cmsComponent = component.type;
    node.dataset.cmsId = component.id;
    return node;
  }

  function renderCanvasNode(component) {
    const definition = definitionFor(component.type);
    const summary = componentSummary(component, definition);
    const hidden = component.hidden === true;
    const wrapper = h("div", {
      class: `builder-node${state.selectedId === component.id ? " is-selected" : ""}${hidden ? " is-hidden" : ""}`,
      attrs: {
        "data-node-id": component.id,
        "data-node-type": component.type,
        role: "group",
        tabindex: "0",
        draggable: "true",
        "aria-label": `${definition?.label || component.type}: ${summary}${hidden ? " (hidden)" : ""}`,
      },
    });
    const content = h("div", { class: "builder-node-content" });
    const element = createComponentElement(component, state.breakpoint);
    content.append(element);
    wrapper.append(content);

    if (component.type === "container") {
      element.append(dropzone(component.id, 0));
      const children = Array.isArray(component.children) ? component.children : [];
      children.forEach((child, index) => {
        element.append(renderCanvasNode(child));
        element.append(dropzone(component.id, index + 1));
      });
      if (children.length === 0) {
        element.append(h("p", { class: "builder-container-empty", text: "Drop components here" }));
      }
    }

    wrapper.append(
      h("span", { class: "builder-node-label" }, [
        h("span", { text: definition?.label || component.type }),
        hidden ? h("span", { class: "builder-node-hidden-badge", text: "Hidden" }) : null,
      ]),
    );
    return wrapper;
  }

  function renderCanvas() {
    const focusedNode = document.activeElement?.closest?.(".builder-node");
    const hadCanvasFocus = focusedNode && refs.canvas.contains(focusedNode);
    const focusedId = focusedNode?.dataset.nodeId;
    const fragment = document.createDocumentFragment();
    fragment.append(dropzone("", 0));
    if (state.document.components.length === 0) {
      fragment.append(
        h("div", { class: "builder-canvas-empty" }, [
          h("p", { class: "text-sm font-medium", text: "Your page is empty" }),
          h("p", { class: "mt-1 text-xs text-base-content/60", text: "Add a component from the library or drop one here." }),
        ]),
      );
    } else {
      state.document.components.forEach((component, index) => {
        fragment.append(renderCanvasNode(component));
        fragment.append(dropzone("", index + 1));
      });
    }
    refs.canvas.replaceChildren(fragment);
    refs.canvas.dataset.breakpoint = state.breakpoint;
    if (hadCanvasFocus && focusedId) {
      refs.canvas.querySelector(`[data-node-id="${cssEscape(focusedId)}"]`)?.focus({ preventScroll: true });
    }
  }

  function cssEscape(value) {
    if (globalThis.CSS?.escape) return CSS.escape(value);
    return String(value).replace(/["\\]/g, "\\$&");
  }

  // --------------------------------------------------------------------------
  // Outline

  function actionButton(label, title, action, id, disabled) {
    return h("button", {
      class: "builder-outline-action",
      text: label,
      attrs: {
        type: "button",
        title,
        "aria-label": title,
        "data-outline-action": action,
        "data-node-id": id,
        disabled: disabled ? "disabled" : undefined,
      },
    });
  }

  function renderOutlineList(components, depth = 1) {
    const list = h("ul", { class: depth === 1 ? "builder-outline-tree" : "builder-outline-children" });
    for (const component of components) {
      const location = findNode(state.document.components, component.id);
      const definition = definitionFor(component.type);
      const parent = location?.parent || null;
      const index = location?.index ?? 0;
      const siblings = location?.siblings || [];
      const previous = index > 0 ? siblings[index - 1] : null;
      const canIndent = previous && previous.type === "container" && canContain(definitionFor(previous.type), component.type);
      const row = h("div", {
        class: `builder-outline-row${state.selectedId === component.id ? " is-selected" : ""}${component.hidden ? " is-hidden" : ""}`,
        style: { "padding-left": `${(depth - 1) * 12}px` },
      });
      const select = h("button", {
        class: "builder-outline-select",
        attrs: {
          type: "button",
          "data-outline-select": component.id,
          "aria-current": state.selectedId === component.id ? "true" : undefined,
          title: componentSummary(component, definition),
        },
      }, [
        h("span", { class: "builder-outline-type", text: definition?.label || component.type }),
        h("span", { class: "builder-outline-summary", text: componentSummary(component, definition) }),
      ]);
      row.append(select);
      const actions = h("div", { class: "builder-outline-actions" });
      actions.append(
        actionButton("↑", "Move up", "up", component.id, index === 0),
        actionButton("↓", "Move down", "down", component.id, index >= siblings.length - 1),
        actionButton("→", "Nest in previous container", "indent", component.id, !canIndent),
        actionButton("←", "Move out of container", "outdent", component.id, !parent),
        actionButton("⧉", "Duplicate", "duplicate", component.id, false),
        actionButton(component.hidden ? "◉" : "◌", component.hidden ? "Show component" : "Hide component", "hide", component.id, false),
        actionButton("✕", "Delete component", "delete", component.id, false),
      );
      row.append(actions);
      const item = h("li", { class: "builder-outline-item" }, [row]);
      if (Array.isArray(component.children) && component.children.length > 0) {
        item.append(renderOutlineList(component.children, depth + 1));
      }
      list.append(item);
    }
    return list;
  }

  function renderOutline() {
    if (state.document.components.length === 0) {
      refs.outline.replaceChildren(h("p", { class: "builder-muted", text: "No components yet." }));
      return;
    }
    refs.outline.replaceChildren(renderOutlineList(state.document.components));
  }

  // --------------------------------------------------------------------------
  // Inspector

  // Continued in the next section (functions are still inside init).

  function controlID(component, property, extra = {}) {
    const suffix = extra.arrayKey ? `-${extra.arrayIndex}-${property.key}` : `-${property.key}`;
    const prefix = `field-${component.id}-${state.breakpoint}`;
    return `${prefix}${suffix}`.replace(/[^A-Za-z0-9_-]/g, "-");
  }

  function propertyValue(component, property, extra = {}) {
    if (extra.arrayKey) {
      const props = resolveProps(component, state.breakpoint);
      const array = Array.isArray(props[extra.arrayKey]) ? props[extra.arrayKey] : [];
      const item = array[extra.arrayIndex] || {};
      return item[property.key];
    }
    const props = resolveProps(component, state.breakpoint);
    return props[property.key];
  }

  function createControl(component, property, extra = {}) {
    const id = controlID(component, property, extra);
    const value = propertyValue(component, property, extra);
    const attrs = {
      id,
      "data-prop-key": property.key,
      "data-prop-kind": property.kind,
      "aria-label": property.label || property.key,
    };
    if (extra.arrayKey) {
      attrs["data-array-key"] = extra.arrayKey;
      attrs["data-array-index"] = extra.arrayIndex;
    }

    if (property.kind === "boolean") {
      const input = h("input", {
        class: "checkbox checkbox-sm",
        attrs: { ...attrs, type: "checkbox" },
      });
      input.checked = value === true;
      return input;
    }

    if (property.kind === "text") {
      const input = h("textarea", {
        class: "textarea textarea-bordered w-full",
        attrs: { ...attrs, rows: 4, maxlength: property.maxLength || undefined, spellcheck: "true" },
      });
      input.value = typeof value === "string" ? value : "";
      return input;
    }

    if (property.kind === "enum" || property.kind === "spacing") {
      const select = h("select", { class: "select select-bordered w-full", attrs });
      const options = property.options || [];
      for (const option of options) {
        select.append(h("option", {
          text: option.label,
          attrs: { value: option.value, selected: option.value === value ? "selected" : undefined },
        }));
      }
      const known = options.some((option) => option.value === value);
      if (!known && options.length > 0) select.value = options[0].value;
      else select.value = typeof value === "string" ? value : "";
      return select;
    }

    if (property.kind === "number") {
      const input = h("input", {
        class: "input input-bordered w-full",
        attrs: {
          ...attrs,
          type: "number",
          min: property.hasRange ? property.min : undefined,
          max: property.hasRange ? property.max : undefined,
          step: property.hasRange ? "1" : "any",
        },
      });
      input.value = Number.isFinite(Number(value)) ? String(value) : "";
      return input;
    }

    if (property.kind === "color") {
      const input = h("input", {
        class: "input input-bordered w-full builder-color-input",
        attrs: {
          ...attrs,
          type: "text",
          placeholder: "#RRGGBB or leave blank",
          maxlength: 7,
          autocomplete: "off",
          pattern: "#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})",
        },
      });
      input.value = typeof value === "string" ? value : "";
      return input;
    }

    if (property.kind === "array") return null;

    const input = h("input", {
      class: "input input-bordered w-full",
      attrs: {
        ...attrs,
        type: "text",
        inputmode: property.kind === "image" || property.kind === "url" || property.kind === "same-origin-url" ? "url" : undefined,
        placeholder: property.placeholder || "",
        maxlength: property.maxLength || undefined,
        autocomplete: "off",
      },
    });
    input.value = typeof value === "string" ? value : "";
    return input;
  }

  function renderArrayField(component, property) {
    const value = propertyValue(component, property);
    const items = Array.isArray(value) ? value : [];
    const wrapper = h("div", { class: "builder-array" });
    items.forEach((item, index) => {
      const fieldset = h("fieldset", { class: "builder-array-item" }, [
        h("legend", { text: `${property.label || property.key} ${index + 1}` }),
      ]);
      for (const itemProperty of property.itemProperties || []) {
        const id = controlID(component, itemProperty, { arrayKey: property.key, arrayIndex: index });
        const control = createControl(component, itemProperty, { arrayKey: property.key, arrayIndex: index });
        const label = h("label", { class: "builder-field", attrs: { for: id } }, [
          h("span", { class: "builder-field-label", text: itemProperty.label || itemProperty.key }),
          control,
        ]);
        if (itemProperty.help) label.append(h("span", { class: "builder-field-help", text: itemProperty.help }));
        fieldset.append(label);
      }
      fieldset.append(
        h("button", {
          class: "btn btn-ghost btn-xs",
          text: "Remove",
          attrs: {
            type: "button",
            "data-array-remove": property.key,
            "data-array-index": index,
            "aria-label": `Remove ${property.label || property.key} ${index + 1}`,
          },
        }),
      );
      wrapper.append(fieldset);
    });
    const atLimit = property.maxItems > 0 && items.length >= property.maxItems;
    wrapper.append(
      h("button", {
        class: "btn btn-outline btn-xs",
        text: `Add ${property.label ? property.label.replace(/s$/, "") : "item"}`,
        attrs: {
          type: "button",
          "data-array-add": property.key,
          disabled: atLimit ? "disabled" : undefined,
        },
      }),
    );
    return wrapper;
  }

  function renderPropertyRow(component, definition, property) {
    const breakpoint = state.breakpoint;
    const override = breakpoint !== "desktop" && hasOverride(component, property.key, breakpoint);
    const row = h("div", {
      class: `builder-property${override ? " has-override" : ""}`,
      attrs: { "data-property-key": property.key },
    });
    const head = h("div", { class: "builder-property-head" });
    if (property.kind === "boolean") {
      const id = controlID(component, property);
      const control = createControl(component, property);
      control.id = id;
      const label = h("label", { class: "builder-field-check", attrs: { for: id } }, [
        control,
        h("span", { text: property.label || property.key }),
      ]);
      head.append(label);
    } else {
      head.append(h("label", {
        class: "builder-field-label",
        text: property.label || property.key,
        attrs: { for: controlID(component, property) },
      }));
    }
    if (override) {
      head.append(
        h("button", {
          class: "builder-reset-prop",
          text: "Reset",
          attrs: {
            type: "button",
            "data-reset-prop": property.key,
            "aria-label": `Reset ${property.label || property.key} override`,
            title: "Remove this breakpoint override",
          },
        }),
      );
    }
    row.append(head);

    if (property.kind === "array") {
      row.append(renderArrayField(component, property));
    } else if (property.kind !== "boolean") {
      const control = createControl(component, property);
      if (control) row.append(control);
    }

    if (property.help) row.append(h("span", { class: "builder-field-help", text: property.help }));
    if (breakpoint !== "desktop" && property.responsive) {
      row.append(
        h("span", {
          class: "builder-inherit-hint",
          text: override ? `Overrides ${breakpoint === "mobile" ? "tablet and desktop" : "desktop"}` : "Inherited value",
        }),
      );
    }
    return row;
  }

  function renderBindingSection(component, definition) {
    if (!definition.bindingAllowed) return null;
    const draftSource = state.bindingDrafts.has(component.id)
      ? state.bindingDrafts.get(component.id)
      : component.binding?.source || "";
    const source = state.dataSources.find((candidate) => candidate.id === draftSource) || null;
    const field = component.binding?.source === draftSource ? component.binding?.field || "" : "";

    const sourceSelect = h("select", {
      class: "select select-bordered select-sm w-full",
      attrs: { "data-binding-source": component.id, "aria-label": "Data source" },
    }, [
      h("option", { text: "No binding", attrs: { value: "" } }),
      ...state.dataSources.map((candidate) =>
        h("option", { text: candidate.label, attrs: { value: candidate.id, selected: candidate.id === draftSource ? "selected" : undefined } }),
      ),
    ]);
    sourceSelect.value = draftSource;

    const fieldSelect = h("select", {
      class: "select select-bordered select-sm w-full",
      attrs: { "data-binding-field": component.id, "aria-label": "Data field" },
    }, [
      h("option", { text: source ? "Choose a field" : "Choose a source first", attrs: { value: "" } }),
      ...(source?.fields || []).map((candidate) =>
        h("option", { text: candidate.label, attrs: { value: candidate.id, selected: candidate.id === field ? "selected" : undefined } }),
      ),
    ]);
    fieldSelect.value = field;

    let preview = null;
    if (component.binding && field) {
      const resolved = state.values[`${component.binding.source}.${component.binding.field}`] || "";
      preview = h("p", { class: "builder-binding-preview", text: `Current value: ${resolved || "(empty)"}` });
    }

    return h("section", { class: "builder-inspector-section" }, [
      h("h3", { class: "builder-inspector-heading", text: "Data binding" }),
      h("label", { class: "builder-field" }, [
        h("span", { class: "builder-field-label", text: "Source" }),
        sourceSelect,
      ]),
      h("label", { class: "builder-field" }, [
        h("span", { class: "builder-field-label", text: "Field" }),
        fieldSelect,
      ]),
      preview,
      h("p", { class: "builder-field-help", text: "Only approved, server-provided values can be bound." }),
    ]);
  }

  function renderPageSettings() {
    const title = h("input", {
      class: "input input-bordered w-full",
      attrs: { type: "text", "data-page-title": "true", maxlength: 240, autocomplete: "off", "aria-label": "Page title" },
    });
    title.value = state.title;
    return h("div", { class: "builder-inspector-empty" }, [
      h("p", { class: "builder-eyebrow", text: "Page" }),
      h("h2", { class: "builder-inspector-title", text: "Page settings" }),
      h("p", { class: "builder-muted", text: "Select a component to edit its properties." }),
      h("label", { class: "builder-field" }, [
        h("span", { class: "builder-field-label", text: "Page title" }),
        title,
      ]),
      h("p", {
        class: "builder-page-meta",
        text: `/${state.document && bootstrap.page.slug ? bootstrap.page.slug : ""} · ${state.status} · revision ${state.revision}`,
        attrs: { "data-page-meta": "true" },
      }),
    ]);
  }

  function renderInspector(options = {}) {
    const scrollTop = refs.inspector.scrollTop;
    const location = selectedNode();
    if (!location) {
      state.selectedId = "";
      refs.inspector.replaceChildren(renderPageSettings());
      refs.inspector.scrollTop = scrollTop;
      return;
    }
    const component = location.component;
    const definition = definitionFor(component.type) || {
      type: component.type,
      label: component.type,
      properties: [],
    };
    const actions = h("div", { class: "builder-inspector-actions" }, [
      h("button", { class: "btn btn-ghost btn-xs", text: "Page", attrs: { type: "button", "data-inspector-action": "page-settings", title: "Edit page settings" } }),
      h("button", { class: "btn btn-ghost btn-xs", text: "Duplicate", attrs: { type: "button", "data-inspector-action": "duplicate" } }),
      h("button", { class: "btn btn-ghost btn-xs", text: component.hidden ? "Show" : "Hide", attrs: { type: "button", "data-inspector-action": "hide" } }),
      h("button", { class: "btn btn-ghost btn-xs text-error", text: "Delete", attrs: { type: "button", "data-inspector-action": "delete" } }),
    ]);
    const header = h("header", { class: "builder-inspector-head" }, [
      h("div", {}, [
        h("p", { class: "builder-eyebrow", text: definition.category || "Component" }),
        h("h2", { class: "builder-inspector-title", text: definition.label || component.type }),
        h("p", { class: "builder-muted", text: component.id }),
      ]),
      actions,
    ]);
    const children = [header];

    const binding = renderBindingSection(component, definition);
    if (binding) children.push(binding);

    const groups = new Map();
    for (const property of definition.properties || []) {
      const group = property.group || "Properties";
      if (!groups.has(group)) groups.set(group, []);
      groups.get(group).push(property);
    }
    for (const [group, properties] of groups) {
      const section = h("section", { class: "builder-inspector-section" }, [
        h("h3", { class: "builder-inspector-heading", text: group }),
      ]);
      for (const property of properties) section.append(renderPropertyRow(component, definition, property));
      children.push(section);
    }

    if (state.breakpoint !== "desktop") {
      children.push(
        h("p", { class: "builder-inspector-note", text: `Editing ${state.breakpoint} overrides. Unoverridden properties inherit desktop values.` }),
      );
    }

    refs.inspector.replaceChildren(...children);
    refs.inspector.scrollTop = scrollTop;
    if (options.focusId) {
      const focus = document.getElementById(options.focusId);
      if (focus && refs.inspector.contains(focus)) focus.focus({ preventScroll: true });
    }
  }

  // --------------------------------------------------------------------------
  // Sections panel

  function renderSections() {
    if (state.sectionsError) {
      refs.sections.replaceChildren(
        h("p", { class: "builder-muted", text: state.sectionsError }),
        h("button", {
          class: "btn btn-outline btn-xs mt-2",
          text: "Retry",
          attrs: { type: "button", "data-action": "reload-sections" },
        }),
      );
      return;
    }
    if (state.sections.length === 0) {
      refs.sections.replaceChildren(h("p", { class: "builder-muted", text: "No saved sections yet. Select a component and save it here to reuse it later." }));
      return;
    }
    const list = h("ul", { class: "builder-section-list" });
    for (const section of state.sections) {
      const label = section.component ? definitionFor(section.component.type)?.label || section.component.type : "Component";
      const item = h("li", { class: "builder-section-item" }, [
        h("div", { class: "builder-section-copy" }, [
          h("strong", { text: section.name }),
          h("small", { text: label }),
        ]),
        h("div", { class: "builder-section-actions" }, [
          h("button", {
            class: "btn btn-outline btn-xs",
            text: "Insert",
            attrs: { type: "button", "data-section-insert": section.id, "aria-label": `Insert ${section.name}` },
          }),
          h("button", {
            class: "btn btn-ghost btn-xs text-error",
            text: "Delete",
            attrs: { type: "button", "data-section-delete": section.id, "aria-label": `Delete ${section.name}` },
          }),
        ]),
      ]);
      list.append(item);
    }
    refs.sections.replaceChildren(list);
  }

  // --------------------------------------------------------------------------
  // Mutations, selection, and commit

  function recordAndMutate(key, mutate) {
    const before = documentJSON();
    const outcome = mutate();
    if (outcome === false || documentJSON() === before) return false;
    // Record the pre-mutation document only after the mutation succeeds, so a
    // denied or no-op action never leaves an empty step in the undo stack.
    undoHistory.record(JSON.parse(before), key);
    return true;
  }

  function refreshDiagnostics() {
    const errors = validateDocument(state.document, state.definitions, { dataSources: state.dataSources });
    state.diagnostics = errors;
    return errors;
  }

  function updateHistoryControls() {
    refs.undo.disabled = !undoHistory.canUndo();
    refs.redo.disabled = !undoHistory.canRedo();
  }

  function updatePageMeta() {
    const meta = refs.inspector.querySelector("[data-page-meta]");
    if (meta) meta.textContent = `${bootstrap.page.slug ? `/${bootstrap.page.slug}` : ""} · ${state.status} · revision ${state.revision}`;
  }

  function commit(options = {}) {
    if (state.selectedId && !findNode(state.document.components, state.selectedId)) state.selectedId = "";
    refreshDiagnostics();
    renderCanvas();
    renderOutline();
    if (!options.skipInspector) renderInspector();
    updateHistoryControls();
    updatePageMeta();
    updateToolbarTitle();
    markDirty();
    if (options.announce) announce(options.announce);
  }

  function selectNode(id, options = {}) {
    const exists = id ? findNode(state.document.components, id) : null;
    state.selectedId = exists ? id : "";
    state.bindingDrafts.delete(state.selectedId);
    renderCanvas();
    renderOutline();
    renderInspector();
    if (options.focus === "canvas" && state.selectedId) {
      const node = refs.canvas.querySelector(`[data-node-id="${cssEscape(state.selectedId)}"]`);
      node?.focus({ preventScroll: false });
    } else if (options.focus === "outline" && state.selectedId) {
      refs.outline.querySelector(`[data-outline-select="${cssEscape(state.selectedId)}"]`)?.focus({ preventScroll: false });
    }
    if (state.selectedId) {
      const definition = definitionFor(selectedNode()?.component.type);
      announce(`${definition?.label || "Component"} selected.`);
    } else {
      announce("Selection cleared.");
    }
  }

  function updateToolbarTitle() {
    refs.toolbarTitle.textContent = state.title || "Untitled page";
  }

  function targetForInsertion() {
    const location = selectedNode();
    if (!location) return { parentId: null, index: null };
    const definition = definitionFor(location.component.type);
    if (definition?.allowChildren) return { parentId: location.component.id, index: null };
    return { parentId: location.parent?.id || null, index: location.index + 1 };
  }

  function insertComponent(component, target, options = {}) {
    const parentId = target.parentId || null;
    if (parentId) {
      const parent = findNode(state.document.components, parentId);
      if (!parent) return false;
      if (!canContain(definitionFor(parent.component.type), component.type)) {
        announce(`${definitionFor(component.type)?.label || component.type} cannot go inside ${definitionFor(parent.component.type)?.label || parent.component.type}.`);
        return false;
      }
    }
    const key = structuralKey("insert", component.id);
    const inserted = recordAndMutate(key, () =>
      insertNode(state.document, component, parentId, target.index ?? null, state.definitions),
    );
    if (!inserted) {
      announce("That move is not allowed.");
      return false;
    }
    state.selectedId = component.id;
    commit({
      announce: `${definitionFor(component.type)?.label || component.type} added.`,
    });
    if (options.focusCanvas) {
      refs.canvas.querySelector(`[data-node-id="${cssEscape(component.id)}"]`)?.focus({ preventScroll: false });
    }
    return true;
  }

  function addLibraryComponent(type) {
    const definition = definitionFor(type);
    if (!definition) return;
    const component = newComponent(definition);
    insertComponent(component, targetForInsertion(), { focusCanvas: true });
  }

  function addPreset(presetId) {
    const preset = state.presets.find((candidate) => candidate.id === presetId);
    if (!preset?.component) return;
    const component = remapIds(preset.component);
    insertComponent(component, targetForInsertion(), { focusCanvas: true });
  }

  function addSection(sectionId) {
    const section = state.sections.find((candidate) => candidate.id === sectionId);
    if (!section?.component) return;
    const component = remapIds(section.component);
    insertComponent(component, targetForInsertion(), { focusCanvas: true });
  }

  // --------------------------------------------------------------------------
  // Inspector events

  function setPageTitle(value) {
    state.title = value;
    state.values["page.title"] = value;
    state.success = null;
    updateToolbarTitle();
    updatePageMeta();
    renderCanvas();
    refreshDirtyStatus();
    refreshBanner();
    scheduleSave();
  }

  function applyTitleControl(control) {
    setPageTitle(control.value);
  }

  function currentArray(component, property) {
    const props = resolveProps(component, state.breakpoint);
    return Array.isArray(props[property.key]) ? clone(props[property.key]) : [];
  }

  function mutateArray(component, property, mutator, key) {
    const next = currentArray(component, property);
    mutator(next);
    const breakpoint = property.responsive ? state.breakpoint : "desktop";
    const changed = recordAndMutate(key, () => updateProp(component, property.key, next, breakpoint));
    if (!changed) return false;
    commit({ skipInspector: true });
    renderInspector();
    return true;
  }

  function applyPropertyControl(control, options = {}) {
    const location = selectedNode();
    if (!location) return false;
    const component = location.component;
    const definition = definitionFor(component.type);
    const arrayKey = control.dataset.arrayKey || "";
    const itemKey = control.dataset.propKey || "";
    let property;
    let itemProperty;
    if (arrayKey) {
      property = definition?.properties.find((candidate) => candidate.key === arrayKey);
      itemProperty = property?.itemProperties?.find((candidate) => candidate.key === itemKey);
      if (!property || !itemProperty) return false;
    } else {
      property = definition?.properties.find((candidate) => candidate.key === itemKey);
      if (!property) return false;
    }
    const value = readControlValue(control, itemProperty || property);
    if (value === null) return false;

    if (arrayKey) {
      const index = Number(control.dataset.arrayIndex);
      const next = currentArray(component, property);
      if (!next[index] || typeof next[index] !== "object") return false;
      next[index][itemKey] = value;
      const breakpoint = property.responsive ? state.breakpoint : "desktop";
      const key = `prop:${component.id}:${breakpoint}:${arrayKey}:${index}:${itemKey}`;
      const changed = recordAndMutate(key, () => updateProp(component, arrayKey, next, breakpoint));
      if (!changed) return false;
      commit({ skipInspector: true });
      return true;
    }

    const breakpoint = property.responsive ? state.breakpoint : "desktop";
    const key = `prop:${component.id}:${breakpoint}:${property.key}`;
    const changed = recordAndMutate(key, () => updateProp(component, property.key, value, breakpoint));
    if (!changed) return false;
    commit({ skipInspector: true });
    return true;
  }

  function readControlValue(control, property) {
    if (property.kind === "boolean") return Boolean(control.checked);
    if (property.kind === "number") {
      if (control.value.trim() === "") return null;
      const parsed = Number(control.value);
      return Number.isFinite(parsed) ? parsed : null;
    }
    return control.value;
  }

  function populateBindingFields(component) {
    const fieldSelect = refs.inspector.querySelector("[data-binding-field]");
    if (!fieldSelect) return;
    const sourceId = state.bindingDrafts.has(component.id)
      ? state.bindingDrafts.get(component.id)
      : component.binding?.source || "";
    const source = state.dataSources.find((candidate) => candidate.id === sourceId);
    fieldSelect.replaceChildren(
      h("option", { text: source ? "Choose a field" : "Choose a source first", attrs: { value: "" } }),
      ...(source?.fields || []).map((candidate) => h("option", { text: candidate.label, attrs: { value: candidate.id } })),
    );
    fieldSelect.value = component.binding?.source === sourceId ? component.binding?.field || "" : "";
  }

  function handleBindingChange(control) {
    const location = selectedNode();
    if (!location) return;
    const component = location.component;
    if (control.hasAttribute("data-binding-source")) {
      const source = control.value;
      if (source === "") {
        state.bindingDrafts.delete(component.id);
        if (component.binding) {
          const changed = recordAndMutate(structuralKey("binding", component.id), () => {
            delete component.binding;
            return true;
          });
          if (changed) commit({ skipInspector: true });
        }
      } else {
        state.bindingDrafts.set(component.id, source);
      }
      populateBindingFields(component);
      return;
    }
    if (control.hasAttribute("data-binding-field")) {
      const source = state.bindingDrafts.get(component.id) || component.binding?.source || "";
      const field = control.value;
      const changed = recordAndMutate(structuralKey("binding", component.id), () => {
        if (!source || !field) {
          delete component.binding;
        } else {
          component.binding = { source, field };
        }
        return true;
      });
      state.bindingDrafts.delete(component.id);
      if (changed) commit({ skipInspector: false });
      return;
    }
  }

  function defaultItemValue(properties) {
    const item = {};
    for (const property of properties || []) {
      if (property.default !== undefined) item[property.key] = clone(property.default);
      else if (property.kind === "boolean") item[property.key] = false;
      else if (property.kind === "enum" && property.options?.length) item[property.key] = property.options[0].value;
      else if (property.kind === "number") item[property.key] = property.min ?? 0;
      else item[property.key] = "";
    }
    return item;
  }

  function handleInspectorClick(event) {
    const control = event.target.closest("[data-binding-source], [data-binding-field]");
    if (control) return;
    const action = event.target.closest("[data-inspector-action]")?.dataset.inspectorAction;
    if (action) {
      event.preventDefault();
      const location = selectedNode();
      if (action === "page-settings") {
        selectNode("");
        return;
      }
      if (!location) return;
      const id = location.component.id;
      if (action === "duplicate") {
        const key = structuralKey("duplicate", id);
        const duplicate = (() => {
          let created = null;
          recordAndMutate(key, () => {
            created = duplicateNode(state.document, id);
            return Boolean(created);
          });
          return created;
        })();
        if (duplicate) {
          state.selectedId = duplicate.id;
          commit({ announce: "Component duplicated." });
        }
      } else if (action === "hide") {
        const hide = location.component.hidden !== true;
        recordAndMutate(structuralKey("hide", id), () => setHidden(state.document, id, hide));
        commit({ announce: hide ? "Component hidden." : "Component shown." });
      } else if (action === "delete") {
        deleteComponent(id);
      }
      return;
    }

    const reset = event.target.closest("[data-reset-prop]");
    if (reset) {
      event.preventDefault();
      const location = selectedNode();
      const propertyKey = reset.dataset.resetProp;
      if (!location || !propertyKey) return;
      const changed = recordAndMutate(structuralKey("reset", location.component.id), () =>
        resetResponsive(location.component, propertyKey, state.breakpoint),
      );
      if (changed) commit({ announce: "Override reset." });
      return;
    }

    const add = event.target.closest("[data-array-add]");
    if (add) {
      event.preventDefault();
      const location = selectedNode();
      if (!location) return;
      const property = definitionFor(location.component.type)?.properties.find((candidate) => candidate.key === add.dataset.arrayAdd);
      if (!property) return;
      mutateArray(
        location.component,
        property,
        (items) => items.push(defaultItemValue(property.itemProperties)),
        structuralKey("array-add", location.component.id),
      );
      return;
    }

    const remove = event.target.closest("[data-array-remove]");
    if (remove) {
      event.preventDefault();
      const location = selectedNode();
      if (!location) return;
      const property = definitionFor(location.component.type)?.properties.find((candidate) => candidate.key === remove.dataset.arrayRemove);
      if (!property) return;
      const index = Number(remove.dataset.arrayIndex);
      mutateArray(
        location.component,
        property,
        (items) => items.splice(index, 1),
        structuralKey("array-remove", location.component.id),
      );
    }
  }

  function handleInspectorInput(event) {
    if (event.target.matches("[data-page-title]")) {
      applyTitleControl(event.target);
      return;
    }
    const control = event.target.closest("[data-prop-key]");
    if (!control) return;
    const kind = control.dataset.propKind;
    if (IMMEDIATE_KINDS.has(kind) || DEFERRED_KINDS.has(kind)) return;
    applyPropertyControl(control);
  }

  function handleInspectorChange(event) {
    if (event.target.matches("[data-page-title]")) {
      applyTitleControl(event.target);
      return;
    }
    const binding = event.target.closest("[data-binding-source], [data-binding-field]");
    if (binding) {
      handleBindingChange(binding);
      return;
    }
    const control = event.target.closest("[data-prop-key]");
    if (!control) return;
    const kind = control.dataset.propKind;
    const id = control.id;
    applyPropertyControl(control);
    renderInspector({ focusId: IMMEDIATE_KINDS.has(kind) ? id : "" });
  }

  // --------------------------------------------------------------------------
  // Outline and section events

  function deleteComponent(id) {
    const location = findNode(state.document.components, id);
    const fallback = location?.parent?.id || null;
    const changed = recordAndMutate(structuralKey("delete", id), () => Boolean(removeNode(state.document, id)));
    if (!changed) return;
    if (state.selectedId === id) state.selectedId = fallback || "";
    commit({ announce: "Component deleted." });
  }

  function handleOutlineClick(event) {
    const select = event.target.closest("[data-outline-select]");
    if (select) {
      selectNode(select.dataset.outlineSelect, { focus: "outline" });
      return;
    }
    const button = event.target.closest("[data-outline-action]");
    if (!button) return;
    event.preventDefault();
    const action = button.dataset.outlineAction;
    const id = button.dataset.nodeId;
    const location = findNode(state.document.components, id);
    if (!location) return;
    const component = location.component;
    let key = structuralKey(action, id);
    let message = "";

    if (action === "up") {
      recordAndMutate(key, () => shiftSibling(state.document, id, -1));
      message = "Moved up.";
    } else if (action === "down") {
      recordAndMutate(key, () => shiftSibling(state.document, id, 1));
      message = "Moved down.";
    } else if (action === "indent") {
      recordAndMutate(key, () => indentNode(state.document, id, state.definitions));
      message = "Nested in the previous container.";
    } else if (action === "outdent") {
      recordAndMutate(key, () => outdentNode(state.document, id, state.definitions));
      message = "Moved out of its container.";
    } else if (action === "duplicate") {
      let duplicate = null;
      recordAndMutate(key, () => {
        duplicate = duplicateNode(state.document, id);
        return Boolean(duplicate);
      });
      if (duplicate) {
        state.selectedId = duplicate.id;
        message = "Component duplicated.";
      }
    } else if (action === "hide") {
      recordAndMutate(key, () => setHidden(state.document, id, component.hidden !== true));
      message = component.hidden ? "Component shown." : "Component hidden.";
    } else if (action === "delete") {
      deleteComponent(id);
      return;
    }
    commit({ announce: message });
    if (state.selectedId) {
      refs.outline.querySelector(`[data-outline-select="${cssEscape(state.selectedId)}"]`)?.focus({ preventScroll: true });
    }
  }

  function handleSectionsClick(event) {
    const insert = event.target.closest("[data-section-insert]");
    if (insert) {
      addSection(insert.dataset.sectionInsert);
      return;
    }
    const remove = event.target.closest("[data-section-delete]");
    if (remove) {
      deleteSection(remove.dataset.sectionDelete);
    }
  }

  // --------------------------------------------------------------------------
  // Drag and drop

  function handleDragStart(event) {
    const library = event.target.closest("[data-library-type], [data-preset-id]");
    if (library) {
      if (library.dataset.libraryType) {
        state.dragPayload = { kind: "library", type: library.dataset.libraryType };
      } else {
        state.dragPayload = { kind: "preset", id: library.dataset.presetId };
      }
      event.dataTransfer.effectAllowed = "copy";
      event.dataTransfer.setData("text/plain", "add-component");
      root.classList.add("builder-dragging");
      return;
    }

    const node = event.target.closest(".builder-node");
    if (!node || !refs.canvas.contains(node)) return;
    if (event.target.closest("input, textarea, select, a, button")) {
      event.preventDefault();
      return;
    }
    state.dragPayload = { kind: "move", id: node.dataset.nodeId };
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", node.dataset.nodeId);
    root.classList.add("builder-dragging");
    node.classList.add("is-dragging");
  }

  function handleDragEnd() {
    root.classList.remove("builder-dragging");
    root.querySelectorAll(".is-dragging").forEach((element) => element.classList.remove("is-dragging"));
    clearDropIndicators();
    state.dragPayload = null;
  }

  function handleDragOver(event) {
    if (!state.dragPayload) return;
    const zone = event.target.closest(".builder-dropzone");
    if (zone && refs.canvas.contains(zone)) {
      event.preventDefault();
      event.dataTransfer.dropEffect = state.dragPayload.kind === "move" ? "move" : "copy";
      setDropTarget({
        parentId: zone.dataset.dropParent || null,
        index: Number(zone.dataset.dropIndex),
        element: zone,
      });
      zone.classList.add("is-over");
      return;
    }

    const node = event.target.closest(".builder-node");
    if (node && refs.canvas.contains(node)) {
      event.preventDefault();
      event.dataTransfer.dropEffect = state.dragPayload.kind === "move" ? "move" : "copy";
      const definition = definitionFor(node.dataset.nodeType);
      const rect = node.getBoundingClientRect();
      const ratio = rect.height > 0 ? (event.clientY - rect.top) / rect.height : 0.5;
      const location = findNode(state.document.components, node.dataset.nodeId);
      if (!location) return;

      if (definition?.allowChildren && ratio > 0.25 && ratio < 0.75) {
        setDropTarget({ parentId: node.dataset.nodeId, index: null, element: node, inside: true });
        node.classList.add("is-drop-inside");
        return;
      }
      const after = ratio >= 0.5;
      setDropTarget({
        parentId: location.parent?.id || null,
        index: location.index + (after ? 1 : 0),
        element: node,
        after,
      });
      return;
    }

    if (refs.canvas.contains(event.target)) {
      event.preventDefault();
      event.dataTransfer.dropEffect = state.dragPayload.kind === "move" ? "move" : "copy";
      setDropTarget({ parentId: null, index: null, element: refs.canvas, root: true });
      refs.canvas.classList.add("is-drop-root");
    }
  }

  function handleDrop(event) {
    if (!state.dragPayload) return;
    event.preventDefault();
    const payload = state.dragPayload;
    const target = state.dropTarget;
    handleDragEnd();
    if (!target) return;
    performDrop(payload, target);
  }

  function performDrop(payload, target) {
    if (payload.kind === "move") {
      const location = findNode(state.document.components, payload.id);
      if (!location) return;
      if (target.parentId === payload.id || isDescendant(location.component, target.parentId)) {
        announce("A component cannot be moved inside itself.");
        return;
      }
      let index = target.index;
      if ((location.parent?.id || null) === (target.parentId || null) && index !== null && location.index < index) {
        index -= 1;
      }
      const key = structuralKey("move", payload.id);
      const changed = recordAndMutate(key, () =>
        moveNode(state.document, payload.id, target.parentId || null, index, state.definitions),
      );
      if (!changed) {
        announce("That move is not allowed.");
        return;
      }
      state.selectedId = payload.id;
      commit({ announce: "Component moved." });
      refs.canvas.querySelector(`[data-node-id="${cssEscape(payload.id)}"]`)?.focus({ preventScroll: true });
      return;
    }

    let component = null;
    if (payload.kind === "library") {
      const definition = definitionFor(payload.type);
      if (!definition) return;
      component = newComponent(definition);
    } else if (payload.kind === "preset") {
      const preset = state.presets.find((candidate) => candidate.id === payload.id);
      if (!preset?.component) return;
      component = remapIds(preset.component);
    }
    if (component) insertComponent(component, target, { focusCanvas: true });
  }

  // --------------------------------------------------------------------------
  // History actions and keyboard

  function undo() {
    const snapshot = undoHistory.undo(state.document);
    if (!snapshot) return;
    state.document = snapshot;
    commit({ announce: "Undo." });
  }

  function redo() {
    const snapshot = undoHistory.redo(state.document);
    if (!snapshot) return;
    state.document = snapshot;
    commit({ announce: "Redo." });
  }

  function duplicateSelected() {
    const location = selectedNode();
    if (!location) return;
    let duplicate = null;
    recordAndMutate(structuralKey("duplicate", location.component.id), () => {
      duplicate = duplicateNode(state.document, location.component.id);
      return Boolean(duplicate);
    });
    if (!duplicate) return;
    state.selectedId = duplicate.id;
    commit({ announce: "Component duplicated." });
  }

  function moveSelected(delta) {
    const location = selectedNode();
    if (!location) return;
    const changed = recordAndMutate(structuralKey("shift", location.component.id), () =>
      shiftSibling(state.document, location.component.id, delta),
    );
    if (changed) commit({ announce: delta < 0 ? "Moved up." : "Moved down." });
  }

  function handleGlobalKeydown(event) {
    const target = event.target;
    const typing = target.closest?.("input, textarea, select, [contenteditable='true']");
    const modifier = event.metaKey || event.ctrlKey;
    const key = event.key.toLowerCase();

    if (modifier && key === "s") {
      event.preventDefault();
      void saveNow();
      return;
    }
    // A modal owns its own keyboard interaction. Without this guard, Delete,
    // undo, or reordering could mutate the page behind a publish, history, or
    // conflict dialog.
    if (document.querySelector("dialog[data-dialog][open]")) return;
    if (!typing && modifier && key === "z") {
      event.preventDefault();
      if (event.shiftKey) redo();
      else undo();
      return;
    }
    if (!typing && modifier && key === "y") {
      event.preventDefault();
      redo();
      return;
    }
    if (!typing && modifier && key === "d") {
      event.preventDefault();
      duplicateSelected();
      return;
    }
    if (!typing && !modifier && !target.closest?.("button, a") && (event.key === "Delete" || event.key === "Backspace")) {
      if (state.selectedId) {
        event.preventDefault();
        deleteComponent(state.selectedId);
      }
      return;
    }
    if (!typing && state.selectedId && event.altKey) {
      if (event.key === "ArrowUp") {
        event.preventDefault();
        moveSelected(-1);
      } else if (event.key === "ArrowDown") {
        event.preventDefault();
        moveSelected(1);
      } else if (event.key === "ArrowRight") {
        event.preventDefault();
        const changed = recordAndMutate(structuralKey("indent", state.selectedId), () =>
          indentNode(state.document, state.selectedId, state.definitions),
        );
        if (changed) commit({ announce: "Component nested." });
      } else if (event.key === "ArrowLeft") {
        event.preventDefault();
        const changed = recordAndMutate(structuralKey("outdent", state.selectedId), () =>
          outdentNode(state.document, state.selectedId, state.definitions),
        );
        if (changed) commit({ announce: "Component moved out." });
      }
      return;
    }
    if (event.key === "Escape" && !typing && state.selectedId) {
      selectNode("");
    }
  }

  // --------------------------------------------------------------------------
  // Saving, status, and the banner

  function setSaveStatus(kind, label) {
    refs.saveStatus.dataset.state = kind;
    refs.saveLabel.textContent = label;
    refs.retrySave.hidden = kind !== "error" && kind !== "conflict";
  }

  function refreshDirtyStatus() {
    state.dirty = isDirty();
    if (state.conflict) {
      setSaveStatus("conflict", "Conflict");
    } else if (saveCoordinator.isSaving) {
      setSaveStatus("saving", "Saving…");
    } else if (state.saveError) {
      setSaveStatus("error", "Not saved");
    } else if (state.dirty) {
      setSaveStatus("dirty", "Unsaved changes");
    } else {
      setSaveStatus("saved", "Saved");
    }
  }

  function markDirty() {
    state.dirty = isDirty();
    if (state.dirty) state.success = null;
    refreshDirtyStatus();
    refreshBanner();
    scheduleSave();
  }

  function scheduleSave(delay = 800) {
    saveCoordinator.cancel();
    state.dirty = isDirty();
    if (!state.dirty || state.conflict) return;
    setSaveStatus("dirty", "Unsaved changes");
    saveCoordinator.schedule(() => {
      void saveNow();
    }, delay);
  }

  // Cancel the pending autosave and wait for any in-flight save before an
  // operation that replaces the draft. Otherwise a debounced save from the
  // previous document could race a restore or reload and create a conflict.
  async function quiesceSaves() {
    saveCoordinator.cancel();
    await saveCoordinator.wait();
    // Let the completed save wrapper run its own follow-up microtasks (which
    // may schedule a save for edits made during the request), then cancel that
    // schedule. The replacement operation reads the newest revision after this
    // point and must not race a queued autosave from the old document.
    await Promise.resolve();
    saveCoordinator.cancel();
  }

  async function saveNow(options = {}) {
    saveCoordinator.cancel();
    if (saveCoordinator.isSaving) {
      await saveCoordinator.wait();
      return saveNow(options);
    }
    if (state.conflict && !options.ignoreConflict) {
      refreshDirtyStatus();
      refreshBanner();
      return false;
    }
    if (!isDirty() && !options.force) {
      refreshDirtyStatus();
      return !state.saveError;
    }

    const snapshot = documentJSON();
    const title = state.title.trim();
    const revision = state.revision;
    state.saveError = "";
    setSaveStatus("saving", "Saving…");
    refreshBanner();

    const ok = await saveCoordinator.run((generation) =>
      performSave({ generation, snapshot, title, revision }),
    );
    refreshDirtyStatus();
    refreshBanner();
    if (ok && isDirty() && !state.conflict) scheduleSave(0);
    return ok;
  }

  async function performSave({ generation, snapshot, title, revision }) {
    try {
      const response = await fetch(state.endpoints.save, {
        method: "PUT",
        credentials: "same-origin",
        cache: "no-store",
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json",
          "If-Match": `"${revision}"`,
        },
        body: JSON.stringify({ title, document: JSON.parse(snapshot), expectedRevision: revision }),
      });
      const payload = await readJSONResponse(response);
      if (!saveCoordinator.isCurrent(generation)) return false;

      if (response.status === 409) {
        state.conflict = true;
        state.conflictCurrent = Number(payload?.currentRevision) || 0;
        state.saveError = "";
        openConflict();
        refreshDirtyStatus();
        refreshBanner();
        return false;
      }
      if (!response.ok) {
        if (Array.isArray(payload?.errors) && payload.errors.length > 0) state.diagnostics = payload.errors;
        state.saveError = problemMessage(payload, response);
        refreshBanner();
        return false;
      }
      if (!response.ok || !payload || !Number.isFinite(Number(payload.revision))) {
        throw new Error("The server returned an incomplete save response.");
      }

      state.revision = Number(payload.revision);
      state.status = payload.status || "draft";
      state.savedDocument = snapshot;
      state.savedTitle = title;
      state.saveError = "";
      state.conflict = false;
      state.conflictCurrent = 0;
      refreshDiagnostics();
      updatePageMeta();
      return true;
    } catch (error) {
      console.error("Save failed", error);
      if (saveCoordinator.isCurrent(generation)) {
        state.saveError = "We could not reach the server. Your work is still in this tab.";
      }
      return false;
    }
  }

  async function readJSONResponse(response) {
    try {
      return await response.json();
    } catch {
      return null;
    }
  }

  function problemMessage(payload, response) {
    if (payload?.message) return payload.message;
    if (response?.status === 401) return "Your session expired. Sign in again to save.";
    if (response?.status === 403) return "You do not have permission to save this page.";
    return "The page could not be saved. Try again.";
  }

  function bannerButton(label, action, style = "btn-outline") {
    return h("button", {
      class: `btn ${style} btn-sm`,
      text: label,
      attrs: { type: "button", "data-banner-action": action },
    });
  }

  function refreshBanner() {
    let title = "";
    let message = "";
    let actions = [];
    let tone = "";

    if (state.conflict) {
      tone = "error";
      title = "This page changed in another session";
      message = "Resolve the revision conflict before saving more changes.";
      actions = [bannerButton("Review conflict", "open-conflict", "btn-error")];
    } else if (state.saveError) {
      tone = "error";
      title = "Changes are not saved";
      message = state.saveError;
      actions = [bannerButton("Retry save", "retry-save", "btn-primary")];
      if (state.diagnostics.length > 0) actions.push(bannerButton("Review issues", "review-diagnostics"));
    } else if (state.diagnostics.length > 0) {
      tone = "warning";
      title = "Page needs attention";
      message = `${state.diagnostics.length} validation ${state.diagnostics.length === 1 ? "issue" : "issues"} must be fixed before publishing.`;
      actions = [bannerButton("Review issues", "review-diagnostics")];
    } else if (state.success) {
      tone = "success";
      title = state.success.title;
      message = state.success.message;
      if (state.success.url) {
        actions.push(
          h("a", {
            class: "btn btn-primary btn-sm",
            text: state.success.label || "View page",
            attrs: { href: state.success.url, target: "_blank", rel: "noopener" },
          }),
        );
      }
      actions.push(bannerButton("Dismiss", "dismiss-banner", "btn-ghost"));
    }

    if (!title) {
      refs.banner.hidden = true;
      refs.bannerActions.replaceChildren();
      refs.banner.dataset.tone = "";
      return;
    }
    refs.banner.hidden = false;
    refs.banner.dataset.tone = tone;
    refs.bannerTitle.textContent = title;
    refs.bannerMessage.textContent = message;
    refs.bannerActions.replaceChildren(...actions);
  }

  // --------------------------------------------------------------------------
  // Diagnostics and publish read model

  function walkWithPaths(components, parentPath, visitor) {
    components.forEach((component, index) => {
      const path = `${parentPath}[${index}]`;
      visitor(component, path);
      if (Array.isArray(component.children) && component.children.length > 0) {
        walkWithPaths(component.children, `${path}.children`, visitor);
      }
    });
  }

  function emptyPropertyValue(property, value) {
    if (value === undefined || value === null) return true;
    if (typeof value === "string") return value.trim() === "";
    if (Array.isArray(value)) return value.length === 0;
    return property.kind === "array" && value.length === 0;
  }

  function publishIssues() {
    const issues = refreshDiagnostics();
    if (state.title.trim() === "") {
      issues.push({ path: "title", message: "Page title is required before publishing." });
    }
    walkWithPaths(state.document.components, "components", (component, path) => {
      const definition = definitionFor(component.type);
      if (!definition) return;
      for (const property of definition.properties || []) {
        const value = component.props?.[property.key];
        if (property.required && emptyPropertyValue(property, value)) {
          issues.push({
            path: `${path}.props.${property.key}`,
            message: `${property.label} is required before publishing.`,
            componentId: component.id,
          });
        }
        if (property.kind === "array") {
          const items = Array.isArray(value) ? value : [];
          if (property.minItems > 0 && items.length < property.minItems) {
            issues.push({
              path: `${path}.props.${property.key}`,
              message: `${property.label} needs at least ${property.minItems} item(s).`,
              componentId: component.id,
            });
          }
          items.forEach((item, index) => {
            for (const itemProperty of property.itemProperties || []) {
              if (itemProperty.required && emptyPropertyValue(itemProperty, item?.[itemProperty.key])) {
                issues.push({
                  path: `${path}.props.${property.key}[${index}].${itemProperty.key}`,
                  message: `${itemProperty.label} is required before publishing.`,
                  componentId: component.id,
                });
              }
            }
          });
        }
      }
      if (definition.bindingRequired && !component.binding) {
        issues.push({
          path: `${path}.binding`,
          message: `${definition.label} needs an approved data binding before publishing.`,
          componentId: component.id,
        });
      }
    });
    return issues;
  }

  function problemList(errors, options = {}) {
    if (!errors || errors.length === 0) {
      return h("p", { class: "builder-muted", text: "No validation issues." });
    }
    const list = h("ul", { class: "builder-problem-list" });
    for (const error of errors) {
      const row = h("li", { class: "builder-problem" }, [
        h("code", { text: error.path || "document" }),
        h("span", { text: error.message || "Invalid value" }),
      ]);
      if (error.componentId && options.selectable) {
        row.append(h("button", {
          class: "btn btn-ghost btn-xs",
          text: "Select",
          attrs: { type: "button", "data-error-component": error.componentId },
        }));
      }
      list.append(row);
    }
    return list;
  }

  function openDiagnostics(errors = state.diagnostics) {
    refs.diagnosticsBody.replaceChildren(problemList(errors, { selectable: true }));
    openDialog("diagnostics");
  }

  async function openPreview() {
    // Open the window synchronously so the click remains a user gesture, but
    // never navigate it to a stale draft when the latest save failed.
    const preview = window.open("about:blank", "builder-preview");
    if (state.conflict) {
      preview?.close();
      openConflict();
      return;
    }
    if (saveCoordinator.isSaving || isDirty()) {
      const saved = await saveNow();
      if (!saved) {
        preview?.close();
        refreshBanner();
        return;
      }
    }
    if (preview) {
      preview.location.href = state.endpoints.preview;
    } else {
      window.open(state.endpoints.preview, "_blank", "noopener");
    }
  }

  function openPublishDialog() {
    const issues = publishIssues();
    refs.publishTitle.textContent = issues.length > 0 ? "Page needs attention" : "Publish page";
    refs.publishBody.replaceChildren();
    if (issues.length > 0) {
      refs.publishBody.append(
        h("p", { class: "text-sm leading-6", text: "Fix these issues before making the page public." }),
        problemList(issues, { selectable: true }),
      );
    } else {
      refs.publishBody.append(
        h("p", {
          class: "text-sm leading-6",
          text: `Revision ${state.revision} will become the public version of /pages/${bootstrap.page.slug}.`,
        }),
      );
      if (isDirty()) {
        refs.publishBody.append(
          h("p", { class: "builder-muted mt-2", text: "Your latest changes are saved automatically before publishing." }),
        );
      }
      refs.publishBody.append(
        h("div", { class: "builder-dialog-actions" }, [
          h("button", {
            class: "btn btn-primary btn-sm",
            text: "Publish now",
            attrs: { type: "button", "data-action": "confirm-publish" },
          }),
        ]),
      );
    }
    openDialog("publish");
  }

  async function confirmPublish(button) {
    if (button) button.disabled = true;
    const saved = await saveNow();
    if (!saved || state.conflict) {
      if (!state.conflict) {
        refs.publishBody.querySelector("[data-publish-error]")?.remove();
        refs.publishBody.prepend(
          h("p", {
            class: "text-sm text-error",
            text: state.saveError || "Your latest changes could not be saved. Try again.",
            attrs: { "data-publish-error": "true", role: "alert" },
          }),
        );
      }
      if (button) button.disabled = false;
      return;
    }
    try {
      const response = await fetch(state.endpoints.publish, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json",
          "If-Match": `"${state.revision}"`,
        },
        body: JSON.stringify({ expectedRevision: state.revision }),
      });
      const payload = await readJSONResponse(response);
      if (response.status === 409) {
        state.conflict = true;
        state.conflictCurrent = Number(payload?.currentRevision) || 0;
        openConflict();
        if (button) button.disabled = false;
        refreshDirtyStatus();
        refreshBanner();
        return;
      }
      if (!response.ok) {
        const errors = Array.isArray(payload?.errors) ? payload.errors : [];
        refs.publishBody.replaceChildren(
          h("p", { class: "text-sm text-error", text: problemMessage(payload, response) }),
          problemList(errors, { selectable: true }),
        );
        if (button) button.disabled = false;
        return;
      }
      state.revision = Number(payload?.revision) || state.revision;
      state.status = payload?.status || "published";
      state.savedDocument = documentJSON();
      state.savedTitle = state.title.trim();
      state.success = {
        title: "Page published",
        message: "The latest revision is now live.",
        url: payload?.publicUrl || state.endpoints.public,
        label: "View page",
      };
      closeDialog("publish");
      refreshDirtyStatus();
      updatePageMeta();
      refreshBanner();
      renderInspector();
      announce("Page published.");
    } catch (error) {
      console.error("Publish failed", error);
      refs.publishBody.replaceChildren(
        h("p", { class: "text-sm text-error", text: "We could not reach the server. Try again." }),
      );
      if (button) button.disabled = false;
    }
  }

  // --------------------------------------------------------------------------
  // Dialogs, revisions, and conflict recovery

  function dialog(name) {
    return document.querySelector(`dialog[data-dialog="${name}"]`);
  }

  function openDialog(name) {
    const target = dialog(name);
    if (!target) return;
    document.querySelectorAll("dialog[data-dialog][open]").forEach((other) => {
      if (other !== target) other.close();
    });
    if (!target.open) target.showModal();
  }

  function closeDialog(name) {
    const target = dialog(name);
    if (target?.open) target.close();
  }

  function openConflict() {
    if (refs.conflictRevision) {
      refs.conflictRevision.textContent = state.conflictCurrent > 0
        ? `The server is at revision ${state.conflictCurrent}.`
        : "The server has a newer revision.";
    }
    openDialog("conflict");
  }

  function applyDraftPayload(payload, success) {
    if (!payload?.document || !Array.isArray(payload.document.components) || !payload.page) {
      throw new Error("The server returned an incomplete draft.");
    }
    state.document = payload.document;
    state.title = payload.page.title || "";
    state.values["page.title"] = state.title;
    state.revision = Number(payload.page.revision) || state.revision;
    state.status = payload.page.status || "draft";
    state.savedDocument = JSON.stringify(state.document);
    state.savedTitle = state.title.trim();
    state.conflict = false;
    state.conflictCurrent = 0;
    state.saveError = "";
    state.success = success || null;
    undoHistory.reset();
    refreshDiagnostics();
    updateToolbarTitle();
    updatePageMeta();
    renderCanvas();
    renderOutline();
    renderInspector();
    renderSections();
    updateHistoryControls();
    refreshDirtyStatus();
    refreshBanner();
    announce("Draft reloaded.");
  }

  async function reloadLatest() {
    await quiesceSaves();
    try {
      const response = await fetch(state.endpoints.draft, {
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/json" },
      });
      const payload = await readJSONResponse(response);
      if (!response.ok) throw new Error(problemMessage(payload, response));
      applyDraftPayload(payload, {
        title: "Loaded the latest revision",
        message: "Your local changes in this tab were discarded.",
      });
      closeDialog("conflict");
    } catch (error) {
      console.error("Reload latest failed", error);
      state.saveError = "We could not load the latest revision. Try again.";
      refreshDirtyStatus();
      refreshBanner();
    }
  }

  async function overwriteConflict() {
    if (state.conflictCurrent <= 0) {
      await reloadLatest();
      return;
    }
    state.revision = state.conflictCurrent;
    state.conflict = false;
    state.conflictCurrent = 0;
    closeDialog("conflict");
    const saved = await saveNow({ force: true, ignoreConflict: true });
    if (saved) {
      state.success = {
        title: "Overwrote the newer revision",
        message: "Your document is now the latest saved draft.",
      };
      refreshBanner();
    }
  }

  async function openHistory() {
    refs.revisionsBody.replaceChildren(h("p", { class: "builder-muted", text: "Loading revisions…" }));
    openDialog("revisions");
    try {
      const response = await fetch(`${state.endpoints.revisions}?limit=50`, {
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/json" },
      });
      const payload = await readJSONResponse(response);
      if (!response.ok) throw new Error(problemMessage(payload, response));
      const revisions = Array.isArray(payload?.revisions) ? payload.revisions : [];
      if (revisions.length === 0) {
        refs.revisionsBody.replaceChildren(h("p", { class: "builder-muted", text: "No revisions yet." }));
        return;
      }
      const list = h("ol", { class: "builder-revision-list" });
      for (const revision of revisions) {
        const date = revision.createdAt ? new Date(revision.createdAt) : null;
        const row = h("li", { class: "builder-revision" }, [
          h("div", { class: "builder-revision-copy" }, [
            h("strong", { text: `Revision ${revision.revision}` }),
            h("span", { text: `${revision.status || "draft"} · ${revision.title || ""}` }),
            h("small", {
              text: `${date && !Number.isNaN(date.getTime()) ? date.toLocaleString() : "Unknown date"}${revision.actorId ? ` · ${revision.actorId}` : ""}`,
            }),
          ]),
          h("button", {
            class: "btn btn-outline btn-xs",
            text: revision.revision === state.revision ? "Current" : "Restore",
            attrs: {
              type: "button",
              "data-restore-revision": revision.revision,
              disabled: revision.revision === state.revision ? "disabled" : undefined,
            },
          }),
        ]);
        list.append(row);
      }
      refs.revisionsBody.replaceChildren(list);
    } catch (error) {
      console.error("Revision history failed", error);
      refs.revisionsBody.replaceChildren(
        h("p", { class: "builder-muted", text: "Revision history is unavailable right now." }),
      );
    }
  }

  async function restoreRevision(revision) {
    const warning = isDirty()
      ? "This page has unsaved changes. Restoring a revision will replace them. Continue?"
      : `Restore revision ${revision}?`;
    if (!window.confirm(warning)) return;
    await quiesceSaves();
    if (state.conflict) {
      openConflict();
      return;
    }
    try {
      const response = await fetch(state.endpoints.restore.replace("{revision}", encodeURIComponent(revision)), {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json",
          "If-Match": `"${state.revision}"`,
        },
        body: JSON.stringify({ expectedRevision: state.revision }),
      });
      const payload = await readJSONResponse(response);
      if (response.status === 409) {
        state.conflict = true;
        state.conflictCurrent = Number(payload?.currentRevision) || 0;
        openConflict();
        refreshDirtyStatus();
        refreshBanner();
        return;
      }
      if (!response.ok) {
        refs.revisionsBody.prepend(h("p", { class: "builder-muted text-error", text: problemMessage(payload, response) }));
        if (isDirty() && !state.conflict) scheduleSave();
        return;
      }
      applyDraftPayload(payload, {
        title: `Revision ${revision} restored`,
        message: "The restored document is a new draft revision.",
      });
      closeDialog("revisions");
    } catch (error) {
      console.error("Restore revision failed", error);
      refs.revisionsBody.prepend(h("p", { class: "builder-muted text-error", text: "The revision could not be restored." }));
      if (isDirty() && !state.conflict) scheduleSave();
    }
  }

  // --------------------------------------------------------------------------
  // Sections API

  async function loadSections() {
    if (!state.endpoints.sections) return;
    refs.sections.replaceChildren(h("p", { class: "builder-muted", text: "Loading sections…" }));
    try {
      const response = await fetch(state.endpoints.sections, {
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/json" },
      });
      const payload = await readJSONResponse(response);
      if (!response.ok) throw new Error(problemMessage(payload, response));
      state.sections = Array.isArray(payload?.sections) ? payload.sections : [];
      state.sectionsError = "";
      renderSections();
    } catch (error) {
      console.error("Load sections failed", error);
      state.sectionsError = "Saved sections are unavailable right now.";
      renderSections();
    }
  }

  async function deleteSection(id) {
    if (!window.confirm("Delete this saved section? Existing pages that use it are not affected.")) return;
    try {
      const response = await fetch(`${state.endpoints.sections}/${encodeURIComponent(id)}`, {
        method: "DELETE",
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/json" },
      });
      if (!response.ok) {
        const payload = await readJSONResponse(response);
        throw new Error(problemMessage(payload, response));
      }
      state.sections = state.sections.filter((section) => section.id !== id);
      renderSections();
      announce("Section deleted.");
    } catch (error) {
      console.error("Delete section failed", error);
      state.sectionsError = "The section could not be deleted.";
      renderSections();
    }
  }

  function openSectionDialog() {
    const location = selectedNode();
    if (!location) {
      announce("Select a component before saving a section.");
      return;
    }
    state.sectionTargetId = location.component.id;
    const definition = definitionFor(location.component.type);
    refs.sectionName.value = `${definition?.label || location.component.type} section`;
    refs.sectionPreview.textContent = `Saves the selected ${definition?.label || location.component.type} and its children. IDs are regenerated when it is inserted elsewhere.`;
    openDialog("section");
    refs.sectionName.focus();
    refs.sectionName.select();
  }

  async function submitSection(event) {
    event.preventDefault();
    const location = findNode(state.document.components, state.sectionTargetId);
    if (!location) {
      refs.sectionPreview.textContent = "The selected component no longer exists. Close this dialog and select it again.";
      return;
    }
    const name = refs.sectionName.value.trim();
    if (!name) {
      refs.sectionPreview.textContent = "A section name is required.";
      return;
    }
    try {
      const response = await fetch(state.endpoints.sections, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ name, component: clone(location.component) }),
      });
      const payload = await readJSONResponse(response);
      if (!response.ok) {
        refs.sectionPreview.textContent = problemMessage(payload, response);
        return;
      }
      state.sections = [payload, ...state.sections];
      state.sectionsError = "";
      renderSections();
      closeDialog("section");
      announce("Section saved.");
    } catch (error) {
      console.error("Save section failed", error);
      refs.sectionPreview.textContent = "The section could not be saved. Try again.";
    }
  }

  // --------------------------------------------------------------------------
  // Toolbar actions and breakpoints

  function setBreakpoint(id) {
    const breakpoint = state.breakpoints.find((candidate) => candidate.id === id) || state.breakpoints[0];
    state.breakpoint = breakpoint.id;
    root.querySelectorAll("button[data-breakpoint]").forEach((button) => {
      button.setAttribute("aria-pressed", button.dataset.breakpoint === breakpoint.id ? "true" : "false");
    });
    refs.canvas.dataset.breakpoint = breakpoint.id;
    refs.canvasBreakpointLabel.textContent = breakpoint.label;
    refs.canvasWidthLabel.textContent = `${breakpoint.width} px`;
    renderCanvas();
    renderInspector();
    announce(`${breakpoint.label} breakpoint.`);
  }

  function setPanel(panel) {
    state.activePanel = panel;
    root.querySelectorAll("[data-panel]").forEach((button) => {
      button.setAttribute("aria-selected", button.dataset.panel === panel ? "true" : "false");
    });
    root.querySelectorAll("[data-panel-view]").forEach((view) => {
      view.hidden = view.dataset.panelView !== panel;
    });
    if (panel === "outline") renderOutline();
    if (panel === "sections") renderSections();
  }

  async function handleAction(action, element) {
    const location = selectedNode();
    switch (action) {
      case "undo":
        undo();
        break;
      case "redo":
        redo();
        break;
      case "preview":
        await openPreview();
        break;
      case "history":
        await openHistory();
        break;
      case "publish":
        openPublishDialog();
        break;
      case "save-section":
        openSectionDialog();
        break;
      case "focus-canvas":
        if (state.selectedId) {
          refs.canvas.querySelector(`[data-node-id="${cssEscape(state.selectedId)}"]`)?.focus();
        } else {
          refs.canvas.focus();
        }
        break;
      case "retry-save":
        if (state.conflict) openConflict();
        else await saveNow();
        break;
      case "open-conflict":
        openConflict();
        break;
      case "reload-latest":
        await reloadLatest();
        break;
      case "overwrite-conflict":
        await overwriteConflict();
        break;
      case "confirm-publish":
        await confirmPublish(element);
        break;
      case "review-diagnostics":
        openDiagnostics();
        break;
      case "dismiss-banner":
        state.success = null;
        refreshBanner();
        break;
      case "reload-sections":
        await loadSections();
        break;
      case "restore-revision":
        await restoreRevision(Number(element.dataset.restoreRevision));
        break;
      default:
        if (location) announce(`Unknown action: ${action}`);
    }
  }

  // --------------------------------------------------------------------------
  // Event wiring

  function handleDocumentClick(event) {
    const close = event.target.closest("[data-dialog-close]");
    if (close) {
      close.closest("dialog")?.close();
      return;
    }

    const errorSelect = event.target.closest("[data-error-component]");
    if (errorSelect) {
      document.querySelectorAll("dialog[data-dialog][open]").forEach((open) => open.close());
      selectNode(errorSelect.dataset.errorComponent, { focus: "canvas" });
      return;
    }

    const restore = event.target.closest("[data-restore-revision]");
    if (restore) {
      event.preventDefault();
      void restoreRevision(Number(restore.dataset.restoreRevision));
      return;
    }

    const bannerAction = event.target.closest("[data-banner-action]");
    if (bannerAction) {
      event.preventDefault();
      void handleAction(bannerAction.dataset.bannerAction, bannerAction);
      return;
    }

    const breakpoint = event.target.closest("button[data-breakpoint]");
    if (breakpoint) {
      event.preventDefault();
      setBreakpoint(breakpoint.dataset.breakpoint);
      return;
    }

    const panel = event.target.closest("[data-panel]");
    if (panel) {
      event.preventDefault();
      setPanel(panel.dataset.panel);
      return;
    }

    const action = event.target.closest("[data-action]");
    if (action) {
      if (!root.contains(action) && !action.closest("dialog[data-dialog]")) return;
      event.preventDefault();
      void handleAction(action.dataset.action, action);
    }
  }

  function wireEvents() {
    document.addEventListener("click", handleDocumentClick);
    document.addEventListener("keydown", handleGlobalKeydown);
    refs.librarySearch.addEventListener("input", () => {
      state.search = refs.librarySearch.value;
      renderLibrary();
    });
    refs.library.addEventListener("click", (event) => {
      const item = event.target.closest("[data-library-type], [data-preset-id]");
      if (!item) return;
      if (item.dataset.libraryType) addLibraryComponent(item.dataset.libraryType);
      else addPreset(item.dataset.presetId);
    });
    refs.inspector.addEventListener("click", handleInspectorClick);
    refs.inspector.addEventListener("input", handleInspectorInput);
    refs.inspector.addEventListener("change", handleInspectorChange);
    refs.outline.addEventListener("click", handleOutlineClick);
    refs.sections.addEventListener("click", handleSectionsClick);
    refs.canvas.addEventListener("click", (event) => {
      const node = event.target.closest(".builder-node");
      if (node && refs.canvas.contains(node)) {
        event.preventDefault();
        selectNode(node.dataset.nodeId);
      } else {
        selectNode("");
      }
    });
    refs.canvas.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      const node = event.target.closest(".builder-node");
      if (node && event.target === node) {
        event.preventDefault();
        selectNode(node.dataset.nodeId, { focus: "canvas" });
      }
    });
    root.addEventListener("dragstart", handleDragStart);
    root.addEventListener("dragend", handleDragEnd);
    root.addEventListener("dragover", handleDragOver);
    root.addEventListener("drop", handleDrop);
    refs.sectionForm.addEventListener("submit", submitSection);

    document.querySelectorAll("dialog[data-dialog]").forEach((element) => {
      element.addEventListener("click", (event) => {
        if (event.target === element) element.close();
      });
    });

    document.addEventListener("click", (event) => {
      const guard = event.target.closest("a[data-navigation-guard]");
      if (guard && isDirty()) {
        if (!window.confirm("You have unsaved changes. Leave this page?")) {
          event.preventDefault();
          event.stopPropagation();
        }
      }
    });

    window.addEventListener("beforeunload", (event) => {
      if (isDirty() || saveCoordinator.isSaving) {
        event.preventDefault();
        event.returnValue = "";
      }
    });
    window.addEventListener("online", () => {
      if (state.saveError && !state.conflict && isDirty()) {
        void saveNow();
      }
    });
  }

  // --------------------------------------------------------------------------
  // Start

  wireEvents();
  renderLibrary();
  renderCanvas();
  renderOutline();
  renderInspector();
  renderSections();
  updateHistoryControls();
  updatePageMeta();
  updateToolbarTitle();
  refreshDiagnostics();
  refreshDirtyStatus();
  refreshBanner();
  setBreakpoint("desktop");
  void loadSections();
}
