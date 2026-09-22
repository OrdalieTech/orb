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
  const { query, forkSession } = await import(pathToFileURL(config.sdk));
  let permissionMode = config.permissionMode === 'plan' ? 'plan' : 'default';
  const options = {
    cwd: config.cwd, pathToClaudeCodeExecutable: config.claude,
    model: config.model === "default" ? undefined : config.model, includePartialMessages: true, agentProgressSummaries: true,
    systemPrompt: { type: 'preset', preset: 'claude_code' },
    permissionMode,
    hooks: { PreToolUse: [{ timeout: 86400, hooks: [async (input, toolID, { signal }) => {
      if (input.tool_name === 'AskUserQuestion' || input.tool_name === 'ExitPlanMode') return {};
      try {
        const result = await request({ type: 'tool', tool: input.tool_name, args: input.tool_input,
          tool_id: toolID, cwd: input.cwd }, signal);
        if (!result.decision || (permissionMode === 'plan' && result.decision === 'allow')) return {};
        return { hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: result.decision,
          permissionDecisionReason: result.reason } };
      } catch { return { hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: 'deny',
        permissionDecisionReason: 'Orb permission check did not complete' } }; }
    }] }] },
    onElicitation: async (input, { signal }) => {
      try { return await request({ type: 'elicitation', elicitation: input }, signal); }
      catch { return { action: 'cancel' }; }
    },
    canUseTool: async (name, input, options) => {
      const { signal } = options;
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
        const relative = path => typeof path === 'string' && path.startsWith(config.cwd + '/') ? path.slice(config.cwd.length + 1) : path;
        const quote = (mark, text) => String(text ?? '').slice(0, 2000).split('\n').map(line => mark + line).join('\n');
        const details = name === 'ExitPlanMode' ? (input.plan ?? 'Claude is ready to leave planning and start implementation.')
          : name === 'Bash' ? [input.description, input.command].filter(Boolean).join('\n\n')
          : name === 'Write' ? `${relative(input.file_path)}\n\n${quote('', input.content)}`
          : name === 'Edit' ? `${relative(input.file_path)}\n\n${quote('- ', input.old_string)}\n${quote('+ ', input.new_string)}`
          : JSON.stringify(input, null, 2);
        const title = name === 'ExitPlanMode' ? 'Approve this plan?' : `Permission requested for ${name}`;
        // The same choices as Orb's own approvals; "this session" becomes native session rules.
        const session = options.suggestions?.length && !options.suppressAlwaysAllowRule;
        const choices = ['y approve once', ...(session ? ['s approve for this session'] : []), 'n deny', 'r deny with a reason'];
        const answer = await ask(`${title}\n\n${details}`, choices, signal);
        if (answer === 'y approve once') return { behavior: 'allow', updatedInput: input };
        if (answer === 's approve for this session') {
          const updatedPermissions = options.suggestions.map(update => ({ ...update, destination: 'session' }));
          await send({ type: 'session', event: updatedPermissions });
          return { behavior: 'allow', updatedInput: input, updatedPermissions };
        }
        const reason = answer === 'r deny with a reason' ? await ask('Why deny this tool call?', [], signal) : '';
        return { behavior: 'deny', message: reason.trim() || 'User denied this action' };
      } catch {
        return { behavior: 'deny', message: 'The user dismissed this request. Do not assume an answer or approval.', interrupt: signal.aborted };
      }
    },
  };
  // Each turn is a new native process, so this Orb session's approvals are re-applied.
  for (const update of config.sessionUpdates ?? []) {
    if (update.type === 'addRules' && update.behavior === 'allow') {
      options.allowedTools = [...(options.allowedTools ?? []), ...update.rules.map(rule => rule.ruleContent ? `${rule.toolName}(${rule.ruleContent})` : rule.toolName)];
    } else if (update.type === 'addDirectories') {
      options.additionalDirectories = [...(options.additionalDirectories ?? []), ...update.directories];
    } else if (update.type === 'setMode' && permissionMode !== 'plan') {
      options.permissionMode = update.mode;
    }
  }
  if (config.effort) options.effort = config.effort;
  if (config.thinking) options.thinking = { type: config.thinking };
  if (config.complete) {
    Object.assign(options, { persistSession: false, tools: [], maxTurns: 1, systemPrompt: config.system });
    let text = '';
    for await (const event of query({ prompt: config.complete, options })) if (event.type === 'result') text = event.result ?? '';
    await send({ type: 'complete', text });
    return;
  }
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
  // Native session IDs come from the SDK. A fork copies the transcript up to the
  // branch point into a new native session, so the original is never rewritten.
  if (config.resume) options.resume = config.fork
    ? (await forkSession(config.resume, { dir: config.cwd, upToMessageId: config.at || undefined })).sessionId
    : config.resume;
  // Keep permission callbacks available until native foreground and background work completes.
  let release;
  const finished = new Promise(resolve => { release = resolve; });
  async function* input() {
    yield { type: 'user', uuid: config.uuid, parent_tool_use_id: null, message: { role: 'user', content: config.content } };
    await finished;
  }
  try {
    active = query({ prompt: input(), options });
    if (cancelled) { await active.interrupt(); return; }
    const tasks = new Set();
    let finishedTurn = false, released = false, levelReported = false;
    for await (const event of active) {
      if (event.type === 'system') {
        if (event.permissionMode) permissionMode = event.permissionMode;
        if (event.subtype === 'background_tasks_changed') {
          levelReported = true;
          tasks.clear();
          for (const task of event.tasks) if (!task.ambient) tasks.add(task.task_id);
        } else if (!levelReported && event.subtype === 'task_started' && event.is_backgrounded && !event.ambient && !event.skip_transcript) {
          tasks.add(event.task_id);
        } else if (!levelReported && event.subtype === 'task_notification') {
          tasks.delete(event.task_id);
        }
      }
      if (tasks.size > 1024) throw new Error('Too many native background tasks');
      if (event.type === 'result') finishedTurn = true;
      const settled = finishedTurn && tasks.size === 0 && !released;
      // The reading precedes the result so the final repaint already shows it.
      if (settled) {
        let timeout;
        try {
          const usage = await Promise.race([
            active.getContextUsage({ detail: 'summary' }),
            new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('Context timeout')), 2000); }),
          ]);
          await send({ type: 'context', event: { maxTokens: usage.maxTokens, totalTokens: usage.totalTokens, percentage: usage.percentage } });
        } catch { /* Context telemetry must not fail a completed turn. */ }
        finally { clearTimeout(timeout); }
      }
      await send({ type: 'sdk', event });
      if (settled) {
        released = true;
        release();
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
