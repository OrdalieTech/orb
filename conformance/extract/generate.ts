import path from "node:path";

import { generateF1 } from "./f1-messages.ts";
import { generateF1PartialJSON } from "./f1-partialjson.ts";
import { generateF1Schema } from "./f1-schema.ts";
import { generateF2 } from "./f2-openai.ts";
import { generateF3 } from "./f3-agent.ts";
import { generateF4 } from "./f4-edit.ts";
import { generateF5 } from "./f5-truncation.ts";
import { generateF6 } from "./f6-session.ts";
import { generateF9 } from "./f9-system-prompt.ts";
import { generateWP250 } from "./wp250-models.ts";
import { generateF10 } from "./f10-compaction.ts";

const upstreamRoot = process.cwd();
const outputRoot = path.resolve(upstreamRoot, process.argv[2] ?? "../conformance/fixtures");
const upstreamCommit = process.argv[3];
if (!upstreamCommit) {
  throw new Error("upstream commit argument is required");
}

const generators = [
  generateF1,
  generateF1PartialJSON,
  generateF1Schema,
  generateF2,
  generateF3,
  generateF4,
  generateF5,
  generateF6,
  generateF9,
  generateWP250,
  generateF10,
];
for (const generate of generators) {
  await generate(upstreamRoot, outputRoot, upstreamCommit);
}
