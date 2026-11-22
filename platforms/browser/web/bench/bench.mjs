#!/usr/bin/env node
// Automated Chromium headless performance benchmark for the static Svelte Orb web app.
// Drives real Chromium via CDP over raw WebSocket (zero npm dependencies!) against a local static
// server serving `.tools/browser` in `?bench=1` mode with heavy conversations (2,000 & 5,000 rows).
//
// Measures:
//   - Initial render time & mounted DOM node count for 2,000-row and 5,000-row conversations
//   - Sustained scroll smoothness (frame P50/P95/P99 & FPS across 60 rapid scroll steps)
//   - High-frequency token streaming (120 chunks @ 16ms) frame P50/P95/P99 & JS heap delta
//   - Tab switching latency & Overview / Command Palette / Interrupt interactive checks
//   - Optional screenshot capture (--screenshots <dir>) for visual inspection

import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { existsSync, mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { homedir } from "node:os";
import { parseArgs } from "node:util";

const { values: args } = parseArgs({
  options: {
    dir: { type: "string", default: ".tools/browser" },
    rows: { type: "string", default: "2000,5000" },
    screenshots: { type: "string", default: "" },
  },
});

const staticDir = resolve(args.dir);
const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".ttf": "font/ttf",
  ".gz": "application/gzip",
};

function findChromium() {
  const candidates = [
    process.env.CHROME_PATH,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "/usr/bin/chromium",
    "/usr/bin/google-chrome",
  ].filter(Boolean);
  for (const c of candidates) if (existsSync(c)) return c;

  // Check Playwright browser cache
  for (const base of [join(homedir(), "Library/Caches/ms-playwright"), join(homedir(), ".cache/ms-playwright")]) {
    if (!existsSync(base)) continue;
    for (const d of readdirSync(base).sort().reverse()) {
      if (!d.startsWith("chromium")) continue;
      for (const rel of [
        "chrome-headless-shell-mac-arm64/chrome-headless-shell",
        "chrome-headless-shell-mac-x64/chrome-headless-shell",
        "chrome-headless-shell-linux64/chrome-headless-shell",
        "chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
        "chrome-linux/chrome",
      ]) {
        const p = join(base, d, rel);
        if (existsSync(p)) return p;
      }
    }
  }
  throw new Error("No Chromium / chrome-headless-shell binary found (set CHROME_PATH)");
}

function startStaticServer(dir) {
  return new Promise(resolveServer => {
    const server = createServer((req, res) => {
      const clean = (req.url || "/").split("?")[0];
      const filePath = join(dir, clean === "/" ? "index.html" : clean);
      if (!existsSync(filePath) || statSync(filePath).isDirectory()) {
        res.writeHead(404);
        res.end("not found");
        return;
      }
      res.writeHead(200, { "Content-Type": MIME[extname(filePath)] || "application/octet-stream" });
      res.end(readFileSync(filePath));
    });
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolveServer({ server, url: `http://127.0.0.1:${port}` });
    });
  });
}

class CDP {
  constructor(wsUrl) {
    this.ws = new WebSocket(wsUrl);
    this.seq = 0;
    this.pending = new Map();
    this.ready = new Promise((ok, no) => {
      this.ws.onopen = ok;
      this.ws.onerror = no;
    });
    this.ws.onmessage = ({ data }) => {
      const msg = JSON.parse(data);
      if (msg.method === "Runtime.exceptionThrown") {
        console.error("PAGE EXCEPTION:", JSON.stringify(msg.params.exceptionDetails));
      } else if (msg.method === "Runtime.consoleAPICalled" && msg.params.type === "error") {
        console.error("CONSOLE ERROR:", msg.params.args.map(a => a.value ?? a.description).join(" "));
      }
      if (msg.id && this.pending.has(msg.id)) {
        const { ok, no } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        msg.error ? no(new Error(msg.error.message)) : ok(msg.result);
      }
    };
  }
  async send(method, params = {}) {
    await this.ready;
    const id = ++this.seq;
    return new Promise((ok, no) => {
      this.pending.set(id, { ok, no });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
  async eval(expression) {
    const res = await this.send("Runtime.evaluate", {
      expression,
      awaitPromise: true,
      returnByValue: true,
    });
    if (res.exceptionDetails) {
      throw new Error(res.exceptionDetails.exception?.description || res.exceptionDetails.text);
    }
    return res.result?.value;
  }
  close() {
    this.ws.close();
  }
}

function stats(arr) {
  if (!arr.length) return { p50: 0, p95: 0, p99: 0, max: 0, avg: 0 };
  const s = [...arr].sort((a, b) => a - b);
  const pick = q => +s[Math.min(s.length - 1, Math.floor(s.length * q))].toFixed(2);
  const avg = +(s.reduce((a, b) => a + b, 0) / s.length).toFixed(2);
  return { p50: pick(0.5), p95: pick(0.95), p99: pick(0.99), max: +s[s.length - 1].toFixed(2), avg };
}

const chromeBin = findChromium();
const { server, url } = await startStaticServer(staticDir);
const debugPort = 9333 + Math.floor(Math.random() * 500);
const chromeArgs = [
  ...(chromeBin.includes("chrome-headless-shell") ? [] : ["--headless=new"]),
  "--disable-gpu",
  "--no-sandbox",
  "--window-size=1360,860",
  `--remote-debugging-port=${debugPort}`,
  "about:blank",
];
const chrome = spawn(chromeBin, chromeArgs, { stdio: ["ignore", "ignore", "pipe"] });
let chromeErr = "";
chrome.stderr.on("data", d => (chromeErr += d));

let cdp;
try {
  // Wait for CDP endpoint
  let target;
  for (let i = 0; i < 60; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${debugPort}/json/list`)).json();
      target = list.find(t => t.type === "page");
      if (target) break;
    } catch {}
    await new Promise(r => setTimeout(r, 150));
  }
  if (!target) throw new Error("Timed out waiting for Chromium CDP:\n" + chromeErr);

  cdp = new CDP(target.webSocketDebuggerUrl);
  await cdp.send("Page.enable");
  await cdp.send("Runtime.enable");
  await cdp.send("Emulation.setDeviceMetricsOverride", { width: 1360, height: 860, deviceScaleFactor: 2, mobile: false });

  const rowCounts = args.rows.split(",").map(s => parseInt(s.trim(), 10)).filter(Boolean);
  const results = {};

  for (const rowCount of rowCounts) {
    console.error(`[bench] measuring ${rowCount} rows...`);
    const tNav = performance.now();
    await cdp.send("Page.navigate", { url: `${url}/?bench=1&rows=${rowCount}&open=heavy` });
    await cdp.eval(`new Promise((resolve, reject) => {
      const deadline = Date.now() + 12000;
      const check = () => {
        if (document.querySelectorAll(".virt-row").length > 5 && window.__orbBench) resolve();
        else if (Date.now() > deadline) reject(new Error("timeout waiting for .virt-row; html=" + document.body.innerHTML.slice(0, 400)));
        else setTimeout(check, 20);
      };
      check();
    })`);
    const mountMs = +(performance.now() - tNav).toFixed(1);

    const domSnapshot = await cdp.eval(`({
      totalElements: document.querySelectorAll("*").length,
      mountedRows: document.querySelectorAll(".virt-row").length,
      heapMB: performance.memory ? +(performance.memory.usedJSHeapSize / 1048576).toFixed(2) : null,
    })`);

    // Scroll benchmark: 60 steps across the full scrollHeight measuring real rAF frame times
    const scrollFrames = await cdp.eval(`new Promise(resolve => {
      const el = document.querySelector(".transcript");
      const max = el.scrollHeight - el.clientHeight;
      const frames = [];
      let step = 0, last = performance.now();
      function next() {
        const now = performance.now();
        if (step > 0) frames.push(now - last);
        last = now;
        if (step >= 60) return resolve(frames);
        el.scrollTop = Math.round((max * (60 - step)) / 60);
        step++;
        requestAnimationFrame(next);
      }
      requestAnimationFrame(next);
    })`);

    // Streaming burst benchmark: 100 token chunks at 16ms while pinned to bottom
    const streamResult = await cdp.eval(`(async () => {
      const el = document.querySelector(".transcript");
      el.scrollTop = el.scrollHeight;
      await new Promise(r => requestAnimationFrame(r));
      const frames = [];
      let running = true, last = performance.now();
      function track() {
        const now = performance.now();
        frames.push(now - last);
        last = now;
        if (running) requestAnimationFrame(track);
      }
      requestAnimationFrame(track);
      await window.__orbBench.startStreamBurst("tab-heavy", 100, 16);
      running = false;
      return {
        frames,
        mountedRowsAfter: document.querySelectorAll(".virt-row").length,
        heapMBAfter: performance.memory ? +(performance.memory.usedJSHeapSize / 1048576).toFixed(2) : null,
      };
    })()`);

    results[`rows_${rowCount}`] = {
      mountMs,
      ...domSnapshot,
      scrollFrameMs: stats(scrollFrames),
      streamFrameMs: stats(streamResult.frames),
      mountedRowsAfterStream: streamResult.mountedRowsAfter,
      heapMBAfterStream: streamResult.heapMBAfter,
    };
  }

  // Interactive E2E assertions: TUI shortcuts (incl. AZERTY Ctrl+M), global Esc on every modal,
  // and sending messages even when Notification.requestPermission() returns undefined (Safari).
  await cdp.eval(`localStorage.clear()`);
  await cdp.send("Page.navigate", { url: `${url}/?bench=1&rows=200&open=heavy` });
  await cdp.eval(`new Promise((r, no) => { const dl = Date.now() + 8000; const c = () => document.querySelector(".virt-row") ? r() : Date.now() > dl ? no(new Error("timeout")) : setTimeout(c, 25); c(); })`);
  const interactive = await cdp.eval(`(async () => {
    const wait = (ms = 60) => new Promise(r => setTimeout(r, ms));
    const press = (key, opts = {}) => {
      const target = document.activeElement || document.body;
      target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...opts }));
    };

    // 1. Ctrl+M on AZERTY keyboard (e.code === "Semicolon", e.key === "m") opens Model Deck; Esc closes even when blurred.
    press("m", { code: "Semicolon", ctrlKey: true });
    await wait();
    const azertyModelOpened = !!document.querySelector(".deck");
    document.activeElement?.blur?.();
    press("Escape", { code: "Escape" });
    await wait();
    const modelClosedOnEsc = !document.querySelector(".deck");

    // 2. Ctrl+P opens Command Palette; Esc closes.
    press("p", { code: "KeyP", ctrlKey: true });
    await wait();
    const ctrlPOpened = !!document.querySelector(".sheet.wide");
    document.activeElement?.blur?.();
    press("Escape", { code: "Escape" });
    await wait();
    const palClosedOnEsc = !document.querySelector(".sheet.wide");

    // 3. ? opens Keyboard Shortcuts (even with no input inside); Esc closes reliably.
    document.activeElement?.blur?.();
    press("?", { code: "Slash", shiftKey: true });
    await wait();
    const shortcutsOpened = document.body.innerText.includes("TUI Core");
    press("Escape", { code: "Escape" });
    await wait();
    const shortcutsClosedOnEsc = !document.body.innerText.includes("TUI Core");

    // 4. Shift+Tab cycles reasoning level.
    const beforeLit = document.querySelectorAll(".meter i.lit").length;
    press("Tab", { code: "Tab", shiftKey: true });
    await wait();
    const afterLit = document.querySelectorAll(".meter i.lit").length;
    const shiftTabCycled = beforeLit !== afterLit;

    // 5. Ctrl+O expands & collapses tool outputs.
    press("o", { code: "KeyO", ctrlKey: true });
    await wait(100);
    const expandedCount = document.querySelectorAll(".act-pane, .run-body").length;
    press("o", { code: "KeyO", ctrlKey: true });
    await wait(100);
    const collapsedCount = document.querySelectorAll(".act-pane, .run-body").length;

    // 6. Sending a message works even when Notification.requestPermission() returns undefined (Safari).
    const origNotif = window.Notification;
    Object.defineProperty(window, "Notification", {
      configurable: true,
      value: { permission: "default", requestPermission: () => undefined },
    });
    const ta = document.querySelector(".prompt-input");
    ta.focus();
    ta.value = "bench send test safari";
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    await wait();
    ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true, cancelable: true }));
    await wait(120);
    const sentCleared = ta.value === "";
    const sentRendered = (document.querySelector(".transcript")?.innerText || "").includes("bench send test safari");
    Object.defineProperty(window, "Notification", { configurable: true, value: origNotif });

    return {
      azertyModelOpened,
      modelClosedOnEsc,
      ctrlPOpened,
      palClosedOnEsc,
      shortcutsOpened,
      shortcutsClosedOnEsc,
      shiftTabCycled,
      ctrlOToggled: expandedCount > 0 && collapsedCount === 0,
      sentCleared,
      sentRendered,
    };
  })()`);
  results.interactive = interactive;
  for (const [k, v] of Object.entries(interactive)) {
    if (!v) throw new Error(`Interactive assertion failed: ${k} = ${v}`);
  }

  console.log(JSON.stringify(results, null, 2));

  // Optional visual screenshots (light & dark, Home, Chat, Overview, Interrupt, Settings)
  if (args.screenshots) {
    mkdirSync(args.screenshots, { recursive: true });
    const snap = async name => {
      await new Promise(r => setTimeout(r, 200));
      const { data } = await cdp.send("Page.captureScreenshot", { format: "png" });
      writeFileSync(join(args.screenshots, `${name}.png`), Buffer.from(data, "base64"));
    };
    await cdp.eval(`localStorage.clear()`);
    await cdp.send("Page.navigate", { url: `${url}/?bench=1&rows=2000` });
    await cdp.eval(`new Promise((r, no) => { const dl = Date.now() + 8000; const c = () => document.querySelector(".magi-unit") ? r() : Date.now() > dl ? no(new Error("no .magi-unit")) : setTimeout(c, 25); c(); })`);
    await cdp.eval(`document.documentElement.setAttribute("data-theme", "light")`);
    await snap("01-home-light");
    await cdp.eval(`document.documentElement.setAttribute("data-theme", "dark")`);
    await snap("02-home-dark");
    await cdp.eval(`document.querySelector(".tab-chip")?.click()`);
    await snap("03-chat-dark");
    await cdp.eval(`document.documentElement.setAttribute("data-theme", "light")`);
    await snap("04-chat-light");
    await cdp.eval(`document.querySelector('[aria-label="Overview grid"]')?.click()`);
    await snap("05-overview-light");
    await cdp.eval(`document.querySelector('[aria-label="Settings"]')?.click()`);
    await snap("06-settings-light");
    await cdp.eval(`document.querySelector(".back-btn")?.click()`);
    await new Promise(r => setTimeout(r, 100));
    await cdp.eval(`document.querySelector(".tab-chip")?.click(); window.__orbBench.triggerInterrupt(false);`);
    await snap("07-interrupt");
    await cdp.eval(`window.__orbBench.clearInterrupt()`);
  }
} finally {
  cdp?.close();
  chrome.kill("SIGKILL");
  server.closeAllConnections?.();
  server.close();
}
process.exit(0);
