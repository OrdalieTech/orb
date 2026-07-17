import path from "node:path";

import { generateF5 } from "./f5-truncation.ts";

const upstreamRoot = process.cwd();
const outputRoot = path.resolve(upstreamRoot, process.argv[2] ?? "../conformance/fixtures");
const upstreamCommit = process.argv[3];
if (!upstreamCommit) {
  throw new Error("upstream commit argument is required");
}

await generateF5(upstreamRoot, outputRoot, upstreamCommit);
