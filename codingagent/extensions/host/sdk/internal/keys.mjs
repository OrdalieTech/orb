// orb-extension-sdk: terminal input → key-id parsing.
// Ported from pi-tui (pi 0.84.1, commit 53fa77cc, packages/tui/src/keys.ts,
// MIT © Mario Zechner), trimmed to parseKey with the Kitty protocol treated as
// inactive: orb's host TUI feeds raw legacy bytes to components, so the
// kitty-active disambiguation branches are folded to their legacy outcomes.
// CSI-u / modified-arrow / functional CSI forms are still parsed, since
// terminals emit them for modified keys regardless of protocol negotiation.

const MODIFIERS = { shift: 1, alt: 2, ctrl: 4, super: 8 };
const LOCK_MASK = 64 + 128; // Caps Lock + Num Lock

const CODEPOINTS = { escape: 27, tab: 9, enter: 13, space: 32, backspace: 127, kpEnter: 57414 };
const ARROW_CODEPOINTS = { up: -1, down: -2, right: -3, left: -4 };
const FUNCTIONAL_CODEPOINTS = { delete: -10, insert: -11, pageUp: -12, pageDown: -13, home: -14, end: -15 };

const KITTY_FUNCTIONAL_KEY_EQUIVALENTS = new Map([
	[57399, 48], [57400, 49], [57401, 50], [57402, 51], [57403, 52], [57404, 53], [57405, 54],
	[57406, 55], [57407, 56], [57408, 57], [57409, 46], [57410, 47], [57411, 42], [57412, 45],
	[57413, 43], [57415, 61], [57416, 44],
	[57417, ARROW_CODEPOINTS.left], [57418, ARROW_CODEPOINTS.right],
	[57419, ARROW_CODEPOINTS.up], [57420, ARROW_CODEPOINTS.down],
	[57421, FUNCTIONAL_CODEPOINTS.pageUp], [57422, FUNCTIONAL_CODEPOINTS.pageDown],
	[57423, FUNCTIONAL_CODEPOINTS.home], [57424, FUNCTIONAL_CODEPOINTS.end],
	[57425, FUNCTIONAL_CODEPOINTS.insert], [57426, FUNCTIONAL_CODEPOINTS.delete],
]);

const SYMBOL_KEYS = new Set([
	"`", "-", "=", "[", "]", "\\", ";", "'", ",", ".", "/", "!", "@", "#", "$", "%", "^", "&",
	"*", "(", ")", "_", "+", "|", "~", "{", "}", ":", "<", ">", "?",
]);

const LEGACY_SEQUENCE_KEY_IDS = {
	"\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left",
	"\x1bOH": "home", "\x1bOF": "end",
	"\x1b[E": "clear", "\x1bOE": "clear", "\x1bOe": "ctrl+clear", "\x1b[e": "shift+clear",
	"\x1b[2~": "insert", "\x1b[2$": "shift+insert", "\x1b[2^": "ctrl+insert",
	"\x1b[3$": "shift+delete", "\x1b[3^": "ctrl+delete",
	"\x1b[[5~": "pageUp", "\x1b[[6~": "pageDown",
	"\x1b[a": "shift+up", "\x1b[b": "shift+down", "\x1b[c": "shift+right", "\x1b[d": "shift+left",
	"\x1bOa": "ctrl+up", "\x1bOb": "ctrl+down", "\x1bOc": "ctrl+right", "\x1bOd": "ctrl+left",
	"\x1b[5$": "shift+pageUp", "\x1b[6$": "shift+pageDown",
	"\x1b[7$": "shift+home", "\x1b[8$": "shift+end",
	"\x1b[5^": "ctrl+pageUp", "\x1b[6^": "ctrl+pageDown",
	"\x1b[7^": "ctrl+home", "\x1b[8^": "ctrl+end",
	"\x1b[1~": "home", "\x1b[4~": "end", "\x1b[7~": "home", "\x1b[8~": "end",
	"\x1bOP": "f1", "\x1bOQ": "f2", "\x1bOR": "f3", "\x1bOS": "f4",
	"\x1b[11~": "f1", "\x1b[12~": "f2", "\x1b[13~": "f3", "\x1b[14~": "f4",
	"\x1b[[A": "f1", "\x1b[[B": "f2", "\x1b[[C": "f3", "\x1b[[D": "f4", "\x1b[[E": "f5",
	"\x1b[15~": "f5", "\x1b[17~": "f6", "\x1b[18~": "f7", "\x1b[19~": "f8",
	"\x1b[20~": "f9", "\x1b[21~": "f10", "\x1b[23~": "f11", "\x1b[24~": "f12",
};

function parseCsiSequence(data) {
	// CSI u (Kitty format, emitted by xterm modifyOtherKeys-era terminals too):
	// \x1b[<codepoint>[:<shifted>[:<base>]][;<mod>[:<event>]]u
	const csiUMatch = data.match(/^\x1b\[(\d+)(?::(\d*))?(?::(\d+))?(?:;(\d+))?(?::(\d+))?u$/);
	if (csiUMatch) {
		return {
			codepoint: Number.parseInt(csiUMatch[1], 10),
			baseLayoutKey: csiUMatch[3] ? Number.parseInt(csiUMatch[3], 10) : undefined,
			modifier: (csiUMatch[4] ? Number.parseInt(csiUMatch[4], 10) : 1) - 1,
		};
	}
	// Arrow keys with modifier: \x1b[1;<mod>[:<event>]A/B/C/D
	const arrowMatch = data.match(/^\x1b\[1;(\d+)(?::(\d+))?([ABCD])$/);
	if (arrowMatch) {
		const arrowCodes = { A: -1, B: -2, C: -3, D: -4 };
		return { codepoint: arrowCodes[arrowMatch[3]], modifier: Number.parseInt(arrowMatch[1], 10) - 1 };
	}
	// Functional keys: \x1b[<num>[;<mod>[:<event>]]~
	const funcMatch = data.match(/^\x1b\[(\d+)(?:;(\d+))?(?::(\d+))?~$/);
	if (funcMatch) {
		const funcCodes = {
			2: FUNCTIONAL_CODEPOINTS.insert, 3: FUNCTIONAL_CODEPOINTS.delete,
			5: FUNCTIONAL_CODEPOINTS.pageUp, 6: FUNCTIONAL_CODEPOINTS.pageDown,
			7: FUNCTIONAL_CODEPOINTS.home, 8: FUNCTIONAL_CODEPOINTS.end,
		};
		const codepoint = funcCodes[Number.parseInt(funcMatch[1], 10)];
		if (codepoint !== undefined && funcMatch[2] !== undefined) {
			return { codepoint, modifier: Number.parseInt(funcMatch[2], 10) - 1 };
		}
		return null; // unmodified forms are handled by the legacy tables
	}
	// Home/End with modifier: \x1b[1;<mod>[:<event>]H/F
	const homeEndMatch = data.match(/^\x1b\[1;(\d+)(?::(\d+))?([HF])$/);
	if (homeEndMatch) {
		return {
			codepoint: homeEndMatch[3] === "H" ? FUNCTIONAL_CODEPOINTS.home : FUNCTIONAL_CODEPOINTS.end,
			modifier: Number.parseInt(homeEndMatch[1], 10) - 1,
		};
	}
	return null;
}

function normalizeKittyFunctionalCodepoint(codepoint) {
	return KITTY_FUNCTIONAL_KEY_EQUIVALENTS.get(codepoint) ?? codepoint;
}

function normalizeShiftedLetterIdentityCodepoint(codepoint, modifier) {
	const effectiveModifier = modifier & ~LOCK_MASK;
	if ((effectiveModifier & MODIFIERS.shift) !== 0 && codepoint >= 65 && codepoint <= 90) {
		return codepoint + 32;
	}
	return codepoint;
}

function formatKeyNameWithModifiers(keyName, modifier) {
	const mods = [];
	const effectiveMod = modifier & ~LOCK_MASK;
	const supportedModifierMask = MODIFIERS.shift | MODIFIERS.ctrl | MODIFIERS.alt | MODIFIERS.super;
	if ((effectiveMod & ~supportedModifierMask) !== 0) return undefined;
	if (effectiveMod & MODIFIERS.shift) mods.push("shift");
	if (effectiveMod & MODIFIERS.ctrl) mods.push("ctrl");
	if (effectiveMod & MODIFIERS.alt) mods.push("alt");
	if (effectiveMod & MODIFIERS.super) mods.push("super");
	return mods.length > 0 ? `${mods.join("+")}+${keyName}` : keyName;
}

function formatParsedKey(codepoint, modifier, baseLayoutKey) {
	const normalizedCodepoint = normalizeKittyFunctionalCodepoint(codepoint);
	const identityCodepoint = normalizeShiftedLetterIdentityCodepoint(normalizedCodepoint, modifier);
	const isLatinLetter = identityCodepoint >= 97 && identityCodepoint <= 122;
	const isDigit = identityCodepoint >= 48 && identityCodepoint <= 57;
	const isKnownSymbol = SYMBOL_KEYS.has(String.fromCharCode(identityCodepoint));
	const effectiveCodepoint =
		isLatinLetter || isDigit || isKnownSymbol ? identityCodepoint : (baseLayoutKey ?? identityCodepoint);
	let keyName;
	if (effectiveCodepoint === CODEPOINTS.escape) keyName = "escape";
	else if (effectiveCodepoint === CODEPOINTS.tab) keyName = "tab";
	else if (effectiveCodepoint === CODEPOINTS.enter || effectiveCodepoint === CODEPOINTS.kpEnter) keyName = "enter";
	else if (effectiveCodepoint === CODEPOINTS.space) keyName = "space";
	else if (effectiveCodepoint === CODEPOINTS.backspace) keyName = "backspace";
	else if (effectiveCodepoint === FUNCTIONAL_CODEPOINTS.delete) keyName = "delete";
	else if (effectiveCodepoint === FUNCTIONAL_CODEPOINTS.insert) keyName = "insert";
	else if (effectiveCodepoint === FUNCTIONAL_CODEPOINTS.home) keyName = "home";
	else if (effectiveCodepoint === FUNCTIONAL_CODEPOINTS.end) keyName = "end";
	else if (effectiveCodepoint === FUNCTIONAL_CODEPOINTS.pageUp) keyName = "pageUp";
	else if (effectiveCodepoint === FUNCTIONAL_CODEPOINTS.pageDown) keyName = "pageDown";
	else if (effectiveCodepoint === ARROW_CODEPOINTS.up) keyName = "up";
	else if (effectiveCodepoint === ARROW_CODEPOINTS.down) keyName = "down";
	else if (effectiveCodepoint === ARROW_CODEPOINTS.left) keyName = "left";
	else if (effectiveCodepoint === ARROW_CODEPOINTS.right) keyName = "right";
	else if (effectiveCodepoint >= 48 && effectiveCodepoint <= 57) keyName = String.fromCharCode(effectiveCodepoint);
	else if (effectiveCodepoint >= 97 && effectiveCodepoint <= 122) keyName = String.fromCharCode(effectiveCodepoint);
	else if (SYMBOL_KEYS.has(String.fromCharCode(effectiveCodepoint))) {
		keyName = String.fromCharCode(effectiveCodepoint);
	}
	if (!keyName) return undefined;
	return formatKeyNameWithModifiers(keyName, modifier);
}

function isWindowsTerminalSession() {
	return process.platform === "win32" && Boolean(process.env.WT_SESSION);
}

/** Parse raw terminal input into a key id ("up", "ctrl+u", "escape", "G", ...) or undefined. */
export function parseKey(data) {
	const csi = parseCsiSequence(data);
	if (csi) return formatParsedKey(csi.codepoint, csi.modifier, csi.baseLayoutKey);

	const legacySequenceKeyId = LEGACY_SEQUENCE_KEY_IDS[data];
	if (legacySequenceKeyId) return legacySequenceKeyId;

	if (data === "\x1b") return "escape";
	if (data === "\x1c") return "ctrl+\\";
	if (data === "\x1d") return "ctrl+]";
	if (data === "\x1f") return "ctrl+-";
	if (data === "\x1b\x1b") return "ctrl+alt+[";
	if (data === "\x1b\x1c") return "ctrl+alt+\\";
	if (data === "\x1b\x1d") return "ctrl+alt+]";
	if (data === "\x1b\x1f") return "ctrl+alt+-";
	if (data === "\t") return "tab";
	if (data === "\r" || data === "\n" || data === "\x1bOM") return "enter";
	if (data === "\x00") return "ctrl+space";
	if (data === " ") return "space";
	if (data === "\x7f") return "backspace";
	if (data === "\x08") return isWindowsTerminalSession() ? "ctrl+backspace" : "backspace";
	if (data === "\x1b[Z") return "shift+tab";
	if (data === "\x1b\r") return "alt+enter";
	if (data === "\x1b ") return "alt+space";
	if (data === "\x1b\x7f" || data === "\x1b\b") return "alt+backspace";
	if (data === "\x1bB") return "alt+left";
	if (data === "\x1bF") return "alt+right";
	if (data.length === 2 && data[0] === "\x1b") {
		const code = data.charCodeAt(1);
		if (code >= 1 && code <= 26) return `ctrl+alt+${String.fromCharCode(code + 96)}`;
		const key = String.fromCharCode(code);
		if ((code >= 97 && code <= 122) || (code >= 48 && code <= 57) || SYMBOL_KEYS.has(key)) {
			return `alt+${key}`;
		}
	}
	if (data === "\x1b[A") return "up";
	if (data === "\x1b[B") return "down";
	if (data === "\x1b[C") return "right";
	if (data === "\x1b[D") return "left";
	if (data === "\x1b[H" || data === "\x1bOH") return "home";
	if (data === "\x1b[F" || data === "\x1bOF") return "end";
	if (data === "\x1b[3~") return "delete";
	if (data === "\x1b[5~") return "pageUp";
	if (data === "\x1b[6~") return "pageDown";

	if (data.length === 1) {
		const code = data.charCodeAt(0);
		if (code >= 1 && code <= 26) return `ctrl+${String.fromCharCode(code + 96)}`;
		if (code >= 32 && code <= 126) return data;
	}
	return undefined;
}
