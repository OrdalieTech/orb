# Go SDK

The `codingagent` package provides the public embedding API for pigo.

## Quick start

```go
import (
    "context"
    "github.com/OrdalieTech/pigo/ai/providers/faux"
    "github.com/OrdalieTech/pigo/codingagent"
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
| `Resources` | `*Resources` | `nil` | Fixed resource snapshot; bypasses default discovery when no ResourceLoader is supplied |
| `ResourceLoader` | `ResourceLoader` | `DefaultResourceLoader` | Reloadable extensions, skills, prompts, themes, context files, and system prompt |
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
- `PromptWithOptions(ctx, text string, options *PromptOptions) error` — prompt expansion, images, streaming delivery, source, and preflight callback
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
- `Agent() *agent.Agent` — direct access to agent state and idle waiting
- `GetActiveToolNames() []string` / `SetActiveToolsByName([]string) error` — inspect or replace active tools
- `SendUserMessage(ctx, content, options) error` / `SendCustomMessage(ctx, message, options) error` — extension-compatible message injection
- `State() agent.AgentState` — current agent state
- `WaitForIdle(ctx) error` — block until settled
- `BindExtensions(ctx) error` — emit the configured session_start once after host bindings are ready
- `Reload(ctx) error` — recreate native extension instances and emit reload lifecycle events

### Prompt options and direct messages

`PromptWithOptions` mirrors upstream prompt preflight and streaming behavior.
`PreflightResult` is called once with `true` after the prompt is accepted or
queued, and with `false` when expansion, an input hook, or streaming policy
rejects it before acceptance.

```go
expand := true
err := session.PromptWithOptions(ctx, "/review staged", &codingagent.PromptOptions{
    ExpandPromptTemplates: &expand,
    Source:                extensions.InputInteractive,
    PreflightResult:       func(accepted bool) { fmt.Println("accepted:", accepted) },
})
```

During an active run, set `StreamingBehavior` to `extensions.DeliverSteer` or
`extensions.DeliverFollowUp`; omitting it returns the same already-processing
error as upstream. `SendUserMessage` and `SendCustomMessage` expose the matching
extension message-delivery semantics without requiring an extension callback.

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

## Resource loading

`NewAgentSession` uses `DefaultResourceLoader` when neither `ResourceLoader` nor
the lower-level fixed `Resources` snapshot is supplied. The loader assembles inline
native extension factories, discovers skills, prompt templates, and context files,
and exposes the theme seam, then applies SDK overrides to one reloadable snapshot.

```go
loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
    CWD:      cwd,
    AgentDir: agentDir,
    SystemPromptOverride: func(_ *string) *string {
        prompt := "You are a concise assistant."
        return &prompt
    },
    SkillsOverride: func(current codingagent.ResourceSkillsResult) codingagent.ResourceSkillsResult {
        current.Skills = append(current.Skills, customSkill)
        return current
    },
})
if err != nil { panic(err) }
if err := loader.Reload(ctx, nil); err != nil { panic(err) }

result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
    ResourceLoader: loader,
})
```

The `ResourceLoader` interface is the full replacement seam:

```go
type ResourceLoader interface {
    GetExtensions() *extensions.Registry
    GetSkills() ResourceSkillsResult
    GetPrompts() ResourcePromptsResult
    GetThemes() ResourceThemesResult
    GetAgentsFiles() ResourceAgentsFilesResult
    GetSystemPrompt() *string
    GetAppendSystemPrompt() []string
    ExtendResources(ResourceExtensionPaths)
    Reload(context.Context, *ResourceLoaderReloadOptions) error
}
```

Callers that pass a custom loader own its initialization and reloads; the SDK
reloads only the default loader it constructs. A static loader may start ready,
as in `12_full_control`; use `DefaultResourceLoader` overrides when the
application wants to filter or append to discovered resources.

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
loader, _ := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
    CWD: cwd,
    ExtensionFactories: []extensions.Factory{func(api extensions.API) error {
        api.On(extensions.EventAgentStart, handler)
        api.RegisterTool(myToolDefinition)
        return nil
    }},
})
if err := loader.Reload(ctx, nil); err != nil { panic(err) }

result, _ := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
    ResourceLoader: loader,
})
```

Passing an `ExtensionRegistry` directly remains useful for hosts that already
own one, but `DefaultResourceLoader.ExtensionFactories` matches the upstream
inline-extension path and keeps extension lifecycle coupled to resource reloads.

### MemoryStore

`codingagent/memory.Store` is the durable memory seam; `memory.NewFileStore(dir)`
provides the append-only JSONL default. Embedders register
`plugins.MemoryWithStore(store)` to use a per-tenant database or another custom
backend without adding fields to `AgentSessionOptions`.

## Replaceable session runtime

`NewAgentSessionRuntime` owns the active `AgentSession` and recreates cwd-bound
services and extension instances on replacement. A host binds session-local
state once, then installs the same callback for every replacement:

```go
createRuntime := codingagent.CreateAgentSessionRuntimeFactory(
    func(_ context.Context, options codingagent.AgentSessionOptions) (*codingagent.AgentSessionResult, error) {
        services, err := codingagent.CreateAgentSessionServices(codingagent.CreateAgentSessionServicesOptions{
            CWD: options.CWD, AgentDir: options.AgentDir,
        })
        if err != nil { return nil, err }
        return codingagent.CreateAgentSessionFromServices(codingagent.CreateAgentSessionFromServicesOptions{
            Services: services, SessionManager: options.SessionManager,
            SessionStartEvent: options.SessionStartEvent,
            Model: options.Model, ThinkingLevel: options.ThinkingLevel,
            ScopedModels: options.ScopedModels, Tools: options.Tools,
            ExcludeTools: options.ExcludeTools, NoTools: options.NoTools,
            CustomTools: options.CustomTools,
        })
    },
)

host, err := codingagent.NewAgentSessionRuntime(ctx, options, createRuntime)
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

`CreateAgentSessionServices` builds the settings manager, model registry,
default resource loader, native extension registry, resource snapshot, and
diagnostics for one effective CWD. `CreateAgentSessionFromServices` reuses that
set with a caller-selected session manager, model, thinking level, and tool
policy. This split is the public seam for hosts that replace sessions while
keeping process-global inputs outside cwd-bound construction.

## Direct SessionRuntime access

For hosts that already assembled an agent, session manager, settings, and resources, use
`NewSessionRuntime` with a `SessionRuntimeConfig`.

## Examples

All examples live in `codingagent/examples/` and run against the faux provider:

| # | Name | Pattern |
|---|------|---------|
| 01 | minimal | Default construction, faux prompting, events, and direct Agent state |
| 02 | custom_model | Model selection and thinking level |
| 03 | custom_prompt | Replace or append the system prompt with DefaultResourceLoader overrides |
| 04 | skills | Discover, filter, and append skills through DefaultResourceLoader |
| 05 | tools | Tool allowlists, denylists, noTools |
| 06 | extensions | Inline extension factory, event interception, and custom tool registration |
| 07 | context_files | Discover and append AGENTS.md context files through DefaultResourceLoader |
| 08 | prompt_templates | Discover and append prompt templates through DefaultResourceLoader |
| 09 | api_keys | Default/custom ModelRegistry locations and runtime API-key callback |
| 10 | settings | Load, override, persist, and surface SettingsManager errors |
| 11 | sessions | In-memory, persistent, continue, list, and open flows |
| 12 | full_control | Explicit model, settings, custom ResourceLoader, session, and tools |
| 13 | session_runtime | Rebuild services with CreateAgentSessionServices and rebind after replacement |

Run any example:

```sh
go run ./codingagent/examples/01_minimal/
```

Each program uses the faux provider, so it performs no network requests. To run
the full matrix without reading or writing a real pi configuration, point both
the home directory and agent directory at temporary paths while preserving the
Go module cache used by your toolchain.
