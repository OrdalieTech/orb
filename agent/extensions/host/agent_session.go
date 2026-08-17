package host

import (
	"context"
	"encoding/json"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

// ServiceError is a structured failure from a protocol service (sdk_v1,
// agent_session_v1, model_runtime_v1). Code and Message cross the wire
// verbatim as the {code,message} error envelope of the request's response;
// service errors never tear the channel.
type ServiceError struct {
	Code    string
	Message string
}

func (err *ServiceError) Error() string { return err.Message }

// Wire kinds of agent_session_event frames, one per AgentSessionCallbacks
// sink. They mirror the SDK transport's event contract
// (sdk/internal/services.mjs): onEvent / onMessagesSnapshot /
// onMessageAppended / onMessageUpdated / onStats.
const (
	SessionEventGeneric          = "event"
	SessionEventMessagesSnapshot = "messages_snapshot"
	SessionEventMessageAppended  = "message_appended"
	SessionEventMessageUpdated   = "message_updated"
	SessionEventStats            = "stats"
)

// AgentSessionService is the runtime seam behind the agent_session_v1
// capability (and sdk_v1's resource reload). The protocol layer owns handle
// minting, per-handle monotonic event seq, frame-cap chunking, request
// cancellation, and error envelopes; the implementation owns the child-session
// runtime — backed by agent.NewAgentSession with extension loading
// disabled (runner lane). Install it with Manager.SetAgentSessionService; the
// default is a stub whose CreateSession returns a precise not-yet-wired
// ServiceError so the protocol layer stays testable before the runtime lands.
type AgentSessionService interface {
	// CreateSession starts a child agent session. Callbacks the implementation
	// invokes before one of the handle's methods returns are delivered to the
	// SDK in seq order before that operation's terminal result; callbacks
	// invoked after the method returns race the result and must not carry
	// turn-scoped events.
	CreateSession(ctx context.Context, request AgentSessionCreateRequest, callbacks AgentSessionCallbacks) (AgentSessionHandle, AgentSessionCreateResult, error)
	// ReloadResources backs DefaultResourceLoader.reload() (sdk_v1
	// sdk_resource_reload): reload or prewarm the resource set (skills,
	// prompts, AGENTS.md context — never extensions when NoExtensions) that
	// the next CreateSession for the same cwd/agentDir observes.
	ReloadResources(ctx context.Context, request AgentSessionResourceLoader) error
}

// AgentSessionHandle is one live child session behind a Go-minted string
// handle. Every method maps 1:1 to an agent_session_v1 request — except
// Messages and SessionStats, which have no wire dispatch: the SDK reads both
// from the event mirrors (messages_snapshot/appended/updated, stats). ctx is
// cancelled when the SDK aborts that request (service_cancel).
type AgentSessionHandle interface {
	// Prompt resolves when the turn settles. Provider quota/limit failures
	// must NOT surface as an error: they land in the message mirror as a final
	// assistant message with stopReason "error" and the provider's verbatim
	// errorMessage (the plugin classifies limits by that wording).
	Prompt(ctx context.Context, text string, options json.RawMessage) error
	// Messages returns the full message mirror as a JSON array of
	// upstream-shaped AgentMessage values.
	Messages(ctx context.Context) (json.RawMessage, error)
	Abort(ctx context.Context) error
	SessionStats(ctx context.Context) (AgentSessionStats, error)
	SetActiveToolsByName(ctx context.Context, names []string) error
	// AppendSessionInfo is best-effort session naming
	// (SessionManager.appendSessionInfo forwarded post-create).
	AppendSessionInfo(ctx context.Context, name string) error
	// Dispose releases the session. The dispatch layer calls it at most once
	// per handle and drops callback events emitted afterwards.
	Dispose(ctx context.Context) error
}

// AgentSessionCreateRequest carries one agent_session_create invocation.
type AgentSessionCreateRequest struct {
	ExtensionID string
	Options     AgentSessionOptions
	// ResolveModelRuntime resolves Options.ModelRuntime to the live registry
	// view it references ("context" = the requesting extension's
	// ctx.modelRegistry; otherwise a model_runtime_v1 handle minted by this
	// host). nil when the SDK passed no modelRuntime.
	ResolveModelRuntime func() (extensions.ModelRegistry, error)
	// ExecuteTool round-trips a customTools invocation into the host JS
	// process (execute_session_tool request): the transport retained the live
	// tool closures under this session's handle and runs prepareArguments
	// before execute. onUpdate, if non-nil, receives streamed tool_update
	// partials; the result is the raw upstream AgentToolResult JSON. The
	// implementation must schema-validate params against the tool's JSON
	// Schema (D14) before calling — passing either the validated value or its
	// order-preserving raw JSON encoding (upstream hands the parsed JS object
	// through with member order intact) — and honor `terminate: true` in the
	// result by ending the turn.
	ExecuteTool func(ctx context.Context, toolName, toolCallID string, params any, onUpdate func(json.RawMessage)) (json.RawMessage, error)
}

// AgentSessionCreateResult mirrors the non-session fields of the SDK's
// CreateAgentSessionResult.
type AgentSessionCreateResult struct {
	ModelFallbackMessage string    `json:"modelFallbackMessage,omitempty"`
	Model                *ai.Model `json:"model,omitempty"`
}

// AgentSessionCallbacks stream a session's activity back to the SDK's local
// mirrors. The dispatch layer constructs them (all funcs non-nil); every
// invocation becomes an agent_session_event frame carrying the handle, a
// per-handle monotonic seq, the kind, and the payload — chunked when it would
// exceed the 4 MiB frame cap. They map 1:1 onto the SDK transport's event
// sinks: the messages mirror is how the plugin observes history (it derives
// usage from the stats mirror, and model fallback/resolution from the create
// result), so implementations must keep these mirrors live DURING Prompt, not
// only at settle — the plugin throttles history off subscribe ticks.
type AgentSessionCallbacks struct {
	// OnEvent mirrors a raw upstream-shaped AgentSessionEvent; it drives
	// session.subscribe listeners.
	OnEvent func(payload any)
	// OnMessagesSnapshot replaces the session.messages mirror wholesale. Use
	// the appended/updated deltas below when volume allows: 16-way fan-outs
	// stream against the 4 MiB frame cap.
	OnMessagesSnapshot func(messages any)
	// OnMessageAppended appends one message to the mirror.
	OnMessageAppended func(message any)
	// OnMessageUpdated replaces the mirror entry at index (delta updates).
	OnMessageUpdated func(index int, message any)
	// OnStats updates the getSessionStats() mirror.
	OnStats func(stats AgentSessionStats)
}

// AgentSessionOptions mirrors the SDK's CreateAgentSessionOptions across the
// wire, field for field.
type AgentSessionOptions struct {
	// CWD is the per-session working directory (fan-out runs pass git
	// worktrees here).
	CWD      string `json:"cwd,omitempty"`
	AgentDir string `json:"agentDir,omitempty"`
	// Model may be an off-catalog synthesized entry (the plugin's model-spec
	// fallback spreads a sibling model with a new id); implementations must
	// route by its fields and not require catalog membership. Model "tiers"
	// never cross this wire: upstream CreateAgentSessionOptions has no tier
	// field — extensions resolve tiers client-side (model-tiers.json) into a
	// concrete Model before calling createAgentSession, which the F13
	// model-routing scenario gates end to end.
	Model         *ai.Model             `json:"model,omitempty"`
	ThinkingLevel ai.ModelThinkingLevel `json:"thinkingLevel,omitempty"`
	ScopedModels  json.RawMessage       `json:"scopedModels,omitempty"`
	// ModelRuntime references the model_runtime_v1 handle whose catalog/auth
	// view the session must resolve models against.
	ModelRuntime *ModelRuntimeRef `json:"modelRuntime,omitempty"`
	// NoTools disables tools wholesale: "all" or "builtin".
	NoTools string `json:"noTools,omitempty"`
	// Tools is a name allowlist over the session's tool set; ExcludeTools is a
	// name denylist applied after it and always wins.
	Tools        []string `json:"tools,omitempty"`
	ExcludeTools []string `json:"excludeTools,omitempty"`
	// CustomTools reference host-JS tool closures executed via
	// AgentSessionCreateRequest.ExecuteTool — except Builtin markers, which the
	// implementation serves with Go-native tools bound to CWD. There is no
	// separate systemTools field: upstream CreateAgentSessionOptions has none —
	// callers (e.g. pi-dynamic-workflows' per-agent store tools) fold system
	// tools into customTools before createAgentSession, so they arrive here
	// already folded.
	CustomTools []AgentSessionTool `json:"customTools,omitempty"`
	// Session carries the SessionManager thin-handle fields; Settings and
	// ResourceLoader the SettingsManager / DefaultResourceLoader ones.
	Session        *AgentSessionStorage        `json:"session,omitempty"`
	Settings       *AgentSessionSettings       `json:"settings,omitempty"`
	ResourceLoader *AgentSessionResourceLoader `json:"resourceLoader,omitempty"`
	// SessionStartEvent is an upstream-shaped session_start payload to replay
	// into the child, when the SDK forwards one.
	SessionStartEvent json.RawMessage `json:"sessionStartEvent,omitempty"`
}

// ModelRuntimeRef names a model_runtime_v1 handle. The reserved handle
// "context" is the requesting extension's own ctx.modelRegistry view, in which
// case ExtensionID identifies the extension.
type ModelRuntimeRef struct {
	Handle      string `json:"handle"`
	ExtensionID string `json:"extensionId,omitempty"`
}

// AgentSessionTool is one customTools entry.
type AgentSessionTool struct {
	// Builtin names one of the SDK's createCodingTools markers ("read",
	// "bash", "edit", "write"): served natively, never called back into JS.
	// PromptSnippet/PromptGuidelines may ride along carrying upstream
	// createCodingTools' system-prompt contribution (bash only upstream); the
	// remaining callback fields are unused for builtin entries.
	Builtin string `json:"builtin,omitempty"`

	Name             string                   `json:"name,omitempty"`
	Label            string                   `json:"label,omitempty"`
	Description      string                   `json:"description,omitempty"`
	PromptSnippet    string                   `json:"promptSnippet,omitempty"`
	PromptGuidelines []string                 `json:"promptGuidelines,omitempty"`
	Parameters       json.RawMessage          `json:"parameters,omitempty"` // JSON Schema (D14)
	ExecutionMode    engine.ToolExecutionMode `json:"executionMode,omitempty"`
}

// AgentSessionStorage carries the SessionManager thin handle. Persisted false
// means SessionManager.inMemory(); when true, SessionDir is the real,
// already-created session directory the SDK computed from the handshake's
// sessionsRoot.
type AgentSessionStorage struct {
	Persisted  bool   `json:"persisted"`
	SessionDir string `json:"sessionDir,omitempty"`
	CWD        string `json:"cwd,omitempty"`
	// SessionInfoNames are appendSessionInfo names queued on the JS handle
	// before the session existed.
	SessionInfoNames []string `json:"sessionInfoNames,omitempty"`
}

// AgentSessionSettings is the inert SettingsManager handle: the implementation
// applies real settings (default model, thinking level, blockImages) from
// these roots when creating the session.
type AgentSessionSettings struct {
	CWD      string `json:"cwd,omitempty"`
	AgentDir string `json:"agentDir,omitempty"`
}

// AgentSessionResourceLoader is the DefaultResourceLoader handle.
// NoExtensions true is the plugin's structural anti-recursion guarantee: the
// child loads no extensions while skills/prompts/AGENTS.md context still load.
type AgentSessionResourceLoader struct {
	ExtensionID  string `json:"extensionId,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	AgentDir     string `json:"agentDir,omitempty"`
	NoExtensions bool   `json:"noExtensions,omitempty"`
}

// AgentSessionStats is the consumed subset of upstream SessionStats.
type AgentSessionStats struct {
	Tokens AgentSessionTokens `json:"tokens"`
	Cost   float64            `json:"cost"`
}

type AgentSessionTokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"total"`
}

// errAgentSessionNotWired is the stub's precise diagnostic: the capability is
// negotiated (the protocol layer is complete) but no runtime implementation
// has been installed yet.
var errAgentSessionNotWired = &ServiceError{
	Code: "agent_session_unimplemented",
	Message: "agent_session_v1: child agent sessions are negotiated by this host but their runtime is not wired in yet:" +
		" no AgentSessionService implementation is installed" +
		" (pending the agent.NewAgentSession-backed implementation installed via Manager.SetAgentSessionService)",
}

type stubAgentSessionService struct{}

func (stubAgentSessionService) CreateSession(context.Context, AgentSessionCreateRequest, AgentSessionCallbacks) (AgentSessionHandle, AgentSessionCreateResult, error) {
	return nil, AgentSessionCreateResult{}, errAgentSessionNotWired
}

// ReloadResources succeeds as a no-op: reload() is a prewarm whose real work
// happens at session create, and failing it would abort workflows before they
// reach the deliberately-failing CreateSession above.
func (stubAgentSessionService) ReloadResources(context.Context, AgentSessionResourceLoader) error {
	return nil
}
