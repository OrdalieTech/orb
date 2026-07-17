import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type Edit = {
  oldText: string;
  newText: string;
};

type FixtureCase = {
  name: string;
  operation:
    | "detect-line-ending"
    | "normalize-lf"
    | "restore-line-endings"
    | "strip-bom"
    | "normalize-fuzzy"
    | "fuzzy-find"
    | "apply"
    | "edit-pipeline"
    | "diff"
    | "patch";
  upstreamCase: string;
  content?: string;
  oldText?: string;
  newText?: string;
  edits?: Edit[];
  path?: string;
  ending?: "\r\n" | "\n";
  contextLines?: number;
};

interface EditDiffModule {
  detectLineEnding(content: string): "\r\n" | "\n";
  normalizeToLF(text: string): string;
  restoreLineEndings(text: string, ending: "\r\n" | "\n"): string;
  normalizeForFuzzyMatch(text: string): string;
  fuzzyFindText(content: string, oldText: string): unknown;
  stripBom(content: string): { bom: string; text: string };
  applyEditsToNormalizedContent(
    normalizedContent: string,
    edits: Edit[],
    path: string,
  ): { baseContent: string; newContent: string };
  generateDiffString(
    oldContent: string,
    newContent: string,
    contextLines?: number,
  ): { diff: string; firstChangedLine: number | undefined };
  generateUnifiedPatch(path: string, oldContent: string, newContent: string, contextLines?: number): string;
}

const toolsTest = "packages/coding-agent/test/tools.test.ts";
const editDiffSource = "packages/coding-agent/src/core/tools/edit-diff.ts";

const basicCases: FixtureCase[] = [
  {
    name: "detect-line-ending-empty",
    operation: "detect-line-ending",
    upstreamCase: `${editDiffSource}:detectLineEnding default`,
    content: "",
  },
  {
    name: "detect-line-ending-lf",
    operation: "detect-line-ending",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve LF`,
    content: "first\nsecond\n",
  },
  {
    name: "detect-line-ending-crlf",
    operation: "detect-line-ending",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve CRLF`,
    content: "first\r\nsecond\r\n",
  },
  {
    name: "detect-line-ending-first-sequence-wins-lf",
    operation: "detect-line-ending",
    upstreamCase: `${editDiffSource}:detectLineEnding mixed input`,
    content: "first\nsecond\r\nthird\n",
  },
  {
    name: "detect-line-ending-first-sequence-wins-crlf",
    operation: "detect-line-ending",
    upstreamCase: `${editDiffSource}:detectLineEnding mixed input`,
    content: "first\r\nsecond\nthird\r\n",
  },
  {
    name: "normalize-lf-crlf-and-cr",
    operation: "normalize-lf",
    upstreamCase: `${editDiffSource}:normalizeToLF`,
    content: "one\r\ntwo\rthree\nfour",
  },
  {
    name: "restore-line-endings-crlf",
    operation: "restore-line-endings",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve CRLF`,
    content: "one\ntwo\nthree\n",
    ending: "\r\n",
  },
  {
    name: "restore-line-endings-lf",
    operation: "restore-line-endings",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve LF`,
    content: "one\ntwo\nthree\n",
    ending: "\n",
  },
  {
    name: "strip-bom-present",
    operation: "strip-bom",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve UTF-8 BOM`,
    content: "\uFEFFhello\n",
  },
  {
    name: "strip-bom-absent",
    operation: "strip-bom",
    upstreamCase: `${editDiffSource}:stripBom`,
    content: "hello\n",
  },
  {
    name: "normalize-fuzzy-all-transform-families",
    operation: "normalize-fuzzy",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching`,
    content:
      "ＡＢＣ１２３   \ncafe\u0301\n\u2018a\u2019 \u201Ab\u201B\n\u201Cc\u201D \u201Ed\u201F\n\u2010\u2011\u2012\u2013\u2014\u2015\u2212\n" +
      "a\u00A0b\u2002c\u2003d\u2004e\u2005f\u2006g\u2007h\u2008i\u2009j\u200Ak\u202Fl\u205Fm\u3000n\n",
  },
  {
    name: "normalize-fuzzy-preserves-next-line-character",
    operation: "normalize-fuzzy",
    upstreamCase: `${editDiffSource}:normalizeForFuzzyMatch JavaScript trimEnd whitespace set`,
    content: "x\u0085\ny\u0085",
  },
  {
    name: "normalize-fuzzy-trims-carriage-return-per-line",
    operation: "normalize-fuzzy",
    upstreamCase: `${editDiffSource}:normalizeForFuzzyMatch JavaScript trimEnd carriage return`,
    content: "x\r\ny",
  },
  {
    name: "fuzzy-find-exact-preferred",
    operation: "fuzzy-find",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/prefer exact match`,
    content: "const x = 'exact';\nconst y = \u2018exact\u2019;\n",
    oldText: "const x = 'exact';",
  },
  {
    name: "fuzzy-find-trailing-whitespace",
    operation: "fuzzy-find",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/trailing whitespace`,
    content: "line one   \nline two  \nline three\n",
    oldText: "line one\nline two\n",
  },
  {
    name: "fuzzy-find-missing",
    operation: "fuzzy-find",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/not found`,
    content: "completely different content\n",
    oldText: "this does not exist",
  },
  {
    name: "fuzzy-find-utf16-exact-index-and-length",
    operation: "fuzzy-find",
    upstreamCase: `${editDiffSource}:fuzzyFindText JavaScript string offsets`,
    content: "a😀b😀c",
    oldText: "😀c",
  },
  {
    name: "fuzzy-find-utf16-normalized-index",
    operation: "fuzzy-find",
    upstreamCase: `${editDiffSource}:fuzzyFindText normalized JavaScript string offsets`,
    content: "😀 prefix\nＡＢＣ\n",
    oldText: "ABC",
  },
];

const pipelineCases: FixtureCase[] = [
  {
    name: "edit-simple-replacement",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/replace text in file`,
    path: "edit-test.txt",
    content: "Hello, world!",
    edits: [{ oldText: "world", newText: "testing" }],
  },
  {
    name: "edit-not-found",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/fail if text not found`,
    path: "edit-test.txt",
    content: "Hello, world!",
    edits: [{ oldText: "nonexistent", newText: "testing" }],
  },
  {
    name: "edit-duplicate",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/fail if text appears multiple times`,
    path: "edit-test.txt",
    content: "foo foo foo",
    edits: [{ oldText: "foo", newText: "bar" }],
  },
  {
    name: "edit-multiple-disjoint",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/replace multiple disjoint regions`,
    path: "edit-multi.txt",
    content: "alpha\nbeta\ngamma\ndelta\n",
    edits: [
      { oldText: "alpha\n", newText: "ALPHA\n" },
      { oldText: "gamma\n", newText: "GAMMA\n" },
    ],
  },
  {
    name: "edit-original-content-not-incremental",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/match edits against original file`,
    path: "edit-multi-original.txt",
    content: "foo\nbar\nbaz\n",
    edits: [
      { oldText: "foo\n", newText: "foo bar\n" },
      { oldText: "bar\n", newText: "BAR\n" },
    ],
  },
  {
    name: "edit-overlapping-regions",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/fail when multi-edit regions overlap`,
    path: "edit-overlap.txt",
    content: "one\ntwo\nthree\n",
    edits: [
      { oldText: "one\ntwo\n", newText: "ONE\nTWO\n" },
      { oldText: "two\nthree\n", newText: "TWO\nTHREE\n" },
    ],
  },
  {
    name: "edit-no-partial-application",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/not partially apply edits`,
    path: "edit-no-partial.txt",
    content: "alpha\nbeta\ngamma\n",
    edits: [
      { oldText: "alpha\n", newText: "ALPHA\n" },
      { oldText: "missing\n", newText: "MISSING\n" },
    ],
  },
  {
    name: "edit-empty-old-text-single",
    operation: "apply",
    upstreamCase: `${editDiffSource}:getEmptyOldTextError single edit`,
    path: "empty.txt",
    content: "hello\n",
    edits: [{ oldText: "", newText: "prefix" }],
  },
  {
    name: "edit-empty-old-text-multiple",
    operation: "apply",
    upstreamCase: `${editDiffSource}:getEmptyOldTextError multiple edits`,
    path: "empty-multi.txt",
    content: "hello\nworld\n",
    edits: [
      { oldText: "hello", newText: "HELLO" },
      { oldText: "", newText: "prefix" },
    ],
  },
  {
    name: "edit-multi-not-found-index",
    operation: "apply",
    upstreamCase: `${editDiffSource}:getNotFoundError multiple edits`,
    path: "not-found-multi.txt",
    content: "alpha\nbeta\n",
    edits: [
      { oldText: "alpha", newText: "ALPHA" },
      { oldText: "missing", newText: "MISSING" },
    ],
  },
  {
    name: "edit-multi-duplicate-index",
    operation: "apply",
    upstreamCase: `${editDiffSource}:getDuplicateError multiple edits`,
    path: "duplicate-multi.txt",
    content: "alpha\nbeta\nbeta\n",
    edits: [
      { oldText: "alpha", newText: "ALPHA" },
      { oldText: "beta", newText: "BETA" },
    ],
  },
  {
    name: "edit-no-change-single",
    operation: "apply",
    upstreamCase: `${editDiffSource}:getNoChangeError single edit`,
    path: "no-change.txt",
    content: "hello\n",
    edits: [{ oldText: "hello", newText: "hello" }],
  },
  {
    name: "edit-no-change-multiple",
    operation: "apply",
    upstreamCase: `${editDiffSource}:getNoChangeError multiple edits`,
    path: "no-change-multi.txt",
    content: "hello\nworld\n",
    edits: [
      { oldText: "hello", newText: "hello" },
      { oldText: "world", newText: "world" },
    ],
  },
  {
    name: "edit-adjacent-regions-do-not-overlap",
    operation: "apply",
    upstreamCase: `${editDiffSource}:applyEditsToNormalizedContent adjacent ranges`,
    path: "adjacent.txt",
    content: "abcdef",
    edits: [
      { oldText: "abc", newText: "ABC" },
      { oldText: "def", newText: "DEF" },
    ],
  },
  {
    name: "edit-normalized-empty-separator-count-quirk",
    operation: "apply",
    upstreamCase: `${editDiffSource}:countOccurrences split empty-string semantics`,
    path: "normalized-empty.txt",
    content: "a b",
    edits: [{ oldText: " ", newText: "_" }],
  },
  {
    name: "fuzzy-trailing-whitespace",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/trailing whitespace stripped`,
    path: "trailing-ws.txt",
    content: "line one   \nline two  \nline three\n",
    edits: [{ oldText: "line one\nline two\n", newText: "replaced\n" }],
  },
  {
    name: "fuzzy-fullwidth-chinese-punctuation",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/fullwidth punctuation`,
    path: "chinese-punctuation.txt",
    content: "你好，世界\n你好（世界）\n",
    edits: [{ oldText: "你好,世界\n你好(世界)\n", newText: "你好，pi\n你好(pi)\n" }],
  },
  {
    name: "fuzzy-unicode-compatibility",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/compatibility-equivalent Unicode`,
    path: "unicode-compatibility.txt",
    content: "ＡＢＣ１２３\ncafe\u0301\n",
    edits: [{ oldText: "ABC123\ncafé\n", newText: "XYZ789\ncoffee\n" }],
  },
  {
    name: "fuzzy-smart-single-quotes",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/smart single quotes`,
    path: "smart-quotes.txt",
    content: "console.log(\u2018hello\u2019);\n",
    edits: [{ oldText: "console.log('hello');", newText: "console.log('world');" }],
  },
  {
    name: "fuzzy-smart-double-quotes",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/smart double quotes`,
    path: "smart-double-quotes.txt",
    content: "const msg = \u201CHello World\u201D;\n",
    edits: [{ oldText: 'const msg = "Hello World";', newText: 'const msg = "Goodbye";' }],
  },
  {
    name: "fuzzy-unicode-dashes",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/Unicode dashes`,
    path: "unicode-dashes.txt",
    content: "range: 1\u20135\nbreak\u2014here\n",
    edits: [{ oldText: "range: 1-5\nbreak-here", newText: "range: 10-50\nbreak--here" }],
  },
  {
    name: "fuzzy-non-breaking-space",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/non-breaking space`,
    path: "nbsp.txt",
    content: "hello\u00A0world\n",
    edits: [{ oldText: "hello world", newText: "hello universe" }],
  },
  {
    name: "fuzzy-exact-preferred",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/prefer exact match`,
    path: "exact-preferred.txt",
    content: "const x = 'exact';\nconst y = 'other';\n",
    edits: [{ oldText: "const x = 'exact';", newText: "const x = 'changed';" }],
  },
  {
    name: "fuzzy-not-found",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/not found`,
    path: "no-match.txt",
    content: "completely different content\n",
    edits: [{ oldText: "this does not exist", newText: "replacement" }],
  },
  {
    name: "fuzzy-duplicates-after-normalization",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/duplicates after normalization`,
    path: "fuzzy-dups.txt",
    content: "hello world   \nhello world\n",
    edits: [{ oldText: "hello world", newText: "replaced" }],
  },
  {
    name: "fuzzy-multiple-edits",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/multi-edit mode`,
    path: "fuzzy-multi.txt",
    content: "console.log(\u2018hello\u2019);\nhello\u00A0world\n",
    edits: [
      { oldText: "console.log('hello');\n", newText: "console.log('world');\n" },
      { oldText: "hello world\n", newText: "hello universe\n" },
    ],
  },
  {
    name: "fuzzy-preserve-correct-duplicate-line",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/preserve correct occurrence`,
    path: "fuzzy-preserve-duplicate-line.txt",
    content: "replace me   \nafter   \n",
    edits: [{ oldText: "replace me\n", newText: "after\n" }],
  },
  {
    name: "fuzzy-preserve-untouched-lines-multiple-edits",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool fuzzy matching/preserve untouched lines`,
    path: "fuzzy-preserve-multi.txt",
    content:
      "keep before  \nfirst target  \nfirst after\nkeep middle   \nsecond target  \nsecond after\nkeep after  \n",
    edits: [
      { oldText: "first target\nfirst after", newText: "FIRST\nFIRST2" },
      { oldText: "second target\nsecond after", newText: "SECOND\nSECOND2" },
    ],
  },
  {
    name: "fuzzy-preserve-astral-prefix",
    operation: "edit-pipeline",
    upstreamCase: `${editDiffSource}:fuzzy replacement with UTF-16 offsets`,
    path: "astral-prefix.txt",
    content: "😀 keep  \nＡＢＣ target  \nafter\n",
    edits: [{ oldText: "ABC target\n", newText: "changed\n" }],
  },
  {
    name: "crlf-match-lf-old-text",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/match LF oldText`,
    path: "crlf-test.txt",
    content: "line one\r\nline two\r\nline three\r\n",
    edits: [{ oldText: "line two\n", newText: "replaced line\n" }],
  },
  {
    name: "crlf-preserve-after-edit",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve CRLF`,
    path: "crlf-preserve.txt",
    content: "first\r\nsecond\r\nthird\r\n",
    edits: [{ oldText: "second\n", newText: "REPLACED\n" }],
  },
  {
    name: "lf-preserve-after-edit",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve LF`,
    path: "lf-preserve.txt",
    content: "first\nsecond\nthird\n",
    edits: [{ oldText: "second\n", newText: "REPLACED\n" }],
  },
  {
    name: "mixed-endings-duplicate-detection",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/duplicates across CRLF/LF`,
    path: "mixed-endings.txt",
    content: "hello\r\nworld\r\n---\r\nhello\nworld\n",
    edits: [{ oldText: "hello\nworld\n", newText: "replaced\n" }],
  },
  {
    name: "bom-preserved-after-edit",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve UTF-8 BOM`,
    path: "bom-test.txt",
    content: "\uFEFFfirst\r\nsecond\r\nthird\r\n",
    edits: [{ oldText: "second\n", newText: "REPLACED\n" }],
  },
  {
    name: "bom-and-crlf-preserved-multiple-edits",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool CRLF handling/preserve CRLF and BOM in multi-edit`,
    path: "bom-crlf-multi.txt",
    content: "\uFEFFfirst\r\nsecond\r\nthird\r\nfourth\r\n",
    edits: [
      { oldText: "second\n", newText: "SECOND\n" },
      { oldText: "fourth\n", newText: "FOURTH\n" },
    ],
  },
];

const diffCases: FixtureCase[] = [
  {
    name: "diff-simple-single-line",
    operation: "diff",
    upstreamCase: `${toolsTest}:edit tool/replace text in file diff`,
    oldText: "Hello, world!",
    newText: "Hello, testing!",
    contextLines: 4,
  },
  {
    name: "diff-no-changes",
    operation: "diff",
    upstreamCase: `${editDiffSource}:generateDiffString no changes`,
    oldText: "same\ncontent\n",
    newText: "same\ncontent\n",
    contextLines: 4,
  },
  {
    name: "diff-change-at-start-and-end",
    operation: "diff",
    upstreamCase: `${editDiffSource}:generateDiffString leading and trailing context`,
    oldText: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n",
    newText: "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN\n",
    contextLines: 2,
  },
  {
    name: "diff-adjacent-change-parts",
    operation: "diff",
    upstreamCase: `${editDiffSource}:generateDiffString context between changes`,
    oldText: "zero\none\ntwo\nthree\nfour\nfive\nsix\n",
    newText: "zero\nONE\ntwo\nthree\nfour\nFIVE\nsix\n",
    contextLines: 2,
  },
  {
    name: "diff-zero-context",
    operation: "diff",
    upstreamCase: `${editDiffSource}:generateDiffString zero context`,
    oldText: "one\ntwo\nthree\n",
    newText: "one\nTWO\nthree\n",
    contextLines: 0,
  },
  {
    name: "diff-no-final-newline",
    operation: "diff",
    upstreamCase: `${editDiffSource}:generateDiffString no final newline`,
    oldText: "one\ntwo",
    newText: "one\nTWO",
    contextLines: 4,
  },
  {
    name: "diff-large-multi-edit-gap",
    operation: "edit-pipeline",
    upstreamCase: `${toolsTest}:edit tool/collapse large unchanged gaps`,
    path: "edit-multi-large-gap.txt",
    content: `${Array.from({ length: 600 }, (_, index) => `line ${String(index + 1).padStart(3, "0")}`).join("\n")}\n`,
    edits: [
      { oldText: "line 100\n", newText: "LINE 100\n" },
      { oldText: "line 300\n", newText: "LINE 300\n" },
      { oldText: "line 500\n", newText: "LINE 500\n" },
    ],
  },
  {
    name: "patch-simple-single-line",
    operation: "patch",
    upstreamCase: `${toolsTest}:edit tool/replace text in file patch`,
    path: "edit-test.txt",
    oldText: "Hello, world!",
    newText: "Hello, testing!",
    contextLines: 4,
  },
  {
    name: "patch-multiple-hunks",
    operation: "patch",
    upstreamCase: `${toolsTest}:edit tool/fuzzy untouched lines applicable patch`,
    path: "multi.txt",
    oldText: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n",
    newText: "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN\n",
    contextLines: 1,
  },
  {
    name: "patch-no-final-newline",
    operation: "patch",
    upstreamCase: `${editDiffSource}:generateUnifiedPatch no final newline`,
    path: "no-final-newline.txt",
    oldText: "one\ntwo",
    newText: "one\nTWO",
    contextLines: 4,
  },
  {
    name: "patch-zero-context",
    operation: "patch",
    upstreamCase: `${editDiffSource}:generateUnifiedPatch zero context`,
    path: "zero-context.txt",
    oldText: "one\ntwo\nthree\n",
    newText: "one\nTWO\nthree\n",
    contextLines: 0,
  },
  {
    name: "patch-no-changes-keeps-file-headers",
    operation: "patch",
    upstreamCase: `${editDiffSource}:generateUnifiedPatch no changes`,
    path: "unchanged.txt",
    oldText: "same\ncontent\n",
    newText: "same\ncontent\n",
    contextLines: 4,
  },
];

const cases = [...basicCases, ...pipelineCases, ...diffCases];

function requireString(value: string | undefined, field: string, fixtureCase: FixtureCase): string {
  if (value === undefined) throw new Error(`${fixtureCase.name}: missing ${field}`);
  return value;
}

function requireEdits(fixtureCase: FixtureCase): Edit[] {
  if (!fixtureCase.edits) throw new Error(`${fixtureCase.name}: missing edits`);
  return fixtureCase.edits;
}

function executeCase(editDiff: EditDiffModule, fixtureCase: FixtureCase): unknown {
  switch (fixtureCase.operation) {
    case "detect-line-ending":
      return editDiff.detectLineEnding(requireString(fixtureCase.content, "content", fixtureCase));
    case "normalize-lf":
      return editDiff.normalizeToLF(requireString(fixtureCase.content, "content", fixtureCase));
    case "restore-line-endings":
      return editDiff.restoreLineEndings(
        requireString(fixtureCase.content, "content", fixtureCase),
        fixtureCase.ending ?? "\n",
      );
    case "strip-bom":
      return editDiff.stripBom(requireString(fixtureCase.content, "content", fixtureCase));
    case "normalize-fuzzy":
      return editDiff.normalizeForFuzzyMatch(requireString(fixtureCase.content, "content", fixtureCase));
    case "fuzzy-find":
      return editDiff.fuzzyFindText(
        requireString(fixtureCase.content, "content", fixtureCase),
        requireString(fixtureCase.oldText, "oldText", fixtureCase),
      );
    case "apply":
      return editDiff.applyEditsToNormalizedContent(
        requireString(fixtureCase.content, "content", fixtureCase),
        requireEdits(fixtureCase),
        requireString(fixtureCase.path, "path", fixtureCase),
      );
    case "edit-pipeline": {
      const rawContent = requireString(fixtureCase.content, "content", fixtureCase);
      const pathValue = requireString(fixtureCase.path, "path", fixtureCase);
      const { bom, text } = editDiff.stripBom(rawContent);
      const ending = editDiff.detectLineEnding(text);
      const normalizedContent = editDiff.normalizeToLF(text);
      const applied = editDiff.applyEditsToNormalizedContent(normalizedContent, requireEdits(fixtureCase), pathValue);
      const generatedDiff = editDiff.generateDiffString(applied.baseContent, applied.newContent);
      return {
        finalContent: bom + editDiff.restoreLineEndings(applied.newContent, ending),
        baseContent: applied.baseContent,
        newContent: applied.newContent,
        diff: generatedDiff.diff,
        ...(generatedDiff.firstChangedLine === undefined ? {} : { firstChangedLine: generatedDiff.firstChangedLine }),
        patch: editDiff.generateUnifiedPatch(pathValue, applied.baseContent, applied.newContent),
      };
    }
    case "diff":
      return editDiff.generateDiffString(
        requireString(fixtureCase.oldText, "oldText", fixtureCase),
        requireString(fixtureCase.newText, "newText", fixtureCase),
        fixtureCase.contextLines,
      );
    case "patch":
      return editDiff.generateUnifiedPatch(
        requireString(fixtureCase.path, "path", fixtureCase),
        requireString(fixtureCase.oldText, "oldText", fixtureCase),
        requireString(fixtureCase.newText, "newText", fixtureCase),
        fixtureCase.contextLines,
      );
  }
}

export async function generateF4(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const moduleURL = pathToFileURL(path.join(upstreamRoot, editDiffSource)).href;
  const editDiff = (await import(moduleURL)) as EditDiffModule;
  const generated = cases.map((fixtureCase) => {
    try {
      return { ...fixtureCase, expected: executeCase(editDiff, fixtureCase) };
    } catch (error) {
      return { ...fixtureCase, expectedError: error instanceof Error ? error.message : String(error) };
    }
  });

  const familyDir = path.join(outputRoot, "F4");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F4",
    upstreamCommit,
    generator: "conformance/extract/f4-edit.ts",
    source: editDiffSource,
    files: ["cases.json"],
  };

  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(
    path.join(familyDir, "cases.json"),
    `${JSON.stringify({ schemaVersion: 1, cases: generated }, null, 2)}\n`,
  );
}
