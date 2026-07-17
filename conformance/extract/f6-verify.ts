import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

const sessionPath = process.argv[2];
if (!sessionPath) throw new Error("session path argument is required");

const upstreamRoot = process.cwd();
const moduleURL = pathToFileURL(
  path.join(upstreamRoot, "packages/coding-agent/src/core/session-manager.ts"),
).href;
const { SessionManager } = await withUpstreamModelData(
  upstreamRoot,
  async () => await import(moduleURL) as typeof import(
    "../../.upstream/packages/coding-agent/src/core/session-manager.ts"
  ),
);
const manager = SessionManager.open(path.resolve(sessionPath));
const projection = {
  header: manager.getHeader(),
  leafId: manager.getLeafId(),
  entryTypes: manager.getEntries().map((entry) => entry.type),
  branchIds: manager.getBranch().map((entry) => entry.id),
  sessionName: manager.getSessionName() ?? null,
  entries: manager.getEntries(),
};
process.stdout.write(`${JSON.stringify(projection)}\n`);
