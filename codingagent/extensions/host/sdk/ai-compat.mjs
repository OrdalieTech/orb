// orb-extension-sdk: @earendil-works/pi-ai "/compat" surface (upstream
// packages/ai/src/compat.ts, pi 0.84.1, commit 53fa77cc): the root surface
// plus the legacy global API (api-dispatch stream()/complete(), api registry,
// generated catalog reads, per-API lazy stream wrappers, image generation).
// modelsAreEqual stays real via the root re-export; everything else throws
// OrbUnsupportedCapability on use.
import manifest from "./sdk.json" with { type: "json" };
import { unsupported } from "./internal/unsupported.mjs";

export * from "./ai.mjs";

const SUPPORTED = manifest.modules.ai.implemented;
const stub = (name) => unsupported("ai", name, SUPPORTED);

// -- Compat-only upstream exports (generated from pinned pi-ai 0.84.1) -------
export const ANTHROPIC_API_KEY_ENV = stub("ANTHROPIC_API_KEY_ENV");
export const ANTHROPIC_AUTH_TOKEN_ENV = stub("ANTHROPIC_AUTH_TOKEN_ENV");
export const ANTHROPIC_OAUTH_TOKEN_ENV = stub("ANTHROPIC_OAUTH_TOKEN_ENV");
export const anthropicMessagesApi = stub("anthropicMessagesApi");
export const azureOpenAIResponsesApi = stub("azureOpenAIResponsesApi");
export const bedrockConverseStreamApi = stub("bedrockConverseStreamApi");
export const complete = stub("complete");
export const completeSimple = stub("completeSimple");
export const findEnvKeys = stub("findEnvKeys");
export const generateImages = stub("generateImages");
export const generateImagesOpenRouter = stub("generateImagesOpenRouter");
export const getApiProvider = stub("getApiProvider");
export const getApiProviders = stub("getApiProviders");
export const getEnvApiKey = stub("getEnvApiKey");
export const getImageModel = stub("getImageModel");
export const getImageModels = stub("getImageModels");
export const getImageProviders = stub("getImageProviders");
export const getImagesApiProvider = stub("getImagesApiProvider");
export const getModel = stub("getModel");
export const getModels = stub("getModels");
export const getProviders = stub("getProviders");
export const googleGenerativeAIApi = stub("googleGenerativeAIApi");
export const googleVertexApi = stub("googleVertexApi");
export const mistralConversationsApi = stub("mistralConversationsApi");
export const openAICodexResponsesApi = stub("openAICodexResponsesApi");
export const openAICompletionsApi = stub("openAICompletionsApi");
export const openAIResponsesApi = stub("openAIResponsesApi");
export const piMessagesApi = stub("piMessagesApi");
export const registerApiProvider = stub("registerApiProvider");
export const registerBuiltInApiProviders = stub("registerBuiltInApiProviders");
export const registerBuiltInImagesApiProviders = stub("registerBuiltInImagesApiProviders");
export const registerFauxProvider = stub("registerFauxProvider");
export const registerImagesApiProvider = stub("registerImagesApiProvider");
export const resetApiProviders = stub("resetApiProviders");
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
export const unregisterApiProviders = stub("unregisterApiProviders");
