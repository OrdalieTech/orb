// orb-extension-sdk: ANSI-aware text measurement, wrapping, and truncation.
// Ported from pi-tui (pi 0.84.1, commit 53fa77cc, packages/tui/src/utils.ts,
// MIT © Mario Zechner), trimmed to what published extensions exercise. The
// upstream dependency on get-east-asian-width is replaced by a compact wide
// range table; emoji handling matches upstream's RGI_Emoji path.

const graphemeSegmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });

// Regexes for character classification (same as upstream / string-width).
const zeroWidthRegex = /^(?:\p{Default_Ignorable_Code_Point}|\p{Control}|\p{Mark}|\p{Surrogate})+$/v;
const leadingNonPrintingRegex =
	/^[\p{Default_Ignorable_Code_Point}\p{Control}\p{Format}\p{Mark}\p{Surrogate}]+/v;
const nonPrintingCharRegex =
	/^(?:\p{Default_Ignorable_Code_Point}|\p{Control}|\p{Format}|\p{Mark}|\p{Surrogate})$/v;
const markCharRegex = /^\p{Mark}$/v;
const terminalSpacingMarkRegex =
	/^(?:[\p{Spacing_Mark}--[\u1734\u302E\u302F]]|[\u065F\u0F7F\u102B\u102C\u1031\u1033-\u1035\u1038\u103A-\u103E])+$/v;
const rgiEmojiRegex = /^\p{RGI_Emoji}$/v;

export const cjkBreakRegex =
	/[\p{Script_Extensions=Han}\p{Script_Extensions=Hiragana}\p{Script_Extensions=Katakana}\p{Script_Extensions=Hangul}\p{Script_Extensions=Bopomofo}]/u;

// East Asian Wide + Fullwidth ranges (Unicode EastAsianWidth W/F), sorted.
const WIDE_RANGES = [
	[0x1100, 0x115f], [0x231a, 0x231b], [0x2329, 0x232a], [0x23e9, 0x23ec], [0x23f0, 0x23f0],
	[0x23f3, 0x23f3], [0x25fd, 0x25fe], [0x2614, 0x2615], [0x2648, 0x2653], [0x267f, 0x267f],
	[0x2693, 0x2693], [0x26a1, 0x26a1], [0x26aa, 0x26ab], [0x26bd, 0x26be], [0x26c4, 0x26c5],
	[0x26ce, 0x26ce], [0x26d4, 0x26d4], [0x26ea, 0x26ea], [0x26f2, 0x26f3], [0x26f5, 0x26f5],
	[0x26fa, 0x26fa], [0x26fd, 0x26fd], [0x2705, 0x2705], [0x270a, 0x270b], [0x2728, 0x2728],
	[0x274c, 0x274c], [0x274e, 0x274e], [0x2753, 0x2755], [0x2757, 0x2757], [0x2795, 0x2797],
	[0x27b0, 0x27b0], [0x27bf, 0x27bf], [0x2b1b, 0x2b1c], [0x2b50, 0x2b50], [0x2b55, 0x2b55],
	[0x2e80, 0x303e], [0x3041, 0x33ff], [0x3400, 0x4dbf], [0x4e00, 0x9fff], [0xa000, 0xa4cf],
	[0xa960, 0xa97f], [0xac00, 0xd7a3], [0xf900, 0xfaff], [0xfe10, 0xfe19], [0xfe30, 0xfe52],
	[0xfe54, 0xfe66], [0xfe68, 0xfe6b], [0xff01, 0xff60], [0xffe0, 0xffe6], [0x16fe0, 0x16fe4],
	[0x17000, 0x187f7], [0x18800, 0x18cd5], [0x1b000, 0x1b2fb], [0x1f004, 0x1f004],
	[0x1f0cf, 0x1f0cf], [0x1f18e, 0x1f18e], [0x1f191, 0x1f19a], [0x1f200, 0x1f202],
	[0x1f210, 0x1f23b], [0x1f240, 0x1f248], [0x1f250, 0x1f251], [0x1f260, 0x1f265],
	[0x1f300, 0x1f64f], [0x1f680, 0x1f6ff], [0x1f900, 0x1f9ff], [0x1fa00, 0x1faff],
	[0x20000, 0x2fffd], [0x30000, 0x3fffd],
];

function eastAsianWidth(cp) {
	let low = 0;
	let high = WIDE_RANGES.length - 1;
	while (low <= high) {
		const mid = (low + high) >> 1;
		const [start, end] = WIDE_RANGES[mid];
		if (cp < start) high = mid - 1;
		else if (cp > end) low = mid + 1;
		else return 2;
	}
	return 1;
}

function couldBeEmoji(segment) {
	const cp = segment.codePointAt(0);
	return (
		(cp >= 0x1f000 && cp <= 0x1fbff) ||
		(cp >= 0x2300 && cp <= 0x23ff) ||
		(cp >= 0x2600 && cp <= 0x27bf) ||
		(cp >= 0x2b50 && cp <= 0x2b55) ||
		segment.includes("\uFE0F") ||
		segment.length > 2
	);
}

export function isPrintableAscii(str) {
	for (let i = 0; i < str.length; i++) {
		const code = str.charCodeAt(i);
		if (code < 0x20 || code > 0x7e) return false;
	}
	return true;
}

/** Extract one ANSI CSI/OSC/APC escape sequence at pos, or null. */
export function extractAnsiCode(str, pos) {
	if (pos >= str.length || str[pos] !== "\x1b") return null;
	const next = str[pos + 1];
	if (next === "[") {
		let j = pos + 2;
		while (j < str.length && !/[mGKHJ]/.test(str[j])) j++;
		if (j < str.length) return { code: str.substring(pos, j + 1), length: j + 1 - pos };
		return null;
	}
	if (next === "]" || next === "_") {
		let j = pos + 2;
		while (j < str.length) {
			if (str[j] === "\x07") return { code: str.substring(pos, j + 1), length: j + 1 - pos };
			if (str[j] === "\x1b" && str[j + 1] === "\\") {
				return { code: str.substring(pos, j + 2), length: j + 2 - pos };
			}
			j++;
		}
		return null;
	}
	return null;
}

export function graphemeWidth(segment) {
	if (segment === "\t") return 3;
	if (terminalSpacingMarkRegex.test(segment)) return [...segment].length;
	if (zeroWidthRegex.test(segment)) return 0;
	if (couldBeEmoji(segment) && rgiEmojiRegex.test(segment)) return 2;
	const base = segment.replace(leadingNonPrintingRegex, "");
	const cp = base.codePointAt(0);
	if (cp === undefined) return 0;
	if (cp >= 0x1f1e6 && cp <= 0x1f1ff) return 2; // regional indicators
	let width = eastAsianWidth(cp);
	let followsMark = false;
	const chars = [...base];
	for (const char of chars.slice(1)) {
		if (terminalSpacingMarkRegex.test(char)) {
			width += 1;
			followsMark = false;
		} else if (markCharRegex.test(char)) {
			followsMark = true;
		} else if (!nonPrintingCharRegex.test(char)) {
			const c = char.codePointAt(0);
			if (followsMark || (c >= 0xff00 && c <= 0xffef)) width += eastAsianWidth(c);
			else if (c === 0x0e33 || c === 0x0eb3) width += 1;
			followsMark = false;
		}
	}
	return width;
}

const WIDTH_CACHE_SIZE = 512;
const widthCache = new Map();

/** Visible width of a string in terminal columns (ANSI-, tab- and wide-char-aware). */
export function visibleWidth(str) {
	if (str.length === 0) return 0;
	if (isPrintableAscii(str)) return str.length;
	const cached = widthCache.get(str);
	if (cached !== undefined) return cached;
	let clean = str;
	if (str.includes("\t")) clean = clean.replace(/\t/g, "   ");
	if (clean.includes("\x1b")) {
		let stripped = "";
		let i = 0;
		while (i < clean.length) {
			const ansi = extractAnsiCode(clean, i);
			if (ansi) {
				i += ansi.length;
				continue;
			}
			stripped += clean[i];
			i++;
		}
		clean = stripped;
	}
	let width = 0;
	for (const { segment } of graphemeSegmenter.segment(clean)) width += graphemeWidth(segment);
	if (widthCache.size >= WIDTH_CACHE_SIZE) {
		const firstKey = widthCache.keys().next().value;
		if (firstKey !== undefined) widthCache.delete(firstKey);
	}
	widthCache.set(str, width);
	return width;
}

// ── ANSI state tracking (carries styles across wraps) ────────────────────────

function parseOsc8Hyperlink(ansiCode) {
	if (!ansiCode.startsWith("\x1b]8;")) return undefined;
	const terminator = ansiCode.endsWith("\x07") ? "\x07" : "\x1b\\";
	const body = ansiCode.slice(4, terminator === "\x07" ? -1 : -2);
	const separatorIndex = body.indexOf(";");
	if (separatorIndex === -1) return undefined;
	const params = body.slice(0, separatorIndex);
	const url = body.slice(separatorIndex + 1);
	if (!url) return null;
	return { params, url, terminator };
}

export class AnsiCodeTracker {
	constructor() {
		this.clear();
	}

	process(ansiCode) {
		const hyperlink = parseOsc8Hyperlink(ansiCode);
		if (hyperlink !== undefined) {
			this.activeHyperlink = hyperlink;
			return;
		}
		if (!ansiCode.endsWith("m")) return;
		const match = ansiCode.match(/\x1b\[([\d;]*)m/);
		if (!match) return;
		const params = match[1];
		if (params === "" || params === "0") {
			this.#reset();
			return;
		}
		const parts = params.split(";");
		let i = 0;
		while (i < parts.length) {
			const code = Number.parseInt(parts[i], 10);
			if (code === 38 || code === 48) {
				if (parts[i + 1] === "5" && parts[i + 2] !== undefined) {
					const colorCode = `${parts[i]};${parts[i + 1]};${parts[i + 2]}`;
					if (code === 38) this.fgColor = colorCode;
					else this.bgColor = colorCode;
					i += 3;
					continue;
				}
				if (parts[i + 1] === "2" && parts[i + 4] !== undefined) {
					const colorCode = `${parts[i]};${parts[i + 1]};${parts[i + 2]};${parts[i + 3]};${parts[i + 4]}`;
					if (code === 38) this.fgColor = colorCode;
					else this.bgColor = colorCode;
					i += 5;
					continue;
				}
			}
			switch (code) {
				case 0: this.#reset(); break;
				case 1: this.bold = true; break;
				case 2: this.dim = true; break;
				case 3: this.italic = true; break;
				case 4: this.underline = true; break;
				case 5: this.blink = true; break;
				case 7: this.inverse = true; break;
				case 8: this.hidden = true; break;
				case 9: this.strikethrough = true; break;
				case 21: this.bold = false; break;
				case 22: this.bold = false; this.dim = false; break;
				case 23: this.italic = false; break;
				case 24: this.underline = false; break;
				case 25: this.blink = false; break;
				case 27: this.inverse = false; break;
				case 28: this.hidden = false; break;
				case 29: this.strikethrough = false; break;
				case 39: this.fgColor = null; break;
				case 49: this.bgColor = null; break;
				default:
					if ((code >= 30 && code <= 37) || (code >= 90 && code <= 97)) this.fgColor = String(code);
					else if ((code >= 40 && code <= 47) || (code >= 100 && code <= 107)) this.bgColor = String(code);
					break;
			}
			i++;
		}
	}

	#reset() {
		this.bold = false;
		this.dim = false;
		this.italic = false;
		this.underline = false;
		this.blink = false;
		this.inverse = false;
		this.hidden = false;
		this.strikethrough = false;
		this.fgColor = null;
		this.bgColor = null;
		// SGR reset does not affect OSC 8 hyperlink state
	}

	clear() {
		this.#reset();
		this.activeHyperlink = null;
	}

	getActiveCodes() {
		const codes = [];
		if (this.bold) codes.push("1");
		if (this.dim) codes.push("2");
		if (this.italic) codes.push("3");
		if (this.underline) codes.push("4");
		if (this.blink) codes.push("5");
		if (this.inverse) codes.push("7");
		if (this.hidden) codes.push("8");
		if (this.strikethrough) codes.push("9");
		if (this.fgColor) codes.push(this.fgColor);
		if (this.bgColor) codes.push(this.bgColor);
		let result = codes.length > 0 ? `\x1b[${codes.join(";")}m` : "";
		if (this.activeHyperlink) {
			result += `\x1b]8;${this.activeHyperlink.params};${this.activeHyperlink.url}${this.activeHyperlink.terminator}`;
		}
		return result;
	}

	getLineEndReset() {
		let result = "";
		if (this.underline) result += "\x1b[24m";
		if (this.activeHyperlink) result += `\x1b]8;;${this.activeHyperlink.terminator}`;
		return result;
	}
}

function updateTrackerFromText(text, tracker) {
	let i = 0;
	while (i < text.length) {
		const ansiResult = extractAnsiCode(text, i);
		if (ansiResult) {
			tracker.process(ansiResult.code);
			i += ansiResult.length;
		} else {
			i++;
		}
	}
}

// ── Wrapping ─────────────────────────────────────────────────────────────────

function splitIntoTokensWithAnsi(text) {
	const tokens = [];
	let current = "";
	let pendingAnsi = "";
	let currentKind = null;
	let i = 0;
	const flushCurrent = () => {
		if (!current) return;
		tokens.push(current);
		current = "";
		currentKind = null;
	};
	while (i < text.length) {
		const ansiResult = extractAnsiCode(text, i);
		if (ansiResult) {
			pendingAnsi += ansiResult.code;
			i += ansiResult.length;
			continue;
		}
		let end = i;
		while (end < text.length && !extractAnsiCode(text, end)) end++;
		for (const { segment } of graphemeSegmenter.segment(text.slice(i, end))) {
			const segmentIsSpace = segment === " ";
			if (!segmentIsSpace && cjkBreakRegex.test(segment)) {
				flushCurrent();
				const token = pendingAnsi + segment;
				pendingAnsi = "";
				tokens.push(token);
				continue;
			}
			const segmentKind = segmentIsSpace ? "space" : "word";
			if (current && currentKind !== segmentKind) flushCurrent();
			if (pendingAnsi) {
				current += pendingAnsi;
				pendingAnsi = "";
			}
			currentKind = segmentKind;
			current += segment;
		}
		i = end;
	}
	if (pendingAnsi) {
		if (current) current += pendingAnsi;
		else if (tokens.length > 0) tokens[tokens.length - 1] += pendingAnsi;
		else current = pendingAnsi;
	}
	if (current) tokens.push(current);
	return tokens;
}

function breakLongWord(word, width, tracker) {
	const lines = [];
	let currentLine = tracker.getActiveCodes();
	let currentWidth = 0;
	let i = 0;
	const segments = [];
	while (i < word.length) {
		const ansiResult = extractAnsiCode(word, i);
		if (ansiResult) {
			segments.push({ type: "ansi", value: ansiResult.code });
			i += ansiResult.length;
		} else {
			let end = i;
			while (end < word.length && !extractAnsiCode(word, end)) end++;
			for (const seg of graphemeSegmenter.segment(word.slice(i, end))) {
				segments.push({ type: "grapheme", value: seg.segment });
			}
			i = end;
		}
	}
	for (const seg of segments) {
		if (seg.type === "ansi") {
			currentLine += seg.value;
			tracker.process(seg.value);
			continue;
		}
		const grapheme = seg.value;
		if (!grapheme) continue;
		const segWidth = visibleWidth(grapheme);
		if (currentWidth + segWidth > width) {
			const lineEndReset = tracker.getLineEndReset();
			if (lineEndReset) currentLine += lineEndReset;
			lines.push(currentLine);
			currentLine = tracker.getActiveCodes();
			currentWidth = 0;
		}
		currentLine += grapheme;
		currentWidth += segWidth;
	}
	if (currentLine) lines.push(currentLine);
	return lines.length > 0 ? lines : [""];
}

function wrapSingleLine(line, width) {
	if (!line) return [""];
	if (visibleWidth(line) <= width) return [line];
	const wrapped = [];
	const tracker = new AnsiCodeTracker();
	const tokens = splitIntoTokensWithAnsi(line);
	let currentLine = "";
	let currentVisibleLength = 0;
	for (const token of tokens) {
		const tokenVisibleLength = visibleWidth(token);
		const isWhitespace = token.trim() === "";
		if (tokenVisibleLength > width && !isWhitespace) {
			if (currentLine) {
				const lineEndReset = tracker.getLineEndReset();
				if (lineEndReset) currentLine += lineEndReset;
				wrapped.push(currentLine);
				currentLine = "";
				currentVisibleLength = 0;
			}
			const broken = breakLongWord(token, width, tracker);
			for (let i = 0; i < broken.length - 1; i++) wrapped.push(broken[i]);
			currentLine = broken[broken.length - 1];
			currentVisibleLength = visibleWidth(currentLine);
			continue;
		}
		const totalNeeded = currentVisibleLength + tokenVisibleLength;
		if (totalNeeded > width && currentVisibleLength > 0) {
			let lineToWrap = currentLine.trimEnd();
			const lineEndReset = tracker.getLineEndReset();
			if (lineEndReset) lineToWrap += lineEndReset;
			wrapped.push(lineToWrap);
			if (isWhitespace) {
				currentLine = tracker.getActiveCodes();
				currentVisibleLength = 0;
			} else {
				currentLine = tracker.getActiveCodes() + token;
				currentVisibleLength = tokenVisibleLength;
			}
		} else {
			currentLine += token;
			currentVisibleLength += tokenVisibleLength;
		}
		updateTrackerFromText(token, tracker);
	}
	if (currentLine) wrapped.push(currentLine);
	return wrapped.length > 0 ? wrapped.map((entry) => entry.trimEnd()) : [""];
}

/** Word-wrap preserving ANSI state across breaks; "" → [""]. Lines are NOT padded. */
export function wrapTextWithAnsi(text, width) {
	if (!text) return [""];
	const inputLines = text.split(/\r\n|\r|\n/);
	const result = [];
	const tracker = new AnsiCodeTracker();
	for (const inputLine of inputLines) {
		const prefix = result.length > 0 ? tracker.getActiveCodes() : "";
		for (const wrappedLine of wrapSingleLine(prefix + inputLine, width)) result.push(wrappedLine);
		updateTrackerFromText(inputLine, tracker);
	}
	return result.length > 0 ? result : [""];
}

/** Apply a background function to a line, padding to full width first. */
export function applyBackgroundToLine(line, width, bgFn) {
	const visibleLen = visibleWidth(line);
	const padding = " ".repeat(Math.max(0, width - visibleLen));
	return bgFn(line + padding);
}

// ── Truncation ───────────────────────────────────────────────────────────────

function getActiveOsc8Close(prefix) {
	if (!prefix.includes("\x1b]8;")) return "";
	let close = "";
	let i = 0;
	while (i < prefix.length) {
		const ansi = extractAnsiCode(prefix, i);
		if (ansi) {
			const hyperlink = parseOsc8Hyperlink(ansi.code);
			if (hyperlink !== undefined) close = hyperlink ? `\x1b]8;;${hyperlink.terminator}` : "";
			i += ansi.length;
			continue;
		}
		i++;
	}
	return close;
}

function truncateFragmentToWidth(text, maxWidth) {
	if (maxWidth <= 0 || text.length === 0) return { text: "", width: 0 };
	if (isPrintableAscii(text)) {
		const clipped = text.slice(0, maxWidth);
		return { text: clipped, width: clipped.length };
	}
	let result = "";
	let width = 0;
	for (const { segment } of graphemeSegmenter.segment(text)) {
		const w = graphemeWidth(segment);
		if (width + w > maxWidth) break;
		result += segment;
		width += w;
	}
	return { text: result, width };
}

function finalizeTruncatedResult(prefix, prefixWidth, ellipsis, ellipsisWidth, maxWidth, pad) {
	const reset = "\x1b[0m";
	const hyperlinkClose = getActiveOsc8Close(prefix);
	const totalWidth = prefixWidth + ellipsisWidth;
	let result;
	if (ellipsis.length > 0) result = `${prefix}${hyperlinkClose}${reset}${ellipsis}${reset}`;
	else result = `${prefix}${hyperlinkClose}${reset}`;
	return pad ? result + " ".repeat(Math.max(0, maxWidth - totalWidth)) : result;
}

/** Truncate to a visible width, appending an ellipsis; optionally pad to exactly maxWidth. */
export function truncateToWidth(text, maxWidth, ellipsis = "...", pad = false) {
	if (maxWidth <= 0) return "";
	if (text.length === 0) return pad ? " ".repeat(maxWidth) : "";
	const ellipsisWidth = visibleWidth(ellipsis);
	if (ellipsisWidth >= maxWidth) {
		const textWidth = visibleWidth(text);
		if (textWidth <= maxWidth) return pad ? text + " ".repeat(maxWidth - textWidth) : text;
		const clippedEllipsis = truncateFragmentToWidth(ellipsis, maxWidth);
		if (clippedEllipsis.width === 0) return pad ? " ".repeat(maxWidth) : "";
		return finalizeTruncatedResult("", 0, clippedEllipsis.text, clippedEllipsis.width, maxWidth, pad);
	}
	if (isPrintableAscii(text)) {
		if (text.length <= maxWidth) return pad ? text + " ".repeat(maxWidth - text.length) : text;
		const targetWidth = maxWidth - ellipsisWidth;
		return finalizeTruncatedResult(text.slice(0, targetWidth), targetWidth, ellipsis, ellipsisWidth, maxWidth, pad);
	}
	const targetWidth = maxWidth - ellipsisWidth;
	let result = "";
	let pendingAnsi = "";
	let visibleSoFar = 0;
	let keptWidth = 0;
	let keepContiguousPrefix = true;
	let overflowed = false;
	let exhaustedInput = false;
	const hasAnsi = text.includes("\x1b");
	const hasTabs = text.includes("\t");
	if (!hasAnsi && !hasTabs) {
		for (const { segment } of graphemeSegmenter.segment(text)) {
			const width = graphemeWidth(segment);
			if (keepContiguousPrefix && keptWidth + width <= targetWidth) {
				result += segment;
				keptWidth += width;
			} else {
				keepContiguousPrefix = false;
			}
			visibleSoFar += width;
			if (visibleSoFar > maxWidth) {
				overflowed = true;
				break;
			}
		}
		exhaustedInput = !overflowed;
	} else {
		let i = 0;
		while (i < text.length) {
			const ansi = extractAnsiCode(text, i);
			if (ansi) {
				pendingAnsi += ansi.code;
				i += ansi.length;
				continue;
			}
			if (text[i] === "\t") {
				if (keepContiguousPrefix && keptWidth + 3 <= targetWidth) {
					if (pendingAnsi) {
						result += pendingAnsi;
						pendingAnsi = "";
					}
					result += "\t";
					keptWidth += 3;
				} else {
					keepContiguousPrefix = false;
					pendingAnsi = "";
				}
				visibleSoFar += 3;
				if (visibleSoFar > maxWidth) {
					overflowed = true;
					break;
				}
				i++;
				continue;
			}
			let end = i;
			while (end < text.length && text[end] !== "\t") {
				if (extractAnsiCode(text, end)) break;
				end++;
			}
			for (const { segment } of graphemeSegmenter.segment(text.slice(i, end))) {
				const width = graphemeWidth(segment);
				if (keepContiguousPrefix && keptWidth + width <= targetWidth) {
					if (pendingAnsi) {
						result += pendingAnsi;
						pendingAnsi = "";
					}
					result += segment;
					keptWidth += width;
				} else {
					keepContiguousPrefix = false;
					pendingAnsi = "";
				}
				visibleSoFar += width;
				if (visibleSoFar > maxWidth) {
					overflowed = true;
					break;
				}
			}
			if (overflowed) break;
			i = end;
		}
		exhaustedInput = i >= text.length;
	}
	if (!overflowed && exhaustedInput) {
		return pad ? text + " ".repeat(Math.max(0, maxWidth - visibleSoFar)) : text;
	}
	return finalizeTruncatedResult(result, keptWidth, ellipsis, ellipsisWidth, maxWidth, pad);
}
