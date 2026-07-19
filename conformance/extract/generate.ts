import path from "node:path";

import { generateF1 } from "./f1-messages.ts";
import { generateF1PartialJSON } from "./f1-partialjson.ts";
import { generateF1Schema } from "./f1-schema.ts";
import { generateF2 } from "./f2-openai.ts";
import { generateF2Codex } from "./f2-codex.ts";
import { generateF3 } from "./f3-agent.ts";
import { generateF3Session } from "./f3-session.ts";
import { generateF4 } from "./f4-edit.ts";
import { generateF5 } from "./f5-truncation.ts";
import { generateF6 } from "./f6-session.ts";
import { generateF6Harness } from "./f6-harness.ts";
import { generateF7 } from "./f7-rpc.ts";
import { generateF7CLI } from "./f7-cli.ts";
import { generateF8 } from "./f8-slash-templates.ts";
import { generateF9 } from "./f9-system-prompt.ts";
import { generateWP250 } from "./wp250-models.ts";
import { generateF10 } from "./f10-compaction.ts";
import { generateF11ExtensionRunner } from "./f11-extension-runner.ts";
import { generateF11ExtensionWiring } from "./f11-extension-wiring.ts";
import { generateF11JSBridge } from "./f11-jsbridge.ts";
import { generateWP360 } from "./wp360-packages.ts";
import { generateF12 } from "./f12-tui.ts";
import { generateF12App } from "./f12-app.ts";
import { generateF12Commands } from "./f12-commands.ts";
import { generateF12ExportJSONL } from "./f12-export-jsonl.ts";
import { generateF12Shutdown } from "./f12-shutdown.ts";
import { generateF12UILifecycle } from "./f12-ui-lifecycle.ts";
import { generateF12VisibleCommands } from "./f12-visible-commands.ts";
import { generateWP440 } from "./wp440-images.ts";
import { generateWP440Read } from "./wp440-read.ts";
import { generateWP370Runtime } from "./wp370-runtime.ts";
import { generateWP450Replay } from "./wp450-replay.ts";
import { generateWP450SessionSelector } from "./wp450-session-selector.ts";

// Goldens are pinned in 256-color mode; upstream capability detection reads
// COLORTERM (packages/tui/src/terminal-image.ts), so the invoking terminal
// must never leak truecolor into extraction.
delete process.env.COLORTERM;

const upstreamRoot = process.cwd();
const outputRoot = path.resolve(
	upstreamRoot,
	process.argv[2] ?? "../conformance/fixtures",
);
const upstreamCommit = process.argv[3];
if (!upstreamCommit) {
	throw new Error("upstream commit argument is required");
}

const generators = [
	generateF1,
	generateF1PartialJSON,
	generateF1Schema,
	generateF2,
	generateF2Codex,
	generateF3,
	generateF3Session,
	generateF4,
	generateF5,
	generateF6,
	generateF6Harness,
	generateF7,
	generateF7CLI,
	generateF8,
	generateF9,
	generateWP250,
	generateF10,
	generateF11ExtensionRunner,
	generateF11ExtensionWiring,
	generateWP360,
	generateF12,
	generateF12App,
	generateF12Commands,
	generateF12ExportJSONL,
	generateF12Shutdown,
	generateF12UILifecycle,
	generateF12VisibleCommands,
	generateWP440,
	generateWP440Read,
	generateWP370Runtime,
	generateWP450Replay,
	generateWP450SessionSelector,
	generateF11JSBridge,
];
for (const generate of generators) {
	await generate(upstreamRoot, outputRoot, upstreamCommit);
}
