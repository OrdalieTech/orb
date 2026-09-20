import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type ToolDefinition = {
	name: string;
	constrainedSampling?: unknown;
	execute: (...args: unknown[]) => Promise<unknown>;
};

type ToolModule = Record<string, (...args: unknown[]) => ToolDefinition>;

async function executeError(tool: ToolDefinition, input: Record<string, unknown>): Promise<string> {
	try {
		await tool.execute("fixture-call", input);
		throw new Error("fixture command unexpectedly succeeded");
	} catch (error) {
		return error instanceof Error ? error.message : String(error);
	}
}

export async function generateF11BuiltInTools(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const sources = {
		bash: "packages/coding-agent/src/core/tools/bash.ts",
		read: "packages/coding-agent/src/core/tools/read.ts",
		edit: "packages/coding-agent/src/core/tools/edit.ts",
		write: "packages/coding-agent/src/core/tools/write.ts",
	} as const;
	const modules = Object.fromEntries(
		await Promise.all(
			Object.entries(sources).map(async ([name, source]) => [
				name,
				(await import(pathToFileURL(path.join(upstreamRoot, source)).href)) as ToolModule,
			]),
		),
	) as Record<keyof typeof sources, ToolModule>;

	const definitions = [
		modules.bash.createBashToolDefinition("/fixture", {
			operations: {
				async exec(_command: string, _cwd: string, options: { onData(data: Buffer): void }) {
					options.onData(Buffer.from("before"));
					return { exitCode: null };
				},
			},
		}),
		modules.read.createReadToolDefinition("/fixture"),
		modules.edit.createEditToolDefinition("/fixture"),
		modules.write.createWriteToolDefinition("/fixture"),
	];
	const localBash = modules.bash.createBashToolDefinition(upstreamRoot, { shellPath: "/bin/sh" });
	const fixture = {
		schemaVersion: 1,
		tools: definitions.map(({ name, constrainedSampling }) => ({ name, constrainedSampling })),
		bash: {
			nullExitError: await executeError(definitions[0], { command: "fixture" }),
			signalExitError: await executeError(localBash, { command: "kill -TERM $$" }),
		},
	};

	const familyDir = path.join(outputRoot, "F11BuiltInTools");
	await mkdir(familyDir, { recursive: true });
	await writeFile(
		path.join(familyDir, "manifest.json"),
		`${JSON.stringify({
			family: "F11BuiltInTools",
			upstreamCommit,
			generator: "conformance/extract/f11-built-in-tools.ts",
			sources: Object.values(sources),
			files: ["tools.json"],
		}, null, 2)}\n`,
	);
	await writeFile(path.join(familyDir, "tools.json"), `${JSON.stringify(fixture, null, 2)}\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
	const upstreamRoot = process.cwd();
	const outputRoot = path.resolve(upstreamRoot, process.argv[2] ?? "../conformance/fixtures");
	const upstreamCommit = process.argv[3];
	if (!upstreamCommit) throw new Error("upstream commit argument is required");
	await generateF11BuiltInTools(upstreamRoot, outputRoot, upstreamCommit);
}
