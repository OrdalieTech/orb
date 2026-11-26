// Recursive binary split-tile layout tree for Web, aligned with platforms/desktop/tiles.go.
// A node is either a leaf { id, tab } (tab === "" renders Home) or a split
// { id, dir: "h" | "v", ratio: 0.18..0.82, a, b }.

let seq = 1;
function nextId(prefix = "t") {
  return `${prefix}${seq++}`;
}

export function isLeaf(node) {
  return !!node && !node.dir;
}

export function makeLeaf(tab = "", id = "") {
  return { id: id || nextId("tile-"), tab: tab || "" };
}

export function listLeaves(node, out = []) {
  if (!node) return out;
  if (isLeaf(node)) {
    out.push(node);
    return out;
  }
  listLeaves(node.a, out);
  listLeaves(node.b, out);
  return out;
}

export function findLeaf(node, id) {
  if (!node || !id) return null;
  if (isLeaf(node)) return node.id === id ? node : null;
  return findLeaf(node.a, id) || findLeaf(node.b, id);
}

export function findLeafByTab(node, tabId) {
  if (!node || !tabId) return null;
  if (isLeaf(node)) return node.tab === tabId ? node : null;
  return findLeafByTab(node.a, tabId) || findLeafByTab(node.b, tabId);
}

export function visibleTabIds(node) {
  const seen = new Set();
  const ids = [];
  for (const leaf of listLeaves(node)) {
    if (leaf.tab && !seen.has(leaf.tab)) {
      seen.add(leaf.tab);
      ids.push(leaf.tab);
    }
  }
  return ids;
}

export function setTileTab(node, tileId, tabId) {
  if (!node) return node;
  if (isLeaf(node)) {
    return node.id === tileId ? { ...node, tab: tabId || "" } : node;
  }
  const a = setTileTab(node.a, tileId, tabId);
  const b = setTileTab(node.b, tileId, tabId);
  if (a === node.a && b === node.b) return node;
  return { ...node, a, b };
}

// Split targetId on side ("right" | "left" | "bottom" | "top") with newTab.
// Caps at 6 tiles to keep panes readable, matching Desktop's room guard.
export function splitTile(tree, targetId, side = "right", newTab = "") {
  const leaves = listLeaves(tree);
  if (leaves.length >= 6) return { tree, createdId: "" };
  const dir = side === "top" || side === "bottom" ? "v" : "h";
  const before = side === "left" || side === "top";
  const created = makeLeaf(newTab);

  function walk(node) {
    if (!node) return node;
    if (isLeaf(node)) {
      if (node.id !== targetId) return node;
      return {
        id: nextId("split-"),
        dir,
        ratio: 0.5,
        a: before ? created : node,
        b: before ? node : created,
      };
    }
    const a = walk(node.a);
    const b = walk(node.b);
    if (a === node.a && b === node.b) return node;
    return { ...node, a, b };
  }

  const next = walk(tree);
  if (next === tree) return { tree, createdId: "" };
  return { tree: equalizeTree(next), createdId: created.id };
}

export function closeTile(tree, targetId) {
  if (!tree) return { tree: makeLeaf(""), nextActiveId: "" };
  if (isLeaf(tree)) {
    if (tree.id !== targetId) return { tree, nextActiveId: tree.id };
    const cleared = { ...tree, tab: "" };
    return { tree: cleared, nextActiveId: cleared.id };
  }

  let promoted = null;
  function walk(node) {
    if (isLeaf(node)) return node;
    if (isLeaf(node.a) && node.a.id === targetId) {
      promoted = listLeaves(node.b)[0] || null;
      return node.b;
    }
    if (isLeaf(node.b) && node.b.id === targetId) {
      promoted = listLeaves(node.a).at(-1) || null;
      return node.a;
    }
    const a = walk(node.a);
    const b = walk(node.b);
    if (a === node.a && b === node.b) return node;
    return { ...node, a, b };
  }

  const next = walk(tree);
  const equalized = next === tree ? tree : equalizeTree(next);
  const leaves = listLeaves(equalized);
  const nextActiveId = promoted?.id || leaves[0]?.id || "";
  return { tree: equalized, nextActiveId };
}

export function setSplitRatio(node, splitId, ratio) {
  if (!node || isLeaf(node)) return node;
  const clamped = Math.max(0.18, Math.min(0.82, ratio));
  if (node.id === splitId) {
    return { ...node, ratio: clamped };
  }
  const a = setSplitRatio(node.a, splitId, clamped);
  const b = setSplitRatio(node.b, splitId, clamped);
  if (a === node.a && b === node.b) return node;
  return { ...node, a, b };
}

// Count contiguous panes along axis dir so a 3-way split gets 1/3 + 2/3 -> 1/2 + 1/2 = 1/3 each.
function axisSpan(node, dir) {
  if (!node || isLeaf(node) || node.dir !== dir) return 1;
  return axisSpan(node.a, dir) + axisSpan(node.b, dir);
}

export function equalizeTree(node) {
  if (!node || isLeaf(node)) return node;
  const a = equalizeTree(node.a);
  const b = equalizeTree(node.b);
  const sa = axisSpan(a, node.dir);
  const sb = axisSpan(b, node.dir);
  return { ...node, ratio: sa / (sa + sb), a, b };
}

export function pruneTree(node, validTabIds) {
  if (!node) return node;
  if (isLeaf(node)) {
    return node.tab && !validTabIds.has(node.tab) ? { ...node, tab: "" } : node;
  }
  const a = pruneTree(node.a, validTabIds);
  const b = pruneTree(node.b, validTabIds);
  if (a === node.a && b === node.b) return node;
  return { ...node, a, b };
}

export function serializeTree(node) {
  if (!node) return null;
  if (isLeaf(node)) return { tab: node.tab || "" };
  return {
    dir: node.dir,
    ratio: Math.round((node.ratio ?? 0.5) * 1000) / 1000,
    a: serializeTree(node.a),
    b: serializeTree(node.b),
  };
}

export function deserializeTree(raw) {
  if (!raw || typeof raw !== "object") return null;
  if (raw.dir === "h" || raw.dir === "v") {
    const a = deserializeTree(raw.a);
    const b = deserializeTree(raw.b);
    if (!a || !b) return a || b || null;
    const ratio = typeof raw.ratio === "number" ? Math.max(0.18, Math.min(0.82, raw.ratio)) : 0.5;
    return { id: nextId("split-"), dir: raw.dir, ratio, a, b };
  }
  return makeLeaf(typeof raw.tab === "string" ? raw.tab : "");
}
