// orb-extension-sdk: @earendil-works/pi-ai/models surface (upstream pi 1.5.2,
// commit b4afdddf, MIT © Mario Zechner). modelsAreEqual is the root's
// implementation; every other upstream export links and throws
// OrbUnsupportedCapability on call, construction, or property access.
import { unsupported } from "./internal/unsupported.mjs";

const stub = (name) => unsupported("ai/models", name, ["none"]);

export const AI_TELEMETRY_SCHEMA = stub("AI_TELEMETRY_SCHEMA");
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
