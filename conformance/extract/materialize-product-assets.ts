import { copyFile, mkdir } from "node:fs/promises";
import path from "node:path";

const upstreamRoot = path.resolve(process.argv[2] ?? ".upstream");
const repositoryRoot = path.resolve(process.argv[3] ?? ".");
const source = path.join(upstreamRoot, "packages/coding-agent/CHANGELOG.md");
const destination = path.join(
	repositoryRoot,
	"codingagent/modes/assets/CHANGELOG.md",
);

await mkdir(path.dirname(destination), { recursive: true });
await copyFile(source, destination);
