// orb-extension-sdk: @earendil-works/pi-ai ROOT surface (upstream
// packages/ai/src/index.ts, pi 0.84.1, commit 53fa77cc). The "/compat"
// subpath serves its superset from ai-compat.mjs, matching upstream's
// exports map. modelsAreEqual is ported from pi-ai (packages/ai/src/models.ts,
// MIT (c) Mario Zechner); every other upstream export throws
// OrbUnsupportedCapability on use.
import manifest from "./sdk.json" with { type: "json" };
import { unsupported } from "./internal/unsupported.mjs";

const SUPPORTED = manifest.modules.ai.implemented;
const stub = (name) => unsupported("ai", name, SUPPORTED);

/**
 * Check if two models are equal by comparing both their id and provider.
 * Returns false if either model is null or undefined.
 */
export function modelsAreEqual(a, b) {
	if (!a || !b) return false;
	return a.id === b.id && a.provider === b.provider;
}

// -- Unsupported upstream exports (generated from pinned pi-ai 0.84.1 root) --
export const AssistantMessageEventStream = stub("AssistantMessageEventStream");
export const EventStream = stub("EventStream");
export const InMemoryCredentialStore = stub("InMemoryCredentialStore");
export const InMemoryModelsStore = stub("InMemoryModelsStore");
export const ModelsError = stub("ModelsError");
export const StringEnum = stub("StringEnum");
export const Type = stub("Type");
export const appendAssistantMessageDiagnostic = stub("appendAssistantMessageDiagnostic");
export const calculateCost = stub("calculateCost");
export const clampThinkingLevel = stub("clampThinkingLevel");
export const cleanupSessionResources = stub("cleanupSessionResources");
export const contentText = stub("contentText");
export const createAssistantMessageDiagnostic = stub("createAssistantMessageDiagnostic");
export const createAssistantMessageEventStream = stub("createAssistantMessageEventStream");
export const createFauxCore = stub("createFauxCore");
export const createImagesModels = stub("createImagesModels");
export const createImagesProvider = stub("createImagesProvider");
export const createModels = stub("createModels");
export const createProvider = stub("createProvider");
export const defaultProviderAuthContext = stub("defaultProviderAuthContext");
export const envApiKeyAuth = stub("envApiKeyAuth");
export const extractDiagnosticError = stub("extractDiagnosticError");
export const fauxAssistantMessage = stub("fauxAssistantMessage");
export const fauxProvider = stub("fauxProvider");
export const fauxText = stub("fauxText");
export const fauxThinking = stub("fauxThinking");
export const fauxToolCall = stub("fauxToolCall");
export const formatThrownValue = stub("formatThrownValue");
export const getOverflowPatterns = stub("getOverflowPatterns");
export const getSupportedThinkingLevels = stub("getSupportedThinkingLevels");
export const hasApi = stub("hasApi");
export const isContextOverflow = stub("isContextOverflow");
export const isRecoverableLength = stub("isRecoverableLength");
export const isRetryableAssistantError = stub("isRetryableAssistantError");
export const lazyApi = stub("lazyApi");
export const lazyOAuth = stub("lazyOAuth");
export const lazyStream = stub("lazyStream");
export const parseJsonWithRepair = stub("parseJsonWithRepair");
export const parseStreamingJson = stub("parseStreamingJson");
export const registerSessionResourceCleanup = stub("registerSessionResourceCleanup");
export const repairJson = stub("repairJson");
export const retryAssistantCall = stub("retryAssistantCall");
export const uuidv7 = stub("uuidv7");
export const validateToolArguments = stub("validateToolArguments");
export const validateToolCall = stub("validateToolCall");
