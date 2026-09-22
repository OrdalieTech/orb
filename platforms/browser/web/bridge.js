// Browser controller state; transport and authentication stay in Go/Wasm.
export const randomID = () => btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16)))).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");

export class RemoteBridge {
  constructor(send) { this.send = send; this.pending = new Map(); this.connection = ""; this.view = null; }
  request(type, fields = {}) {
    const id = randomID();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error("Bridge request timed out")); }, 25000);
      this.pending.set(id, { resolve, reject, timer });
      this.send({ type, id, connection: this.connection, ...fields });
    });
  }
  receive(message) {
    if (message.type === "bridge.closed") {
      if (message.connection === this.connection) { this.connection = ""; this.view = null; }
      return;
    }
    const call = this.pending.get(message.id);
    if (!call) return;
    clearTimeout(call.timer); this.pending.delete(message.id);
    if (message.error) call.reject(new Error(message.error)); else call.resolve(message.result);
  }
  reset() {
    for (const call of this.pending.values()) { clearTimeout(call.timer); call.reject(new Error("Bridge disconnected")); }
    this.pending.clear(); this.connection = ""; this.view = null;
  }
  async connect(invitation, identity) {
    const result = await this.request("bridge.connect", { url: invitation.locator, peer: invitation.peer_id, ...identity });
    this.connection = result.connection; this.view = null;
    this.peer = invitation.peer_id;
    if (invitation.token) await this.call("pair.claim", { invitation_id: invitation.invitation_id, token: invitation.token });
    return result;
  }
  async disconnect() { await this.request("bridge.disconnect"); this.reset(); }
  call(method, params = {}) {
    if (!this.connection) return Promise.reject(new Error("Bridge disconnected. Reconnect to continue."));
    return this.request("bridge.call", { method, params });
  }
  async instances() {
    const items = []; let cursor = "";
    for (let pages = 0; pages < 32; pages++) {
      const page = await this.call("instances.list", { cursor });
      items.push(...page.items);
      if (!page.cursor) return items;
      if (page.cursor === cursor) throw new Error("Invalid catalog cursor");
      cursor = page.cursor;
    }
    throw new Error("Bridge catalog exceeds debug limit");
  }
  async sessions(instance) {
    const items = []; let offset = "";
    for (let pages = 0; pages < 128; pages++) {
      const page = await this.call("instances.call", { instance_id: instance, service: "orb.instance/1", method: "session.list", args: { offset } });
      items.push(...page.items);
      if (!page.offset) return items;
      if (offset === page.offset) throw new Error("Invalid session cursor");
      offset = page.offset;
    }
    throw new Error("Session list exceeds debug limit");
  }
  async refresh(instance) {
    const info = await this.call("instances.describe", { instance_id: instance });
    let view = this.view;
    if (!view || view.instance !== instance || view.info.target.session_id !== info.target.session_id || view.info.registration_generation !== info.registration_generation || view.info.target.session_revision !== info.target.session_revision) {
      view = { instance, messages: [], partial: null, cursor: "", info };
    }
    if (!view.cursor) {
      let snapshot = "", offset = "", size = 0;
      view.messages = [];
      try {
        for (let pages = 0; ; pages++) {
          if (pages >= 128) throw new Error("Conversation exceeds debug limit");
          const page = await this.call("events.subscribe", { instance_id: instance, snapshot_id: snapshot, offset });
          snapshot = page.snapshot_id;
          size += JSON.stringify(page.messages).length;
          if (size > 8 * 1024 * 1024) throw new Error("Conversation exceeds debug limit");
          view.messages.push(...page.messages); view.partial = page.partial;
          if (!page.offset) { view.cursor = page.cursor; break; }
          if (offset === page.offset) throw new Error("Invalid conversation cursor");
          offset = page.offset;
        }
      } finally {
        if (snapshot) await this.call("events.unsubscribe", { instance_id: instance, snapshot_id: snapshot });
      }
    } else {
      let page;
      try { page = await this.call("events.subscribe", { instance_id: instance, cursor: view.cursor }); }
      catch (error) { this.view = null; throw error; }
      for (const { data: event } of page.events) {
        if (event.type === "message_start" || event.type === "message_update") view.partial = event.message;
        if (event.type === "message_end") { view.messages.push(event.message); view.partial = null; }
        if (event.type === "agent_end") view.partial = null;
      }
      view.cursor = page.cursor;
      if (view.messages.length > 16384 || JSON.stringify(view.messages).length > 8 * 1024 * 1024) { this.view = null; throw new Error("Conversation exceeds debug limit"); }
    }
    const current = await this.call("instances.describe", { instance_id: instance });
    if (current.target.session_id !== info.target.session_id || current.target.session_revision !== info.target.session_revision || current.registration_generation !== info.registration_generation) {
      this.view = null; throw new Error("Remote session changed; refreshing");
    }
    view.info = current; this.view = view;
    return view;
  }
  command(method, args = {}) {
    if (!this.view) return Promise.reject(new Error("Wait for the remote conversation to load"));
    const { instance, info } = this.view;
    if (!info.methods.includes(method)) return Promise.reject(new Error("This control is not permitted"));
    return this.call("instances.call", {
      instance_id: instance, service: "orb.instance/1", method, args,
      session_id: info.target.session_id, operation_id: randomID(),
      expected: { registration_generation: info.registration_generation, session_revision: info.target.session_revision },
    });
  }
}
