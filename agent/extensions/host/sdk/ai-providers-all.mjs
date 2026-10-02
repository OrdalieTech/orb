// orb-extension-sdk: stub-only surface of @earendil-works/pi-ai/providers/all (upstream pi 1.0.0,
// commit a13d35a7, MIT © Mario Zechner). orb implements nothing on this legacy
// subpath: every upstream export name links and throws OrbUnsupportedCapability
// on call, construction, or property access.
import { unsupported } from "./internal/unsupported.mjs";

const stub = (name) => unsupported("ai/providers/all", name, ["none"]);

export const builtinModels = stub("builtinModels");
export const builtinProviders = stub("builtinProviders");
export const getAllBuiltinModels = stub("getAllBuiltinModels");
export const getBuiltinClassifierModel = stub("getBuiltinClassifierModel");
export const getBuiltinClassifierModels = stub("getBuiltinClassifierModels");
export const getBuiltinImageModel = stub("getBuiltinImageModel");
export const getBuiltinImageModels = stub("getBuiltinImageModels");
export const getBuiltinModel = stub("getBuiltinModel");
export const getBuiltinModelDataGeneratedAt = stub("getBuiltinModelDataGeneratedAt");
export const getBuiltinModels = stub("getBuiltinModels");
export const getBuiltinProviders = stub("getBuiltinProviders");
export const radiusProvider = stub("radiusProvider");
