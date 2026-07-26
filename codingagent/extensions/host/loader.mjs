import { readdir, readFile, stat } from "node:fs/promises";
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

// PIGO_PI_SDK_ROOT is only ever pigo's own npm root — pigo never borrows the SDK
// bundled inside an installed pi — so when it is empty the import cannot be
// satisfied at all. Node's "Cannot find package" names neither the reason nor a
// way forward, and this is the one place that knows both.
// ponytail: Bun has no resolve hook, so there the same import fails with Bun's
// own message; upgrade path is a Bun plugin that intercepts nested resolution.
function sdkUnavailableError(specifier, cause) {
	const root = join(process.env.PI_CODING_AGENT_DIR || "~/.pi/agent", "npm");
	return new Error(
		`${specifier} is part of the pi SDK, which is not installed in pigo's own npm root (${root}); pigo never borrows it from an installed pi. Install it with \`npm i --prefix ${root} @earendil-works/pi-coding-agent\`, or set PIGO_PI_SDK_ROOT to the copy pigo should use. Extensions that declare their own SDK dependency are unaffected`,
		{ cause },
	);
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
		return legacy ?? (await nextResolve(specifier, context));
	} catch (error) {
		for (const candidate of fallbackURLs(specifier, context.parentURL, error)) {
			if (await isFile(candidate)) return await nextResolve(candidate.href, context);
		}
		const sdk = await installedSDK(specifier, context, nextResolve);
		if (sdk) return { ...sdk, shortCircuit: true };
		if (sdkAliases[specifier] && !process.env.PIGO_PI_SDK_ROOT) throw sdkUnavailableError(specifier, error);
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
