// Only this host speaks Anthropic's SDK protocol. No credentials cross its pipes.
import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';
import { once } from 'node:events';
const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
const pending = new Map();
let active, nextID = 0, idle, open = false;
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
function ask(title, choices, signal, ruled = false) { return request({ type: 'input', title, choices, ruled }, signal); }
async function run(config) {
  const { query } = await import(pathToFileURL(config.sdk));
  let permissionMode = config.permissionMode || 'default';
  const options = {
    cwd: config.cwd, pathToClaudeCodeExecutable: config.claude,
    model: config.model === "default" ? undefined : config.model, includePartialMessages: true, agentProgressSummaries: true,
    systemPrompt: { type: 'preset', preset: 'claude_code', append: config.append || undefined },
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
        const title = name === 'ExitPlanMode' ? 'Approve this plan?'
          : `Permission requested for ${name}${options.agentID ? ' (Claude subagent)' : ''}`;
        // The same choices as Orb's own approvals; "this session" becomes native session rules.
        const session = options.suggestions?.length && !options.suppressAlwaysAllowRule;
        const choices = ['y approve once', ...(session ? ['s approve for this session'] : []), 'n deny', 'r deny with a reason'];
        // A user's own ask rule always wants a human; other asks may use Orb's headless fallback.
        const answer = await ask(`${title}\n\n${details}`, choices, signal, Boolean(options.matchedAskRule) || name === 'ExitPlanMode');
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
  // A restarted host re-applies this Orb session's approvals.
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
  // A control request runs no model turn and writes no transcript.
  async function control(ask) {
    options.persistSession = false;
    let release;
    const done = new Promise(resolve => { release = resolve; });
    async function* empty() { await done; }
    try {
      active = query({ prompt: empty(), options });
      await send(await ask(active));
    } finally { release(); active?.close(); active = undefined; }
  }
  if (config.catalog) return control(async q => ({ type: 'catalog', models: await q.supportedModels() }));
  // ponytail: the SDK marks this experimental; the pinned version keeps it, and a failure shows usage as unavailable.
  if (config.usage) return control(async q => {
    const usage = await q.usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET({ skipBehaviors: true });
    return { type: 'usage', plan: usage.subscription_type, limits: usage.rate_limits_available ? usage.rate_limits : null };
  });
  // An Orb conversation is one Claude Code session: Orb brings it up to the
  // branch it holds, Claude resumes it there, and Orb reads back what Claude wrote.
  if (config.resume) options.resume = config.resume;
  if (config.at) options.resumeSessionAt = config.at;
  if (config.session) options.sessionId = config.session;
  // One live query per Orb session: prompts arrive on stdin. An Orb turn settles once
  // Claude answered its prompts, no native turn is due and no background task runs.
  active = query({ prompt: input(), options });
  const tasks = new Set();
  let session = config.resume ?? config.session, echoes = false, due = false;
  for await (const event of active) {
    if (event.session_id && !event.parent_tool_use_id) session = event.session_id;
    if (event.type === 'system') {
      if (event.permissionMode) permissionMode = event.permissionMode;
      // ponytail: 2.1.280 is the oldest CLI verified to echo prompt uuids; older ones settle on any result.
      if (event.subtype === 'init') echoes = !older(event.claude_code_version, [2, 1, 280]);
      // A task runs until its notification: Claude reads it in the running turn, or in a
      // turn of its own when it finished between turns.
      if (event.subtype === 'background_tasks_changed') {
        for (const task of event.tasks) if (!task.ambient) tasks.add(task.task_id);
      } else if (event.subtype === 'task_started' && event.is_backgrounded && !event.ambient && !event.skip_transcript) {
        tasks.add(event.task_id);
      } else if (event.subtype === 'task_notification' && tasks.delete(event.task_id)) {
        due = true;
      }
    }
    if (tasks.size > 1024) throw new Error('Too many native background tasks');
    // A result echoing no prompt ends a native turn Orb did not send (a task's
    // notification, an auto-continuation after resume): it answers none of Orb's.
    if (event.type === 'result') {
      const echo = event.user_message_uuids ?? (event.user_message_uuid ? [event.user_message_uuid] : []);
      for (const id of echo) awaited.delete(id);
      if (!echoes || (!echo.length && event.is_error)) awaited.clear();
      due = event.queued_turn_count > 0;
    }
    const settled = open && awaited.size === 0 && !due && tasks.size === 0;
    // The reading precedes the result so the final repaint already shows it; each
    // native turn's result refreshes it, so a long Orb turn keeps it current.
    if (settled || (event.type === 'result' && !due)) {
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
      open = false;
      await send({ type: 'settled', session });
      // ponytail: an idle host exits after 10 minutes; the next prompt resumes it.
      idle = setTimeout(() => lines.close(), 10 * 60 * 1000);
    }
  }
}
const older = (version, floor) => {
  const parts = String(version ?? '').split('.').map(Number);
  const i = floor.findIndex((n, i) => parts[i] !== n);
  return i >= 0 && !(parts[i] > floor[i]);
};
const inbox = [], awaited = new Set();
let wake, ended = false;
async function* input() {
  for (;;) {
    while (inbox.length) yield inbox.shift();
    if (ended) return;
    await new Promise(resolve => { wake = resolve; });
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
    } else if (message.type === 'prompt') {
      clearTimeout(idle);
      open = true;
      awaited.add(message.uuid);
      inbox.push({ type: 'user', uuid: message.uuid, parent_tool_use_id: null, message: { role: 'user', content: message.content } });
      wake?.();
    } else if (message.type === 'reply') {
      const resolve = pending.get(message.id);
      pending.delete(message.id);
      resolve?.(message.value, message.cancelled);
    } else if (message.type === 'cancel') {
      active?.interrupt().catch(() => {});
    }
  } catch { active?.close(); process.exitCode = 1; lines.close(); process.stdin.destroy(); }
});
// Orb closing stdin (idle, disposal or exit) ends the prompt stream, so the native
// session finishes its current turn and exits; Orb kills a host that lingers.
lines.on('close', () => { if (!running) process.exitCode = 1; clearTimeout(idle); ended = true; wake?.(); });
