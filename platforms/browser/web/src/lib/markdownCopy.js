// Clean Markdown serialization for one-click copy (tables, code, messages)
// and DOM Selection -> Markdown conversion on Cmd+C / Ctrl+C inside the transcript.

function formatSpan(s) {
  const text = s?.t || "";
  if (!text) return "";
  if (s.c) {
    let ticks = "`";
    while (text.includes(ticks)) ticks += "`";
    const padded = text.startsWith("`") || text.endsWith("`") ? ` ${text} ` : text;
    const inner = `${ticks}${padded}${ticks}`;
    return s.h ? `[${inner}](${s.h})` : inner;
  }
  const m = /^([ \t\n]*)([\s\S]*?)([ \t\n]*)$/.exec(text);
  const lead = m[1], core0 = m[2], trail = m[3];
  if (!core0) return text;
  let core = core0;
  if (s.b && s.i) core = `***${core}***`;
  else if (s.b) core = `**${core}**`;
  else if (s.i) core = `*${core}*`;
  if (s.s) core = `~~${core}~~`;
  if (s.h) core = `[${core}](${s.h})`;
  return `${lead}${core}${trail}`;
}

export function spansToMarkdown(spans) {
  return (spans || []).map(formatSpan).join("");
}

function escapeCell(text) {
  return (text || "").replace(/\|/g, "\\|").replace(/\n+/g, "<br>").trim();
}

export function fenceCode(lang, text) {
  const body = (text || "").replace(/\n$/, "");
  let fence = "```";
  while (body.includes(fence)) fence += "`";
  return `${fence}${lang || ""}\n${body}\n${fence}`;
}

export function tableToMarkdown(rows) {
  if (!rows || rows.length === 0) return "";
  const cols = Math.max(0, ...rows.map(r => (r ? r.length : 0)));
  if (cols === 0) return "";
  const lines = [];
  for (let ri = 0; ri < rows.length; ri++) {
    const row = rows[ri] || [];
    const vals = [];
    for (let ci = 0; ci < cols; ci++) {
      vals.push(escapeCell(spansToMarkdown(row[ci])));
    }
    lines.push(`| ${vals.join(" | ")} |`);
    if (ri === 0) {
      lines.push(`| ${Array(cols).fill("---").join(" | ")} |`);
    }
  }
  return lines.join("\n");
}

function quoteLines(text) {
  return text
    .split("\n")
    .map(l => (l ? `> ${l}` : ">"))
    .join("\n");
}

export function blockToMarkdown(b) {
  if (!b) return "";
  switch (b.type) {
    case "p":
      return spansToMarkdown(b.spans);
    case "h": {
      const lvl = Math.max(1, Math.min(6, b.level || 2));
      return `${"#".repeat(lvl)} ${spansToMarkdown(b.spans)}`;
    }
    case "li": {
      const mark = !b.mark || b.mark === "·" ? "-" : b.mark;
      return `${"  ".repeat(b.depth || 0)}${mark} ${spansToMarkdown(b.spans)}`;
    }
    case "quote":
      return quoteLines(blocksToMarkdown(b.blocks || []));
    case "code":
    case "art":
      return fenceCode(b.lang || (b.type === "art" ? "text" : ""), b.text || "");
    case "table":
      return tableToMarkdown(b.rows);
    case "rule":
      return "---";
    default:
      return "";
  }
}

export function blocksToMarkdown(blocks) {
  const items = [];
  for (const b of blocks || []) {
    const text = blockToMarkdown(b);
    if (text) items.push({ kind: b.type, text });
  }
  let out = "";
  for (let i = 0; i < items.length; i++) {
    if (i > 0) {
      out += items[i].kind === "li" && items[i - 1].kind === "li" ? "\n" : "\n\n";
    }
    out += items[i].text;
  }
  return out;
}

// ── DOM Selection -> Clean Markdown ─────────────────────────────────────────

function closestEl(node, selector) {
  const el = node?.nodeType === 1 ? node : node?.parentElement;
  return el ? el.closest(selector) : null;
}

function wrapInline(text, prefix, suffix) {
  const m = /^([ \t\n]*)([\s\S]*?)([ \t\n]*)$/.exec(text);
  if (!m || !m[2]) return text;
  return `${m[1]}${prefix}${m[2]}${suffix}${m[3]}`;
}

function serializeInline(node) {
  if (!node) return "";
  if (node.nodeType === 3) return node.nodeValue || "";
  if (node.nodeType !== 1) return "";
  if (node.hasAttribute("data-no-copy")) return "";

  const tag = node.tagName;
  if (tag === "BR") return "\n";
  if (tag === "CODE") {
    const raw = node.textContent || "";
    if (!raw) return "";
    let ticks = "`";
    while (raw.includes(ticks)) ticks += "`";
    const padded = raw.startsWith("`") || raw.endsWith("`") ? ` ${raw} ` : raw;
    return `${ticks}${padded}${ticks}`;
  }
  let inner = "";
  for (const child of node.childNodes) inner += serializeInline(child);
  if (!inner) return "";
  switch (tag) {
    case "STRONG":
    case "B":
      return wrapInline(inner, "**", "**");
    case "EM":
    case "I":
      return wrapInline(inner, "*", "*");
    case "S":
    case "DEL":
      return wrapInline(inner, "~~", "~~");
    case "A": {
      const href = node.getAttribute("href");
      return href ? `[${inner}](${href})` : inner;
    }
    default:
      return inner;
  }
}

function serializeTableEl(tableEl) {
  const rows = [];
  for (const tr of tableEl.querySelectorAll("tr")) {
    const cells = [];
    for (const cell of tr.children) {
      if (cell.tagName === "TH" || cell.tagName === "TD") {
        cells.push(escapeCell(serializeInline(cell)));
      }
    }
    if (cells.length > 0) rows.push(cells);
  }
  if (rows.length === 0) return "";
  if (rows.length === 1 && rows[0].length === 1) return rows[0][0];
  const cols = Math.max(...rows.map(r => r.length));
  const lines = [];
  for (let ri = 0; ri < rows.length; ri++) {
    const padded = Array.from({ length: cols }, (_, ci) => rows[ri][ci] ?? "");
    lines.push(`| ${padded.join(" | ")} |`);
    if (ri === 0 && rows.length > 1) {
      lines.push(`| ${Array(cols).fill("---").join(" | ")} |`);
    }
  }
  return lines.join("\n");
}

function collectBlocks(root, out) {
  for (const child of root.childNodes) {
    if (child.nodeType === 3) {
      const text = child.nodeValue || "";
      if (text.trim()) out.push({ kind: "p", text });
      continue;
    }
    if (child.nodeType !== 1 || child.hasAttribute("data-no-copy")) continue;

    const tag = child.tagName;
    if (tag === "H3" || tag === "H4" || tag === "H1" || tag === "H2") {
      const text = serializeInline(child).trim();
      if (text) {
        const lvl = Number(child.getAttribute("data-md-level")) || Number(tag[1]) || 2;
        out.push({ kind: "h", text: `${"#".repeat(Math.max(1, Math.min(6, lvl)))} ${text}` });
      }
      continue;
    }
    if (child.classList?.contains("md-li")) {
      const bodyEl = child.querySelector(".md-li-body") || child;
      const text = serializeInline(bodyEl).trim();
      if (text) {
        const markAttr = child.getAttribute("data-md-mark");
        const mark = !markAttr || markAttr === "·" ? "-" : markAttr;
        const depth = Number(child.getAttribute("data-md-depth")) || 0;
        out.push({ kind: "li", text: `${"  ".repeat(depth)}${mark} ${text}` });
      }
      continue;
    }
    if (tag === "BLOCKQUOTE") {
      const nested = [];
      collectBlocks(child, nested);
      const joined = joinBlocks(nested);
      if (joined) out.push({ kind: "quote", text: quoteLines(joined) });
      continue;
    }
    if (tag === "PRE") {
      const text = child.textContent || "";
      if (text) {
        const lang = child.getAttribute("data-md-lang") || "";
        out.push({ kind: "code", text: fenceCode(lang, text) });
      }
      continue;
    }
    if (tag === "TABLE") {
      const tbl = serializeTableEl(child);
      if (tbl) out.push({ kind: "table", text: tbl });
      continue;
    }
    if (tag === "HR") {
      out.push({ kind: "rule", text: "---" });
      continue;
    }
    if (tag === "P") {
      const text = serializeInline(child);
      if (text.trim()) out.push({ kind: "p", text });
      continue;
    }
    if (tag === "TR") {
      const cells = [];
      for (const cell of child.children) {
        if (cell.tagName === "TH" || cell.tagName === "TD") {
          cells.push(escapeCell(serializeInline(cell)));
        }
      }
      if (cells.length === 1) out.push({ kind: "p", text: cells[0] });
      else if (cells.length > 1) out.push({ kind: "tr", text: `| ${cells.join(" | ")} |` });
      continue;
    }
    if (tag === "TH" || tag === "TD") {
      const text = serializeInline(child).trim();
      if (text) out.push({ kind: "p", text });
      continue;
    }
    // Inline-only wrapper (e.g. partial clone inside a paragraph or span)
    if (tag === "STRONG" || tag === "EM" || tag === "CODE" || tag === "S" || tag === "A" || tag === "SPAN") {
      const text = serializeInline(child);
      if (text) {
        if (out.length > 0 && out[out.length - 1].kind === "inline") {
          out[out.length - 1].text += text;
        } else {
          out.push({ kind: "inline", text });
        }
      }
      continue;
    }
    // Container div (virt-row, row-said, md-pre-wrap, md-table-wrap, etc.)
    collectBlocks(child, out);
  }
}

function joinBlocks(items) {
  let out = "";
  for (let i = 0; i < items.length; i++) {
    if (i > 0) {
      const prev = items[i - 1].kind;
      const cur = items[i].kind;
      if ((cur === "li" && prev === "li") || (cur === "tr" && prev === "tr")) {
        out += "\n";
      } else {
        out += "\n\n";
      }
    }
    out += items[i].text;
  }
  return out;
}

// Convert the active DOM Selection inside a transcript container into clean Markdown.
// Returns null if selection is empty or inside an input/textarea.
export function selectionToMarkdown(selection) {
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return null;
  const range = selection.getRangeAt(0);
  const ancestor = range.commonAncestorContainer;

  // Selecting inside a single <pre> copies plain code substring (no triple-backtick wrapper).
  const singlePre = closestEl(ancestor, "pre");
  if (singlePre) {
    const plain = range.toString();
    return plain || null;
  }

  // Selecting inside a single table cell copies its clean inline markdown.
  const singleCell = closestEl(ancestor, "td, th");
  if (singleCell) {
    const frag = range.cloneContents();
    let inline = "";
    for (const child of frag.childNodes) inline += serializeInline(child);
    return inline || range.toString() || null;
  }

  // Selecting inside a single inline formatting element (e.g. inside <strong> or <code>).
  const frag = range.cloneContents();
  const blocks = [];
  collectBlocks(frag, blocks);

  // If the range lived wholly inside one inline tag, re-apply its formatting wrapper.
  const singleInline = closestEl(ancestor, "code, strong, em, s, a");
  if (singleInline && blocks.length === 1 && (blocks[0].kind === "p" || blocks[0].kind === "inline")) {
    const txt = blocks[0].text;
    const tag = singleInline.tagName;
    if (tag === "CODE") {
      let ticks = "`";
      while (txt.includes(ticks)) ticks += "`";
      return `${ticks}${txt}${ticks}`;
    }
    if (tag === "STRONG") return wrapInline(txt, "**", "**");
    if (tag === "EM") return wrapInline(txt, "*", "*");
    if (tag === "S") return wrapInline(txt, "~~", "~~");
    if (tag === "A" && singleInline.getAttribute("href")) {
      return `[${txt}](${singleInline.getAttribute("href")})`;
    }
  }

  // If the entire selection was inside a single heading/list-item/blockquote, preserve its prefix
  // when starting at the beginning of that element.
  const singleHeading = closestEl(ancestor, "h1, h2, h3, h4");
  if (singleHeading && blocks.length === 1 && blocks[0].kind !== "h" && range.startOffset === 0) {
    const lvl = Number(singleHeading.getAttribute("data-md-level")) || Number(singleHeading.tagName[1]) || 2;
    blocks[0].text = `${"#".repeat(Math.max(1, Math.min(6, lvl)))} ${blocks[0].text}`;
  }
  const singleQuote = closestEl(ancestor, "blockquote");
  if (singleQuote && blocks.length > 0 && blocks[0].kind !== "quote") {
    return quoteLines(joinBlocks(blocks));
  }

  const md = joinBlocks(blocks);
  return md || null;
}
