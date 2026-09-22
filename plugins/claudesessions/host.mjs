// Only this host speaks Anthropic's SDK protocol. No credentials cross its pipes.
import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';
import { once } from 'node:events';
const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
const pending = new Map();
let active, cancelled = false, nextID = 0;
async function send(value) {
  const line = JSON.stringify(value) + '\n';
  if (Buffer.byteLength(line) > 8 * 1024 * 1024) throw new Error('Claude SDK event exceeds 8 MiB');
  if (!process.stdout.write(line)) await once(process.stdout, 'drain');
}
function request(message, signal) {
  const id = String(++nextID);
  return new Promise((resolve, reject) => {
    const abort = () => { pending.delete(id); reject(new Error('Input cancelled')); };
    if (signal.aborted) return abort();
    signal.addEventListener('abort', abort, { once: true });
    pending.set(id, (value, cancelled) => {
      signal.removeEventListener('abort', abort);
      if (cancelled) reject(new Error('Approval cancelled')); else resolve(value);
    });
    send({ ...message, id }).catch(reject);
  });
}
function ask(title, choices, signal) { return request({ type: 'input', title, choices }, signal); }
async function run(config) {
  const { query } = await import(pathToFileURL(config.sdk));
  const options = {
    cwd: config.cwd, pathToClaudeCodeExecutable: config.claude,
    model: config.model === "default" ? undefined : config.model, includePartialMessages: true,
    systemPrompt: { type: 'preset', preset: 'claude_code' },
    permissionMode: 'default',
    hooks: { PreToolUse: [{ timeout: 86400, hooks: [async (input, toolID, { signal }) => {
      if (input.tool_name === 'AskUserQuestion' || input.tool_name === 'ExitPlanMode') return {};
      try {
        const result = await request({ type: 'tool', tool: input.tool_name, args: input.tool_input,
          tool_id: toolID, cwd: input.cwd }, signal);
        if (!result.decision) return {};
        return { hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: result.decision,
          permissionDecisionReason: result.reason } };
      } catch { return { hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: 'deny',
        permissionDecisionReason: 'Orb permission check did not complete' } }; }
    }] }] },
    canUseTool: async (name, input, { signal }) => {
      try {
        if (name === 'AskUserQuestion') {
          const result = await request({ type: 'questions', questions: input.questions }, signal);
          if (result.cancelled) return { behavior: 'deny', message: 'The user dismissed the questions. Do not assume an answer.' };
          const answers = Object.fromEntries(input.questions.map((q, i) => {
            const answer = result.answers[i];
            return [q.question, [...answer.selected, ...(answer.custom ? [answer.custom] : [])].join(', ')];
          }));
          return { behavior: 'allow', updatedInput: { ...input, answers } };
        }
        const details = name === 'ExitPlanMode' ? (input.plan ?? 'Claude is ready to leave planning and start implementation.')
          : name === 'Bash' ? [input.description, input.command].filter(Boolean).join('\n\n')
          : JSON.stringify(input, null, 2);
        const answer = await ask(`${name === 'ExitPlanMode' ? 'Approve this plan?' : name}\n\n${details}`, ['Deny', 'Allow once'], signal);
        return answer === 'Allow once'
          ? { behavior: 'allow', updatedInput: input }
          : { behavior: 'deny', message: 'User denied this action' };
      } catch {
        return { behavior: 'deny', message: 'The user dismissed this request. Do not assume an answer or approval.', interrupt: signal.aborted };
      }
    },
  };
  if (config.effort) options.effort = config.effort;
  if (config.thinking) options.thinking = { type: config.thinking };
  if (config.catalog) {
    options.persistSession = false;
    let release;
    const done = new Promise(resolve => { release = resolve; });
    async function* empty() { await done; }
    try {
      active = query({ prompt: empty(), options });
      await send({ type: 'catalog', models: await active.supportedModels() });
    } finally { release(); active?.close(); active = undefined; }
    return;
  }
  if (config.resume) options.resume = config.resume;
  if (config.fork) { options.forkSession = true; options.sessionId = config.session; options.resumeSessionAt = config.at; }
  else if (!config.resume) options.sessionId = config.session;
  // Keep the input stream open for permission callbacks until the native result.
  let release;
  const finished = new Promise(resolve => { release = resolve; });
  async function* input() {
    yield { type: 'user', session_id: config.session, parent_tool_use_id: null, message: { role: 'user', content: config.content } };
    await finished;
  }
  try {
    active = query({ prompt: input(), options });
    if (cancelled) { await active.interrupt(); return; }
    for await (const event of active) {
      await send({ type: 'sdk', event });
      if (event.type === 'result') {
        let timeout;
        try {
          const usage = await Promise.race([
            active.getContextUsage({ detail: 'summary' }),
            new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('Context timeout')), 2000); }),
          ]);
          await send({ type: 'context', event: { maxTokens: usage.maxTokens, totalTokens: usage.totalTokens, percentage: usage.percentage } });
        } catch { /* Context telemetry must not fail a completed turn. */ }
        finally { clearTimeout(timeout); }
        release(); break;
      }
    }
  } finally {
    release();
    active?.close();
    active = undefined;
  }
}
let running = false;
lines.on('line', line => {
  try {
    if (Buffer.byteLength(line) > 8 * 1024 * 1024) throw new Error('Input exceeds 8 MiB');
    const message = JSON.parse(line);
    if (message.type === 'start' && !running) {
      running = true;
      run(message).catch(error => send({ type: 'error', message: String(error.message).slice(0, 4096) }))
        .finally(() => { lines.close(); process.stdin.destroy(); });
    } else if (message.type === 'reply') {
      const resolve = pending.get(message.id);
      pending.delete(message.id);
      resolve?.(message.value, message.cancelled);
    } else if (message.type === 'cancel') {
      cancelled = true;
      active?.interrupt().catch(() => {}).finally(() => active?.close());
    }
  } catch { active?.close(); process.exitCode = 1; lines.close(); process.stdin.destroy(); }
});
lines.on('close', () => { if (!running) process.exitCode = 1; });
