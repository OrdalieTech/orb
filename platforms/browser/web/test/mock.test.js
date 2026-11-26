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
  const replyFor = id => frames.find(f => f.t === "reply" && f.id === id);
  return { peer, frames, latestState, replyFor };
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
