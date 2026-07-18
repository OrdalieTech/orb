import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

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
};

function style(name: StyleName | undefined): (value: string) => string {
  switch (name ?? "none") {
    case "red": return (value) => `\u001b[31m${value}\u001b[39m`;
    case "blue-bg": return (value) => `\u001b[44m${value}\u001b[49m`;
    case "bracket": return (value) => `[${value}]`;
    case "none": return (value) => value;
  }
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
  const tui = (await import(pathToFileURL(path.join(upstreamRoot, source)).href)) as TuiModule;
  const generated = cases.map((fixtureCase) => {
    const built = build(tui, fixtureCase.node);
    try { return { ...fixtureCase, expected: built.component.render(fixtureCase.width) }; }
    finally { built.cleanup?.(); }
  });
  const familyDir = path.join(outputRoot, "F12");
  await mkdir(familyDir, { recursive: true });
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({ family: "F12", upstreamCommit, generator: "conformance/extract/f12-tui.ts", source, files: ["primitives.json"] }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "primitives.json"), `${JSON.stringify({ schemaVersion: 1, cases: generated }, null, 2)}\n`);
}
