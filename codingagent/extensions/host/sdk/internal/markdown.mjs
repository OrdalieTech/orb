// orb-extension-sdk: Markdown → styled terminal lines.
// Follows the contract of pi-tui's Markdown component (pi 0.84.1, commit
// 53fa77cc, packages/tui/src/components/markdown.ts, MIT © Mario Zechner):
// the 14 MarkdownTheme style functions plus optional highlightCode and
// codeBlockIndent. The upstream `marked`-based lexer is replaced by a compact
// line-oriented renderer — presentation is Orb-owned (D35), so block coverage
// (headings, paragraphs, fenced code, quotes, lists, hr, inline styles, links)
// targets what extensions display rather than CommonMark completeness.

function escapeRestore(text) {
	return text.replace(/\\([\\`*_{}[\]()#+\-.!~])/g, "$1");
}

/** Apply inline markdown styles using the theme's style functions. */
export function renderInline(text, theme) {
	let out = "";
	let i = 0;
	const src = text;
	while (i < src.length) {
		const ch = src[i];
		if (ch === "\\" && i + 1 < src.length) {
			out += src[i + 1];
			i += 2;
			continue;
		}
		if (ch === "`") {
			let fence = 1;
			while (src[i + fence] === "`") fence++;
			const close = src.indexOf("`".repeat(fence), i + fence);
			if (close !== -1) {
				out += theme.code(src.slice(i + fence, close));
				i = close + fence;
				continue;
			}
		}
		if (src.startsWith("~~", i)) {
			const close = src.indexOf("~~", i + 2);
			if (close !== -1 && close > i + 2) {
				out += theme.strikethrough(renderInline(src.slice(i + 2, close), theme));
				i = close + 2;
				continue;
			}
		}
		if (src.startsWith("**", i) || src.startsWith("__", i)) {
			const marker = src.slice(i, i + 2);
			const close = src.indexOf(marker, i + 2);
			if (close !== -1 && close > i + 2) {
				out += theme.bold(renderInline(src.slice(i + 2, close), theme));
				i = close + 2;
				continue;
			}
		}
		if ((ch === "*" || ch === "_") && src[i + 1] !== ch && src[i + 1] !== undefined && src[i + 1] !== " ") {
			const close = src.indexOf(ch, i + 1);
			if (close !== -1) {
				out += theme.italic(renderInline(src.slice(i + 1, close), theme));
				i = close + 1;
				continue;
			}
		}
		if (ch === "[") {
			const closeBracket = src.indexOf("]", i + 1);
			if (closeBracket !== -1 && src[closeBracket + 1] === "(") {
				const closeParen = src.indexOf(")", closeBracket + 2);
				if (closeParen !== -1) {
					const label = src.slice(i + 1, closeBracket);
					const url = src.slice(closeBracket + 2, closeParen);
					out += theme.link(renderInline(label, theme));
					if (url && url !== label) out += theme.linkUrl(` (${url})`);
					i = closeParen + 1;
					continue;
				}
			}
		}
		out += ch;
		i++;
	}
	return out;
}

/**
 * Render markdown source to styled (unwrapped, unpadded) lines.
 * @param {string} text - markdown source (tabs already normalized by caller)
 * @param {object} theme - MarkdownTheme style functions
 */
export function renderMarkdownLines(text, theme) {
	const lines = text.split("\n");
	const out = [];
	const codeIndent = theme.codeBlockIndent ?? "  ";
	let paragraph = [];
	const flushParagraph = () => {
		if (paragraph.length === 0) return;
		out.push(renderInline(escapeRestore(paragraph.join(" ")), theme));
		out.push("");
		paragraph = [];
	};
	let i = 0;
	while (i < lines.length) {
		const line = lines[i];
		const trimmed = line.trim();

		const fenceMatch = trimmed.match(/^(```+|~~~+)\s*(\S+)?\s*$/);
		if (fenceMatch) {
			flushParagraph();
			const fence = fenceMatch[1][0].repeat(3);
			const lang = fenceMatch[2];
			const code = [];
			i++;
			while (i < lines.length && !lines[i].trim().startsWith(fence)) {
				code.push(lines[i]);
				i++;
			}
			i++; // closing fence (or end of input)
			const source = code.join("\n");
			const highlighted =
				theme.highlightCode?.(source, lang) ?? source.split("\n").map((entry) => theme.codeBlock(entry));
			for (const codeLine of highlighted) out.push(codeIndent + codeLine);
			out.push("");
			continue;
		}

		if (trimmed === "") {
			flushParagraph();
			i++;
			continue;
		}

		const headingMatch = trimmed.match(/^(#{1,6})\s+(.*)$/);
		if (headingMatch) {
			flushParagraph();
			out.push(theme.heading(`${headingMatch[1]} ${renderInline(escapeRestore(headingMatch[2]), theme)}`));
			out.push("");
			i++;
			continue;
		}

		if (/^(?:-{3,}|\*{3,}|_{3,})$/.test(trimmed)) {
			flushParagraph();
			out.push(theme.hr("─".repeat(40)));
			out.push("");
			i++;
			continue;
		}

		if (trimmed.startsWith(">")) {
			flushParagraph();
			while (i < lines.length && lines[i].trim().startsWith(">")) {
				const content = lines[i].trim().replace(/^>\s?/, "");
				out.push(theme.quoteBorder("│ ") + theme.quote(renderInline(escapeRestore(content), theme)));
				i++;
			}
			out.push("");
			continue;
		}

		const listMatch = line.match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
		if (listMatch) {
			flushParagraph();
			while (i < lines.length) {
				const itemMatch = lines[i].match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
				if (!itemMatch) {
					// Lazy continuation of the previous item.
					if (lines[i].trim() !== "" && /^\s+/.test(lines[i])) {
						out[out.length - 1] += ` ${renderInline(escapeRestore(lines[i].trim()), theme)}`;
						i++;
						continue;
					}
					break;
				}
				const indent = " ".repeat(itemMatch[1].length);
				const bullet = /^\d/.test(itemMatch[2]) ? `${itemMatch[2]} ` : "- ";
				out.push(indent + theme.listBullet(bullet) + renderInline(escapeRestore(itemMatch[3]), theme));
				i++;
			}
			out.push("");
			continue;
		}

		paragraph.push(trimmed);
		i++;
	}
	flushParagraph();
	while (out.length > 0 && out[out.length - 1] === "") out.pop();
	return out;
}
