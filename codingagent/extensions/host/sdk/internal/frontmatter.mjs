// orb-extension-sdk: frontmatter extraction.
// Ported from pi-coding-agent (pi 0.84.1, commit 53fa77cc,
// packages/coding-agent/src/utils/frontmatter.ts, MIT © Mario Zechner). The
// upstream `yaml` dependency is replaced by a minimal parser covering the flat
// scalar/array/one-level-map shapes agent and command frontmatter actually
// uses; anything richer throws, and callers (which wrap in try/catch upstream
// too) fall back.

const normalizeNewlines = (value) => value.replace(/\r\n/g, "\n").replace(/\r/g, "\n");

function extractRaw(content) {
	const normalized = normalizeNewlines(content);
	if (!normalized.startsWith("---")) return { yamlString: null, body: normalized };
	const endIndex = normalized.indexOf("\n---", 3);
	if (endIndex === -1) return { yamlString: null, body: normalized };
	return {
		yamlString: normalized.slice(4, endIndex),
		body: normalized.slice(endIndex + 4).trim(),
	};
}

function parseScalar(raw) {
	const value = raw.trim();
	if (value === "" || value === "~" || value === "null" || value === "Null" || value === "NULL") return null;
	if ((value.startsWith('"') && value.endsWith('"') && value.length >= 2)) {
		return value.slice(1, -1).replace(/\\(["\\nrt])/g, (_all, ch) =>
			ch === "n" ? "\n" : ch === "r" ? "\r" : ch === "t" ? "\t" : ch,
		);
	}
	if (value.startsWith("'") && value.endsWith("'") && value.length >= 2) {
		return value.slice(1, -1).replace(/''/g, "'");
	}
	if (value === "true" || value === "True" || value === "TRUE") return true;
	if (value === "false" || value === "False" || value === "FALSE") return false;
	if (/^[+-]?\d+$/.test(value)) return Number.parseInt(value, 10);
	if (/^[+-]?(?:\d+\.\d*|\.\d+|\d+)(?:[eE][+-]?\d+)?$/.test(value)) return Number.parseFloat(value);
	return value;
}

function splitInlineList(inner) {
	const items = [];
	let current = "";
	let quote = null;
	for (let i = 0; i < inner.length; i++) {
		const ch = inner[i];
		if (quote) {
			current += ch;
			if (ch === quote && (quote !== "'" || inner[i + 1] !== "'")) quote = null;
			else if (ch === quote) {
				current += inner[++i];
			}
		} else if (ch === '"' || ch === "'") {
			quote = ch;
			current += ch;
		} else if (ch === ",") {
			items.push(current);
			current = "";
		} else {
			current += ch;
		}
	}
	if (current.trim() !== "" || items.length > 0) items.push(current);
	return items.map((item) => parseScalar(item));
}

function parseValue(raw) {
	const value = raw.trim();
	if (value.startsWith("[")) {
		if (!value.endsWith("]")) throw new Error(`unterminated inline list: ${value}`);
		const inner = value.slice(1, -1).trim();
		if (inner === "") return [];
		return splitInlineList(inner);
	}
	if (value.startsWith("{")) throw new Error(`inline maps are not supported: ${value}`);
	return parseScalar(value);
}

function stripComment(line) {
	let quote = null;
	for (let i = 0; i < line.length; i++) {
		const ch = line[i];
		if (quote) {
			if (ch === quote) quote = null;
		} else if (ch === '"' || ch === "'") {
			quote = ch;
		} else if (ch === "#" && (i === 0 || line[i - 1] === " " || line[i - 1] === "\t")) {
			return line.slice(0, i);
		}
	}
	return line;
}

const KEY_LINE = /^([A-Za-z0-9_][\w.\- ]*?)\s*:(.*)$/;

/** Minimal YAML mapping parser: flat scalars, inline/block lists, one nested map level. */
export function parseYamlLite(source) {
	const root = {};
	const lines = source.split("\n");
	let index = 0;
	const parseMap = (target, indent) => {
		while (index < lines.length) {
			const rawLine = lines[index];
			if (rawLine.trim() === "" || stripComment(rawLine).trim() === "") {
				index++;
				continue;
			}
			const line = stripComment(rawLine);
			const currentIndent = line.length - line.trimStart().length;
			if (currentIndent < indent) return;
			if (currentIndent > indent) throw new Error(`unexpected indentation: ${rawLine}`);
			const trimmed = line.trim();
			const keyMatch = trimmed.match(KEY_LINE);
			if (!keyMatch) throw new Error(`unsupported YAML line: ${rawLine}`);
			const key = keyMatch[1].trim();
			const rest = keyMatch[2].trim();
			index++;
			if (rest !== "") {
				target[key] = parseValue(rest);
				continue;
			}
			// Empty value: block list, nested map, or null.
			let lookahead = index;
			while (lookahead < lines.length && stripComment(lines[lookahead]).trim() === "") lookahead++;
			if (lookahead >= lines.length) {
				target[key] = null;
				continue;
			}
			const nextLine = stripComment(lines[lookahead]);
			const nextIndent = nextLine.length - nextLine.trimStart().length;
			if (nextIndent <= indent) {
				target[key] = null;
				continue;
			}
			if (nextLine.trim().startsWith("- ") || nextLine.trim() === "-") {
				const list = [];
				index = lookahead;
				while (index < lines.length) {
					const itemLine = stripComment(lines[index]);
					if (itemLine.trim() === "") {
						index++;
						continue;
					}
					const itemIndent = itemLine.length - itemLine.trimStart().length;
					if (itemIndent <= indent) break;
					const item = itemLine.trim();
					if (!item.startsWith("-")) break;
					list.push(item === "-" ? null : parseValue(item.slice(1).trim()));
					index++;
				}
				target[key] = list;
				continue;
			}
			const nested = {};
			index = lookahead;
			parseMap(nested, nextIndent);
			target[key] = nested;
		}
	};
	parseMap(root, 0);
	return root;
}

/** Parse `---` frontmatter; body is trimmed, missing/invalid frontmatter yields {}. */
export function parseFrontmatter(content) {
	const { yamlString, body } = extractRaw(content);
	if (!yamlString) return { frontmatter: {}, body };
	const parsed = parseYamlLite(yamlString);
	return { frontmatter: parsed ?? {}, body };
}

export function stripFrontmatterBody(content) {
	return parseFrontmatter(content).body;
}
