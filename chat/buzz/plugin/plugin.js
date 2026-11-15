// Orb agents: one `orb --mode acp` process per agent, one ACP session per Buzz
// thread or Solo chat (kept across restarts), and Orb's last message of a turn
// posted as the agent's reply there. Orb's session updates drive the live step,
// the Activity tab, session status and usage; a message sent while a turn runs
// steers it. Model and thinking are Orb's own ACP config options.
export const inject = ["agents", "host"];

const NOTE =
  "You are a Buzz chat agent. Each message below mentions you in a Buzz thread or Solo chat; " +
  "your last message of each turn is posted there as your reply, so never send that reply yourself. " +
  "The `buzz` CLI acts as you (credentials are in your environment): use it to read the thread " +
  "and its attachments, or to post somewhere else. Messages posted in the conversation since " +
  "your last turn come first, in <thread-context>.";
const STANDALONE =
  "This turn belongs to no conversation, so nothing you write is posted: use the `buzz` CLI to post.";
const RESUMED =
  "Your previous turn here stopped partway when the app closed or reloaded; check what it left done before continuing.";
const IDENTITIES = "~/Library/Application Support/xyz.block.buzz.desktop/agents/identities.json";
const text = (bytes) => new TextDecoder().decode(bytes);

/** The agent's Buzz credentials for its `buzz` CLI, as buzz-acp used to pass
 * them: the relay and NIP-OA tag the app saved, and the key the owner put in
 * ~/.config/buzz-orb/<pubkey> (the app keeps keys out of plugins' reach). */
async function credentials(host, pubkey) {
  const env = {};
  try {
    const identity = JSON.parse(text(await host.readFile(IDENTITIES))).find((i) => i.pubkey === pubkey);
    if (identity?.relay) env.BUZZ_RELAY_URL = identity.relay;
    if (identity?.auth) env.BUZZ_AUTH_TAG = identity.auth;
  } catch {}
  try {
    env.BUZZ_PRIVATE_KEY = text(await host.readFile(`~/.config/buzz-orb/${pubkey}`)).trim();
  } catch {}
  return env;
}
const DEFAULTS = { model: "", effort: "", instructions: "", workspace: "~/.buzz" };
const CATALOG = "orb.catalog";

const settings = (config) => ({ ...DEFAULTS, ...config });
const system = (config) => [settings(config).instructions, NOTE].filter(Boolean).join("\n\n");

/** The thread a message belongs to (NIP-10, as the relay reads it), or itself. */
function rootOf(event) {
  let root, reply;
  for (const [name, value, , marker] of event.tags) {
    if (name !== "e" || !/^[0-9a-f]{64}$/i.test(value ?? "")) continue;
    if (marker === "root") root = value;
    if (marker === "reply") reply = value;
  }
  return reply ? (root ?? reply) : event.id;
}

/** Whether the channel is a Solo chat, as Buzz reads its metadata: one
 * conversation, answered at its top level. */
async function soloChat(agent, channelId) {
  const [metadata] = await agent.query([{ kinds: [39000], "#d": [channelId], limit: 1 }]);
  const types = metadata?.tags.filter(([name]) => name === "t").map(([, value]) => value) ?? [];
  return types[0] === "stream" && types.includes("buzz.solo") && metadata.tags.some(([name]) => name === "private");
}

// Orb's own tools are lowercase; Claude's keep the names Buzz phrases steps for.
const TOOLS = { bash: "Bash", read: "Read", edit: "Edit", write: "Write", grep: "Grep", find: "Glob", ls: "ListFiles" };
const TARGETS = ["description", "file_path", "notebook_path", "path", "pattern", "url", "query", "command"];
/** The live step an update shows, as Buzz's ACP harnesses read it. */
function stepOf(update) {
  if (update.sessionUpdate === "agent_thought_chunk") return ["Thinking"];
  if (update.sessionUpdate === "agent_message_chunk") return ["Writing reply"];
  if (update.sessionUpdate !== "tool_call") return undefined;
  const title = String(update.title || "Tool");
  const key = TARGETS.find((k) => typeof update.rawInput?.[k] === "string" && update.rawInput[k]);
  const target = key && String(update.rawInput[key]);
  return [TOOLS[title] ?? title.split("__").at(-1), key?.endsWith("path") ? target.split("/").at(-1) : target];
}

// Conversations, as kept across restarts under `orb.session:<pubkey>:<channel>[:<root>]`,
// with their live state and transcript while the app runs.
const SESSION = "orb.session:";
const places = new Map();
const listeners = new Set();
let version = 0;
let pending;
function notify() {
  pending ??= setTimeout(() => {
    pending = undefined;
    version++;
    for (const listener of listeners) listener();
  }, 100);
}
const subscribe = (listener) => (listeners.add(listener), () => listeners.delete(listener));
const keyOf = (pubkey, channelId, root) => `${SESSION}${pubkey}:${channelId}${root ? `:${root}` : ""}`;
function saved(key) {
  const value = localStorage.getItem(key);
  if (!value) return undefined;
  try {
    return JSON.parse(value);
  } catch {
    return { id: value }; // Saved before records: the session id alone.
  }
}
function placeOf(pubkey, channelId, root) {
  const key = keyOf(pubkey, channelId, root);
  let place = places.get(key);
  if (!place) places.set(key, (place = { key, pubkey, channelId, root, ...saved(key), items: [], deliveries: [] }));
  return place;
}
function save(place) {
  const { id, channelId, root, at, seen, title, team } = place;
  if (place.key) localStorage.setItem(place.key, JSON.stringify({ id, channelId, root, at, seen, title, team }));
  notify();
}
/** Every conversation the agent has here, the live ones with their state. */
function placesFor(pubkey) {
  const prefix = `${SESSION}${pubkey}:`;
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (key?.startsWith(prefix) && !places.has(key)) {
      const record = saved(key);
      const [channelId, root] = key.slice(prefix.length).split(":");
      places.set(key, { key, pubkey, channelId: record.channelId ?? channelId, root: record.root ?? root, ...record, items: [], deliveries: [] });
    }
  }
  return [...places.values()].filter((p) => p.pubkey === pubkey && p.key).sort((a, b) => (b.at ?? 0) - (a.at ?? 0));
}
const statusOf = (place) => place.status ?? (place.at ? "Idle" : undefined);

/** Adds an update to a conversation's transcript, chunks to the item they continue. */
function record(place, update) {
  const items = place.items;
  const last = items.at(-1);
  const chunk = { user_message_chunk: "prompt", agent_message_chunk: "message", agent_thought_chunk: "thought" }[update.sessionUpdate];
  if (chunk && update.content?.type === "text") {
    if (last?.type === chunk && !last.done) last.text += update.content.text;
    else items.push({ type: chunk, text: update.content.text });
  } else if (update.sessionUpdate === "tool_call") {
    const [tool, target] = stepOf(update);
    items.push({ type: "tool", id: update.toolCallId, tool, target, status: update.status ?? "in_progress" });
  } else if (update.sessionUpdate === "tool_call_update") {
    const item = items.findLast((i) => i.type === "tool" && i.id === update.toolCallId);
    if (item) {
      item.status = update.status ?? item.status;
      const output = update.content?.map((c) => c.content?.text ?? "").join("");
      if (output) item.output = output.length > 4000 ? `${output.slice(0, 4000)}…` : output;
    }
  } else return;
  if (items.length > 500) items.splice(0, items.length - 500);
  notify();
}

/** One observation per turn from Orb's cumulative session usage. */
const USAGE = "orb.usage:";
function usage(place, update) {
  const key = `${USAGE}${place.id}`;
  const now = Date.now();
  const kept = JSON.parse(localStorage.getItem(key) ?? "null");
  const entry = kept ?? { id: place.id, pubkey: place.pubkey, channelId: place.channelId, root: place.root, title: place.title, startedAt: now, observations: [] };
  // Totals restart with a reloaded session that recounts less than was seen.
  const last = (update.accumulatedInputTokens ?? 0) < (entry.last?.accumulatedInputTokens ?? 0) ? {} : (entry.last ?? {});
  const delta = (name) => (update[name] ?? 0) - (last[name] ?? 0);
  const cacheRead = delta("accumulatedCachedInputTokens");
  const cacheWrite = delta("accumulatedCacheWriteTokens");
  const output = delta("accumulatedOutputTokens");
  const input = delta("accumulatedInputTokens") - cacheRead - cacheWrite;
  if (input || output || cacheRead || cacheWrite) {
    const cost = delta("accumulatedCost");
    entry.observations.push({ at: now, model: place.model || "orb", tokens: { input, output, cacheRead, cacheWrite }, ...(cost > 0 && { cost: { usd: cost, source: "orb" } }) });
  }
  entry.last = update;
  entry.lastAt = now;
  localStorage.setItem(key, JSON.stringify(entry));
}

class Orb {
  constructor(host, onExit, pubkey) {
    this.host = host;
    this.next = 0;
    this.pending = new Map();
    this.sessions = new Map();
    this.buffer = "";
    this.stderr = "";
    this.ready = (async () => {
      const env = pubkey ? await credentials(host, pubkey) : {};
      this.process = await host.spawn("orb", {
        env,
        onStdout: (data) => this.read(data),
        onStderr: (data) => (this.stderr = (this.stderr + data).slice(-2000)),
      });
      this.process.exited.then(() => {
        onExit();
        const error = new Error(`Orb exited${this.stderr.trim() ? `: ${this.stderr.trim().split("\n").at(-1)}` : ""}`);
        for (const { reject } of this.pending.values()) reject(error);
      });
      await this.call("initialize", { protocolVersion: 1, clientCapabilities: {} });
    })();
  }

  send(message) {
    return this.process.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);
  }

  call(method, params) {
    const id = ++this.next;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.send({ id, method, params }).catch(reject);
    });
  }

  read(data) {
    this.buffer += data;
    let end;
    while ((end = this.buffer.indexOf("\n")) >= 0) {
      const line = this.buffer.slice(0, end).trim();
      this.buffer = this.buffer.slice(end + 1);
      let message;
      try {
        message = line && JSON.parse(line);
      } catch {}
      if (!message) continue;
      const place = message.params?.sessionId && this.sessions.get(message.params.sessionId);
      if (message.method === undefined) {
        const waiter = this.pending.get(message.id);
        this.pending.delete(message.id);
        if (message.error) waiter?.reject(new Error(message.error.message));
        else waiter?.resolve(message.result);
      } else if (message.method === "session/update") {
        if (place) this.update(place, message.params.update);
      } else if (message.method === "_session/answered") {
        if (place) this.answered(place);
      } else if (message.method === "_goose/unstable/session/update") {
        if (place?.id && message.params.update?.sessionUpdate === "usage_update") usage(place, message.params.update);
      } else if (message.id !== undefined) {
        // Orb runs its own tools; allow anything it still asks about.
        const option = message.params?.options?.find((o) => o.kind?.startsWith("allow"));
        this.send(
          message.method === "session/request_permission" && option
            ? { id: message.id, result: { outcome: { outcome: "selected", optionId: option.optionId } } }
            : { id: message.id, error: { code: -32601, message: "Not supported" } },
        );
      }
    }
  }

  update(place, update) {
    record(place, update);
    // A reopened session replays its transcript before the turn's prompt goes out.
    if (!place.live) return;
    if (update.sessionUpdate === "tool_call") place.reply = "";
    if (update.sessionUpdate === "agent_message_chunk" && update.content?.type === "text") place.reply += update.content.text;
    const step = stepOf(update);
    if (!step || step.join(" ") === place.step) return;
    place.step = step.join(" ");
    place.status = step[1] ? `Working · ${step[0]} · ${step[1]}` : `Working · ${step[0]}`;
    place.deliveries.at(-1)?.step?.(...step);
    notify();
  }

  /** Posts a reply as soon as Orb says the assistant's message is complete,
   * without waiting for background work that keeps the turn open; a later
   * message of the same turn, as a task reports back, is its own post. */
  answered(place) {
    const reply = place.reply.trim();
    place.reply = "";
    if (!place.live || !place.key || !reply) return;
    const delivery = place.deliveries.at(-1);
    place.posting = (place.posting ?? Promise.resolve()).then(() => this.post(delivery.agent, place, reply, mentionOf(delivery))).catch(() => {});
    const posts = (place.posts = (place.posts ?? 0) + 1);
    // A turn still open a moment later is waiting on its background work.
    setTimeout(() => {
      if (!place.live || place.posts !== posts) return;
      place.step = place.status = "Waiting on background work";
      place.deliveries.at(-1)?.step?.("Waiting on background work");
      notify();
    }, 1500);
  }

  /** The conversation's session in this Orb, opened once. */
  open(place, config) {
    if (place.orb !== this || !place.opening) {
      place.orb = this;
      place.opening = this.load(place, config);
      place.opening.catch(() => (place.opening = undefined));
    }
    return place.opening;
  }

  /** Reopens the session (replaying its transcript) when Orb still has it, else starts one. */
  async load(place, config) {
    await this.ready;
    const cwd = await this.host.resolveDirectory(settings(config).workspace || DEFAULTS.workspace);
    const options = { cwd, mcpServers: [], systemPrompt: { append: system(config) } };
    if (place.id) {
      this.sessions.set(place.id, place);
      place.items = [];
      try {
        await this.call("session/load", { ...options, sessionId: place.id });
        place.items.forEach((item) => (item.done = true));
        return place.id;
      } catch {
        this.sessions.delete(place.id);
      }
    }
    const { sessionId, configOptions } = await this.call("session/new", options);
    saveCatalog(configOptions);
    // A new session has seen nothing of the conversation yet.
    Object.assign(place, { id: sessionId, at: undefined, seen: undefined, team: undefined });
    this.sessions.set(sessionId, place);
    save(place);
    return sessionId;
  }

  /** Runs one conversation's messages in order; one sent while its turn runs
   * joins that turn, unless it picks another model or effort. */
  async turn(delivery, target) {
    const place = placeOf(delivery.agent.pubkey, target.channelId, target.root);
    if (place.busy && !delivery.model && !delivery.effort) {
      try {
        const prompt = await this.input(place, delivery, target);
        await this.call("_session/steering", { sessionId: place.id, prompt: [{ type: "text", text: prompt }] });
        record(place, { sessionUpdate: "user_message_chunk", content: { type: "text", text: delivery.trigger.event?.content ?? target.text } });
        place.items.at(-1).done = true;
        place.reply &&= `${place.reply}\n\n`;
        place.deliveries.at(-1)?.superseded?.();
        place.deliveries.push(delivery);
        delivery.acknowledged?.();
        delivery.interruptible?.(() => this.stop(place));
        return place.running;
      } catch {}
    }
    const turn = (place.queue ?? Promise.resolve()).then(() => this.prompt(place, delivery, target));
    place.queue = turn.catch(() => {});
    return turn;
  }

  stop(place) {
    return this.send({ method: "session/cancel", params: { sessionId: place.id } });
  }

  /** The turn's message: what the conversation said since the session's last
   * turn, then the message itself. */
  async input(place, delivery, target) {
    const { agent, team, trigger } = delivery;
    const event = trigger.event;
    const parts = [trigger.resumed && RESUMED, team && team !== place.team && team];
    place.team = team;
    if (place.seen || !place.at) {
      const filter = place.root ? { kinds: [9], "#e": [place.root] } : { kinds: [9], "#h": [place.channelId] };
      const ids = !place.seen && place.root ? [{ ids: [place.root] }] : [];
      const events = await agent.query([{ ...filter, limit: 30, ...(place.seen && { since: place.seen + 1 }) }, ...ids]).catch(() => []);
      const before = event?.created_at ?? Infinity;
      const lines = events
        .filter((e) => e.id !== event?.id && e.pubkey !== agent.pubkey && e.created_at <= before)
        .sort((a, b) => a.created_at - b.created_at)
        .map((e) => `${e.pubkey === agent.owner ? "your owner" : e.pubkey}: ${e.content.slice(0, 2000)}`);
      if (lines.length) parts.push(`<thread-context>\n${lines.join("\n\n")}\n</thread-context>`);
    }
    place.seen = Math.max(place.seen ?? 0, event?.created_at ?? Math.floor(Date.now() / 1000));
    const where = place.root ? `channel ${place.channelId}, thread ${place.root}` : `Solo chat ${place.channelId}`;
    const from = !event ? `a ${trigger.type} wake` : event.pubkey === agent.owner ? "your owner" : event.pubkey;
    parts.push(`Message from ${from} (${where}):\n\n${event?.content ?? target.text}`);
    return parts.filter(Boolean).join("\n\n");
  }

  async prompt(place, delivery, target) {
    const { agent } = delivery;
    Object.assign(place, { busy: true, step: undefined, status: "Working", deliveries: [delivery] });
    delivery.interruptible?.(() => this.stop(place));
    place.running = this.start(place, delivery, target).then(
      async ({ stopReason }) => {
        const reply = place.reply.trim();
        await place.posting;
        if (reply) await this.post(agent, place, reply, mentionOf(place.deliveries.at(-1)));
        place.status = stopReason === "cancelled" ? "Stopped" : "Idle";
        return stopReason === "cancelled" ? { ok: true, stopped: true } : { ok: true };
      },
      async (error) => {
        const message = String(error?.message ?? error);
        place.status = "Needs attention";
        await this.post(agent, place, `⚠️ I couldn't finish that: ${message.slice(0, 500)}`, [["p", agent.owner]]).catch(() => {});
        return { ok: false, error: message };
      },
    );
    try {
      return await place.running;
    } finally {
      Object.assign(place, { busy: false, live: false, at: Date.now(), deliveries: [] });
      place.items.forEach((item) => (item.done = true));
      save(place);
    }
  }

  /** Opens the session, applies the picks and sends the turn's message. */
  async start(place, delivery, target) {
    const { trigger } = delivery;
    const config = settings(delivery.config);
    const sessionId = await this.open(place, delivery.config);
    // Empty means Orb's own default from the manifest arguments.
    const model = delivery.model ?? config.model;
    const effort = delivery.effort ?? config.effort;
    if (model) await this.call("session/set_config_option", { sessionId, configId: "model", value: model });
    if (effort) await this.call("session/set_config_option", { sessionId, configId: "thought_level", value: effort });
    const prompt = await this.input(place, delivery, target);
    place.title ??= (trigger.event?.content ?? target.text ?? "").slice(0, 80);
    place.model = model || readCatalog()?.model;
    record(place, { sessionUpdate: "user_message_chunk", content: { type: "text", text: trigger.event?.content ?? target.text } });
    place.items.at(-1).done = true;
    Object.assign(place, { live: true, reply: "" });
    const done = this.call("session/prompt", { sessionId, prompt: [{ type: "text", text: prompt }] });
    delivery.acknowledged?.();
    return done;
  }

  /** Posts in the thread, as Buzz's own agents answer, or at a Solo chat's top level. */
  post(agent, place, content, tags) {
    const root = place.root ? [["e", place.root, "", "reply"]] : [];
    return agent.publish({ kind: 9, content, tags: [["h", place.channelId], ...root, ...tags] });
  }

  /** A watch's or timer's turn, in a session of its own. */
  async once(turn) {
    const { delivery, signal } = turn;
    const place = { channelId: "", items: [], deliveries: [delivery] };
    await this.open(place, delivery.config);
    const prompt = await turn.prompt();
    if (prompt === undefined) return { ok: true };
    const stop = () => this.stop(place);
    signal.addEventListener("abort", stop);
    Object.assign(place, { busy: true, live: true, reply: "" });
    try {
      await this.call("session/prompt", { sessionId: place.id, prompt: [{ type: "text", text: `${STANDALONE}\n\n${prompt}` }] });
      return { ok: true };
    } catch (error) {
      return { ok: false, error: String(error?.message ?? error) };
    } finally {
      signal.removeEventListener("abort", stop);
      this.sessions.delete(place.id);
    }
  }

  /** Summarizes a conversation's session to free context. */
  async compact(place, config) {
    place.compacting = true;
    notify();
    try {
      await this.call("session/prompt", { sessionId: await this.open(place, config), prompt: [{ type: "text", text: "/compact" }] });
    } finally {
      place.compacting = false;
      notify();
    }
  }
}

// Orb's model and thinking choices, as its last new session listed them.
const catalogListeners = new Set();
function readCatalog() {
  try {
    return JSON.parse(localStorage.getItem(CATALOG)) ?? undefined;
  } catch {
    return undefined;
  }
}
function saveCatalog(configOptions = []) {
  const option = (id) => configOptions.find((o) => o.id === id);
  const choices = (id) => (option(id)?.options ?? []).map(({ value, name }) => ({ value, label: name }));
  const catalog = {
    models: choices("model"),
    efforts: choices("thought_level"),
    model: option("model")?.currentValue ?? "",
    effort: option("thought_level")?.currentValue ?? "",
  };
  if (!catalog.models.length) return;
  localStorage.setItem(CATALOG, JSON.stringify(catalog));
  for (const listener of catalogListeners) listener();
}
/** Asks Orb for its choices once per app run, in a short-lived session. */
let discovering;
function discover(host) {
  discovering ??= (async () => {
    const orb = new Orb(host, () => {});
    try {
      await orb.ready;
      saveCatalog((await orb.call("session/new", { cwd: await host.resolveDirectory("~/.buzz"), mcpServers: [] })).configOptions);
    } finally {
      orb.process?.kill();
    }
  })().catch(() => {});
  return discovering;
}

export function apply(ctx) {
  const { agents, host, react: React } = ctx;
  if (!host.spawn) return;
  const h = React.createElement;
  const running = new Map();
  ctx.effect(() => () => {
    for (const orb of running.values()) orb.process?.kill();
  });
  function orbFor(agent) {
    let orb = running.get(agent.key);
    if (!orb) running.set(agent.key, (orb = new Orb(host, () => running.delete(agent.key), agent.pubkey)));
    return orb;
  }

  function useCatalog() {
    const catalog = React.useSyncExternalStore(
      (listener) => (catalogListeners.add(listener), () => catalogListeners.delete(listener)),
      () => localStorage.getItem(CATALOG),
      () => localStorage.getItem(CATALOG),
    );
    React.useEffect(() => void discover(host), []);
    return catalog ? JSON.parse(catalog) : { models: [], efforts: [], model: "", effort: "" };
  }
  const label = (choices, value) => choices.find((c) => c.value === value)?.label ?? value;
  /** Orb names models `provider/model`; thinking levels have no provider. */
  const providerOf = (value) => (value.includes("/") ? value.slice(0, value.indexOf("/")) : "");
  const withProvider = (choices, value) => [label(choices, value), providerOf(value)].filter(Boolean).join(" · ");

  function Field({ label: text, description, children }) {
    return h("label", { className: "buzz-field", "data-buzz-ui": "" },
      h("span", { className: "buzz-field-label" }, text),
      children,
      description && h("span", { className: "buzz-field-description" }, description));
  }
  function Choice({ text, value, choices, fallback, onChange, disabled }) {
    const known = choices.some((c) => c.value === value);
    return h(Field, { label: text },
      h("select", { className: "buzz-input", value, disabled, onChange: (e) => onChange(e.target.value) },
        h("option", { value: "" }, `Default · ${fallback}`),
        value && !known && h("option", { value }, value),
        [...new Set(choices.map((c) => providerOf(c.value)))].map((provider) => {
          const options = choices
            .filter((c) => providerOf(c.value) === provider)
            .map((c) => h("option", { key: c.value, value: c.value }, c.label));
          return provider ? h("optgroup", { key: provider, label: provider }, options) : options;
        })));
  }
  function ModelFields({ config, onChange, disabled }) {
    const catalog = useCatalog();
    return h(React.Fragment, null,
      h(Choice, { text: "Model", value: config.model, choices: catalog.models, disabled,
        fallback: withProvider(catalog.models, catalog.model) || "Orb's default", onChange: (model) => onChange({ ...config, model }) }),
      h(Choice, { text: "Thinking", value: config.effort, choices: catalog.efforts, disabled,
        fallback: catalog.effort || "Orb's default", onChange: (effort) => onChange({ ...config, effort }) }));
  }
  function Instructions({ value, onChange, disabled }) {
    return h(Field, { label: "Instructions", description: "Added to Orb's system prompt in every conversation." },
      h("textarea", { className: "buzz-textarea", rows: 8, value, disabled, onChange: (e) => onChange(e.target.value) }));
  }
  function Create({ config, onChange, disabled }) {
    const draft = settings(config);
    return h("div", { className: "grid gap-4" },
      h(ModelFields, { config: draft, onChange, disabled }),
      h(Instructions, { value: draft.instructions, disabled, onChange: (instructions) => onChange({ ...draft, instructions }) }));
  }
  function CreateModel({ config, onChange, disabled }) {
    const catalog = useCatalog();
    const draft = settings(config);
    return h(Choice, { text: "Model", value: draft.model, choices: catalog.models, disabled,
      fallback: withProvider(catalog.models, catalog.model) || "Orb's default", onChange: (model) => onChange({ ...draft, model }) });
  }
  function SettingsTab({ agent, save, settingsHost, settingsContent }) {
    const saved = settings(agent.config);
    const [draft, setDraft] = React.useState(saved);
    const [saving, setSaving] = React.useState(false);
    const [error, setError] = React.useState("");
    const configChanged = JSON.stringify(draft) !== JSON.stringify(saved);
    const submit = async (event) => {
      event.preventDefault();
      setSaving(true);
      setError("");
      try {
        await save({ ...draft, workspace: draft.workspace.trim() || DEFAULTS.workspace });
      } catch (reason) {
        setError(String(reason?.message ?? reason));
      } finally {
        setSaving(false);
      }
    };
    return h("form", { className: "grid gap-4", onSubmit: submit },
      error && h("p", { role: "alert", className: "buzz-field-error" }, error),
      settingsHost?.typeControl?.(configChanged),
      h(ModelFields, { config: draft, onChange: setDraft }),
      h(Instructions, { value: draft.instructions, onChange: (instructions) => setDraft({ ...draft, instructions }) }),
      h(Field, { label: "Workspace", description: "The folder Orb works in. Use ~/ for your home folder." },
        h("input", { className: "buzz-input", value: draft.workspace, onChange: (e) => setDraft({ ...draft, workspace: e.target.value }) })),
      h("button", { type: "submit", className: "buzz-button", "data-buzz-ui": "", "data-variant": "primary", "data-size": "md",
        disabled: saving || !(configChanged || settingsHost?.pendingSave) },
        h("span", { className: "buzz-button-label" }, saving ? "Saving…" : "Save")),
      settingsContent);
  }

  // The Activity tab: each conversation's transcript, live while its turn runs.
  const muted = { opacity: 0.65 };
  const block = { whiteSpace: "pre-wrap", overflowWrap: "anywhere", margin: 0 };
  const MARK = { completed: "✓", failed: "✗" };
  function Item({ item }) {
    if (item.type === "prompt")
      return h("p", { style: { ...block, borderLeft: "2px solid currentColor", paddingLeft: 8, ...muted } }, item.text);
    if (item.type === "message") return h("p", { style: block }, item.text);
    if (item.type === "thought")
      return h("details", { style: muted }, h("summary", null, "Thinking"), h("p", { style: block }, item.text));
    return h("details", null,
      h("summary", { style: { fontFamily: "ui-monospace, monospace", fontSize: 12 } },
        `${MARK[item.status] ?? "…"} ${item.tool}${item.target ? ` · ${item.target}` : ""}`),
      item.output && h("pre", { style: { ...block, fontSize: 12, maxHeight: 320, overflow: "auto", ...muted } }, item.output));
  }
  function ActivityTab({ agent, conversation, initialTranscript, openConversation }) {
    React.useSyncExternalStore(subscribe, () => version);
    const list = placesFor(agent.pubkey);
    const asked = conversation && list.find((p) => p.channelId === conversation.channelId && (p.root ?? undefined) === (conversation.rootId ?? undefined));
    const [chosen, choose] = React.useState(asked?.key);
    React.useEffect(() => initialTranscript?.done(), [initialTranscript?.opening]);
    const place = list.find((p) => p.key === chosen) ?? asked ?? list[0];
    React.useEffect(() => {
      if (place?.id && !place.items.length && !place.busy) orbFor(agent).open(place, agent.config).catch(() => {});
    }, [place?.key]);
    if (!place) return h("p", { style: muted }, "No conversations yet.");
    return h("div", { className: "grid gap-3" },
      h("select", { className: "buzz-input", value: place.key, onChange: (e) => choose(e.target.value) },
        list.map((p) => h("option", { key: p.key, value: p.key },
          `${p.title || (p.root ? `Thread ${p.root.slice(0, 8)}` : "Solo chat")}${statusOf(p) ? ` · ${statusOf(p)}` : ""}`))),
      h("div", { style: { display: "flex", gap: 8, alignItems: "center" } },
        h("span", { style: { flex: 1, ...muted } }, statusOf(place) ?? ""),
        place.busy && h("button", { className: "buzz-button", "data-buzz-ui": "", "data-size": "sm", onClick: () => orbFor(agent).stop(place) }, "Stop"),
        openConversation && h("button", { className: "buzz-button", "data-buzz-ui": "", "data-size": "sm",
          onClick: () => openConversation(place.channelId, place.root) }, "Open")),
      h("div", { className: "grid gap-2" }, place.items.map((item, i) => h(Item, { key: i, item }))));
  }

  agents.register({
    id: "orb",
    title: "Orb",
    description: "Orb on this computer, with any model Orb offers. Each thread it is mentioned in is one continuing session.",
    defaults: () => ({ config: { ...DEFAULTS } }),
    Create,
    CreateModel,
    adopt: (carry, config) => ({
      ...settings(config),
      instructions: carry.instructions ?? settings(config).instructions,
      workspace: carry.workspace ?? settings(config).workspace,
    }),
    carry: (config) => {
      const { instructions, workspace } = settings(config);
      return {
        ...(instructions.trim() && { instructions }),
        ...(workspace !== DEFAULTS.workspace && { workspace }),
      };
    },
    summary: (agent) => {
      const { model } = settings(agent.config);
      const catalog = readCatalog();
      return (catalog && withProvider(catalog.models, model || catalog.model)) || model || "Orb";
    },
    async models(agent) {
      await discover(host);
      const catalog = readCatalog() ?? { models: [], efforts: [], model: "" };
      const efforts = catalog.efforts.map(({ value, label }) => ({ id: value, label }));
      return {
        current: settings(agent.config).model || catalog.model,
        models: catalog.models.map(({ value }) => ({ id: value, label: withProvider(catalog.models, value), efforts })),
      };
    },
    tabs: [
      { id: "activity", title: "Activity", role: "activity", component: ActivityTab },
      { id: "settings", title: "Settings", role: "settings", component: SettingsTab },
    ],
    sessions: {
      list: (key) => placesFor(key.slice(0, 64)).map((p) => ({ channelId: p.channelId, root: p.root, at: p.at, status: statusOf(p) })),
      subscribe,
    },
    sessionControls: {
      scope: () => "thread",
      state(agent, { channelId, root }) {
        const place = places.get(keyOf(agent.pubkey, channelId, root));
        return place?.compacting ? "compacting" : place?.busy ? "active" : "idle";
      },
      async run(agent, { channelId, root }, action) {
        const place = placeOf(agent.pubkey, channelId, root);
        const orb = running.get(agent.key);
        if (action === "interrupt") return place.busy ? orb?.stop(place) : undefined;
        if (action === "compact") return place.id && orbFor(agent).compact(place, agent.config);
        orb?.sessions.delete(place.id);
        localStorage.removeItem(place.key);
        places.delete(place.key);
        notify();
      },
      subscribe,
    },
    async usage(agent, { since }) {
      const pubkey = agent.pubkey;
      const sessions = [];
      for (let i = 0; i < localStorage.length; i++) {
        const key = localStorage.key(i);
        if (!key?.startsWith(USAGE)) continue;
        const { last, pubkey: owner, ...entry } = JSON.parse(localStorage.getItem(key));
        if (owner === pubkey && entry.lastAt >= since)
          sessions.push({ ...entry, revision: `${entry.observations.length}:${entry.lastAt}`, gaps: [] });
      }
      return sessions;
    },
    timeoutMs: 2 * 60_000,
    resumes: true,
    run(delivery) {
      const { trigger } = delivery;
      if (trigger.type === "mention") {
        const channelId = delivery.channelId ?? trigger.event.tags.find(([name]) => name === "h")?.[1];
        const target = soloChat(delivery.agent, channelId).then((solo) => ({ channelId, root: solo ? undefined : rootOf(trigger.event) }));
        return delivery.workingUntil(target.then((t) => orbFor(delivery.agent).turn(delivery, t)).catch(failed));
      }
      // A plugin's, Attention's or the agent's own wake, in a conversation.
      if ((trigger.type === "plugin" || trigger.type === "attention") && trigger.channel)
        return delivery.workingUntil(orbFor(delivery.agent).turn(delivery, { channelId: trigger.channel, root: trigger.root, text: trigger.text }).catch(failed));
      delivery.ignored?.();
    },
    runOnce: (turn) => orbFor(turn.delivery.agent).once(turn),
  });
}

const mentionOf = (delivery) => (delivery.trigger.event ? [["p", delivery.trigger.event.pubkey]] : []);
const failed = (error) => ({ ok: false, error: String(error?.message ?? error) });
