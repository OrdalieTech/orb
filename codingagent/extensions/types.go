package extensions

import (
	"context"
	"encoding/json"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/codingagent/tools"
)

type Mode string

const (
	ModeTUI   Mode = "tui"
	ModeRPC   Mode = "rpc"
	ModeJSON  Mode = "json"
	ModePrint Mode = "print"
)

type EventType string

const (
	EventProjectTrust          EventType = "project_trust"
	EventResourcesDiscover     EventType = "resources_discover"
	EventSessionStart          EventType = "session_start"
	EventSessionInfoChanged    EventType = "session_info_changed"
	EventSessionBeforeSwitch   EventType = "session_before_switch"
	EventSessionBeforeFork     EventType = "session_before_fork"
	EventSessionBeforeCompact  EventType = "session_before_compact"
	EventSessionCompact        EventType = "session_compact"
	EventSessionShutdown       EventType = "session_shutdown"
	EventSessionBeforeTree     EventType = "session_before_tree"
	EventSessionTree           EventType = "session_tree"
	EventContext               EventType = "context"
	EventBeforeProviderRequest EventType = "before_provider_request"
	EventBeforeProviderHeaders EventType = "before_provider_headers"
	EventAfterProviderResponse EventType = "after_provider_response"
	EventBeforeAgentStart      EventType = "before_agent_start"
	EventAgentStart            EventType = "agent_start"
	EventAgentEnd              EventType = "agent_end"
	EventAgentSettled          EventType = "agent_settled"
	EventTurnStart             EventType = "turn_start"
	EventTurnEnd               EventType = "turn_end"
	EventMessageStart          EventType = "message_start"
	EventMessageUpdate         EventType = "message_update"
	EventMessageEnd            EventType = "message_end"
	EventToolExecutionStart    EventType = "tool_execution_start"
	EventToolExecutionUpdate   EventType = "tool_execution_update"
	EventToolExecutionEnd      EventType = "tool_execution_end"
	EventModelSelect           EventType = "model_select"
	EventThinkingLevelSelect   EventType = "thinking_level_select"
	EventToolCall              EventType = "tool_call"
	EventToolResult            EventType = "tool_result"
	EventUserBash              EventType = "user_bash"
	EventInput                 EventType = "input"
)

type Event interface {
	Type() EventType
}

type Handler func(context.Context, Event, Context) (any, error)

type Factory func(API) error

type ProjectTrustDecision string

const (
	ProjectTrustYes       ProjectTrustDecision = "yes"
	ProjectTrustNo        ProjectTrustDecision = "no"
	ProjectTrustUndecided ProjectTrustDecision = "undecided"
)

type ProjectTrustEvent struct{ CWD string }

func (ProjectTrustEvent) Type() EventType { return EventProjectTrust }

type ProjectTrustResult struct {
	Trusted  ProjectTrustDecision `json:"trusted"`
	Remember bool                 `json:"remember,omitempty"`
}

type ProjectTrustContext interface {
	CWD() string
	Mode() Mode
	HasUI() bool
	UI() TrustUI
}

type ResourcesDiscoverReason string

const (
	ResourcesDiscoverStartup ResourcesDiscoverReason = "startup"
	ResourcesDiscoverReload  ResourcesDiscoverReason = "reload"
)

type ResourcesDiscoverEvent struct {
	CWD    string
	Reason ResourcesDiscoverReason
}

func (ResourcesDiscoverEvent) Type() EventType { return EventResourcesDiscover }

type ResourcesDiscoverResult struct {
	SkillPaths  []string `json:"skillPaths,omitempty"`
	PromptPaths []string `json:"promptPaths,omitempty"`
	ThemePaths  []string `json:"themePaths,omitempty"`
}

type DiscoveredPath struct {
	Path          string `json:"path"`
	ExtensionPath string `json:"extensionPath"`
}

type DiscoveredResources struct {
	SkillPaths  []DiscoveredPath `json:"skillPaths"`
	PromptPaths []DiscoveredPath `json:"promptPaths"`
	ThemePaths  []DiscoveredPath `json:"themePaths"`
}

type SessionStartReason string

const (
	SessionStartStartup SessionStartReason = "startup"
	SessionStartReload  SessionStartReason = "reload"
	SessionStartNew     SessionStartReason = "new"
	SessionStartResume  SessionStartReason = "resume"
	SessionStartFork    SessionStartReason = "fork"
)

type SessionStartEvent struct {
	Reason              SessionStartReason
	PreviousSessionFile *string
}

func (SessionStartEvent) Type() EventType { return EventSessionStart }

type SessionInfoChangedEvent struct{ Name *string }

func (SessionInfoChangedEvent) Type() EventType { return EventSessionInfoChanged }

type SessionSwitchReason string

const (
	SessionSwitchNew    SessionSwitchReason = "new"
	SessionSwitchResume SessionSwitchReason = "resume"
)

type SessionBeforeSwitchEvent struct {
	Reason            SessionSwitchReason
	TargetSessionFile *string
}

func (SessionBeforeSwitchEvent) Type() EventType { return EventSessionBeforeSwitch }

type ForkPosition string

const (
	ForkBefore ForkPosition = "before"
	ForkAt     ForkPosition = "at"
)

type SessionBeforeForkEvent struct {
	EntryID  string
	Position ForkPosition
}

func (SessionBeforeForkEvent) Type() EventType { return EventSessionBeforeFork }

type CompactionReason string

const (
	CompactionManual    CompactionReason = "manual"
	CompactionThreshold CompactionReason = "threshold"
	CompactionOverflow  CompactionReason = "overflow"
)

type SessionBeforeCompactEvent struct {
	Preparation        harness.CompactionPreparation
	BranchEntries      []session.SessionEntry
	CustomInstructions *string
	Reason             CompactionReason
	WillRetry          bool
	Signal             context.Context
}

func (SessionBeforeCompactEvent) Type() EventType { return EventSessionBeforeCompact }

type SessionCompactEvent struct {
	CompactionEntry session.SessionEntry
	FromExtension   bool
	Reason          CompactionReason
	WillRetry       bool
}

func (SessionCompactEvent) Type() EventType { return EventSessionCompact }

type SessionShutdownReason string

const (
	SessionShutdownQuit   SessionShutdownReason = "quit"
	SessionShutdownReload SessionShutdownReason = "reload"
	SessionShutdownNew    SessionShutdownReason = "new"
	SessionShutdownResume SessionShutdownReason = "resume"
	SessionShutdownFork   SessionShutdownReason = "fork"
)

type SessionShutdownEvent struct {
	Reason            SessionShutdownReason
	TargetSessionFile *string
}

func (SessionShutdownEvent) Type() EventType { return EventSessionShutdown }

type TreePreparation struct {
	TargetID            string
	OldLeafID           *string
	CommonAncestorID    *string
	EntriesToSummarize  []session.SessionEntry
	UserWantsSummary    bool
	CustomInstructions  *string
	ReplaceInstructions bool
	Label               *string
}

type SessionBeforeTreeEvent struct {
	Preparation TreePreparation
	Signal      context.Context
}

func (SessionBeforeTreeEvent) Type() EventType { return EventSessionBeforeTree }

type SessionTreeEvent struct {
	NewLeafID     *string
	OldLeafID     *string
	SummaryEntry  *session.SessionEntry
	FromExtension *bool
}

func (SessionTreeEvent) Type() EventType { return EventSessionTree }

type ContextEvent struct{ Messages agent.AgentMessages }

func (ContextEvent) Type() EventType { return EventContext }

type ContextResult struct{ Messages agent.AgentMessages }

type BeforeProviderRequestEvent struct{ Payload any }

func (BeforeProviderRequestEvent) Type() EventType { return EventBeforeProviderRequest }

// ProviderRequestResult distinguishes an unchanged payload from an explicit nil replacement.
type ProviderRequestResult struct {
	Payload any
	Replace bool
}

type BeforeProviderHeadersEvent struct{ Headers ai.ProviderHeaders }

func (BeforeProviderHeadersEvent) Type() EventType { return EventBeforeProviderHeaders }

type AfterProviderResponseEvent struct {
	Status  int
	Headers map[string]string
}

func (AfterProviderResponseEvent) Type() EventType { return EventAfterProviderResponse }

type ContextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Skill struct {
	Name                   string     `json:"name"`
	Description            string     `json:"description"`
	FilePath               string     `json:"filePath"`
	BaseDir                string     `json:"baseDir"`
	SourceInfo             SourceInfo `json:"sourceInfo"`
	DisableModelInvocation bool       `json:"disableModelInvocation"`
}

type SystemPromptOptions struct {
	CustomPrompt       *string           `json:"customPrompt,omitempty"`
	SelectedTools      []string          `json:"selectedTools,omitempty"`
	ToolSnippets       map[string]string `json:"toolSnippets,omitempty"`
	PromptGuidelines   []string          `json:"promptGuidelines,omitempty"`
	AppendSystemPrompt *string           `json:"appendSystemPrompt,omitempty"`
	CWD                string            `json:"cwd"`
	ContextFiles       []ContextFile     `json:"contextFiles,omitempty"`
	Skills             []Skill           `json:"skills,omitempty"`
}

type BeforeAgentStartEvent struct {
	Prompt              string
	Images              []*ai.ImageContent
	SystemPrompt        string
	SystemPromptOptions SystemPromptOptions
}

func (BeforeAgentStartEvent) Type() EventType { return EventBeforeAgentStart }

type CustomMessage struct {
	CustomType string `json:"customType"`
	Content    any    `json:"content"`
	Display    bool   `json:"display"`
	Details    any    `json:"details,omitempty"`
}

type BeforeAgentStartResult struct {
	Message      *CustomMessage
	SystemPrompt *string
}

type BeforeAgentStartCombinedResult struct {
	Messages     []CustomMessage
	SystemPrompt *string
}

type AgentStartEvent struct{}

func (AgentStartEvent) Type() EventType { return EventAgentStart }

type AgentEndEvent struct{ Messages agent.AgentMessages }

func (AgentEndEvent) Type() EventType { return EventAgentEnd }

type AgentSettledEvent struct{}

func (AgentSettledEvent) Type() EventType { return EventAgentSettled }

type TurnStartEvent struct {
	TurnIndex int
	Timestamp int64
}

func (TurnStartEvent) Type() EventType { return EventTurnStart }

type TurnEndEvent struct {
	TurnIndex   int
	Message     agent.AgentMessage
	ToolResults []*ai.ToolResultMessage
}

func (TurnEndEvent) Type() EventType { return EventTurnEnd }

type MessageStartEvent struct{ Message agent.AgentMessage }

func (MessageStartEvent) Type() EventType { return EventMessageStart }

type MessageUpdateEvent struct {
	Message               agent.AgentMessage
	AssistantMessageEvent ai.AssistantMessageEvent
}

func (MessageUpdateEvent) Type() EventType { return EventMessageUpdate }

type MessageEndEvent struct{ Message agent.AgentMessage }

func (MessageEndEvent) Type() EventType { return EventMessageEnd }

type MessageEndResult struct{ Message agent.AgentMessage }

type ToolExecutionStartEvent struct {
	ToolCallID string
	ToolName   string
	Args       any
}

func (ToolExecutionStartEvent) Type() EventType { return EventToolExecutionStart }

type ToolExecutionUpdateEvent struct {
	ToolCallID    string
	ToolName      string
	Args          any
	PartialResult any
}

func (ToolExecutionUpdateEvent) Type() EventType { return EventToolExecutionUpdate }

type ToolExecutionEndEvent struct {
	ToolCallID string
	ToolName   string
	Result     any
	IsError    bool
}

func (ToolExecutionEndEvent) Type() EventType { return EventToolExecutionEnd }

type ModelSelectSource string

const (
	ModelSelectSet     ModelSelectSource = "set"
	ModelSelectCycle   ModelSelectSource = "cycle"
	ModelSelectRestore ModelSelectSource = "restore"
)

type ModelSelectEvent struct {
	Model         *ai.Model
	PreviousModel *ai.Model
	Source        ModelSelectSource
}

func (ModelSelectEvent) Type() EventType { return EventModelSelect }

type ThinkingLevelSelectEvent struct {
	Level         agent.ThinkingLevel
	PreviousLevel agent.ThinkingLevel
}

func (ThinkingLevelSelectEvent) Type() EventType { return EventThinkingLevelSelect }

type ToolCallEvent struct {
	ToolCallID string
	ToolName   string
	Input      map[string]any
}

func (ToolCallEvent) Type() EventType { return EventToolCall }

type ToolCallResult struct {
	Block  bool   `json:"block,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type ToolResultEvent struct {
	ToolCallID string
	ToolName   string
	Input      map[string]any
	Content    ai.ToolResultContent
	Details    any
	IsError    bool
	Usage      *ai.Usage
}

func (ToolResultEvent) Type() EventType { return EventToolResult }

type ToolResultResult struct {
	Content *ai.ToolResultContent
	Details *any
	IsError *bool
	Usage   *ai.Usage
}

type BashResult struct {
	Output     string  `json:"output"`
	ExitCode   *int    `json:"exitCode"`
	Cancelled  bool    `json:"cancelled"`
	Truncated  bool    `json:"truncated"`
	FullOutput *string `json:"fullOutputPath,omitempty"`
}

type UserBashEvent struct {
	Command            string
	ExcludeFromContext bool
	CWD                string
}

func (UserBashEvent) Type() EventType { return EventUserBash }

type UserBashResult struct {
	Operations tools.BashOperations
	Result     *BashResult
}

type InputSource string

const (
	InputInteractive InputSource = "interactive"
	InputRPC         InputSource = "rpc"
	InputExtension   InputSource = "extension"
)

type DeliveryMode string

const (
	DeliverSteer    DeliveryMode = "steer"
	DeliverFollowUp DeliveryMode = "followUp"
	DeliverNextTurn DeliveryMode = "nextTurn"
)

type InputEvent struct {
	Text              string
	Images            []*ai.ImageContent
	Source            InputSource
	StreamingBehavior *DeliveryMode
}

func (InputEvent) Type() EventType { return EventInput }

type InputAction string

const (
	InputContinue  InputAction = "continue"
	InputTransform InputAction = "transform"
	InputHandled   InputAction = "handled"
)

type InputResult struct {
	Action InputAction        `json:"action"`
	Text   string             `json:"text,omitempty"`
	Images []*ai.ImageContent `json:"images,omitempty"`
}

type SessionBeforeSwitchResult struct{ Cancel bool }

type SessionBeforeForkResult struct {
	Cancel                  bool
	SkipConversationRestore bool
}

type SessionBeforeCompactResult struct {
	Cancel     bool
	Compaction *harness.CompactionResult
}

type TreeSummary struct {
	Summary string
	Details any
	Usage   *ai.Usage
}

type SessionBeforeTreeResult struct {
	Cancel              bool
	Summary             *TreeSummary
	CustomInstructions  *string
	ReplaceInstructions *bool
	Label               *string
}

type SourceScope string

const (
	SourceScopeUser      SourceScope = "user"
	SourceScopeProject   SourceScope = "project"
	SourceScopeTemporary SourceScope = "temporary"
)

type SourceOrigin string

const (
	SourceOriginPackage  SourceOrigin = "package"
	SourceOriginTopLevel SourceOrigin = "top-level"
)

type SourceInfo struct {
	Path    string       `json:"path"`
	Source  string       `json:"source"`
	Scope   SourceScope  `json:"scope"`
	Origin  SourceOrigin `json:"origin"`
	BaseDir *string      `json:"baseDir,omitempty"`
}

type ToolRenderResultOptions struct {
	Expanded  bool
	IsPartial bool
}

type ToolRenderContext struct {
	Args             any
	ToolCallID       string
	Invalidate       func()
	LastComponent    Component
	State            map[string]any
	CWD              string
	ExecutionStarted bool
	ArgsComplete     bool
	IsPartial        bool
	Expanded         bool
	ShowImages       bool
	IsError          bool
}

type ToolDefinition struct {
	Name             string
	Label            string
	Description      string
	PromptSnippet    string
	PromptGuidelines []string
	Parameters       ai.JSONSchema
	RenderShell      RenderShell
	PrepareArguments agent.PrepareArgumentsFunc
	ExecutionMode    agent.ToolExecutionMode
	Execute          func(context.Context, string, any, agent.AgentToolUpdateCallback, Context) (agent.AgentToolResult, error)
	RenderCall       func(any, Theme, ToolRenderContext) Component
	RenderResult     func(agent.AgentToolResult, ToolRenderResultOptions, Theme, ToolRenderContext) Component
}

type RenderShell string

const (
	RenderShellDefault RenderShell = "default"
	RenderShellSelf    RenderShell = "self"
)

type RegisteredTool struct {
	Definition ToolDefinition
	SourceInfo SourceInfo
}

type ToolInfo struct {
	Name             string        `json:"name"`
	Description      string        `json:"description"`
	Parameters       ai.JSONSchema `json:"parameters"`
	PromptGuidelines []string      `json:"promptGuidelines,omitempty"`
	SourceInfo       SourceInfo    `json:"sourceInfo"`
}

type AutocompleteItem struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Command struct {
	Name                   string
	SourceInfo             SourceInfo
	Description            string
	GetArgumentCompletions func(context.Context, string) ([]AutocompleteItem, error)
	Handler                func(context.Context, string, CommandContext) error
}

type ResolvedCommand struct {
	Command
	InvocationName string
}

type Shortcut struct {
	Shortcut      string
	Description   string
	Handler       func(context.Context, Context) error
	ExtensionPath string
}

type FlagType string

const (
	FlagBoolean FlagType = "boolean"
	FlagString  FlagType = "string"
)

type Flag struct {
	Name          string
	Description   string
	Type          FlagType
	Default       any
	ExtensionPath string
}

type SlashCommandSource string

const (
	SlashCommandExtension SlashCommandSource = "extension"
	SlashCommandPrompt    SlashCommandSource = "prompt"
	SlashCommandSkill     SlashCommandSource = "skill"
)

type SlashCommandInfo struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Source      SlashCommandSource `json:"source"`
	SourceInfo  SourceInfo         `json:"sourceInfo"`
}

type SendMessageOptions struct {
	TriggerTurn bool
	DeliverAs   DeliveryMode
}

type SendUserMessageOptions struct{ DeliverAs DeliveryMode }

type ExecOptions struct {
	Context context.Context
	Timeout int64
	CWD     string
	Env     []string
}

type ExecResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Code   int    `json:"code"`
	Killed bool   `json:"killed"`
}

type ContextUsage struct {
	Tokens        *int64
	ContextWindow int64
	Percent       *float64
}

type CompactOptions struct {
	CustomInstructions string
	OnComplete         func(harness.CompactionResult)
	OnError            func(error)
}

type ReadonlySessionManager interface {
	IsPersisted() bool
	GetCWD() string
	GetSessionDir() string
	GetSessionID() string
	GetSessionFile() string
	GetLeafID() *string
	GetLeafEntry() *session.SessionEntry
	GetEntry(string) *session.SessionEntry
	GetEntries() []session.SessionEntry
	GetHeader() *session.SessionHeader
	GetSessionName() *string
	GetLabel(string) *string
	GetChildren(string) []session.SessionEntry
	GetBranch(...string) []session.SessionEntry
	GetTree() []*session.SessionTreeNode
	BuildContextEntries() []session.SessionEntry
	BuildSessionContext() session.SessionContext
}

type ModelRegistry interface {
	Reload() error
	Error() string
	Models() []ai.Model
	Find(provider, id string) (ai.Model, bool)
	HasConfiguredAuth(provider string, env map[string]string) bool
	GetProviderAuthStatus(provider string, env map[string]string) AuthStatus
	IsUsingOAuth(provider string) bool
	Available(env map[string]string) []ai.Model
	AvailableWithError(env map[string]string) ([]ai.Model, error)
	ResolveAPIKey(context.Context, string, map[string]string) (*string, error)
	ResolveProviderAuth(context.Context, string, map[string]string) (*aiauth.AuthResult, error)
	ResolveModelHeaders(context.Context, ai.Model, map[string]string, ...*string) (*map[string]string, error)
	StreamSimple(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error)
	Provider(string) (Provider, bool)
	ProviderDisplayName(string) string
	ProviderAuth(string) aiauth.ProviderAuth
	RegisteredProviderConfig(string) (ProviderConfig, bool)
	RegisteredNativeProvider(string) (Provider, bool)
	RegisteredProviderIDs() []string
	RegisterProvider(Provider) error
	RegisterProviderConfig(string, ProviderConfig) error
	UnregisterProvider(string) error
}

type AuthStatus struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
	Label      string `json:"label,omitempty"`
}

var _ ReadonlySessionManager = (*session.SessionManager)(nil)

type Context interface {
	UI() UI
	Mode() Mode
	HasUI() bool
	CWD() string
	SessionManager() ReadonlySessionManager
	ModelRegistry() ModelRegistry
	Model() *ai.Model
	IsIdle() bool
	IsProjectTrusted() bool
	Signal() context.Context
	Abort()
	HasPendingMessages() bool
	Shutdown()
	GetContextUsage() *ContextUsage
	Compact(*CompactOptions)
	GetSystemPrompt() string
}

type CommandContext interface {
	Context
	GetSystemPromptOptions() SystemPromptOptions
	WaitForIdle(context.Context) error
	NewSession(context.Context, *NewSessionOptions) (SessionReplacementResult, error)
	Fork(context.Context, string, *ForkOptions) (SessionReplacementResult, error)
	NavigateTree(context.Context, string, *NavigateTreeOptions) (SessionReplacementResult, error)
	SwitchSession(context.Context, string, *SwitchSessionOptions) (SessionReplacementResult, error)
	Reload(context.Context) error
}

type ReplacedSessionContext interface {
	CommandContext
	SendMessage(context.Context, CustomMessage, *SendMessageOptions) error
	SendUserMessage(context.Context, ai.UserContent, *SendUserMessageOptions) error
}

type SessionReplacementResult struct{ Cancelled bool }

type NewSessionOptions struct {
	ParentSession string
	Setup         func(*session.SessionManager) error
	WithSession   func(context.Context, ReplacedSessionContext) error
}

type ForkOptions struct {
	Position    ForkPosition
	WithSession func(context.Context, ReplacedSessionContext) error
}

type NavigateTreeOptions struct {
	Summarize           bool
	CustomInstructions  string
	ReplaceInstructions bool
	Label               string
}

type SwitchSessionOptions struct {
	WithSession func(context.Context, ReplacedSessionContext) error
}

type OAuthCredentials struct {
	Refresh string         `json:"refresh"`
	Access  string         `json:"access"`
	Expires int64          `json:"expires"`
	Extra   map[string]any `json:"-"`
}

func (credentials OAuthCredentials) MarshalJSON() ([]byte, error) {
	value := make(map[string]any, len(credentials.Extra)+3)
	for name, field := range credentials.Extra {
		value[name] = field
	}
	value["refresh"] = credentials.Refresh
	value["access"] = credentials.Access
	value["expires"] = credentials.Expires
	return json.Marshal(value)
}

func (credentials *OAuthCredentials) UnmarshalJSON(data []byte) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if raw := value["refresh"]; raw != nil {
		if err := json.Unmarshal(raw, &credentials.Refresh); err != nil {
			return err
		}
	}
	if raw := value["access"]; raw != nil {
		if err := json.Unmarshal(raw, &credentials.Access); err != nil {
			return err
		}
	}
	if raw := value["expires"]; raw != nil {
		if err := json.Unmarshal(raw, &credentials.Expires); err != nil {
			return err
		}
	}
	delete(value, "refresh")
	delete(value, "access")
	delete(value, "expires")
	if len(value) > 0 {
		credentials.Extra = make(map[string]any, len(value))
		for name, raw := range value {
			var field any
			if err := json.Unmarshal(raw, &field); err != nil {
				return err
			}
			credentials.Extra[name] = field
		}
	}
	return nil
}

type OAuthAuthInfo struct {
	URL          string
	Instructions string
}

type OAuthDeviceCodeInfo struct {
	UserCode         string
	VerificationURI  string
	IntervalSeconds  int
	ExpiresInSeconds int
}

type OAuthPrompt struct {
	Message     string
	Placeholder string
	AllowEmpty  bool
}

type OAuthSelectOption struct {
	ID    string
	Label string
}

type OAuthSelectPrompt struct {
	Message string
	Options []OAuthSelectOption
}

type OAuthLoginCallbacks struct {
	Signal            context.Context
	OnAuth            func(OAuthAuthInfo)
	OnDeviceCode      func(OAuthDeviceCodeInfo)
	OnPrompt          func(OAuthPrompt) (string, error)
	OnProgress        func(string)
	OnManualCodeInput func() (string, error)
	OnSelect          func(OAuthSelectPrompt) (*string, error)
}

type OAuthProvider struct {
	Name         string
	Login        func(context.Context, OAuthLoginCallbacks) (OAuthCredentials, error)
	RefreshToken func(context.Context, OAuthCredentials) (OAuthCredentials, error)
	GetAPIKey    func(OAuthCredentials) (string, error)
	ModifyModels func([]ai.Model, OAuthCredentials) ([]ai.Model, error)
}

type RefreshModelsContext struct {
	Credential   *aiauth.Credential
	Store        ProviderModelStore
	AllowNetwork bool
	Force        bool
	Signal       context.Context
}

type ProviderModelsStoreEntry struct {
	Models    []ai.Model `json:"models"`
	CheckedAt *int64     `json:"checkedAt,omitempty"`
}

type ProviderModelStore interface {
	Read(context.Context) (*ProviderModelsStoreEntry, error)
	Write(context.Context, ProviderModelsStoreEntry) error
	Delete(context.Context) error
}

type ProviderModelConfig struct {
	ID               string
	Name             string
	API              ai.API
	BaseURL          string
	Reasoning        bool
	ThinkingLevelMap *map[ai.ModelThinkingLevel]*string
	Input            ai.InputModalities
	Cost             ai.ModelCost
	ContextWindow    float64
	MaxTokens        float64
	Headers          map[string]string
	Compat           json.RawMessage
}

type ProviderConfig struct {
	Name          string
	BaseURL       string
	APIKey        string
	API           ai.API
	Stream        agent.StreamFn
	Headers       map[string]string
	AuthHeader    *bool
	Models        []ProviderModelConfig
	RefreshModels func(RefreshModelsContext) ([]ProviderModelConfig, error)
	OAuth         *OAuthProvider
	Defined       map[string]bool
	// RegistrationValues retains owner-scoped values solely so the bridge can
	// expose the effective registration through the owning VM.
	RegistrationValues map[string]any
}

type Provider struct {
	ID            string
	Name          string
	BaseURL       string
	Headers       map[string]string
	Auth          aiauth.ProviderAuth
	Config        ProviderConfig
	FilterModels  func([]ai.Model, *aiauth.Credential) ([]ai.Model, error)
	GetModels     func() ([]ai.Model, error)
	RefreshModels func(RefreshModelsContext) error
	Stream        agent.StreamFn
	StreamSimple  agent.StreamFn
	// RegistrationValue is returned only to the VM that owns it; callbacks
	// above remain the cross-VM representation held by the shared registry.
	RegistrationValue any
}

type API interface {
	On(EventType, Handler)
	RegisterTool(ToolDefinition)
	RegisterCommand(string, Command)
	RegisterShortcut(string, Shortcut)
	RegisterFlag(string, Flag)
	GetFlag(string) (any, bool)
	RegisterMessageRenderer(string, MessageRenderer)
	RegisterEntryRenderer(string, EntryRenderer)
	SendMessage(context.Context, CustomMessage, *SendMessageOptions) error
	SendUserMessage(context.Context, ai.UserContent, *SendUserMessageOptions) error
	AppendEntry(context.Context, string, any) error
	SetSessionName(context.Context, string) error
	GetSessionName(context.Context) (*string, error)
	SetLabel(context.Context, string, *string) error
	Exec(context.Context, string, []string, *ExecOptions) (ExecResult, error)
	GetActiveTools() ([]string, error)
	GetAllTools() ([]ToolInfo, error)
	SetActiveTools([]string) error
	GetCommands() ([]SlashCommandInfo, error)
	SetModel(context.Context, *ai.Model) (bool, error)
	GetThinkingLevel() (agent.ThinkingLevel, error)
	SetThinkingLevel(agent.ThinkingLevel) error
	RegisterProvider(Provider)
	RegisterProviderConfig(string, ProviderConfig)
	UnregisterProvider(string)
	Events() EventBus
}
