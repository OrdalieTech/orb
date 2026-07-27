export default function stateExtension(pi) {
	pi.registerFlag("state-label", { type: "string", default: "handshake-default" });
	if (pi.getFlag("state-label") !== "handshake-default") {
		throw new Error("synchronous flag getter missed the registered default");
	}
	if (pi.getThinkingLevel() !== "off" || pi.getActiveTools().length !== 0 || pi.getCommands().length !== 0) {
		throw new Error("synchronous getters missed the handshake snapshot");
	}

	pi.events.on("state-channel", (data) => {
		pi.sendUserMessage(`bus:${data.value}`);
	});

	pi.registerCommand("state-probe", {
		description: "Exercise state actions and synchronous mirrors",
		async handler(args, ctx) {
			const before = pi.getSessionName() ?? "unset";
			pi.setSessionName(args);
			const optimistic = pi.getSessionName() ?? "unset";
			const result = await pi.exec("/bin/sh", ["-c", "printf exec-ok"], { cwd: ctx.cwd });
			pi.sendUserMessage(`probe:${before}:${optimistic}:${result.stdout}:${result.code}`);
		},
	});

	pi.registerCommand("state-bus", {
		handler(args) {
			pi.events.emit("state-channel", { value: args });
		},
	});

	pi.registerCommand("state-abort", {
		handler(_args, ctx) {
			ctx.abort();
			pi.sendUserMessage("abort:queued");
		},
	});

	pi.registerCommand("state-exec-abort", {
		async handler() {
			const controller = new AbortController();
			setTimeout(() => controller.abort(), 20);
			const result = await pi.exec("/bin/sh", ["-c", "exec sleep 5"], { signal: controller.signal });
			pi.sendUserMessage(`exec-abort:${result.killed}:${result.code}`);
		},
	});

	pi.registerCommand("state-pi-version", {
		async handler() {
			const result = await pi.exec("pi", ["--version"]);
			pi.sendUserMessage(`pi-version:${result.stdout.trim()}`);
		},
	});

	pi.registerCommand("state-auth", {
		async handler(_args, ctx) {
			const provider = await ctx.modelRegistry.getProviderAuth("openai-codex");
			const model = await ctx.modelRegistry.getApiKeyAndHeaders(ctx.model);
			const key = await ctx.modelRegistry.getApiKeyForProvider("openai-codex");
			pi.sendUserMessage(`auth:${[
				provider?.auth?.apiKey === "codex-key",
				provider?.auth?.headers?.Authorization === "Bearer codex-secret",
				provider?.auth?.baseUrl === "https://chatgpt.com",
				provider?.env?.CODEX_ENV === "provider",
				provider?.source === "OAuth",
				model?.ok === true,
				model?.apiKey === "codex-key",
				model?.headers?.Authorization === "Bearer codex-secret",
				model?.headers?.["X-Model"] === "provider",
				model?.env?.CODEX_ENV === "provider",
				key === "codex-key",
			].every(Boolean)}`);
		},
	});

	pi.registerCommand("state-auth-readonly", {
		async handler(_args, ctx) {
			for (let round = 0; round < 2; round++) {
				const model = await ctx.modelRegistry.getApiKeyAndHeaders(ctx.model);
				const provider = await ctx.modelRegistry.getProviderAuth("openai-codex");
				if (
					model?.ok !== true ||
					model?.headers?.Authorization !== "Bearer codex-secret" ||
					provider?.auth?.headers?.Authorization !== "Bearer codex-secret"
				) throw new Error("readonly auth sequence returned the wrong credentials");
			}
		},
	});

	pi.registerCommand("state-late-append", {
		handler() {
			setTimeout(() => pi.appendEntry("late", {}), 20);
		},
	});

	pi.registerCommand("state-signal", {
		async handler(_args, ctx) {
			if (!ctx.signal || typeof ctx.signal.addEventListener !== "function") throw new Error("ctx.signal is not an AbortSignal");
			pi.sendUserMessage("signal:ready");
			if (!ctx.signal.aborted) {
				await new Promise((resolve) => ctx.signal.addEventListener("abort", resolve, { once: true }));
			}
			pi.sendUserMessage(`signal:aborted:${ctx.signal.aborted}:${String(ctx.signal.reason)}`);
		},
	});

	pi.on("agent_start", () => {
		const active = pi.getActiveTools().join(",");
		const command = pi.getCommands()[0]?.name ?? "none";
		pi.sendUserMessage(`delta:${pi.getSessionName() ?? "unset"}:${active}:${command}:${pi.getThinkingLevel()}`);
	});

	pi.on("tool_call", (event) => {
		if (event.type !== "tool_call") throw new Error(`unexpected event type ${event.type}`);
		event.input.hostMutated = true;
		if (event.toolName === "blocked") return { block: true, reason: "blocked by host fixture" };
	});

	pi.on("session_before_tree", (event) => {
		pi.sendUserMessage(`event-signal:${event.type}:${typeof event.signal?.addEventListener === "function"}:${event.signal?.aborted}`);
	});

	pi.on("session_start", (_event, ctx) => {
		const manager = ctx.sessionManager;
		const entries = manager.getEntries();
		const tree = manager.getTree();
		const context = manager.buildSessionContext();
		pi.sendUserMessage(`session-probe:${JSON.stringify({
			id: manager.getSessionId(),
			persisted: manager.isPersisted(),
			entries: entries.length,
			branch: manager.getBranch().length,
			children: manager.getChildren(entries[0]?.id).length,
			label: manager.getLabel(entries[0]?.id),
			contextEntries: manager.buildContextEntries().map((entry) => entry.type),
			contextMessages: context.messages.map((message) => message.role),
			model: context.model,
			roots: tree.length,
			rootLabel: tree[0]?.label,
		})}`);
	});

	pi.on("before_provider_request", (event) => {
		if (event.payload.replaceWithNull === true) return null;
		return { ...event.payload, hostMutated: true };
	});

	pi.on("before_provider_headers", (event) => {
		event.headers["x-state-host"] = "mutated";
	});

	pi.on("context", (event) => {
		event.messages = [{ role: "user", content: "context-host", timestamp: 7 }];
	});

	pi.on("message_end", (event) => {
		if (event.message.role !== "user") return;
		return { message: { ...event.message, content: "message-host" } };
	});

	pi.on("tool_result", () => ({
		content: [{ type: "text", text: "tool-result-host" }],
		details: { source: "host" },
		isError: false,
	}));

	pi.on("user_bash", (event) => {
		if (event.command !== "delegate") return;
		return {
			operations: {
				exec(command, cwd, options) {
					options.onData(Buffer.from(`${command}@${cwd}`));
					return { exitCode: 7 };
				},
			},
		};
	});
}
