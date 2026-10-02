// orb-extension-sdk: @earendil-works/pi-ai/models surface (upstream pi 1.0.0,
// commit a13d35a7, MIT © Mario Zechner). modelsAreEqual is the root's
// implementation; every other upstream export links and throws
// OrbUnsupportedCapability on call, construction, or property access.
import { unsupported } from "./internal/unsupported.mjs";

const stub = (name) => unsupported("ai/models", name, ["none"]);

export const ModelsError = stub("ModelsError");
export const calculateCost = stub("calculateCost");
export const clampThinkingLevel = stub("clampThinkingLevel");
export const createModels = stub("createModels");
export const createProvider = stub("createProvider");
export const getModelType = stub("getModelType");
export const getSupportedThinkingLevels = stub("getSupportedThinkingLevels");
export const hasApi = stub("hasApi");
export const isModelType = stub("isModelType");
export { modelsAreEqual } from "./ai.mjs";
