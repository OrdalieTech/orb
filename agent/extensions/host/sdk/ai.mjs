// orb-extension-sdk: @earendil-works/pi-ai surface as extensions see it (upstream
// packages/coding-agent/src/core/extensions/pi-ai-extension-view.ts, pi 1.5.2,
// commit b4afdddf): pi-ai's compat entry plus TypeBox's Type and a TypeBox
// StringEnum, served for both the root and "/compat" (ai-compat.mjs).
// modelsAreEqual is ported from pi-ai (packages/ai/src/models.ts, MIT (c) Mario
// Zechner); every other upstream export throws OrbUnsupportedCapability on use.
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
export const AI_TELEMETRY_SCHEMA = stub("AI_TELEMETRY_SCHEMA");
export const ANTHROPIC_API_KEY_ENV = stub("ANTHROPIC_API_KEY_ENV");
export const ANTHROPIC_AUTH_TOKEN_ENV = stub("ANTHROPIC_AUTH_TOKEN_ENV");
export const ANTHROPIC_FEDERATION_RULE_ID_ENV = stub("ANTHROPIC_FEDERATION_RULE_ID_ENV");
export const ANTHROPIC_IDENTITY_TOKEN_FILE_ENV = stub("ANTHROPIC_IDENTITY_TOKEN_FILE_ENV");
export const ANTHROPIC_OAUTH_TOKEN_ENV = stub("ANTHROPIC_OAUTH_TOKEN_ENV");
export const ANTHROPIC_ORGANIZATION_ID_ENV = stub("ANTHROPIC_ORGANIZATION_ID_ENV");
export const ANTHROPIC_SERVICE_ACCOUNT_ID_ENV = stub("ANTHROPIC_SERVICE_ACCOUNT_ID_ENV");
export const ANTHROPIC_WORKSPACE_ID_ENV = stub("ANTHROPIC_WORKSPACE_ID_ENV");
export const AssistantMessageEventStream = stub("AssistantMessageEventStream");
export const CACHE_WARMING_IDLE_CONTINUATION_PROBABILITY = stub("CACHE_WARMING_IDLE_CONTINUATION_PROBABILITY");
export const CACHE_WARMING_MAX_AGE_MS = stub("CACHE_WARMING_MAX_AGE_MS");
export const CACHE_WARMING_MAX_IDLE_AGE_MS = stub("CACHE_WARMING_MAX_IDLE_AGE_MS");
export const CACHE_WARMING_MINIMUM_EXPECTED_SAVINGS = stub("CACHE_WARMING_MINIMUM_EXPECTED_SAVINGS");
export const DEFAULT_MAX_AGENT_RETRY_DELAY_MS = stub("DEFAULT_MAX_AGENT_RETRY_DELAY_MS");
export const EventStream = stub("EventStream");
export const InMemoryCredentialStore = stub("InMemoryCredentialStore");
export const InMemoryModelsStore = stub("InMemoryModelsStore");
export const ModelsError = stub("ModelsError");
export const PI_AI_VERSION = stub("PI_AI_VERSION");
export const PI_ATTRIBUTION = stub("PI_ATTRIBUTION");
export const StreamingJsonParser = stub("StreamingJsonParser");
export const StringEnum = stub("StringEnum");
export const ToolCallArgumentsStream = stub("ToolCallArgumentsStream");
export const Type = stub("Type");
export const anthropicMessagesApi = stub("anthropicMessagesApi");
export const appendAssistantMessageDiagnostic = stub("appendAssistantMessageDiagnostic");
export const azureOpenAIResponsesApi = stub("azureOpenAIResponsesApi");
export const bedrockConverseStreamApi = stub("bedrockConverseStreamApi");
export const calculateCost = stub("calculateCost");
export const clampThinkingLevel = stub("clampThinkingLevel");
export const cleanupSessionResources = stub("cleanupSessionResources");
export const collapseSystemMessages = stub("collapseSystemMessages");
export const complete = stub("complete");
export const completeSimple = stub("completeSimple");
export const contentText = stub("contentText");
export const createAssistantMessageDiagnostic = stub("createAssistantMessageDiagnostic");
export const createAssistantMessageEventStream = stub("createAssistantMessageEventStream");
export const createFauxCore = stub("createFauxCore");
export const createInitialSystemMessage = stub("createInitialSystemMessage");
export const createModels = stub("createModels");
export const createProvider = stub("createProvider");
export const declarationsEqual = stub("declarationsEqual");
export const defaultProviderAuthContext = stub("defaultProviderAuthContext");
export const envApiKeyAuth = stub("envApiKeyAuth");
export const evaluateCacheWarming = stub("evaluateCacheWarming");
export const extractDiagnosticError = stub("extractDiagnosticError");
export const fauxAssistantMessage = stub("fauxAssistantMessage");
export const fauxProvider = stub("fauxProvider");
export const fauxText = stub("fauxText");
export const fauxThinking = stub("fauxThinking");
export const fauxToolCall = stub("fauxToolCall");
export const findEnvKeys = stub("findEnvKeys");
export const formatThrownValue = stub("formatThrownValue");
export const formatUserAgent = stub("formatUserAgent");
export const generateImages = stub("generateImages");
export const generateImagesOpenRouter = stub("generateImagesOpenRouter");
export const getApiProvider = stub("getApiProvider");
export const getApiProviders = stub("getApiProviders");
export const getAttributionHeaders = stub("getAttributionHeaders");
export const getCacheWarmingDelayMs = stub("getCacheWarmingDelayMs");
export const getCacheWarmingRequestOptions = stub("getCacheWarmingRequestOptions");
export const getClientVersion = stub("getClientVersion");
export const getCurrentSystemMessage = stub("getCurrentSystemMessage");
export const getCurrentSystemPrompt = stub("getCurrentSystemPrompt");
export const getCurrentTools = stub("getCurrentTools");
export const getDeclaredTools = stub("getDeclaredTools");
export const getEnvApiKey = stub("getEnvApiKey");
export const getIdentityHeaders = stub("getIdentityHeaders");
export const getImageModel = stub("getImageModel");
export const getImageModels = stub("getImageModels");
export const getImageProviders = stub("getImageProviders");
export const getImagesApiProvider = stub("getImagesApiProvider");
export const getInitialSystemMessage = stub("getInitialSystemMessage");
export const getModel = stub("getModel");
export const getModelType = stub("getModelType");
export const getModels = stub("getModels");
export const getOpenCodeSessionHeaders = stub("getOpenCodeSessionHeaders");
export const getOverflowPatterns = stub("getOverflowPatterns");
export const getPromptCacheTtlMs = stub("getPromptCacheTtlMs");
export const getProviders = stub("getProviders");
export const getSupportedThinkingLevels = stub("getSupportedThinkingLevels");
export const getSystemMessageText = stub("getSystemMessageText");
export const getToolStateChanges = stub("getToolStateChanges");
export const googleGenerativeAIApi = stub("googleGenerativeAIApi");
export const googleVertexApi = stub("googleVertexApi");
export const hasApi = stub("hasApi");
export const hasNonAdditiveToolChanges = stub("hasNonAdditiveToolChanges");
export const hasToolRedefinitions = stub("hasToolRedefinitions");
export const isCacheWarmingReplayable = stub("isCacheWarmingReplayable");
export const isContextOverflow = stub("isContextOverflow");
export const isModelType = stub("isModelType");
export const isRecoverableLength = stub("isRecoverableLength");
export const isRetryableAssistantError = stub("isRetryableAssistantError");
export const lazyApi = stub("lazyApi");
export const lazyOAuth = stub("lazyOAuth");
export const lazyStream = stub("lazyStream");
export const mergeClientIdentity = stub("mergeClientIdentity");
export const mistralConversationsApi = stub("mistralConversationsApi");
export const normalizeContext = stub("normalizeContext");
export const openAICodexResponsesApi = stub("openAICodexResponsesApi");
export const openAICompletionsApi = stub("openAICompletionsApi");
export const openAIResponsesApi = stub("openAIResponsesApi");
export const parseJsonWithRepair = stub("parseJsonWithRepair");
export const parseStreamingJson = stub("parseStreamingJson");
export const parseToolCallArguments = stub("parseToolCallArguments");
export const piMessagesApi = stub("piMessagesApi");
export const registerApiProvider = stub("registerApiProvider");
export const registerBuiltInApiProviders = stub("registerBuiltInApiProviders");
export const registerBuiltInImagesApiProviders = stub("registerBuiltInImagesApiProviders");
export const registerFauxProvider = stub("registerFauxProvider");
export const registerImagesApiProvider = stub("registerImagesApiProvider");
export const registerSessionResourceCleanup = stub("registerSessionResourceCleanup");
export const renderSystemMessageUpdate = stub("renderSystemMessageUpdate");
export const repairJson = stub("repairJson");
export const resetApiProviders = stub("resetApiProviders");
export const resolveTranscript = stub("resolveTranscript");
export const resolveTranscriptTools = stub("resolveTranscriptTools");
export const retryAssistantCall = stub("retryAssistantCall");
export const retryDelayMs = stub("retryDelayMs");
export const setBedrockProviderLoader = stub("setBedrockProviderLoader");
export const setBedrockProviderModule = stub("setBedrockProviderModule");
export const stream = stub("stream");
export const streamAnthropic = stub("streamAnthropic");
export const streamAzureOpenAIResponses = stub("streamAzureOpenAIResponses");
export const streamGoogle = stub("streamGoogle");
export const streamGoogleVertex = stub("streamGoogleVertex");
export const streamMistral = stub("streamMistral");
export const streamOpenAICodexResponses = stub("streamOpenAICodexResponses");
export const streamOpenAICompletions = stub("streamOpenAICompletions");
export const streamOpenAIResponses = stub("streamOpenAIResponses");
export const streamSimple = stub("streamSimple");
export const streamSimpleAnthropic = stub("streamSimpleAnthropic");
export const streamSimpleAzureOpenAIResponses = stub("streamSimpleAzureOpenAIResponses");
export const streamSimpleGoogle = stub("streamSimpleGoogle");
export const streamSimpleGoogleVertex = stub("streamSimpleGoogleVertex");
export const streamSimpleMistral = stub("streamSimpleMistral");
export const streamSimpleOpenAICodexResponses = stub("streamSimpleOpenAICodexResponses");
export const streamSimpleOpenAICompletions = stub("streamSimpleOpenAICompletions");
export const streamSimpleOpenAIResponses = stub("streamSimpleOpenAIResponses");
export const toToolDeclaration = stub("toToolDeclaration");
export const unregisterApiProviders = stub("unregisterApiProviders");
export const uuidv7 = stub("uuidv7");
export const validateToolArguments = stub("validateToolArguments");
export const validateToolCall = stub("validateToolCall");
export const withClientIdentity = stub("withClientIdentity");
export const withoutInitialSystemMessage = stub("withoutInitialSystemMessage");

