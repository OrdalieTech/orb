export default function (pi) {
	pi.registerShortcut("ctrl+alt+h", {
		description: "Host shortcut",
		handler(ctx) {
			if (!ctx.cwd) throw new Error("shortcut context has no cwd");
		},
	});
	pi.registerMessageRenderer("host-message", (message, options, theme) => ({
		render(width) {
			return [`message:${message.content}:${options.expanded}:${theme.getColorMode()}:${width}`];
		},
	}));
	pi.registerEntryRenderer("host-entry", (entry, options) => ({
		render(width) {
			return [`entry:${entry.value}:${options.expanded}:${width}`];
		},
	}));
}
