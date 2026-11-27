import test from "node:test";
import assert from "node:assert/strict";
import { createMockPeer } from "../src/lib/mock.js";

function makePeer() {
  globalThis.window ??= {};
  const frames = [];
  const peer = createMockPeer({
    emit: raw => frames.push(JSON.parse(raw)),
    rowCount: 12,
  });
  const latestState = () => frames.filter(f => f.t === "state").at(-1);
  const latestHome = () => frames.filter(f => f.t === "home").at(-1);
  const replyFor = id => frames.find(f => f.t === "reply" && f.id === id);
  return { peer, frames, latestState, latestHome, replyFor };
}

test("createMockPeer handles tab close + open (reopen), move, and rename", () => {
  const { peer, latestState, replyFor } = makePeer();
  peer.app(JSON.stringify({ do: "hello" }));

  const initialTabs = latestState().tabs.map(t => t.id);
  assert.deepEqual(initialTabs, ["tab-heavy", "tab-live"]);

  // Move tab-live to index 0
  peer.app(JSON.stringify({ do: "move", tab: "tab-live", to: 0 }));
  assert.deepEqual(latestState().tabs.map(t => t.id), ["tab-live", "tab-heavy"]);

  // Rename tab-live
  peer.app(JSON.stringify({ do: "rename", tab: "tab-live", name: "Renamed tab" }));
  assert.equal(latestState().tabs.find(t => t.id === "tab-live").title, "Renamed tab");

  // Close tab-live, then reopen it
  peer.app(JSON.stringify({ do: "close", tab: "tab-live" }));
  assert.deepEqual(latestState().tabs.map(t => t.id), ["tab-heavy"]);

  peer.app(JSON.stringify({ do: "open", id: "req-reopen", key: "t:tab-live" }));
  assert.equal(replyFor("req-reopen").result.tab, "tab-live");
  assert.deepEqual(latestState().tabs.map(t => t.id), ["tab-heavy", "tab-live"]);
});

test("createMockPeer host.terminal open/write/read echoes text, Enter, Backspace, and Ctrl+C", async () => {
  const { peer } = makePeer();

  const opened = JSON.parse(
    await peer.mitosisAdmin("remote", JSON.stringify({ method: "host.terminal.open", params: {} }))
  );
  assert.equal(opened.terminal_id, "term-1");

  const readFrom = async offset => {
    const res = JSON.parse(
      await peer.mitosisAdmin(
        "remote",
        JSON.stringify({ method: "host.terminal.read", params: { offset } })
      )
    );
    return { text: res.data ? atob(res.data) : "", offset: res.offset };
  };

  const writeText = async data => {
    await peer.mitosisAdmin(
      "remote",
      JSON.stringify({ method: "host.terminal.write", params: { data: btoa(data) } })
    );
  };

  // Initial prompt
  let cur = await readFrom(0);
  assert.match(cur.text, /orb@macbook/);

  // Echo text + Backspace + Enter
  await writeText("lsx\x7f\r");
  cur = await readFrom(cur.offset);
  assert.ok(cur.text.includes("lsx\b \b"));
  assert.ok(cur.text.includes("(demo shell) ls"));

  // Ctrl+C cancels current input and prints ^C + fresh prompt
  await writeText("abc\x03");
  cur = await readFrom(cur.offset);
  assert.ok(cur.text.includes("abc^C\r\n"));
  assert.match(cur.text, /orb@macbook/);
});

test("createMockPeer handles spread.info, spread.ssh, and invite", () => {
  const { peer, replyFor } = makePeer();

  peer.app(JSON.stringify({ do: "spread.info", id: "req-spread-info" }));
  const info = replyFor("req-spread-info").result;
  assert.equal(info.machine_name, "MacBook Pro");
  assert.deepEqual(info.ssh_hosts, ["ordalie-lab-3", "hetzner-01"]);
  assert.deepEqual(
    info.clients.map(c => c.id),
    ["claude", "codex", "pi"],
  );

  peer.app(JSON.stringify({ do: "invite", id: "req-invite" }));
  const inv = replyFor("req-invite").result;
  assert.match(inv.code, /^orb-mitosis:v1:/);

  peer.app(
    JSON.stringify({
      do: "spread.ssh",
      id: "req-spread-ssh",
      machine: info.machine,
      target: "ordalie-lab-3",
      code: inv.code,
    }),
  );
  const ssh = replyFor("req-spread-ssh").result;
  assert.equal(ssh.ok, true);
  assert.equal(ssh.target, "ordalie-lab-3");
  assert.equal(ssh.output, "joined · Mitosis running");
});

test("createMockPeer handles archive, threads.archived, and unarchive", () => {
  const { peer, latestHome, replyFor } = makePeer();
  peer.app(JSON.stringify({ do: "hello" }));

  const targetKey = "s:thread-0";
  assert.ok(latestHome().entries.some(e => e.key === targetKey));

  // Archive thread -> removed from active home.entries
  peer.app(JSON.stringify({ do: "archive", id: "req-arch", key: targetKey, archived: true }));
  assert.equal(replyFor("req-arch").result.ok, true);
  assert.ok(!latestHome().entries.some(e => e.key === targetKey));

  // threads.archived -> lists the archived thread
  peer.app(JSON.stringify({ do: "threads.archived", id: "req-list" }));
  const archived = replyFor("req-list").result.entries;
  assert.equal(archived.length, 1);
  assert.equal(archived[0].key, targetKey);

  // Unarchive thread -> restored to active home.entries and removed from archived list
  peer.app(JSON.stringify({ do: "archive", id: "req-unarch", key: targetKey, archived: false }));
  assert.equal(replyFor("req-unarch").result.ok, true);
  assert.ok(latestHome().entries.some(e => e.key === targetKey));

  peer.app(JSON.stringify({ do: "threads.archived", id: "req-list-2" }));
  assert.equal(replyFor("req-list-2").result.entries.length, 0);
});
