import { readFile } from "node:fs/promises";
import path from "node:path";

// Fixture values whose upstream source no longer exists are carried from the
// committed tree, never re-extracted: upstream v1.0.0 deleted its experimental
// harness (Orb's engine/harness is Orb-owned since; see the divergence ledger)
// and the models.json examples from its docs.
export async function committedFixture(family: string, file = "cases.json"): Promise<any> {
  return JSON.parse(await readFile(path.join(import.meta.dirname, "../fixtures", family, file), "utf8"));
}
