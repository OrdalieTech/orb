// orb-extension-sdk: stub-only surface of @earendil-works/pi-agent-core (upstream
// pi 1.0.0, commit a13d35a7, MIT © Mario Zechner). orb implements nothing on this
// legacy subpath: every upstream export name links and throws
// OrbUnsupportedCapability on call, construction, or property access.
import { unsupported } from "./internal/unsupported.mjs";

const stub = (name) => unsupported("agent-core", name, ["none"]);

export const Agent = stub("Agent");
export const agentLoop = stub("agentLoop");
export const agentLoopContinue = stub("agentLoopContinue");
export const runAgentLoop = stub("runAgentLoop");
export const runAgentLoopContinue = stub("runAgentLoopContinue");
export const runToolCall = stub("runToolCall");
export const setDefaultStreamFn = stub("setDefaultStreamFn");
export const streamProxy = stub("streamProxy");
