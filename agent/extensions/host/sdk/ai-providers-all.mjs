// orb-extension-sdk: stub-only surface of @earendil-works/pi-ai/providers/all (upstream pi 0.84.1,
// commit 53fa77cc, MIT © Mario Zechner). orb implements nothing on this legacy
// subpath: every upstream export name links and throws OrbUnsupportedCapability
// on call, construction, or property access.
import { unsupported } from "./internal/unsupported.mjs";

const stub = (name) => unsupported("ai/providers/all", name, ["none"]);

export const builtinImagesModels = stub("builtinImagesModels");
export const builtinImagesProviders = stub("builtinImagesProviders");
export const builtinModels = stub("builtinModels");
export const builtinProviders = stub("builtinProviders");
export const getBuiltinModel = stub("getBuiltinModel");
export const getBuiltinModelDataGeneratedAt = stub("getBuiltinModelDataGeneratedAt");
export const getBuiltinModels = stub("getBuiltinModels");
export const getBuiltinProviders = stub("getBuiltinProviders");
export const radiusProvider = stub("radiusProvider");
