// orb-extension-sdk: @earendil-works/pi-ai "/compat" surface, the same
// extension view as the root (ai.mjs, pi 1.5.0, commit f9a26365).
import manifest from "./sdk.json" with { type: "json" };
import { unsupported } from "./internal/unsupported.mjs";

export * from "./ai.mjs";

const SUPPORTED = manifest.modules.ai.implemented;
const stub = (name) => unsupported("ai", name, SUPPORTED);

// -- Compat-only upstream exports (generated from pinned pi-ai 0.84.1) -------
