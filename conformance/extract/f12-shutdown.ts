import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

type ShutdownCapture = { order: string[]; output: string };

async function sha256(filePath: string): Promise<string> {
	return createHash("sha256")
		.update(await readFile(filePath))
		.digest("hex");
}

export async function generateF12Shutdown(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	process.env.FORCE_COLOR = "3";
	const source =
		"packages/coding-agent/src/modes/interactive/interactive-mode.ts";
	const module = await import(
		pathToFileURL(path.join(upstreamRoot, source)).href
	);
	const shutdown = Reflect.get(module.InteractiveMode.prototype, "shutdown");
	if (typeof shutdown !== "function")
		throw new Error("InteractiveMode.shutdown not found");

	const temporary = await mkdtemp(path.join(tmpdir(), "pi-f12-shutdown-"));
	try {
		const sessionDir = path.join(temporary, "custom pi's sessions");
		const sessionFile = path.join(sessionDir, "session.jsonl");
		await mkdir(sessionDir, { recursive: true });
		await writeFile(sessionFile, "{}\n");

		const capture = async (fromSignal: boolean): Promise<ShutdownCapture> => {
			const order: string[] = [];
			let output = "";
			const sessionManager = {
				isPersisted: () => true,
				getSessionFile: () => sessionFile,
				getSessionId: () => "fixture-session",
				getSessionDir: () => sessionDir,
				usesDefaultSessionDir: () => false,
			};
			const context = Object.create(module.InteractiveMode.prototype) as Record<
				string,
				unknown
			>;
			context.isShuttingDown = false;
			context.runtimeHost = {
				dispose: async () => order.push("dispose"),
				session: { sessionManager },
			};
			context.themeController = {
				disableAutoSync: () => order.push("theme-stop"),
			};
			context.ui = {
				terminal: {
					drainInput: async (milliseconds: number) =>
						order.push(`drain:${milliseconds}`),
				},
			};
			context.stop = () => order.push("stop");

			const stdoutDescriptor = Object.getOwnPropertyDescriptor(
				process.stdout,
				"isTTY",
			);
			const originalWrite = process.stdout.write;
			const originalExit = process.exit;
			Object.defineProperty(process.stdout, "isTTY", {
				configurable: true,
				value: true,
			});
			process.stdout.write = ((chunk: string | Uint8Array) => {
				output +=
					typeof chunk === "string"
						? chunk
						: Buffer.from(chunk).toString("utf8");
				return true;
			}) as typeof process.stdout.write;
			process.exit = ((code?: number) => {
				order.push(`exit:${code ?? 0}`);
				throw new Error("<fixture-exit>");
			}) as typeof process.exit;
			try {
				await shutdown.call(
					context,
					fromSignal ? { fromSignal: true } : undefined,
				);
			} catch (error) {
				if (!(error instanceof Error) || error.message !== "<fixture-exit>")
					throw error;
			} finally {
				process.stdout.write = originalWrite;
				process.exit = originalExit;
				if (stdoutDescriptor)
					Object.defineProperty(process.stdout, "isTTY", stdoutDescriptor);
				else Reflect.deleteProperty(process.stdout, "isTTY");
			}
			return { order, output: output.replaceAll(temporary, "<tmp>") };
		};

		const familyDir = path.join(outputRoot, "F12-shutdown");
		await mkdir(familyDir, { recursive: true });
		await writeFile(
			path.join(familyDir, "cases.json"),
			`${JSON.stringify({ schema: 1, ordinary: await capture(false), signal: await capture(true) }, null, 2)}\n`,
		);
		const sources = [
			source,
			"packages/coding-agent/src/config.ts",
			"packages/coding-agent/test/format-resume-command.test.ts",
			"packages/coding-agent/test/suite/regressions/5080-signal-shutdown-extension-cleanup.test.ts",
		];
		await writeFile(
			path.join(familyDir, "manifest.json"),
			`${JSON.stringify(
				{
					family: "F12-shutdown",
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
					cases: ["ordinary-persisted-custom-dir", "signal-cleanup-order"],
				},
				null,
				2,
			)}\n`,
		);
	} finally {
		await rm(temporary, { recursive: true, force: true });
	}
}
