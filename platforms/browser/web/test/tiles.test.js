import test from "node:test";
import assert from "node:assert/strict";
import {
  makeLeaf,
  isLeaf,
  listLeaves,
  splitTile,
  equalizeTree,
  closeTile,
  setSplitRatio,
  pruneTree,
  serializeTree,
  deserializeTree,
} from "../src/lib/tiles.js";

test("makeLeaf creates leaf nodes with defaults and explicit ids", () => {
  const a = makeLeaf("tab-1", "tile-a");
  assert.equal(isLeaf(a), true);
  assert.deepEqual(a, { id: "tile-a", tab: "tab-1" });

  const home = makeLeaf();
  assert.equal(isLeaf(home), true);
  assert.equal(home.tab, "");
  assert.ok(home.id.startsWith("tile-"));
});

test("splitTile handles right, left, top, bottom and enforces 6-tile cap", () => {
  const root = makeLeaf("tab-1", "t1");

  const r1 = splitTile(root, "t1", "right", "tab-2");
  assert.equal(r1.tree.dir, "h");
  assert.equal(r1.tree.a.id, "t1");
  assert.equal(r1.tree.b.id, r1.createdId);
  assert.equal(r1.tree.b.tab, "tab-2");

  const r2 = splitTile(root, "t1", "left", "tab-left");
  assert.equal(r2.tree.dir, "h");
  assert.equal(r2.tree.a.id, r2.createdId);
  assert.equal(r2.tree.b.id, "t1");

  const r3 = splitTile(root, "t1", "top", "tab-top");
  assert.equal(r3.tree.dir, "v");
  assert.equal(r3.tree.a.id, r3.createdId);
  assert.equal(r3.tree.b.id, "t1");

  const r4 = splitTile(root, "t1", "bottom", "tab-bottom");
  assert.equal(r4.tree.dir, "v");
  assert.equal(r4.tree.a.id, "t1");
  assert.equal(r4.tree.b.id, r4.createdId);

  // Split until 6 tiles, then 7th must be rejected.
  let tree = makeLeaf("tab-1", "cap-1");
  for (let i = 2; i <= 6; i++) {
    const res = splitTile(tree, listLeaves(tree).at(-1).id, "right", `tab-${i}`);
    assert.ok(res.createdId, `split ${i} should succeed`);
    tree = res.tree;
  }
  assert.equal(listLeaves(tree).length, 6);

  const capped = splitTile(tree, listLeaves(tree)[0].id, "right", "tab-7");
  assert.equal(capped.createdId, "");
  assert.equal(capped.tree, tree);
  assert.equal(listLeaves(capped.tree).length, 6);
});

test("equalizeTree balances 3-way horizontal split to 1/3 each and handles nested H/V", () => {
  let tree = makeLeaf("a", "la");
  let res = splitTile(tree, "la", "right", "b");
  tree = res.tree;
  const lb = res.createdId;
  res = splitTile(tree, lb, "right", "c");
  tree = res.tree;

  const eq = equalizeTree(tree);
  assert.equal(eq.dir, "h");
  assert.ok(Math.abs(eq.ratio - 1 / 3) < 1e-9, `expected 1/3, got ${eq.ratio}`);
  assert.equal(eq.b.dir, "h");
  assert.ok(Math.abs(eq.b.ratio - 0.5) < 1e-9, `expected 1/2, got ${eq.b.ratio}`);

  // Nested V inside H: vertical split counts as span 1 along H, so outer H stays 1/2.
  const nested = splitTile(makeLeaf("a", "n1"), "n1", "right", "b");
  const nested2 = splitTile(nested.tree, nested.createdId, "bottom", "c");
  const eqNested = equalizeTree(nested2.tree);
  assert.equal(eqNested.dir, "h");
  assert.ok(Math.abs(eqNested.ratio - 0.5) < 1e-9);
  assert.equal(eqNested.b.dir, "v");
  assert.ok(Math.abs(eqNested.b.ratio - 0.5) < 1e-9);
});

test("closeTile promotes sibling and resets last tile to Home", () => {
  const root = makeLeaf("tab-1", "solo");
  const closedSolo = closeTile(root, "solo");
  assert.equal(isLeaf(closedSolo.tree), true);
  assert.equal(closedSolo.tree.id, "solo");
  assert.equal(closedSolo.tree.tab, "");
  assert.equal(closedSolo.nextActiveId, "solo");

  const split = splitTile(makeLeaf("tab-1", "t1"), "t1", "right", "tab-2");
  const closedLeft = closeTile(split.tree, "t1");
  assert.equal(isLeaf(closedLeft.tree), true);
  assert.equal(closedLeft.tree.id, split.createdId);
  assert.equal(closedLeft.tree.tab, "tab-2");
  assert.equal(closedLeft.nextActiveId, split.createdId);

  const closedRight = closeTile(split.tree, split.createdId);
  assert.equal(isLeaf(closedRight.tree), true);
  assert.equal(closedRight.tree.id, "t1");
  assert.equal(closedRight.nextActiveId, "t1");
});

test("setSplitRatio clamps ratio to [0.18, 0.82]", () => {
  const { tree } = splitTile(makeLeaf("a", "a"), "a", "right", "b");
  const low = setSplitRatio(tree, tree.id, 0.01);
  assert.equal(low.ratio, 0.18);

  const high = setSplitRatio(tree, tree.id, 0.99);
  assert.equal(high.ratio, 0.82);

  const mid = setSplitRatio(tree, tree.id, 0.65);
  assert.equal(mid.ratio, 0.65);
});

test("pruneTree clears tabs missing from validTabIds", () => {
  const { tree } = splitTile(makeLeaf("tab-1", "t1"), "t1", "right", "tab-2");
  const pruned = pruneTree(tree, new Set(["tab-2"]));
  assert.equal(pruned.a.tab, "");
  assert.equal(pruned.b.tab, "tab-2");
});

test("serializeTree and deserializeTree round-trip structure and clamped ratios", () => {
  let { tree, createdId } = splitTile(makeLeaf("tab-1", "t1"), "t1", "right", "tab-2");
  tree = splitTile(tree, createdId, "bottom", "tab-3").tree;
  tree = setSplitRatio(tree, tree.id, 0.3333);

  const raw = serializeTree(tree);
  assert.deepEqual(raw, {
    dir: "h",
    ratio: 0.333,
    a: { tab: "tab-1" },
    b: {
      dir: "v",
      ratio: 0.5,
      a: { tab: "tab-2" },
      b: { tab: "tab-3" },
    },
  });

  const restored = deserializeTree(raw);
  assert.deepEqual(serializeTree(restored), raw);
  assert.equal(listLeaves(restored).map(l => l.tab).join(","), "tab-1,tab-2,tab-3");

  // Out-of-range ratios in serialized payload are clamped on deserialize.
  const clamped = deserializeTree({ dir: "h", ratio: 0.05, a: { tab: "a" }, b: { tab: "b" } });
  assert.equal(clamped.ratio, 0.18);
});
