#!/usr/bin/env node

// Failure taxonomy for the ecosystem extension matrix.
//
// Every non-passing package gets one machine-readable class plus the specific
// capability that was missing, so a follow-up agent can act on it without
// re-reading raw stderr. Raw diagnostics are never discarded: `evidence` is a
// bounded excerpt of the text the class was derived from, and the full stderr
// stays in the per-package JSONL stream written by matrix.mjs.

export const FAILURE_CLASSES = [
	"install_failure", // the package never materialized in the prepared tree
	"escaping_entrypoint", // pi.extensions declares a path outside the package root
	"invalid_entrypoint", // pi.extensions declares something that is not a path
	"environment_constraint", // the package demands a toolchain the harness does not provide
	"native_addon", // requires a compiled .node addon / node-gyp / prebuilt binding
	"typescript_unsupported", // TypeScript under node_modules that the runtime refused to evaluate
	"missing_node_builtin", // a node:* builtin (or shim of one) is absent
	"missing_dependency", // a plain module specifier did not resolve
	"unsupported_sdk_export", // the pi SDK shim is missing a named export the package imports
	"unsupported_pi_api", // pi.<member> is absent or not callable
	"unsupported_syntax", // the runtime could not parse the extension source
	"extension_load_error", // the host reported a package-specific load failure
	"registration_mismatch", // loaded, but the observable registration differs from upstream
	"timeout", // exceeded the per-process deadline or the per-package budget
	"crash", // exited/aborted before the probe completed
	"resource_exhaustion", // host ran out of PIDs/memory/threads; a harness condition, not a verdict
	"runtime_not_forced", // the probe did not run on the JS engine the tier requires
	"flaky", // attempts disagreed with each other
	"unknown", // retained raw diagnostics, unclassified
];

const RESOURCE_PATTERNS = [
	/\bEAGAIN\b/,
	/\bENOMEM\b/,
	/Resource temporarily unavailable/i,
	/new Worker/,
	/pthread_create/i,
	/Cannot allocate memory/i,
	/JavaScript heap out of memory/i,
	/std::bad_alloc/i,
	/fork failed/i,
];

// Ordered: the first rule that matches wins, so specific rules precede generic ones.
const RULES = [
	// Environment constraints are the package telling us it wants a different
	// toolchain. They are not orb incompatibilities and must never be counted as
	// one; the corpus pins Node 24 and npm by construction.
	{
		class: "environment_constraint",
		pattern: /EBADENGINE|Unsupported engine|engine "node" is incompatible|required: \{[^}]*node/,
		capability: (match, text) => {
			const wanted =
				/(?:node['"]?\s*:\s*['"]?)([^'",}\s]+)/.exec(text) ?? /Expected version\s+['"]([^'"]+)['"]/.exec(text);
			return `engines.node ${wanted ? wanted[1] : "unsatisfied"}`;
		},
	},
	{
		class: "environment_constraint",
		pattern: /please-use-(pnpm|yarn|bun)|Use "(pnpm|yarn|bun)" to install|only works with pnpm/i,
		capability: (match) => `package_manager(${match[1] ?? match[2] ?? "non-npm"})`,
	},
	{
		class: "resource_exhaustion",
		test: (text) => RESOURCE_PATTERNS.some((pattern) => pattern.test(text)),
		capability: () => "host_process_or_memory_headroom",
	},
	{
		class: "unsupported_sdk_export",
		pattern: /The requested module '([^']+)' does not provide an export named '([^']+)'/,
		capability: (match) => `${match[1]} export ${match[2]}`,
	},
	{
		class: "unsupported_sdk_export",
		pattern: /export '([^']+)' \(imported as [^)]+\) was not found in '([^']+)'/,
		capability: (match) => `${match[2]} export ${match[1]}`,
	},
	// Bun phrases the same defect differently from Node. Normalising it keeps a
	// single missing export from looking like two unrelated failures when the
	// Node and Bun tiers are compared.
	{
		class: "unsupported_sdk_export",
		pattern: /Export named '([^']+)' not found in module '([^']+)'/,
		capability: (match) => `${packageOf(match[2])} export ${match[1]}`,
	},
	{
		class: "native_addon",
		pattern: /ERR_DLOPEN_FAILED|Could not locate the bindings file|node-gyp|was compiled against a different Node\.js version|invalid ELF header|\.node['"]?\)?: cannot open|prebuild-install/,
		capability: (match, text) => nativeModuleName(text) ?? "native_node_addon",
	},
	{
		class: "missing_node_builtin",
		pattern: /(?:Cannot find module|No such built-in module:?|ERR_UNKNOWN_BUILTIN_MODULE:?)\s*'?(node:[A-Za-z_/]+)'?/,
		capability: (match) => match[1],
	},
	{
		class: "missing_node_builtin",
		pattern: /(?:is not implemented|not implemented yet|is not supported) in Bun.*?['"](node:[A-Za-z_/]+)['"]/i,
		capability: (match) => match[1],
	},
	// Node refuses to strip types for TypeScript that lives under node_modules;
	// Bun evaluates the same file. This is the mission's "TypeScript under
	// node_modules" class and must not be lumped in with generic load errors.
	{
		class: "typescript_unsupported",
		pattern: /Stripping types is currently unsupported for files under node_modules/,
		capability: () => "typescript_in_node_modules",
	},
	{
		class: "typescript_unsupported",
		pattern: /ERR_UNKNOWN_FILE_EXTENSION.*?['"]?(\.[cm]?tsx?)['"]?|Unknown file extension "(\.[cm]?tsx?)"/,
		capability: (match) => `typescript_evaluation(${match[1] ?? match[2]})`,
	},
	{
		class: "typescript_unsupported",
		pattern: /ERR_INVALID_TYPESCRIPT_SYNTAX|Transforming (?:const enums|namespaces|parameter properties|enums)[^\n]*is not supported|type-stripping|--experimental-strip-types/,
		capability: (match) => `typescript_transform(${(match[0] ?? "").slice(0, 60).trim()})`,
	},
	{
		class: "unsupported_pi_api",
		pattern: /(?:TypeError:\s*)?(?:pi|ctx|context)\.([A-Za-z_$][\w$]*)(?:\.[A-Za-z_$][\w$]*)? is not a function/,
		capability: (match) => `pi.${match[1]}`,
	},
	{
		class: "unsupported_pi_api",
		pattern: /Cannot read propert(?:y|ies) (?:'|")([A-Za-z_$][\w$]*)(?:'|") of undefined \(reading[^)]*\)|undefined is not an object \(evaluating '(?:pi|ctx)\.([A-Za-z_$][\w$]*)/,
		capability: (match) => `pi.${match[1] ?? match[2]}`,
	},
	{
		class: "missing_dependency",
		pattern: /(?:ERR_MODULE_NOT_FOUND|MODULE_NOT_FOUND)[\s\S]{0,200}?Cannot find (?:module|package) '([^']+)'|Cannot find (?:module|package) '([^']+)'/,
		capability: (match) => match[1] ?? match[2],
	},
	{
		class: "unsupported_syntax",
		pattern: /SyntaxError: ([^\n]{0,120})/,
		capability: (match) => `parse(${match[1].trim()})`,
	},
	// Last resort for a host-reported load failure with a package-specific
	// message: keep the message as the capability so it stays actionable.
	{
		class: "extension_load_error",
		pattern: /(?:Failed to load extension|Extension error \([^)]*\)):\s*([^\n]{0,160})/,
		capability: (match) => match[1].trim(),
	},
];

// Recover the package specifier from a resolved file path inside node_modules.
function packageOf(modulePath) {
	const parts = String(modulePath).split("node_modules/");
	const tail = parts.length > 1 ? parts[parts.length - 1] : modulePath;
	const segments = String(tail).split("/");
	if (segments[0]?.startsWith("@") && segments.length > 1) return `${segments[0]}/${segments[1]}`;
	return segments[0] ?? modulePath;
}

function nativeModuleName(text) {
	const direct = /([\w@/.-]+\.node)/.exec(text);
	if (direct) return direct[1];
	const bindings = /bindings file[^\n]*?["']([^"']+)["']/.exec(text);
	return bindings ? bindings[1] : null;
}

function excerpt(text, anchor) {
	if (!text) return "";
	const lines = text.split("\n").filter((line) => line.trim().length > 0);
	const index = anchor ? lines.findIndex((line) => line.includes(anchor)) : -1;
	const start = index >= 0 ? index : lines.findIndex((line) => /error|warning|failed|cannot|not a function/i.test(line));
	const slice = lines.slice(Math.max(0, start === -1 ? 0 : start), (start === -1 ? 0 : start) + 4).join("\n");
	return slice.length > 900 ? `${slice.slice(0, 900)}…` : slice;
}

/**
 * Classify a runtime-level load or execution failure from its raw diagnostics.
 * `diagnostics` is the array retained by matrix.mjs runtimeSummary().
 */
export function classifyDiagnostics(diagnostics = [], context = {}) {
	const ordered = [...diagnostics].sort((left, right) => (right.count ?? 0) - (left.count ?? 0));
	for (const diagnostic of ordered) {
		const text = [diagnostic.loadError, diagnostic.stderr, diagnostic.error, diagnostic.stdoutRemainder]
			.filter(Boolean)
			.join("\n");
		if (diagnostic.runtimeNotForced) {
			return {
				class: "runtime_not_forced",
				capability: `js_engine(${context.expectedEngine ?? "?"})`,
				detail: `probe observed ${diagnostic.observedEngine ?? "unknown"} instead of ${context.expectedEngine ?? "?"}`,
				evidence: excerpt(text),
			};
		}
		for (const rule of RULES) {
			if (rule.test) {
				if (!rule.test(text)) continue;
				return { class: rule.class, capability: rule.capability(null, text), detail: null, evidence: excerpt(text) };
			}
			const match = rule.pattern.exec(text);
			if (!match) continue;
			return {
				class: rule.class,
				capability: rule.capability(match, text),
				detail: match[0].slice(0, 200),
				evidence: excerpt(text, match[0].slice(0, 40)),
			};
		}
		if (diagnostic.timedOut) {
			return { class: "timeout", capability: "load_within_deadline", detail: diagnostic.error, evidence: excerpt(text) };
		}
		if (/exited before probe|signal|SIGABRT|SIGSEGV|SIGKILL|core dumped/i.test(text)) {
			return { class: "crash", capability: "process_survives_load", detail: diagnostic.error, evidence: excerpt(text) };
		}
	}
	if (context.budgetExceeded) {
		return { class: "timeout", capability: "package_within_budget", detail: "per-package budget exceeded", evidence: "" };
	}
	const first = ordered[0];
	return {
		class: "unknown",
		capability: null,
		detail: first?.error ?? null,
		evidence: excerpt([first?.loadError, first?.stderr].filter(Boolean).join("\n")),
	};
}

function toolKey(tool) {
	return typeof tool === "string" ? tool : (tool?.name ?? "");
}

function indexByName(values) {
	const index = new Map();
	for (const value of values ?? []) {
		const name = toolKey(value);
		if (!index.has(name)) index.set(name, []);
		index.get(name).push(value);
	}
	return index;
}

function whitespaceOnlyDifference(left, right) {
	return typeof left === "string" && typeof right === "string" && left !== right && left.trim() === right.trim();
}

function fieldDifference(referenceTool, candidateTool) {
	if (typeof referenceTool === "string" || typeof candidateTool === "string") return null;
	for (const field of ["description", "parameters", "promptGuidelines"]) {
		const left = referenceTool?.[field];
		const right = candidateTool?.[field];
		if (JSON.stringify(left) === JSON.stringify(right)) continue;
		const dropped = Array.isArray(left) && Array.isArray(right) && right.length === 0 && left.length > 0;
		return {
			field,
			kind: dropped ? "dropped" : whitespaceOnlyDifference(left, right) ? "whitespace" : "differs",
			reference: JSON.stringify(left)?.slice(0, 240) ?? null,
			candidate: JSON.stringify(right)?.slice(0, 240) ?? null,
		};
	}
	return null;
}

/**
 * Explain a registration mismatch in terms of the specific surface, names and
 * fields that differ between the upstream reference delta and the candidate
 * delta. Both deltas are already observer-baseline subtracted.
 */
export function describeMismatch(referenceDelta, candidateDelta) {
	const findings = [];
	for (const surface of ["activeTools", "allTools", "commands", "rpcCommands"]) {
		for (const side of ["added", "removed"]) {
			const reference = referenceDelta?.[surface]?.[side] ?? [];
			const candidate = candidateDelta?.[surface]?.[side] ?? [];
			const referenceKeys = new Set(reference.map((value) => JSON.stringify(value)));
			const candidateKeys = new Set(candidate.map((value) => JSON.stringify(value)));
			const referenceOnly = reference.filter((value) => !candidateKeys.has(JSON.stringify(value)));
			const candidateOnly = candidate.filter((value) => !referenceKeys.has(JSON.stringify(value)));
			if (referenceOnly.length === 0 && candidateOnly.length === 0) continue;

			const candidateByName = indexByName(candidateOnly);
			const matchedNames = new Set();
			for (const tool of referenceOnly) {
				const name = toolKey(tool);
				const counterpart = candidateByName.get(name)?.[0];
				if (!counterpart) continue;
				matchedNames.add(name);
				const difference = fieldDifference(tool, counterpart);
				findings.push({
					surface,
					side,
					kind: difference ? `${difference.field}_${difference.kind}` : "value_differs",
					name,
					field: difference?.field ?? null,
					reference: difference?.reference ?? null,
					candidate: difference?.candidate ?? null,
				});
			}
			const missing = referenceOnly.filter((tool) => !matchedNames.has(toolKey(tool))).map(toolKey);
			const extra = candidateOnly.filter((tool) => !matchedNames.has(toolKey(tool))).map(toolKey);
			if (missing.length > 0) findings.push({ surface, side, kind: "missing_in_candidate", names: missing.sort() });
			if (extra.length > 0) findings.push({ surface, side, kind: "extra_in_candidate", names: extra.sort() });
		}
	}
	return findings;
}

const SURFACE_CAPABILITY = {
	activeTools: "active_tool_gating",
	allTools: "tool_definition",
	commands: "command_registration",
	rpcCommands: "command_registration",
};

/**
 * Reduce mismatch findings to one actionable capability string plus detail.
 */
export function classifyMismatch(referenceDelta, candidateDelta) {
	const findings = describeMismatch(referenceDelta, candidateDelta);
	if (findings.length === 0) {
		return { class: "registration_mismatch", capability: "unknown_registration_surface", detail: null, findings };
	}
	// A name present on both add and remove of allTools on the reference side only
	// means the extension replaced a built-in tool definition and the candidate ignored it.
	const overridden = new Set();
	for (const finding of findings) {
		if (finding.surface !== "allTools") continue;
		if (finding.kind !== "missing_in_candidate") continue;
		for (const name of finding.names ?? []) overridden.add(`${finding.side}:${name}`);
	}
	const overrideNames = [...overridden]
		.filter((entry) => entry.startsWith("added:"))
		.map((entry) => entry.slice("added:".length))
		.filter((name) => overridden.has(`removed:${name}`))
		.sort();
	if (overrideNames.length > 0) {
		return {
			class: "registration_mismatch",
			capability: `builtin_tool_definition_override(${overrideNames.join(",")})`,
			detail: "upstream replaced these built-in tool definitions; the candidate left them unchanged",
			findings,
		};
	}
	const fieldFinding = findings.find((finding) => finding.field);
	if (fieldFinding) {
		return {
			class: "registration_mismatch",
			capability: `${SURFACE_CAPABILITY[fieldFinding.surface]}.${fieldFinding.field}`,
			detail: `${fieldFinding.name}: ${fieldFinding.kind}`,
			findings,
		};
	}
	const first = findings[0];
	const names = (first.names ?? [first.name]).filter(Boolean).slice(0, 6);
	return {
		class: "registration_mismatch",
		capability: `${SURFACE_CAPABILITY[first.surface]}(${names.join(",")})`,
		detail: `${first.surface}.${first.side} ${first.kind}`,
		findings,
	};
}

/**
 * Explain a runtime that flaked without any failed attempt: every probe
 * succeeded but the registrations they reported were not identical. That is a
 * nondeterminism defect in the runtime under test, and naming the unstable
 * surface makes it actionable.
 */
export function classifyInstability(variants = []) {
	const surfaces = { activeTools: new Set(), allTools: new Set(), commands: new Set(), rpcCommands: new Set() };
	const encode = (value) => JSON.stringify(value);
	for (const surface of Object.keys(surfaces)) {
		const seen = variants.map((variant) => {
			const snapshot = variant.snapshot ?? {};
			const values = surface === "rpcCommands" ? snapshot.rpcCommands : snapshot.registration?.[surface];
			return new Map((values ?? []).map((value) => [encode(value), value]));
		});
		if (seen.length < 2) continue;
		const union = new Set(seen.flatMap((entry) => [...entry.keys()]));
		for (const key of union) {
			if (seen.every((entry) => entry.has(key))) continue;
			surfaces[surface].add(toolKey(JSON.parse(key)));
		}
	}
	const unstable = Object.entries(surfaces).filter(([, names]) => names.size > 0);
	if (unstable.length === 0) {
		return {
			class: "flaky",
			capability: "registration_instability",
			detail: `${variants.length} distinct registration snapshots across attempts`,
			evidence: "",
		};
	}
	const parts = unstable.map(([surface, names]) => `${surface}:${[...names].sort().slice(0, 6).join(",")}`);
	return {
		class: "flaky",
		capability: `registration_instability(${parts.join(" ")})`,
		detail: `${variants.length} distinct registration snapshots across attempts; every attempt itself succeeded`,
		evidence: "",
		findings: unstable.map(([surface, names]) => ({ surface, kind: "unstable_between_attempts", names: [...names].sort() })),
	};
}

export function summarizeFailures(records) {
	const counts = new Map();
	for (const record of records) {
		const failure = record?.failure;
		if (!failure) continue;
		const key = `${failure.class} ${failure.capability ?? ""}`;
		const existing = counts.get(key);
		if (existing) {
			existing.count++;
			if (existing.packages.length < 25) existing.packages.push(record.package);
		} else {
			counts.set(key, { class: failure.class, capability: failure.capability, count: 1, packages: [record.package] });
		}
	}
	return [...counts.values()].sort((left, right) => right.count - left.count || (left.class < right.class ? -1 : 1));
}
