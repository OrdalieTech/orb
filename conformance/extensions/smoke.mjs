#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, readdir, rename, rm, stat, writeFile } from "node:fs/promises";
import { networkInterfaces, tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DEFAULT_CASES = path.join(HERE, "smoke-cases.json");
const DEFAULT_CORPUS = path.join(HERE, "corpus.json");
const DIALOG_METHODS = new Set(["select", "confirm", "input", "editor"]);
const MODEL_EVENT_TYPES = new Set([
	"agent_start",
	"turn_start",
	"tool_execution_start",
	"tool_execution_update",
	"tool_execution_end",
]);
const LOAD_ERROR = /(?:extension error \(|failed to load extension)/i;
const MAX_DIAGNOSTIC_BYTES = 128 * 1024;
const MAX_CAPTURED_EVENTS = 100;

function usage() {
	return `Usage: node smoke.mjs --packages <directory> --pigo <binary> --output <file> [options]

Options:
  --pi <binary>         Upstream Pi executable (default: <packages>/node_modules/.bin/pi)
  --cases <file>        Smoke case manifest (default: smoke-cases.json beside this file)
  --corpus <file>       Locked extension corpus (default: corpus.json beside this file)
  --only <a,b,...>      Case ids, package names, commands, or ranks (repeatable)
  --warmups <n>         Unmeasured attempts per runtime and case (default: 1)
  --samples <n>         Measured attempts per runtime and case (default: 5)
  --timeout-ms <n>      Per-process deadline (default: 30000)
  --settle-ms <n>       Capture window after command response (default: 100)
  --allow-network       Bypass the network-namespace guard; unsafe for proof runs
  --validate-only       Validate manifests without starting either runtime
  --help                Show this help

The runner refuses to execute when it can see a non-loopback network interface.
Run it in the same networkless container used by matrix.mjs. A passing result
means only that the configured slash-command handler loaded, was advertised,
completed without a model turn, and emitted its inspected evidence.`;
}

function integer(value, name, allowZero = false) {
	const parsed = Number(value);
	if (!Number.isInteger(parsed) || parsed < (allowZero ? 0 : 1)) {
		throw new Error(`${name} must be an integer${allowZero ? " at least zero" : " greater than zero"}`);
	}
	return parsed;
}

function parseArgs(argv) {
	const options = {
		cases: DEFAULT_CASES,
		corpus: DEFAULT_CORPUS,
		packages: "",
		pigo: "",
		pi: "",
		output: "",
		only: [],
		warmups: 1,
		samples: 5,
		timeoutMs: 30_000,
		settleMs: 100,
		allowNetwork: false,
		validateOnly: false,
	};
	for (let index = 0; index < argv.length; index++) {
		const argument = argv[index];
		if (argument === "--help" || argument === "-h") return { help: true };
		if (argument === "--validate-only") {
			options.validateOnly = true;
			continue;
		}
		if (argument === "--allow-network") {
			options.allowNetwork = true;
			continue;
		}
		if (argument === "--only" && index + 1 < argv.length) {
			options.only.push(...argv[++index].split(",").filter(Boolean));
			continue;
		}
		const pathOptions = new Set(["--cases", "--corpus", "--packages", "--pigo", "--pi", "--output"]);
		if (pathOptions.has(argument) && index + 1 < argv.length) {
			options[argument.slice(2)] = path.resolve(argv[++index]);
			continue;
		}
		if (argument === "--warmups" && index + 1 < argv.length) {
			options.warmups = integer(argv[++index], argument, true);
			continue;
		}
		if (argument === "--samples" && index + 1 < argv.length) {
			options.samples = integer(argv[++index], argument);
			continue;
		}
		if (argument === "--timeout-ms" && index + 1 < argv.length) {
			options.timeoutMs = integer(argv[++index], argument);
			continue;
		}
		if (argument === "--settle-ms" && index + 1 < argv.length) {
			options.settleMs = integer(argv[++index], argument, true);
			continue;
		}
		throw new Error(`unknown or incomplete argument: ${argument}`);
	}
	if (!options.validateOnly && (!options.packages || !options.pigo || !options.output)) {
		throw new Error("--packages, --pigo, and --output are required");
	}
	if (!options.pi && options.packages) {
		options.pi = path.join(options.packages, "node_modules", ".bin", "pi");
	}
	return options;
}

function compareText(left, right) {
	return left < right ? -1 : left > right ? 1 : 0;
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

async function loadInputs(caseFilename, corpusFilename, selectors) {
	const [caseBytes, corpusBytes] = await Promise.all([readFile(caseFilename), readFile(corpusFilename)]);
	const manifest = JSON.parse(caseBytes.toString("utf8"));
	const corpus = JSON.parse(corpusBytes.toString("utf8"));
	if (
		manifest.schemaVersion !== 1 ||
		manifest.upstreamVersion !== "0.81.1" ||
		!Array.isArray(manifest.cases) ||
		!Array.isArray(manifest.workflowCases)
	) {
		throw new Error(`${caseFilename} must be a Pi 0.81.1 command-smoke manifest v1`);
	}
	if (corpus.schemaVersion !== 1 || !Array.isArray(corpus.extensions)) {
		throw new Error(`${corpusFilename} must be an extension corpus v1`);
	}
	const corpusByPackage = new Map();
	for (const extension of corpus.extensions) {
		if (typeof extension.package !== "string" || !Number.isInteger(extension.rank)) {
			throw new Error(`${corpusFilename} contains an invalid extension record`);
		}
		if (!Array.isArray(extension.extensions) || extension.extensions.length === 0) {
			throw new Error(`${extension.package} has no pi.extensions entrypoints`);
		}
		extension.extensions = extension.extensions.map((item) => validateRelativeEntrypoint(item, extension.package));
		corpusByPackage.set(extension.package, extension);
	}
	const ids = new Set();
	const validateCase = (item, kind) => {
		if (
			typeof item.id !== "string" ||
			typeof item.package !== "string" ||
			typeof item.command !== "string" ||
			typeof item.args !== "string" ||
			!Number.isInteger(item.rank) ||
			!Array.isArray(item.evidencePatterns) ||
			item.evidencePatterns.length === 0
		) {
			throw new Error(`${caseFilename} contains an invalid ${kind} case`);
		}
		if (ids.has(item.id)) throw new Error(`${caseFilename} contains duplicate case id ${item.id}`);
		ids.add(item.id);
		const extension = corpusByPackage.get(item.package);
		if (!extension || extension.rank !== item.rank) {
			throw new Error(`${item.id} does not match rank/package in ${corpusFilename}`);
		}
		for (const pattern of item.evidencePatterns) {
			if (typeof pattern !== "string" || pattern.length === 0) throw new Error(`${item.id} has an invalid evidence pattern`);
			try {
				new RegExp(pattern, "i");
			} catch (error) {
				throw new Error(`${item.id} has invalid evidence pattern ${pattern}: ${error.message}`);
			}
		}
		item.extension = extension;
	};
	for (const item of manifest.cases) {
		validateCase(item, "command-smoke");
		item.caseKind = "command";
	}
	for (const item of manifest.workflowCases) {
		validateCase(item, "workflow-smoke");
		item.caseKind = "workflow";
		if (
			typeof item.module !== "string" ||
			typeof item.marker !== "string" ||
			item.marker.length === 0 ||
			!Array.isArray(item.fixtures) ||
			item.fixtures.length === 0
		) {
			throw new Error(`${item.id} has an invalid workflow definition`);
		}
		item.module = validateRelativeEntrypoint(item.module, item.package);
		const fixturePaths = new Set();
		for (const fixture of item.fixtures) {
			if (typeof fixture?.content !== "string") throw new Error(`${item.id} has an invalid fixture`);
			fixture.path = validateRelativeEntrypoint(fixture.path, item.id);
			if (fixturePaths.has(fixture.path)) throw new Error(`${item.id} has duplicate fixture ${fixture.path}`);
			fixturePaths.add(fixture.path);
		}
	}
	manifest.totalCommandCount = manifest.cases.length;
	manifest.totalWorkflowCount = manifest.workflowCases.length;
	if (selectors.length > 0) {
		const requested = new Set(selectors);
		const matchesSelector = (item) =>
			requested.has(item.id) ||
			requested.has(item.package) ||
			requested.has(item.command) ||
			requested.has(String(item.rank));
		manifest.cases = manifest.cases.filter(matchesSelector);
		manifest.workflowCases = manifest.workflowCases.filter(matchesSelector);
		const selected = [...manifest.cases, ...manifest.workflowCases];
		for (const selector of requested) {
			if (
				!selected.some(
				(item) =>
						item.id === selector ||
						item.package === selector ||
						item.command === selector ||
						String(item.rank) === selector,
				)
			) {
				throw new Error(`--only did not match ${selector}`);
			}
		}
	}
	if (manifest.cases.length + manifest.workflowCases.length === 0) throw new Error("no smoke cases selected");
	return { manifest, caseBytes, corpusBytes };
}

function packageDirectory(packages, packageName) {
	return path.join(packages, "node_modules", ...packageName.split("/"));
}

async function resolveDirectoryEntrypoints(directory) {
	try {
		const packageJSON = JSON.parse(await readFile(path.join(directory, "package.json"), "utf8"));
		if (Array.isArray(packageJSON.pi?.extensions) && packageJSON.pi.extensions.length > 0) {
			const entries = [];
			for (const declared of packageJSON.pi.extensions) {
				const entrypoint = path.resolve(directory, validateRelativeEntrypoint(declared, directory));
				try {
					if ((await stat(entrypoint)).isFile()) entries.push(entrypoint);
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
		const candidate = path.join(directory, item.name);
		if (item.isFile() && (item.name.endsWith(".ts") || item.name.endsWith(".js"))) {
			entries.push(candidate);
		} else if (item.isDirectory()) {
			const nested = await resolveDirectoryEntrypoints(candidate);
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
		else if (info.isFile()) entries.push(entrypoint);
	}
	if (entries.length === 0) throw new Error(`${extension.package} resolved no extension entrypoints`);
	return [...new Set(entries)];
}

async function inspectNetworkIsolation() {
	try {
		const interfaces = networkInterfaces();
		const external = Object.entries(interfaces)
			.flatMap(([name, addresses]) => (addresses ?? []).map((address) => ({ name, ...address })))
			.filter((address) => !address.internal);
		return { isolated: external.length === 0, method: "os.networkInterfaces", external };
	} catch (error) {
		try {
			const data = await readFile("/proc/net/dev", "utf8");
			const interfaces = data
				.split("\n")
				.slice(2)
				.map((line) => line.split(":", 1)[0].trim())
				.filter(Boolean);
			const external = interfaces.filter((name) => name !== "lo");
			return {
				isolated: external.length === 0,
				method: "/proc/net/dev fallback",
				external: external.map((name) => ({ name })),
				interfaceInspectionError: error.message,
			};
		} catch (fallbackError) {
			return {
				isolated: false,
				method: "unavailable",
				external: [],
				interfaceInspectionError: error.message,
				fallbackError: fallbackError.message,
			};
		}
	}
}

function sha256(bytes) {
	return createHash("sha256").update(bytes).digest("hex");
}

async function fileDigest(filename) {
	return sha256(await readFile(filename));
}

function executableVersion(command) {
	const result = spawnSync(command, ["--version"], { encoding: "utf8", timeout: 10_000 });
	return {
		status: result.status,
		stdout: result.stdout?.trim() ?? "",
		stderr: result.stderr?.trim() ?? "",
		error: result.error?.message ?? null,
	};
}

function writeLine(child, value) {
	if (!child.stdin.destroyed) child.stdin.write(`${JSON.stringify(value)}\n`);
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
	if (child.exitCode === null && child.signalCode === null) signalProcessGroup(child, "SIGKILL");
}

function appendCapped(current, chunk) {
	if (current.length >= MAX_DIAGNOSTIC_BYTES) return current;
	return (current + chunk).slice(0, MAX_DIAGNOSTIC_BYTES);
}

function normalizeForReport(value, replacements) {
	let encoded = JSON.stringify(value);
	for (const [from, to] of replacements) encoded = encoded.split(from).join(to);
	return JSON.parse(encoded);
}

function collectStrings(value, output = []) {
	if (typeof value === "string") output.push(value);
	else if (Array.isArray(value)) {
		for (const item of value) collectStrings(item, output);
	} else if (value && typeof value === "object") {
		for (const item of Object.values(value)) collectStrings(item, output);
	}
	return output;
}

function scrubDynamicText(value) {
	return value
		.replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi, "<UUID>")
		.replace(/\bsubagent-chat-[0-9a-f]{8}\b/gi, "subagent-chat-<ID>")
		.replace(/pigo-extension-smoke-[A-Za-z0-9]+/g, "pigo-extension-smoke-<RANDOM>");
}

function scrubDynamicValues(value) {
	if (typeof value === "string") return scrubDynamicText(value);
	if (Array.isArray(value)) return value.map(scrubDynamicValues);
	if (value && typeof value === "object") {
		return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, scrubDynamicValues(item)]));
	}
	return value;
}

function observableTranscript(events) {
	return events.flatMap((event) => {
		if (event?.type === "extension_ui_request") {
			const { type: _type, id: _id, ...request } = event;
			return [{ kind: "ui", ...scrubDynamicValues(request) }];
		}
		if (event?.type === "message_end" && event?.message?.role === "custom") {
			return [
				{
					kind: "custom_message",
					customType: event.message.customType,
					content: scrubDynamicValues(event.message.content),
					display: event.message.display,
				},
			];
		}
		return [];
	});
}

function messageStartsModelActivity(message) {
	if (MODEL_EVENT_TYPES.has(message?.type)) return true;
	if ((message?.type === "message_start" || message?.type === "message_end") && message?.message?.role === "assistant") {
		return true;
	}
	return false;
}

function workflowWrapperSource(smokeCase, modulePath) {
	return `import { readFile } from "node:fs/promises";
import {
  loadStagedKnowledgeBase,
  resolveKnowledgeBaseInput,
  stageKnowledgeBaseInput,
} from ${JSON.stringify(modulePath)};

function snapshot(input) {
  return {
    sourceKind: input.sourceKind,
    sourceLabel: input.sourceLabel,
    files: input.files.map((file) => ({
      logicalPath: file.logicalPath,
      content: file.content,
      bytes: file.bytes,
      sha256: file.sha256,
    })),
    totalBytes: input.totalBytes,
    aggregateSha256: input.aggregateSha256,
  };
}

export default function pioliumKnowledgeBaseWorkflowProbe(pi) {
  pi.registerCommand(${JSON.stringify(smokeCase.command)}, {
    description: "Exercise Piolium's real knowledge-base input pipeline",
    handler: async (_args, ctx) => {
      const resolved = await resolveKnowledgeBaseInput({
        targetDir: ctx.cwd,
        path: ctx.cwd + "/knowledge-base",
        adoptPriorRun: false,
      });
      if (!resolved) throw new Error("Piolium returned no knowledge-base input");
      const reference = await stageKnowledgeBaseInput(ctx.cwd + "/piolium", resolved);
      const loaded = await loadStagedKnowledgeBase(ctx.cwd + "/piolium");
      let stagedManifest;
      try {
        const parsed = JSON.parse(await readFile(
          ctx.cwd + "/piolium/attack-surface/knowledge-base-input/manifest.json",
          "utf8",
        ));
        stagedManifest = {
          schema_version: parsed.schema_version,
          source_kind: parsed.source_kind,
          source_label: parsed.source_label,
          aggregate_sha256: parsed.aggregate_sha256,
          total_bytes: parsed.total_bytes,
          file_count: parsed.file_count,
          files: parsed.files,
        };
      } catch (error) {
        stagedManifest = { error: error instanceof Error ? error.message : String(error) };
      }
      ctx.ui.notify(${JSON.stringify(smokeCase.marker)} + JSON.stringify({
        resolved: snapshot(resolved),
        reference,
        loaded: loaded ? snapshot(loaded) : null,
        stagedManifest,
      }), "info");
    },
  });
}
`;
}

async function prepareWorkflowCase(smokeCase, cwd, options) {
	for (const fixture of smokeCase.fixtures) {
		const filename = path.join(cwd, fixture.path);
		await mkdir(path.dirname(filename), { recursive: true });
		await writeFile(filename, fixture.content, "utf8");
	}
	const modulePath = path.join(packageDirectory(options.packages, smokeCase.package), smokeCase.module);
	if (!(await stat(modulePath)).isFile()) throw new Error(`${smokeCase.id} module is missing: ${modulePath}`);
	const wrapper = path.join(cwd, "piolium-knowledge-base-workflow-probe.ts");
	await writeFile(wrapper, workflowWrapperSource(smokeCase, modulePath), "utf8");
	return [wrapper];
}

function validateWorkflowPayload(smokeCase, payload) {
	const errors = [];
	if (!payload || typeof payload !== "object") return ["payload is not an object"];
	const resolved = payload.resolved;
	const loaded = payload.loaded;
	const reference = payload.reference;
	const manifest = payload.stagedManifest;
	if (!resolved || !Array.isArray(resolved.files)) errors.push("resolved snapshot is missing");
	else {
		const expectedContents = smokeCase.fixtures.map((fixture) => fixture.content).sort(compareText);
		const actualContents = resolved.files.map((file) => file?.content).sort(compareText);
		if (JSON.stringify(actualContents) !== JSON.stringify(expectedContents)) errors.push("resolved fixture contents differ");
		if (resolved.files.length !== smokeCase.fixtures.length) errors.push("resolved fixture count differs");
	}
	if (!loaded) errors.push("staged knowledge base did not reload");
	else if (JSON.stringify(canonical(loaded)) !== JSON.stringify(canonical(resolved))) {
		errors.push("reloaded snapshot differs from resolved snapshot");
	}
	if (!reference || reference.aggregate_sha256 !== resolved?.aggregateSha256) {
		errors.push("reference aggregate hash differs");
	}
	if (reference?.file_count !== smokeCase.fixtures.length) errors.push("reference file count differs");
	if (!manifest || manifest.aggregate_sha256 !== resolved?.aggregateSha256) {
		errors.push("staged manifest aggregate hash differs");
	}
	if (manifest?.file_count !== smokeCase.fixtures.length) errors.push("staged manifest file count differs");
	return errors;
}

async function runAttempt(runtimeName, executable, entries, smokeCase, options) {
	const runRoot = await mkdtemp(path.join(tmpdir(), "pigo-extension-smoke-"));
	const home = path.join(runRoot, "home");
	const cwd = path.join(runRoot, "project");
	const agentDir = path.join(runRoot, "agent");
	await Promise.all([mkdir(home, { recursive: true }), mkdir(cwd, { recursive: true }), mkdir(agentDir, { recursive: true })]);
	await writeFile(path.join(agentDir, "settings.json"), '{"compaction":{"enabled":false},"retry":{"enabled":false}}\n');
	if (smokeCase.caseKind === "workflow") entries = await prepareWorkflowCase(smokeCase, cwd, options);

	const args = [
		"--mode",
		"rpc",
		"--no-session",
		"--provider",
		"openai",
		"--model",
		"gpt-4o-mini",
		"--api-key",
		"extension-smoke-offline-key",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-context-files",
		"--no-themes",
		"--no-approve",
		"--offline",
	];
	for (const entry of entries) args.push("-e", entry);
	const child = spawn(executable, args, {
		cwd,
		detached: true,
		stdio: ["pipe", "pipe", "pipe"],
		env: {
			HOME: home,
			PATH: `${path.join(options.packages, "node_modules", ".bin")}:${process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin"}`,
			PI_CODING_AGENT_DIR: agentDir,
			XDG_CONFIG_HOME: path.join(home, ".config"),
			XDG_CACHE_HOME: path.join(home, ".cache"),
			TMPDIR: runRoot,
			NO_COLOR: "1",
			TERM: "dumb",
			CI: "1",
			SHELL: "/bin/sh",
			HTTP_PROXY: "http://127.0.0.1:9",
			HTTPS_PROXY: "http://127.0.0.1:9",
			ALL_PROXY: "http://127.0.0.1:9",
			http_proxy: "http://127.0.0.1:9",
			https_proxy: "http://127.0.0.1:9",
			all_proxy: "http://127.0.0.1:9",
			NO_PROXY: "",
			no_proxy: "",
			npm_config_offline: "true",
		},
	});
	child.stdin.on("error", () => {});

	const startedAt = performance.now();
	let phase = "startup";
	let stderr = "";
	let stdoutRemainder = "";
	let stdoutBuffer = "";
	let settled = false;
	let startupMs = null;
	let commandSentAt = null;
	let handlerMs = null;
	let commandAdvertised = null;
	let commandSourceCorrect = false;
	let commandResponse = null;
	let completionError = null;
	let timedOut = false;
	let modelActivityDetected = false;
	let handlerEventOverflow = 0;
	let workflowPayload = null;
	let workflowPayloadError = null;
	const startupUI = [];
	const handlerEvents = [];
	const extensionErrors = [];
	const nonJSONLines = [];
	const replacements = [
		[runRoot, "<RUN_ROOT>"],
		[options.packages, "<PACKAGES>"],
	];

	const captureHandlerEvent = (message) => {
		if (handlerEvents.length < MAX_CAPTURED_EVENTS) handlerEvents.push(normalizeForReport(message, replacements));
		else handlerEventOverflow++;
	};

	const completion = new Promise((resolve) => {
		let settleTimer = null;
		const deadline = setTimeout(() => {
			timedOut = true;
			finish(`timeout after ${options.timeoutMs}ms`);
		}, options.timeoutMs);
		deadline.unref?.();

		const finish = (error) => {
			if (settled) return;
			settled = true;
			completionError = error;
			clearTimeout(deadline);
			if (settleTimer) clearTimeout(settleTimer);
			resolve();
		};

		child.once("error", (error) => finish(error.message));
		child.once("exit", (code, signal) => {
			if (!settled) finish(`process exited before command smoke completed (${signal ?? `exit ${code}`})`);
		});

		child.stdout.on("data", (chunk) => {
			stdoutBuffer += chunk.toString("utf8");
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
					if (nonJSONLines.length < 20) nonJSONLines.push(raw.slice(0, 2_000));
					continue;
				}
				if (messageStartsModelActivity(message)) modelActivityDetected = true;
				if (message.type === "extension_error") extensionErrors.push(normalizeForReport(message, replacements));
				if (message.type === "extension_ui_request") {
					if (phase === "startup") startupUI.push(normalizeForReport(message, replacements));
					else captureHandlerEvent(message);
					if (DIALOG_METHODS.has(message.method)) {
						writeLine(child, { type: "extension_ui_response", id: message.id, cancelled: true });
					}
					if (
						smokeCase.caseKind === "workflow" &&
						message.method === "notify" &&
						typeof message.message === "string" &&
						message.message.startsWith(smokeCase.marker)
					) {
						try {
							workflowPayload = JSON.parse(message.message.slice(smokeCase.marker.length));
						} catch (error) {
							workflowPayloadError = error instanceof Error ? error.message : String(error);
						}
					}
				} else if (phase !== "startup" && message.type !== "response") {
					captureHandlerEvent(message);
				}

				if (message.type === "response" && message.id === "commands") {
					if (!message.success) {
						finish(`get_commands failed: ${message.error ?? "unknown error"}`);
						continue;
					}
					startupMs = performance.now() - startedAt;
					const commands = Array.isArray(message.data?.commands) ? message.data.commands : [];
					commandAdvertised = commands.find((command) => command?.name === smokeCase.command) ?? null;
					if (!commandAdvertised) {
						finish(`command /${smokeCase.command} was not advertised`);
						continue;
					}
					commandSourceCorrect = commandAdvertised.source === "extension";
					if (!commandSourceCorrect) {
						finish(`/${smokeCase.command} was advertised by ${commandAdvertised.source ?? "an unknown source"}, not the extension`);
						continue;
					}
					phase = "handler";
					commandSentAt = performance.now();
					const suffix = smokeCase.args ? ` ${smokeCase.args}` : "";
					writeLine(child, { id: "handler", type: "prompt", message: `/${smokeCase.command}${suffix}` });
					continue;
				}
				if (message.type === "response" && message.id === "handler") {
					commandResponse = normalizeForReport(message, replacements);
					handlerMs = commandSentAt === null ? null : performance.now() - commandSentAt;
					captureHandlerEvent(message);
					if (!message.success) {
						finish(`/${smokeCase.command} failed: ${message.error ?? "unknown error"}`);
						continue;
					}
					phase = "settle";
					settleTimer = setTimeout(() => finish(null), options.settleMs);
					settleTimer.unref?.();
				}
			}
		});
	});

	child.stderr.on("data", (chunk) => {
		stderr = appendCapped(stderr, chunk.toString("utf8"));
	});
	writeLine(child, { id: "commands", type: "get_commands" });
	await completion;
	await stopChild(child);
	stdoutRemainder = stdoutBuffer.trim().slice(0, MAX_DIAGNOSTIC_BYTES);
	await rm(runRoot, { recursive: true, force: true });

	const evidenceText = collectStrings(handlerEvents).join("\n");
	const evidence = smokeCase.evidencePatterns.map((pattern) => ({
		pattern,
		matched: new RegExp(pattern, "i").test(evidenceText),
	}));
	const observableOutput = {
		startup: observableTranscript(startupUI),
		handler: observableTranscript(handlerEvents),
	};
	const workflowInvariantErrors =
		smokeCase.caseKind === "workflow" && workflowPayload !== null
			? validateWorkflowPayload(smokeCase, workflowPayload)
			: [];
	const loadError = LOAD_ERROR.test(stderr) || extensionErrors.length > 0;
	const failureKinds = [];
	if (completionError) failureKinds.push(timedOut ? "timeout" : "rpc_or_process_error");
	if (loadError) failureKinds.push("extension_load_error");
	if (!commandAdvertised) failureKinds.push("command_not_advertised");
	else if (!commandSourceCorrect) failureKinds.push("command_not_from_extension");
	if (!commandResponse?.success) failureKinds.push("command_response_failed");
	if (modelActivityDetected) failureKinds.push("unexpected_model_activity");
	if (evidence.some((item) => !item.matched)) failureKinds.push("missing_handler_evidence");
	if (smokeCase.caseKind === "workflow" && (workflowPayload === null || workflowPayloadError !== null)) {
		failureKinds.push("invalid_workflow_payload");
	}
	if (workflowInvariantErrors.length > 0) failureKinds.push("workflow_invariant_failed");
	return {
		runtime: runtimeName,
		success: failureKinds.length === 0,
		failureKinds,
		error: completionError,
		timedOut,
		startupMs: startupMs === null ? null : Number(startupMs.toFixed(3)),
		handlerMs: handlerMs === null ? null : Number(handlerMs.toFixed(3)),
		commandAdvertised: commandAdvertised ? normalizeForReport(commandAdvertised, replacements) : null,
		commandResponse,
		evidence,
		workflowPayload,
		workflowPayloadError,
		workflowInvariantErrors,
		modelActivityDetected,
		loadError,
		extensionErrors,
		startupUI,
		handlerEvents,
		observableOutput,
		handlerEventOverflow,
		stderr: stderr.trim(),
		stdoutRemainder,
		nonJSONLines,
	};
}

function median(values) {
	if (values.length === 0) return null;
	const sorted = [...values].sort((left, right) => left - right);
	const middle = Math.floor(sorted.length / 2);
	return sorted.length % 2 === 1 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2;
}

function percentile(sorted, fraction) {
	return sorted[Math.max(0, Math.ceil(sorted.length * fraction) - 1)];
}

function latencySummary(values) {
	if (values.length === 0) return { n: 0, medianMs: null, p90Ms: null, madMs: null, noisy: null };
	const sorted = [...values].sort((left, right) => left - right);
	const center = median(sorted);
	const mad = median(sorted.map((value) => Math.abs(value - center)));
	return {
		n: sorted.length,
		medianMs: Number(center.toFixed(3)),
		p90Ms: Number(percentile(sorted, 0.9).toFixed(3)),
		madMs: Number(mad.toFixed(3)),
		noisy: center === 0 ? mad > 0 : mad / center > 0.1,
	};
}

function passStatus(caseKind) {
	return caseKind === "workflow" ? "workflow_smoke_pass" : "command_handler_smoke_pass";
}

function summarizeRuntime(attempts, caseKind) {
	const measured = attempts.filter((attempt) => !attempt.warmup);
	const passed = attempts.filter((attempt) => attempt.success).length;
	const status = passed === attempts.length ? passStatus(caseKind) : passed === 0 ? `${caseKind}_smoke_fail` : "flaky";
	return {
		status,
		attemptCount: attempts.length,
		passedAttemptCount: passed,
		measuredHandlerLatency: latencySummary(measured.filter((attempt) => attempt.success).map((attempt) => attempt.handlerMs)),
		attempts,
	};
}

function canonical(value) {
	if (Array.isArray(value)) return value.map(canonical);
	if (value && typeof value === "object") {
		return Object.fromEntries(
			Object.entries(value)
				.sort(([left], [right]) => compareText(left, right))
				.map(([key, item]) => [key, canonical(item)]),
		);
	}
	return value;
}

function compareWorkflowPayloads(piAttempts, pigoAttempts) {
	const pi = new Set(piAttempts.filter((attempt) => attempt.success).map((attempt) => JSON.stringify(canonical(attempt.workflowPayload))));
	const pigo = new Set(
		pigoAttempts.filter((attempt) => attempt.success).map((attempt) => JSON.stringify(canonical(attempt.workflowPayload))),
	);
	const stable = pi.size === 1 && pigo.size === 1;
	return {
		stableWithinEachRuntime: stable,
		equalAcrossRuntimes: stable && [...pi][0] === [...pigo][0],
		piDistinctPayloadCount: pi.size,
		pigoDistinctPayloadCount: pigo.size,
	};
}

function compareObservableOutputs(piAttempts, pigoAttempts) {
	const pi = new Set(
		piAttempts.filter((attempt) => attempt.success).map((attempt) => JSON.stringify(canonical(attempt.observableOutput))),
	);
	const pigo = new Set(
		pigoAttempts.filter((attempt) => attempt.success).map((attempt) => JSON.stringify(canonical(attempt.observableOutput))),
	);
	const stableWithinEachRuntime = pi.size === 1 && pigo.size === 1;
	return {
		stableWithinEachRuntime,
		equalAcrossRuntimes: stableWithinEachRuntime && [...pi][0] === [...pigo][0],
		piDistinctOutputCount: pi.size,
		pigoDistinctOutputCount: pigo.size,
		piRepresentative: pi.size > 0 ? JSON.parse([...pi][0]) : null,
		pigoRepresentative: pigo.size > 0 ? JSON.parse([...pigo][0]) : null,
	};
}

function compareLatency(pi, pigo) {
	let pigoVsPiRatio = null;
	let ratioSuppressedBecause = null;
	if (pi.medianMs === null || pigo.medianMs === null || pi.medianMs <= 0 || pigo.medianMs <= 0) {
		ratioSuppressedBecause = "below_resolution";
	} else if (pi.noisy || pigo.noisy) {
		ratioSuppressedBecause = "noisy_mad_over_10_percent";
	} else {
		pigoVsPiRatio = Number((pigo.medianMs / pi.medianMs).toFixed(3));
	}
	return {
		metric: "rpc_prompt_write_to_success_response_including_command_handler",
		pi,
		pigo,
		pigoVsPiRatio,
		ratioSuppressedBecause,
	};
}

async function writeAtomic(filename, value) {
	await mkdir(path.dirname(filename), { recursive: true });
	const temporary = path.join(path.dirname(filename), `.${path.basename(filename)}.${process.pid}.${Date.now()}.tmp`);
	await writeFile(temporary, `${JSON.stringify(value, null, 2)}\n`, { flag: "wx" });
	await rename(temporary, filename);
}

async function runCaseGroup(cases, caseKind, options) {
	const results = [];
	for (let caseIndex = 0; caseIndex < cases.length; caseIndex++) {
		const smokeCase = cases[caseIndex];
		const entries =
			caseKind === "workflow" ? [] : await extensionEntrypoints(options.packages, smokeCase.extension);
		const attempts = { pi: [], pigo: [] };
		const total = options.warmups + options.samples;
		for (let attemptIndex = 0; attemptIndex < total; attemptIndex++) {
			const warmup = attemptIndex < options.warmups;
			const sequence = (caseIndex + attemptIndex) % 2 === 0 ? ["pi", "pigo"] : ["pigo", "pi"];
			for (const runtimeName of sequence) {
				process.stderr.write(
					`[${caseKind}-smoke] ${smokeCase.id} ${warmup ? "warmup" : "sample"} ${attemptIndex + 1}/${total} ${runtimeName}\n`,
				);
				const executable = runtimeName === "pi" ? options.pi : options.pigo;
				let attempt;
				try {
					attempt = await runAttempt(runtimeName, executable, entries, smokeCase, options);
				} catch (error) {
					attempt = {
						runtime: runtimeName,
						success: false,
						failureKinds: ["harness_error"],
						error: error instanceof Error ? error.message : String(error),
						handlerMs: null,
					};
				}
				attempt.warmup = warmup;
				attempt.phase = warmup ? "warmup" : "measured";
				attempt.sample = warmup ? attemptIndex + 1 : attemptIndex - options.warmups + 1;
				attempts[runtimeName].push(attempt);
			}
		}
		const pi = summarizeRuntime(attempts.pi, caseKind);
		const pigo = summarizeRuntime(attempts.pigo, caseKind);
		const payload = caseKind === "workflow" ? compareWorkflowPayloads(attempts.pi, attempts.pigo) : null;
		const output = compareObservableOutputs(attempts.pi, attempts.pigo);
		const parity =
			pi.status === passStatus(caseKind) &&
			pigo.status === passStatus(caseKind) &&
			output.equalAcrossRuntimes &&
			(payload?.equalAcrossRuntimes ?? true);
		results.push({
			id: smokeCase.id,
			rank: smokeCase.rank,
			package: smokeCase.package,
			version: smokeCase.extension.version,
			integrity: smokeCase.extension.integrity,
			...(caseKind === "workflow"
				? {
					module: path.join(packageDirectory(options.packages, smokeCase.package), smokeCase.module),
					entrypoint: "generated disposable wrapper",
					fixtures: smokeCase.fixtures,
				  }
				: { entrypoints: entries }),
			command: smokeCase.command,
			args: smokeCase.args,
			evidencePatterns: smokeCase.evidencePatterns,
			rationale: smokeCase.rationale,
			pi,
			pigo,
			comparison: {
				parity,
				observableOutput: output,
				handlerLatency: compareLatency(pi.measuredHandlerLatency, pigo.measuredHandlerLatency),
				...(payload ? { workflowPayload: payload } : {}),
			},
		});
	}
	return results;
}

async function main() {
	const options = parseArgs(process.argv.slice(2));
	if (options.help) {
		process.stdout.write(`${usage()}\n`);
		return;
	}
	const inputs = await loadInputs(options.cases, options.corpus, options.only);
	if (options.validateOnly) {
		process.stdout.write(
			`${JSON.stringify({
				valid: true,
				commandCount: inputs.manifest.totalCommandCount,
				workflowCount: inputs.manifest.totalWorkflowCount,
				selectedCommandCount: inputs.manifest.cases.length,
				selectedWorkflowCount: inputs.manifest.workflowCases.length,
			})}\n`,
		);
		return;
	}

	const network = await inspectNetworkIsolation();
	if (!network.isolated && !options.allowNetwork) {
		throw new Error(
			`network isolation check failed via ${network.method}; run inside a networkless container or pass --allow-network for a non-proof diagnostic run`,
		);
	}
	const [piVersion, pigoVersion] = [executableVersion(options.pi), executableVersion(options.pigo)];
	if (piVersion.status !== 0 || piVersion.stdout !== inputs.manifest.upstreamVersion) {
		throw new Error(`upstream Pi must report exactly ${inputs.manifest.upstreamVersion}; got ${JSON.stringify(piVersion)}`);
	}
	if (pigoVersion.status !== 0 || !pigoVersion.stdout.includes(`upstream pi ${inputs.manifest.upstreamVersion}`)) {
		throw new Error(`Pigo must report upstream pi ${inputs.manifest.upstreamVersion}; got ${JSON.stringify(pigoVersion)}`);
	}

	const packageLock = path.join(options.packages, "package-lock.json");
	let packageLockSHA256 = null;
	try {
		packageLockSHA256 = await fileDigest(packageLock);
	} catch {}
	const commandResults = await runCaseGroup(inputs.manifest.cases, "command", options);
	const workflowResults = await runCaseGroup(inputs.manifest.workflowCases, "workflow", options);
	const commandPassCount = commandResults.filter((result) => result.comparison.parity).length;
	const workflowPassCount = workflowResults.filter((result) => result.comparison.parity).length;
	const report = {
		schemaVersion: 1,
		generatedAt: new Date().toISOString(),
		claimScope: inputs.manifest.claimScope,
		safety: {
			networkNamespaceGuard: network,
			allowNetworkOverrideUsed: options.allowNetwork,
			credentialsInherited: false,
			dummyAPIKey: true,
			proxySink: "127.0.0.1:9",
			modelActivityFailsAttempt: true,
			dialogRequests: "cancelled",
		},
		inputs: {
			harness: { path: fileURLToPath(import.meta.url), sha256: await fileDigest(fileURLToPath(import.meta.url)) },
			upstreamPi: { path: options.pi, sha256: await fileDigest(options.pi), version: piVersion },
			pigo: { path: options.pigo, sha256: await fileDigest(options.pigo), version: pigoVersion },
			packages: options.packages,
			packageLockSHA256,
			cases: { path: options.cases, sha256: sha256(inputs.caseBytes) },
			corpus: { path: options.corpus, sha256: sha256(inputs.corpusBytes) },
			warmups: options.warmups,
			samples: options.samples,
			timeoutMs: options.timeoutMs,
			settleMs: options.settleMs,
		},
		inspectedExclusions: inputs.manifest.inspectedExclusions,
		summary: {
			commandCaseCount: commandResults.length,
			commandPassCount,
			allCommandsComparedPass: commandPassCount === commandResults.length,
			piCommandPassCount: commandResults.filter((result) => result.pi.status === passStatus("command")).length,
			pigoCommandPassCount: commandResults.filter((result) => result.pigo.status === passStatus("command")).length,
			workflowCaseCount: workflowResults.length,
			workflowPassCount,
			allWorkflowsComparedPass: workflowPassCount === workflowResults.length,
			piWorkflowPassCount: workflowResults.filter((result) => result.pi.status === passStatus("workflow")).length,
			pigoWorkflowPassCount: workflowResults.filter((result) => result.pigo.status === passStatus("workflow")).length,
			allComparedPass: commandPassCount === commandResults.length && workflowPassCount === workflowResults.length,
		},
		commandResults,
		workflowResults,
	};
	await writeAtomic(options.output, report);
	process.stdout.write(`${options.output}\n`);
	if (!report.summary.allComparedPass) process.exitCode = 2;
}

main().catch((error) => {
	process.stderr.write(`smoke harness error: ${error instanceof Error ? error.stack ?? error.message : String(error)}\n`);
	process.exitCode = 1;
});
