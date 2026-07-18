import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withOfflineGeneratedCatalog } from "./f3-agent.ts";
import { generateF12Components } from "./f12-components.ts";
import { generateF12Core } from "./f12-core.ts";

type StyleName = "none" | "red" | "blue-bg" | "bracket";

type FixtureNode = {
  type: "text" | "truncated-text" | "spacer" | "container" | "box" | "loader";
  text?: string;
  message?: string;
  paddingX?: number;
  paddingY?: number;
  lines?: number;
  style?: StyleName;
  spinnerStyle?: StyleName;
  messageStyle?: StyleName;
  frames?: string[];
  children?: FixtureNode[];
};

type FixtureCase = { name: string; width: number; node: FixtureNode };

type FullScreenFixtureCase = {
  name: string;
  width: number;
  rows: number;
  user: string;
  assistant: string;
  editor: string;
  status: string;
};

type MarkdownFixtureCase = {
  name: string;
  text: string;
  width: number;
  paddingX?: number;
  paddingY?: number;
  defaultStyle?: "gray-italic" | "magenta" | "cyan" | "yellow-italic";
  preserveOrderedListMarkers?: boolean;
  preserveBackslashEscapes?: boolean;
  hyperlinks?: boolean;
};

type ThemeModule = {
  initTheme(name: string): void;
  theme: {
    getColorMode(): string;
    getFgAnsi(name: string): string;
    getBgAnsi(name: string): string;
    fg(name: string, value: string): string;
  };
  getMarkdownTheme(): Record<string, unknown>;
  highlightCode(code: string, language?: string): string[];
  getResolvedThemeColors(name: string): Record<string, string>;
  getThemeExportColors(name: string): Record<string, string | undefined>;
  loadThemeFromPath(path: string): unknown;
};

type ResourceDiagnostic = {
  type: string;
  message: string;
  path?: string;
  collision?: {
    resourceType: string;
    name: string;
    winnerPath: string;
    loserPath: string;
  };
};

type ResourceLoaderModule = {
  DefaultResourceLoader: new (options: {
    cwd: string;
    agentDir: string;
    additionalThemePaths?: string[];
    noExtensions?: boolean;
    noSkills?: boolean;
    noPromptTemplates?: boolean;
    noContextFiles?: boolean;
  }) => {
    reload(): Promise<void>;
    extendResources(paths: {
      themePaths: Array<{
        path: string;
        metadata: { source: string; scope: "temporary"; origin: "top-level" };
      }>;
    }): void;
    getThemes(): {
      themes: Array<{ name?: string; sourcePath?: string }>;
      diagnostics: ResourceDiagnostic[];
    };
  };
};

const foregroundThemeTokens = [
  "accent", "border", "borderAccent", "borderMuted", "success", "error", "warning", "muted", "dim", "text", "thinkingText",
  "userMessageText", "customMessageText", "customMessageLabel", "toolTitle", "toolOutput", "mdHeading", "mdLink", "mdLinkUrl", "mdCode",
  "mdCodeBlock", "mdCodeBlockBorder", "mdQuote", "mdQuoteBorder", "mdHr", "mdListBullet", "toolDiffAdded", "toolDiffRemoved", "toolDiffContext",
  "syntaxComment", "syntaxKeyword", "syntaxFunction", "syntaxVariable", "syntaxString", "syntaxNumber", "syntaxType", "syntaxOperator",
  "syntaxPunctuation", "thinkingOff", "thinkingMinimal", "thinkingLow", "thinkingMedium", "thinkingHigh", "thinkingXhigh", "thinkingMax", "bashMode",
];
const backgroundThemeTokens = ["selectedBg", "userMessageBg", "customMessageBg", "toolPendingBg", "toolSuccessBg", "toolErrorBg"];

const cases: FixtureCase[] = [
  { name: "text-wrap-padding", width: 8, node: { type: "text", text: "hello world", paddingX: 1, paddingY: 1 } },
  { name: "text-ansi-wrap", width: 5, node: { type: "text", text: "\u001b[31mred blue\u001b[0m", paddingX: 0, paddingY: 0 } },
  { name: "text-tab-cjk-emoji", width: 7, node: { type: "text", text: "A\t界🙂", paddingX: 0, paddingY: 0 } },
  { name: "text-whitespace-empty", width: 9, node: { type: "text", text: " \t ", paddingX: 1, paddingY: 1 } },
  { name: "truncated-long", width: 12, node: { type: "truncated-text", text: "first line that is long", paddingX: 1, paddingY: 1 } },
  { name: "truncated-styled", width: 13, node: { type: "truncated-text", text: "\u001b[31mstyled content beyond width\u001b[0m", paddingX: 1, paddingY: 0 } },
  { name: "truncated-first-line", width: 16, node: { type: "truncated-text", text: "first\nsecond", paddingX: 2, paddingY: 0 } },
  { name: "truncated-wide", width: 10, node: { type: "truncated-text", text: "🙂界🙂界🙂界", paddingX: 1, paddingY: 0 } },
  { name: "spacer", width: 20, node: { type: "spacer", lines: 3 } },
  {
    name: "container-order",
    width: 6,
    node: { type: "container", children: [{ type: "truncated-text", text: "one" }, { type: "spacer", lines: 1 }, { type: "truncated-text", text: "two" }] },
  },
  {
    name: "box-padding-background",
    width: 8,
    node: { type: "box", paddingX: 1, paddingY: 1, style: "blue-bg", children: [{ type: "truncated-text", text: "ok" }] },
  },
  {
    name: "box-multiple-children",
    width: 10,
    node: { type: "box", paddingX: 1, paddingY: 0, children: [{ type: "text", text: "alpha beta", paddingX: 0, paddingY: 0 }, { type: "truncated-text", text: "tail" }] },
  },
  { name: "loader-static", width: 20, node: { type: "loader", message: "Work", frames: ["*"], spinnerStyle: "red", messageStyle: "bracket" } },
  { name: "loader-hidden-indicator", width: 18, node: { type: "loader", message: "Waiting", frames: [], messageStyle: "bracket" } },
];

const fullScreenCases: FullScreenFixtureCase[] = [100, 72, 48, 32].map((width) => ({
  name: `chat-editor-status-${width}`,
  width,
  rows: width >= 72 ? 30 : 20,
  user: "Verify the full TUI frame at this width, including CJK and emoji rendering.",
  assistant: [
    "The replay uses the same deterministic component tree at every width.",
    "",
    "- Chinese: 你好世界",
    "- Japanese: 日本語テスト",
    "- Emoji: 👩🏽‍💻 ✅",
    "",
    "```go",
    "frame := root.Render(width)",
    "```",
  ].join("\n"),
  editor: "Reply with parity evidence: 你好世界 / 日本語 / 👩🏽‍💻",
  status: "gpt-5.1 • medium • 42% context • main",
}));

const markdownCases: MarkdownFixtureCase[] = [
  { name: "list-nested", text: "- Item 1\n  - Nested 1.1\n  - Nested 1.2\n- Item 2", width: 80 },
  { name: "list-deep", text: "- Level 1\n  - Level 2\n    - Level 3\n      - Level 4", width: 80 },
  { name: "list-ordered-nested", text: "1. First\n   1. Nested first\n   2. Nested second\n2. Second", width: 80 },
  { name: "list-normalize-markers", text: "1. alpha\n1. beta\n1. gamma", width: 80 },
  { name: "list-preserve-markers", text: "  4. forth\n  3. third\n\n10) ten\n7) seven\n\n+ plus\n* star\n- minus\n+", width: 80, preserveOrderedListMarkers: true },
  { name: "list-mixed", text: "1. Ordered item\n   - Unordered nested\n   - Another nested\n2. Second ordered\n   - More nested", width: 80 },
  { name: "list-loose", text: "1. Lorem ipsum dolor sit amet.\n\n   Ut enim ad minim veniam.\n\n2. Duis aute irure dolor.\n\n   Excepteur sint occaecat cupidatat.\n\n3. Beep boop", width: 80 },
  { name: "list-task", text: "- [ ] beep\n- [x] boop", width: 80 },
  { name: "list-code-between", text: "1. First item\n\n```typescript\n// code block\n```\n\n2. Second item\n\n```typescript\n// another code block\n```\n\n3. Third item", width: 80 },
  { name: "list-wrap-unordered", text: "- alpha beta gamma delta epsilon", width: 20 },
  { name: "list-wrap-ordered", text: "1. alpha beta gamma delta epsilon", width: 20 },
  { name: "list-wrap-multidigit", text: "10. alpha beta gamma delta epsilon", width: 21 },
  { name: "list-wrap-nested", text: "- parent\n  - alpha beta gamma delta epsilon", width: 24 },
  { name: "list-wrap-nested-ordered", text: "1. parent\n   - alpha beta gamma delta epsilon", width: 24 },
  { name: "list-blockquote", text: "- > alpha beta gamma delta epsilon zeta", width: 24 },
  { name: "list-code", text: "- ```ts\n  alpha beta gamma delta epsilon zeta\n  ```", width: 24 },
  { name: "table-simple", text: "| Name | Age |\n| --- | --- |\n| Alice | 30 |\n| Bob | 25 |", width: 80 },
  { name: "table-longest-word", text: "| Column One | Column Two |\n| --- | --- |\n| superlongword short | otherword |\n| small | tiny |", width: 32 },
  { name: "table-alignment", text: "| Left | Center | Right |\n| :--- | :---: | ---: |\n| A | B | C |\n| Long text | Middle | End |", width: 80 },
  { name: "table-varying", text: "| Short | Very long column header |\n| --- | --- |\n| A | This is a much longer cell content |\n| B | Short |", width: 80 },
  { name: "table-wrap", text: "| Command | Description | Example |\n| --- | --- | --- |\n| npm install | Install all dependencies | npm install |\n| npm run build | Build the project | npm run build |", width: 50 },
  { name: "table-long-cell", text: "| Header |\n| --- |\n| This is a very long cell content that should wrap |", width: 25 },
  { name: "table-long-url", text: "| Value |\n| --- |\n| prefix https://example.com/this/is/a/very/long/url/that/should/wrap |", width: 30 },
  { name: "table-inline-code", text: "| Code |\n| --- |\n| `averyveryveryverylongidentifier` |", width: 20 },
  { name: "table-narrow", text: "| A | B | C |\n| --- | --- | --- |\n| 1 | 2 | 3 |", width: 15 },
  { name: "table-natural", text: "| A | B |\n| --- | --- |\n| 1 | 2 |", width: 80 },
  { name: "table-padding", text: "| Column One | Column Two |\n| --- | --- |\n| Data 1 | Data 2 |", width: 40, paddingX: 2 },
  { name: "table-blockquote-style-context", text: "> | A |\n> | --- |\n> | before `code` after |", width: 40, defaultStyle: "magenta" },
  { name: "table-min-word-weight", text: "| A | B |\n| --- | --- |\n| aa aa aa aa aa | longword |", width: 15 },
  { name: "combined", text: "# Test Document\n\n- Item 1\n  - Nested item\n- Item 2\n\n| Col1 | Col2 |\n| --- | --- |\n| A | B |", width: 80 },
  { name: "escape-normalized", text: String.raw`"\"`, width: 80 },
  { name: "escape-preserved", text: String.raw`"\"`, width: 80, preserveBackslashEscapes: true },
  { name: "default-gray-code", text: "This is thinking with `inline code` and more text after", width: 80, paddingX: 1, defaultStyle: "gray-italic" },
  { name: "default-gray-bold", text: "This is thinking with **bold text** and more after", width: 80, paddingX: 1, defaultStyle: "gray-italic" },
  { name: "spacing-code", text: "hello world\n\n```js\nconst hello = \"world\";\n```\n\nagain, hello world", width: 80 },
  { name: "spacing-code-adjacent", text: "hello this is text\n```\ncode block\n```\nmore text", width: 80 },
  { name: "spacing-code-blank", text: "hello this is text\n\n```\ncode block\n```\n\nmore text", width: 80 },
  { name: "spacing-code-final", text: "hello world\n\n```js\nconst hello = 'world';\n```", width: 80 },
  { name: "spacing-divider", text: "hello world\n\n---\n\nagain, hello world", width: 80 },
  { name: "spacing-divider-final", text: "---", width: 80 },
  { name: "spacing-heading", text: "# Hello\n\nThis is a paragraph", width: 80 },
  { name: "spacing-heading-final", text: "# Hello", width: 80 },
  { name: "spacing-quote", text: "hello world\n\n> This is a quote\n\nagain, hello world", width: 80 },
  { name: "spacing-quote-final", text: "> This is a quote", width: 80 },
  { name: "quote-lazy", text: ">Foo\nbar", width: 80, defaultStyle: "magenta" },
  { name: "quote-explicit", text: ">Foo\n>bar", width: 80, defaultStyle: "cyan" },
  { name: "quote-list", text: "> 1. bla bla\n> - nested bullet", width: 80 },
  { name: "quote-wrap", text: "> This is a very long blockquote line that should wrap to multiple lines when rendered", width: 30 },
  { name: "quote-wrap-styled", text: "> This is styled text that is long enough to wrap", width: 25, defaultStyle: "yellow-italic" },
  { name: "quote-inline", text: "> Quote with **bold** and `code`", width: 80 },
  { name: "heading-inline-code", text: "### Why `sourceInfo` should not be optional", width: 80 },
  { name: "heading-h1-code", text: "# Title with `code` inside", width: 80 },
  { name: "heading-h1-code-final", text: "# Important distinction from `open()`", width: 80 },
  { name: "heading-bold", text: "## Heading with **bold** and more", width: 80 },
  { name: "strike-double", text: "Use ~~strikethrough~~ here", width: 80 },
  { name: "strike-single", text: "Use ~strikethrough~ literally", width: 80 },
  { name: "link-email", text: "Contact user@example.com for help", width: 80 },
  { name: "link-bare", text: "Visit https://example.com for more", width: 80 },
  { name: "link-explicit", text: "[click here](https://example.com)", width: 80 },
  { name: "link-mailto", text: "[Email me](mailto:test@example.com)", width: 80 },
  { name: "link-osc8", text: "[click here](https://example.com)", width: 80, hyperlinks: true },
  { name: "link-mailto-osc8", text: "[Email me](mailto:test@example.com)", width: 80, hyperlinks: true },
  { name: "link-bare-osc8", text: "Visit https://example.com for more", width: 80, hyperlinks: true },
  { name: "html-inline", text: "This is text with <thinking>hidden content</thinking> that should be visible", width: 80 },
  { name: "html-code", text: "```html\n<div>Some HTML</div>\n```", width: 80 },
  { name: "fence-partial", text: "```ts\nconst x = 1;\n``", width: 80 },
  { name: "fence-inner", text: "```md\nnot a closing fence:\n``\n```", width: 80 },
  { name: "fence-empty", text: "```ts\n``", width: 80 },
  { name: "fence-four", text: "````\n```", width: 80 },
  { name: "fence-tilde", text: "~~~~~\n~~~~", width: 80 },
  { name: "fence-followed", text: "```md\nnot a closing fence:\n``\n```\n\nafter", width: 80 },
];

const highlightCases = [
  { name: "typescript", language: "typescript", code: "const answer: number = 42; // value" },
  { name: "powershell-operator", language: "powershell", code: "$value -eq 42" },
];

type TuiModule = {
  Text: new (text?: string, paddingX?: number, paddingY?: number, style?: (value: string) => string) => { render(width: number): string[] };
  TruncatedText: new (text: string, paddingX?: number, paddingY?: number) => { render(width: number): string[] };
  Spacer: new (lines?: number) => { render(width: number): string[] };
  Container: new () => { addChild(child: unknown): void; render(width: number): string[] };
  Box: new (paddingX?: number, paddingY?: number, style?: (value: string) => string) => { addChild(child: unknown): void; render(width: number): string[] };
  Loader: new (
    ui: { requestRender(): void },
    spinnerStyle: (value: string) => string,
    messageStyle: (value: string) => string,
    message?: string,
    indicator?: { frames?: string[]; intervalMs?: number },
  ) => { render(width: number): string[]; stop(): void };
  Markdown: new (
    text: string,
    paddingX: number,
    paddingY: number,
    theme: Record<string, unknown>,
    defaultStyle?: Record<string, unknown>,
    options?: Record<string, unknown>,
  ) => { render(width: number): string[] };
  TUI: new (terminal: unknown) => { requestRender(): void };
  Editor: new (
    ui: { requestRender(): void },
    theme: {
      borderColor: (value: string) => string;
      selectList: {
        selectedPrefix: (value: string) => string;
        selectedText: (value: string) => string;
        description: (value: string) => string;
        scrollInfo: (value: string) => string;
        noMatch: (value: string) => string;
      };
    },
  ) => { setText(value: string): void; render(width: number): string[] };
  setCapabilities(capabilities: { images: null; trueColor: boolean; hyperlinks: boolean }): void;
  resetCapabilitiesCache(): void;
};

function style(name: StyleName | undefined): (value: string) => string {
  switch (name ?? "none") {
    case "red": return (value) => `\u001b[31m${value}\u001b[39m`;
    case "blue-bg": return (value) => `\u001b[44m${value}\u001b[49m`;
    case "bracket": return (value) => `[${value}]`;
    case "none": return (value) => value;
  }
}

const ansi = {
  bold: (value: string) => `\u001b[1m${value}\u001b[22m`,
  italic: (value: string) => `\u001b[3m${value}\u001b[23m`,
  underline: (value: string) => `\u001b[4m${value}\u001b[24m`,
  strike: (value: string) => `\u001b[9m${value}\u001b[29m`,
  fg: (code: number) => (value: string) => `\u001b[${code}m${value}\u001b[39m`,
};

const markdownTheme = {
  heading: (value: string) => ansi.bold(ansi.fg(36)(value)),
  link: ansi.fg(34),
  linkUrl: (value: string) => `\u001b[2m${value}\u001b[22m`,
  code: ansi.fg(33),
  codeBlock: ansi.fg(32),
  codeBlockBorder: (value: string) => `\u001b[2m${value}\u001b[22m`,
  quote: ansi.italic,
  quoteBorder: (value: string) => `\u001b[2m${value}\u001b[22m`,
  hr: (value: string) => `\u001b[2m${value}\u001b[22m`,
  listBullet: ansi.fg(36),
  bold: ansi.bold,
  italic: ansi.italic,
  strikethrough: ansi.strike,
  underline: ansi.underline,
};

const fullScreenEditorTheme = {
  borderColor: (value: string) => `\u001b[2m${value}\u001b[22m`,
  selectList: {
    selectedPrefix: (value: string) => value,
    selectedText: ansi.bold,
    description: (value: string) => `\u001b[2m${value}\u001b[22m`,
    scrollInfo: (value: string) => `\u001b[2m${value}\u001b[22m`,
    noMatch: (value: string) => `\u001b[2m${value}\u001b[22m`,
  },
};

function buildFullScreen(tui: TuiModule, fixtureCase: FullScreenFixtureCase): { render(width: number): string[] } {
  const root = new tui.Container();
  root.addChild(new tui.Text(fixtureCase.user, 1, 0, (value) => `\u001b[44m${value}\u001b[49m`));
  root.addChild(new tui.Spacer(1));
  root.addChild(new tui.Markdown(fixtureCase.assistant, 1, 0, markdownTheme));
  root.addChild(new tui.Spacer(1));
  const terminal = {
    start() {}, stop() {}, async drainInput() {}, write() {}, moveBy() {}, hideCursor() {}, showCursor() {},
    clearLine() {}, clearFromCursor() {}, clearScreen() {}, setTitle() {}, setProgress() {},
    get columns() { return fixtureCase.width; },
    get rows() { return fixtureCase.rows; },
    get kittyProtocolActive() { return false; },
  };
  const editor = new tui.Editor(new tui.TUI(terminal), fullScreenEditorTheme);
  editor.setText(fixtureCase.editor);
  root.addChild(editor);
  root.addChild(new tui.TruncatedText(fixtureCase.status, 1, 0));
  return root;
}

function defaultMarkdownStyle(name: MarkdownFixtureCase["defaultStyle"]): Record<string, unknown> | undefined {
  switch (name) {
    case "gray-italic": return { color: ansi.fg(90), italic: true };
    case "magenta": return { color: ansi.fg(35) };
    case "cyan": return { color: ansi.fg(36) };
    case "yellow-italic": return { color: ansi.fg(33), italic: true };
    default: return undefined;
  }
}

function summarizeThemeDiscovery(
  result: ReturnType<InstanceType<ResourceLoaderModule["DefaultResourceLoader"]>["getThemes"]>,
  name: string,
  labels: Map<string, string>,
) {
  const label = (value: string | undefined) => value === undefined ? undefined : (labels.get(value) ?? value);
  return {
    selected: label(result.themes.find((theme) => theme.name === name)?.sourcePath),
    diagnostics: result.diagnostics.map((diagnostic) => ({
      type: diagnostic.type,
      message: diagnostic.message,
      path: label(diagnostic.path),
      ...(diagnostic.collision ? {
        collision: {
          resourceType: diagnostic.collision.resourceType,
          name: diagnostic.collision.name,
          winnerPath: label(diagnostic.collision.winnerPath),
          loserPath: label(diagnostic.collision.loserPath),
        },
      } : {}),
    })),
  };
}

async function generateThemeDiscovery(upstreamRoot: string) {
  return withOfflineGeneratedCatalog(upstreamRoot, async () => {
    const source = "packages/coding-agent/src/core/resource-loader.ts";
    const resourceModule = (await import(pathToFileURL(path.join(upstreamRoot, source)).href)) as ResourceLoaderModule;
    const root = await mkdtemp(path.join(tmpdir(), "pi-go-f12-theme-"));
    try {
      const agentDir = path.join(root, "agent");
      const cwd = path.join(root, "project");
      const userTheme = path.join(agentDir, "themes", "user.json");
      const projectTheme = path.join(cwd, ".pi", "themes", "project.json");
      const firstTheme = path.join(root, "first.json");
      const secondTheme = path.join(root, "second.json");
      const dark = JSON.parse(await readFile(path.join(upstreamRoot, "packages/coding-agent/src/modes/interactive/theme/dark.json"), "utf8")) as {
        name: string;
        vars: Record<string, string>;
      };
      const writeTheme = async (file: string, name: string, accent: string) => {
        const document = structuredClone(dark);
        document.name = name;
        document.vars.accent = accent;
        await mkdir(path.dirname(file), { recursive: true });
        await writeFile(file, JSON.stringify(document));
      };

      await writeTheme(userTheme, "project-over-user", "#111111");
      await writeTheme(projectTheme, "project-over-user", "#222222");
      const projectLoader = new resourceModule.DefaultResourceLoader({
        cwd,
        agentDir,
        noExtensions: true,
        noSkills: true,
        noPromptTemplates: true,
        noContextFiles: true,
      });
      await projectLoader.reload();

      await writeTheme(firstTheme, "extend-first-wins", "#333333");
      await writeTheme(secondTheme, "extend-first-wins", "#444444");
      const extendLoader = new resourceModule.DefaultResourceLoader({
        cwd: path.join(root, "extend-project"),
        agentDir: path.join(root, "extend-agent"),
        additionalThemePaths: [firstTheme],
        noExtensions: true,
        noSkills: true,
        noPromptTemplates: true,
        noContextFiles: true,
      });
      await mkdir(path.join(root, "extend-project"), { recursive: true });
      await mkdir(path.join(root, "extend-agent"), { recursive: true });
      await extendLoader.reload();
      extendLoader.extendResources({
        themePaths: [{
          path: secondTheme,
          metadata: { source: "extension", scope: "temporary", origin: "top-level" },
        }],
      });

      return {
        projectOverUser: summarizeThemeDiscovery(projectLoader.getThemes(), "project-over-user", new Map([
          [projectTheme, "<project-theme>"],
          [userTheme, "<user-theme>"],
        ])),
        extendFirstWins: summarizeThemeDiscovery(extendLoader.getThemes(), "extend-first-wins", new Map([
          [firstTheme, "<first-theme>"],
          [secondTheme, "<second-theme>"],
        ])),
      };
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
}

function build(tui: TuiModule, node: FixtureNode): { component: { render(width: number): string[] }; cleanup?: () => void } {
  switch (node.type) {
    case "text": return { component: new tui.Text(node.text ?? "", node.paddingX ?? 1, node.paddingY ?? 1, node.style ? style(node.style) : undefined) };
    case "truncated-text": return { component: new tui.TruncatedText(node.text ?? "", node.paddingX ?? 0, node.paddingY ?? 0) };
    case "spacer": return { component: new tui.Spacer(node.lines ?? 1) };
    case "container": {
      const container = new tui.Container();
      for (const child of node.children ?? []) container.addChild(build(tui, child).component);
      return { component: container };
    }
    case "box": {
      const box = new tui.Box(node.paddingX ?? 1, node.paddingY ?? 1, node.style ? style(node.style) : undefined);
      for (const child of node.children ?? []) box.addChild(build(tui, child).component);
      return { component: box };
    }
    case "loader": {
      const loader = new tui.Loader(
        { requestRender() {} },
        style(node.spinnerStyle),
        style(node.messageStyle),
        node.message ?? "Loading...",
        { frames: node.frames ?? ["*"], intervalMs: 100_000 },
      );
      return { component: loader, cleanup: () => loader.stop() };
    }
  }
}

export async function generateF12(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const source = "packages/tui/src/index.ts";
  const sources = [
    source,
    "packages/tui/src/components/editor.ts",
    "packages/tui/src/components/input.ts",
    "packages/tui/src/components/select-list.ts",
    "packages/tui/src/components/settings-list.ts",
    "packages/tui/src/autocomplete.ts",
    "packages/tui/src/fuzzy.ts",
    "packages/tui/src/word-navigation.ts",
  ];
  const tui = (await import(pathToFileURL(path.join(upstreamRoot, source)).href)) as TuiModule;
  const generated = cases.map((fixtureCase) => {
    const built = build(tui, fixtureCase.node);
    try { return { ...fixtureCase, expected: built.component.render(fixtureCase.width) }; }
    finally { built.cleanup?.(); }
  });
  const markdown = markdownCases.map((fixtureCase) => {
    tui.setCapabilities({ images: null, trueColor: false, hyperlinks: fixtureCase.hyperlinks ?? false });
    try {
      const component = new tui.Markdown(
        fixtureCase.text,
        fixtureCase.paddingX ?? 0,
        fixtureCase.paddingY ?? 0,
        markdownTheme,
        defaultMarkdownStyle(fixtureCase.defaultStyle),
        {
          preserveOrderedListMarkers: fixtureCase.preserveOrderedListMarkers ?? false,
          preserveBackslashEscapes: fixtureCase.preserveBackslashEscapes ?? false,
        },
      );
      return { ...fixtureCase, expected: component.render(fixtureCase.width) };
    } finally {
      tui.resetCapabilitiesCache();
    }
  });
  const fullScreen = fullScreenCases.map((fixtureCase) => ({
    ...fixtureCase,
    expected: buildFullScreen(tui, fixtureCase).render(fixtureCase.width),
  }));
  const themeSource = "packages/coding-agent/src/modes/interactive/theme/theme.ts";
  process.env.PI_PACKAGE_DIR = path.join(upstreamRoot, "packages/coding-agent");
  process.env.FORCE_COLOR = "3";
  ((await import(pathToFileURL(path.join(upstreamRoot, "node_modules/chalk/source/index.js")).href)).default as unknown as { level: number }).level = 3;
  tui.setCapabilities({ images: null, trueColor: true, hyperlinks: false });
  const themeModule = (await import(pathToFileURL(path.join(upstreamRoot, themeSource)).href)) as ThemeModule;
  const themes = ["dark", "light"].map((name) => {
    themeModule.initTheme(name);
    const foreground = Object.fromEntries(foregroundThemeTokens.map((token) => [token, themeModule.theme.getFgAnsi(token)]));
    const background = Object.fromEntries(backgroundThemeTokens.map((token) => [token, themeModule.theme.getBgAnsi(token)]));
    const sample = new tui.Markdown(
      "# Theme sample\n\n> quote with **bold** and `code`\n\n```typescript\nconst answer: number = 42; // value\n```",
      0,
      0,
      { ...themeModule.getMarkdownTheme(), codeBlockIndent: ">>" },
    ).render(72);
    return {
      name,
      mode: themeModule.theme.getColorMode(),
      foreground,
      background,
      sample,
      highlighted: themeModule.highlightCode("const answer: number = 42; // value", "typescript"),
      highlights: highlightCases.map((fixtureCase) => ({
        ...fixtureCase,
        expected: themeModule.highlightCode(fixtureCase.code, fixtureCase.language),
      })),
      fallback: themeModule.highlightCode("plain text", "definitely-not-a-language"),
      resolved: themeModule.getResolvedThemeColors(name),
      export: themeModule.getThemeExportColors(name),
    };
  });
  const validationRoot = await mkdtemp(path.join(tmpdir(), "pi-go-f12-theme-validation-"));
  let trailingDocumentAccepted = false;
  let unknownForegroundThrows = false;
  try {
    const trailingPath = path.join(validationRoot, "trailing.json");
    const darkJSON = await readFile(path.join(upstreamRoot, "packages/coding-agent/src/modes/interactive/theme/dark.json"), "utf8");
    await writeFile(trailingPath, `${darkJSON}\n{}`);
    try {
      themeModule.loadThemeFromPath(trailingPath);
      trailingDocumentAccepted = true;
    } catch {
      trailingDocumentAccepted = false;
    }
    try {
      themeModule.theme.fg("__pi_go_unknown__", "value");
    } catch {
      unknownForegroundThrows = true;
    }
  } finally {
    await rm(validationRoot, { recursive: true, force: true });
  }
  const discovery = await generateThemeDiscovery(upstreamRoot);
  tui.resetCapabilitiesCache();
  const terminalImageSource = "packages/tui/src/terminal-image.ts";
  const terminalImage = await import(pathToFileURL(path.join(upstreamRoot, terminalImageSource)).href);
  const imageSource = "packages/tui/src/components/image.ts";
  const imageModule = await import(pathToFileURL(path.join(upstreamRoot, imageSource)).href);
  const encodingCases = [
    {
      name: "kitty-small",
      kind: "kitty",
      data: "QUJD",
      options: { columns: 10, rows: 4, imageId: 42, moveCursor: false },
      expected: terminalImage.encodeKitty("QUJD", { columns: 10, rows: 4, imageId: 42, moveCursor: false }),
    },
    {
      name: "kitty-chunk-boundary",
      kind: "kitty",
      data: "a".repeat(4100),
      options: {},
      expected: terminalImage.encodeKitty("a".repeat(4100)),
    },
    {
      name: "iterm-all-parameters",
      kind: "iterm2",
      data: "QUJD",
      options: { width: 12, height: "auto", name: "cat.png", preserveAspectRatio: false, inline: true },
      expected: terminalImage.encodeITerm2("QUJD", { width: 12, height: "auto", name: "cat.png", preserveAspectRatio: false, inline: true }),
    },
  ];
  const cellCases = [
    { name: "landscape-width", dimensions: { widthPx: 1000, heightPx: 500 }, maxWidth: 40, maxHeight: 20, cell: { widthPx: 10, heightPx: 20 } },
    { name: "portrait-square-pixels", dimensions: { widthPx: 200, heightPx: 800 }, maxWidth: 30, maxHeight: 15, cell: { widthPx: 9, heightPx: 18 } },
    { name: "floors-and-clamps", dimensions: { widthPx: 0, heightPx: -1 }, maxWidth: 0.8, maxHeight: 0.2, cell: { widthPx: 9, heightPx: 18 } },
  ].map((fixtureCase) => ({ ...fixtureCase, expected: terminalImage.calculateImageCellSize(fixtureCase.dimensions, fixtureCase.maxWidth, fixtureCase.maxHeight, fixtureCase.cell) }));
  const renderInputs = [
    { name: "kitty-component", protocol: "kitty", width: 80, options: { maxWidthCells: 20, maxHeightCells: 10, filename: "sample.png", imageId: 99 } },
    { name: "kitty-zero-options", protocol: "kitty", width: 80, options: { maxWidthCells: 0, maxHeightCells: 0, filename: "sample.png", imageId: 0 } },
    { name: "iterm-component", protocol: "iterm2", width: 80, options: { maxWidthCells: 20, maxHeightCells: 10, filename: "sample.png", imageId: 99 } },
    { name: "fallback-component", protocol: null, width: 80, options: { maxWidthCells: 20, maxHeightCells: 10, filename: "sample.png", imageId: 99 } },
  ];
  const dimensions = { widthPx: 400, heightPx: 200 };
  terminalImage.setCellDimensions({ widthPx: 10, heightPx: 20 });
  const renderCases = renderInputs.map((fixtureCase) => {
    terminalImage.setCapabilities({ images: fixtureCase.protocol, trueColor: true, hyperlinks: true });
    const component = new imageModule.Image("QUJD", "image/png", { fallbackColor: (value: string) => `<${value}>` }, fixtureCase.options, dimensions);
    return { ...fixtureCase, data: "QUJD", mimeType: "image/png", dimensions, expected: component.render(fixtureCase.width) };
  });
  terminalImage.resetCapabilitiesCache();
  terminalImage.setCellDimensions({ widthPx: 9, heightPx: 18 });
  const terminalImages = { schemaVersion: 2, encodingCases, cellCases, renderCases };
  const familyDir = path.join(outputRoot, "F12");
  await mkdir(familyDir, { recursive: true });
  const componentFiles = await generateF12Components(upstreamRoot, outputRoot);
  const coreFiles = await generateF12Core(upstreamRoot, familyDir);
  const fixtureSources = [
    ...sources,
    "packages/tui/src/components/markdown.ts",
    "packages/tui/test/markdown.test.ts",
    themeSource,
    "packages/coding-agent/src/modes/interactive/theme/dark.json",
    "packages/coding-agent/src/modes/interactive/theme/light.json",
    "packages/coding-agent/src/utils/syntax-highlight.ts",
    "packages/coding-agent/src/core/resource-loader.ts",
    "packages/coding-agent/src/core/package-manager.ts",
    "packages/coding-agent/src/core/settings-manager.ts",
    terminalImageSource,
    imageSource,
    ...coreFiles.sources,
  ];
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({ family: "F12", upstreamCommit, generator: "conformance/extract/f12-tui.ts", source: fixtureSources.join("; "), sources: fixtureSources, files: ["primitives.json", "markdown.json", "themes.json", "terminal-images.json", "full-screen.json", ...coreFiles.files, ...componentFiles] }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "primitives.json"), `${JSON.stringify({ schemaVersion: 1, cases: generated }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "markdown.json"), `${JSON.stringify({ schemaVersion: 1, cases: markdown }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "full-screen.json"), `${JSON.stringify({ schemaVersion: 1, cases: fullScreen }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "themes.json"), `${JSON.stringify({ schemaVersion: 1, themes, discovery, validation: { trailingDocumentAccepted, unknownForegroundThrows } }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "terminal-images.json"), `${JSON.stringify(terminalImages, null, 2)}\n`);
}
