import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

const FIXED_NOW = "2026-07-17T01:02:03.456Z";

const input =
	[
		'{"type":"session","version":3,"id":"export-session","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/fixture/work","futureHeader":{"kept":true}}',
		'{"type":"message","futureBefore":{"z":1},"id":"root","parentId":"wrong-root","timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":"root"},"futureAfter":"<>&"}',
		'{"type":"message","id":"abandoned","parentId":"root","timestamp":"2025-01-01T00:00:02.000Z","message":{"role":"assistant","content":"not exported"}}',
		'{"type":"custom","id":"chosen","unknown":42,"parentId":"root","timestamp":"2025-01-01T00:00:03.000Z","customType":"fixture","data":{"b":2,"a":1}}',
		'{"type":"message","id":"leaf","parentId":"chosen","timestamp":"2025-01-01T00:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"leaf"}]},"tail":[3,2,1]}',
	].join("\n") + "\n";

async function sha256(filePath: string): Promise<string> {
	return createHash("sha256")
		.update(await readFile(filePath))
		.digest("hex");
}

export async function generateF12ExportJSONL(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const source = "packages/coding-agent/src/core/agent-session.ts";
	const sources = [
		source,
		"packages/coding-agent/src/core/session-manager.ts",
		"packages/coding-agent/src/utils/paths.ts",
	];
	const module = await import(
		pathToFileURL(path.join(upstreamRoot, source)).href
	);
	const exportToJsonl = Reflect.get(
		module.AgentSession.prototype,
		"exportToJsonl",
	);
	if (typeof exportToJsonl !== "function")
		throw new Error("AgentSession.exportToJsonl not found");

	const parsed = input
		.trimEnd()
		.split("\n")
		.slice(1)
		.map((line) => JSON.parse(line) as Record<string, unknown>);
	const byID = new Map(parsed.map((entry) => [entry.id, entry]));
	const branch = ["root", "chosen", "leaf"].map((id) => byID.get(id));
	if (branch.some((entry) => entry === undefined))
		throw new Error("fixture branch is incomplete");

	const temporary = await mkdtemp(path.join(tmpdir(), "pi-f12-export-jsonl-"));
	try {
		const outputPath = path.join(temporary, "export.jsonl");
		const OriginalDate = globalThis.Date;
		class FixedDate extends OriginalDate {
			constructor(value?: string | number) {
				super(value ?? FIXED_NOW);
			}
			static override now(): number {
				return new OriginalDate(FIXED_NOW).getTime();
			}
		}
		globalThis.Date = FixedDate as DateConstructor;
		try {
			exportToJsonl.call(
				{
					sessionManager: {
						getSessionId: () => "export-session",
						getCwd: () => "/fixture/work",
						getBranch: () => branch,
					},
				},
				outputPath,
			);
		} finally {
			globalThis.Date = OriginalDate;
		}

		const outputDir = path.join(outputRoot, "F12-export-jsonl");
		await mkdir(outputDir, { recursive: true });
		await writeFile(
			path.join(outputDir, "case.json"),
			`${JSON.stringify(
				{
					schema: 1,
					nowUnixMilli: new OriginalDate(FIXED_NOW).getTime(),
					input,
					expected: await readFile(outputPath, "utf8"),
				},
				null,
				2,
			)}\n`,
		);
		await writeFile(
			path.join(outputDir, "manifest.json"),
			`${JSON.stringify(
				{
					family: "F12-export-jsonl",
					schema: 1,
					upstream: {
						commit: upstreamCommit,
						files: await Promise.all(
							sources.map(async (sourcePath) => ({
								path: sourcePath,
								sha256: await sha256(path.join(upstreamRoot, sourcePath)),
							})),
						),
					},
					cases: ["branched-unknown-fields"],
				},
				null,
				2,
			)}\n`,
		);
	} finally {
		await rm(temporary, { recursive: true, force: true });
	}
}
