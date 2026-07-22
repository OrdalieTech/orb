export default function (pi) {
	pi.registerTool({
		name: "host_ui_surface",
		label: "Host UI surface",
		description: "Exercise extension-host UI transport",
		parameters: { type: "object" },
		async execute(_toolCallId, _params, _signal, _onUpdate, ctx) {
			ctx.ui.notify("host notification", "warning");
			ctx.ui.setStatus("host-status", "working");
			ctx.ui.setStatus("host-status", undefined);
			ctx.ui.setWorkingMessage("host working");
			ctx.ui.setWorkingVisible(false);
			ctx.ui.setWorkingIndicator({ frames: ["a", "b"], intervalMs: 25 });
			ctx.ui.setHiddenThinkingLabel("host thinking");
			ctx.ui.setWidget("host-widget", ["widget one", "widget two"], { placement: "belowEditor" });
			ctx.ui.setWidget("host-widget", undefined);
			ctx.ui.setTitle("host title");
			ctx.ui.pasteToEditor("pasted text");
			ctx.ui.setEditorText("editor text");
			ctx.ui.setToolsExpanded(true);

			const selected = await ctx.ui.select("Pick one", ["first", "second"], { timeout: 5000 });
			const confirmed = await ctx.ui.confirm("Confirm", "Continue?", { timeout: 5000 });
			const input = await ctx.ui.input("Input", "placeholder", { timeout: 5000 });
			const edited = await ctx.ui.editor("Editor", "prefill");
			const abortController = new AbortController();
			const abortedPromise = ctx.ui.select("Abort dialog", ["wait"], { signal: abortController.signal });
			abortController.abort();
			const aborted = await abortedPromise;
			const custom = await ctx.ui.custom((_tui, theme, keybindings, done) => {
				let count = 0;
				return {
					focused: false,
					render(width) {
						return [theme.bold(`count:${count}`), `width:${width}`, `keys:${keybindings.keys("app.interrupt").join(",")}`, `focused:${this.focused}`];
					},
					handleInput(data) {
						if (data === "+") count += 1;
						if (data === "q") done({ count });
					},
				};
			});

			return {
				content: [{ type: "text", text: "ui complete" }],
				details: {
					selected,
					confirmed,
					input,
					edited,
					aborted: aborted === undefined,
					custom,
					editorText: ctx.ui.getEditorText(),
					toolsExpanded: ctx.ui.getToolsExpanded(),
				},
			};
		},
	});

	pi.registerTool({
		name: "host_ui_pending_dialog",
		label: "Pending host dialog",
		description: "Wait for a host-backed dialog",
		parameters: { type: "object" },
		async execute(_toolCallId, _params, _signal, _onUpdate, ctx) {
			await ctx.ui.select("Pending dialog", ["wait"]);
			return { content: [{ type: "text", text: "unexpected" }] };
		},
	});

	pi.registerTool({
		name: "host_ui_crash",
		label: "Crash host UI fixture",
		description: "Terminate the extension host",
		parameters: { type: "object" },
		execute() {
			process.exit(82);
		},
	});
}
