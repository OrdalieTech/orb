import assert from "node:assert/strict";
import { RemoteBridge } from "../../../platforms/browser/web/bridge.js";

const client = new RemoteBridge(() => {});
const info = { target: { session_id: "session", session_revision: "1" }, registration_generation: "2", methods: ["prompt"] };
const calls = [];
let turn = 0;
client.call = async (method, params) => {
  calls.push({ method, params });
  if (method === "instances.describe") return structuredClone(info);
  if (method === "instances.list") return params.cursor ? { items: [{ instance_id: "b" }] } : { items: [{ instance_id: "a" }], cursor: "next" };
  if (method === "events.unsubscribe") return {};
  if (method === "events.subscribe") {
    if (params.cursor) {
      if (turn++ === 0) return { events: [{ data: { type: "message_end", message: { role: "assistant", content: "answer" } } }], cursor: "c2" };
      throw new Error("cursor_expired");
    }
    return params.offset ? { messages: [{ role: "user", content: "second" }], snapshot_id: "snap", cursor: "c1" } : { messages: [{ role: "user", content: "first" }], snapshot_id: "snap", offset: "next", cursor: "c1" };
  }
  if (method === "instances.call") return { status: "accepted", operation_id: params.operation_id };
  throw new Error(method);
};
assert.equal((await client.instances()).length, 2);
assert.equal((await client.refresh("instance")).messages.length, 2);
assert.ok(calls.some(c => c.method === "events.unsubscribe"));
assert.equal((await client.refresh("instance")).messages.at(-1).content, "answer");
const receipt = await client.command("prompt", { text: "hi" });
assert.equal(receipt.status, "accepted");
const sent = calls.at(-1).params;
assert.equal(sent.session_id, "session");
assert.deepEqual(sent.expected, { registration_generation: "2", session_revision: "1" });
await assert.rejects(client.command("cancel"), /not permitted/);
await assert.rejects(client.refresh("instance"), /cursor_expired/);
assert.equal(client.view, null);
assert.equal((await client.refresh("instance")).messages.length, 2);
const call = client.call;
let descriptions = 0;
client.call = async (method, params) => {
  const result = await call(method, params);
  if (method === "instances.describe" && ++descriptions === 2) result.target.session_id = "changed";
  return result;
};
client.view = null;
await assert.rejects(client.refresh("instance"), /session changed/);
assert.equal(client.view, null);
client.call = async () => { throw new Error("connection lost"); };
client.view = { instance: "instance", info };
await assert.rejects(client.command("prompt", { text: "do not replay" }), /connection lost/);
console.log("Browser Bridge controller OK: pagination, events, session fences, grants, no automatic command replay");
