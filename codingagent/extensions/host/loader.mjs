import { readdir, readFile, realpath, stat } from "node:fs/promises";
import { dirname, join } from "node:path";
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

// pi-ai's root entry keeps only the side-effect-free core; the global API that
// published extensions import lives on the "/compat" subpath, which re-exports
// the root. Redirecting through the same context keeps the copy the extension
// resolved to, so a pinned or older install still wins.
async function legacySurface(specifier, context, nextResolve) {
	const target = sdkAliases[specifier];
	if (!target || !target.startsWith(`${specifier}/`)) return undefined;
	try {
		return await nextResolve(target, context);
	} catch {
		return undefined;
	}
}

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

function declaredNames(source, names) {
	for (const declaration of source.matchAll(/\bexport\s+(?:declare\s+)?(?:interface|type)\s+([A-Za-z_$][\w$]*)/g)) {
		names.types.add(declaration[1]);
	}
	for (const declaration of source.matchAll(
		/\bexport\s+(?:declare\s+)?(?:abstract\s+)?(?:async\s+)?(?:const|let|var|function|class|enum)\b[\s*]*([A-Za-z_$][\w$]*)/g,
	)) {
		names.values.add(declaration[1]);
	}
	for (const clause of source.matchAll(/\bexport\s+(type\s+)?\{([^}]*)\}/g)) {
		for (const entry of clause[2].split(",")) {
			const parts = entry.trim().split(/\s+as\s+/);
			const exported = (parts[1] ?? parts[0] ?? "").trim().replace(/^type\s+/, "");
			if (!/^[A-Za-z_$][\w$]*$/.test(exported)) continue;
			(clause[1] || /^type\s/.test(entry.trim()) ? names.types : names.values).add(exported);
		}
	}
	return names;
}

// ponytail: the package surface is read from its declaration files instead of a
// real `exports`-map walk that follows `export *`; upgrade path is resolving the
// subpath entry the way Node does and following its re-export graph.
const packageDeclarationBudget = 4 << 20;
const packageTypeNamesCache = new Map();

async function packageTypeNames(directory) {
	const cached = packageTypeNamesCache.get(directory);
	if (cached) return cached;
	const names = { types: new Set(), values: new Set() };
	let budget = packageDeclarationBudget;
	for (const pending = [directory]; pending.length > 0 && budget > 0; ) {
		const current = pending.pop();
		let listing;
		try {
			listing = await readdir(current, { withFileTypes: true });
		} catch {
			continue;
		}
		for (const child of listing) {
			if (child.isDirectory()) {
				if (child.name !== "node_modules") pending.push(join(current, child.name));
			} else if (/\.(?:ts|mts|cts)$/.test(child.name) && budget > 0) {
				try {
					const source = await readFile(join(current, child.name), "utf8");
					budget -= source.length;
					declaredNames(source, names);
				} catch {}
			}
		}
	}
	const types = new Set(Array.from(names.types).filter(name => !names.values.has(name)));
	packageTypeNamesCache.set(directory, types);
	return types;
}

async function packageDirectory(name, from) {
	for (let directory = from; ; ) {
		const candidate = join(directory, "node_modules", name);
		if (await isFile(pathToFileURL(join(candidate, "package.json")))) return candidate;
		const parent = dirname(directory);
		if (parent === directory) return undefined;
		directory = parent;
	}
}

// Node's type stripping keeps every named import, so a type imported from a bare
// specifier fails to link; upstream's jiti transform elides it.
async function importedTypeNames(specifier, url) {
	const target = await sourceURL(specifier, url);
	if (target) {
		if (!/\.(?:ts|mts|cts)$/.test(target.pathname)) return undefined;
		return declaredNames(await readFile(target, "utf8"), { types: new Set(), values: new Set() }).types;
	}
	const name = specifier.match(/^(@[^/]+\/[^/]+|[^@.#/][^/:]*)(?:\/|$)/)?.[1];
	if (!name) return undefined;
	let directory = await packageDirectory(name, dirname(fileURLToPath(url)));
	if (!directory && process.env.PIGO_PI_SDK_ROOT) {
		const aliased = sdkAliases[specifier]?.match(/^(@[^/]+\/[^/]+|[^@./][^/]*)(?:\/|$)/)?.[1];
		if (aliased) directory = await packageDirectory(aliased, process.env.PIGO_PI_SDK_ROOT);
	}
	return directory ? await packageTypeNames(directory) : undefined;
}

async function markTypeOnlyImports(source, url) {
	const pattern = /import\s*\{([\s\S]*?)\}\s*from\s*(["'])([^"']+)\2/g;
	let rewritten = "";
	let offset = 0;
	for (const match of source.matchAll(pattern)) {
		const typeNames = await importedTypeNames(match[3], url);
		if (!typeNames || typeNames.size === 0) continue;
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
		const legacy = await legacySurface(specifier, context, nextResolve);
		const resolved = legacy ?? (await nextResolve(specifier, context));
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
