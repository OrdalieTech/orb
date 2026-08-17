import { readdir, readFile, realpath, stat } from "node:fs/promises";
import nodeModule from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

// Added in Node 22.13; absent on the 22.6-22.12 range the host still runs, where
// TypeScript under node_modules is refused with no way to transpile it and the
// load hook says so precisely instead.
const stripTypeScriptTypes = nodeModule.stripTypeScriptTypes;

// Node 26 removed TypeScript transformation: it accepts only mode:"strip", and
// rejects sourceMap alongside it because strip-only output keeps the original
// positions. Enums and parameter properties therefore cannot run there at all,
// natively or here. The call shape is taken from what this build accepts rather
// than from its version, so a vendor or nightly build off the release timeline
// keeps working.
const stripOptions = (() => {
	for (const options of [{ mode: "transform", sourceMap: true }, { mode: "strip" }]) {
		try {
			stripTypeScriptTypes?.("", { ...options, sourceUrl: "file:///probe.ts" });
			return options;
		} catch {}
	}
	return { mode: "strip" };
})();

// Every legacy pi SDK specifier resolves into the embedded orb-extension-sdk
// that the manager materializes and names via ORB_EXTENSION_SDK_ROOT. This map
// is the loader's entire knowledge of that surface: a specifier under the
// legacy scopes that is absent here resolves nowhere — in particular never
// from node_modules, whatever is installed there — and fails with a precise
// error instead. The pi-ai root serves upstream's index surface and "/compat"
// its superset with the legacy global API, matching the upstream exports map.
// ponytail: Bun has no resolve hook, so these aliases have a weaker Bun
// counterpart: NODE_PATH wrapper packages (runtime_entry.go), which a real
// install in the extension's own tree would still outrank; upgrade path is a
// Bun plugin that intercepts nested resolution.
const sdkModules = {
	"@earendil-works/pi-coding-agent": "coding-agent.mjs",
	"@earendil-works/pi-agent-core": "agent-core.mjs",
	"@earendil-works/pi-ai": "ai.mjs",
	"@earendil-works/pi-ai/compat": "ai-compat.mjs",
	"@earendil-works/pi-ai/oauth": "ai-oauth.mjs",
	"@earendil-works/pi-ai/providers/all": "ai-providers-all.mjs",
	"@earendil-works/pi-tui": "tui.mjs",
};
for (const [specifier, module] of Object.entries(sdkModules)) {
	sdkModules[specifier.replace("@earendil-works/", "@mariozechner/")] = module;
}

const legacySDKScopes = /^@(?:earendil-works|mariozechner)\/pi-/;

function importerPath(parentURL) {
	if (!parentURL) return "<extension host>";
	try {
		return parentURL.startsWith("file:") ? fileURLToPath(new URL(parentURL)) : parentURL;
	} catch {
		return parentURL;
	}
}

function resolveSDKModule(specifier, context) {
	if (!legacySDKScopes.test(specifier)) return undefined;
	const module = sdkModules[specifier];
	const importer = importerPath(context.parentURL);
	if (!module) {
		const surface = Object.keys(sdkModules)
			.filter((name) => name.startsWith("@earendil-works/"))
			.join(", ");
		throw new Error(
			`"${specifier}" (imported by ${importer}) is not part of the orb extension SDK surface. orb-extension-sdk serves ${surface} (and their @mariozechner/* historical names); no other pi SDK module exists, and a real pi SDK is never resolved from node_modules`,
		);
	}
	const root = process.env.ORB_EXTENSION_SDK_ROOT;
	if (!root) {
		throw new Error(
			`"${specifier}" (imported by ${importer}) needs the embedded orb-extension-sdk, but ORB_EXTENSION_SDK_ROOT is not set in the host environment; this is an orb bug`,
		);
	}
	return { url: pathToFileURL(join(root, module)).href, shortCircuit: true };
}

// The guard behind the alias map: every resolved module realpath is recorded,
// and one that lands inside an installed pi SDK package aborts the load with
// the import chain. Resolution precedes evaluation, so a real SDK module trips
// this before a line of it runs — catching the transitive routes the alias map
// cannot see: relative paths into node_modules, symlinked package names, a
// dependency resolving the package for itself.
const realSDKPathPattern = /node_modules[/\\]@(?:earendil-works|mariozechner)[/\\]pi-/;
const guardRefusalCode = "ORB_REAL_PI_SDK_REFUSED";
const resolvedRealpaths = new Map(); // resolved URL -> realpath ("" when unreadable)
const resolvedImporters = new Map(); // resolved URL -> importing module URL

async function recordResolution(specifier, context, resolution) {
	const url = resolution?.url;
	if (typeof url !== "string" || !url.startsWith("file:")) return resolution;
	if (!resolvedImporters.has(url) && context.parentURL) resolvedImporters.set(url, context.parentURL);
	let real = resolvedRealpaths.get(url);
	if (real === undefined) {
		try {
			real = await realpath(fileURLToPath(new URL(url)));
		} catch {
			real = "";
		}
		resolvedRealpaths.set(url, real);
	}
	if (realSDKPathPattern.test(real)) throw realSDKRefusal(specifier, context.parentURL, real);
	return resolution;
}

function realSDKRefusal(specifier, parentURL, real) {
	const chain = [];
	for (let url = parentURL; url && chain.length < 32; url = resolvedImporters.get(url)) {
		chain.unshift(importerPath(url));
	}
	const message =
		`refusing to load "${specifier}": it resolves into a real pi SDK install at ${real} ` +
		`(import chain: ${chain.join(" -> ") || "<extension host>"} -> "${specifier}"). ` +
		`orb serves the pi SDK surface only from its embedded orb-extension-sdk and never executes ` +
		`@earendil-works/pi-* or @mariozechner/pi-* code from node_modules; remove the installed copy or the import reaching it`;
	// The diagnostic also goes to the host's stderr: an extension that catches
	// its own failed dynamic import would otherwise swallow it silently.
	console.error(`orb extension host: ${message}`);
	return Object.assign(new Error(message), { code: guardRefusalCode });
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

// The SDK ships one declaration file per module, mirroring the full upstream
// export surface; names declared only as types are the ones Node's stripping
// would leave dangling as imports.
const sdkTypeNamesCache = new Map();

async function sdkTypeNames(module) {
	const root = process.env.ORB_EXTENSION_SDK_ROOT;
	if (!module || !root) return undefined;
	const cached = sdkTypeNamesCache.get(module);
	if (cached) return cached;
	let names = { types: new Set(), values: new Set() };
	try {
		names = declaredNames(await readFile(join(root, module.replace(/\.mjs$/, ".d.ts")), "utf8"), names);
	} catch {
		return undefined;
	}
	const types = new Set(Array.from(names.types).filter((name) => !names.values.has(name)));
	sdkTypeNamesCache.set(module, types);
	return types;
}

// Node's type stripping keeps every named import, so a type imported from a bare
// specifier fails to link; upstream's jiti transform elides it. Legacy SDK
// specifiers classify against the embedded SDK's declarations — the module they
// actually resolve to — never against an installed package.
async function importedTypeNames(specifier, url) {
	const target = await sourceURL(specifier, url);
	if (target) {
		if (!/\.(?:ts|mts|cts)$/.test(target.pathname)) return undefined;
		return declaredNames(await readFile(target, "utf8"), { types: new Set(), values: new Set() }).types;
	}
	if (legacySDKScopes.test(specifier)) return await sdkTypeNames(sdkModules[specifier]);
	const name = specifier.match(/^(@[^/]+\/[^/]+|[^@.#/][^/:]*)(?:\/|$)/)?.[1];
	if (!name) return undefined;
	const directory = await packageDirectory(name, dirname(fileURLToPath(url)));
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
	const sdk = resolveSDKModule(specifier, context);
	if (sdk) return sdk;
	try {
		return await recordResolution(specifier, context, await nextResolve(specifier, context));
	} catch (error) {
		if (error?.code === guardRefusalCode) throw error;
		for (const candidate of fallbackURLs(specifier, context.parentURL, error)) {
			if (await isFile(candidate)) {
				return await recordResolution(specifier, context, await nextResolve(candidate.href, context));
			}
		}
		throw error;
	}
}

// Node refuses to strip types from any TypeScript file whose path contains a
// node_modules segment, which is where every installed extension and its
// dependencies live. Supplying the transpiled source from the load hook removes
// the restriction rather than routing around it: resolution stays exactly as the
// package manager laid it out, so a dependency's own dependencies — at any
// nesting depth, through pnpm's store symlinks — keep resolving normally.
function isRefusedTypeScript(url) {
	if (!url.startsWith("file:") || !/\.(?:ts|mts|cts)(?:\?|$)/.test(url)) return false;
	return new URL(url).pathname.split("/").includes("node_modules");
}

// Below Node 22.13 there is no transpiler to substitute, so the refusal stands.
// Node's own ERR_UNSUPPORTED_NODE_MODULES_TYPE_STRIPPING names neither the
// version that lifts it nor a way forward, and it is thrown from a worker whose
// stack points into Node internals.
function refusedTypeScriptError(url) {
	return Object.assign(
		new Error(
			`${fileURLToPath(url)} is TypeScript published inside node_modules, which Node ${process.versions.node} cannot compile. Upgrade to Node >=22.13, or use a build of this package that ships JavaScript.`,
		),
		{ code: "ERR_UNSUPPORTED_NODE_MODULES_TYPE_STRIPPING" },
	);
}

async function loadRefusedTypeScript(url) {
	const source = await markTypeOnlyImports(await readFile(fileURLToPath(url), "utf8"), url);
	return {
		format: await typeScriptFormat(url, source),
		shortCircuit: true,
		// transform wherever it exists, matching --experimental-transform-types,
		// so enums and parameter properties run exactly as they do outside
		// node_modules on the same build.
		source: stripTypeScriptTypes(source, { ...stripOptions, sourceUrl: url }),
	};
}

// A bare .ts with no top-level "type" is ambiguous, and Node resolves it by
// looking for module syntax rather than assuming commonjs. Short-circuiting the
// load hook takes that detection away, so it has to be reproduced: without it an
// ESM extension published without "type":"module" — the shape npm packs by
// default — fails on `export`.
// ponytail: detection is a scan for a top-level import/export instead of a
// parse, so a file whose only ESM marker is `import.meta` or top-level await is
// read as commonjs; upgrade path is Node exposing containsModuleSyntax.
const moduleSyntax = /^[ \t]*(?:export\b|import\s+(?:[\w*{"']|type\b))/m;

async function typeScriptFormat(url, source) {
	if (/\.mts(?:\?|$)/.test(url)) return "module";
	if (/\.cts(?:\?|$)/.test(url)) return "commonjs";
	for (let directory = dirname(fileURLToPath(url)); ; directory = dirname(directory)) {
		try {
			const manifest = JSON.parse(await readFile(join(directory, "package.json"), "utf8"));
			if (manifest.type === "module" || manifest.type === "commonjs") return manifest.type;
			break;
		} catch {
			const parent = dirname(directory);
			if (parent === directory) break;
		}
	}
	return moduleSyntax.test(source) ? "module" : "commonjs";
}

export async function load(url, context, nextLoad) {
	// Where Node raises the refusal moves between versions — from inside nextLoad
	// on 22.12-22.13 to compile time on 24 and later — so catching around nextLoad
	// is not reliable and the condition is checked instead. Files outside
	// node_modules keep Node's own native stripping.
	if (isRefusedTypeScript(url)) {
		if (!stripTypeScriptTypes) throw refusedTypeScriptError(url);
		return await loadRefusedTypeScript(url);
	}
	const loaded = await nextLoad(url, context);
	if (!url.startsWith("file:") || !/\.(?:ts|mts|cts)(?:\?|$)/.test(url) || loaded.source == null) {
		return loaded;
	}
	const source = typeof loaded.source === "string"
		? loaded.source
		: Buffer.from(loaded.source).toString("utf8");
	// Node 22.6 rejects a string here for the module-typescript format its own
	// loader reports (ERR_INVALID_RETURN_PROPERTY_VALUE), which made every
	// TypeScript extension fail on it; a buffer is accepted by every version.
	return { ...loaded, source: Buffer.from(await markTypeOnlyImports(source, url)) };
}
