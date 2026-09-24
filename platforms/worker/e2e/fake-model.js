// Scripted OpenAI Chat Completions model for the Worker end-to-end checks.
// It is a plain script so it can be prepended to a Worker bundle: it defines
// globalThis.orbFakeModel and answers fetches to https://fake-model.invalid
// itself, so a deployed test Worker needs no provider key. e2e.mjs serves the
// same handler over local HTTP for workerd and Celld.
//
// A prompt "orb-e2e write <path> <text...>" makes the model call write, then
// read, then answer "read back: <file>"; "orb-e2e read <path>" skips the write.
// "orb-e2e bridge <peer> <instance> <text...>" makes it inspect that Bridge
// instance with bridge_call, prompt it with <text> at the inspected target,
// then answer "bridge prompt: <receipt status>".
(() => {
  const encoder = new TextEncoder();

  const text = content =>
    typeof content === "string" ? content : Array.isArray(content) ? content.map(part => part.text ?? "").join("") : "";

  function pieces(value, count) {
    const size = Math.max(1, Math.ceil(value.length / count));
    const result = [];
    for (let offset = 0; offset < value.length; offset += size) result.push(value.slice(offset, offset + size));
    return result;
  }

  function step(messages) {
    const userIndex = messages.findLastIndex(message => message.role === "user");
    const [marker, verb, path, ...words] = text(messages[userIndex]?.content).trim().split(/\s+/);
    const since = messages.slice(userIndex + 1);
    const calls = since.flatMap(message => (message.role === "assistant" ? (message.tool_calls ?? []) : []));
    const id = `call_${messages.length}`;
    if (marker !== "orb-e2e") return { text: "unscripted prompt" };
    if (verb === "bridge") return bridge(path, words, calls, since.filter(message => message.role === "tool").map(message => text(message.content)), id);
    if (verb === "write" && calls.length === 0) return { id, tool: "write", args: { path, content: `${words.join(" ")}\n` } };
    if ((verb === "write" && calls.length === 1) || (verb === "read" && calls.length === 0)) return { id, tool: "read", args: { path } };
    const result = since.findLast(message => message.role === "tool");
    return { text: `read back: ${text(result?.content).trim()}` };
  }

  function operationID() {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return btoa(String.fromCharCode(...bytes)).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
  }

  function bridge(peer, [instance, ...words], calls, results, id) {
    const call = { instance_id: instance, service: "orb.instance/1" };
    if (calls.length === 0) return { id, tool: "bridge_call", args: { peer_id: peer, call: { ...call, method: "inspect", args: {} } } };
    let result = {};
    try {
      result = JSON.parse(results.at(-1));
    } catch {
      return { text: `bridge failed: ${results.at(-1)}` };
    }
    if (calls.length === 1) {
      const expected = { registration_generation: result.registration_generation, session_revision: result.target?.session_revision };
      const prompt = { ...call, method: "prompt", session_id: result.target?.session_id, expected, operation_id: operationID(), args: { text: words.join(" ") } };
      return { id, tool: "bridge_call", args: { peer_id: peer, call: prompt } };
    }
    return { text: `bridge prompt: ${result.status}` };
  }

  async function handle(request) {
    if (!new URL(request.url).pathname.endsWith("/chat/completions")) return new Response("not found", { status: 404 });
    const body = await request.json();
    const next = step(body.messages ?? []);
    const chunk = (delta, finish = null) =>
      `data: ${JSON.stringify({ id: "chatcmpl-orb-e2e", object: "chat.completion.chunk", created: 0, model: body.model, choices: [{ index: 0, delta, finish_reason: finish }] })}\n\n`;
    const events = [chunk({ role: "assistant", content: "" })];
    if (next.tool) {
      events.push(chunk({ tool_calls: [{ index: 0, id: next.id, type: "function", function: { name: next.tool, arguments: "" } }] }));
      for (const part of pieces(JSON.stringify(next.args), 3)) events.push(chunk({ tool_calls: [{ index: 0, function: { arguments: part } }] }));
      events.push(chunk({}, "tool_calls"));
    } else {
      for (const part of pieces(next.text, 4)) events.push(chunk({ content: part }));
      events.push(chunk({}, "stop"));
    }
    const usage = { prompt_tokens: 100, completion_tokens: 10, total_tokens: 110 };
    events.push(`data: ${JSON.stringify({ id: "chatcmpl-orb-e2e", object: "chat.completion.chunk", created: 0, model: body.model, choices: [], usage })}\n\n`, "data: [DONE]\n\n");
    // Paced chunks exercise incremental reads of the response stream.
    const stream = new ReadableStream({
      async start(controller) {
        for (const event of events) {
          controller.enqueue(encoder.encode(event));
          await new Promise(resolve => setTimeout(resolve, 5));
        }
        controller.close();
      },
    });
    return new Response(stream, { headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-store" } });
  }

  globalThis.orbFakeModel = { handle };
  const realFetch = globalThis.fetch;
  globalThis.fetch = (input, init) => {
    const url = new URL(typeof input === "string" ? input : input.url);
    if (url.hostname !== "fake-model.invalid") return realFetch(input, init);
    const { credentials, mode, ...options } = init ?? {};
    return handle(new Request(input, options));
  };
})();
