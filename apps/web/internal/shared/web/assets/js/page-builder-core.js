// Pure document-state operations for the visual page builder.
//
// The editor's canvas, outline, and inspector only mutate the JSON document
// through these functions. They are deliberately free of DOM access so the
// same behavior can be tested under `node --test` and reused by future clients.

export const SCHEMA_VERSION = 2;
export const HISTORY_LIMIT = 100;

export function clone(value) {
  if (typeof structuredClone === "function") return structuredClone(value);
  return JSON.parse(JSON.stringify(value));
}

export function makeId(prefix = "c") {
  if (globalThis.crypto?.randomUUID) return `${prefix}-${crypto.randomUUID()}`;
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export function definitionsMap(definitions) {
  return new Map(definitions.map((definition) => [definition.type, definition]));
}

export function defaultProps(definition) {
  const props = {};
  for (const property of definition.properties || []) {
    if (property.default !== undefined) props[property.key] = clone(property.default);
    if (property.kind === "array") props[property.key] = [];
  }
  return props;
}

export function newComponent(definition, idFactory = makeId) {
  if (!definition) throw new Error("unknown component definition");
  const component = {
    id: idFactory(definition.type),
    type: definition.type,
    props: defaultProps(definition),
  };
  if (definition.allowChildren) component.children = [];
  return component;
}

export function walk(components, visitor, parent = null, depth = 0) {
  for (let index = 0; index < components.length; index += 1) {
    const component = components[index];
    visitor(component, parent, index, depth);
    if (Array.isArray(component.children) && component.children.length > 0) {
      walk(component.children, visitor, component, depth + 1);
    }
  }
}

export function findNode(components, id, parent = null) {
  if (!id) return null;
  for (let index = 0; index < components.length; index += 1) {
    const component = components[index];
    if (component.id === id) {
      return { component, parent, index, siblings: components };
    }
    if (Array.isArray(component.children) && component.children.length > 0) {
      const found = findNode(component.children, id, component);
      if (found) return found;
    }
  }
  return null;
}

export function findPath(components, id, trail = []) {
  for (const component of components) {
    const next = [...trail, component.id];
    if (component.id === id) return next;
    if (Array.isArray(component.children)) {
      const found = findPath(component.children, id, next);
      if (found) return found;
    }
  }
  return null;
}

export function isDescendant(component, id) {
  if (!component || !Array.isArray(component.children)) return false;
  for (const child of component.children) {
    if (child.id === id || isDescendant(child, id)) return true;
  }
  return false;
}

export function canContain(definition, childType) {
  if (!definition || !definition.allowChildren) return false;
  if (!definition.allowedChildren || definition.allowedChildren.length === 0) return true;
  return definition.allowedChildren.includes(childType);
}

function parentList(document, parentId) {
  if (!parentId) return document.components;
  const found = findNode(document.components, parentId);
  if (!found || found.component.type !== "container") return null;
  if (!Array.isArray(found.component.children)) found.component.children = [];
  return found.component.children;
}

export function insertNode(document, component, parentId = null, index = null, definitions = null) {
  const list = parentList(document, parentId);
  if (!list) return false;
  if (parentId && definitions) {
    const parent = findNode(document.components, parentId);
    const definition = definitionsMap(definitions).get(parent?.component.type);
    if (!canContain(definition, component.type)) return false;
  }
  const destination = index === null || index < 0 || index > list.length ? list.length : index;
  list.splice(destination, 0, component);
  return true;
}

export function removeNode(document, id) {
  const node = findNode(document.components, id);
  if (!node) return null;
  node.siblings.splice(node.index, 1);
  return node.component;
}

export function moveNode(document, id, parentId = null, index = null, definitions = null) {
  const node = findNode(document.components, id);
  if (!node) return false;
  if (parentId === id || isDescendant(node.component, parentId)) return false;
  // Resolve and validate the destination before removing the node. Without
  // this preflight an invalid target would remove the component and then fail
  // to reinsert it, silently deleting user content.
  if (parentId) {
    const targetParent = findNode(document.components, parentId);
    if (!targetParent) return false;
    if (definitions) {
      const targetDefinition = definitionsMap(definitions).get(targetParent.component.type);
      if (!canContain(targetDefinition, node.component.type)) return false;
    }
  }
  if (!parentList(document, parentId)) return false;
  const sourceParent = node.parent;
  const sourceIndex = node.index;
  removeNode(document, id);
  if (insertNode(document, node.component, parentId, index, definitions)) return true;
  // Defensive recovery: insertNode should be infallible after preflight, but
  // never leave the document without the moved component if it is not.
  const sourceList = sourceParent ? (sourceParent.children ||= []) : document.components;
  sourceList.splice(Math.min(sourceIndex, sourceList.length), 0, node.component);
  return false;
}

export function shiftSibling(document, id, delta) {
  const node = findNode(document.components, id);
  if (!node) return false;
  const destination = node.index + delta;
  if (destination < 0 || destination >= node.siblings.length) return false;
  const [moved] = node.siblings.splice(node.index, 1);
  node.siblings.splice(destination, 0, moved);
  return true;
}

export function indentNode(document, id, definitions = null) {
  const node = findNode(document.components, id);
  if (!node || node.index === 0) return false;
  const previous = node.siblings[node.index - 1];
  if (!previous || previous.type !== "container") return false;
  if (definitions) {
    const definition = definitionsMap(definitions).get(previous.type);
    if (!canContain(definition, node.component.type)) return false;
  }
  removeNode(document, id);
  previous.children ||= [];
  previous.children.push(node.component);
  return true;
}

export function outdentNode(document, id, definitions = null) {
  const node = findNode(document.components, id);
  if (!node || !node.parent) return false;
  const parentNode = findNode(document.components, node.parent.id);
  if (!parentNode) return false;
  if (parentNode.parent) {
    const grandparentDefinition = definitions
      ? definitionsMap(definitions).get(parentNode.parent.type)
      : null;
    if (definitions && !canContain(grandparentDefinition, node.component.type)) return false;
  }
  removeNode(document, id);
  parentNode.siblings.splice(parentNode.index + 1, 0, node.component);
  return true;
}

export function remapIds(component, idFactory = makeId) {
  const copy = clone(component);
  walk([copy], (node) => {
    node.id = idFactory(node.type);
  });
  return copy;
}

export function duplicateNode(document, id, idFactory = makeId) {
  const node = findNode(document.components, id);
  if (!node) return null;
  const duplicate = remapIds(node.component, idFactory);
  node.siblings.splice(node.index + 1, 0, duplicate);
  return duplicate;
}

export function updateProp(component, key, value, breakpoint = "desktop") {
  if (!component) return false;
  if (!breakpoint || breakpoint === "desktop") {
    component.props ||= {};
    component.props[key] = value;
    return true;
  }
  if (breakpoint !== "tablet" && breakpoint !== "mobile") return false;
  component.responsive ||= {};
  component.responsive[breakpoint] ||= {};
  component.responsive[breakpoint][key] = value;
  return true;
}

export function resetResponsive(component, key, breakpoint) {
  if (!component?.responsive?.[breakpoint]) return false;
  delete component.responsive[breakpoint][key];
  if (Object.keys(component.responsive[breakpoint]).length === 0) {
    delete component.responsive[breakpoint];
  }
  return true;
}

export function hasOverride(component, key, breakpoint) {
  return Boolean(component?.responsive?.[breakpoint] && Object.hasOwn(component.responsive[breakpoint], key));
}

export function resolveProps(component, breakpoint = "desktop") {
  const resolved = { ...(component?.props || {}) };
  if (breakpoint === "tablet" || breakpoint === "mobile") {
    Object.assign(resolved, component?.responsive?.tablet || {});
  }
  if (breakpoint === "mobile") {
    Object.assign(resolved, component?.responsive?.mobile || {});
  }
  return resolved;
}

export function inheritedValue(component, key, breakpoint) {
  if (breakpoint === "mobile") {
    if (hasOverride(component, key, "tablet")) return component.responsive.tablet[key];
    return component.props?.[key];
  }
  return component.props?.[key];
}

export function setHidden(document, id, hidden) {
  const node = findNode(document.components, id);
  if (!node) return false;
  if (hidden) node.component.hidden = true;
  else delete node.component.hidden;
  return true;
}

export function componentSummary(component, definition = null) {
  const props = component?.props || {};
  const text = props.text || props.label || props.title || props.brand || props.fallback || props.slot;
  if (typeof text === "string" && text.trim() !== "") {
    return text.length > 52 ? `${text.slice(0, 49)}…` : text;
  }
  return definition?.label || component?.type || "Component";
}

export function allNodes(document) {
  const nodes = [];
  walk(document.components, (component, parent, index, depth) => {
    nodes.push({ component, parent, index, depth });
  });
  return nodes;
}

const typeChecks = {
  string: (value) => typeof value === "string",
  text: (value) => typeof value === "string",
  number: (value) => typeof value === "number" && Number.isFinite(value),
  boolean: (value) => typeof value === "boolean",
  enum: (value, property) => property.options?.some((option) => option.value === value),
  color: (value) => value === "" || /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(value),
  image: (value) => value === "" || safeURL(value),
  url: (value) => value === "" || safeURL(value),
  "same-origin-url": (value) => value === "" || safeSameOrigin(value),
  spacing: (value, property) =>
    (property.options || []).some((option) => option.value === value),
  array: (value) => Array.isArray(value),
};

export function safeURL(value) {
  if (typeof value !== "string" || value === "" || /[\r\n\t\\]/.test(value)) return false;
  if (value.startsWith("/")) return safeSameOrigin(value);
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && !parsed.username && !parsed.password;
  } catch {
    return false;
  }
}

export function safeSameOrigin(value) {
  return typeof value === "string" && value.startsWith("/") && !value.startsWith("//") && !/[\\\r\n\t]/.test(value);
}

const MAX_COMPONENT_DEPTH = 6;
const MAX_COMPONENT_NODES = 200;
const MAX_PROPS_PER_COMPONENT = 32;
const COMPONENT_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;

function validateProperty(property, value, path, errors) {
  const check = typeChecks[property.kind];
  if (value === null || value === undefined) {
    errors.push({ path, message: `${property.label || property.key} cannot be empty` });
    return;
  }
  if (!check || !check(value, property)) {
    errors.push({ path, message: `${property.label || property.key} has an invalid value` });
    return;
  }
  if ((property.kind === "string" || property.kind === "text") && property.maxLength && value.length > property.maxLength) {
    errors.push({ path, message: `${property.label || property.key} is too long` });
  }
  if (property.kind === "number" && property.hasRange && (value < property.min || value > property.max)) {
    errors.push({ path, message: `${property.label || property.key} must be between ${property.min} and ${property.max}` });
  }
  if (property.kind === "array") {
    if (property.maxItems && value.length > property.maxItems) {
      errors.push({ path, message: `${property.label || property.key} has too many items` });
    }
    const itemProperties = property.itemProperties || [];
    value.forEach((item, index) => {
      if (typeof item !== "object" || item === null || Array.isArray(item)) {
        errors.push({ path: `${path}[${index}]`, message: "Item must be an object" });
        return;
      }
      for (const [key, itemValue] of Object.entries(item)) {
        const itemProperty = itemProperties.find((candidate) => candidate.key === key);
        if (!itemProperty) {
          errors.push({ path: `${path}[${index}].${key}`, message: "Unknown object property" });
        } else {
          validateProperty(itemProperty, itemValue, `${path}[${index}].${key}`, errors);
        }
      }
    });
  }
}

function approvedBinding(dataSources, source, field) {
  if (!Array.isArray(dataSources)) return true;
  return dataSources.some(
    (candidate) =>
      candidate.id === source &&
      (candidate.fields || []).some((candidateField) => candidateField.id === field),
  );
}

function validateComponents(components, parentPath, definitions, dataSources, seen, errors) {
  let nodes = 0;
  const visit = (list, path, parentDefinition, depth) => {
    list.forEach((component, index) => {
      const componentPath = `${path}[${index}]`;
      nodes += 1;
      if (nodes > MAX_COMPONENT_NODES) {
        if (nodes === MAX_COMPONENT_NODES + 1) {
          errors.push({ path: componentPath, message: `Document exceeds ${MAX_COMPONENT_NODES} components` });
        }
        return;
      }
      if (typeof component.id !== "string" || !COMPONENT_ID_PATTERN.test(component.id)) {
        errors.push({ path: `${componentPath}.id`, message: "Component id is invalid" });
      } else if (seen.has(component.id)) {
        errors.push({ path: `${componentPath}.id`, message: `Duplicate component id ${component.id}` });
      } else {
        seen.add(component.id);
      }
      const definition = definitions.get(component.type);
      if (!definition) {
        errors.push({ path: `${componentPath}.type`, message: `Unsupported component type ${component.type}` });
        return;
      }
      if (depth > MAX_COMPONENT_DEPTH) {
        errors.push({ path: componentPath, message: `Component nesting exceeds ${MAX_COMPONENT_DEPTH} levels` });
        return;
      }
      const props = component.props || {};
      if (Object.keys(props).length > MAX_PROPS_PER_COMPONENT) {
        errors.push({ path: `${componentPath}.props`, message: "Component has too many properties" });
      }
      for (const [key, value] of Object.entries(props)) {
        const property = definition.properties.find((candidate) => candidate.key === key);
        if (!property) errors.push({ path: `${componentPath}.props.${key}`, message: "Unknown property key" });
        else validateProperty(property, value, `${componentPath}.props.${key}`, errors);
      }
      for (const [breakpoint, overrides] of Object.entries(component.responsive || {})) {
        if (breakpoint !== "tablet" && breakpoint !== "mobile") {
          errors.push({ path: `${componentPath}.responsive`, message: `Unsupported breakpoint ${breakpoint}` });
          continue;
        }
        for (const [key, value] of Object.entries(overrides || {})) {
          const property = definition.properties.find((candidate) => candidate.key === key);
          if (!property) {
            errors.push({ path: `${componentPath}.responsive.${breakpoint}.${key}`, message: "Unknown property key" });
          } else if (!property.responsive) {
            errors.push({ path: `${componentPath}.responsive.${breakpoint}.${key}`, message: "Property is not responsive" });
          } else {
            validateProperty(property, value, `${componentPath}.responsive.${breakpoint}.${key}`, errors);
          }
        }
      }
      if (component.binding) {
        if (!definition.bindingAllowed) {
          errors.push({ path: `${componentPath}.binding`, message: `${definition.label} does not support data binding` });
        } else if (!approvedBinding(dataSources, component.binding.source, component.binding.field)) {
          errors.push({ path: `${componentPath}.binding`, message: "Binding must use an approved data source" });
        }
      }
      if (component.hidden !== undefined && typeof component.hidden !== "boolean") {
        errors.push({ path: `${componentPath}.hidden`, message: "Hidden must be a boolean" });
      }
      const children = Array.isArray(component.children) ? component.children : [];
      if (children.length > 0 && !definition.allowChildren) {
        errors.push({ path: `${componentPath}.children`, message: `${definition.label} cannot contain children` });
        return;
      }
      if (parentDefinition && !parentDefinition.allowChildren) {
        errors.push({ path: componentPath, message: "Parent cannot contain children" });
      }
      if (definition.allowedChildren?.length) {
        children.forEach((child, childIndex) => {
          if (!definition.allowedChildren.includes(child.type)) {
            errors.push({
              path: `${componentPath}.children[${childIndex}].type`,
              message: `${child.type} cannot be nested inside ${definition.label}`,
            });
          }
        });
      }
      visit(children, `${componentPath}.children`, definition, depth + 1);
    });
  };
  visit(components, parentPath, null, 1);
}

export function validateDocument(document, definitions, options = {}) {
  const errors = [];
  const byType = definitionsMap(definitions || []);
  if (!document || document.schemaVersion !== SCHEMA_VERSION) {
    errors.push({ path: "schemaVersion", message: `Unsupported schema version ${document?.schemaVersion}` });
  }
  validateComponents(
    Array.isArray(document?.components) ? document.components : [],
    "components",
    byType,
    options.dataSources,
    new Set(),
    errors,
  );
  return errors;
}

// SaveCoordinator owns autosave timing and single-flight sequencing without
// touching the DOM. The editor supplies the actual save function; tests can
// inject timers and a save function to cover debounce, failure, and stale-reply
// behavior deterministically.
export class SaveCoordinator {
  constructor(options = {}) {
    this.delay = Number.isFinite(options.delay) ? Math.max(0, options.delay) : 800;
    this.setTimer = options.setTimeout ?? ((callback, delay) => setTimeout(callback, delay));
    this.clearTimer = options.clearTimeout ?? ((timer) => clearTimeout(timer));
    this.timer = null;
    this.inFlight = null;
    this.generation = 0;
  }

  get isSaving() {
    return this.inFlight !== null;
  }

  schedule(callback, delay = this.delay) {
    this.cancel();
    this.timer = this.setTimer(() => {
      this.timer = null;
      callback();
    }, delay);
  }

  cancel() {
    if (this.timer === null) return;
    this.clearTimer(this.timer);
    this.timer = null;
  }

  async wait() {
    if (this.inFlight) await this.inFlight;
  }

  // run serializes saves. A request that arrives while another is in flight
  // waits for it, then runs again so it can persist the newer document.
  async run(save) {
    this.cancel();
    if (this.inFlight) {
      await this.wait();
      return this.run(save);
    }
    this.generation += 1;
    const generation = this.generation;
    const promise = Promise.resolve().then(() => save(generation));
    this.inFlight = promise;
    try {
      return await promise;
    } finally {
      if (this.inFlight === promise) this.inFlight = null;
    }
  }

  isCurrent(generation) {
    return generation === this.generation;
  }
}

export class History {
  constructor(initial) {
    this.undoStack = [];
    this.redoStack = [];
    this.lastKey = "";
    this.lastAt = 0;
    this.limit = HISTORY_LIMIT;
    this.reset(initial);
  }

  reset() {
    this.undoStack = [];
    this.redoStack = [];
    this.lastKey = "";
    this.lastAt = 0;
  }

  record(snapshot, key = "", now = Date.now()) {
    if (key && key === this.lastKey && now - this.lastAt < 900) {
      this.lastAt = now;
      return;
    }
    this.undoStack.push(clone(snapshot));
    if (this.undoStack.length > this.limit) this.undoStack.shift();
    this.redoStack = [];
    this.lastKey = key;
    this.lastAt = now;
  }

  undo(current) {
    if (this.undoStack.length === 0) return null;
    this.redoStack.push(clone(current));
    this.lastKey = "";
    return this.undoStack.pop();
  }

  redo(current) {
    if (this.redoStack.length === 0) return null;
    this.undoStack.push(clone(current));
    this.lastKey = "";
    return this.redoStack.pop();
  }

  canUndo() {
    return this.undoStack.length > 0;
  }

  canRedo() {
    return this.redoStack.length > 0;
  }
}
