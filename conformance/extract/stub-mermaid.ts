import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

/**
 * Upstream interactive-mode.ts imports lovely-mermaid unconditionally,
 * but no extracted scenario renders a Mermaid diagram (Orb's own Mermaid
 * conformance is a frozen D35 snapshot). Installing a render-nothing
 * stub keeps the F12 extractors loadable without carrying the dependency.
 * Idempotent; overwrites a real install so extraction output never depends
 * on which one is present.
 */
export async function stubMermaid(upstreamDir: string): Promise<void> {
	const dir = path.join(upstreamDir, "node_modules", "lovely-mermaid");
	await mkdir(dir, { recursive: true });
	await writeFile(
		path.join(dir, "package.json"),
		JSON.stringify({ name: "lovely-mermaid", version: "0.0.0-orb-stub", type: "module", main: "index.js" }),
	);
	await writeFile(path.join(dir, "index.js"), "export function render() {\n\treturn null;\n}\nexport function toAnsi() {\n\treturn \"\";\n}\n");
}
