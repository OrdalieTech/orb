import path from "node:path";

import { generateF1 } from "./f1-messages.ts";
import { generateF1PartialJSON } from "./f1-partialjson.ts";
import { generateF1Schema } from "./f1-schema.ts";
import { generateF2 } from "./f2-openai.ts";
import { generateF5 } from "./f5-truncation.ts";

const upstreamRoot = process.cwd();
const outputRoot = path.resolve(upstreamRoot, process.argv[2] ?? "../conformance/fixtures");
const upstreamCommit = process.argv[3];
if (!upstreamCommit) {
  throw new Error("upstream commit argument is required");
}

await generateF1(upstreamRoot, outputRoot, upstreamCommit);
await generateF1PartialJSON(upstreamRoot, outputRoot, upstreamCommit);
await generateF1Schema(upstreamRoot, outputRoot, upstreamCommit);
await generateF2(upstreamRoot, outputRoot, upstreamCommit);
await generateF5(upstreamRoot, outputRoot, upstreamCommit);
