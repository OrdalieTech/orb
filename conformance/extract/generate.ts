import path from "node:path";

import { stubGrokMermaid } from "./stub-grok-mermaid.ts";

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
import { generateF6Release } from "./f6-release.ts";
import { generateF6Harness } from "./f6-harness.ts";
import { generateF7 } from "./f7-rpc.ts";
import { generateF7CLI } from "./f7-cli.ts";
import { generateF8 } from "./f8-slash-templates.ts";
import { generateF9 } from "./f9-system-prompt.ts";
import { generateWP250 } from "./wp250-models.ts";
import { generateF10 } from "./f10-compaction.ts";
import { generateF11ExtensionRunner } from "./f11-extension-runner.ts";
import { generateF11ExtensionWiring } from "./f11-extension-wiring.ts";
import { generateWP360 } from "./wp360-packages.ts";
import { generateF13DynamicWorkflows } from "./f13-dynamic-workflows.ts";
import { generateWP440 } from "./wp440-images.ts";
import { generateWP440Read } from "./wp440-read.ts";
import { generateWP370Runtime } from "./wp370-runtime.ts";
import { generateWP450SessionSelector } from "./wp450-session-selector.ts";

// The D35 render-golden families (F12* and the WP450 replay/UI demos) are
// Orb-owned snapshots: they regenerate from Orb's own renderer via
// `make fixtures-tui` (ORB_UPDATE_F12=1), never from upstream extraction.

// Goldens are pinned in 256-color mode; upstream capability detection reads
// COLORTERM and the terminal-identity variables below (packages/tui/src/
// terminal-image.ts detectCapabilities), so the invoking terminal must never
// leak truecolor into extraction. GHOSTTY_RESOURCES_DIR alone flips the theme
// to truecolor even with COLORTERM unset.
for (const name of [
	"COLORTERM",
	"FORCE_COLOR",
	"TERM_PROGRAM",
	"GHOSTTY_RESOURCES_DIR",
	"GHOSTTY_BIN_DIR",
	"KITTY_WINDOW_ID",
	"WEZTERM_PANE",
	"TMUX",
]) {
	delete process.env[name];
}
process.env.TERM = "xterm-256color";

const upstreamRoot = process.cwd();
const outputRoot = path.resolve(
	upstreamRoot,
	process.argv[2] ?? "../conformance/fixtures",
);
const upstreamCommit = process.argv[3];
if (!upstreamCommit) {
	throw new Error("upstream commit argument is required");
}

await stubGrokMermaid(upstreamRoot);

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
	generateF6Release,
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
	generateF13DynamicWorkflows,
	generateWP440,
	generateWP440Read,
	generateWP370Runtime,
	generateWP450SessionSelector,
];
for (const generate of generators) {
	await generate(upstreamRoot, outputRoot, upstreamCommit);
}
