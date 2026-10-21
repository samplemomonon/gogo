import test from "node:test";
import assert from "node:assert/strict";

import {
  History,
  SCHEMA_VERSION,
  SaveCoordinator,
  canContain,
  clone,
  duplicateNode,
  findNode,
  findPath,
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

const text = (key, extra = {}) => ({
  key,
  label: key,
  kind: "text",
  maxLength: 500,
  ...extra,
});

const enumProp = (key, options, extra = {}) => ({
  key,
  label: key,
  kind: "enum",
  options: options.map((value) => ({ value, label: value })),
  default: options[0],
  ...extra,
});

const definitions = [
  {
    type: "container",
    label: "Container",
    allowChildren: true,
    properties: [
      { key: "layout", label: "Layout", kind: "enum", options: [{ value: "stack", label: "Stack" }], default: "stack" },
    ],
  },
  {
    type: "heading",
    label: "Heading",
    properties: [text("text"), enumProp("level", ["h1", "h2"], { default: "h2" })],
  },
  {
    type: "text",
    label: "Text",
    properties: [text("text")],
  },
  {
    type: "slot",
    label: "Restricted",
    bindingAllowed: true,
    properties: [],
  },
];

const definitionByType = (type) => definitions.find((definition) => definition.type === type);

const heading = (id, value = "Heading") => ({
  id,
  type: "heading",
  props: { text: value, level: "h2", align: "left", weight: "normal", color: "" },
});

const container = (id, children = []) => ({
  id,
  type: "container",
  props: { layout: "stack", gap: "md", padding: "md", background: "", maxWidth: "full", rounded: true, columns: 2 },
  children,
});

function documentWith(components) {
  return { schemaVersion: SCHEMA_VERSION, components };
}

function ids(components) {
  return components.map((component) => component.id);
}

test("factory creates defaults, ids, and child lists from the registry", () => {
  const component = newComponent(definitionByType("container"), (type) => `${type}-fixed`);
  assert.equal(component.id, "container-fixed");
  assert.equal(component.type, "container");
  assert.deepEqual(component.children, []);
  assert.equal(component.props.layout, "stack");
});

test("insertion appends at root, honours an index, and refuses invalid nesting", () => {
  const document = documentWith([heading("a")]);
  assert.equal(insertNode(document, heading("b"), null, 0, definitions), true);
  assert.deepEqual(ids(document.components), ["b", "a"]);

  const region = container("region");
  assert.equal(insertNode(document, region, null, null, definitions), true);
  assert.equal(insertNode(document, heading("c"), "region", null, definitions), true);
  assert.deepEqual(ids(document.components.at(-1).children), ["c"]);

  const restricted = {
    ...definitionByType("container"),
    allowedChildren: ["heading"],
  };
  assert.equal(canContain(restricted, "text"), false);
  assert.equal(canContain(restricted, "heading"), true);

  const limited = [...definitions.filter((definition) => definition.type !== "container"), restricted];
  assert.equal(insertNode(document, { id: "nope", type: "text", props: {} }, "region", null, limited), false);
});

test("find and remove walk nested children and report their parent", () => {
  const child = heading("child");
  const document = documentWith([container("parent", [child])]);
  const found = findNode(document.components, "child");
  assert.equal(found.component, child);
  assert.equal(found.parent.id, "parent");
  assert.equal(found.index, 0);
  assert.equal(removeNode(document, "child").id, "child");
  assert.deepEqual(findNode(document.components, "parent").component.children, []);
});

test("selection paths identify nested nodes from the document root", () => {
  const document = documentWith([
    heading("before"),
    container("outer", [heading("nested"), container("inner", [heading("deep")])]),
  ]);
  assert.deepEqual(findPath(document.components, "deep"), ["outer", "inner", "deep"]);
  assert.deepEqual(findPath(document.components, "before"), ["before"]);
  assert.equal(findPath(document.components, "missing"), null);
});

test("moving a node between parents preserves order and rejects cycles", () => {
  const document = documentWith([
    container("left", [heading("a"), heading("b")]),
    container("right", [heading("c")]),
  ]);
  assert.equal(moveNode(document, "b", "right", 1, definitions), true);
  assert.deepEqual(ids(document.components[1].children), ["c", "b"]);
  assert.deepEqual(ids(document.components[0].children), ["a"]);

  assert.equal(moveNode(document, "left", "left", 0, definitions), false);

  const outer = container("outer", [container("inner")]);
  const nested = documentWith([outer]);
  assert.equal(moveNode(nested, "outer", "inner", 0, definitions), false);
  assert.equal(isDescendant(document.components[0], "a"), true);
});

test("an invalid move never deletes the source component", () => {
  const document = documentWith([container("region", [heading("a")]), heading("b")]);
  const before = clone(document);

  assert.equal(moveNode(document, "a", "b", 0, definitions), false);
  assert.equal(moveNode(document, "a", "missing", 0, definitions), false);
  assert.deepEqual(document, before);
});

test("sibling reordering, nesting, and outdenting keep the tree valid", () => {
  const document = documentWith([heading("a"), container("region", [heading("b")]), heading("c")]);
  assert.equal(indentNode(document, "c", definitions), true);
  assert.deepEqual(ids(document.components), ["a", "region"]);
  assert.deepEqual(ids(document.components[1].children), ["b", "c"]);

  assert.equal(outdentNode(document, "b", definitions), true);
  assert.deepEqual(ids(document.components), ["a", "region", "b"]);
  assert.deepEqual(ids(document.components[1].children), ["c"]);

  const flat = documentWith([heading("a"), heading("b")]);
  assert.equal(shiftSibling(flat, "b", -1), true);
  assert.deepEqual(ids(flat.components), ["b", "a"]);
  assert.equal(shiftSibling(flat, "b", -1), false);
  assert.equal(outdentNode(flat, "a", definitions), false);
  assert.equal(indentNode(flat, "a", definitions), false);
});

test("duplicates and inserted sections receive fresh ids at every depth", () => {
  const original = container("region", [heading("child")]);
  const document = documentWith([original]);
  const duplicate = duplicateNode(document, "region", (type) => `${type}-new`);
  assert.equal(duplicate.id, "container-new");
  assert.equal(duplicate.children[0].id, "heading-new");
  assert.equal(document.components.length, 2);

  let sequence = 0;
  const remapped = remapIds(original, (type) => `${type}-${++sequence}`);
  assert.equal(remapped.id, "container-1");
  assert.equal(remapped.children[0].id, "heading-2");
  assert.equal(original.id, "region");
});

test("property updates use base props and inherit through responsive breakpoints", () => {
  const node = heading("a", "Base");
  assert.equal(updateProp(node, "text", "Tablet", "tablet"), true);
  assert.equal(updateProp(node, "text", "Mobile", "mobile"), true);

  assert.equal(resolveProps(node, "desktop").text, "Base");
  assert.equal(resolveProps(node, "tablet").text, "Tablet");
  assert.equal(resolveProps(node, "mobile").text, "Mobile");
  assert.equal(hasOverride(node, "text", "tablet"), true);
  assert.equal(hasOverride(node, "text", "desktop"), false);
  assert.equal(updateProp(node, "text", "ignored", "watch"), false);

  assert.equal(resetResponsive(node, "text", "tablet"), true);
  assert.equal(hasOverride(node, "text", "tablet"), false);
  assert.equal(resolveProps(node, "mobile").text, "Mobile");
  assert.equal(resetResponsive(node, "text", "tablet"), false);
});

test("mobile-only overrides fall back to desktop when tablet has no value", () => {
  const node = heading("a", "Base");
  updateProp(node, "text", "Mobile", "mobile");
  assert.equal(resolveProps(node, "tablet").text, "Base");
  assert.equal(resolveProps(node, "mobile").text, "Mobile");
});

test("history restores documents without sharing references", () => {
  const initial = documentWith([heading("a", "One")]);
  const history = new History(initial);
  const before = clone(initial);
  initial.components[0].props.text = "Two";
  history.record(before, "typing:one", 1);

  const undo = history.undo({ ...initial, components: clone(initial.components) });
  assert.equal(undo.components[0].props.text, "One");
  undo.components[0].props.text = "Mutated undo";
  assert.equal(history.redoStack[0].components[0].props.text, "Two");

  const redo = history.redo(undo);
  assert.equal(redo.components[0].props.text, "Two");
  assert.equal(history.canRedo(), false);
});

test("history coalesces only the same key inside the typing window", () => {
  const history = new History(documentWith([]));
  const snapshot = documentWith([]);
  history.record(snapshot, "typing:a", 1_000);
  history.record(snapshot, "typing:a", 1_500);
  assert.equal(history.undoStack.length, 1);
  history.record(snapshot, "typing:a", 2_500);
  assert.equal(history.undoStack.length, 2);
  history.record(snapshot, "typing:b", 2_600);
  assert.equal(history.undoStack.length, 3);
  history.record(snapshot, "", 2_700);
  history.record(snapshot, "", 2_800);
  assert.equal(history.undoStack.length, 5);
});

test("history is bounded", () => {
  const history = new History(documentWith([]));
  for (let index = 0; index < 150; index += 1) history.record(documentWith([]), `key-${index}`, index);
  assert.equal(history.undoStack.length, 100);
});

test("hidden is a removable document flag", () => {
  const document = documentWith([heading("a")]);
  assert.equal(setHidden(document, "a", true), true);
  assert.equal(document.components[0].hidden, true);
  assert.equal(setHidden(document, "a", false), true);
  assert.equal("hidden" in document.components[0], false);
});

test("validation reports canonical nested paths and safe values", () => {
  const document = documentWith([
    heading("a"),
    container("region", [
      { id: "a", type: "heading", props: { text: "Duplicate", level: "h2" } },
      { id: "weird id!", type: "text", props: { text: "Bad id" } },
      { id: "unknown", type: "carousel", props: {} },
      { id: "extra", type: "heading", props: { text: "Extra", level: "h2", danger: "x" } },
    ]),
  ]);
  const errors = validateDocument(document, definitions);
  const paths = errors.map((error) => error.path);
  assert.ok(paths.includes("schemaVersion") === false);
  assert.ok(paths.includes("components[1].children[0].id"));
  assert.ok(paths.includes("components[1].children[1].id"));
  assert.ok(paths.includes("components[1].children[2].type"));
  assert.ok(paths.includes("components[1].children[3].props.danger"));
  assert.ok(paths.some((path) => path.endsWith(".message") === false));
});

test("validation rejects the wrong schema version and unregistered properties", () => {
  const errors = validateDocument({ schemaVersion: 1, components: [] }, definitions);
  assert.deepEqual(errors.map((error) => error.path), ["schemaVersion"]);

  const document = documentWith([{ id: "a", type: "heading", props: { text: "x", level: "nope" } }]);
  const messages = validateDocument(document, definitions).map((error) => error.message);
  assert.ok(messages.includes("level has an invalid value"));
});

test("validation enforces approved bindings and child restrictions", () => {
  const dataSources = [{ id: "page", label: "Page", fields: [{ id: "title", label: "Title" }] }];
  const bound = documentWith([
    { id: "a", type: "slot", props: {}, binding: { source: "page", field: "title" } },
  ]);
  assert.deepEqual(validateDocument(bound, definitions, { dataSources }), []);

  const badSource = documentWith([
    { id: "a", type: "slot", props: {}, binding: { source: "secret", field: "token" } },
  ]);
  assert.equal(validateDocument(badSource, definitions, { dataSources })[0].message, "Binding must use an approved data source");

  const forbidden = documentWith([
    { id: "a", type: "heading", props: { text: "x", level: "h2" }, binding: { source: "page", field: "title" } },
  ]);
  assert.equal(validateDocument(forbidden, definitions, { dataSources })[0].message, "Heading does not support data binding");
});

test("validation catches duplicate ids, depth and node limits", () => {
  const duplicate = documentWith([heading("same"), heading("same")]);
  assert.ok(validateDocument(duplicate, definitions).some((error) => error.message.includes("Duplicate component id")));

  let deep = heading("leaf");
  for (let depth = 0; depth < 7; depth += 1) deep = container(`layer-${depth}`, [deep]);
  const deepErrors = validateDocument(documentWith([deep]), definitions);
  assert.ok(deepErrors.some((error) => error.message.includes("nesting exceeds")));

  const wide = documentWith(Array.from({ length: 201 }, (_, index) => heading(`n-${index}`)));
  assert.ok(validateDocument(wide, definitions).some((error) => error.message.includes("exceeds 200 components")));
});

test("safeURL permits same-origin and https URLs only", () => {
  assert.equal(safeURL("/media/hero.jpg"), true);
  assert.equal(safeURL("https://cdn.example.com/a.png"), true);
  assert.equal(safeURL("//evil.example/x"), false);
  assert.equal(safeURL("http://example.com"), false);
  assert.equal(safeURL("javascript:alert(1)"), false);
  assert.equal(safeURL("https://user:pass@example.com"), false);
  assert.equal(safeURL("https://example.com/a b"), true);
  assert.equal(safeURL("https://example.com/\\evil"), false);
});

function fakeTimers() {
  let nextId = 1;
  const pending = new Map();
  return {
    setTimeout(callback) {
      const id = nextId;
      nextId += 1;
      pending.set(id, callback);
      return id;
    },
    clearTimeout(id) {
      pending.delete(id);
    },
    runAll() {
      const callbacks = Array.from(pending.values());
      pending.clear();
      for (const callback of callbacks) callback();
    },
  };
}

test("save coordinator debounces autosave and runs only the newest schedule", () => {
  const calls = [];
  const timers = fakeTimers();
  const coordinator = new SaveCoordinator({
    delay: 10,
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
  });
  coordinator.schedule(() => calls.push("first"));
  coordinator.schedule(() => calls.push("second"));
  assert.deepEqual(calls, []);
  timers.runAll();
  assert.deepEqual(calls, ["second"]);
});

test("save coordinator cancels a pending autosave", () => {
  const calls = [];
  const timers = fakeTimers();
  const coordinator = new SaveCoordinator({
    delay: 10,
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
  });
  coordinator.schedule(() => calls.push("saved"));
  coordinator.cancel();
  timers.runAll();
  assert.deepEqual(calls, []);
});

test("save coordinator serializes a save that arrives while another is in flight", async () => {
  const events = [];
  let releaseFirst;
  let markStarted;
  const firstStarted = new Promise((resolve) => {
    markStarted = resolve;
  });
  const coordinator = new SaveCoordinator({ delay: 0 });

  const first = coordinator.run(async (generation) => {
    events.push(`start:${generation}`);
    markStarted();
    await new Promise((resolve) => {
      releaseFirst = resolve;
    });
    events.push(`end:${generation}`);
    return generation;
  });
  await firstStarted;
  assert.equal(coordinator.isSaving, true);

  const second = coordinator.run(async (generation) => {
    events.push(`start:${generation}`);
    return generation;
  });

  releaseFirst();
  assert.equal(await first, 1);
  assert.equal(await second, 2);
  assert.deepEqual(events, ["start:1", "end:1", "start:2"]);
  assert.equal(coordinator.isSaving, false);
});

test("save coordinator recovers after a failed save and advances the generation", async () => {
  const coordinator = new SaveCoordinator({ delay: 0 });
  await assert.rejects(
    coordinator.run(async () => {
      throw new Error("offline");
    }),
    /offline/,
  );
  assert.equal(coordinator.isSaving, false);

  const generation = await coordinator.run(async (next) => next);
  assert.equal(generation, 2);
  assert.equal(coordinator.isCurrent(2), true);
  assert.equal(coordinator.isCurrent(1), false);
  assert.equal(coordinator.isSaving, false);
});
