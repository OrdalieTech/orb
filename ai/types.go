package ai

import "encoding/json"

type API string

const (
	APIUnknown              API = ""
	APIOpenAICompletions    API = "openai-completions"
	APIMistralConversations API = "mistral-conversations"
	APIOpenAIResponses      API = "openai-responses"
	APIAzureOpenAIResponses API = "azure-openai-responses"
	APIOpenAICodexResponses API = "openai-codex-responses"
	APIAnthropicMessages    API = "anthropic-messages"
	APIBedrockConverse      API = "bedrock-converse-stream"
	APIGoogleGenerativeAI   API = "google-generative-ai"
	APIGoogleVertex         API = "google-vertex"
	APIPiMessages           API = "pi-messages"
)

type ProviderID string

type ThinkingLevel string

const (
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

type ModelThinkingLevel string

const (
	ModelThinkingOff     ModelThinkingLevel = "off"
	ModelThinkingMinimal ModelThinkingLevel = "minimal"
	ModelThinkingLow     ModelThinkingLevel = "low"
	ModelThinkingMedium  ModelThinkingLevel = "medium"
	ModelThinkingHigh    ModelThinkingLevel = "high"
	ModelThinkingXHigh   ModelThinkingLevel = "xhigh"
	ModelThinkingMax     ModelThinkingLevel = "max"
)

type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

type TextSignatureV1 struct {
	Version int    `json:"v"`
	ID      string `json:"id"`
	Phase   string `json:"phase,omitempty"`
}

type TextContent struct {
	Text          string  `json:"text"`
	TextSignature *string `json:"textSignature,omitempty"`
}

type ThinkingContent struct {
	Thinking          string  `json:"thinking"`
	ThinkingSignature *string `json:"thinkingSignature,omitempty"`
	Redacted          *bool   `json:"redacted,omitempty"`
}

type ImageContent struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type ToolCall struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature *string        `json:"thoughtSignature,omitempty"`
}

type UserContentBlock interface {
	isUserContentBlock()
}

type AssistantContentBlock interface {
	isAssistantContentBlock()
}

type ToolResultContentBlock interface {
	isToolResultContentBlock()
}

func (*TextContent) isUserContentBlock()          {}
func (*TextContent) isAssistantContentBlock()     {}
func (*TextContent) isToolResultContentBlock()    {}
func (*ThinkingContent) isAssistantContentBlock() {}
func (*ImageContent) isUserContentBlock()         {}
func (*ImageContent) isToolResultContentBlock()   {}
func (*ToolCall) isAssistantContentBlock()        {}

type UserContentBlocks []UserContentBlock
type AssistantContent []AssistantContentBlock
type ToolResultContent []ToolResultContentBlock

type UserContent struct {
	Text   *string
	Blocks UserContentBlocks
}

func NewUserText(text string) UserContent {
	return UserContent{Text: &text}
}

func NewUserContent(blocks ...UserContentBlock) UserContent {
	if blocks == nil {
		blocks = UserContentBlocks{}
	}
	return UserContent{Blocks: blocks}
}

type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type Usage struct {
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	CacheRead    int64  `json:"cacheRead"`
	CacheWrite   int64  `json:"cacheWrite"`
	CacheWrite1h *int64 `json:"cacheWrite1h,omitempty"`
	Reasoning    *int64 `json:"reasoning,omitempty"`
	TotalTokens  int64  `json:"totalTokens"`
	Cost         Cost   `json:"cost"`
}

type DiagnosticErrorInfo struct {
	Name    *string         `json:"name,omitempty"`
	Message string          `json:"message"`
	Stack   *string         `json:"stack,omitempty"`
	Code    json.RawMessage `json:"code,omitempty"`
}

type AssistantMessageDiagnostic struct {
	Type      string               `json:"type"`
	Timestamp int64                `json:"timestamp"`
	Error     *DiagnosticErrorInfo `json:"error,omitempty"`
	Details   json.RawMessage      `json:"details,omitempty"`
}

type Message interface {
	isMessage()
}

type UserMessage struct {
	Content   UserContent `json:"content"`
	Timestamp int64       `json:"timestamp"`
}

type AssistantMessage struct {
	Content       AssistantContent              `json:"content"`
	API           API                           `json:"api"`
	Provider      ProviderID                    `json:"provider"`
	Model         string                        `json:"model"`
	ResponseModel *string                       `json:"responseModel,omitempty"`
	ResponseID    *string                       `json:"responseId,omitempty"`
	Diagnostics   *[]AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
	Usage         Usage                         `json:"usage"`
	StopReason    StopReason                    `json:"stopReason"`
	ErrorMessage  *string                       `json:"errorMessage,omitempty"`
	Timestamp     int64                         `json:"timestamp"`
}

type ToolResultMessage struct {
	ToolCallID     string            `json:"toolCallId"`
	ToolName       string            `json:"toolName"`
	Content        ToolResultContent `json:"content"`
	Details        json.RawMessage   `json:"details,omitempty"`
	AddedToolNames *[]string         `json:"addedToolNames,omitempty"`
	IsError        bool              `json:"isError"`
	Timestamp      int64             `json:"timestamp"`
}

func (*UserMessage) isMessage()       {}
func (*AssistantMessage) isMessage()  {}
func (*ToolResultMessage) isMessage() {}

type MessageList []Message

type ImagesAPI string
type ImagesProviderID string
type ImagesStopReason string

const (
	ImagesAPIOpenRouter      ImagesAPI        = "openrouter-images"
	ImagesProviderOpenRouter ImagesProviderID = "openrouter"

	ImagesStopReasonStop    ImagesStopReason = "stop"
	ImagesStopReasonError   ImagesStopReason = "error"
	ImagesStopReasonAborted ImagesStopReason = "aborted"
)

type ImagesContentBlock interface {
	isImagesContentBlock()
}

func (*TextContent) isImagesContentBlock()  {}
func (*ImageContent) isImagesContentBlock() {}

type ImagesContent []ImagesContentBlock

type ImagesContext struct {
	Input ImagesContent `json:"input"`
}

type AssistantImages struct {
	API          ImagesAPI        `json:"api"`
	Provider     ImagesProviderID `json:"provider"`
	Model        string           `json:"model"`
	Output       ImagesContent    `json:"output"`
	ResponseID   *string          `json:"responseId,omitempty"`
	Usage        *Usage           `json:"usage,omitempty"`
	StopReason   ImagesStopReason `json:"stopReason"`
	ErrorMessage *string          `json:"errorMessage,omitempty"`
	Timestamp    int64            `json:"timestamp"`
}
