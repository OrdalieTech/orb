// F12 extension (WP-420): drives upstream Editor, Input, SelectList,
// SettingsList, fuzzy, and word-navigation through scripted operations and
// records observations the Go port must reproduce.
//
// Public cursor and helper indices remain the upstream UTF-16 offsets. The Go
// runner converts only at its private storage boundary.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

const dim = (s: string) => `\u001b[2m${s}\u001b[22m`;
const bold = (s: string) => `\u001b[1m${s}\u001b[22m`;

const selectListTheme = {
  selectedPrefix: (s: string) => s,
  selectedText: bold,
  description: dim,
  scrollInfo: dim,
  noMatch: dim,
};
const editorTheme = { borderColor: dim, selectList: selectListTheme };
const settingsTheme = {
  label: (t: string, sel: boolean) => (sel ? bold(t) : t),
  value: (t: string, sel: boolean) => (sel ? bold(t) : dim(t)),
  description: dim,
  cursor: "→ ",
  hint: dim,
};

async function flush(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await Promise.resolve();
    await new Promise((resolve) => setImmediate(resolve));
  }
}

// ---------------------------------------------------------------------------
// Case definitions (shared shapes with the Go runner)
// ---------------------------------------------------------------------------

type EditorOp =
  | { do: "input"; data: string }
  | { do: "setText"; text: string }
  | { do: "insertText"; text: string }
  | { do: "addHistory"; text: string }
  | { do: "setPaddingX"; value: number }
  | { do: "focus" }
  | { do: "text" }
  | { do: "cursor" }
  | { do: "expanded" }
  | { do: "showing" }
  | { do: "render"; width: number };

type ProviderSpec = {
  commands?: { name: string; description?: string; argumentHint?: string; argumentCompletions?: { value: string; label: string; description?: string }[] }[];
  files?: { dirs?: string[]; files?: string[] };
};

type EditorCase = { name: string; rows?: number; provider?: ProviderSpec; ops: EditorOp[] };

const up = "\u001b[A";
const down = "\u001b[B";
const right = "\u001b[C";
const left = "\u001b[D";
const ctrlLeft = "\u001b[1;5D";
const ctrlRight = "\u001b[1;5C";

const repeat = (op: EditorOp, count: number): EditorOp[] => Array.from({ length: count }, () => op);

const bigPaste = "line\n".repeat(20).trimEnd();

const editorCases: EditorCase[] = [
  {
    name: "history-navigation",
    ops: [
      { do: "addHistory", text: "first" },
      { do: "addHistory", text: "second" },
      { do: "addHistory", text: "third" },
      { do: "input", data: up }, { do: "text" },
      { do: "input", data: up }, { do: "text" },
      { do: "input", data: up }, { do: "text" },
      { do: "input", data: up }, { do: "text" },
      { do: "input", data: down }, { do: "text" }, { do: "cursor" },
    ],
  },
  {
    name: "history-draft-restore",
    ops: [
      { do: "addHistory", text: "prompt" },
      { do: "setText", text: "draft" },
      { do: "input", data: left }, { do: "input", data: left },
      { do: "input", data: up }, { do: "text" }, { do: "cursor" },
      { do: "input", data: up }, { do: "text" },
      { do: "input", data: down }, { do: "text" }, { do: "cursor" },
    ],
  },
  {
    name: "backslash-enter",
    ops: [
      { do: "input", data: "a" }, { do: "input", data: "\\" },
      { do: "input", data: "\r" }, { do: "text" }, { do: "cursor" },
      { do: "input", data: "b" }, { do: "input", data: "\r" }, { do: "text" },
    ],
  },
  {
    name: "unicode-editing",
    ops: [
      { do: "input", data: "ä" }, { do: "input", data: "ö" }, { do: "input", data: "ü" },
      { do: "input", data: "\u007f" }, { do: "text" },
      { do: "input", data: left }, { do: "input", data: "x" }, { do: "text" }, { do: "cursor" },
      { do: "setText", text: "" },
      { do: "input", data: "😀" }, { do: "input", data: "👍" }, { do: "input", data: "🎉" },
      { do: "input", data: left }, { do: "input", data: left }, { do: "input", data: "y" },
      { do: "text" }, { do: "cursor" },
      { do: "input", data: "\u007f" }, { do: "text" },
    ],
  },
  {
    name: "cjk-word-movement",
    ops: [
      { do: "setText", text: "你好，世界" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlRight }, { do: "cursor" },
      { do: "input", data: ctrlRight }, { do: "cursor" },
      { do: "input", data: ctrlRight }, { do: "cursor" },
      { do: "setText", text: "hello你好，world世界" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlLeft }, { do: "cursor" },
      { do: "input", data: ctrlRight }, { do: "cursor" },
      { do: "input", data: ctrlRight }, { do: "cursor" },
    ],
  },
  {
    name: "kill-ring",
    ops: [
      { do: "setText", text: "foo bar baz" },
      { do: "input", data: "\u0017" }, { do: "text" },
      { do: "input", data: "\u0001" }, { do: "input", data: "\u0019" }, { do: "text" },
      { do: "setText", text: "one two three" },
      { do: "input", data: "\u0017" }, { do: "input", data: "\u0017" }, { do: "input", data: "\u0017" },
      { do: "text" },
      { do: "input", data: "\u0019" }, { do: "text" },
      { do: "setText", text: "first" }, { do: "input", data: "\u0017" },
      { do: "setText", text: "second" }, { do: "input", data: "\u0017" },
      { do: "setText", text: "" },
      { do: "input", data: "\u0019" }, { do: "text" },
      { do: "input", data: "\u001by" }, { do: "text" },
    ],
  },
  {
    name: "undo-coalescing",
    ops: [
      ...["h", "e", "l", "l", "o", " ", "w", "o", "r", "l", "d"].map((c) => ({ do: "input", data: c }) as EditorOp),
      { do: "text" },
      { do: "input", data: "\u001f" }, { do: "text" },
      { do: "input", data: "\u001f" }, { do: "text" },
    ],
  },
  {
    name: "paste-collapse-lines",
    ops: [
      { do: "input", data: `\u001b[200~${bigPaste}\u001b[201~` },
      { do: "text" }, { do: "cursor" }, { do: "expanded" },
      { do: "input", data: "\u007f" }, { do: "text" },
      { do: "input", data: "\u001f" }, { do: "text" },
    ],
  },
  {
    name: "paste-collapse-chars",
    ops: [
      { do: "input", data: `\u001b[200~${"x".repeat(1001)}\u001b[201~` },
      { do: "text" },
    ],
  },
  {
    name: "paste-marker-navigation",
    ops: [
      { do: "input", data: "A" },
      { do: "input", data: `\u001b[200~${bigPaste}\u001b[201~` },
      { do: "input", data: "B" },
      { do: "input", data: "\u0001" }, { do: "cursor" },
      { do: "input", data: right }, { do: "cursor" },
      { do: "input", data: right }, { do: "cursor" },
      { do: "input", data: right }, { do: "cursor" },
      { do: "input", data: left }, { do: "cursor" },
      { do: "input", data: left }, { do: "cursor" },
    ],
  },
  {
    name: "sticky-column",
    ops: [
      { do: "setText", text: "2222222222x222\n\n1111111111_111111111111" },
      { do: "cursor" },
      { do: "input", data: "\u0001" },
      ...repeat({ do: "input", data: right }, 10),
      { do: "cursor" },
      { do: "input", data: up }, { do: "cursor" },
      { do: "input", data: up }, { do: "cursor" },
    ],
  },
  {
    name: "vertical-astral-snap",
    ops: [
      { do: "setText", text: "😀x\nab" },
      { do: "input", data: "\u0001" },
      { do: "input", data: right },
      { do: "input", data: up },
      { do: "cursor" },
    ],
  },
  {
    name: "jump-to-char",
    ops: [
      { do: "setText", text: "hello world" },
      { do: "input", data: "\u0001" },
      { do: "input", data: "\u001d" }, { do: "input", data: "o" }, { do: "cursor" },
      { do: "input", data: "\u001d" }, { do: "input", data: "o" }, { do: "cursor" },
      { do: "input", data: "\u0005" },
      { do: "input", data: "\u001b\u001d" }, { do: "input", data: "l" }, { do: "cursor" },
    ],
  },
  {
    name: "render-empty-focused",
    ops: [{ do: "focus" }, { do: "render", width: 12 }],
  },
  {
    name: "render-basic-text",
    ops: [
      { do: "setText", text: "hello" },
      { do: "focus" },
      { do: "render", width: 12 },
      { do: "input", data: left },
      { do: "render", width: 12 },
    ],
  },
  {
    name: "render-wrap-cjk",
    ops: [
      { do: "setText", text: "こんにちは世界こんにちは世界" },
      { do: "render", width: 12 },
    ],
  },
  {
    name: "render-wrap-emoji",
    ops: [
      { do: "setText", text: "emoji 😀😀😀 wrap boundary test" },
      { do: "render", width: 12 },
    ],
  },
  {
    name: "render-padding",
    ops: [
      { do: "setText", text: "padded" },
      { do: "setPaddingX", value: 2 },
      { do: "focus" },
      { do: "render", width: 16 },
    ],
  },
  {
    name: "render-scroll-indicators",
    ops: [
      { do: "setText", text: "line\n".repeat(30).trimEnd() },
      { do: "input", data: up },
      { do: "render", width: 20 },
    ],
  },
  {
    name: "render-word-wrap",
    ops: [
      { do: "setText", text: "Hello world this is a test of word wrapping functionality" },
      { do: "render", width: 24 },
    ],
  },
  {
    name: "slash-autocomplete",
    provider: {
      commands: [
        { name: "help", description: "Show help" },
        { name: "hotkeys", description: "Show hotkeys", argumentHint: "<name>" },
        { name: "clear", description: "Clear the session" },
      ],
    },
    ops: [
      { do: "input", data: "/" }, { do: "showing" },
      { do: "render", width: 60 },
      { do: "input", data: "h" }, { do: "showing" },
      { do: "render", width: 60 },
      { do: "input", data: "\u001b[B" },
      { do: "render", width: 60 },
      { do: "input", data: "\t" }, { do: "text" }, { do: "cursor" }, { do: "showing" },
    ],
  },
  {
    name: "slash-confirm-submits",
    provider: { commands: [{ name: "help", description: "Show help" }] },
    ops: [
      { do: "input", data: "/" }, { do: "input", data: "h" }, { do: "showing" },
      { do: "input", data: "\r" }, { do: "text" }, { do: "showing" },
    ],
  },
  {
    name: "slash-argument-completion",
    provider: {
      commands: [
        {
          name: "model",
          description: "Pick model",
          argumentCompletions: [
            { value: "gpt-4o", label: "gpt-4o" },
            { value: "gpt-4o-mini", label: "gpt-4o-mini" },
            { value: "o1", label: "o1" },
          ],
        },
      ],
    },
    ops: [
      { do: "input", data: "/" }, { do: "input", data: "m" },
      { do: "input", data: "\t" }, { do: "showing" },
      { do: "input", data: "\t" }, { do: "text" }, { do: "showing" },
      { do: "input", data: "g" }, { do: "text" }, { do: "showing" },
      { do: "render", width: 60 },
      { do: "input", data: "\t" }, { do: "text" }, { do: "showing" },
    ],
  },
  {
    name: "force-file-completion",
    provider: { files: { dirs: ["src"], files: ["update.sh", "utils.ts", "src/index.ts"] } },
    ops: [
      { do: "input", data: "." }, { do: "input", data: "/" }, { do: "input", data: "u" }, { do: "input", data: "p" },
      { do: "input", data: "\t" }, { do: "text" }, { do: "showing" },
      { do: "setText", text: "" },
      { do: "input", data: "u" }, { do: "input", data: "t" },
      { do: "input", data: "\t" }, { do: "text" }, { do: "showing" },
    ],
  },
  {
    name: "force-file-menu",
    provider: { files: { dirs: ["src", "dist"], files: ["readme.md", "src/a.ts", "dist/b.js"] } },
    ops: [
      { do: "input", data: "\t" }, { do: "showing" },
      { do: "render", width: 60 },
      { do: "input", data: "\u001b" }, { do: "showing" },
    ],
  },
];

type InputOp =
  | { do: "input"; data: string }
  | { do: "setValue"; text: string }
  | { do: "focus" }
  | { do: "value" }
  | { do: "cursor" }
  | { do: "render"; width: number };

type InputCase = { name: string; ops: InputOp[] };

const inputCases: InputCase[] = [
  {
    name: "typing-and-kill-ring",
    ops: [
      { do: "setValue", text: "foo bar baz" },
      { do: "input", data: "\u0005" },
      { do: "input", data: "\u0017" }, { do: "value" },
      { do: "input", data: "\u0001" }, { do: "input", data: "\u0019" }, { do: "value" }, { do: "cursor" },
    ],
  },
  {
    name: "punctuation-boundary",
    ops: [
      { do: "setValue", text: "foo.bar" },
      { do: "input", data: "\u0005" },
      { do: "input", data: "\u0017" }, { do: "value" },
    ],
  },
  {
    name: "undo-coalescing",
    ops: [
      { do: "input", data: "h" }, { do: "input", data: "i" }, { do: "input", data: " " }, { do: "input", data: "y" }, { do: "input", data: "o" },
      { do: "value" },
      { do: "input", data: "\u001f" }, { do: "value" },
      { do: "input", data: "\u001f" }, { do: "value" },
    ],
  },
  {
    name: "paste-strips-newlines",
    ops: [
      { do: "input", data: "\u001b[200~line1\nline2\r\nline3\ttab\u001b[201~" },
      { do: "value" }, { do: "cursor" },
    ],
  },
  {
    name: "render-plain",
    ops: [
      { do: "setValue", text: "hello" },
      { do: "focus" },
      { do: "input", data: "\u0005" },
      { do: "render", width: 20 },
      { do: "input", data: left },
      { do: "render", width: 20 },
    ],
  },
  {
    name: "render-scrolled-cjk",
    ops: [
      { do: "setValue", text: "가나다라마바사아자차카타파하" },
      { do: "focus" },
      { do: "input", data: "\u0001" },
      ...repeat({ do: "input", data: right } as InputOp, 5) as InputOp[],
      { do: "render", width: 20 },
    ],
  },
  {
    name: "render-scrolled-japanese",
    ops: [
      { do: "setValue", text: "こんにちは世界こんにちは世界" },
      { do: "focus" },
      { do: "input", data: "\u0001" },
      ...repeat({ do: "input", data: right } as InputOp, 5) as InputOp[],
      { do: "render", width: 20 },
    ],
  },
  {
    name: "render-scrolled-chinese",
    ops: [
      { do: "setValue", text: "你好世界你好世界你好世界" },
      { do: "focus" },
      { do: "input", data: "\u0001" },
      ...repeat({ do: "input", data: right } as InputOp, 5) as InputOp[],
      { do: "render", width: 20 },
    ],
  },
  {
    name: "unicode-word-delete-backward",
    ops: [
      { do: "setValue", text: "你好世界。你好，世界" },
      { do: "input", data: "\u0005" },
      { do: "input", data: "\u0017" }, { do: "value" },
      { do: "input", data: "\u0017" }, { do: "value" },
      { do: "input", data: "\u0017" }, { do: "value" },
      { do: "input", data: "\u0017" }, { do: "value" },
      { do: "input", data: "\u0017" }, { do: "value" },
      { do: "input", data: "\u0017" }, { do: "value" },
    ],
  },
  {
    name: "unicode-word-delete-forward",
    ops: [
      { do: "setValue", text: "你好世界。你好，世界" },
      { do: "input", data: "\u0001" },
      { do: "input", data: "\u001bd" }, { do: "value" },
      { do: "input", data: "\u001bd" }, { do: "value" },
      { do: "input", data: "\u001bd" }, { do: "value" },
      { do: "input", data: "\u001bd" }, { do: "value" },
      { do: "input", data: "\u001bd" }, { do: "value" },
      { do: "input", data: "\u001bd" }, { do: "value" },
    ],
  },
];

type SelectOp =
  | { do: "input"; data: string }
  | { do: "setFilter"; text: string }
  | { do: "setSelectedIndex"; value: number }
  | { do: "selected" }
  | { do: "render"; width: number };

type SelectCase = {
  name: string;
  items: { value: string; label?: string; description?: string }[];
  maxVisible: number;
  layout?: { minPrimaryColumnWidth?: number; maxPrimaryColumnWidth?: number };
  ops: SelectOp[];
};

const selectCases: SelectCase[] = [
  {
    name: "descriptions-and-alignment",
    items: [
      { value: "short", label: "short", description: "short description" },
      { value: "very-long-command-name-that-needs-truncation", label: "very-long-command-name-that-needs-truncation", description: "long description" },
    ],
    maxVisible: 5,
    ops: [{ do: "render", width: 80 }, { do: "render", width: 30 }],
  },
  {
    name: "multiline-description-normalized",
    items: [{ value: "test", label: "test", description: "Line one\nLine two\nLine three" }],
    maxVisible: 5,
    ops: [{ do: "render", width: 100 }],
  },
  {
    name: "layout-bounds",
    items: [
      { value: "a", label: "a", description: "first" },
      { value: "bb", label: "bb", description: "second" },
    ],
    maxVisible: 5,
    layout: { minPrimaryColumnWidth: 12, maxPrimaryColumnWidth: 20 },
    ops: [{ do: "render", width: 80 }],
  },
  {
    name: "navigation-wrap-and-scroll",
    items: [{ value: "one" }, { value: "two" }, { value: "three" }, { value: "four" }],
    maxVisible: 2,
    ops: [
      { do: "render", width: 40 },
      { do: "input", data: up }, { do: "selected" },
      { do: "render", width: 40 },
      { do: "input", data: down }, { do: "selected" },
      { do: "input", data: down }, { do: "selected" },
      { do: "render", width: 40 },
    ],
  },
  {
    name: "filtering",
    items: [{ value: "alpha" }, { value: "beta" }, { value: "alp" }],
    maxVisible: 5,
    ops: [
      { do: "setFilter", text: "al" },
      { do: "selected" },
      { do: "render", width: 40 },
      { do: "setFilter", text: "zzz" },
      { do: "render", width: 40 },
    ],
  },
  {
    name: "wide-labels",
    items: [
      { value: "命令一", label: "命令一", description: "第一个命令的描述" },
      { value: "命令二", label: "命令二", description: "第二个命令的描述" },
    ],
    maxVisible: 5,
    ops: [{ do: "render", width: 60 }, { do: "input", data: down }, { do: "render", width: 60 }],
  },
];

type SettingsOp =
  | { do: "input"; data: string }
  | { do: "updateValue"; id: string; value: string }
  | { do: "render"; width: number };

type SettingsCase = {
  name: string;
  items: { id: string; label: string; description?: string; currentValue: string; values?: string[] }[];
  maxVisible: number;
  enableSearch?: boolean;
  ops: SettingsOp[];
};

const settingsCases: SettingsCase[] = [
  {
    name: "cycle-values",
    items: [
      { id: "theme", label: "Theme", currentValue: "dark", values: ["dark", "light"], description: "Color theme" },
      { id: "sound", label: "Sound", currentValue: "on", values: ["on", "off"] },
    ],
    maxVisible: 5,
    ops: [
      { do: "render", width: 60 },
      { do: "input", data: "\r" },
      { do: "render", width: 60 },
      { do: "input", data: " " },
      { do: "render", width: 60 },
      { do: "input", data: down },
      { do: "render", width: 60 },
    ],
  },
  {
    name: "scroll-and-description",
    items: [
      { id: "a", label: "Alpha", currentValue: "1", description: "A longer description that wraps when the settings list is rendered narrow" },
      { id: "b", label: "Beta", currentValue: "2" },
      { id: "c", label: "Gamma", currentValue: "3" },
    ],
    maxVisible: 2,
    ops: [{ do: "render", width: 48 }, { do: "input", data: down }, { do: "render", width: 48 }],
  },
  {
    name: "search",
    items: [
      { id: "theme", label: "Theme", currentValue: "dark" },
      { id: "model", label: "Model", currentValue: "gpt" },
    ],
    maxVisible: 5,
    enableSearch: true,
    ops: [
      { do: "render", width: 60 },
      { do: "input", data: "m" },
      { do: "input", data: "o" },
      { do: "render", width: 60 },
      { do: "input", data: "z" },
      { do: "render", width: 60 },
    ],
  },
  {
    name: "update-value",
    items: [{ id: "x", label: "X", currentValue: "old" }],
    maxVisible: 5,
    ops: [
      { do: "updateValue", id: "x", value: "new" },
      { do: "render", width: 40 },
    ],
  },
];

// Word-wrap chunk cases (from upstream editor.test.ts "Word wrapping").
const wordWrapCases: { line: string; width: number }[] = [
  { line: "hello world test", width: 11 },
  { line: "hello world test", width: 12 },
  { line: "aaaaaaaaaaaa aaaa", width: 12 },
  { line: "      aaaaaaaaaaaa", width: 12 },
  { line: "Lorem ipsum dolor sit amet,    consectetur", width: 30 },
  { line: "Lorem ipsum dolor sit amet,              consectetur", width: 30 },
  { line: "Lorem ipsum dolor sit amet,               consectetur", width: 30 },
  { line: "Lorem ipsum dolor sit amet,                         consectetur", width: 30 },
  { line: "Lorem ipsum dolor sit amet,                          consectetur", width: 30 },
  { line: "Lorem ipsum dolor sit amet,                                     consectetur", width: 30 },
  { line: ` ${"a".repeat(186)}你`, width: 187 },
  { line: "Check https://example.com/very/long/path here", width: 30 },
  { line: "你好世界你好世界你好世界", width: 8 },
  { line: "mixed 你好 and ascii text", width: 10 },
  { line: "emoji 😀😀😀 wrap", width: 8 },
];

// Fuzzy cases (from upstream fuzzy.test.ts).
const fuzzyMatchCases: { query: string; text: string }[] = [
  { query: "", text: "anything" },
  { query: "toolong", text: "abc" },
  { query: "abc", text: "abc" },
  { query: "cba", text: "abc" },
  { query: "ABC", text: "abc" },
  { query: "abc", text: "ABC" },
  { query: "abc", text: "abcdef" },
  { query: "abc", text: "axbxcx" },
  { query: "fb", text: "foo-bar" },
  { query: "fb", text: "xxfxxbxx" },
  { query: "o1", text: "1o" },
  { query: "4o", text: "o4-mini" },
  { query: "gpt4", text: "gpt-4o-mini" },
  { query: "cld", text: "claude-sonnet" },
  { query: "a", text: "😀a" },
  { query: "😀", text: "x😀" },
  { query: "i̇", text: "İ" },
];

const fuzzyFilterCases: { items: string[]; query: string }[] = [
  { items: ["apple", "banana", "cherry"], query: "an" },
  { items: ["xtestx", "test", "tempest"], query: "test" },
  { items: ["openai/gpt-4", "anthropic/claude"], query: "open/gpt" },
  { items: ["alpha", "beta", "gamma"], query: "" },
  { items: ["settings-list", "select-list", "editor"], query: "list" },
];

// Word-navigation corpus, including dictionary-backed CJK cases.
const wordNavCases: { text: string; cursor: number }[] = [
  { text: "hello world", cursor: 11 },
  { text: "hello world", cursor: 6 },
  { text: "hello world", cursor: 0 },
  { text: "hello world", cursor: 5 },
  { text: "foo.bar", cursor: 7 },
  { text: "foo.bar", cursor: 4 },
  { text: "foo.bar", cursor: 3 },
  { text: "foo.bar", cursor: 0 },
  { text: "foo:bar", cursor: 7 },
  { text: "foo:bar", cursor: 4 },
  { text: "path/to/file", cursor: 12 },
  { text: "path/to/file", cursor: 8 },
  { text: "path/to/file", cursor: 7 },
  { text: "path/to/file", cursor: 5 },
  { text: "path/to/file", cursor: 4 },
  { text: "path/to/file", cursor: 0 },
  { text: "  hello  ", cursor: 9 },
  { text: "  hello  ", cursor: 2 },
  { text: "  hello  ", cursor: 0 },
  { text: "  hello  ", cursor: 7 },
  { text: "foo...bar", cursor: 9 },
  { text: "foo...bar", cursor: 6 },
  { text: "foo...bar", cursor: 3 },
  { text: "foo...bar", cursor: 0 },
  { text: "你好，世界", cursor: 5 },
  { text: "你好，世界", cursor: 3 },
  { text: "你好，世界", cursor: 2 },
  { text: "你好，世界", cursor: 0 },
  { text: "你好世界 test", cursor: 5 },
  { text: "你好世界 test", cursor: 0 },
  { text: "々!a", cursor: 0 },
  { text: "⺀々!a", cursor: 0 },
  { text: "⺀々〻ﾟ!a", cursor: 0 },
  { text: "ﾟﾟ!a", cursor: 0 },
  { text: "ﾟ漢!a", cursor: 0 },
  { text: "々ﾟ々!a", cursor: 0 },
  { text: "゛ﾟカ!a", cursor: 0 },
  { text: "々́!a", cursor: 0 },
  { text: "⺀ﾞﾟ!日本", cursor: 0 },
  { text: "日本ﾞﾟ!a", cursor: 1 },
  { text: "ｶﾞｸｾｲ!a", cursor: 0 },
  { text: "漢゛字漢゜字!a", cursor: 3 },
  // ICU 78.2 dictionary ranges at every UTF-16 cursor offset: Chinese
  // ambiguity, Japanese mixed scripts, NFKC forms, ignored characters, and
  // supplementary Han indices. Keep the focused boundary cases above as
  // regression witnesses alongside this broader generated corpus.
  ...[
    "中华人民共和国", "北京大学生前来应聘", "研究生命起源", "商品和服务", "国际化与本地化",
    "私は学生です", "東京都に行きます", "すももももももものうち", "カタカナテスト",
    "コンピューターサイエンス", "ひらがなカタカナ漢字", "龟山岛测试", "𠀀𠀁测试",
    "𮯰𮯱世界", "𲎰𲎱世界",
    "你好abc世界", "你好・世界", "你好ー世界", "ﾃｽﾄ世界", "がくせい", "ｶﾞｸｾｲ",
    "你好‍世界", "你︀好世界", "日々是好日", "申し込みます", "おはようございます",
    "ソフトウェアエンジニア", "東京2026オリンピック",
  ].flatMap((text) => Array.from({ length: text.length + 1 }, (_, cursor) => ({ text, cursor }))),
];

// ---------------------------------------------------------------------------
// Execution against upstream implementations
// ---------------------------------------------------------------------------

function setupProviderFiles(spec: ProviderSpec | undefined): { baseDir: string; cleanup: () => void } {
  const baseDir = mkdtempSync(path.join(tmpdir(), "pi-f12-"));
  for (const dir of spec?.files?.dirs ?? []) {
    mkdirSync(path.join(baseDir, dir), { recursive: true });
  }
  for (const file of spec?.files?.files ?? []) {
    mkdirSync(path.dirname(path.join(baseDir, file)), { recursive: true });
    writeFileSync(path.join(baseDir, file), "content\n");
  }
  return { baseDir, cleanup: () => rmSync(baseDir, { recursive: true, force: true }) };
}

export async function generateF12Components(upstreamRoot: string, outputRoot: string): Promise<string[]> {
  const load = async (rel: string) => import(pathToFileURL(path.join(upstreamRoot, rel)).href);
  const editorModule = await load("packages/tui/src/components/editor.ts");
  const inputModule = await load("packages/tui/src/components/input.ts");
  const selectModule = await load("packages/tui/src/components/select-list.ts");
  const settingsModule = await load("packages/tui/src/components/settings-list.ts");
  const fuzzyModule = await load("packages/tui/src/fuzzy.ts");
  const wordNavModule = await load("packages/tui/src/word-navigation.ts");
  const autocompleteModule = await load("packages/tui/src/autocomplete.ts");
  const tuiModule = await load("packages/tui/src/tui.ts");

  // Minimal Terminal stub: the editor only reads rows/columns and requests
  // renders (upstream tests use @xterm-backed VirtualTerminal, a dev dep the
  // extraction pipeline does not need).
  const stubTerminal = (columns: number, rows: number) => ({
    start() {}, stop() {}, async drainInput() {}, write() {},
    get columns() { return columns; }, get rows() { return rows; },
    get kittyProtocolActive() { return false; },
    moveBy() {}, hideCursor() {}, showCursor() {}, clearLine() {},
    clearFromCursor() {}, clearScreen() {}, setTitle() {}, setProgress() {},
  });

  const familyDir = path.join(outputRoot, "F12");
  mkdirSync(familyDir, { recursive: true });
  const writeFixture = (name: string, payload: unknown) =>
    writeFileSync(path.join(familyDir, name), `${JSON.stringify(payload, null, 2)}\n`);

  // Editor sessions.
  const editorResults = [] as unknown[];
  for (const editorCase of editorCases) {
    const tui = new tuiModule.TUI(stubTerminal(80, editorCase.rows ?? 24));
    const editor = new editorModule.Editor(tui, editorTheme);
    const observations: { kind: string; value: unknown }[] = [];
    editor.onSubmit = (text: string) => observations.push({ kind: "submit", value: text });

    let cleanup = () => {};
    if (editorCase.provider) {
      const { baseDir, cleanup: cleanupFiles } = setupProviderFiles(editorCase.provider);
      cleanup = cleanupFiles;
      const commands = (editorCase.provider.commands ?? []).map((command) => ({
        name: command.name,
        description: command.description,
        argumentHint: command.argumentHint,
        ...(command.argumentCompletions
          ? { getArgumentCompletions: () => command.argumentCompletions }
          : {}),
      }));
      editor.setAutocompleteProvider(new autocompleteModule.CombinedAutocompleteProvider(commands, baseDir, null));
    }

    for (const op of editorCase.ops) {
      switch (op.do) {
        case "input": editor.handleInput(op.data); break;
        case "setText": editor.setText(op.text); break;
        case "insertText": editor.insertTextAtCursor(op.text); break;
        case "addHistory": editor.addToHistory(op.text); break;
        case "setPaddingX": editor.setPaddingX(op.value); break;
        case "focus": editor.focused = true; break;
        case "text": observations.push({ kind: "text", value: editor.getText() }); break;
        case "expanded": observations.push({ kind: "expanded", value: editor.getExpandedText() }); break;
        case "showing": observations.push({ kind: "showing", value: editor.isShowingAutocomplete() }); break;
        case "cursor": {
          const cursor = editor.getCursor();
          observations.push({ kind: "cursor", value: cursor });
          break;
        }
        case "render": observations.push({ kind: "render", value: editor.render(op.width) }); break;
      }
      await flush();
    }
    cleanup();
    editorResults.push({ name: editorCase.name, rows: editorCase.rows ?? 24, provider: editorCase.provider ?? null, ops: editorCase.ops, observations });
  }
  writeFixture("editor.json", { schemaVersion: 1, cases: editorResults });

  // Input sessions.
  const inputResults = [] as unknown[];
  for (const inputCase of inputCases) {
    const input = new inputModule.Input();
    const observations: { kind: string; value: unknown }[] = [];
    input.onSubmit = (value: string) => observations.push({ kind: "submit", value });
    for (const op of inputCase.ops) {
      switch (op.do) {
        case "input": input.handleInput(op.data); break;
        case "setValue": input.setValue(op.text); break;
        case "focus": input.focused = true; break;
        case "value": observations.push({ kind: "value", value: input.getValue() }); break;
        case "cursor": {
          observations.push({ kind: "cursor", value: (input as unknown as { cursor: number }).cursor });
          break;
        }
        case "render": observations.push({ kind: "render", value: input.render(op.width) }); break;
      }
    }
    inputResults.push({ name: inputCase.name, ops: inputCase.ops, observations });
  }
  writeFixture("input.json", { schemaVersion: 1, cases: inputResults });

  // SelectList sessions.
  const selectResults = [] as unknown[];
  for (const selectCase of selectCases) {
    const list = new selectModule.SelectList(selectCase.items, selectCase.maxVisible, selectListTheme, selectCase.layout ?? {});
    const observations: { kind: string; value: unknown }[] = [];
    list.onSelect = (item: { value: string }) => observations.push({ kind: "select", value: item.value });
    list.onCancel = () => observations.push({ kind: "cancel", value: true });
    for (const op of selectCase.ops) {
      switch (op.do) {
        case "input": list.handleInput(op.data); break;
        case "setFilter": list.setFilter(op.text); break;
        case "setSelectedIndex": list.setSelectedIndex(op.value); break;
        case "selected": observations.push({ kind: "selected", value: list.getSelectedItem()?.value ?? null }); break;
        case "render": observations.push({ kind: "render", value: list.render(op.width) }); break;
      }
    }
    selectResults.push({ name: selectCase.name, items: selectCase.items, maxVisible: selectCase.maxVisible, layout: selectCase.layout ?? null, ops: selectCase.ops, observations });
  }
  writeFixture("select-list.json", { schemaVersion: 1, cases: selectResults });

  // SettingsList sessions.
  const settingsResults = [] as unknown[];
  for (const settingsCase of settingsCases) {
    const observations: { kind: string; value: unknown }[] = [];
    const list = new settingsModule.SettingsList(
      settingsCase.items.map((item) => ({ ...item })),
      settingsCase.maxVisible,
      settingsTheme,
      (id: string, value: string) => observations.push({ kind: "change", value: `${id}=${value}` }),
      () => observations.push({ kind: "cancel", value: true }),
      { enableSearch: settingsCase.enableSearch ?? false },
    );
    for (const op of settingsCase.ops) {
      switch (op.do) {
        case "input": list.handleInput(op.data); break;
        case "updateValue": list.updateValue(op.id, op.value); break;
        case "render": observations.push({ kind: "render", value: list.render(op.width) }); break;
      }
    }
    settingsResults.push({ name: settingsCase.name, items: settingsCase.items, maxVisible: settingsCase.maxVisible, enableSearch: settingsCase.enableSearch ?? false, ops: settingsCase.ops, observations });
  }
  writeFixture("settings-list.json", { schemaVersion: 1, cases: settingsResults });

  // Word-wrap chunks retain upstream UTF-16 indices.
  const wordWrapResults = wordWrapCases.map(({ line, width }) => ({
    line,
    width,
    chunks: editorModule.wordWrapLine(line, width).map((chunk: { text: string; startIndex: number; endIndex: number }) => ({
      text: chunk.text,
      startIndex: chunk.startIndex,
      endIndex: chunk.endIndex,
    })),
  }));
  writeFixture("word-wrap.json", { schemaVersion: 1, cases: wordWrapResults });

  // Fuzzy matching.
  writeFixture("fuzzy.json", {
    schemaVersion: 1,
    matches: fuzzyMatchCases.map(({ query, text }) => {
      const result = fuzzyModule.fuzzyMatch(query, text);
      return { query, text, matches: result.matches, score: result.score };
    }),
    filters: fuzzyFilterCases.map(({ items, query }) => ({
      items,
      query,
      result: fuzzyModule.fuzzyFilter(items, query, (item: string) => item),
    })),
  });

  // Word navigation uses upstream UTF-16 cursor positions.
  writeFixture("word-navigation.json", {
    schemaVersion: 1,
    cases: wordNavCases.map(({ text, cursor }) => ({
      text,
      cursor,
      backward: wordNavModule.findWordBackward(text, cursor),
      forward: wordNavModule.findWordForward(text, cursor),
    })),
  });

  return [
    "editor.json",
    "input.json",
    "select-list.json",
    "settings-list.json",
    "word-wrap.json",
    "fuzzy.json",
    "word-navigation.json",
  ];
}
