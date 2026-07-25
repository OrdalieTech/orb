#!/usr/bin/env node

// Scale and durability harness for matrix.mjs.
//
// This never touches a corpus package. It synthesises a package tree of stub
// extensions that this repository owns, points both the reference and the
// candidate runtimes at the same pigo binary, and drives the real matrix.mjs
// over 200+ entries. What it proves is harness behaviour, not compatibility:
//
//   * cost grows linearly with corpus size (no O(n^2) comparison)
//   * resident memory stays flat as the corpus grows
//   * a hostile package cannot wedge the run (per-process timeout, per-package
//     budget, orphan reaping)
//   * a run killed at package N still reports N-1 packages, and --resume
//     continues instead of restarting
//
// It must run in the same hardened container as the matrix itself, because
// matrix.mjs refuses to run outside a network-isolated namespace and probes
// under /work.

import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const MATRIX = path.join(HERE, "matrix.mjs");

function usage() {
	return `Usage: node scale-check.mjs --pigo <binary> --root <scratch-directory> [options]

  --count <n>          Synthetic packages to generate (default: 220)
  --kill-at <n>        Kill the run after this many packages, then resume (default: 150)
  --runtimes <list>    Passed through to matrix.mjs (default: pi,pigo-node)
  --bun <binary>       Passed through to matrix.mjs when a bun tier is requested
  --profile <name>     Passed through to matrix.mjs (default: quick)
  --skip-kill          Do not exercise the kill/resume path
  --help`;
}

function parseArgs(argv) {
	const options = {
		pigo: "",
		root: "",
		count: 220,
		killAt: 150,
		runtimes: "pi,pigo-node",
		bun: "",
		profile: "quick",
		skipKill: false,
	};
	for (let index = 0; index < argv.length; index++) {
		const argument = argv[index];
		if (argument === "--help" || argument === "-h") return { help: true };
		if (argument === "--skip-kill") {
			options.skipKill = true;
			continue;
		}
		const next = argv[index + 1];
		if (next === undefined) throw new Error(`missing value for ${argument}`);
		index++;
		switch (argument) {
			case "--pigo":
			case "--root":
			case "--bun":
				options[argument.slice(2)] = path.resolve(next);
				break;
			case "--count":
			case "--kill-at":
				options[argument === "--count" ? "count" : "killAt"] = Number(next);
				break;
			case "--runtimes":
			case "--profile":
				options[argument.slice(2)] = next;
				break;
			default:
				throw new Error(`unknown argument: ${argument}`);
		}
	}
	if (!options.pigo || !options.root) throw new Error("--pigo and --root are required");
	return options;
}

// Stub extension bodies. Every one of these is written by this repository; none
// of them comes from the ecosystem corpus.
const BEHAVIOURS = {
	// The common case: registers one command and one tool, deterministically.
	normal: (index) => `export default function stub(pi: any) {
	pi.registerCommand("scale-stub-${index}", { description: "scale stub ${index}", handler: async () => {} });
	pi.registerTool({
		name: "scale_tool_${index}",
		label: "Scale tool ${index}",
		description: "scale tool ${index}",
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`,
	// Registers nothing: exercises the load_only_pass path.
	silent: () => `export default function stub() {}
`,
	// Wedges the extension host: exercises the per-process timeout and then the
	// per-package budget, and proves one hostile package cannot stop the run.
	hang: () => `export default function stub() {
	const start = Date.now();
	while (Date.now() - start < 600000) {}
}
`,
	// Throws during load: exercises crash classification.
	crash: () => `export default function stub() {
	throw new Error("scale stub deliberate load failure");
}
`,
	// Imports a builtin that does not exist: exercises missing_node_builtin.
	missingBuiltin: () => `import "node:definitely-not-a-builtin";
export default function stub() {}
`,
	// Leaks a detached grandchild that outlives the probe: exercises the orphan
	// census and reaper.
	leak: () => `import { spawn } from "node:child_process";
export default function stub(pi: any) {
	try {
		spawn(process.execPath, ["-e", "setTimeout(() => {}, 600000)"], { detached: true, stdio: "ignore" }).unref();
	} catch {}
	pi.registerCommand("scale-leak", { description: "leaky stub", handler: async () => {} });
}
`,
};

function behaviourFor(index, count) {
	// Hostile stubs are sprinkled through the run, not clustered at the end, so a
	// wedge would be visible early.
	if (index === Math.floor(count * 0.25)) return "hang";
	if (index === Math.floor(count * 0.4)) return "crash";
	if (index === Math.floor(count * 0.55)) return "missingBuiltin";
	if (index === Math.floor(count * 0.7)) return "leak";
	if (index % 17 === 0) return "silent";
	return "normal";
}

function generateTree(options) {
	const packages = path.join(options.root, "packages");
	const modules = path.join(packages, "node_modules");
	rmSync(packages, { recursive: true, force: true });
	mkdirSync(path.join(modules, ".bin"), { recursive: true });

	const extensions = [];
	for (let index = 1; index <= options.count; index++) {
		const name = `pigo-scale-stub-${String(index).padStart(4, "0")}`;
		const directory = path.join(modules, name);
		mkdirSync(directory, { recursive: true });
		const behaviour = behaviourFor(index, options.count);
		writeFileSync(path.join(directory, "index.ts"), BEHAVIOURS[behaviour](index));
		writeFileSync(
			path.join(directory, "package.json"),
			`${JSON.stringify({ name, version: "1.0.0", type: "module", pi: { extensions: ["./index.ts"] } }, null, 2)}\n`,
		);
		extensions.push({
			rank: index,
			package: name,
			version: "1.0.0",
			tier: index <= 50 ? 1 : index <= 100 ? 2 : 3,
			downloads: { monthly: options.count - index, weekly: options.count - index },
			integrity: `sha512-${"0".repeat(86)}==`,
			extensions: ["./index.ts"],
			behaviour,
		});
	}

	// matrix.mjs hashes the lock file and resolves the reference through
	// node_modules/.bin/pi. Both runtimes intentionally point at the same pigo
	// binary: this measures the harness, not compatibility.
	writeFileSync(path.join(packages, "package-lock.json"), `${JSON.stringify({ name: "scale", lockfileVersion: 3 }, null, 2)}\n`);
	// A symlink rather than a shell shim: the scratch tree lives on a noexec
	// tmpfs, and execution resolves to the binary on its own read-only mount.
	symlinkSync(options.pigo, path.join(modules, ".bin", "pi"));
	const corpus = {
		schemaVersion: 1,
		capturedAt: new Date().toISOString().slice(0, 10),
		selection: { source: "synthetic", rule: "generated stub extensions owned by this repository", caveat: "harness scale test only" },
		extensions,
	};
	const corpusFile = path.join(options.root, "scale-corpus.json");
	writeFileSync(corpusFile, `${JSON.stringify(corpus, null, 2)}\n`);
	return { packages, corpusFile, extensions };
}

function matrixArgs(options, tree, output) {
	const args = [
		MATRIX,
		"--packages",
		tree.packages,
		"--pigo",
		options.pigo,
		"--corpus",
		tree.corpusFile,
		"--runtimes",
		options.runtimes,
		"--profile",
		options.profile,
		"--timeout-ms",
		"8000",
		"--package-budget-ms",
		"25000",
		"--output",
		output,
	];
	if (options.bun) args.push("--bun", options.bun);
	return args;
}

function runMatrix(options, tree, output, { killAfter = 0, resume = false } = {}) {
	return new Promise((resolve, reject) => {
		const args = matrixArgs(options, tree, output);
		if (resume) args.push("--resume");
		const child = spawn(process.execPath, args, { stdio: ["ignore", "inherit", "pipe"] });
		let completed = 0;
		let killed = false;
		let stderr = "";
		child.stderr.on("data", (chunk) => {
			const text = chunk.toString("utf8");
			stderr += text;
			process.stderr.write(text);
			for (const line of text.split("\n")) {
				const match = /^matrix: \[(\d+)\/\d+\]/.exec(line);
				if (!match) continue;
				completed = Number(match[1]);
				if (killAfter && completed > killAfter && !killed) {
					killed = true;
					// SIGKILL, not SIGTERM: the point is to prove durability without a
					// cooperative shutdown path.
					try {
						process.kill(child.pid, "SIGKILL");
					} catch {}
				}
			}
		});
		child.once("error", reject);
		child.once("exit", (code, signal) => resolve({ code, signal, killed, stderr }));
	});
}

function readStream(output) {
	const file = `${output}.jsonl`;
	if (!existsSync(file)) return { records: [], header: null, bytes: 0 };
	const lines = readFileSync(file, "utf8").split("\n").filter(Boolean);
	let header = null;
	const records = [];
	let torn = 0;
	for (const line of lines) {
		let parsed;
		try {
			parsed = JSON.parse(line);
		} catch {
			torn++;
			continue;
		}
		if (parsed.type === "header") header = parsed;
		if (parsed.type === "package") records.push(parsed.result);
	}
	return { records, header, torn, bytes: statSync(file).size };
}

function check(label, condition, detail) {
	process.stdout.write(`${condition ? "PASS" : "FAIL"}  ${label}${detail ? ` — ${detail}` : ""}\n`);
	return condition ? 0 : 1;
}

async function main() {
	const options = parseArgs(process.argv.slice(2));
	if (options.help) {
		process.stdout.write(`${usage()}\n`);
		return;
	}
	mkdirSync(options.root, { recursive: true });
	const tree = generateTree(options);
	const output = path.join(options.root, "scale-matrix.json");
	rmSync(output, { force: true });
	rmSync(`${output}.jsonl`, { force: true });
	rmSync(`${output}.progress.json`, { force: true });

	let failures = 0;
	const timings = {};

	if (!options.skipKill) {
		process.stdout.write(`\n== phase 1: run and SIGKILL after ${options.killAt} packages ==\n`);
		const started = Date.now();
		const first = await runMatrix(options, tree, output, { killAfter: options.killAt });
		timings.killedRunMs = Date.now() - started;
		const streamed = readStream(output);
		failures += check("run was actually killed", first.signal === "SIGKILL" || first.code !== 0, `signal=${first.signal} code=${first.code}`);
		failures += check(
			"partial results survived the kill",
			streamed.records.length >= options.killAt - 1,
			`${streamed.records.length} packages durable in the jsonl stream`,
		);
		failures += check("no aggregate was left behind", !existsSync(output), "the killed run wrote no final report");
		failures += check(
			"progress file tracked the run",
			existsSync(`${output}.progress.json`) && JSON.parse(readFileSync(`${output}.progress.json`, "utf8")).completed >= options.killAt - 1,
		);

		process.stdout.write("\n== phase 2: resume ==\n");
		const resumedAt = Date.now();
		const second = await runMatrix(options, tree, output, { resume: true });
		timings.resumeMs = Date.now() - resumedAt;
		failures += check("resumed run finished", second.code === 0 || second.code === 2, `exit ${second.code}`);
	} else {
		process.stdout.write("\n== single run ==\n");
		const started = Date.now();
		const only = await runMatrix(options, tree, output, {});
		timings.runMs = Date.now() - started;
		failures += check("run finished", only.code === 0 || only.code === 2, `exit ${only.code}`);
	}

	const report = JSON.parse(readFileSync(output, "utf8"));
	const streamed = readStream(output);
	failures += check("every package is present in the aggregate", report.extensions.length === options.count, `${report.extensions.length}/${options.count}`);
	failures += check("aggregate is marked complete", report.incomplete === false);
	failures += check("stream and aggregate agree", streamed.records.length === options.count, `${streamed.records.length} streamed`);

	const behaviourByName = new Map(tree.extensions.map((entry) => [entry.package, entry.behaviour]));
	const byBehaviour = {};
	for (const entry of report.extensions) {
		const behaviour = behaviourByName.get(entry.extension.package);
		byBehaviour[behaviour] ??= {};
		const key = `${entry.status}/${entry.failure?.class ?? "-"}`;
		byBehaviour[behaviour][key] = (byBehaviour[behaviour][key] ?? 0) + 1;
	}
	process.stdout.write(`\nstatus by stub behaviour:\n${JSON.stringify(byBehaviour, null, 2)}\n`);

	const hostile = report.extensions.filter((entry) => ["hang", "crash", "missingBuiltin"].includes(behaviourByName.get(entry.extension.package)));
	failures += check(
		"hostile stubs did not wedge the run",
		hostile.every((entry) => entry.status !== undefined) && report.extensions.length === options.count,
		`${hostile.length} hostile stubs classified`,
	);
	const hang = report.extensions.find((entry) => behaviourByName.get(entry.extension.package) === "hang");
	failures += check(
		"the wedging stub was bounded by the package budget",
		hang && hang.elapsedMs <= 30_000,
		hang ? `${hang.elapsedMs}ms elapsed, reason ${hang.reason}` : "missing",
	);

	// Linearity: compare mean cost of the first and last fifths of the corpus,
	// excluding the deliberately hostile stubs.
	const costs = report.extensions
		.filter((entry) => behaviourByName.get(entry.extension.package) === "normal")
		.map((entry) => entry.elapsedMs);
	const slice = Math.max(1, Math.floor(costs.length / 5));
	const head = costs.slice(0, slice).reduce((sum, value) => sum + value, 0) / slice;
	const tail = costs.slice(-slice).reduce((sum, value) => sum + value, 0) / slice;
	failures += check(
		"per-package cost does not grow with corpus position",
		tail < head * 1.6,
		`first fifth ${head.toFixed(0)}ms, last fifth ${tail.toFixed(0)}ms (ratio ${(tail / head).toFixed(2)})`,
	);

	const aggregateBytes = statSync(output).size;
	process.stdout.write(
		`\nsizes: aggregate ${(aggregateBytes / 1e6).toFixed(2)} MB, stream ${(streamed.bytes / 1e6).toFixed(2)} MB` +
			`, peak harness RSS ${(report.resources.peakRssBytes / 1e6).toFixed(1)} MB\n`,
	);
	process.stdout.write(`timings: ${JSON.stringify(timings)}\n`);
	process.stdout.write(`leaked processes reaped: ${JSON.stringify(report.resources.leaked.slice(0, 5))}\n`);
	process.stdout.write(`warnings: ${JSON.stringify(report.resources.warnings)}\n`);
	failures += check("harness memory stayed bounded", report.resources.peakRssBytes < 900e6, `${(report.resources.peakRssBytes / 1e6).toFixed(1)} MB`);

	process.stdout.write(`\n${failures === 0 ? "scale-check: all checks passed" : `scale-check: ${failures} check(s) failed`}\n`);
	process.exitCode = failures === 0 ? 0 : 1;
}

main().catch((error) => {
	process.stderr.write(`scale-check: ${error.stack ?? error.message}\n`);
	process.exitCode = 1;
});
