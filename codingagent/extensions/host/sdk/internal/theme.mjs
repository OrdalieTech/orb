// orb-extension-sdk: access to the host's active-theme snapshot.
// host.mjs publishes the theme facade under these globals before invoking any
// extension factory or renderer (activateSDKTheme); theme-dependent SDK
// symbols (getMarkdownTheme, renderDiff) read it here.
const THEME_GLOBALS = [
	Symbol.for("@earendil-works/pi-coding-agent:theme"),
	Symbol.for("@mariozechner/pi-coding-agent:theme"),
];

const identity = (text) => String(text);

// Colorless fallback so theme-dependent symbols stay callable outside a themed
// host context (module init, unit tests): every style function is identity.
const fallbackTheme = {
	fg: (_color, text) => String(text),
	bg: (_color, text) => String(text),
	bold: identity,
	italic: identity,
	underline: identity,
	inverse: identity,
	strikethrough: identity,
};

/** The host theme facade ({fg,bg,bold,italic,underline,inverse,...}) or a colorless fallback. */
export function activeTheme() {
	for (const key of THEME_GLOBALS) {
		const theme = globalThis[key];
		if (theme && typeof theme.fg === "function") return theme;
	}
	return fallbackTheme;
}
