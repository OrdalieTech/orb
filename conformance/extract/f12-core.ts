import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type FixtureSize = number | `${number}%`;
type FixtureMargin = number | { top?: number; right?: number; bottom?: number; left?: number };
type FixtureOverlayOptions = {
  width?: FixtureSize;
  minWidth?: number;
  maxHeight?: FixtureSize;
  anchor?: string;
  offsetX?: number;
  offsetY?: number;
  row?: FixtureSize;
  col?: FixtureSize;
  margin?: FixtureMargin;
  nonCapturing?: boolean;
  visibleMinWidth?: number;
};
type FixtureOverlay = { lines: string[]; options?: FixtureOverlayOptions };
type FixtureOverlayAction = { action: "focus" | "hide" | "setHidden" | "hideOverlay"; overlay?: number; hidden?: boolean };
type OverlayRenderCase = {
  name: string;
  width: number;
  rows: number;
  base: string[];
  overlays: FixtureOverlay[];
  actions?: FixtureOverlayAction[];
};

type FocusTraceOptions = {
  nonCapturing?: boolean;
  visibleFlag?: string;
  row?: number;
  col?: number;
  width?: number;
};
type FocusTraceOperation =
  | { op: "show"; component: string; handle: string; options?: FocusTraceOptions }
  | { op: "focus" | "hide"; handle: string }
  | { op: "setHidden"; handle: string; hidden: boolean }
  | { op: "unfocus"; handle: string; target?: string | null }
  | { op: "hideOverlay" }
  | { op: "setFocus"; target: string | null }
  | { op: "setFlag"; flag: string; value: boolean }
  | { op: "mount"; components: string[] }
  | { op: "input"; data: string }
  | { op: "schedule"; operations: FocusTraceOperation[] }
  | { op: "flush" }
  | { op: "observe"; label: string; probe?: string[] };
type FocusTraceHandler = { component: string; data: string; operations: FocusTraceOperation[] };
type FocusTraceSpec = {
  name: string;
  components: string[];
  nonFocusable?: string[];
  mounted?: string[];
  initialFocus?: string | null;
  flags?: Record<string, boolean>;
  handlers?: FocusTraceHandler[];
  operations: FocusTraceOperation[];
  width?: number;
  rows?: number;
};
type FocusTraceObservation = {
  label: string;
  focused: string[];
  inputs: Record<string, string[]>;
  handles: Record<string, { hidden: boolean; focused: boolean }>;
  hasOverlay: boolean;
  front?: string | null;
};
type FocusTraceCase = FocusTraceSpec & { expected: FocusTraceObservation[] };

type Component = {
  focused?: boolean;
  inputs?: string[];
  render(width: number): string[];
  handleInput?(data: string): void;
  invalidate(): void;
};
type OverlayHandle = {
  hide(): void;
  setHidden(hidden: boolean): void;
  isHidden(): boolean;
  focus(): void;
  unfocus(options?: { target: Component | null }): void;
  isFocused(): boolean;
};
type CoreTui = {
  addChild(component: Component): void;
  setFocus(component: Component | null): void;
  showOverlay(component: Component, options?: Record<string, unknown>): OverlayHandle;
  hideOverlay(): void;
  hasOverlay(): boolean;
  start(): void;
  stop(): void;
  requestRender(force?: boolean): void;
  addInputListener(listener: (data: string) => { consume?: boolean; data?: string } | undefined): () => void;
  onTerminalColorSchemeChange(listener: (scheme: string) => void): () => void;
  setTerminalColorSchemeNotifications(enabled: boolean): void;
  queryTerminalBackgroundColor(options: { timeoutMs: number }): Promise<{ r: number; g: number; b: number } | undefined>;
  queryTerminalColorScheme(options: { timeoutMs: number }): Promise<string | undefined>;
};
type CoreContainer = Component & { addChild(component: Component): void; clear(): void };
type TuiModule = {
  TUI: new (terminal: CoreTerminal) => CoreTui;
  Container: new () => CoreContainer;
  setCapabilities(capabilities: { images: null; trueColor: boolean; hyperlinks: boolean }): void;
  resetCapabilitiesCache(): void;
};
type TerminalColorsModule = {
  isOsc11BackgroundColorResponse(data: string): boolean;
  parseOsc11BackgroundColor(data: string): { r: number; g: number; b: number } | undefined;
  parseTerminalColorSchemeReport(data: string): string | undefined;
};

class CoreTerminal {
  writes: string[] = [];
  cursorEvents: string[] = [];
  private inputHandler?: (data: string) => void;

  constructor(public columns: number, public rows: number) {}

  start(onInput: (data: string) => void): void { this.inputHandler = onInput; }
  stop(): void { this.inputHandler = undefined; }
  async drainInput(): Promise<void> {}
  write(data: string): void { this.writes.push(data); }
  moveBy(): void {}
  hideCursor(): void { this.cursorEvents.push("hide"); }
  showCursor(): void { this.cursorEvents.push("show"); }
  clearLine(): void {}
  clearFromCursor(): void {}
  clearScreen(): void {}
  setTitle(): void {}
  setProgress(): void {}
  get kittyProtocolActive(): boolean { return false; }
  send(data: string): void { this.inputHandler?.(data); }
}

class StaticComponent implements Component {
  focused = false;
  inputs: string[] = [];
  requestedWidths: number[] = [];
  onInput?: (data: string) => void;

  constructor(private readonly lines: string[]) {}

  render(width: number): string[] {
    this.requestedWidths.push(width);
    return this.lines;
  }
  handleInput(data: string): void {
    this.inputs.push(data);
    this.onInput?.(data);
  }
  invalidate(): void {}
}

class PlainComponent implements Component {
  constructor(readonly lines: string[]) {}
  render(): string[] { return this.lines; }
  invalidate(): void {}
}

const overlayRenderCases: OverlayRenderCase[] = [
  {
    name: "short-content-centered",
    width: 80,
    rows: 24,
    base: ["Line 1", "Line 2", "Line 3"],
    overlays: [{ lines: ["OVERLAY_TOP", "OVERLAY_MID", "OVERLAY_BOT"] }],
  },
  {
    name: "anchor-margin-offset",
    width: 20,
    rows: 8,
    base: ["base 0", "base 1", "base 2"],
    overlays: [{
      lines: ["BOTTOM", "EDGE"],
      options: { width: 8, anchor: "bottom-right", margin: { right: 2, bottom: 1 }, offsetX: -1, offsetY: -1 },
    }],
  },
  {
    name: "percentage-size-position-and-max-height",
    width: 40,
    rows: 10,
    base: [],
    overlays: [{
      lines: ["L1", "L2", "L3", "L4", "L5", "L6", "L7"],
      options: { width: "50%", minWidth: 12, maxHeight: "50%", row: "100%", col: "50%" },
    }],
  },
  {
    name: "focus-order-overrides-creation-order",
    width: 20,
    rows: 6,
    base: [],
    overlays: [
      { lines: ["AAAAAAAA"], options: { width: 8, row: 0, col: 0, nonCapturing: true } },
      { lines: ["BBBB"], options: { width: 4, row: 0, col: 0, nonCapturing: true } },
    ],
    actions: [{ action: "focus", overlay: 0 }],
  },
  {
    name: "all-hidden-overlays-still-reserve-terminal-height",
    width: 20,
    rows: 6,
    base: ["only"],
    overlays: [{ lines: ["HIDDEN"], options: { width: 6, row: 0, col: 0 } }],
    actions: [{ action: "setHidden", overlay: 0, hidden: true }],
  },
  {
    name: "responsive-overlay-hidden-at-current-width",
    width: 20,
    rows: 6,
    base: ["base"],
    overlays: [{ lines: ["RESPONSIVE"], options: { width: 10, row: 0, col: 0, visibleMinWidth: 30 } }],
  },
  {
    name: "cjk-tab-ansi-boundary",
    width: 20,
    rows: 4,
    base: ["\u001b[3mabcd让EFGHXXXXXXXXX\u001b[23m", "INPUT"],
    overlays: [{ lines: ["\tX"], options: { width: 4, row: 0, col: 5 } }],
  },
  {
    name: "declared-width-truncates-overlay",
    width: 20,
    rows: 4,
    base: [],
    overlays: [{ lines: ["X".repeat(100)], options: { width: 6, anchor: "top-left" } }],
  },
  {
    name: "overlay-is-relative-to-bottom-viewport",
    width: 20,
    rows: 4,
    base: ["0", "1", "2", "3", "4", "5", "6", "7"],
    overlays: [{ lines: ["TOP"], options: { width: 4, anchor: "top-left" } }],
  },
  {
    name: "complex-ansi-overlay",
    width: 80,
    rows: 24,
    base: [],
    overlays: [{
      lines: [
        "\x1b[48;2;40;50;40m \x1b[38;2;128;128;128mSome styled content\x1b[39m\x1b[49m\x1b]8;;http://example.com\x07link\x1b]8;;\x07" + " more content ".repeat(10),
      ],
      options: { width: 60 },
    }],
  },
  {
    name: "styled-base-overlay",
    width: 80,
    rows: 24,
    base: Array(3).fill(`\x1b[1m\x1b[38;2;255;0;0m${"X".repeat(80)}\x1b[0m`),
    overlays: [{ lines: ["OVERLAY"], options: { width: 20, anchor: "center" } }],
  },
  {
    name: "wide-character-overlay-boundary",
    width: 80,
    rows: 24,
    base: [],
    overlays: [{ lines: ["中文日本語한글テスト漢字"], options: { width: 15 } }],
  },
  {
    name: "terminal-right-edge",
    width: 80,
    rows: 24,
    base: [],
    overlays: [{ lines: ["X".repeat(50)], options: { col: 60, width: 20 } }],
  },
  {
    name: "osc-styled-base-overlay",
    width: 80,
    rows: 24,
    base: Array(3).fill(`See \x1b]8;;file:///path/to/file.ts\x07file.ts\x1b]8;;\x07 for details ${"X".repeat(50)}`),
    overlays: [{ lines: ["OVERLAY-TEXT"], options: { anchor: "center", width: 20 } }],
  },
  { name: "width-percent", width: 100, rows: 24, base: [], overlays: [{ lines: ["test"], options: { width: "50%" } }] },
  { name: "width-percent-minimum", width: 100, rows: 24, base: [], overlays: [{ lines: ["test"], options: { width: "10%", minWidth: 30 } }] },
  { name: "anchor-top-left", width: 80, rows: 24, base: [], overlays: [{ lines: ["TOP-LEFT"], options: { anchor: "top-left", width: 10 } }] },
  { name: "anchor-bottom-right", width: 80, rows: 24, base: [], overlays: [{ lines: ["BTM-RIGHT"], options: { anchor: "bottom-right", width: 10 } }] },
  { name: "anchor-top-center", width: 80, rows: 24, base: [], overlays: [{ lines: ["CENTERED"], options: { anchor: "top-center", width: 10 } }] },
  {
    name: "negative-margins-clamp-to-zero",
    width: 80,
    rows: 24,
    base: [],
    overlays: [{ lines: ["NEG-MARGIN"], options: { anchor: "top-left", width: 12, margin: { top: -5, left: -10, right: 0, bottom: 0 } } }],
  },
  { name: "uniform-margin", width: 80, rows: 24, base: [], overlays: [{ lines: ["MARGIN"], options: { anchor: "top-left", width: 10, margin: 5 } }] },
  {
    name: "object-margin",
    width: 80,
    rows: 24,
    base: [],
    overlays: [{ lines: ["MARGIN"], options: { anchor: "top-left", width: 10, margin: { top: 2, left: 3, right: 0, bottom: 0 } } }],
  },
  { name: "anchor-offset", width: 80, rows: 24, base: [], overlays: [{ lines: ["OFFSET"], options: { anchor: "top-left", width: 10, offsetX: 10, offsetY: 5 } }] },
  { name: "percent-row-col", width: 80, rows: 24, base: [], overlays: [{ lines: ["PCT"], options: { width: 10, row: "50%", col: "50%" } }] },
  { name: "row-percent-zero", width: 80, rows: 24, base: [], overlays: [{ lines: ["TOP"], options: { width: 10, row: "0%" } }] },
  { name: "row-percent-hundred", width: 80, rows: 24, base: [], overlays: [{ lines: ["BOTTOM"], options: { width: 10, row: "100%" } }] },
  {
    name: "max-height-absolute",
    width: 80,
    rows: 24,
    base: [],
    overlays: [{ lines: ["Line 1", "Line 2", "Line 3", "Line 4", "Line 5"], options: { maxHeight: 3 } }],
  },
  {
    name: "max-height-percent",
    width: 80,
    rows: 10,
    base: [],
    overlays: [{ lines: ["L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8", "L9", "L10"], options: { maxHeight: "50%" } }],
  },
  { name: "row-col-override-anchor", width: 80, rows: 24, base: [], overlays: [{ lines: ["ABSOLUTE"], options: { anchor: "bottom-right", row: 3, col: 5, width: 10 } }] },
  {
    name: "later-overlay-on-top",
    width: 80,
    rows: 24,
    base: [],
    overlays: [
      { lines: ["FIRST-OVERLAY"], options: { anchor: "top-left", width: 20 } },
      { lines: ["SECOND"], options: { anchor: "top-left", width: 10 } },
    ],
  },
  {
    name: "different-overlay-positions",
    width: 80,
    rows: 24,
    base: [],
    overlays: [
      { lines: ["TOP-LEFT"], options: { anchor: "top-left", width: 15 } },
      { lines: ["BTM-RIGHT"], options: { anchor: "bottom-right", width: 15 } },
    ],
  },
  {
    name: "hide-overlay-stack-order",
    width: 80,
    rows: 24,
    base: [],
    overlays: [
      { lines: ["FIRST"], options: { anchor: "top-left", width: 10 } },
      { lines: ["SECOND"], options: { anchor: "top-left", width: 10 } },
    ],
    actions: [{ action: "hideOverlay" }],
  },
  {
    name: "style-leak-no-overlay",
    width: 20,
    rows: 6,
    base: [`\x1b[3m${"X".repeat(20)}\x1b[23m`, "INPUT"],
    overlays: [],
  },
  {
    name: "style-leak-with-overlay",
    width: 20,
    rows: 6,
    base: [`\x1b[3m${"X".repeat(20)}\x1b[23m`, "INPUT"],
    overlays: [{ lines: ["OVR"], options: { row: 0, col: 5, width: 3 } }],
  },
  { name: "cjk-overlay-starts-inside-wide-grapheme", width: 20, rows: 4, base: ["abcd让EFGH"], overlays: [{ lines: ["│XX│"], options: { row: 0, col: 5, width: 4 } }] },
  { name: "cjk-overlay-starts-at-wide-grapheme-boundary", width: 20, rows: 4, base: ["abcd让EFGH"], overlays: [{ lines: ["│XX│"], options: { row: 0, col: 4, width: 4 } }] },
  {
    name: "tab-overlay-one-physical-row",
    width: 16,
    rows: 3,
    base: ["base 0          ", "base 1          ", "base 2          "],
    overlays: [{ lines: ["\tX"], options: { width: 4, row: 1, col: 4 } }],
  },
  { name: "anchor-top-right", width: 24, rows: 8, base: [], overlays: [{ lines: ["TR"], options: { anchor: "top-right", width: 4 } }] },
  { name: "anchor-bottom-left", width: 24, rows: 8, base: [], overlays: [{ lines: ["BL"], options: { anchor: "bottom-left", width: 4 } }] },
  { name: "anchor-bottom-center", width: 24, rows: 8, base: [], overlays: [{ lines: ["BC"], options: { anchor: "bottom-center", width: 4 } }] },
  { name: "anchor-left-center", width: 24, rows: 8, base: [], overlays: [{ lines: ["LC"], options: { anchor: "left-center", width: 4 } }] },
  { name: "anchor-right-center", width: 24, rows: 8, base: [], overlays: [{ lines: ["RC"], options: { anchor: "right-center", width: 4 } }] },
  {
    name: "offset-clamps-to-object-margins",
    width: 24,
    rows: 8,
    base: [],
    overlays: [{ lines: ["CLAMP"], options: { anchor: "bottom-right", width: 6, offsetX: 100, offsetY: 100, margin: { top: 1, right: 2, bottom: 2, left: 1 } } }],
  },
  { name: "col-percent-zero", width: 24, rows: 8, base: [], overlays: [{ lines: ["LEFT"], options: { width: 6, col: "0%" } }] },
  { name: "col-percent-hundred", width: 24, rows: 8, base: [], overlays: [{ lines: ["RIGHT"], options: { width: 6, col: "100%" } }] },
];

const overlayOptionCoverage: Record<string, string> = {
  "should truncate overlay lines that exceed declared width": "declared-width-truncates-overlay",
  "should handle overlay with complex ANSI sequences without crashing": "complex-ansi-overlay",
  "should handle overlay composited on styled base content": "styled-base-overlay",
  "should handle wide characters at overlay boundary": "wide-character-overlay-boundary",
  "should handle overlay positioned at terminal edge": "terminal-right-edge",
  "should handle overlay on base content with OSC sequences": "osc-styled-base-overlay",
  "should render overlay at percentage of terminal width": "width-percent",
  "should respect minWidth when widthPercent results in smaller width": "width-percent-minimum",
  "should position overlay at top-left": "anchor-top-left",
  "should position overlay at bottom-right": "anchor-bottom-right",
  "should position overlay at top-center": "anchor-top-center",
  "should clamp negative margins to zero": "negative-margins-clamp-to-zero",
  "should respect margin as number": "uniform-margin",
  "should respect margin object": "object-margin",
  "should apply offsetX and offsetY from anchor position": "anchor-offset",
  "should position with rowPercent and colPercent": "percent-row-col",
  "rowPercent 0 should position at top": "row-percent-zero",
  "rowPercent 100 should position at bottom": "row-percent-hundred",
  "should truncate overlay to maxHeight": "max-height-absolute",
  "should truncate overlay to maxHeightPercent": "max-height-percent",
  "row and col should override anchor": "row-col-override-anchor",
  "should render multiple overlays with later ones on top": "later-overlay-on-top",
  "should handle overlays at different positions without interference": "different-overlay-positions",
  "should properly hide overlays in stack order": "hide-overlay-stack-order",
};

function namedTests(source: string): string[] {
  return [...source.matchAll(/^\s*it\("([^"]+)"/gm)].map((match) => match[1]);
}

function assertExactNames(label: string, upstream: string[], covered: string[]): void {
  if (JSON.stringify(upstream) !== JSON.stringify(covered)) {
    throw new Error(`${label} coverage differs\nupstream: ${JSON.stringify(upstream)}\ncovered: ${JSON.stringify(covered)}`);
  }
}

function runtimeOptions(options: FixtureOverlayOptions | undefined): Record<string, unknown> | undefined {
  if (!options) return undefined;
  const { visibleMinWidth, ...rest } = options;
  return visibleMinWidth === undefined ? rest : { ...rest, visible: (width: number) => width >= visibleMinWidth };
}

async function forceRender(ui: CoreTui): Promise<void> {
  ui.requestRender(true);
  await new Promise<void>((resolve) => process.nextTick(resolve));
}

async function generateOverlayRenders(tui: TuiModule): Promise<Array<OverlayRenderCase & { expected: string }>> {
  const generated = [];
  for (const fixtureCase of overlayRenderCases) {
    const terminal = new CoreTerminal(fixtureCase.width, fixtureCase.rows);
    const ui = new tui.TUI(terminal);
    ui.addChild(new StaticComponent(fixtureCase.base));
    const overlayComponents = fixtureCase.overlays.map((overlay) => new StaticComponent(overlay.lines));
    const handles = fixtureCase.overlays.map((overlay, index) =>
      ui.showOverlay(overlayComponents[index], runtimeOptions(overlay.options)),
    );
    for (const action of fixtureCase.actions ?? []) {
      if (action.action === "hideOverlay") {
        ui.hideOverlay();
        continue;
      }
      const handle = handles[action.overlay!];
      if (action.action === "focus") handle.focus();
      else if (action.action === "hide") handle.hide();
      else handle.setHidden(action.hidden ?? false);
    }
    ui.start();
    terminal.writes = [];
    await forceRender(ui);
    const expected = terminal.writes.join("");
    ui.stop();
    const expectedRequestedWidths = overlayComponents.map((component) => component.requestedWidths.at(-1) ?? null);
    generated.push({ ...fixtureCase, expected, expectedRequestedWidths });
  }
  return generated;
}

async function generateOverlayCursorTrace(tui: TuiModule): Promise<string[]> {
  const terminal = new CoreTerminal(20, 6);
  const ui = new tui.TUI(terminal);
  ui.addChild(new PlainComponent([]));
  ui.start();
  await forceRender(ui);
  terminal.cursorEvents = [];
  const first = ui.showOverlay(new PlainComponent(["A"]), { nonCapturing: true });
  first.hide();
  ui.showOverlay(new PlainComponent(["A"]), { nonCapturing: true });
  ui.showOverlay(new PlainComponent(["B"]), { nonCapturing: true });
  ui.hideOverlay();
  ui.hideOverlay();
  ui.stop();
  return terminal.cursorEvents;
}

function handleInput(ui: CoreTui, data: string): void {
  (ui as unknown as { handleInput(data: string): void }).handleInput(data);
}

const show = (component: string, options?: FocusTraceOptions): FocusTraceOperation => ({
  op: "show",
  component,
  handle: component,
  ...(options ? { options } : {}),
});
const focus = (handle: string): FocusTraceOperation => ({ op: "focus", handle });
const hide = (handle: string): FocusTraceOperation => ({ op: "hide", handle });
const hidden = (handle: string, value: boolean): FocusTraceOperation => ({ op: "setHidden", handle, hidden: value });
const unfocus = (handle: string, ...target: [string | null] | []): FocusTraceOperation => ({
  op: "unfocus",
  handle,
  ...(target.length ? { target: target[0] } : {}),
});
const setFocus = (target: string | null): FocusTraceOperation => ({ op: "setFocus", target });
const input = (data: string): FocusTraceOperation => ({ op: "input", data });
const observe = (label: string, probe?: string[]): FocusTraceOperation => ({ op: "observe", label, ...(probe ? { probe } : {}) });
const handler = (component: string, data: string, ...operations: FocusTraceOperation[]): FocusTraceHandler => ({
  component,
  data,
  operations,
});
const editorCase = (
  name: string,
  components: string[],
  operations: FocusTraceOperation[],
  extra: Partial<FocusTraceSpec> = {},
): FocusTraceSpec => ({ name, components: ["editor", ...components], initialFocus: "editor", operations, ...extra });
const visualOptions: FocusTraceOptions = { row: 0, col: 0, width: 1, nonCapturing: true };

const focusTraceSpecs: FocusTraceSpec[] = [
  editorCase("non-capturing overlay preserves focus on creation", ["overlay"], [show("overlay", { nonCapturing: true }), observe("shown")]),
  editorCase("focus() transfers focus to the overlay", ["overlay"], [show("overlay", { nonCapturing: true }), focus("overlay"), observe("focused")]),
  editorCase("unfocus() restores previous focus", ["overlay"], [show("overlay", { nonCapturing: true }), focus("overlay"), unfocus("overlay"), observe("unfocused")]),
  editorCase("setHidden(false) on non-capturing overlay does not auto-focus", ["overlay"], [show("overlay", { nonCapturing: true }), hidden("overlay", true), hidden("overlay", false), observe("shown")]),
  editorCase("hide() when overlay is not focused does not change focus", ["overlay"], [show("overlay", { nonCapturing: true }), hide("overlay"), observe("hidden")]),
  editorCase("hide() when focused restores focus correctly", ["overlay"], [show("overlay", { nonCapturing: true }), focus("overlay"), hide("overlay"), observe("hidden")]),
  editorCase("capturing overlay removed with non-capturing below restores focus to editor", ["nonCapturing", "capturing"], [
    show("nonCapturing", { nonCapturing: true }), show("capturing"), observe("capturing"), hide("capturing"), observe("restored"),
  ]),
  editorCase("sub-overlay cleanup then hideOverlay restores focus and input to editor", ["timer", "controller"], [
    show("timer", { nonCapturing: true }), show("controller"), observe("active"), hide("timer"), { op: "hideOverlay" }, input("x"), observe("closed"),
  ]),
  editorCase("removed focused child overlay does not become parent overlay fallback", ["child", "parent"], [
    show("child", { nonCapturing: true }), focus("child"), show("parent"), hide("child"), hide("parent"), input("x"), observe("closed"),
  ]),
  editorCase("microtask-deferred sub-overlay pattern (showExtensionCustom simulation) restores focus", ["timer", "controller"], [
    show("timer", { nonCapturing: true }), { op: "schedule", operations: [show("controller")] }, { op: "flush" }, observe("active"),
    hide("timer"), { op: "hideOverlay" }, observe("closed"), input("x"), observe("input"),
  ]),
  editorCase("handleInput redirection skips non-capturing overlays when focused overlay becomes invisible", ["fallbackCapturing", "nonCapturing", "primary"], [
    show("fallbackCapturing"), show("nonCapturing", { nonCapturing: true }), show("primary", { visibleFlag: "visible" }),
    { op: "setFlag", flag: "visible", value: false }, input("x"), observe("fallback"),
  ], { flags: { visible: true } }),
  editorCase("active base focus replacement receives close input before overlay restore", ["replacement", "overlay"], [
    show("overlay"), input("b"), observe("replacement"), input("\r"), observe("restored"), input("x"), observe("overlay-input"),
  ], { handlers: [handler("overlay", "b", setFocus("replacement")), handler("replacement", "\r", setFocus("editor"))] }),
  editorCase("active replacement still receives input when it is another overlay preFocus", ["replacement", "passive", "overlay"], [
    setFocus("replacement"), show("passive", { nonCapturing: true }), setFocus("editor"), show("overlay"), input("b"), observe("replacement"),
    input("1"), input("\r"), observe("restored"),
  ], { handlers: [handler("overlay", "b", setFocus("replacement")), handler("replacement", "\r", setFocus("editor"))] }),
  editorCase("blocked replacement can move focus internally before overlay restore", ["firstReplacement", "secondReplacement", "overlay"], [
    show("overlay"), input("b"), input("n"), input("2"), input("\r"), observe("restored"),
  ], {
    mounted: ["editor", "firstReplacement", "secondReplacement"],
    handlers: [
      handler("overlay", "b", setFocus("firstReplacement")),
      handler("firstReplacement", "n", setFocus("secondReplacement")),
      handler("secondReplacement", "\r", { op: "mount", components: ["editor"] }, setFocus("editor")),
    ],
  }),
  {
    name: "removed replacement restores overlay even when overlay preFocus differs from next focus",
    components: ["editor", "palette", "replacement", "overlay"],
    mounted: ["editor", "palette", "replacement"],
    initialFocus: "palette",
    handlers: [
      handler("overlay", "b", setFocus("replacement")),
      handler("replacement", "\r", { op: "mount", components: ["editor"] }, setFocus("editor")),
    ],
    operations: [show("overlay"), input("b"), input("\r"), input("x"), observe("restored")],
  },
  {
    name: "unfocus target releases a blocked overlay while replacement remains focused",
    components: ["fallback", "target", "replacement", "overlay"],
    initialFocus: null,
    handlers: [
      handler("replacement", "\r", setFocus("fallback")),
      handler("overlay", "b", setFocus("replacement"), unfocus("overlay", "target")),
    ],
    operations: [show("overlay"), input("b"), observe("replacement"), input("\r"), input("x"), observe("target")],
  },
  editorCase("handleInput restores focus to a visible focused overlay after base focus steal", ["replacement", "overlay"], [
    show("overlay"), setFocus("replacement"), setFocus("editor"), input("x"), observe("restored"),
  ]),
  editorCase("handleInput restores focus to explicitly focused raw sub-overlay after base focus steal", ["controller", "subOverlay"], [
    show("controller"), show("subOverlay", { nonCapturing: true }), focus("subOverlay"), setFocus("editor"), input("x"), observe("restored"),
  ]),
  editorCase("passive non-capturing overlay does not regain input after base focus", ["passive"], [show("passive", { nonCapturing: true }), input("x"), observe("passive")]),
  editorCase("explicitly focused non-capturing overlay regains input after base focus steal", ["overlay"], [
    show("overlay", { nonCapturing: true }), focus("overlay"), setFocus("editor"), input("x"), observe("restored"),
  ]),
  editorCase("unfocus() prevents visible overlay from regaining input", ["overlay"], [show("overlay"), unfocus("overlay"), input("x"), observe("unfocused")]),
  { name: "setFocus(null) explicitly clears visible overlay restore", components: ["overlay"], initialFocus: null, operations: [show("overlay"), setFocus(null), input("x"), observe("cleared")] },
  {
    name: "blocked replacement setFocus(null) resumes the visible overlay",
    components: ["replacement", "overlay"],
    initialFocus: null,
    handlers: [handler("replacement", "\r", setFocus(null)), handler("overlay", "b", setFocus("replacement"))],
    operations: [show("overlay"), input("b"), input("\r"), input("x"), observe("restored")],
  },
  editorCase("temporarily invisible focused overlay falls back without losing restore eligibility", ["overlay"], [
    show("overlay", { visibleFlag: "visible" }), setFocus("editor"), { op: "setFlag", flag: "visible", value: false }, input("x"), observe("invisible"),
    { op: "setFlag", flag: "visible", value: true }, input("y"), observe("visible"),
  ], { flags: { visible: true } }),
  {
    name: "temporarily invisible focused overlay with null preFocus restores when visible again",
    components: ["overlay"],
    initialFocus: null,
    flags: { visible: true },
    operations: [show("overlay", { visibleFlag: "visible" }), { op: "setFlag", flag: "visible", value: false }, input("x"), observe("invisible"), { op: "setFlag", flag: "visible", value: true }, input("y"), observe("visible")],
  },
  {
    name: "cyclic overlay preFocus ancestry does not hang focus changes",
    components: ["editor", "overlay"],
    initialFocus: "overlay",
    operations: [show("overlay", { nonCapturing: true }), focus("overlay"), setFocus("editor"), input("x"), observe("cycle")],
  },
  editorCase("handleInput restores the focus-order top overlay after base focus steal", ["lower", "upper"], [
    show("lower"), show("upper"), focus("lower"), setFocus("editor"), input("x"), observe("lower"),
  ]),
  editorCase("hideOverlay() does not reassign focus when topmost overlay is non-capturing", ["capturing", "nonCapturing"], [
    show("capturing"), show("nonCapturing", { nonCapturing: true }), observe("before"), { op: "hideOverlay" }, observe("after"),
  ]),
  editorCase("multiple capturing and non-capturing overlays restore focus through removals", ["c1", "n1", "c2", "n2"], [
    show("c1"), show("n1", { nonCapturing: true }), show("c2"), show("n2", { nonCapturing: true }), observe("c2"),
    hide("c2"), observe("c1"), hide("c1"), observe("editor"),
  ]),
  editorCase("capturing overlay unfocus() on topmost capturing overlay falls back to preFocus", ["capturing"], [show("capturing"), observe("capturing"), unfocus("capturing"), observe("editor")]),
  editorCase("focus() on hidden overlay is a no-op", ["overlay"], [show("overlay", { nonCapturing: true }), hidden("overlay", true), focus("overlay"), observe("hidden")]),
  editorCase("focus() after hide() is a no-op", ["overlay"], [show("overlay", { nonCapturing: true }), hide("overlay"), focus("overlay"), observe("removed")]),
  editorCase("unfocus() when overlay does not have focus is a no-op", ["overlay"], [show("overlay", { nonCapturing: true }), unfocus("overlay"), observe("passive")]),
  { name: "unfocus() with null preFocus clears focus and does not route input back to overlay", components: ["overlay"], initialFocus: null, operations: [show("overlay"), unfocus("overlay"), input("x"), observe("cleared")] },
  editorCase("toggle focus between non-capturing overlays then unfocus returns to editor", ["a", "b"], [
    show("a", { nonCapturing: true }), show("b", { nonCapturing: true }), focus("a"), focus("b"), focus("a"), unfocus("a"), observe("editor"),
  ]),
  editorCase("explicit unfocus target supports cycling between three overlays and editor", ["a", "b", "c"], [
    show("a"), show("b"), show("c"), focus("a"), input("a"), focus("b"), input("b"), focus("c"), input("c"),
    unfocus("c", "editor"), input("e"), focus("a"), input("A"), unfocus("a", "editor"), input("E"), observe("cycled"),
  ]),
  { name: "explicit null unfocus target clears focus without restoring overlays", components: ["overlay"], initialFocus: null, operations: [show("overlay"), unfocus("overlay", null), input("x"), observe("cleared")] },
  editorCase("hiding focused overlay falls back to next visual-frontmost overlay", ["a", "b", "c"], [
    show("a"), show("b"), show("c"), focus("a"), focus("b"), hidden("b", true), input("x"), observe("a"),
  ]),
  editorCase("focus() on already-focused overlay bumps visual order", ["a", "b", "c"], [
    show("a", visualOptions), show("b", visualOptions), focus("a"), show("c", visualOptions), observe("c", ["a", "b", "c"]), focus("a"), observe("a", ["a", "b", "c"]),
  ], { width: 20, rows: 6, nonFocusable: ["a", "b", "c"] }),
  { name: "default rendering order for overlapping overlays follows creation order", components: ["a", "b"], nonFocusable: ["a", "b"], initialFocus: null, width: 20, rows: 6, operations: [show("a", visualOptions), show("b", visualOptions), observe("b", ["a", "b"])] },
  { name: "focus() on lower overlay renders it on top", components: ["a", "b"], nonFocusable: ["a", "b"], initialFocus: null, width: 20, rows: 6, operations: [show("a", visualOptions), show("b", visualOptions), observe("b", ["a", "b"]), focus("a"), observe("a", ["a", "b"])] },
  { name: "focusing middle overlay places it on top while preserving others relative order", components: ["a", "b", "c"], nonFocusable: ["a", "b", "c"], initialFocus: null, width: 20, rows: 6, operations: [
    show("a", visualOptions), show("b", visualOptions), show("c", visualOptions), observe("c", ["a", "b", "c"]), focus("b"), observe("b", ["a", "b", "c"]), hide("b"), observe("c-again", ["a", "b", "c"]), hide("c"), observe("a", ["a", "b", "c"]),
  ] },
  { name: "capturing overlay hidden and shown again renders on top after unhide", components: ["a", "b", "c"], nonFocusable: ["a", "b", "c"], initialFocus: null, width: 20, rows: 6, operations: [
    show("a", visualOptions), show("b", { row: 0, col: 0, width: 1 }), observe("b", ["a", "b", "c"]), hidden("b", true), show("c", visualOptions), observe("c", ["a", "b", "c"]), hidden("b", false), observe("b-again", ["a", "b", "c"]),
  ] },
  editorCase("unfocus() does not change visual order until another overlay is focused", ["a", "b"], [
    show("a", visualOptions), show("b", visualOptions), observe("b", ["a", "b"]), focus("a"), observe("a", ["a", "b"]), unfocus("a"), observe("a-unfocused", ["a", "b"]), focus("b"), observe("b-focused", ["a", "b"]),
  ], { width: 20, rows: 6, nonFocusable: ["a", "b"] }),
];

function traceLine(name: string): string {
  return name.length === 1 ? name.toUpperCase() : `@@${name}@@`;
}

async function generateOverlayFocus(tui: TuiModule): Promise<FocusTraceCase[]> {
  const cases: FocusTraceCase[] = [];
  for (const spec of focusTraceSpecs) {
    const terminal = new CoreTerminal(spec.width ?? 80, spec.rows ?? 24);
    const ui = new tui.TUI(terminal);
    const nonFocusable = new Set(spec.nonFocusable ?? []);
    const components: Record<string, Component> = Object.fromEntries(spec.components.map((name) => [
      name,
      nonFocusable.has(name) ? new PlainComponent([traceLine(name)]) : new StaticComponent([traceLine(name)]),
    ]));
    const root = new tui.Container();
    ui.addChild(root);
    const mount = (names: string[]) => {
      root.clear();
      for (const name of names) root.addChild(components[name]);
    };
    mount(spec.mounted ?? []);
    ui.setFocus(spec.initialFocus == null ? null : components[spec.initialFocus]);
    const flags = { ...(spec.flags ?? {}) };
    const handles: Record<string, OverlayHandle> = {};
    const pending: FocusTraceOperation[][] = [];
    let applyOperation: (operation: FocusTraceOperation) => void;
    applyOperation = (operation) => {
      switch (operation.op) {
        case "show": {
          const options = operation.options;
          const runtime = options ? {
            ...options,
            ...(options.visibleFlag ? { visible: () => flags[options.visibleFlag!] ?? false } : {}),
          } : undefined;
          if (runtime) delete (runtime as { visibleFlag?: string }).visibleFlag;
          handles[operation.handle] = ui.showOverlay(components[operation.component], runtime);
          break;
        }
        case "focus": handles[operation.handle].focus(); break;
        case "hide": handles[operation.handle].hide(); break;
        case "setHidden": handles[operation.handle].setHidden(operation.hidden); break;
        case "unfocus":
          if (Object.hasOwn(operation, "target")) {
            handles[operation.handle].unfocus({ target: operation.target == null ? null : components[operation.target] });
          } else {
            handles[operation.handle].unfocus();
          }
          break;
        case "hideOverlay": ui.hideOverlay(); break;
        case "setFocus": ui.setFocus(operation.target == null ? null : components[operation.target]); break;
        case "setFlag": flags[operation.flag] = operation.value; break;
        case "mount": mount(operation.components); break;
        case "input": terminal.send(operation.data); break;
        case "schedule": pending.push(operation.operations); break;
        case "flush": {
          const scheduled = pending.splice(0);
          for (const operations of scheduled) for (const nested of operations) applyOperation(nested);
          break;
        }
        case "observe": throw new Error("observe must be handled by the async trace loop");
      }
    };
    for (const entry of spec.handlers ?? []) {
      const component = components[entry.component] as StaticComponent;
      component.onInput = (data) => {
        if (data === entry.data) for (const operation of entry.operations) applyOperation(operation);
      };
    }
    ui.start();
    terminal.writes = [];
    const expected: FocusTraceObservation[] = [];
    for (const operation of spec.operations) {
      if (operation.op !== "observe") {
        applyOperation(operation);
        continue;
      }
      let front: string | null | undefined;
      if (operation.probe) {
        terminal.writes = [];
        await forceRender(ui);
        const output = terminal.writes.join("");
        let lastMarker = -1;
        front = null;
        for (const name of operation.probe) {
          const marker = output.lastIndexOf(traceLine(name));
          if (marker > lastMarker) {
            lastMarker = marker;
            front = name;
          }
        }
      }
      const inputs = Object.fromEntries(Object.entries(components)
        .filter(([, component]) => (component.inputs?.length ?? 0) > 0)
        .map(([name, component]) => [name, [...(component.inputs ?? [])]]));
      const handleStates = Object.fromEntries(Object.entries(handles).map(([name, handle]) => [name, {
        hidden: handle.isHidden(),
        focused: handle.isFocused(),
      }]));
      expected.push({
        label: operation.label,
        focused: Object.entries(components).filter(([, component]) => component.focused === true).map(([name]) => name),
        inputs,
        handles: handleStates,
        hasOverlay: ui.hasOverlay(),
        ...(operation.probe ? { front } : {}),
      });
    }
    ui.stop();
    cases.push({ ...spec, expected });
  }
  return cases;
}

const oscParserInputs = [
  ["rgb-16-bit", "\u001b]11;rgb:0000/8000/ffff\u0007"],
  ["rgba-single-digit", "\u001b]11;rgba:f/8/0/f\u0007"],
  ["hex-6-bel", "\u001b]11;#ffffff\u0007"],
  ["hex-6-black", "\u001b]11;#000000\u0007"],
  ["hex-6-st", "\u001b]11;#0080ff\u001b\\"],
  ["hex-12", "\u001b]11;#00008000ffff\u0007"],
  ["strict-unparseable", "\u001b]11;not-a-color\u0007"],
  ["leading-data", "x\u001b]11;#ffffff\u0007"],
  ["wrong-osc", "\u001b]10;#ffffff\u0007"],
  ["trailing-data", "\u001b]11;#ffffff\u0007x"],
] as const;

async function generateTerminalColors(tui: TuiModule, colors: TerminalColorsModule): Promise<Record<string, unknown>> {
  const parserCases = oscParserInputs.map(([name, input]) => ({
    name,
    input,
    isResponse: colors.isOsc11BackgroundColorResponse(input),
    expected: colors.parseOsc11BackgroundColor(input) ?? null,
  }));
  const schemeParserCases = ["\u001b[?997;1n", "\u001b[?997;2n", "\u001b[?997;3n", "\u001b[?996n", "x\u001b[?997;1n"]
    .map((input) => ({ input, expected: colors.parseTerminalColorSchemeReport(input) ?? null }));

  const oscQueryWrites = (terminal: CoreTerminal) => terminal.writes.filter((write) => write === "\u001b]11;?\u0007");
  const backgroundCases: Array<Record<string, unknown>> = [];
  {
    const terminal = new CoreTerminal(80, 24);
    const ui = new tui.TUI(terminal);
    ui.start();
    const query = ui.queryTerminalBackgroundColor({ timeoutMs: 1_000 });
    terminal.send("\u001b]11;#ffffff\u0007");
    backgroundCases.push({
      name: "writes OSC 11 query and resolves with the parsed RGB reply",
      scenario: "valid",
      writes: oscQueryWrites(terminal),
      result: (await query) ?? null,
      listenerInputs: [],
      focusedInputs: [],
    });
    ui.stop();
  }
  {
    const terminal = new CoreTerminal(80, 24);
    const ui = new tui.TUI(terminal);
    const focused = new StaticComponent(["INPUT"]);
    const listenerInputs: string[] = [];
    ui.addChild(focused);
    ui.setFocus(focused);
    ui.addInputListener((data) => { listenerInputs.push(data); return undefined; });
    ui.start();
    const query = ui.queryTerminalBackgroundColor({ timeoutMs: 1_000 });
    terminal.send("\u001b]11;#000000\u0007");
    backgroundCases.push({
      name: "consumes OSC 11 replies before input listeners and focused component dispatch",
      scenario: "consumed",
      writes: oscQueryWrites(terminal),
      result: (await query) ?? null,
      listenerInputs,
      focusedInputs: focused.inputs,
    });
    ui.stop();
  }
  {
    const terminal = new CoreTerminal(80, 24);
    const ui = new tui.TUI(terminal);
    const focused = new StaticComponent(["INPUT"]);
    const listenerInputs: string[] = [];
    ui.addChild(focused);
    ui.setFocus(focused);
    ui.addInputListener((data) => { listenerInputs.push(data); return undefined; });
    ui.start();
    const query = ui.queryTerminalBackgroundColor({ timeoutMs: 1_000 });
    terminal.send("\u001b]11;not-a-color\u0007");
    backgroundCases.push({
      name: "consumes unparseable strict OSC 11 replies and resolves undefined",
      scenario: "invalid",
      writes: oscQueryWrites(terminal),
      result: (await query) ?? null,
      listenerInputs,
      focusedInputs: focused.inputs,
    });
    ui.stop();
  }
  {
    const terminal = new CoreTerminal(80, 24);
    const ui = new tui.TUI(terminal);
    const focused = new StaticComponent(["INPUT"]);
    const listenerInputs: string[] = [];
    ui.addChild(focused);
    ui.setFocus(focused);
    ui.addInputListener((data) => { listenerInputs.push(data); return undefined; });
    ui.start();
    let settled = false;
    const query = ui.queryTerminalBackgroundColor({ timeoutMs: 1_000 }).then((value) => {
      settled = true;
      return value;
    });
    terminal.send("x");
    await Promise.resolve();
    const settledAfterNonMatchingInput = settled;
    terminal.send("\u001b]11;#ffffff\u0007");
    backgroundCases.push({
      name: "dispatches non-matching input normally while waiting for an OSC 11 reply",
      scenario: "nonMatching",
      writes: oscQueryWrites(terminal),
      result: (await query) ?? null,
      settledAfterNonMatchingInput,
      listenerInputs,
      focusedInputs: focused.inputs,
    });
    ui.stop();
  }
  {
    const terminal = new CoreTerminal(80, 24);
    const ui = new tui.TUI(terminal);
    const focused = new StaticComponent(["INPUT"]);
    const listenerInputs: string[] = [];
    ui.addChild(focused);
    ui.setFocus(focused);
    ui.addInputListener((data) => { listenerInputs.push(data); return undefined; });
    ui.start();
    const query = ui.queryTerminalBackgroundColor({ timeoutMs: 1 });
    await new Promise((resolve) => setTimeout(resolve, 5));
    const result = await query;
    terminal.send("\u001b]11;#ffffff\u0007");
    backgroundCases.push({
      name: "keeps consuming a late OSC 11 reply after timeout",
      scenario: "late",
      writes: oscQueryWrites(terminal),
      result: result ?? null,
      listenerInputs,
      focusedInputs: focused.inputs,
    });
    ui.stop();
  }

  const queueTerminal = new CoreTerminal(80, 24);
  const queueUI = new tui.TUI(queueTerminal);
  const queueFocused = new StaticComponent(["INPUT"]);
  const queueListenerInputs: string[] = [];
  queueUI.setFocus(queueFocused);
  queueUI.addInputListener((data) => { queueListenerInputs.push(data); return undefined; });
  const first = queueUI.queryTerminalBackgroundColor({ timeoutMs: 1 });
  await new Promise((resolve) => setTimeout(resolve, 5));
  const firstResult = await first;
  let secondSettled = false;
  const second = queueUI.queryTerminalBackgroundColor({ timeoutMs: 1_000 }).then((value) => {
    secondSettled = true;
    return value;
  });
  handleInput(queueUI, "\u001b]11;#111111\u0007");
  await Promise.resolve();
  const settledAfterFirstLateReply = secondSettled;
  handleInput(queueUI, "\u001b]11;rgb:ffff/0000/8000\u001b\\");
  const secondResult = await second;
  handleInput(queueUI, "x");

  const schemeTerminal = new CoreTerminal(80, 24);
  const schemeUI = new tui.TUI(schemeTerminal);
  const schemeEvents: string[] = [];
  schemeUI.onTerminalColorSchemeChange((scheme) => schemeEvents.push(scheme));
  const schemeQuery = schemeUI.queryTerminalColorScheme({ timeoutMs: 1_000 });
  handleInput(schemeUI, "\u001b[?997;2n");
  const schemeResult = await schemeQuery;

  const schemeTimeoutTerminal = new CoreTerminal(80, 24);
  const schemeTimeoutUI = new tui.TUI(schemeTimeoutTerminal);
  const schemeTimeoutFocused = new StaticComponent(["INPUT"]);
  const schemeTimeoutListenerInputs: string[] = [];
  const schemeTimeoutEvents: string[] = [];
  schemeTimeoutUI.setFocus(schemeTimeoutFocused);
  schemeTimeoutUI.addInputListener((data) => { schemeTimeoutListenerInputs.push(data); return undefined; });
  schemeTimeoutUI.onTerminalColorSchemeChange((scheme) => schemeTimeoutEvents.push(scheme));
  const timedOutScheme = schemeTimeoutUI.queryTerminalColorScheme({ timeoutMs: 1 });
  await new Promise((resolve) => setTimeout(resolve, 5));
  const timedOutSchemeResult = await timedOutScheme;
  handleInput(schemeTimeoutUI, "\u001b[?997;1n");

  const concurrentTerminal = new CoreTerminal(80, 24);
  const concurrentUI = new tui.TUI(concurrentTerminal);
  const concurrentEvents: string[] = [];
  concurrentUI.onTerminalColorSchemeChange((scheme) => concurrentEvents.push(scheme));
  const concurrentFirst = concurrentUI.queryTerminalColorScheme({ timeoutMs: 1_000 });
  const concurrentSecond = concurrentUI.queryTerminalColorScheme({ timeoutMs: 1_000 });
  handleInput(concurrentUI, "\u001b[?997;2n");
  const concurrentResults = [await concurrentFirst, await concurrentSecond];

  const listenerTerminal = new CoreTerminal(80, 24);
  const listenerUI = new tui.TUI(listenerTerminal);
  const listenerOrder: string[] = [];
  const removeFirstListener = listenerUI.onTerminalColorSchemeChange((scheme) => listenerOrder.push(`first:${scheme}`));
  listenerUI.onTerminalColorSchemeChange((scheme) => listenerOrder.push(`second:${scheme}`));
  handleInput(listenerUI, "\u001b[?997;1n");
  removeFirstListener();
  handleInput(listenerUI, "\u001b[?997;2n");

  const mutationTerminal = new CoreTerminal(80, 24);
  const mutationUI = new tui.TUI(mutationTerminal);
  const listenerMutationOrder: string[] = [];
  let removeSecondListener = () => {};
  mutationUI.onTerminalColorSchemeChange((scheme) => {
    listenerMutationOrder.push(`first:${scheme}`);
    removeSecondListener();
    mutationUI.onTerminalColorSchemeChange((addedScheme) => listenerMutationOrder.push(`added:${addedScheme}`));
  });
  removeSecondListener = mutationUI.onTerminalColorSchemeChange((scheme) => listenerMutationOrder.push(`second:${scheme}`));
  handleInput(mutationUI, "\u001b[?997;1n");

  const notificationsTerminal = new CoreTerminal(80, 24);
  const notificationsUI = new tui.TUI(notificationsTerminal);
  notificationsUI.setTerminalColorSchemeNotifications(true);
  notificationsUI.setTerminalColorSchemeNotifications(true);
  notificationsUI.start();
  notificationsUI.setTerminalColorSchemeNotifications(false);
  notificationsUI.setTerminalColorSchemeNotifications(false);
  notificationsUI.stop();
  const notificationWrites = notificationsTerminal.writes.filter((write) => write.includes("2031"));

  const stopNotificationsTerminal = new CoreTerminal(80, 24);
  const stopNotificationsUI = new tui.TUI(stopNotificationsTerminal);
  stopNotificationsUI.setTerminalColorSchemeNotifications(true);
  stopNotificationsUI.start();
  stopNotificationsUI.stop();
  const notificationStopWrites = stopNotificationsTerminal.writes.filter((write) => write.includes("2031"));

  return {
    schemaVersion: 1,
    parserCases,
    schemeParserCases,
    backgroundCases,
    backgroundLateQueue: {
      writes: oscQueryWrites(queueTerminal),
      firstResult: firstResult ?? null,
      settledAfterFirstLateReply,
      secondResult: secondResult ?? null,
      listenerInputs: queueListenerInputs,
      focusedInputs: queueFocused.inputs,
    },
    schemeProtocols: {
      query: { writes: schemeTerminal.writes.filter((write) => write === "\u001b[?996n"), result: schemeResult ?? null, events: schemeEvents },
      timeoutLate: {
        writes: schemeTimeoutTerminal.writes.filter((write) => write === "\u001b[?996n"),
        result: timedOutSchemeResult ?? null,
        events: schemeTimeoutEvents,
        listenerInputs: schemeTimeoutListenerInputs,
        focusedInputs: schemeTimeoutFocused.inputs,
      },
      concurrent: {
        writes: concurrentTerminal.writes.filter((write) => write === "\u001b[?996n"),
        results: concurrentResults,
        events: concurrentEvents,
      },
      listenerOrder,
      listenerMutationOrder,
    },
    notificationWrites,
    notificationStopWrites,
  };
}

export async function generateF12Core(
  upstreamRoot: string,
  familyDir: string,
): Promise<{ files: string[]; sources: string[] }> {
  const tuiSource = "packages/tui/src/index.ts";
  const terminalColorsSource = "packages/tui/src/terminal-colors.ts";
  const focusTestSource = "packages/tui/test/overlay-non-capturing.test.ts";
  const optionsTestSource = "packages/tui/test/overlay-options.test.ts";
  const conformanceSources = [
    "packages/tui/src/tui.ts",
    "packages/tui/src/utils.ts",
    terminalColorsSource,
    optionsTestSource,
    focusTestSource,
    "packages/tui/test/overlay-short-content.test.ts",
    "packages/tui/test/regression-overlay-cjk-boundary.test.ts",
    "packages/tui/test/tui-overlay-style-leak.test.ts",
    "packages/tui/test/tab-width.test.ts",
    "packages/tui/test/terminal-colors.test.ts",
  ];
  const tui = (await import(pathToFileURL(path.join(upstreamRoot, tuiSource)).href)) as TuiModule;
  const colors = (await import(pathToFileURL(path.join(upstreamRoot, terminalColorsSource)).href)) as TerminalColorsModule;
  const upstreamFocusNames = namedTests(await readFile(path.join(upstreamRoot, focusTestSource), "utf8"));
  const upstreamOptionNames = namedTests(await readFile(path.join(upstreamRoot, optionsTestSource), "utf8"));
  assertExactNames("overlay focus", upstreamFocusNames, focusTraceSpecs.map((fixtureCase) => fixtureCase.name));
  assertExactNames("overlay options", upstreamOptionNames, Object.keys(overlayOptionCoverage));
  tui.setCapabilities({ images: null, trueColor: false, hyperlinks: false });
  try {
    const overlays = {
      schemaVersion: 1,
      renderCases: await generateOverlayRenders(tui),
      focusCases: await generateOverlayFocus(tui),
      cursorTrace: await generateOverlayCursorTrace(tui),
      upstreamCoverage: { focusCaseNames: upstreamFocusNames, overlayOptionCases: overlayOptionCoverage },
    };
    const terminalColors = await generateTerminalColors(tui, colors);
    await writeFile(path.join(familyDir, "overlays.json"), `${JSON.stringify(overlays, null, 2)}\n`);
    await writeFile(path.join(familyDir, "terminal-colors.json"), `${JSON.stringify(terminalColors, null, 2)}\n`);
  } finally {
    tui.resetCapabilitiesCache();
  }
  return { files: ["overlays.json", "terminal-colors.json"], sources: conformanceSources };
}
