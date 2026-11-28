import test from "node:test";
import assert from "node:assert/strict";
import { stripAnsi, hasVisibleText, named, tidy, peerActivity, busyActivity, collectLiveActivity } from "../src/lib/utils.js";

test("stripAnsi removes CSI escape sequences and bare SGR codes", () => {
  assert.equal(stripAnsi("\x1b[1;32mhello\x1b[0m world"), "hello world");
  assert.equal(stripAnsi("[38;5;240mdim[0m"), "dim");
  assert.equal(stripAnsi(""), "");
});

test("hasVisibleText ignores lone status dots/ANSI and detects real text", () => {
  assert.equal(hasVisibleText(["○"]), false);
  assert.equal(hasVisibleText(["●"]), false);
  assert.equal(hasVisibleText(["\x1b[32m●\x1b[0m   "]), false);
  assert.equal(hasVisibleText([]), false);
  assert.equal(hasVisibleText(["○ 2 tasks"]), true);
  assert.equal(hasVisibleText(["\x1b[1mready\x1b[0m"]), true);
});

test("named capitalizes first character and defaults to New session", () => {
  assert.equal(named("deploy check"), "Deploy check");
  assert.equal(named(""), "New session");
  assert.equal(named(null), "New session");
});

test("tidy replaces /Users/<name> and /home/<name> with ~", () => {
  assert.equal(tidy("/Users/alice/dev/orb"), "~/dev/orb");
  assert.equal(tidy("/home/bob/work"), "~/work");
  assert.equal(tidy("/srv/agents/buzz"), "/srv/agents/buzz");
  assert.equal(tidy(""), "");
});

test("peerActivity distinguishes working agents from idle open processes", () => {
  assert.deepEqual(peerActivity(null), { working: 0, open: 0, label: "" });
  assert.deepEqual(peerActivity({ running: [] }), { working: 0, open: 0, label: "" });
  assert.deepEqual(
    peerActivity({ running: [{ busy: true }] }),
    { working: 1, open: 0, label: "1 working" },
  );
  assert.deepEqual(
    peerActivity({ running: [{ busy: true }, { busy: true }, { busy: false }] }),
    { working: 2, open: 1, label: "2 working · 1 open" },
  );
  assert.deepEqual(
    peerActivity({ running: [{ busy: false }, { busy: false }] }),
    { working: 0, open: 2, label: "2 open" },
  );
});

test("busyActivity returns active tool verb, live run now, tab status, or working", () => {
  assert.equal(busyActivity({ busy: false }, []), "");
  assert.equal(busyActivity({ busy: true }, []), "working");
  assert.equal(busyActivity({ busy: true, status: "analyzing diff…" }, []), "analyzing diff…");
  assert.equal(
    busyActivity({ busy: true }, [
      { kind: "run", live: true, now: "bash · running", actions: [] },
    ]),
    "bash · running",
  );
  assert.equal(
    busyActivity({ busy: true }, [
      {
        kind: "run",
        live: true,
        now: "",
        actions: [
          { verb: "read", live: false },
          { verb: "bash", live: true },
        ],
      },
    ]),
    "bash · running",
  );
});

test("collectLiveActivity returns empty structure when idle", () => {
  const res = collectLiveActivity(
    [{ id: "t1", title: "idle", peer: "p1", busy: false, ask: null }],
    {},
    { machines: [{ id: "p1", name: "lab", hue: 2, running: [{ instance: "i1", busy: false, open: true }] }] },
  );
  assert.deepEqual(res.inputs, []);
  assert.deepEqual(res.agents, []);
  assert.deepEqual(res.remotes, []);
  assert.equal(res.totalRunning, 0);
  assert.equal(res.summary, "");
});

test("collectLiveActivity collects inputs, running agents with processes, and unopened remotes", () => {
  const home = {
    machines: [
      { id: "lab", name: "ordalie-lab-3", hue: 2, running: [{ instance: "i1", label: "open", busy: true, open: true }] },
      {
        id: "mac",
        name: "macbook-pro",
        hue: 1,
        running: [{ instance: "i-remote", label: "Deploy worker", busy: true, open: false }],
      },
    ],
  };
  const tabs = [
    { id: "t-run", title: "refactor auth", peer: "lab", where: "ordalie-lab-3", busy: true, ask: null, status: "" },
    { id: "t-bg", title: "audit SQL", peer: "lab", where: "ordalie-lab-3", busy: true, ask: null, status: "analyzing diff…" },
    {
      id: "t-ask",
      title: "release preflight",
      peer: "mac",
      where: "macbook-pro",
      busy: false,
      ask: { title: "Approve migration lock check?" },
    },
  ];
  const rowsByTab = {
    "t-run": [
      {
        k: "r1",
        kind: "run",
        live: true,
        actions: [
          { k: "a1", verb: "read", target: "auth.go", live: false },
          { k: "a2", verb: "bash", target: "go test -race ./internal/auth/...", live: true },
        ],
      },
      {
        k: "r2",
        kind: "peer",
        live: true,
        exchange: { scope: "subagent", who: "scout", text: "inspect locks", steps: "read auth.go", live: true },
      },
    ],
    "t-bg": [],
  };

  const res = collectLiveActivity(tabs, rowsByTab, home);
  assert.equal(res.inputs.length, 1);
  assert.equal(res.inputs[0].tabId, "t-ask");
  assert.equal(res.inputs[0].prompt, "Approve migration lock check?");
  assert.equal(res.inputs[0].hue, 1);

  assert.equal(res.agents.length, 2);
  assert.equal(res.agents[0].processes.length, 2);
  assert.equal(res.agents[0].processes[0].verb, "bash");
  assert.equal(res.agents[0].processes[0].target, "go test -race ./internal/auth/...");
  assert.equal(res.agents[0].processes[1].kind, "subagent");
  assert.equal(res.agents[0].processes[1].verb, "scout");

  assert.equal(res.agents[1].processes.length, 1);
  assert.equal(res.agents[1].processes[0].kind, "status");
  assert.equal(res.agents[1].processes[0].target, "analyzing diff…");

  assert.equal(res.remotes.length, 1);
  assert.equal(res.remotes[0].instance, "i-remote");
  assert.equal(res.remotes[0].label, "Deploy worker");

  assert.equal(res.totalRunning, 3);
  assert.equal(res.summary, "3 running · 1 input · bash: go test -race ./internal/auth/...");
});
