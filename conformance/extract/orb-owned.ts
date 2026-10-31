import { readFile } from "node:fs/promises";
import path from "node:path";

// Fixture values whose upstream source no longer exists are carried from the
// committed tree, never re-extracted: upstream v1.0.0 deleted its experimental
// harness (Orb's engine/harness is Orb-owned since; see the divergence ledger)
// and the models.json examples from its docs.
export async function committedFixture(family: string, file = "cases.json"): Promise<any> {
  return JSON.parse(await readFile(path.join(import.meta.dirname, "../fixtures", family, file), "utf8"));
}

// Cases where Orb deliberately differs from upstream are carried from the committed tree, never re-extracted
// (divergence ledger).
export async function orbOwned(family: string, file: string, generated: any[], names: string[], key?: string): Promise<void> {
  const committed = await committedFixture(family, file);
  const cases: any[] = key ? committed[key] : committed;
  for (const [index, item] of generated.entries()) {
    if (names.includes(item.name)) generated[index] = cases.find((entry) => entry.name === item.name);
  }
}

// Orb does not report bugs to pi's developers and shares sessions as GitHub gists only, without pi's Radius.
export function orbCommands<T extends { name: string; description: string }>(commands: T[]): T[] {
  return commands
    .filter((command) => command.name !== "bug")
    .map((command) => (command.name === "share" ? { ...command, description: "Share session as a secret GitHub gist" } : command));
}

// A stream's shared partial message gains durationMs when the stream ends, which a consumer lagging behind
// its producer sees on the events before the terminal one; the contract is the terminal message alone.
export function cloneStreamEvent<T extends { type: string }>(event: T): T {
  const clone = JSON.parse(JSON.stringify(event));
  if (clone.type !== "done" && clone.type !== "error") delete clone.partial?.durationMs;
  return clone;
}
