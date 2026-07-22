#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DEFAULT_CORPUS = path.join(HERE, "corpus.json");
const DEFAULT_OBSERVER = path.join(HERE, "observer.ts");
const OBSERVER_COMMAND = "__extension_matrix_probe";
const OBSERVER_MARKER = "PI_EXTENSION_MATRIX:";
const DIALOG_METHODS = new Set(["select", "confirm", "input", "editor"]);
const LOAD_ERROR = /(?:extension[^\n]*(?:error|failed)|failed to load extension|unsupported external module|cannot find (?:module|package)|ERR_[A-Z_]+)/i;

function usage() {
	return `Usage: node matrix.mjs --packages <directory> --pigo <binary> [options]

Options:
  --corpus <file>       Corpus manifest (default: corpus.json beside this file)
  --observer <file>     Observer extension (default: observer.ts beside this file)
  --output <file>       Write formatted JSON atomically; otherwise write to stdout
  --only <a,b,...>      Test package names or numeric ranks (repeatable)
  --warmups <n>         Warm-up samples per runtime (default: 2)
  --samples <n>         Measured samples per runtime (default: 11)
  --timeout-ms <n>      Per-process deadline (default: 30000)
  --validate-only       Validate inputs without executing Pi or extensions
  --help                Show this help

The benchmark is intentionally sequential and interleaves Pi/Pigo within every
sample. Run it only inside the networkless hardened container documented in
README.md.`;
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
		output: "",
		only: [],
		warmups: 2,
		samples: 11,
		timeoutMs: 30_000,
		validateOnly: false,
	};
	for (let index = 0; index < argv.length; index++) {
		const argument = argv[index];
		if (argument === "--help" || argument === "-h") return { help: true };
		if (argument === "--validate-only") {
			options.validateOnly = true;
			continue;
		}
		if (argument === "--only" && index + 1 < argv.length) {
			options.only.push(...argv[++index].split(",").filter(Boolean));
			continue;
		}
		const paths = new Set(["--corpus", "--observer", "--packages", "--pigo", "--output"]);
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
		throw new Error(`unknown or incomplete argument: ${argument}`);
	}
	if (!options.validateOnly && (!options.packages || !options.pigo)) {
		throw new Error("--packages and --pigo are required");
	}
	return options;
}

function validateRelativeEntrypoint(entrypoint, packageName) {
	if (typeof entrypoint !== "string" || entrypoint.length === 0 || path.isAbsolute(entrypoint)) {
		throw new Error(`${packageName} has invalid extension entrypoint ${JSON.stringify(entrypoint)}`);
	}
	const normalized = path.normalize(entrypoint);
	if (normalized === ".." || normalized.startsWith(`..${path.sep}`)) {
		throw new Error(`${packageName} extension entrypoint escapes its package: ${entrypoint}`);
	}
	return normalized;
}

async function loadCorpus(filename, only) {
	const corpus = JSON.parse(await readFile(filename, "utf8"));
	if (corpus.schemaVersion !== 1 || !Array.isArray(corpus.extensions) || corpus.extensions.length !== 43) {
		throw new Error(`${filename} must be the 43-package extension corpus v1`);
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
		extension.extensions = extension.extensions.map((entrypoint) => validateRelativeEntrypoint(entrypoint, extension.package));
	}
	corpus.extensions.sort((left, right) => left.rank - right.rank);
	if (only.length === 0) return corpus;
	const requested = new Set(only);
	const selected = corpus.extensions.filter((extension) => requested.has(extension.package) || requested.has(String(extension.rank)));
	for (const item of requested) {
		if (!selected.some((extension) => extension.package === item || String(extension.rank) === item)) {
			throw new Error(`--only did not match ${item}`);
		}
	}
	return { ...corpus, extensions: selected };
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

function executableVersion(command) {
	const result = spawnSync(command, ["--version"], { encoding: "utf8", timeout: 10_000 });
	return {
		status: result.status,
		stdout: result.stdout?.trim() ?? "",
		stderr: result.stderr?.trim() ?? "",
		error: result.error?.message,
	};
}

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

async function runProbe(executable, extensionPaths, options) {
	const runRoot = path.join("/work", `matrix-${process.pid}`);
	const home = path.join(runRoot, "home");
	const cwd = path.join(runRoot, "project");
	const agentDir = path.join(runRoot, "agent");
	await rm(runRoot, { recursive: true, force: true });
	await Promise.all([
		mkdir(home, { recursive: true }),
		mkdir(cwd, { recursive: true }),
		mkdir(agentDir, { recursive: true }),
	]);
	await writeFile(path.join(agentDir, "settings.json"), '{"compaction":{"enabled":false},"retry":{"enabled":false}}\n');

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
		"--no-tools",
		"--no-approve",
		"--offline",
	];
	for (const extensionPath of extensionPaths) args.push("-e", extensionPath);

	const startedAt = performance.now();
	const child = spawn(executable, args, {
		cwd,
		detached: true,
		stdio: ["pipe", "pipe", "pipe"],
		env: {
			HOME: home,
			PATH: `${path.join(options.packages, "node_modules", ".bin")}:${process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin"}`,
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
	return {
		success: !completionResult.error,
		error: completionResult.error,
		timedOut: completionResult.timedOut,
		loadError: LOAD_ERROR.test(stderr) ? stderr.trim() : null,
		startupMs,
		commandMs,
		getCommands,
		observation,
		uiRequestCount,
		stderr: stderr.trim(),
		stdoutRemainder: stdoutBuffer.trim(),
	};
}

function runtimeSummary(attempts) {
	const measured = attempts.filter((attempt) => !attempt.warmup);
	const lastRegistration = measured.findLast((attempt) => attempt.success && !attempt.loadError);
	const diagnosticCounts = new Map();
	for (const attempt of attempts.filter((item) => !item.success || item.loadError)) {
		const diagnostic = {
			error: attempt.error,
			timedOut: attempt.timedOut,
			loadError: attempt.loadError,
			stderr: attempt.stderr,
			stdoutRemainder: attempt.stdoutRemainder,
		};
		const encoded = JSON.stringify(diagnostic);
		const existing = diagnosticCounts.get(encoded);
		if (existing) existing.count++;
		else diagnosticCounts.set(encoded, { count: 1, ...diagnostic });
	}
	const compactAttempts = attempts.map((attempt) => {
		return {
			warmup: attempt.warmup,
			sample: attempt.sample,
			success: attempt.success && !attempt.loadError,
			startupMs: attempt.startupMs === null ? null : Number(attempt.startupMs.toFixed(3)),
			commandMs: attempt.commandMs === null ? null : Number(attempt.commandMs.toFixed(3)),
			uiRequestCount: attempt.uiRequestCount,
		};
	});
	return {
		ok: measured.length > 0 && measured.every((attempt) => attempt.success && !attempt.loadError),
		startup: summarize(
			measured.filter((attempt) => attempt.success && !attempt.loadError).map((attempt) => attempt.startupMs),
		),
		command: summarize(
			measured.filter((attempt) => attempt.success && !attempt.loadError).map((attempt) => attempt.commandMs),
		),
		attempts: compactAttempts,
		diagnostics: [...diagnosticCounts.values()],
		registration: lastRegistration?.observation ?? null,
		rpcCommands: normalizeCommands(lastRegistration?.getCommands ?? null),
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

function registrationDelta(current, baseline) {
	if (!current?.registration || !baseline?.registration) return null;
	return {
		activeTools: stringDelta(current.registration.activeTools, baseline.registration.activeTools),
		allTools: stringDelta(current.registration.allTools, baseline.registration.allTools),
		commands: commandDelta(current.registration.commands, baseline.registration.commands),
		rpcCommands: commandDelta(current.rpcCommands, baseline.rpcCommands),
	};
}

function registrationDifference(pi, pigo, baselines) {
	const piDelta = registrationDelta(pi, baselines.pi);
	const pigoDelta = registrationDelta(pigo, baselines.pigo);
	if (JSON.stringify(piDelta) === JSON.stringify(pigoDelta)) {
		return { difference: null, piDelta, pigoDelta };
	}
	return { difference: { pi: piDelta, pigo: pigoDelta }, piDelta, pigoDelta };
}

function subtract(value, baseline) {
	if (value === null || baseline === null) return null;
	return Number((value - baseline).toFixed(3));
}

function ratio(numerator, denominator) {
	if (numerator === null || denominator === null || denominator <= 0) return null;
	return Number((numerator / denominator).toFixed(3));
}

async function benchmark(extension, runtimeExecutables, options) {
	const extensionPaths = [options.observer];
	if (extension) {
		extensionPaths.push(...(await extensionEntrypoints(options.packages, extension)));
	}
	const attempts = { pi: [], pigo: [] };
	const total = options.warmups + options.samples;
	for (let index = 0; index < total; index++) {
		const order = index % 2 === 0 ? ["pi", "pigo"] : ["pigo", "pi"];
		for (const runtime of order) {
			const attempt = await runProbe(runtimeExecutables[runtime], extensionPaths, options);
			attempt.warmup = index < options.warmups;
			attempt.sample = index < options.warmups ? index + 1 : index - options.warmups + 1;
			attempts[runtime].push(attempt);
		}
	}
	return { pi: runtimeSummary(attempts.pi), pigo: runtimeSummary(attempts.pigo) };
}

function classify(result, baselines) {
	if (!baselines.pi.ok || !baselines.pigo.ok) {
		return { status: "infrastructure_failure", difference: null, deltas: null };
	}
	if (!result.pi.ok) return { status: "pi_baseline_failure", difference: null, deltas: null };
	if (!result.pigo.ok) return { status: "pigo_load_failure", difference: null, deltas: null };
	const compared = registrationDifference(result.pi, result.pigo, baselines);
	if (compared.difference) {
		return {
			status: "registration_mismatch",
			difference: compared.difference,
			deltas: { pi: compared.piDelta, pigo: compared.pigoDelta },
		};
	}
	return { status: "pass", difference: null, deltas: { pi: compared.piDelta, pigo: compared.pigoDelta } };
}

function performanceComparison(result, baselines) {
	const piStartup = result.pi.startup.medianMs;
	const pigoStartup = result.pigo.startup.medianMs;
	const piNet = subtract(piStartup, baselines.pi.startup.medianMs);
	const pigoNet = subtract(pigoStartup, baselines.pigo.startup.medianMs);
	return {
		startupRatio: ratio(pigoStartup, piStartup),
		baselineSubtractedLoadMs: { pi: piNet, pigo: pigoNet },
		baselineSubtractedLoadRatio: ratio(pigoNet, piNet),
		commandRatio: ratio(result.pigo.command.medianMs, result.pi.command.medianMs),
	};
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

async function main() {
	const options = parseArgs(process.argv.slice(2));
	if (options.help) {
		process.stdout.write(usage() + "\n");
		return;
	}
	const corpus = await loadCorpus(options.corpus, options.only);
	if (options.validateOnly) {
		process.stdout.write(`valid corpus: ${corpus.extensions.length} extension packages\n`);
		return;
	}

	const pi = path.join(options.packages, "node_modules", ".bin", "pi");
	const runtimeExecutables = { pi, pigo: options.pigo };
	const report = {
		schemaVersion: 1,
		generatedAt: new Date().toISOString(),
		method: {
			warmups: options.warmups,
			samples: options.samples,
			timeoutMs: options.timeoutMs,
			interleaved: true,
			network: "disabled by required container invocation",
			performance: "process spawn to get_commands; observer command to deterministic UI marker and RPC response",
		},
		corpus: {
			source: options.corpus,
			capturedAt: corpus.capturedAt,
			selection: corpus.selection,
			count: corpus.extensions.length,
		},
		runtimes: {
			node: process.version,
			pi: { executable: pi, version: executableVersion(pi) },
			pigo: { executable: options.pigo, version: executableVersion(options.pigo) },
		},
		baseline: null,
		extensions: [],
		summary: null,
	};

	process.stderr.write("matrix: measuring observer-only baseline\n");
	report.baseline = await benchmark(null, runtimeExecutables, options);
	for (const extension of corpus.extensions) {
		process.stderr.write(`matrix: ${extension.rank}/${corpus.extensions.at(-1).rank} ${extension.package}@${extension.version}\n`);
		const result = await benchmark(extension, runtimeExecutables, options);
		const classification = classify(result, report.baseline);
		report.extensions.push({
			extension,
			status: classification.status,
			registrationDeltas: classification.deltas,
			registrationDifference: classification.difference,
			pi: result.pi,
			pigo: result.pigo,
			performance: performanceComparison(result, report.baseline),
		});
	}
	const counts = {};
	for (const result of report.extensions) counts[result.status] = (counts[result.status] ?? 0) + 1;
	const denominator = report.extensions.filter((result) => result.status !== "pi_baseline_failure").length;
	report.summary = {
		counts,
		compatibilityDenominator: denominator,
		compatible: counts.pass ?? 0,
		compatibilityPercent: denominator === 0 ? null : Number((((counts.pass ?? 0) / denominator) * 100).toFixed(1)),
	};
	await writeOutput(options.output, report);
}

main().catch((error) => {
	process.stderr.write(`matrix: ${error.stack ?? error.message}\n`);
	process.exitCode = 1;
});
