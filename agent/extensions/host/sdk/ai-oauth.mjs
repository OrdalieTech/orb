// orb-extension-sdk: stub-only surface of @earendil-works/pi-ai/oauth (upstream pi 1.4.0,
// commit 4a080252, MIT © Mario Zechner). orb implements nothing on this legacy
// subpath: every upstream export name links and throws OrbUnsupportedCapability
// on call, construction, or property access.
// Upstream publishes this entry as type-only (runtime `export {}`); the names
// are stubbed as values anyway so an untyped named import still links and
// fails on use rather than at link time.
import { unsupported } from "./internal/unsupported.mjs";

const stub = (name) => unsupported("ai/oauth", name, ["none"]);

