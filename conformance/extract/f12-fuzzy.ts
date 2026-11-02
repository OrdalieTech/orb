import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

// fuzzyMatch scores and fuzzyFilter orders from the pinned packages/tui/src/fuzzy.ts.
export async function generateF12Fuzzy(upstreamRoot: string, outputRoot: string): Promise<void> {
	const { fuzzyFilter, fuzzyMatch } = await import(
		pathToFileURL(path.join(upstreamRoot, "packages/tui/src/fuzzy.ts")).href
	);
	const matches: [string, string][] = [
		["", "anything"],
		["toolong", "abc"],
		["abc", "abc"],
		["cba", "abc"],
		["ABC", "abc"],
		["abc", "ABC"],
		["abc", "abcdef"],
		["abc", "axbxcx"],
		["fb", "foo-bar"],
		["fb", "xxfxxbxx"],
		["o1", "1o"],
		["4o", "o4-mini"],
		["gpt4", "gpt-4o-mini"],
		["cld", "claude-sonnet"],
		["a", "😀a"],
		["\ud83d\ude00", "x😀"],
		["i\u0307", "İ"],
	];
	const filters: [string[], string][] = [
		[["apple", "banana", "cherry"], "an"],
		[["xtestx", "test", "tempest"], "test"],
		[["openai/gpt-4", "anthropic/claude"], "open/gpt"],
		[["alpha", "beta", "gamma"], ""],
		[["settings-list", "select-list", "editor"], "list"],
	];
	const fixture = {
		schemaVersion: 1,
		matches: matches.map(([query, text]) => ({ query, text, ...fuzzyMatch(query, text) })),
		filters: filters.map(([items, query]) => ({ items, query, result: fuzzyFilter(items, query, (item: string) => item) })),
	};
	await mkdir(path.join(outputRoot, "F12"), { recursive: true });
	await writeFile(path.join(outputRoot, "F12", "fuzzy.json"), `${JSON.stringify(fixture, null, 2)}\n`);
}
