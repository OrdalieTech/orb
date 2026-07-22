#!/usr/bin/env node

import { copyFile, mkdir, readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DEFAULT_CORPUS = path.join(HERE, "corpus.json");
const PACKAGE_MANIFEST = path.join(HERE, "package.json");
const PACKAGE_LOCK = path.join(HERE, "package-lock.json");
const PI_PACKAGE = "@earendil-works/pi-coding-agent";
const PI_VERSION = "0.81.1";

function usage() {
	return `Usage: node prepare.mjs --output <empty-directory> [--corpus <corpus.json>]

Installs the committed package-lock with npm ci. The output is intended to be
mounted read-only by matrix.mjs. npm lifecycle scripts are always disabled.`;
}

function parseArgs(argv) {
	const options = { corpus: DEFAULT_CORPUS, output: "" };
	for (let index = 0; index < argv.length; index++) {
		const argument = argv[index];
		if (argument === "--help" || argument === "-h") return { help: true };
		if ((argument === "--corpus" || argument === "--output") && index + 1 < argv.length) {
			options[argument.slice(2)] = path.resolve(argv[++index]);
			continue;
		}
		throw new Error(`unknown or incomplete argument: ${argument}`);
	}
	if (!options.output) throw new Error("--output is required");
	return options;
}

async function readCorpus(filename) {
	const corpus = JSON.parse(await readFile(filename, "utf8"));
	if (corpus.schemaVersion !== 1 || !Array.isArray(corpus.extensions) || corpus.extensions.length === 0) {
		throw new Error(`${filename} is not an extension corpus v1`);
	}
	const names = new Set();
	for (const extension of corpus.extensions) {
		if (!Number.isInteger(extension.rank) || typeof extension.package !== "string" || typeof extension.version !== "string") {
			throw new Error(`${filename} contains an invalid extension record`);
		}
		if (names.has(extension.package)) throw new Error(`${filename} contains duplicate package ${extension.package}`);
		names.add(extension.package);
	}
	return corpus;
}

async function runNPM(output) {
	await new Promise((resolve, reject) => {
		const child = spawn(
			"npm",
			["ci", "--ignore-scripts", "--legacy-peer-deps", "--no-audit", "--no-fund"],
			{ cwd: output, stdio: "inherit", env: process.env },
		);
		child.once("error", reject);
		child.once("exit", (code, signal) => {
			if (code === 0) resolve();
			else reject(new Error(`npm install failed (${signal ?? `exit ${code}`})`));
		});
	});
}

function expectedDependencies(corpus) {
	const dependencies = { [PI_PACKAGE]: PI_VERSION };
	for (const extension of corpus.extensions) dependencies[extension.package] = extension.version;
	return dependencies;
}

async function verifyManifest(corpus) {
	const manifest = JSON.parse(await readFile(PACKAGE_MANIFEST, "utf8"));
	const expected = expectedDependencies(corpus);
	if (JSON.stringify(Object.entries(manifest.dependencies ?? {}).sort()) !== JSON.stringify(Object.entries(expected).sort())) {
		throw new Error("package.json dependencies do not match corpus.json");
	}
}

async function verifyLock(output, corpus) {
	const lock = JSON.parse(await readFile(path.join(output, "package-lock.json"), "utf8"));
	for (const extension of corpus.extensions) {
		const key = `node_modules/${extension.package}`;
		const record = lock.packages?.[key];
		if (!record) throw new Error(`package lock is missing ${extension.package}`);
		if (record.version !== extension.version) {
			throw new Error(`${extension.package}: installed ${record.version}, expected ${extension.version}`);
		}
		if (record.integrity !== extension.integrity) {
			throw new Error(`${extension.package}: integrity differs from corpus`);
		}
	}
	const pi = lock.packages?.[`node_modules/${PI_PACKAGE}`];
	if (!pi || pi.version !== PI_VERSION) throw new Error(`${PI_PACKAGE}@${PI_VERSION} was not installed exactly`);
}

async function main() {
	const options = parseArgs(process.argv.slice(2));
	if (options.help) {
		process.stdout.write(usage() + "\n");
		return;
	}
	const corpus = await readCorpus(options.corpus);
	await verifyManifest(corpus);
	await mkdir(options.output, { recursive: true });
	if ((await readdir(options.output)).length !== 0) {
		throw new Error(`--output must be an empty disposable directory: ${options.output}`);
	}

	await Promise.all([
		copyFile(PACKAGE_MANIFEST, path.join(options.output, "package.json")),
		copyFile(PACKAGE_LOCK, path.join(options.output, "package-lock.json")),
	]);
	await runNPM(options.output);
	await verifyLock(options.output, corpus);
}

main().catch((error) => {
	process.stderr.write(`prepare: ${error.message}\n`);
	process.exitCode = 1;
});
