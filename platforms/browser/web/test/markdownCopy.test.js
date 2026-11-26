import test from "node:test";
import assert from "node:assert/strict";
import {
  spansToMarkdown,
  tableToMarkdown,
  blocksToMarkdown,
} from "../src/lib/markdownCopy.js";

test("spansToMarkdown formats bold, italic, bold+italic, code, strike, links, and keeps whitespace outside delimiters", () => {
  assert.equal(spansToMarkdown([{ t: "bold", b: true }]), "**bold**");
  assert.equal(spansToMarkdown([{ t: "ital", i: true }]), "*ital*");
  assert.equal(spansToMarkdown([{ t: "both", b: true, i: true }]), "***both***");
  assert.equal(spansToMarkdown([{ t: "gone", s: true }]), "~~gone~~");
  assert.equal(
    spansToMarkdown([{ t: "docs", h: "https://example.com" }]),
    "[docs](https://example.com)"
  );
  assert.equal(
    spansToMarkdown([{ t: "  spaced  ", b: true }]),
    "  **spaced**  "
  );
  // Inline code with embedded backticks uses longer delimiter and pads edge backticks.
  assert.equal(spansToMarkdown([{ t: "a`b", c: true }]), "``a`b``");
  assert.equal(spansToMarkdown([{ t: "`edge`", c: true }]), "`` `edge` ``");
  assert.equal(
    spansToMarkdown([{ t: "fn()", c: true, h: "https://example.com/fn" }]),
    "[`fn()`](https://example.com/fn)"
  );
});

test("tableToMarkdown produces GFM pipes, escapes pipes, converts newlines to <br>, and pads jagged rows", () => {
  const md = tableToMarkdown([
    [[{ t: "Col A" }], [{ t: "Col | B" }]],
    [[{ t: "line1\nline2" }]],
  ]);
  assert.equal(
    md,
    [
      "| Col A | Col \\| B |",
      "| --- | --- |",
      "| line1<br>line2 |  |",
    ].join("\n")
  );
  assert.equal(tableToMarkdown([]), "");
});

test("blocksToMarkdown renders headings, nested lists, blockquotes, fenced code with nested backticks, tables, and rules", () => {
  const md = blocksToMarkdown([
    { type: "h", level: 2, spans: [{ t: "Summary" }] },
    { type: "li", depth: 0, mark: "·", spans: [{ t: "first" }] },
    { type: "li", depth: 1, mark: "-", spans: [{ t: "nested" }] },
    {
      type: "quote",
      blocks: [
        { type: "p", spans: [{ t: "quoted line" }] },
      ],
    },
    { type: "code", lang: "md", text: "```js\nconsole.log(1)\n```" },
    {
      type: "table",
      rows: [
        [[{ t: "K" }], [{ t: "V" }]],
        [[{ t: "a" }], [{ t: "1" }]],
      ],
    },
    { type: "rule" },
  ]);

  assert.equal(
    md,
    [
      "## Summary",
      "",
      "- first",
      "  - nested",
      "",
      "> quoted line",
      "",
      "````md",
      "```js",
      "console.log(1)",
      "```",
      "````",
      "",
      "| K | V |",
      "| --- | --- |",
      "| a | 1 |",
      "",
      "---",
    ].join("\n")
  );
});
