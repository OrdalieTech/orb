package agent

import (
	"context"
	"errors"

	"github.com/OrdalieTech/pi-go/ai"
)

// AgentMessage is an LLM message or an application-defined message. Standard
// messages are ai.Message values; any JSON-marshalable value may be carried so
// applications retain upstream's custom-message extension point.
type AgentMessage = any

type AgentMessages []AgentMessage

type StreamFn func(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error)

type ToolExecutionMode string

const (
	ToolExecutionSequential ToolExecutionMode = "sequential"
	ToolExecutionParallel   ToolExecutionMode = "parallel"
)

type QueueMode string

const (
	QueueAll        QueueMode = "all"
	QueueOneAtATime QueueMode = "one-at-a-time"
)

type ThinkingLevel = ai.ModelThinkingLevel

const (
	ThinkingOff     ThinkingLevel = ai.ModelThinkingOff
	ThinkingMinimal ThinkingLevel = ai.ModelThinkingMinimal
	ThinkingLow     ThinkingLevel = ai.ModelThinkingLow
	ThinkingMedium  ThinkingLevel = ai.ModelThinkingMedium
	ThinkingHigh    ThinkingLevel = ai.ModelThinkingHigh
	ThinkingXHigh   ThinkingLevel = ai.ModelThinkingXHigh
	ThinkingMax     ThinkingLevel = ai.ModelThinkingMax
)

type AgentToolResult struct {
	Content        ai.ToolResultContent
	Details        any
	AddedToolNames *[]string
	Terminate      *bool
}

// AgentToolUpdateCallback snapshots accepted updates and returns before event
// listeners settle. It is scoped to Execute; calls after Execute returns, or
// racing with its return, may be ignored.
type AgentToolUpdateCallback func(AgentToolResult)

type PrepareArgumentsFunc func(args any) (any, error)

type ToolExecuteFunc func(
	ctx context.Context,
	toolCallID string,
	params any,
	onUpdate AgentToolUpdateCallback,
) (AgentToolResult, error)

type AgentToolSpec struct {
	Name             string
	Label            string
	Description      string
	Parameters       ai.JSONSchema
	PrepareArguments PrepareArgumentsFunc
	ExecutionMode    ToolExecutionMode
}

// AgentTool is the execution seam shared by built-in, extension, and MCP
// tools. Spec returns immutable call metadata for a single loop decision.
type AgentTool interface {
	Spec() AgentToolSpec
	Execute(context.Context, string, any, AgentToolUpdateCallback) (AgentToolResult, error)
}

// ParallelExecutionPreparer reserves invocation-ordered resources before a
// parallel batch is launched. The loop calls it in tool-call source order.
type ParallelExecutionPreparer interface {
	PrepareParallelExecution(context.Context, any) (context.Context, func(), error)
}

// AgentToolFunc adapts a function to AgentTool without introducing a second
// implementation-specific interface.
type AgentToolFunc struct {
	AgentToolSpec
	Run ToolExecuteFunc
}

func (tool AgentToolFunc) Spec() AgentToolSpec {
	return tool.AgentToolSpec
}

func (tool AgentToolFunc) Execute(
	ctx context.Context,
	toolCallID string,
	params any,
	onUpdate AgentToolUpdateCallback,
) (AgentToolResult, error) {
	if tool.Run == nil {
		return AgentToolResult{}, errors.New("agent: tool has no execute function")
	}
	return tool.Run(ctx, toolCallID, params, onUpdate)
}

type AgentContext struct {
	SystemPrompt string
	Messages     AgentMessages
	Tools        []AgentTool
}

type AgentState struct {
	SystemPrompt     string
	Model            *ai.Model
	ThinkingLevel    ThinkingLevel
	Tools            []AgentTool
	Messages         AgentMessages
	IsStreaming      bool
	StreamingMessage AgentMessage
	PendingToolCalls map[string]struct{}
	ErrorMessage     *string
}

type BeforeToolCallResult struct {
	Block  bool
	Reason string
}

type AfterToolCallResult struct {
	Content    ai.ToolResultContent
	Details    any
	DetailsSet bool
	IsError    *bool
	Terminate  *bool
}

type BeforeToolCallContext struct {
	AssistantMessage *ai.AssistantMessage
	ToolCall         *ai.ToolCall
	Args             any
	Context          *AgentContext
}

type AfterToolCallContext struct {
	AssistantMessage *ai.AssistantMessage
	ToolCall         *ai.ToolCall
	Args             any
	Result           AgentToolResult
	IsError          bool
	Context          *AgentContext
}

type ShouldStopAfterTurnContext struct {
	Message     *ai.AssistantMessage
	ToolResults []*ai.ToolResultMessage
	Context     *AgentContext
	NewMessages AgentMessages
}

type PrepareNextTurnContext = ShouldStopAfterTurnContext

type AgentLoopTurnUpdate struct {
	Context       *AgentContext
	Model         *ai.Model
	ThinkingLevel *ThinkingLevel
}

type ConvertToLLMFunc func(context.Context, AgentMessages) (ai.MessageList, error)
type TransformContextFunc func(context.Context, AgentMessages) (AgentMessages, error)
type GetAPIKeyFunc func(context.Context, ai.ProviderID) (*string, error)
type RequestAuth struct {
	APIKey  *string
	Headers map[string]string
	Env     ai.ProviderEnv
	BaseURL *string
}
type GetRequestAuthFunc func(context.Context, ai.ProviderID) (*RequestAuth, error)
type GetModelHeadersFunc func(context.Context, *ai.Model, *string, ai.ProviderEnv) (*map[string]string, error)
type ShouldStopAfterTurnFunc func(context.Context, ShouldStopAfterTurnContext) (bool, error)
type PrepareNextTurnFunc func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error)
type GetQueuedMessagesFunc func(context.Context) (AgentMessages, error)
type BeforeToolCallFunc func(context.Context, BeforeToolCallContext) (*BeforeToolCallResult, error)

// AfterToolCallFunc may be invoked concurrently for parallel tool calls.
type AfterToolCallFunc func(context.Context, AfterToolCallContext) (*AfterToolCallResult, error)

type AgentLoopConfig struct {
	ai.SimpleStreamOptions
	Model               *ai.Model
	StreamFn            StreamFn
	ConvertToLLM        ConvertToLLMFunc
	TransformContext    TransformContextFunc
	GetAPIKey           GetAPIKeyFunc
	GetRequestAuth      GetRequestAuthFunc
	GetModelHeaders     GetModelHeadersFunc
	ShouldStopAfterTurn ShouldStopAfterTurnFunc
	PrepareNextTurn     PrepareNextTurnFunc
	GetSteeringMessages GetQueuedMessagesFunc
	GetFollowUpMessages GetQueuedMessagesFunc
	ToolExecution       ToolExecutionMode
	BeforeToolCall      BeforeToolCallFunc
	AfterToolCall       AfterToolCallFunc
	Now                 func() int64
}
