# Go SDK

The `codingagent` package provides the public embedding API for pi-go.

## Quick start

```go
import (
    "context"
    "github.com/OrdalieTech/pi-go/ai/providers/faux"
    "github.com/OrdalieTech/pi-go/codingagent"
)

provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("Hello!")})

result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
    StreamFn: provider.StreamSimple,
    Model:    provider.GetModel(),
})
if err != nil { panic(err) }
defer result.Session.Dispose()

result.Session.Prompt(context.Background(), "Hello")
```

## Entry point

### NewAgentSession

```go
func NewAgentSession(opts AgentSessionOptions) (*AgentSessionResult, error)
```

Creates a configured `AgentSession` with upstream-compatible core construction:
it creates the internal Agent, wires streaming, resolves model and thinking-level
defaults, constructs built-in tools, restores messages from an existing session,
and returns a ready-to-prompt session. `AgentSessionRuntime` adds replacement
orchestration for hosts that support new, resume, fork, import, and reload flows.

### AgentSessionOptions

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `CWD` | `string` | `"."` | Working directory for tool execution and resource discovery |
| `AgentDir` | `string` | `~/.pi/agent` | Global config directory |
| `Model` | `*ai.Model` | restored/settings/available | Initial model; nil restores the session model, then tries settings and available authenticated models |
| `ThinkingLevel` | `ai.ModelThinkingLevel` | medium/off | Clamped to model's supported range |
| `ScopedModels` | `[]ScopedModel` | `nil` | Restricts CycleModel |
| `StreamFn` | `agent.StreamFn` | `aiapi.StreamSimple` | LLM streaming backend |
| `GetAPIKey` | `agent.GetAPIKeyFunc` | registry-derived for default streaming | API key resolver |
| `GetRequestAuth` | `agent.GetRequestAuthFunc` | registry-derived for default streaming | Request-time auth (OAuth, Copilot baseURL); takes precedence over GetAPIKey |
| `GetModelHeaders` | `agent.GetModelHeadersFunc` | registry-derived for default streaming | Per-request headers |
| `AvailableModels` | `func() []ai.Model` | `ModelRegistry.Available` | All available models |
| `ModelRegistry` | `*config.ModelRegistry` | from AgentDir | Model resolution, auth, restoration, and available-model discovery |
| `NoTools` | `string` | `""` | `"all"` disables all tools; `"builtin"` disables default built-ins |
| `Tools` | `[]string` | default set | Allowlist of tool names |
| `ExcludeTools` | `[]string` | `nil` | Denylist of tool names |
| `CustomTools` | `[]extensions.ToolDefinition` | `nil` | Additional tool definitions |
| `SessionManager` | `*sessionstore.SessionManager` | persistent (errors on failure) | Session persistence (upstream default: persistent) |
| `Settings` | `*config.SettingsManager` | from CWD | Runtime settings |
| `Resources` | `*Resources` | discovered | System prompt, skills, context files, prompt templates |
| `ExtensionRegistry` | `*extensions.Registry` | `nil` | Extension registry for event hooks and custom tools |
| `SessionStartEvent` | `*extensions.SessionStartEvent` | `nil` | Metadata for extension session_start event |
| `DeferExtensionStart` | `bool` | `false` | Leave session_start activation to `BindExtensions`; set automatically by AgentSessionRuntime |
| `ProjectTrustContext` | `extensions.ProjectTrustContext` | `nil` | Effective-CWD trust context passed to custom runtime factories |
| `SlashResolver` | `*SlashResolver` | auto | Slash command expansion |

### AgentSessionResult

```go
type AgentSessionResult struct {
    Session              *AgentSession
    ExtensionRegistry    *extensions.Registry
    ModelFallbackMessage string
    Services             *AgentSessionServices
    Diagnostics          []AgentSessionRuntimeDiagnostic
}
```

### AgentSession

`AgentSession` is a type alias for `SessionRuntime`. It exposes the full agent
lifecycle:

- `Prompt(ctx context.Context, input any, images ...*ai.ImageContent) error` — send a user message
- `PromptSync(ctx, text string) error` — prompt and wait for idle
- `Subscribe(func(any)) func()` — event callback, returns unsubscribe
- `SubscribeChan(bufferSize int) (<-chan any, func())` — channel adapter
- `Continue(ctx) error` — continue after tool use
- `Steer(text string) error` — inject steering text
- `FollowUp(text string) error` — queue follow-up
- `Abort()` — cancel current generation
- `Dispose()` — release resources
- `Compact(ctx, instructions string) (*harness.CompactionResult, error)` — compact message history
- `SetModel(ctx, model ai.Model) error` — change model
- `CycleModel(ctx) (*ModelCycleResult, error)` — cycle through available models
- `SetThinkingLevel(level) error` — change thinking budget
- `CycleThinkingLevel() (*ai.ModelThinkingLevel, error)` — cycle thinking levels
- `NavigateTree(ctx, targetID, options) (NavigateTreeResult, error)` — session tree navigation
- `State() agent.AgentState` — current agent state
- `WaitForIdle(ctx) error` — block until settled
- `BindExtensions(ctx) error` — emit the configured session_start once after host bindings are ready
- `Reload(ctx) error` — recreate native extension instances and emit reload lifecycle events

## Tools

Built-in tools are constructed automatically from CWD: read, bash, edit, write,
grep, find, ls. Control which are active via `Tools`, `NoTools`, and
`ExcludeTools`.

```go
// Read-only mode
result, _ := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
    Tools: []string{"read", "grep", "find", "ls"},
})

// No tools at all
result, _ := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
    NoTools: "all",
})

// Exclude write operations
result, _ := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
    ExcludeTools: []string{"write", "edit"},
})
```

Custom tools are registered via `CustomTools` or through an `ExtensionRegistry`.

## Events

Subscribe receives `agent.AgentEvent` variants plus these session-level event types:

| Type | Description |
|------|-------------|
| `SessionAgentEndEvent` | Agent turn complete with messages |
| `AgentSettledEvent` | Agent fully settled (idle) |
| `QueueUpdateEvent` | Steering/follow-up queue changed |
| `CompactionStartEvent` | Compaction beginning |
| `CompactionEndEvent` | Compaction finished |
| `AutoRetryStartEvent` | Automatic retry starting |
| `AutoRetryEndEvent` | Automatic retry finished |
| `EntryAppendedEvent` | New entry added to session |
| `SessionInfoChangedEvent` | Session metadata changed |
| `ThinkingLevelChangedEvent` | Thinking level changed |

## Channel adapter

```go
ch, cancel := session.SubscribeChan(64)
defer cancel()

for event := range ch {
    switch event.(type) {
    case codingagent.AgentSettledEvent:
        fmt.Println("settled")
    }
}
```

Delivery is ordered and lossless while the subscription is active, even when
the public buffer fills. Cancel is safe to call concurrently and multiple times;
it closes promptly and discards events still queued at cancellation.

## Resources

```go
type Resources struct {
    ContextFiles       []ContextFile
    SystemPrompt       *string
    AppendSystemPrompt []string
    Skills             []Skill
    PromptTemplates    []PromptTemplate
    Diagnostics        []ResourceDiagnostic
}
```

Pass via `AgentSessionOptions.Resources` to control system prompt, AGENTS.md
context files, skills, and prompt templates. When nil, those four resource classes
are discovered from CWD and AgentDir; full upstream `DefaultResourceLoader` package,
trust, settings-path, and extension behavior remains a Sprint 1 parity item.

## Session management

```go
// Persistent (default — matches upstream SessionManager.create())
sm, _ := sessionstore.Create(cwd, sessionDir)

// In-memory (no persistence)
sm, _ := sessionstore.InMemory(".")
```

## Settings

```go
settings, _ := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
settings.SetDefaultThinkingLevel(ai.ModelThinkingLow)
```

## Extensions

```go
registry := extensions.NewRegistry(".")
registry.Register("<my-ext>", func(api extensions.API) error {
    api.On(extensions.EventAgentStart, handler)
    api.RegisterTool(myToolDefinition)
    return nil
})

result, _ := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
    ExtensionRegistry: registry,
})
```

## Replaceable session runtime

`NewAgentSessionRuntime` owns the active `AgentSession` and recreates cwd-bound
services and extension instances on replacement. A host binds session-local
state once, then installs the same callback for every replacement:

```go
host, err := codingagent.NewAgentSessionRuntime(ctx, options)
if err != nil { panic(err) }
defer host.Dispose(ctx)

bind := func(session *codingagent.AgentSession) error {
    return session.BindExtensions(ctx)
}
host.SetRebindSession(bind)
if err := bind(host.Session()); err != nil { panic(err) }

_, err = host.NewSession(ctx, &extensions.NewSessionOptions{
    WithSession: func(ctx context.Context, replaced extensions.ReplacedSessionContext) error {
        return replaced.SendUserMessage(ctx, ai.NewUserText("continue here"), nil)
    },
})
```

`NewSession`, `SwitchSession`, `Fork`, and `ImportFromJSONL` emit the upstream
before/shutdown/start lifecycle, invalidate captured old contexts, rebind before
`WithSession`, and retain model-fallback, services, CWD, and diagnostic state.

## Direct SessionRuntime access

For hosts that already assembled an agent, session manager, settings, and resources, use
`NewSessionRuntime` with a `SessionRuntimeConfig`.

## Examples

All examples live in `codingagent/examples/` and run against the faux provider:

| # | Name | Pattern |
|---|------|---------|
| 01 | minimal | Simplest possible usage |
| 02 | custom_model | Model selection and thinking level |
| 03 | custom_prompt | System prompt via Resources |
| 04 | skills | Custom skills |
| 05 | tools | Tool allowlists, denylists, noTools |
| 06 | extensions | Extension event interception and custom tools |
| 07 | context_files | AGENTS.md context files |
| 08 | prompt_templates | Prompt template registration |
| 09 | api_keys | API key provider callback |
| 10 | settings | SettingsManager configuration |
| 11 | sessions | In-memory and persistent sessions |
| 12 | full_control | Explicit model, session, resources, and tool selection |
| 13 | session_runtime | AgentSessionRuntime replacement and host rebinding |

Run any example:

```sh
go run ./codingagent/examples/01_minimal/
```
