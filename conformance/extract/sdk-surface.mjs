// Regenerates the export surface of the embedded orb-extension-sdk
// (agent/extensions/host/sdk) from the pinned upstream package sources: every
// upstream value export links (implemented, or a stub that throws
// OrbUnsupportedCapability on use) and every type export is declared, so
// extensions written against the pinned pi link unchanged. Orb's own
// implementations and hand-written declarations are kept; stubs and
// declarations for names upstream no longer exports are dropped.
//
// Run from the upstream checkout: node conformance/extract/sdk-surface.mjs <sdk dir> <commit>
import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const ts = require("typescript");
const [sdkDir, commit] = process.argv.slice(2);
if (!sdkDir || !commit) throw new Error("usage: sdk-surface.mjs <sdk dir> <commit>");
const version = JSON.parse(readFileSync("packages/coding-agent/package.json", "utf8")).version;
const provenance = `pi ${version}, commit ${commit.slice(0, 8)}`;

// Orb module file → upstream package specifier and entry source.
const modules = {
	"coding-agent": ["@earendil-works/pi-coding-agent", "packages/coding-agent/src/index.ts"],
	"agent-core": ["@earendil-works/pi-agent-core", "packages/agent/src/index.ts"],
	ai: ["@earendil-works/pi-ai", "packages/ai/src/index.ts"],
	"ai-compat": ["@earendil-works/pi-ai/compat", "packages/ai/src/compat.ts"],
	"ai-models": ["@earendil-works/pi-ai/models", "packages/ai/src/models.ts"],
	"ai-oauth": ["@earendil-works/pi-ai/oauth", "packages/ai/src/oauth.ts"],
	"ai-providers-all": ["@earendil-works/pi-ai/providers/all", "packages/ai/src/providers/all.ts"],
	tui: ["@earendil-works/pi-tui", "packages/tui/src/index.ts"],
};

const program = ts.createProgram(
	Object.values(modules).map(([, entry]) => path.resolve(entry)),
	{
		allowImportingTsExtensions: true,
		noEmit: true,
		module: ts.ModuleKind.NodeNext,
		moduleResolution: ts.ModuleResolutionKind.NodeNext,
		target: ts.ScriptTarget.ES2022,
		skipLibCheck: true,
	},
);
const checker = program.getTypeChecker();

function upstreamSurface(entry) {
	const source = program.getSourceFile(path.resolve(entry));
	const values = [];
	const types = [];
	for (const exported of checker.getExportsOfModule(checker.getSymbolAtLocation(source))) {
		const target = exported.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(exported) : exported;
		(target.flags & ts.SymbolFlags.Value ? values : types).push(exported.name);
	}
	return { values: values.sort(), types: types.sort() };
}

// Top-level declarations of an existing .d.ts, by exported name.
function existingDeclarations(file) {
	const declarations = new Map();
	if (!existsSync(file)) return declarations;
	const source = ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true);
	for (const statement of source.statements) {
		const names = ts.isVariableStatement(statement)
			? statement.declarationList.declarations.map((declaration) => declaration.name.getText(source))
			: statement.name
				? [statement.name.getText(source)]
				: [];
		for (const name of names) declarations.set(name, statement.getText(source));
	}
	return declarations;
}

// Names a module's .mjs exports other than through its own stub lines; a star
// re-export contributes every name the re-exported module exports.
const STUB_LINE = /^export const (\w+) = stub\("\1"\);$/;
function implementedNames(text) {
	const names = new Set();
	for (const line of text.split("\n")) {
		const star = line.match(/^export \* from "\.\/([^"]+)";/);
		if (star) {
			const reexported = readFileSync(path.join(sdkDir, star[1]), "utf8");
			for (const name of implementedNames(reexported)) names.add(name);
			for (const stubbed of reexported.matchAll(/^export const (\w+) = stub\("\1"\);$/gm)) names.add(stubbed[1]);
		}
		if (STUB_LINE.test(line)) continue;
		const declared = line.match(/^export (?:async function\*?|function\*?|class|const|let|var) (\w+)/);
		if (declared) names.add(declared[1]);
	}
	for (const listed of text.matchAll(/^export \{([^}]*)\}/gm)) {
		for (const part of listed[1].split(",")) if (part.trim()) names.add(part.trim().split(/\s+as\s+/).pop());
	}
	return names;
}

const report = [];
for (const [module, [specifier, entry]] of Object.entries(modules)) {
	const surface = upstreamSurface(entry);
	const moduleFile = path.join(sdkDir, `${module}.mjs`);
	const declarationFile = path.join(sdkDir, `${module}.d.ts`);
	let text = existsSync(moduleFile)
		? readFileSync(moduleFile, "utf8")
		: `// orb-extension-sdk: stub-only surface of ${specifier} (upstream ${provenance},\n` +
			`// MIT © Mario Zechner). orb implements nothing on this subpath: every\n` +
			`// upstream export name links and throws OrbUnsupportedCapability on call,\n` +
			`// construction, or property access.\n` +
			`import { unsupported } from "./internal/unsupported.mjs";\n\n` +
			`const stub = (name) => unsupported("${specifier.replace("@earendil-works/pi-", "")}", name, ["none"]);\n\n`;
	text = text
		.replace(/pi \d+\.\d+\.\d+, commit [0-9a-f]{8}/g, provenance)
		.replace(/pi \d+\.\d+\.\d+,\n\/\/ commit [0-9a-f]{8}/g, `pi ${version},\n// commit ${commit.slice(0, 8)}`);
	const implemented = implementedNames(text);
	const stubs = surface.values.filter((name) => !implemented.has(name)).map((name) => `export const ${name} = stub("${name}");`);
	const lines = text.split("\n");
	const first = lines.findIndex((line) => STUB_LINE.test(line));
	const kept = lines.filter((line) => !STUB_LINE.test(line));
	const at = first < 0 ? kept.length - (kept.at(-1) === "" ? 1 : 0) : first;
	kept.splice(at, 0, ...stubs);
	writeFileSync(moduleFile, kept.join("\n"));

	const declarations = existingDeclarations(declarationFile);
	const declare = (name, fallback) => declarations.get(name) ?? fallback;
	const implementedCount = surface.values.filter((name) => implemented.has(name)).length;
	const body = [
		`// orb-extension-sdk: type surface of ${specifier} (upstream ${provenance}, MIT © Mario Zechner).`,
		"// Generated by conformance/extract/sdk-surface.mjs from the pinned upstream package sources.",
		"// Every upstream export name is declared so Node type-stripping and the loader's",
		"// type-only-import classifier see the same surface as the real package.",
		"",
		`// Value exports (${surface.values.length}; ${implementedCount} implemented, the rest are unsupported stubs at runtime):`,
		...surface.values.map((name) => declare(name, `export declare const ${name}: any;`)),
		"",
		`// Type-only exports (${surface.types.length}):`,
		...surface.types.map((name) => declare(name, `export type ${name} = any;`)),
		"",
	];
	writeFileSync(declarationFile, body.join("\n"));
	const orphaned = [...implemented].filter((name) => !surface.values.includes(name) && !surface.types.includes(name));
	report.push(`${module}: ${surface.values.length} values (${implementedCount} implemented), ${surface.types.length} types` +
		(orphaned.length ? `; implemented but no longer upstream: ${orphaned.join(", ")}` : ""));
}
console.log(report.join("\n"));
