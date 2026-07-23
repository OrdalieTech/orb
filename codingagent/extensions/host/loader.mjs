import { readFile, realpath, stat } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const sdkAliases = {
	"@earendil-works/pi-coding-agent": "@earendil-works/pi-coding-agent",
	"@earendil-works/pi-agent-core": "@earendil-works/pi-agent-core",
	"@earendil-works/pi-ai": "@earendil-works/pi-ai/compat",
	"@earendil-works/pi-ai/compat": "@earendil-works/pi-ai/compat",
	"@earendil-works/pi-ai/oauth": "@earendil-works/pi-ai/oauth",
	"@earendil-works/pi-ai/providers/all": "@earendil-works/pi-ai/providers/all",
	"@earendil-works/pi-tui": "@earendil-works/pi-tui",
	"@mariozechner/pi-coding-agent": "@earendil-works/pi-coding-agent",
	"@mariozechner/pi-agent-core": "@earendil-works/pi-agent-core",
	"@mariozechner/pi-ai": "@earendil-works/pi-ai/compat",
	"@mariozechner/pi-ai/compat": "@earendil-works/pi-ai/compat",
	"@mariozechner/pi-ai/oauth": "@earendil-works/pi-ai/oauth",
	"@mariozechner/pi-ai/providers/all": "@earendil-works/pi-ai/providers/all",
	"@mariozechner/pi-tui": "@earendil-works/pi-tui",
	"@sinclair/typebox": "typebox",
	"@sinclair/typebox/compile": "typebox/compile",
	"@sinclair/typebox/value": "typebox/value",
	typebox: "typebox",
	"typebox/compile": "typebox/compile",
	"typebox/value": "typebox/value",
};

async function installedSDK(specifier, context, nextResolve) {
	const target = sdkAliases[specifier];
	const root = process.env.PIGO_PI_SDK_ROOT;
	if (!target || !root) return undefined;
	try {
		return await nextResolve(target, {
			...context,
			parentURL: pathToFileURL(join(root, "package.json")).href,
		});
	} catch {
		return undefined;
	}
}

async function resolveFromSource(specifier, context, nextResolve) {
	if (!context.parentURL?.startsWith("file:") || !context.parentURL.includes("/host/entries/")) return undefined;
	try {
		const parentURL = pathToFileURL(await realpath(fileURLToPath(context.parentURL))).href;
		if (parentURL === context.parentURL) return undefined;
		return await nextResolve(specifier, { ...context, parentURL });
	} catch {
		return undefined;
	}
}

function stagedTypeScriptURL(url) {
	if (!url.startsWith("file:") || !/\.(?:ts|mts|cts)(?:\?|$)/.test(url)) return undefined;
	const resolved = new URL(url);
	const match = resolved.pathname.match(/^(.*\/host\/entries\/[^/]+)\/node_modules\/((?:@[^/]+\/)?[^/]+)(\/.*)$/);
	if (!match) return undefined;
	resolved.pathname = `${match[1]}/packages/${match[2]}${match[3]}`;
	return resolved;
}

function fallbackResolvedURL(resolved) {
	if (resolved.protocol !== "file:") return [];
	const pathname = resolved.pathname;
	const suffixes = pathname.endsWith(".js")
		? [".ts", ".tsx"]
		: pathname.endsWith(".mjs")
			? [".mts"]
			: pathname.endsWith(".cjs")
				? [".cts"]
				: pathname.endsWith(".jsx")
					? [".tsx"]
					: pathname.match(/\.[^/]+$/)
						? []
						: [".ts", ".tsx", ".js", ".mjs", ".cjs", ".mts", ".cts"];
	const candidates = suffixes.map((suffix) => {
		const candidate = new URL(resolved);
		candidate.pathname = pathname.replace(/\.(?:mjs|cjs|jsx|js)$/, "") + suffix;
		return candidate;
	});
	if (!pathname.match(/\.[^/]+$/)) {
		for (const suffix of [".ts", ".tsx", ".js", ".mjs", ".cjs", ".mts", ".cts"]) {
			const candidate = new URL(resolved);
			candidate.pathname = `${pathname.replace(/\/$/, "")}/index${suffix}`;
			candidates.push(candidate);
		}
	}
	return candidates;
}

function fallbackURLs(specifier, parentURL, error) {
	if (parentURL && (specifier.startsWith("./") || specifier.startsWith("../"))) {
		return fallbackResolvedURL(new URL(specifier, parentURL));
	}
	if (typeof error?.url === "string" && error.url.startsWith("file:")) {
		return fallbackResolvedURL(new URL(error.url));
	}
	const match = error?.message?.match(/Cannot find module '([^']+)'/);
	if (match?.[1]?.startsWith("file:")) {
		return fallbackResolvedURL(new URL(match[1]));
	}
	return [];
}

async function isFile(url) {
	try {
		return (await stat(fileURLToPath(url))).isFile();
	} catch {
		return false;
	}
}

async function sourceURL(specifier, parentURL) {
	if (!specifier.startsWith("./") && !specifier.startsWith("../")) return undefined;
	const resolved = new URL(specifier, parentURL);
	if (await isFile(resolved)) return resolved;
	for (const candidate of fallbackResolvedURL(resolved)) {
		if (await isFile(candidate)) return candidate;
	}
	return undefined;
}

async function markTypeOnlyImports(source, url) {
	const pattern = /import\s*\{([\s\S]*?)\}\s*from\s*(["'])([^"']+)\2/g;
	let rewritten = "";
	let offset = 0;
	for (const match of source.matchAll(pattern)) {
		const target = await sourceURL(match[3], url);
		if (!target || !/\.(?:ts|mts|cts)$/.test(target.pathname)) continue;
		const targetSource = await readFile(target, "utf8");
		const typeNames = new Set(Array.from(
			targetSource.matchAll(/\bexport\s+(?:declare\s+)?(?:interface|type)\s+([A-Za-z_$][\w$]*)/g),
			declaration => declaration[1],
		));
		if (typeNames.size === 0) continue;
		const imports = match[1].split(",").map(part => {
			const trimmed = part.trim();
			if (trimmed.startsWith("type ")) return part;
			const imported = trimmed.split(/\s+as\s+/)[0];
			return typeNames.has(imported) ? part.replace(imported, `type ${imported}`) : part;
		});
		const replacement = match[0].replace(match[1], imports.join(","));
		rewritten += source.slice(offset, match.index) + replacement;
		offset = match.index + match[0].length;
	}
	return offset === 0 ? source : rewritten + source.slice(offset);
}

export async function resolve(specifier, context, nextResolve) {
	try {
		const resolved = await nextResolve(specifier, context);
		const staged = stagedTypeScriptURL(resolved.url);
		if (staged && await isFile(staged)) return { ...resolved, url: staged.href, shortCircuit: true };
		return resolved;
	} catch (error) {
		for (const candidate of fallbackURLs(specifier, context.parentURL, error)) {
			if (!await isFile(candidate)) continue;
			const resolved = await nextResolve(candidate.href, context);
			const staged = stagedTypeScriptURL(resolved.url);
			if (staged && await isFile(staged)) return { ...resolved, url: staged.href, shortCircuit: true };
			return resolved;
		}
		const sdk = await installedSDK(specifier, context, nextResolve);
		if (sdk) return { ...sdk, shortCircuit: true };
		const source = await resolveFromSource(specifier, context, nextResolve);
		if (source) return { ...source, shortCircuit: true };
		throw error;
	}
}

export async function load(url, context, nextLoad) {
	const loaded = await nextLoad(url, context);
	if (!url.startsWith("file:") || !/\.(?:ts|mts|cts)(?:\?|$)/.test(url) || loaded.source == null) {
		return loaded;
	}
	const source = typeof loaded.source === "string"
		? loaded.source
		: Buffer.from(loaded.source).toString("utf8");
	return { ...loaded, source: await markTypeOnlyImports(source, url) };
}
