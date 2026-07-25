#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { appendFileSync, readFileSync, readdirSync, existsSync, writeFileSync, renameSync, mkdirSync } from "node:fs";
import { mkdir, readFile, readdir, rm, stat, symlink, writeFile } from "node:fs/promises";
import { networkInterfaces } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { classifyDiagnostics, classifyInstability, classifyMismatch, summarizeFailures } from "./taxonomy.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SELF = fileURLToPath(import.meta.url);
const TAXONOMY = path.join(HERE, "taxonomy.mjs");
const DEFAULT_CORPUS = path.join(HERE, "corpus.json");
const DEFAULT_OBSERVER = path.join(HERE, "observer.ts");
const OBSERVER_COMMAND = "__extension_matrix_probe";
const OBSERVER_MARKER = "PI_EXTENSION_MATRIX:";
const DIALOG_METHODS = new Set(["select", "confirm", "input", "editor"]);
const LOAD_ERROR = /(?:extension error \(|failed to load extension)/i;
const STATUSES = ["load_register_pass", "load_only_pass", "flaky", "unsupported", "infra_error"];

// A candidate runtime is one pigo binary pinned to one JavaScript engine. The
// reference is always upstream Pi on Node, because Node 24 is upstream's own
// documented requirement; comparing pigo-on-Bun against pi-on-Bun would compare
// two unknowns.
export const RUNTIME_SPECS = {
	pi: { role: "reference", engine: "node", binary: "pi" },
	"pigo-node": { role: "candidate", engine: "node", binary: "pigo" },
	"pigo-bun": { role: "candidate", engine: "bun", binary: "pigo" },
};
const DEFAULT_RUNTIMES = ["pi", "pigo-node"];
const LEGACY_ALIAS = { pigo: "pigo-node" };

// Sampling profiles. `perf` reproduces the historical 2 warm-up / 11 sample
// methodology needed for publishable timing. `compat` is the cheap profile used
// for the long tail, where the question is only "does it load and register the
// same", and 1 cold + 1 warm-up + 2 samples still detects disagreement.
export const PROFILES = {
	perf: { warmups: 2, samples: 11 },
	compat: { warmups: 1, samples: 2 },
	quick: { warmups: 0, samples: 1 },
};
const DEFAULT_TIER_PROFILES = { 1: "perf", 2: "compat", 3: "compat" };

function usage() {
	return `Usage: node matrix.mjs --packages <directory> --pigo <binary> [options]

Selection:
  --corpus <file>       Corpus manifest (default: corpus.json beside this file)
  --tier <n[,n]>        Only run these corpus tiers (1 = top 50, 2 = next 50, 3 = rest)
  --package <name[,..]> Only run these package names (repeatable)
  --only <a,b,...>      Package names or numeric ranks (repeatable, legacy)

Runtimes:
  --runtimes <list>     Comma list of ${Object.keys(RUNTIME_SPECS).join(", ")} (default: ${DEFAULT_RUNTIMES.join(",")})
  --bun <binary>        Bun executable used by the pigo-bun tier (default: bun on PATH)
  --node <binary>       Node executable used by the node tiers (default: inherited PATH)

Sampling:
  --profile <name>      Force one profile for every tier: ${Object.keys(PROFILES).join(", ")}
  --tier-profile <t=p>  Per-tier profile override (repeatable, e.g. --tier-profile 2=quick)
  --warmups <n>         Explicit warm-up count, overrides every profile
  --samples <n>         Explicit measured sample count, overrides every profile
  --timeout-ms <n>      Per-process deadline (default: 30000)
  --package-budget-ms   Per-package wall-clock budget before abandoning it (default: 420000)

Durability:
  --output <file>       Final report. Progress streams to <file>.jsonl and <file>.progress.json
  --resume              Reuse packages already present in <file>.jsonl with matching inputs
  --finalize            Do not probe; rebuild <file> from an existing <file>.jsonl
  --retain-registrations <diagnostic|all>
                        Aggregate size control (default: diagnostic; raw always in .jsonl)

Other:
  --observer <file>     Observer extension (default: observer.ts beside this file)
  --no-reap             Do not kill processes leaked by a package between packages
  --validate-only       Validate inputs without executing Pi or extensions
  --help                Show this help

The benchmark is intentionally sequential and interleaves every runtime within
every sample. Run it only inside the networkless hardened container documented
in README.md.`;
}

function positiveInteger(value, name, allowZero = false) {
	const parsed = Number(value);
	if (!Number.isInteger(parsed) || parsed < (allowZero ? 0 : 1)) throw new Error(`${name} must be an integer`);
	return parsed;
}

function parseArgs(argv) {
	const options = {
		corpus: DEFAULT_CORPUS,
		observer: DEFAULT_OBSERVER,
		packages: "",
		pigo: "",
		bun: "",
		node: "",
		output: "",
		only: [],
		packageFilter: [],
		tiers: [],
		runtimes: null,
		profile: "",
		tierProfiles: { ...DEFAULT_TIER_PROFILES },
		warmups: null,
		samples: null,
		timeoutMs: 30_000,
		packageBudgetMs: 420_000,
		retainRegistrations: "diagnostic",
		resume: false,
		finalize: false,
		reap: true,
		validateOnly: false,
	};
	for (let index = 0; index < argv.length; index++) {
		const argument = argv[index];
		if (argument === "--help" || argument === "-h") return { help: true };
		if (argument === "--validate-only") {
			options.validateOnly = true;
			continue;
		}
		if (argument === "--resume") {
			options.resume = true;
			continue;
		}
		if (argument === "--finalize") {
			options.finalize = true;
			continue;
		}
		if (argument === "--no-reap") {
			options.reap = false;
			continue;
		}
		if (argument === "--only" && index + 1 < argv.length) {
			options.only.push(...argv[++index].split(",").filter(Boolean));
			continue;
		}
		if (argument === "--package" && index + 1 < argv.length) {
			options.packageFilter.push(...argv[++index].split(",").filter(Boolean));
			continue;
		}
		if (argument === "--tier" && index + 1 < argv.length) {
			for (const tier of argv[++index].split(",").filter(Boolean)) options.tiers.push(positiveInteger(tier, "--tier"));
			continue;
		}
		if (argument === "--runtimes" && index + 1 < argv.length) {
			options.runtimes = argv[++index]
				.split(",")
				.filter(Boolean)
				.map((name) => LEGACY_ALIAS[name] ?? name);
			continue;
		}
		if (argument === "--profile" && index + 1 < argv.length) {
			options.profile = argv[++index];
			if (!PROFILES[options.profile]) throw new Error(`unknown profile: ${options.profile}`);
			continue;
		}
		if (argument === "--tier-profile" && index + 1 < argv.length) {
			const [tier, profile] = argv[++index].split("=");
			if (!PROFILES[profile]) throw new Error(`unknown profile: ${profile}`);
			options.tierProfiles[positiveInteger(tier, "--tier-profile")] = profile;
			continue;
		}
		if (argument === "--retain-registrations" && index + 1 < argv.length) {
			options.retainRegistrations = argv[++index];
			if (!["diagnostic", "all"].includes(options.retainRegistrations)) {
				throw new Error("--retain-registrations must be diagnostic or all");
			}
			continue;
		}
		const paths = new Set(["--corpus", "--observer", "--packages", "--pigo", "--output", "--bun", "--node"]);
		if (paths.has(argument) && index + 1 < argv.length) {
			options[argument.slice(2)] = path.resolve(argv[++index]);
			continue;
		}
		if (argument === "--warmups" && index + 1 < argv.length) {
			options.warmups = positiveInteger(argv[++index], argument, true);
			continue;
		}
		if (argument === "--samples" && index + 1 < argv.length) {
			options.samples = positiveInteger(argv[++index], argument);
			continue;
		}
		if (argument === "--timeout-ms" && index + 1 < argv.length) {
			options.timeoutMs = positiveInteger(argv[++index], argument);
			continue;
		}
		if (argument === "--package-budget-ms" && index + 1 < argv.length) {
			options.packageBudgetMs = positiveInteger(argv[++index], argument);
			continue;
		}
		throw new Error(`unknown or incomplete argument: ${argument}`);
	}
	options.runtimes ??= DEFAULT_RUNTIMES;
	for (const runtime of options.runtimes) {
		if (!RUNTIME_SPECS[runtime]) throw new Error(`unknown runtime: ${runtime}`);
	}
	if (!options.runtimes.includes("pi")) throw new Error("the pi reference runtime is required for comparison");
	if (options.runtimes.filter((runtime) => RUNTIME_SPECS[runtime].role === "candidate").length === 0) {
		throw new Error("at least one candidate runtime is required");
	}
	if (options.finalize && !options.output) throw new Error("--finalize requires --output");
	if (!options.validateOnly && !options.finalize && (!options.packages || !options.pigo)) {
		throw new Error("--packages and --pigo are required");
	}
	return options;
}

// ---------------------------------------------------------------------------
// Corpus
// ---------------------------------------------------------------------------

// A malformed entrypoint is a property of the published package, not of the
// corpus file, so it is flagged and skipped rather than thrown. Several real
// gallery packages declare entries like `../other-package/index.ts`; one of them
// must never be able to abort a 300-package run before the first probe.
export function inspectEntrypoint(entrypoint, packageName) {
	if (typeof entrypoint !== "string" || entrypoint.length === 0) {
		return { ok: false, class: "invalid_entrypoint", detail: `${packageName}: entrypoint ${JSON.stringify(entrypoint)} is not a path` };
	}
	if (path.isAbsolute(entrypoint)) {
		return { ok: false, class: "escaping_entrypoint", detail: `${packageName}: absolute entrypoint ${entrypoint}` };
	}
	const normalized = path.normalize(entrypoint);
	if (normalized === ".." || normalized.startsWith(`..${path.sep}`)) {
		return { ok: false, class: "escaping_entrypoint", detail: `${packageName}: entrypoint escapes its package root: ${entrypoint}` };
	}
	return { ok: true, value: normalized };
}

// The corpus agent owns corpus.json. `tier` is optional in the schema we code
// against: when it is absent the tier is derived from rank with the documented
// 50/50/rest split, so the same harness runs against the 44-package corpus and
// against the 200+ corpus without an edit here.
export function resolveTier(extension) {
	if (Number.isInteger(extension.tier) && extension.tier >= 1) return extension.tier;
	if (extension.rank <= 50) return 1;
	if (extension.rank <= 100) return 2;
	return 3;
}

export async function loadCorpus(filename, options) {
	const corpus = JSON.parse(await readFile(filename, "utf8"));
	if (corpus.schemaVersion !== 1 || !Array.isArray(corpus.extensions) || corpus.extensions.length === 0) {
		throw new Error(`${filename} must be an extension corpus v1`);
	}
	const ranks = new Set();
	const names = new Set();
	for (const extension of corpus.extensions) {
		if (!Number.isInteger(extension.rank) || extension.rank < 1 || typeof extension.package !== "string") {
			throw new Error(`${filename} contains an invalid extension record`);
		}
		if (ranks.has(extension.rank) || names.has(extension.package)) throw new Error(`${filename} contains duplicates`);
		ranks.add(extension.rank);
		names.add(extension.package);
		if (typeof extension.version !== "string" || !/^sha512-/.test(extension.integrity)) {
			throw new Error(`${extension.package} is missing its exact version or npm integrity`);
		}
		if (!Number.isInteger(extension.downloads?.monthly) || !Number.isInteger(extension.downloads?.weekly)) {
			throw new Error(`${extension.package} is missing download snapshots`);
		}
		if (!Array.isArray(extension.extensions) || extension.extensions.length === 0) {
			throw new Error(`${extension.package} has no pi.extensions entrypoints`);
		}
		const inspected = extension.extensions.map((entrypoint) => inspectEntrypoint(entrypoint, extension.package));
		const rejected = inspected.filter((entry) => !entry.ok);
		extension.extensions = inspected.filter((entry) => entry.ok).map((entry) => entry.value);
		if (rejected.length > 0) {
			extension.entrypointProblems = rejected.map(({ class: className, detail }) => ({ class: className, detail }));
		}
		if (extension.extensions.length === 0) {
			extension.unusable = {
				class: rejected[0].class,
				capability: `manifest_entrypoint(${extension.package})`,
				detail: rejected.map((entry) => entry.detail).join("; ").slice(0, 400),
			};
			extension.extensions = [];
		}
		extension.tier = resolveTier(extension);
	}
	corpus.extensions.sort((left, right) => left.rank - right.rank);
	corpus.totalCount = corpus.extensions.length;
	corpus.tierCounts = {};
	for (const extension of corpus.extensions) {
		corpus.tierCounts[extension.tier] = (corpus.tierCounts[extension.tier] ?? 0) + 1;
	}

	const only = options.only ?? [];
	const requestedPackages = options.packageFilter ?? [];
	const tiers = options.tiers ?? [];
	if (only.length === 0 && requestedPackages.length === 0 && tiers.length === 0) return corpus;

	let selected = corpus.extensions;
	if (tiers.length > 0) {
		const wanted = new Set(tiers);
		selected = selected.filter((extension) => wanted.has(extension.tier));
		if (selected.length === 0) throw new Error(`--tier matched no corpus entries: ${tiers.join(",")}`);
	}
	if (requestedPackages.length > 0) {
		const wanted = new Set(requestedPackages);
		selected = selected.filter((extension) => wanted.has(extension.package));
		for (const name of wanted) {
			if (!selected.some((extension) => extension.package === name)) throw new Error(`--package did not match ${name}`);
		}
	}
	if (only.length > 0) {
		const requested = new Set(only);
		selected = selected.filter((extension) => requested.has(extension.package) || requested.has(String(extension.rank)));
		for (const item of requested) {
			if (!selected.some((extension) => extension.package === item || String(extension.rank) === item)) {
				throw new Error(`--only did not match ${item}`);
			}
		}
	}
	return { ...corpus, extensions: selected, filtered: { tiers, packages: requestedPackages, only } };
}

function packageDirectory(packages, packageName) {
	return path.join(packages, "node_modules", ...packageName.split("/"));
}

async function resolveDirectoryEntrypoints(directory) {
	try {
		const manifest = JSON.parse(await readFile(path.join(directory, "package.json"), "utf8"));
		if (Array.isArray(manifest.pi?.extensions) && manifest.pi.extensions.length > 0) {
			const entries = [];
			for (const entrypoint of manifest.pi.extensions) {
				const resolved = path.resolve(directory, entrypoint);
				try {
					await stat(resolved);
					entries.push(resolved);
				} catch {}
			}
			if (entries.length > 0) return entries;
		}
	} catch {}

	for (const filename of ["index.ts", "index.js"]) {
		const entrypoint = path.join(directory, filename);
		try {
			if ((await stat(entrypoint)).isFile()) return [entrypoint];
		} catch {}
	}
	return null;
}

async function directoryEntrypoints(directory) {
	const rootEntries = await resolveDirectoryEntrypoints(directory);
	if (rootEntries) return rootEntries;

	const entries = [];
	for (const item of await readdir(directory, { withFileTypes: true })) {
		if (item.name.startsWith(".") || item.name === "node_modules") continue;
		const entrypoint = path.join(directory, item.name);
		if (item.isFile() && (item.name.endsWith(".ts") || item.name.endsWith(".js"))) {
			entries.push(entrypoint);
		} else if (item.isDirectory()) {
			const nested = await resolveDirectoryEntrypoints(entrypoint);
			if (nested) entries.push(...nested);
		}
	}
	return entries.sort(compareText);
}

async function extensionEntrypoints(packages, extension) {
	const root = packageDirectory(packages, extension.package);
	const entries = [];
	for (const declared of extension.extensions) {
		const entrypoint = path.resolve(root, declared);
		const info = await stat(entrypoint);
		if (info.isDirectory()) entries.push(...(await directoryEntrypoints(entrypoint)));
		else entries.push(entrypoint);
	}
	return [...new Set(entries)];
}

// ---------------------------------------------------------------------------
// Environment identity and isolation
// ---------------------------------------------------------------------------

function executableVersion(command, extraPath) {
	const env = extraPath ? { ...process.env, PATH: extraPath } : process.env;
	const result = spawnSync(command, ["--version"], { encoding: "utf8", timeout: 10_000, env });
	return {
		status: result.status,
		stdout: result.stdout?.trim() ?? "",
		stderr: result.stderr?.trim() ?? "",
		error: result.error?.message,
	};
}

function sha256(bytes) {
	return createHash("sha256").update(bytes).digest("hex");
}

async function fileIdentity(filename) {
	const bytes = await readFile(filename);
	return { sha256: sha256(bytes), bytes: bytes.length };
}

function inspectNetworkIsolation() {
	const external = Object.entries(networkInterfaces())
		.flatMap(([name, addresses]) => (addresses ?? []).map((address) => ({ name, ...address })))
		.filter((address) => !address.internal);
	return { isolated: external.length === 0, method: "os.networkInterfaces", external };
}

function whichInPath(name, pathValue) {
	for (const directory of (pathValue ?? "").split(path.delimiter).filter(Boolean)) {
		const candidate = path.join(directory, name);
		try {
			const info = readFileSync ? existsSync(candidate) : false;
			if (info) return candidate;
		} catch {}
	}
	return null;
}

/**
 * Snapshot of the container process table. Used to attribute EAGAIN/SIGABRT
 * style failures to leaked processes from an earlier package instead of
 * recording them as the current package's verdict.
 */
export function processCensus() {
	if (!existsSync("/proc")) return { available: false };
	let entries;
	try {
		entries = readdirSync("/proc").filter((name) => /^\d+$/.test(name));
	} catch {
		return { available: false };
	}
	const orphans = [];
	let zombies = 0;
	for (const entry of entries) {
		const pid = Number(entry);
		let stat;
		try {
			stat = readFileSync(`/proc/${entry}/stat`, "utf8");
		} catch {
			continue;
		}
		const close = stat.lastIndexOf(")");
		const fields = stat.slice(close + 2).split(" ");
		const state = fields[0];
		const parent = Number(fields[1]);
		if (state === "Z") zombies++;
		if (parent === 1 && pid !== 1 && pid !== process.pid && pid !== process.ppid) {
			orphans.push({ pid, state, comm: stat.slice(stat.indexOf("(") + 1, close) });
		}
	}
	return { available: true, total: entries.length, zombies, orphans };
}

function reapOrphans(census) {
	const killed = [];
	for (const orphan of census.orphans ?? []) {
		if (orphan.state === "Z") continue;
		try {
			process.kill(orphan.pid, "SIGKILL");
			killed.push(orphan);
		} catch {}
	}
	return killed;
}

// ---------------------------------------------------------------------------
// Statistics
// ---------------------------------------------------------------------------

function percentile(sorted, fraction) {
	if (sorted.length === 0) return null;
	return sorted[Math.max(0, Math.ceil(sorted.length * fraction) - 1)];
}

function median(values) {
	if (values.length === 0) return null;
	const sorted = [...values].sort((left, right) => left - right);
	const middle = Math.floor(sorted.length / 2);
	return sorted.length % 2 === 1 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2;
}

function summarize(values) {
	if (values.length === 0) return { n: 0, medianMs: null, p90Ms: null, madMs: null, noisy: null };
	const sorted = [...values].sort((left, right) => left - right);
	const center = median(sorted);
	const deviation = sorted.map((value) => Math.abs(value - center));
	const mad = median(deviation);
	return {
		n: sorted.length,
		medianMs: Number(center.toFixed(3)),
		p90Ms: Number(percentile(sorted, 0.9).toFixed(3)),
		madMs: Number(mad.toFixed(3)),
		noisy: center === 0 ? mad > 0 : mad / center > 0.1,
	};
}

// ---------------------------------------------------------------------------
// Probe
// ---------------------------------------------------------------------------

function writeLine(child, value) {
	if (!child.stdin.destroyed) child.stdin.write(JSON.stringify(value) + "\n");
}

function normalizeError(error) {
	return error instanceof Error ? error.message : String(error);
}

function signalProcessGroup(child, signal) {
	try {
		process.kill(-child.pid, signal);
	} catch {
		try {
			child.kill(signal);
		} catch {}
	}
}

async function stopChild(child) {
	if (child.exitCode === null && child.signalCode === null) {
		child.stdin.end();
		await Promise.race([
			new Promise((resolve) => child.once("exit", resolve)),
			new Promise((resolve) => setTimeout(resolve, 300)),
		]);
	}
	if (child.exitCode === null && child.signalCode === null) {
		signalProcessGroup(child, "SIGTERM");
		await Promise.race([
			new Promise((resolve) => child.once("exit", resolve)),
			new Promise((resolve) => setTimeout(resolve, 300)),
		]);
	}
	const exited = child.exitCode === null && child.signalCode === null ? new Promise((resolve) => child.once("exit", resolve)) : null;
	signalProcessGroup(child, "SIGKILL");
	if (exited) await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 300))]);
}

async function runProbe(runtime, extensionPaths, options) {
	const runRoot = path.join("/work", `matrix-${process.pid}`);
	const home = path.join(runRoot, "home");
	const cwd = path.join(runRoot, "project");
	const agentDir = path.join(runRoot, "agent");
	await rm(runRoot, { recursive: true, force: true });
	await Promise.all([
		mkdir(home, { recursive: true }),
		mkdir(cwd, { recursive: true }),
		mkdir(agentDir, { recursive: true }),
		mkdir(path.join(runRoot, "enginebin"), { recursive: true }),
	]);
	await writeFile(path.join(agentDir, "settings.json"), '{"compaction":{"enabled":false},"retry":{"enabled":false}}\n');
	const observerPath = path.join(runRoot, "observer.ts");
	await symlink(options.observer, observerPath);
	await symlink(path.join(options.packages, "node_modules"), path.join(runRoot, "node_modules"));
	extensionPaths = extensionPaths.map((extensionPath) => (extensionPath === options.observer ? observerPath : extensionPath));

	// Engine forcing. pigo's DiscoverRuntime (codingagent/extensions/host/runtime.go:32)
	// resolves `node` from PATH first and only falls back to `bun`, and there is no
	// product env override. The bun tier therefore gets a PATH whose only engine is
	// a bun symlink; `node` must not be resolvable at all, and the observer proves
	// after the fact which engine actually evaluated the extension.
	const engineBin = path.join(runRoot, "enginebin");
	let probePath;
	if (runtime.engine === "bun") {
		await symlink(runtime.enginePath, path.join(engineBin, "bun"));
		probePath = `${engineBin}${path.delimiter}${path.join(options.packages, "node_modules", ".bin")}`;
		const leaked = whichInPath("node", probePath);
		if (leaked) {
			return {
				success: false,
				error: `bun tier PATH still resolves node at ${leaked}`,
				timedOut: false,
				runtimeNotForced: true,
				loadError: null,
				startupMs: null,
				commandMs: null,
				getCommands: null,
				observation: null,
				uiRequestCount: 0,
				stderr: "",
				stdoutRemainder: "",
			};
		}
	} else if (runtime.enginePath) {
		await symlink(runtime.enginePath, path.join(engineBin, "node"));
		probePath = `${engineBin}${path.delimiter}${path.join(options.packages, "node_modules", ".bin")}${path.delimiter}${process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin"}`;
	} else {
		probePath = `${path.join(options.packages, "node_modules", ".bin")}${path.delimiter}${process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin"}`;
	}

	const args = [
		"--mode",
		"rpc",
		"--no-session",
		"--provider",
		"openai",
		"--model",
		"gpt-4o-mini",
		"--api-key",
		"extension-matrix-offline-key",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-context-files",
		"--no-themes",
		"--no-approve",
		"--offline",
	];
	for (const extensionPath of extensionPaths) args.push("-e", extensionPath);

	const startedAt = performance.now();
	const child = spawn(runtime.executable, args, {
		cwd,
		detached: true,
		stdio: ["pipe", "pipe", "pipe"],
		env: {
			HOME: home,
			PATH: probePath,
			PI_CODING_AGENT_DIR: agentDir,
			NO_COLOR: "1",
			TERM: "dumb",
			TMPDIR: runRoot,
		},
	});
	child.stdin.on("error", () => {});

	let stderr = "";
	let settled = false;
	let commandSentAt = null;
	let startupMs = null;
	let commandMs = null;
	let getCommands = null;
	let observation = null;
	let promptResponse = false;
	let uiRequestCount = 0;
	let stdoutBuffer = "";
	// Everything the runtime writes to stderr after the probe has been answered
	// belongs to teardown, not to loading. Counting a shutdown-phase message as a
	// load error turns a fully successful attempt into a false flake.
	let stderrAtProbe = null;

	const completion = new Promise((resolve) => {
		const finish = (result) => {
			if (settled) return;
			settled = true;
			resolve(result);
		};
		const deadline = setTimeout(() => finish({ error: `timeout after ${options.timeoutMs}ms`, timedOut: true }), options.timeoutMs);
		deadline.unref?.();

		child.once("error", (error) => finish({ error: normalizeError(error), timedOut: false }));
		child.once("exit", (code, signal) => {
			if (!settled && !(observation && promptResponse)) {
				finish({ error: `process exited before probe (${signal ?? `exit ${code}`})`, timedOut: false });
			}
		});

		child.stdout.on("data", (chunk) => {
			const text = chunk.toString("utf8");
			stdoutBuffer += text;
			for (;;) {
				const newline = stdoutBuffer.indexOf("\n");
				if (newline < 0) break;
				const raw = stdoutBuffer.slice(0, newline).replace(/\r$/, "");
				stdoutBuffer = stdoutBuffer.slice(newline + 1);
				if (!raw) continue;
				let message;
				try {
					message = JSON.parse(raw);
				} catch {
					continue;
				}
				if (message.type === "extension_ui_request") {
					uiRequestCount++;
					if (DIALOG_METHODS.has(message.method)) {
						writeLine(child, { type: "extension_ui_response", id: message.id, cancelled: true });
					}
					if (message.method === "notify" && typeof message.message === "string" && message.message.startsWith(OBSERVER_MARKER)) {
						try {
							observation = JSON.parse(message.message.slice(OBSERVER_MARKER.length));
						} catch (error) {
							finish({ error: `invalid observer payload: ${normalizeError(error)}`, timedOut: false });
						}
					}
				}
				if (message.type === "response" && message.id === "commands") {
					if (!message.success) {
						finish({ error: `get_commands failed: ${message.error ?? "unknown error"}`, timedOut: false });
						continue;
					}
					startupMs = performance.now() - startedAt;
					getCommands = message.data?.commands ?? [];
					commandSentAt = performance.now();
					writeLine(child, { id: "probe", type: "prompt", message: `/${OBSERVER_COMMAND}` });
				}
				if (message.type === "response" && message.id === "probe") {
					if (!message.success) {
						finish({ error: `observer command failed: ${message.error ?? "unknown error"}`, timedOut: false });
						continue;
					}
					promptResponse = true;
				}
				if (observation && promptResponse && commandSentAt !== null) {
					commandMs = performance.now() - commandSentAt;
					stderrAtProbe = stderr.length;
					clearTimeout(deadline);
					finish({ error: null, timedOut: false });
				}
			}
		});
	});

	child.stderr.on("data", (chunk) => {
		stderr += chunk.toString("utf8");
	});
	writeLine(child, { id: "commands", type: "get_commands" });
	const completionResult = await completion;
	await stopChild(child);
	await rm(runRoot, { recursive: true, force: true });

	// End-to-end proof of which engine evaluated the extension: the observer runs
	// inside the extension host process, so this is the host's own self-report.
	// Split stderr at the moment the probe completed. When it never completed the
	// whole stream is load phase, which is what we want for a failed attempt.
	const loadPhaseStderr = stderrAtProbe === null ? stderr : stderr.slice(0, stderrAtProbe);
	const shutdownPhaseStderr = stderrAtProbe === null ? "" : stderr.slice(stderrAtProbe);
	const host = observation?.host ?? null;
	const observedEngine = host?.engine ?? null;
	const runtimeNotForced = observedEngine !== null && observedEngine !== runtime.engine;
	return {
		success: !completionResult.error && !runtimeNotForced,
		error: runtimeNotForced ? `expected ${runtime.engine} host, observed ${observedEngine}` : completionResult.error,
		timedOut: completionResult.timedOut,
		runtimeNotForced,
		observedEngine,
		host,
		loadError: LOAD_ERROR.test(loadPhaseStderr) ? loadPhaseStderr.trim() : null,
		shutdownStderr: shutdownPhaseStderr.trim() || null,
		startupMs,
		commandMs,
		getCommands,
		observation,
		uiRequestCount,
		stderr: stderr.trim(),
		stdoutRemainder: stdoutBuffer.trim(),
	};
}

function attemptSucceeded(attempt) {
	return attempt.success && !attempt.loadError;
}

function registrationSnapshot(attempt) {
	if (!attempt.observation) return null;
	const { host, ...registration } = attempt.observation;
	return {
		registration,
		rpcCommands: normalizeCommands(attempt.getCommands),
	};
}

function runtimeSummary(attempts, context = {}) {
	const measured = attempts.filter((attempt) => !attempt.warmup);
	const successful = attempts.filter(attemptSucceeded);
	const diagnosticCounts = new Map();
	for (const attempt of attempts.filter((item) => !attemptSucceeded(item))) {
		const diagnostic = {
			error: attempt.error,
			timedOut: attempt.timedOut,
			runtimeNotForced: attempt.runtimeNotForced ?? false,
			observedEngine: attempt.observedEngine ?? null,
			loadError: attempt.loadError,
			stderr: attempt.stderr,
			stdoutRemainder: attempt.stdoutRemainder,
		};
		const encoded = JSON.stringify(diagnostic);
		const existing = diagnosticCounts.get(encoded);
		if (existing) {
			existing.count++;
			existing.phases[attempt.phase]++;
		} else {
			diagnosticCounts.set(encoded, {
				count: 1,
				phases: { cold: 0, warmup: 0, sample: 0, [attempt.phase]: 1 },
				...diagnostic,
			});
		}
	}
	// Registration variants are keyed by digest so a 200-package run does not hold
	// one full JSON string per attempt in memory.
	const registrationCounts = new Map();
	for (const attempt of successful) {
		const snapshot = registrationSnapshot(attempt);
		const key = sha256(JSON.stringify(snapshot));
		const existing = registrationCounts.get(key);
		if (existing) existing.count++;
		else registrationCounts.set(key, { count: 1, snapshot });
	}
	const registrationVariants = [...registrationCounts.values()];
	const registrationStable = registrationVariants.length <= 1;
	const allSucceeded = attempts.length > 0 && successful.length === attempts.length;
	const state = allSucceeded && registrationStable ? "stable" : successful.length === 0 ? "failed" : "flaky";
	const compactAttempts = attempts.map((attempt) => {
		return {
			phase: attempt.phase,
			warmup: attempt.warmup,
			sample: attempt.sample,
			success: attemptSucceeded(attempt),
			startupMs: attempt.startupMs === null ? null : Number(attempt.startupMs.toFixed(3)),
			commandMs: attempt.commandMs === null ? null : Number(attempt.commandMs.toFixed(3)),
			uiRequestCount: attempt.uiRequestCount,
		};
	});
	const representative = registrationVariants[0]?.snapshot ?? null;
	const hostIdentities = [...new Set(successful.map((attempt) => JSON.stringify(attempt.host ?? null)))].map((value) =>
		JSON.parse(value),
	);
	return {
		state,
		ok: state === "stable",
		engine: context.engine ?? null,
		engineVerified: hostIdentities.length === 1 && hostIdentities[0]?.engine === context.engine,
		hostIdentity: hostIdentities.length === 1 ? hostIdentities[0] : hostIdentities,
		budgetExceeded: context.budgetExceeded ?? false,
		failures: {
			cold: attempts.filter((attempt) => attempt.phase === "cold" && !attemptSucceeded(attempt)).length,
			warmup: attempts.filter((attempt) => attempt.phase === "warmup" && !attemptSucceeded(attempt)).length,
			sample: attempts.filter((attempt) => attempt.phase === "sample" && !attemptSucceeded(attempt)).length,
			total: attempts.length - successful.length,
		},
		startup: summarize(measured.filter(attemptSucceeded).map((attempt) => attempt.startupMs)),
		command: summarize(measured.filter(attemptSucceeded).map((attempt) => attempt.commandMs)),
		attempts: compactAttempts,
		diagnostics: [...diagnosticCounts.values()],
		registrationStable,
		registrationVariantCount: registrationVariants.length,
		registrationVariants: registrationVariants.length > 1 ? registrationVariants : [],
		registration: representative?.registration ?? null,
		rpcCommands: representative?.rpcCommands ?? null,
	};
}

function normalizeCommands(commands) {
	if (!Array.isArray(commands)) return null;
	return commands
		.map((command) => ({
			name: typeof command?.name === "string" ? command.name : "",
			description: typeof command?.description === "string" ? command.description : "",
		}))
		.sort((left, right) => compareText(left.name, right.name) || compareText(left.description, right.description));
}

function compareText(left, right) {
	return left < right ? -1 : left > right ? 1 : 0;
}

function stringDelta(current = [], baseline = []) {
	const before = new Set(baseline ?? []);
	const after = new Set(current ?? []);
	return {
		added: [...after].filter((value) => !before.has(value)).sort(),
		removed: [...before].filter((value) => !after.has(value)).sort(),
	};
}

function structuredDelta(current = [], baseline = []) {
	const key = (value) => JSON.stringify(value);
	const before = new Map((baseline ?? []).map((value) => [key(value), value]));
	const after = new Map((current ?? []).map((value) => [key(value), value]));
	const sort = (values) => values.sort((left, right) => compareText(key(left), key(right)));
	return {
		added: sort([...after].filter(([item]) => !before.has(item)).map(([, value]) => value)),
		removed: sort([...before].filter(([item]) => !after.has(item)).map(([, value]) => value)),
	};
}

function registrationDelta(current, baseline) {
	if (!current?.registration || !baseline?.registration) return null;
	return {
		activeTools: stringDelta(current.registration.activeTools, baseline.registration.activeTools),
		allTools: structuredDelta(current.registration.allTools, baseline.registration.allTools),
		commands: commandDelta(current.registration.commands, baseline.registration.commands),
		rpcCommands: commandDelta(current.rpcCommands, baseline.rpcCommands),
	};
}

function commandDelta(current = [], baseline = []) {
	const key = (command) => `${command.name}\u0000${command.description}`;
	const before = new Map((baseline ?? []).map((command) => [key(command), command]));
	const after = new Map((current ?? []).map((command) => [key(command), command]));
	const sort = (commands) =>
		commands.sort((left, right) => compareText(left.name, right.name) || compareText(left.description, right.description));
	return {
		added: sort([...after].filter(([item]) => !before.has(item)).map(([, command]) => command)),
		removed: sort([...before].filter(([item]) => !after.has(item)).map(([, command]) => command)),
	};
}

function registrationDifference(reference, candidate, baselines, referenceId, candidateId) {
	const referenceDelta = registrationDelta(reference, baselines[referenceId]);
	const candidateDelta = registrationDelta(candidate, baselines[candidateId]);
	if (JSON.stringify(referenceDelta) === JSON.stringify(candidateDelta)) {
		return { difference: null, referenceDelta, candidateDelta };
	}
	return {
		difference: { pi: referenceDelta, pigo: candidateDelta },
		referenceDelta,
		candidateDelta,
	};
}

function registrationHasChanges(delta) {
	return Object.values(delta ?? {}).some((change) => (change.added?.length ?? 0) > 0 || (change.removed?.length ?? 0) > 0);
}

function subtract(value, baseline) {
	if (value === null || baseline === null) return null;
	return Number((value - baseline).toFixed(3));
}

function ratio(numerator, denominator) {
	if (numerator === null || denominator === null || numerator <= 0 || denominator <= 0) return null;
	return Number((numerator / denominator).toFixed(3));
}

function measuredRatio(numerator, denominator, noisy) {
	if (numerator === null || denominator === null) return { ratio: null, quality: "unavailable" };
	if (noisy) return { ratio: null, quality: "noisy" };
	if (numerator <= 0 || denominator <= 0) return { ratio: null, quality: "below_resolution" };
	return { ratio: ratio(numerator, denominator), quality: "ok" };
}

// ---------------------------------------------------------------------------
// Benchmark driver
// ---------------------------------------------------------------------------

async function benchmark(extension, runtimes, options, sampling) {
	const extensionPaths = [options.observer];
	if (extension) {
		extensionPaths.push(...(await extensionEntrypoints(options.packages, extension)));
	}
	const ids = Object.keys(runtimes);
	const attempts = Object.fromEntries(ids.map((id) => [id, []]));
	const total = sampling.warmups + sampling.samples;
	const startedAt = Date.now();
	let budgetExceeded = false;
	for (let index = 0; index < total; index++) {
		if (extension && Date.now() - startedAt > options.packageBudgetMs) {
			budgetExceeded = true;
			break;
		}
		// Rotate first position so no runtime is systematically advantaged.
		const order = ids.map((_, position) => ids[(position + index) % ids.length]);
		for (const id of order) {
			const attempt = await runProbe(runtimes[id], extensionPaths, options);
			attempt.warmup = index < sampling.warmups;
			attempt.phase = index === 0 ? "cold" : attempt.warmup ? "warmup" : "sample";
			attempt.sample = index < sampling.warmups ? index + 1 : index - sampling.warmups + 1;
			attempts[id].push(attempt);
		}
	}
	return Object.fromEntries(
		ids.map((id) => [id, runtimeSummary(attempts[id], { engine: runtimes[id].engine, budgetExceeded })]),
	);
}

function classifyCandidate(reference, candidate, baselines, referenceId, candidateId, context) {
	const engineContext = { expectedEngine: RUNTIME_SPECS[candidateId]?.engine, budgetExceeded: candidate.budgetExceeded };
	if (baselines[referenceId].state !== "stable" || baselines[candidateId].state !== "stable") {
		return { status: "infra_error", reason: "observer_baseline_unstable", upstreamSupported: null, difference: null, deltas: null, failure: null };
	}
	if (reference.state === "flaky") {
		return {
			status: "flaky",
			reason: "upstream_flaky",
			upstreamSupported: false,
			difference: null,
			deltas: null,
			failure: { ...classifyDiagnostics(reference.diagnostics, engineContext), class: "flaky", side: "upstream" },
		};
	}
	if (reference.state === "failed") {
		return {
			status: "unsupported",
			reason: "upstream_load_failure",
			upstreamSupported: false,
			difference: null,
			deltas: null,
			failure: { ...classifyDiagnostics(reference.diagnostics, engineContext), side: "upstream" },
		};
	}
	if (candidate.state === "flaky") {
		// Two very different flakes: attempts that failed, and attempts that all
		// succeeded but disagreed about what got registered.
		const underlying =
			candidate.failures.total === 0
				? classifyInstability(candidate.registrationVariants)
				: classifyDiagnostics(candidate.diagnostics, engineContext);
		return {
			status: "flaky",
			reason: underlying.class === "resource_exhaustion" ? "resource_exhaustion" : "pigo_flaky",
			upstreamSupported: true,
			difference: null,
			deltas: null,
			failure: { ...underlying, flakeClass: underlying.class, class: underlying.class === "resource_exhaustion" ? "resource_exhaustion" : "flaky", side: "candidate" },
		};
	}
	if (candidate.state === "failed") {
		const underlying = classifyDiagnostics(candidate.diagnostics, engineContext);
		return {
			status: "unsupported",
			reason: candidate.budgetExceeded ? "package_budget_exceeded" : "pigo_load_failure",
			upstreamSupported: true,
			difference: null,
			deltas: null,
			failure: { ...underlying, side: "candidate" },
		};
	}
	const compared = registrationDifference(reference, candidate, baselines, referenceId, candidateId);
	if (compared.difference) {
		return {
			status: "unsupported",
			reason: "registration_mismatch",
			upstreamSupported: true,
			difference: compared.difference,
			deltas: { pi: compared.referenceDelta, pigo: compared.candidateDelta },
			failure: { ...classifyMismatch(compared.referenceDelta, compared.candidateDelta), side: "candidate" },
		};
	}
	const status = registrationHasChanges(compared.referenceDelta) ? "load_register_pass" : "load_only_pass";
	return {
		status,
		reason: status,
		upstreamSupported: true,
		difference: null,
		deltas: { pi: compared.referenceDelta, pigo: compared.candidateDelta },
		failure: null,
	};
}

function performanceComparison(reference, candidate, baselines, referenceId, candidateId) {
	if (
		baselines[referenceId].state !== "stable" ||
		baselines[candidateId].state !== "stable" ||
		reference.state !== "stable" ||
		candidate.state !== "stable"
	) {
		return { available: false, reason: "requires stable successful probes in both runtimes" };
	}
	const piStartup = reference.startup.medianMs;
	const pigoStartup = candidate.startup.medianMs;
	const piNet = subtract(piStartup, baselines[referenceId].startup.medianMs);
	const pigoNet = subtract(pigoStartup, baselines[candidateId].startup.medianMs);
	return {
		available: true,
		startup: {
			metric: "process_spawn_to_get_commands",
			...measuredRatio(pigoStartup, piStartup, reference.startup.noisy || candidate.startup.noisy),
		},
		baselineSubtractedLoad: {
			metric: "global_observer_baseline_subtracted_startup",
			piMs: piNet,
			pigoMs: pigoNet,
			...measuredRatio(
				pigoNet,
				piNet,
				reference.startup.noisy ||
					candidate.startup.noisy ||
					baselines[referenceId].startup.noisy ||
					baselines[candidateId].startup.noisy,
			),
		},
		observerRPC: {
			metric: "observer_command_rpc_round_trip_not_extension_work",
			...measuredRatio(candidate.command.medianMs, reference.command.medianMs, reference.command.noisy || candidate.command.noisy),
		},
	};
}

function percentage(numerator, denominator) {
	return denominator === 0 ? null : Number(((numerator / denominator) * 100).toFixed(1));
}

function emptyCounts() {
	return Object.fromEntries(STATUSES.map((status) => [status, 0]));
}

export function aggregateSummary(results, corpusTotal, baselines, candidates, primary) {
	const counts = emptyCounts();
	const reasons = {};
	const byRuntime = Object.fromEntries(candidates.map((id) => [id, { counts: emptyCounts(), reasons: {}, failureClasses: {} }]));
	const byTier = {};
	const divergence = { none: 0, node_only_failure: 0, bun_only_failure: 0, both_fail: 0, disagree: 0 };
	const failureRecords = [];
	for (const result of results) {
		counts[result.status]++;
		reasons[result.reason] = (reasons[result.reason] ?? 0) + 1;
		const tier = result.extension?.tier ?? resolveTier(result.extension ?? { rank: 1 });
		byTier[tier] ??= { counts: emptyCounts(), total: 0 };
		byTier[tier].total++;
		byTier[tier].counts[result.status]++;
		for (const id of candidates) {
			const entry = result.candidates?.[id];
			if (!entry) continue;
			byRuntime[id].counts[entry.status]++;
			byRuntime[id].reasons[entry.reason] = (byRuntime[id].reasons[entry.reason] ?? 0) + 1;
			if (entry.failure) {
				byRuntime[id].failureClasses[entry.failure.class] = (byRuntime[id].failureClasses[entry.failure.class] ?? 0) + 1;
				failureRecords.push({ package: result.extension.package, runtime: id, failure: entry.failure });
			}
		}
		if (result.crossRuntime?.divergence) divergence[result.crossRuntime.divergence]++;
	}
	const completeCorpus = results.length === corpusTotal;
	const valid = candidates.concat(["pi"]).every((id) => baselines[id]?.state === "stable");
	const loadRegisterPass = counts.load_register_pass;
	const loadOnlyPass = counts.load_only_pass;
	const loadCompatible = loadRegisterPass + loadOnlyPass;
	const failureTaxonomy = summarizeFailures(failureRecords.map((record) => ({ package: `${record.package} [${record.runtime}]`, failure: record.failure })));
	if (!valid) {
		return {
			valid: false,
			reason: "observer baseline is not stable in every runtime",
			completeCorpus,
			counts,
			reasons,
			byRuntime,
			byTier,
			divergence,
			failureTaxonomy,
			allCorpus: { total: corpusTotal, tested: results.length, loadCompatible: null, loadCompatiblePercent: null },
			parity: null,
		};
	}
	const upstreamSupported = results.filter((result) => result.upstreamSupported === true).length;
	return {
		valid: true,
		completeCorpus,
		primaryCandidate: primary,
		counts,
		reasons,
		byRuntime,
		byTier,
		divergence,
		failureTaxonomy,
		allCorpus: {
			total: corpusTotal,
			tested: results.length,
			loadRegisterPass,
			loadOnlyPass,
			loadCompatible,
			loadCompatiblePercent: completeCorpus ? percentage(loadCompatible, corpusTotal) : null,
			loadRegisterPercent: completeCorpus ? percentage(loadRegisterPass, corpusTotal) : null,
		},
		parity: {
			scope: completeCorpus ? "all upstream-supported corpus packages" : "tested upstream-supported packages only",
			upstreamSupported,
			loadRegisterPass,
			loadOnlyPass,
			loadCompatible,
			loadCompatiblePercent: percentage(loadCompatible, upstreamSupported),
			loadRegisterPercent: percentage(loadRegisterPass, upstreamSupported),
		},
	};
}

function divergenceOf(candidates) {
	const node = candidates["pigo-node"];
	const bun = candidates["pigo-bun"];
	if (!node || !bun) return null;
	const passing = (entry) => entry.status === "load_register_pass" || entry.status === "load_only_pass";
	if (passing(node) && passing(bun)) return node.status === bun.status ? "none" : "disagree";
	if (passing(node) && !passing(bun)) return "bun_only_failure";
	if (!passing(node) && passing(bun)) return "node_only_failure";
	return "both_fail";
}

// ---------------------------------------------------------------------------
// Durable output
// ---------------------------------------------------------------------------

class ResultStream {
	constructor(outputFile, header) {
		this.enabled = Boolean(outputFile);
		if (!this.enabled) return;
		this.jsonl = `${outputFile}.jsonl`;
		this.progress = `${outputFile}.progress.json`;
		this.header = header;
		mkdirSync(path.dirname(outputFile), { recursive: true });
	}

	fingerprint() {
		return sha256(JSON.stringify(this.header ?? {}));
	}

	start(resume) {
		if (!this.enabled) return new Map();
		const reusable = new Map();
		if (resume && existsSync(this.jsonl)) {
			const lines = readFileSync(this.jsonl, "utf8").split("\n").filter(Boolean);
			let matching = false;
			for (const line of lines) {
				let record;
				try {
					record = JSON.parse(line);
				} catch {
					continue; // a torn final line from a killed run
				}
				if (record.type === "header") {
					matching = record.fingerprint === this.fingerprint();
					continue;
				}
				if (record.type === "package" && matching) reusable.set(record.result.extension.package, record.result);
			}
			if (reusable.size > 0) return reusable;
		}
		writeFileSync(this.jsonl, `${JSON.stringify({ type: "header", fingerprint: this.fingerprint(), ...this.header })}\n`);
		return reusable;
	}

	append(result) {
		if (!this.enabled) return;
		appendFileSync(this.jsonl, `${JSON.stringify({ type: "package", result })}\n`);
	}

	writeProgress(state) {
		if (!this.enabled) return;
		const temporary = `${this.progress}.tmp`;
		writeFileSync(temporary, `${JSON.stringify(state, null, 2)}\n`);
		renameSync(temporary, this.progress);
	}
}

async function writeOutput(filename, report) {
	const encoded = JSON.stringify(report, null, 2) + "\n";
	if (!filename) {
		process.stdout.write(encoded);
		return;
	}
	await mkdir(path.dirname(filename), { recursive: true });
	const temporary = `${filename}.tmp-${process.pid}`;
	await writeFile(temporary, encoded);
	await import("node:fs/promises").then(({ rename }) => rename(temporary, filename));
}

function stripHeavyRegistrations(record, mode) {
	if (mode === "all") return record;
	const keepDetail = record.status !== "load_register_pass" && record.status !== "load_only_pass";
	if (keepDetail) return record;
	for (const key of Object.keys(record.runtimeSummaries ?? {})) {
		const summary = record.runtimeSummaries[key];
		summary.registration = null;
		summary.rpcCommands = null;
	}
	for (const candidate of Object.values(record.candidates ?? {})) {
		candidate.deltas = null;
	}
	return record;
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

function samplingFor(extension, options) {
	if (options.warmups !== null || options.samples !== null) {
		const profile = PROFILES[options.profile || "perf"];
		return {
			profile: options.profile || "explicit",
			warmups: options.warmups ?? profile.warmups,
			samples: options.samples ?? profile.samples,
		};
	}
	const name = options.profile || options.tierProfiles[extension?.tier ?? 1] || "compat";
	return { profile: name, ...PROFILES[name] };
}

/**
 * A package that could not be probed at all: nothing was installed, or its
 * manifest never resolves to a loadable entrypoint. Recorded with the same
 * shape as a measured package so downstream consumers need no special case.
 */
export function skippedRecord(extension, candidates, failure, startedAt) {
	const candidateEntry = {
		status: "unsupported",
		reason: failure.class,
		upstreamSupported: null,
		failure,
		deltas: null,
		difference: null,
		performance: { available: false, reason: failure.class },
	};
	return {
		extension,
		tier: extension.tier,
		status: "unsupported",
		reason: failure.class,
		upstreamSupported: null,
		failure,
		registrationDeltas: null,
		registrationDifference: null,
		candidates: Object.fromEntries(candidates.map((id) => [id, { ...candidateEntry }])),
		crossRuntime: {
			node: candidates.includes("pigo-node") ? "unsupported" : null,
			bun: candidates.includes("pigo-bun") ? "unsupported" : null,
			divergence: "both_fail",
		},
		runtimeSummaries: {},
		pi: null,
		pigo: null,
		performance: { available: false, reason: failure.class },
		elapsedMs: Date.now() - startedAt,
	};
}

async function main() {
	const options = parseArgs(process.argv.slice(2));
	if (options.help) {
		process.stdout.write(usage() + "\n");
		return;
	}
	const corpus = await loadCorpus(options.corpus, options);
	if (options.validateOnly) {
		const tiers = Object.entries(corpus.tierCounts)
			.map(([tier, count]) => `tier ${tier}: ${count}`)
			.join(", ");
		process.stdout.write(`valid corpus: ${corpus.extensions.length} extension packages (${tiers})\n`);
		return;
	}

	const pi = path.join(options.packages, "node_modules", ".bin", "pi");
	const runtimes = {};
	for (const id of options.runtimes) {
		const spec = RUNTIME_SPECS[id];
		runtimes[id] = {
			id,
			engine: spec.engine,
			executable: spec.binary === "pi" ? pi : options.pigo,
			enginePath: spec.engine === "bun" ? options.bun || whichInPath("bun", process.env.PATH) : options.node || null,
		};
		if (spec.engine === "bun" && !runtimes[id].enginePath) {
			throw new Error("the pigo-bun tier needs --bun <binary> or bun on PATH");
		}
	}
	const candidates = options.runtimes.filter((id) => RUNTIME_SPECS[id].role === "candidate");
	const primary = candidates.includes("pigo-node") ? "pigo-node" : candidates[0];

	const networkNamespaceGuard = inspectNetworkIsolation();
	if (!networkNamespaceGuard.isolated) {
		throw new Error("matrix requires a network-isolated namespace; use the documented --network none container");
	}
	const [harnessIdentity, taxonomyIdentity, corpusIdentity, observerIdentity, packageLockIdentity, piIdentity, pigoIdentity] =
		await Promise.all([
			fileIdentity(SELF),
			fileIdentity(TAXONOMY),
			fileIdentity(options.corpus),
			fileIdentity(options.observer),
			fileIdentity(path.join(options.packages, "package-lock.json")),
			fileIdentity(pi),
			fileIdentity(options.pigo),
		]);
	const engineIdentities = {};
	for (const [id, runtime] of Object.entries(runtimes)) {
		engineIdentities[id] = runtime.enginePath ? await fileIdentity(runtime.enginePath) : null;
	}

	const tier1 = samplingFor({ tier: 1 }, options);
	const report = {
		schemaVersion: 3,
		generatedAt: new Date().toISOString(),
		method: {
			warmups: tier1.warmups,
			samples: tier1.samples,
			profileByTier: Object.fromEntries(
				Object.keys(corpus.tierCounts ?? { 1: 0 }).map((tier) => [tier, samplingFor({ tier: Number(tier) }, options)]),
			),
			timeoutMs: options.timeoutMs,
			packageBudgetMs: options.packageBudgetMs,
			interleaved: true,
			runtimes: options.runtimes,
			primaryCandidate: primary,
			network: "runner refuses non-isolated network namespaces",
			performance: "startup and global-baseline-subtracted load; observer RPC timing is not package-specific work",
			entrypoints: "manifest-declared directories are expanded by this harness, so runtime discovery parity is out of scope",
			isolation: "the documented container protects the host, but packages share writable container tmpfs state",
			engineForcing:
				"each probe runs with a PATH whose only JavaScript engine is the tier engine; the observer reports the engine that actually evaluated it",
			registrationRetention: options.retainRegistrations,
		},
		safety: {
			networkNamespaceGuard,
			credentialsInherited: false,
		},
		inputs: {
			harness: harnessIdentity,
			taxonomy: taxonomyIdentity,
			corpus: corpusIdentity,
			observer: observerIdentity,
			packageLock: packageLockIdentity,
			upstreamPi: piIdentity,
			pigo: pigoIdentity,
			engines: engineIdentities,
		},
		corpus: {
			source: options.corpus,
			capturedAt: corpus.capturedAt,
			selection: corpus.selection,
			count: corpus.extensions.length,
			totalCount: corpus.totalCount,
			tierCounts: corpus.tierCounts,
			filtered: corpus.filtered ?? null,
		},
		runtimes: {
			node: process.version,
			pi: { executable: pi, version: executableVersion(pi) },
			pigo: { executable: options.pigo, version: executableVersion(options.pigo) },
			engines: Object.fromEntries(
				Object.entries(runtimes).map(([id, runtime]) => [
					id,
					{
						engine: runtime.engine,
						enginePath: runtime.enginePath,
						version: runtime.enginePath ? executableVersion(runtime.enginePath) : null,
					},
				]),
			),
		},
		baseline: null,
		resources: { pid: process.pid, isPid1: process.pid === 1, census: null, leaked: [], peakRssBytes: 0, warnings: [] },
		extensions: [],
		summary: null,
		incomplete: false,
	};

	// The corpus file hash is deliberately absent: the corpus grows while a run
	// is being iterated on, and every reused record is re-checked against its own
	// package identity below. The installed lock, binaries, observer and method do
	// gate reuse, because those change what a probe measures.
	const stream = new ResultStream(options.output, {
		harness: harnessIdentity.sha256,
		taxonomy: taxonomyIdentity.sha256,
		observer: observerIdentity.sha256,
		packageLock: packageLockIdentity.sha256,
		pigo: pigoIdentity.sha256,
		pi: piIdentity.sha256,
		runtimes: options.runtimes,
		method: { timeoutMs: options.timeoutMs, tierProfiles: options.tierProfiles, profile: options.profile, warmups: options.warmups, samples: options.samples },
	});

	if (options.finalize) {
		const reused = stream.start(true);
		report.extensions = [...reused.values()];
		report.baseline = {};
		process.stderr.write(`matrix: finalizing ${report.extensions.length} streamed packages\n`);
		report.incomplete = report.extensions.length !== corpus.extensions.length;
		report.summary = { valid: false, reason: "rebuilt from stream without a baseline", counts: null };
		await writeOutput(options.output, report);
		return;
	}

	const reused = stream.start(options.resume);

	let aborted = false;
	const abort = (signal) => {
		if (aborted) return;
		aborted = true;
		process.stderr.write(`matrix: ${signal} received, finishing current package then writing partial results\n`);
	};
	process.once("SIGINT", () => abort("SIGINT"));
	process.once("SIGTERM", () => abort("SIGTERM"));

	process.stderr.write(`matrix: measuring observer-only baseline across ${options.runtimes.join(", ")}\n`);
	report.baseline = await benchmark(null, runtimes, options, samplingFor({ tier: 1 }, options));
	for (const [id, summary] of Object.entries(report.baseline)) {
		if (!summary.engineVerified) {
			report.resources.warnings.push(`baseline for ${id} did not verify the ${runtimes[id].engine} engine`);
		}
	}

	const startedAt = Date.now();
	let completed = 0;
	const finalize = async () => {
		report.summary = aggregateSummary(report.extensions, corpus.totalCount, report.baseline, candidates, primary);
		report.resources.census = processCensus();
		if (report.resources.census.zombies > 0 && report.resources.isPid1) {
			report.resources.warnings.push(
				"zombie processes accumulated while the harness was PID 1; run the container with --init so leaked children are reaped",
			);
		}
		await writeOutput(options.output, report);
	};

	for (const extension of corpus.extensions) {
		if (aborted) {
			report.incomplete = true;
			break;
		}
		const cached = reused.get(extension.package);
		if (cached && cached.extension?.version === extension.version && cached.extension?.integrity === extension.integrity) {
			report.extensions.push(cached);
			completed++;
			continue;
		}
		const sampling = samplingFor(extension, options);
		process.stderr.write(
			`matrix: [${completed + 1}/${corpus.extensions.length}] tier ${extension.tier} ${extension.package}@${extension.version} (${sampling.profile})\n`,
		);
		const packageStartedAt = Date.now();
		let record;
		if (extension.unusable) {
			// No probe: the manifest never resolves to a loadable entrypoint, so
			// there is nothing either runtime could evaluate.
			record = skippedRecord(extension, candidates, extension.unusable, packageStartedAt);
			stream.append(record);
			report.extensions.push(record);
			completed++;
			continue;
		}
		try {
			const result = await benchmark(extension, runtimes, options, sampling);
			const candidateResults = {};
			for (const id of candidates) {
				const classification = classifyCandidate(result.pi, result[id], report.baseline, "pi", id, options);
				candidateResults[id] = {
					status: classification.status,
					reason: classification.reason,
					upstreamSupported: classification.upstreamSupported,
					engineVerified: result[id].engineVerified,
					failure: classification.failure,
					deltas: classification.deltas,
					difference: classification.difference,
					performance: performanceComparison(result.pi, result[id], report.baseline, "pi", id),
				};
			}
			const head = candidateResults[primary];
			record = {
				extension,
				tier: extension.tier,
				status: head.status,
				reason: head.reason,
				upstreamSupported: head.upstreamSupported,
				failure: head.failure,
				registrationDeltas: head.deltas,
				registrationDifference: head.difference,
				candidates: candidateResults,
				crossRuntime: {
					node: candidateResults["pigo-node"]?.status ?? null,
					bun: candidateResults["pigo-bun"]?.status ?? null,
					divergence: divergenceOf(candidateResults),
				},
				runtimeSummaries: result,
				pi: result.pi,
				pigo: result[primary],
				performance: head.performance,
				elapsedMs: Date.now() - packageStartedAt,
			};
		} catch (error) {
			// A harness-level failure for one package (a missing entrypoint from a
			// failed install, an unreadable manifest) must not end the run.
			record = skippedRecord(
				extension,
				candidates,
				{
					class: "install_failure",
					capability: `installed_package(${extension.package}@${extension.version})`,
					detail: normalizeError(error),
					evidence: (error?.stack ?? "").slice(0, 600),
				},
				packageStartedAt,
			);
		}

		stream.append(record);
		report.extensions.push(stripHeavyRegistrations(record, options.retainRegistrations));
		completed++;

		if (options.reap) {
			const census = processCensus();
			if (census.available && census.orphans.length > 0) {
				const killed = reapOrphans(census);
				if (killed.length > 0) {
					report.resources.leaked.push({ package: extension.package, killed: killed.length, sample: killed.slice(0, 5) });
				}
			}
		}
		const rss = process.memoryUsage().rss;
		report.resources.peakRssBytes = Math.max(report.resources.peakRssBytes, rss);
		stream.writeProgress({
			startedAt: new Date(startedAt).toISOString(),
			updatedAt: new Date().toISOString(),
			completed,
			total: corpus.extensions.length,
			elapsedMs: Date.now() - startedAt,
			rssBytes: rss,
			lastPackage: extension.package,
			lastStatus: record.status,
			lastReason: record.reason,
			counts: report.extensions.reduce((counts, entry) => {
				counts[entry.status] = (counts[entry.status] ?? 0) + 1;
				return counts;
			}, {}),
		});
	}

	report.incomplete = report.incomplete || report.extensions.length !== corpus.extensions.length;
	await finalize();
	if (report.extensions.some((entry) => entry.status === "flaky" && entry.reason === "resource_exhaustion")) {
		process.stderr.write("matrix: resource exhaustion was observed; raise --pids-limit/--memory and rerun the affected packages\n");
		process.exitCode = 2;
	}
}

const invokedDirectly = process.argv[1] && path.resolve(process.argv[1]) === SELF;
if (invokedDirectly) {
	main().catch((error) => {
		process.stderr.write(`matrix: ${error.stack ?? error.message}\n`);
		process.exitCode = 1;
	});
}

export {
	benchmark,
	classifyCandidate,
	divergenceOf,
	performanceComparison,
	registrationDelta,
	runtimeSummary,
	ResultStream,
	samplingFor,
	stripHeavyRegistrations,
	summarize,
	writeOutput,
};
