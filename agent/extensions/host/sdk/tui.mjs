// orb-extension-sdk: @earendil-works/pi-tui surface.
// Implemented symbols are ported from pi-tui (pi 0.84.1, commit 53fa77cc,
// MIT © Mario Zechner), trimmed to the D15 Component contract published
// extensions exercise: render(width) → string[] flowing through the host's
// ui_component_render push bridge — no per-frame RPC, no terminal ownership.
// Every other upstream export throws OrbUnsupportedCapability on use.
import manifest from "./sdk.json" with { type: "json" };
import { renderMarkdownLines } from "./internal/markdown.mjs";
import { parseKey } from "./internal/keys.mjs";
import {
	applyBackgroundToLine,
	truncateToWidth,
	visibleWidth,
	wrapTextWithAnsi,
} from "./internal/text.mjs";
import { unsupported } from "./internal/unsupported.mjs";

const SUPPORTED = manifest.modules.tui.implemented;
const stub = (name) => unsupported("tui", name, SUPPORTED);

export { parseKey, truncateToWidth, visibleWidth, wrapTextWithAnsi };

/** Container - a component that renders its children in order. */
export class Container {
	children = [];

	addChild(component) {
		this.children.push(component);
	}

	removeChild(component) {
		const index = this.children.indexOf(component);
		if (index !== -1) this.children.splice(index, 1);
	}

	clear() {
		this.children = [];
	}

	invalidate() {
		for (const child of this.children) child.invalidate?.();
	}

	render(width) {
		const lines = [];
		for (const child of this.children) {
			for (const line of child.render(width)) lines.push(line);
		}
		return lines;
	}
}

/** Spacer - renders empty lines. */
export class Spacer {
	#lines;

	constructor(lines = 1) {
		this.#lines = lines;
	}

	setLines(lines) {
		this.#lines = lines;
	}

	invalidate() {}

	render(_width) {
		const result = [];
		for (let i = 0; i < this.#lines; i++) result.push("");
		return result;
	}
}

/** Text - multi-line text with word wrapping, margins, and optional background. */
export class Text {
	#text;
	#paddingX;
	#paddingY;
	#customBgFn;
	#cachedText;
	#cachedWidth;
	#cachedLines;

	constructor(text = "", paddingX = 1, paddingY = 1, customBgFn) {
		this.#text = text;
		this.#paddingX = paddingX;
		this.#paddingY = paddingY;
		this.#customBgFn = customBgFn;
	}

	setText(text) {
		this.#text = text;
		this.invalidate();
	}

	setCustomBgFn(customBgFn) {
		this.#customBgFn = customBgFn;
		this.invalidate();
	}

	invalidate() {
		this.#cachedText = undefined;
		this.#cachedWidth = undefined;
		this.#cachedLines = undefined;
	}

	render(width) {
		if (this.#cachedLines && this.#cachedText === this.#text && this.#cachedWidth === width) {
			return this.#cachedLines;
		}
		if (!this.#text || this.#text.trim() === "") {
			this.#cachedText = this.#text;
			this.#cachedWidth = width;
			this.#cachedLines = [];
			return this.#cachedLines;
		}
		const normalizedText = this.#text.replace(/\t/g, "   ");
		const contentWidth = Math.max(1, width - this.#paddingX * 2);
		const wrappedLines = wrapTextWithAnsi(normalizedText, contentWidth);
		const margin = " ".repeat(this.#paddingX);
		const contentLines = [];
		for (const line of wrappedLines) {
			const lineWithMargins = margin + line + margin;
			if (this.#customBgFn) {
				contentLines.push(applyBackgroundToLine(lineWithMargins, width, this.#customBgFn));
			} else {
				const paddingNeeded = Math.max(0, width - visibleWidth(lineWithMargins));
				contentLines.push(lineWithMargins + " ".repeat(paddingNeeded));
			}
		}
		const emptyLine = " ".repeat(width);
		const emptyLines = [];
		for (let i = 0; i < this.#paddingY; i++) {
			emptyLines.push(this.#customBgFn ? applyBackgroundToLine(emptyLine, width, this.#customBgFn) : emptyLine);
		}
		const result = [...emptyLines, ...contentLines, ...emptyLines];
		this.#cachedText = this.#text;
		this.#cachedWidth = width;
		this.#cachedLines = result.length > 0 ? result : [""];
		return this.#cachedLines;
	}
}

/** Markdown - renders markdown text through the MarkdownTheme style functions. */
export class Markdown {
	#text;
	#paddingX;
	#paddingY;
	#theme;
	#defaultTextStyle;
	#options;
	#cachedText;
	#cachedWidth;
	#cachedLines;

	constructor(text, paddingX, paddingY, theme, defaultTextStyle, options) {
		this.#text = text;
		this.#paddingX = paddingX;
		this.#paddingY = paddingY;
		this.#theme = theme;
		this.#defaultTextStyle = defaultTextStyle;
		this.#options = options ? { ...options } : {};
	}

	setText(text) {
		this.#text = text;
		this.invalidate();
	}

	invalidate() {
		this.#cachedText = undefined;
		this.#cachedWidth = undefined;
		this.#cachedLines = undefined;
	}

	render(width) {
		if (this.#cachedLines && this.#cachedText === this.#text && this.#cachedWidth === width) {
			return this.#cachedLines;
		}
		const contentWidth = Math.max(1, width - this.#paddingX * 2);
		const text = this.#options.transform?.(this.#text, contentWidth) ?? this.#text;
		if (!text || text.trim() === "") {
			this.#cachedText = this.#text;
			this.#cachedWidth = width;
			this.#cachedLines = [];
			return this.#cachedLines;
		}
		const renderedLines = renderMarkdownLines(text.replace(/\t/g, "   "), this.#theme);
		const wrappedLines = [];
		for (const line of renderedLines) {
			for (const wrappedLine of wrapTextWithAnsi(line, contentWidth)) wrappedLines.push(wrappedLine);
		}
		const margin = " ".repeat(this.#paddingX);
		const bgFn = this.#defaultTextStyle?.bgColor;
		const contentLines = [];
		for (const line of wrappedLines) {
			const lineWithMargins = margin + line + margin;
			if (bgFn) {
				contentLines.push(applyBackgroundToLine(lineWithMargins, width, bgFn));
			} else {
				const paddingNeeded = Math.max(0, width - visibleWidth(lineWithMargins));
				contentLines.push(lineWithMargins + " ".repeat(paddingNeeded));
			}
		}
		const emptyLine = " ".repeat(width);
		const emptyLines = [];
		for (let i = 0; i < this.#paddingY; i++) {
			emptyLines.push(bgFn ? applyBackgroundToLine(emptyLine, width, bgFn) : emptyLine);
		}
		const result = emptyLines.concat(contentLines, emptyLines);
		this.#cachedText = this.#text;
		this.#cachedWidth = width;
		this.#cachedLines = result.length > 0 ? result : [""];
		return this.#cachedLines;
	}
}

const DEFAULT_PRIMARY_COLUMN_WIDTH = 32;
const PRIMARY_COLUMN_GAP = 2;
const MIN_DESCRIPTION_WIDTH = 10;

const normalizeToSingleLine = (text) => text.replace(/[\r\n]+/g, " ").trim();
const clamp = (value, min, max) => Math.max(min, Math.min(value, max));

// The host snapshot carries only app-level keybinding ids, so the tui.select.*
// actions ship their upstream defaults here (keybindings.ts TUI_KEYBINDINGS).
const SELECT_KEYBINDINGS = {
	up: ["up"],
	down: ["down"],
	confirm: ["enter"],
	cancel: ["escape", "ctrl+c"],
};

function matchesSelectAction(keyData, action) {
	const keyId = parseKey(keyData);
	return keyId !== undefined && SELECT_KEYBINDINGS[action].includes(keyId);
}

/** SelectList - scrolling selection list with filter and two-column layout. */
export class SelectList {
	#items;
	#filteredItems;
	#selectedIndex = 0;
	#maxVisible;
	#theme;
	#layout;

	onSelect;
	onCancel;
	onSelectionChange;

	constructor(items, maxVisible, theme, layout = {}) {
		this.#items = items;
		this.#filteredItems = items;
		this.#maxVisible = maxVisible;
		this.#theme = theme;
		this.#layout = layout;
	}

	setFilter(filter) {
		this.#filteredItems = this.#items.filter((item) =>
			item.value.toLowerCase().startsWith(filter.toLowerCase()),
		);
		this.#selectedIndex = 0;
	}

	setSelectedIndex(index) {
		this.#selectedIndex = Math.max(0, Math.min(index, this.#filteredItems.length - 1));
	}

	invalidate() {}

	render(width) {
		const lines = [];
		if (this.#filteredItems.length === 0) {
			lines.push(this.#theme.noMatch("  No matching commands"));
			return lines;
		}
		const primaryColumnWidth = this.#getPrimaryColumnWidth();
		const startIndex = Math.max(
			0,
			Math.min(
				this.#selectedIndex - Math.floor(this.#maxVisible / 2),
				this.#filteredItems.length - this.#maxVisible,
			),
		);
		const endIndex = Math.min(startIndex + this.#maxVisible, this.#filteredItems.length);
		for (let i = startIndex; i < endIndex; i++) {
			const item = this.#filteredItems[i];
			if (!item) continue;
			const isSelected = i === this.#selectedIndex;
			const descriptionSingleLine = item.description ? normalizeToSingleLine(item.description) : undefined;
			lines.push(this.#renderItem(item, isSelected, width, descriptionSingleLine, primaryColumnWidth));
		}
		if (startIndex > 0 || endIndex < this.#filteredItems.length) {
			const scrollText = `  (${this.#selectedIndex + 1}/${this.#filteredItems.length})`;
			lines.push(this.#theme.scrollInfo(truncateToWidth(scrollText, width - 2, "")));
		}
		return lines;
	}

	handleInput(keyData) {
		if (matchesSelectAction(keyData, "up")) {
			this.#selectedIndex =
				this.#selectedIndex === 0 ? this.#filteredItems.length - 1 : this.#selectedIndex - 1;
			this.#notifySelectionChange();
		} else if (matchesSelectAction(keyData, "down")) {
			this.#selectedIndex =
				this.#selectedIndex === this.#filteredItems.length - 1 ? 0 : this.#selectedIndex + 1;
			this.#notifySelectionChange();
		} else if (matchesSelectAction(keyData, "confirm")) {
			const selectedItem = this.#filteredItems[this.#selectedIndex];
			if (selectedItem && this.onSelect) this.onSelect(selectedItem);
		} else if (matchesSelectAction(keyData, "cancel")) {
			if (this.onCancel) this.onCancel();
		}
	}

	#renderItem(item, isSelected, width, descriptionSingleLine, primaryColumnWidth) {
		const prefix = isSelected ? "→ " : "  ";
		const prefixWidth = visibleWidth(prefix);
		if (descriptionSingleLine && width > 40) {
			const effectivePrimaryColumnWidth = Math.max(1, Math.min(primaryColumnWidth, width - prefixWidth - 4));
			const maxPrimaryWidth = Math.max(1, effectivePrimaryColumnWidth - PRIMARY_COLUMN_GAP);
			const truncatedValue = this.#truncatePrimary(item, isSelected, maxPrimaryWidth, effectivePrimaryColumnWidth);
			const truncatedValueWidth = visibleWidth(truncatedValue);
			const spacing = " ".repeat(Math.max(1, effectivePrimaryColumnWidth - truncatedValueWidth));
			const descriptionStart = prefixWidth + truncatedValueWidth + spacing.length;
			const remainingWidth = width - descriptionStart - 2;
			if (remainingWidth > MIN_DESCRIPTION_WIDTH) {
				const truncatedDesc = truncateToWidth(descriptionSingleLine, remainingWidth, "");
				if (isSelected) {
					return this.#theme.selectedText(`${prefix}${truncatedValue}${spacing}${truncatedDesc}`);
				}
				const descText = this.#theme.description(spacing + truncatedDesc);
				return prefix + truncatedValue + descText;
			}
		}
		const maxWidth = width - prefixWidth - 2;
		const truncatedValue = this.#truncatePrimary(item, isSelected, maxWidth, maxWidth);
		if (isSelected) return this.#theme.selectedText(`${prefix}${truncatedValue}`);
		return prefix + truncatedValue;
	}

	#getPrimaryColumnWidth() {
		const { min, max } = this.#getPrimaryColumnBounds();
		const widestPrimary = this.#filteredItems.reduce(
			(widest, item) => Math.max(widest, visibleWidth(this.#getDisplayValue(item)) + PRIMARY_COLUMN_GAP),
			0,
		);
		return clamp(widestPrimary, min, max);
	}

	#getPrimaryColumnBounds() {
		const rawMin =
			this.#layout.minPrimaryColumnWidth ?? this.#layout.maxPrimaryColumnWidth ?? DEFAULT_PRIMARY_COLUMN_WIDTH;
		const rawMax =
			this.#layout.maxPrimaryColumnWidth ?? this.#layout.minPrimaryColumnWidth ?? DEFAULT_PRIMARY_COLUMN_WIDTH;
		return { min: Math.max(1, Math.min(rawMin, rawMax)), max: Math.max(1, Math.max(rawMin, rawMax)) };
	}

	#truncatePrimary(item, isSelected, maxWidth, columnWidth) {
		const displayValue = this.#getDisplayValue(item);
		const truncatedValue = this.#layout.truncatePrimary
			? this.#layout.truncatePrimary({ text: displayValue, maxWidth, columnWidth, item, isSelected })
			: truncateToWidth(displayValue, maxWidth, "");
		return truncateToWidth(truncatedValue, maxWidth, "");
	}

	#getDisplayValue(item) {
		return item.label || item.value;
	}

	#notifySelectionChange() {
		const selectedItem = this.#filteredItems[this.#selectedIndex];
		if (selectedItem && this.onSelectionChange) this.onSelectionChange(selectedItem);
	}

	getSelectedItem() {
		return this.#filteredItems[this.#selectedIndex] || null;
	}
}

// ── Unsupported upstream exports (generated from pinned pi-tui 0.84.1) ───────
export const Box = stub("Box");
export const CURSOR_MARKER = stub("CURSOR_MARKER");
export const CancellableLoader = stub("CancellableLoader");
export const CombinedAutocompleteProvider = stub("CombinedAutocompleteProvider");
export const Editor = stub("Editor");
export const HStack = stub("HStack");
export const Image = stub("Image");
export const Input = stub("Input");
export const Key = stub("Key");
export const KeybindingsManager = stub("KeybindingsManager");
export const Loader = stub("Loader");
export const Marked = stub("Marked");
export const ProcessTerminal = stub("ProcessTerminal");
export const ScrollView = stub("ScrollView");
export const SettingsList = stub("SettingsList");
export const StdinBuffer = stub("StdinBuffer");
export const TUI_KEYBINDINGS = stub("TUI_KEYBINDINGS");
export const TruncatedText = stub("TruncatedText");
export const TuiAltScreen = stub("TuiAltScreen");
export const TuiMainScreen = stub("TuiMainScreen");
export const VStack = stub("VStack");
export const allocateImageId = stub("allocateImageId");
export const calculateImageRows = stub("calculateImageRows");
export const compositeTuiLine = stub("compositeTuiLine");
export const decodeKittyPrintable = stub("decodeKittyPrintable");
export const deleteAllKittyImages = stub("deleteAllKittyImages");
export const deleteKittyImage = stub("deleteKittyImage");
export const detectCapabilities = stub("detectCapabilities");
export const encodeITerm2 = stub("encodeITerm2");
export const encodeKitty = stub("encodeKitty");
export const fuzzyFilter = stub("fuzzyFilter");
export const fuzzyMatch = stub("fuzzyMatch");
export const getCapabilities = stub("getCapabilities");
export const getCellDimensions = stub("getCellDimensions");
export const getGifDimensions = stub("getGifDimensions");
export const getImageDimensions = stub("getImageDimensions");
export const getJpegDimensions = stub("getJpegDimensions");
export const getKeybindings = stub("getKeybindings");
export const getOsc8LinkAtColumn = stub("getOsc8LinkAtColumn");
export const getPngDimensions = stub("getPngDimensions");
export const getWebpDimensions = stub("getWebpDimensions");
export const hyperlink = stub("hyperlink");
export const imageFallback = stub("imageFallback");
export const isFocusable = stub("isFocusable");
export const isKeyRelease = stub("isKeyRelease");
export const isKeyRepeat = stub("isKeyRepeat");
export const isKittyProtocolActive = stub("isKittyProtocolActive");
export const isViewportTUI = stub("isViewportTUI");
export const matchesKey = stub("matchesKey");
export const parseOsc11BackgroundColor = stub("parseOsc11BackgroundColor");
export const parseTerminalColorSchemeReport = stub("parseTerminalColorSchemeReport");
export const renderImage = stub("renderImage");
export const renderLatex = stub("renderLatex");
export const resetCapabilitiesCache = stub("resetCapabilitiesCache");
export const setCapabilities = stub("setCapabilities");
export const setCellDimensions = stub("setCellDimensions");
export const setKeybindings = stub("setKeybindings");
export const setKittyProtocolActive = stub("setKittyProtocolActive");
export const sliceByColumn = stub("sliceByColumn");
export const stripTerminalSequences = stub("stripTerminalSequences");
