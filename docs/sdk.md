# Go SDK

The `agent` and `agent` packages provide Orb's public embedding APIs.

## Quick start

```go
import (
    "context"
    "github.com/OrdalieTech/orb/ai/providers/faux"
    "github.com/OrdalieTech/orb/agent"
)

provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("Hello!")})

result, err := agent.NewAgentSession(agent.AgentSessionOptions{
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
- `Compact(ctx, instructions string) (*session.CompactionResult, error)` — compact message history
- `SetModel(ctx, model ai.Model) error` — change model
- `CycleModel(ctx) (*ModelCycleResult, error)` — cycle through available models
- `SetThinkingLevel(level) error` — change thinking budget
- `CycleThinkingLevel() (*ai.ModelThinkingLevel, error)` — cycle thinking levels
- `NavigateTree(ctx, targetID, options) (NavigateTreeResult, error)` — session tree navigation
- `Agent() *engine.Agent` — direct access to agent state and idle waiting
- `GetActiveToolNames() []string` / `SetActiveToolsByName([]string) error` — inspect or replace active tools
- `SendUserMessage(ctx, content, options) error` / `SendCustomMessage(ctx, message, options) error` — extension-compatible message injection
- `State() engine.AgentState` — current agent state
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
err := session.PromptWithOptions(ctx, "/review staged", &agent.PromptOptions{
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
result, _ := agent.NewAgentSession(agent.AgentSessionOptions{
    Tools: []string{"read", "grep", "find", "ls"},
})

// No tools at all
result, _ := agent.NewAgentSession(agent.AgentSessionOptions{
    NoTools: "all",
})

// Exclude write operations
result, _ := agent.NewAgentSession(agent.AgentSessionOptions{
    ExcludeTools: []string{"write", "edit"},
})
```

Custom tools are registered via `CustomTools` or through an `ExtensionRegistry`.

## Events

Subscribe receives `engine.AgentEvent` variants plus these session-level event types:

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
    case agent.AgentSettledEvent:
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
loader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
    CWD:      cwd,
    AgentDir: agentDir,
    SystemPromptOverride: func(_ *string) *string {
        prompt := "You are a concise assistant."
        return &prompt
    },
    SkillsOverride: func(current agent.ResourceSkillsResult) agent.ResourceSkillsResult {
        current.Skills = append(current.Skills, customSkill)
        return current
    },
})
if err != nil { panic(err) }
if err := loader.Reload(ctx, nil); err != nil { panic(err) }

result, err := agent.NewAgentSession(agent.AgentSessionOptions{
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

For SDK consumers, native persistence is opt-in through `storage/sqlite.Open(ctx, absolutePath)`.
Use `db.Sessions(namespace)` as a `harness.SessionRepo`, then adapt its storage
with `sessionstore.FromHarnessStorage(s.Storage(), sessionstore.WithHarnessRepo(repo))`.
The caller owns the database lifetime. Sessions have IDs, no synthetic file paths;
`ByteSessionStorage.Bytes()` exports Pi v3 JSONL and `repo.Import` admits it without
replacing conflicting history. Existing SDK constructors remain file-backed.

Global settings, credentials and trust accept `db.Document(namespace, key)` through
`WithGlobalDocument`, `NewAuthStorageWithDocument` and
`NewProjectTrustStoreWithDocument`. These hooks share the existing codecs and do
not migrate files automatically. Only importing `storage/sqlite` links the driver.

`db.Foreign(profile)` is a disposable remote-session cache, separate from owned session
repositories. Keys include the peer, namespace and session ID. Previews retain at most eight
visible user/assistant messages, 4 KiB per message and 32 KiB encoded per session; seven-day
expiry and 128-per-peer / 1,024-per-profile limits bound retention. Allocate a refresh token
before network I/O with `Begin`; `Forget` fences older responses as well as deleting content.
The CLI uses SQLite by default at `$ORB_STATE_HOME/orb.db`, or `~/.orb/state/orb.db`.
An explicit `PI_CODING_AGENT_DIR` defaults to `<agentDir>/state/orb.db`; otherwise an explicit
`ORB_BRIDGE_HOME` defaults to `<bridgeHome>/state/orb.db`. These are assembly choices, never
implicit SDK configuration.
Only visited conversations are cached; reopening always consults the owning Bridge.

Native startup automatically migrates existing v1/v2/v3 sessions and global capability state after
checking that legacy Orb processes have stopped. To include additional session roots before the
first startup, run `orb storage migrate /absolute/legacy/root ...`. Originals are retained and
must not be reopened for writing by old binaries. Failed migrations leave them intact; resume
with the same inventory and source bytes. A changed source after admission is rejected, not
silently overwritten. Harness v4 remains available through its existing SDK APIs, but v4 journals
are not admitted by the native v3 adapter.

Use `orb storage import file.jsonl`, `orb storage export <id> file.jsonl`, and
`orb storage backup /private/directory/backup.db`. `orb storage restore backup.db` recovers
conversations without overwriting conflicting current history or restoring old authority and
receipts; it is not an in-place full-state rollback. Export files and backups never overwrite an
existing destination. `orb --export <id> output.html` also supports `.md` output. Native
`--session-dir` is replaced by the state-root setting and explicit migration/import commands;
`orb --pi-files ...` retains the file-based CLI contract for a separate compatibility root.

Global files can be deliberately exchanged with `orb storage config export settings.json file.json`
and `orb storage config import settings.json file.json`, including `models.json`, `keybindings.json`,
`auth.json`, `accounts.json`, `trust.json` and `models-store.json`. Import atomically replaces that
native document; restart to refresh already-loaded configuration. Exports are private
and refuse to overwrite files. Never edit the retained migration originals to configure native Orb.

Models, account catalogs and chat/memory also accept explicit persistence through
`NewModelRegistryWithDocuments`, `accounts.NewStoreWithDocument`, `chat.WithPersistence`,
`chat.NewLocalWithSpool` and `db.Memory(namespace)`. `AgentSessionRuntime.SetSessionClaim`
lets a host reject destination ownership before tearing down the current runtime; it is unset
by default and changes no existing interface method set.


## Settings

```go
settings, _ := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
settings.SetDefaultThinkingLevel(ai.ModelThinkingLow)
```

## Extensions

```go
loader, _ := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
    CWD: cwd,
    ExtensionFactories: []extensions.Factory{func(api extensions.API) error {
        api.On(extensions.EventAgentStart, handler)
        api.RegisterTool(myToolDefinition)
        return nil
    }},
})
if err := loader.Reload(ctx, nil); err != nil { panic(err) }

result, _ := agent.NewAgentSession(agent.AgentSessionOptions{
    ResourceLoader: loader,
})
```

Passing an `ExtensionRegistry` directly remains useful for hosts that already
own one, but `DefaultResourceLoader.ExtensionFactories` matches the upstream
inline-extension path and keeps extension lifecycle coupled to resource reloads.

### MemoryStore

`memory.Store` from `github.com/OrdalieTech/orb/memory` is the tenant-scoped durable seam.
`memory.NewFileStore(dir)` is the append-only JSONL default for one local profile. A plain
`engine.Agent` attaches the shared behavior directly:

```go
import (
    "github.com/OrdalieTech/orb/engine"
    "github.com/OrdalieTech/orb/memory"
    agentmemory "github.com/OrdalieTech/orb/memory/agent"
)

store, _ := memory.NewFileStore(dir)
runtime := engine.NewAgent(stream, engine.WithInitialState(state))
if err := agentmemory.Attach(ctx, runtime, store); err != nil { panic(err) }
```

The bundled coding-agent plugin remains disabled by default. Local users enable
`"plugins":{"memory":true}`; `agent` embedders register
`plugins.MemoryWithStore(store)`. Both paths use the same `memory/agent` runtime.

Enablement is the only mode. At session start the plugin freezes a bounded `USER PROFILE`
(1,375 Unicode characters) and `MEMORY` (2,200) into the system prompt. `remember` adds a
declarative fact, `recall` searches all items, `replace` consolidates one uniquely matched
entry, and `forget` removes one. Capacity errors expose the bounded current section so the
model can replace or remove entries. All behavior uses the Store's bounded 100-item window.
The runtime reserves and hides the tags
`orb:memory:user` and `orb:memory:memory`; existing untagged items remain `MEMORY`, while
items tagged `user` remain in `USER PROFILE`.

Service backends should pass one tenant-bound Store handle per user; the interface has no tenant
field and sharing an unscoped Store leaks memories by construction. Stores must accept concurrent
calls. Implement `memory.TransactionalStore` when several sessions or processes can mutate one
tenant concurrently, so remember/replace/forget run as one transaction. Without that optional
seam, one runtime remains coherent but separate runtimes can interleave compound mutations.
`FileStore` implements transactions across processes, but its reads scan the append-only log, so
use an indexed database Store rather than `FileStore` for service-scale history.

## Replaceable session runtime

`NewAgentSessionRuntime` owns the active `AgentSession` and recreates cwd-bound
services and extension instances on replacement. A host binds session-local
state once, then installs the same callback for every replacement:

```go
createRuntime := agent.CreateAgentSessionRuntimeFactory(
    func(_ context.Context, options agent.AgentSessionOptions) (*agent.AgentSessionResult, error) {
        services, err := agent.CreateAgentSessionServices(agent.CreateAgentSessionServicesOptions{
            CWD: options.CWD, AgentDir: options.AgentDir,
        })
        if err != nil { return nil, err }
        return agent.CreateAgentSessionFromServices(agent.CreateAgentSessionFromServicesOptions{
            Services: services, SessionManager: options.SessionManager,
            SessionStartEvent: options.SessionStartEvent,
            Model: options.Model, ThinkingLevel: options.ThinkingLevel,
            ScopedModels: options.ScopedModels, Tools: options.Tools,
            ExcludeTools: options.ExcludeTools, NoTools: options.NoTools,
            CustomTools: options.CustomTools,
        })
    },
)

host, err := agent.NewAgentSessionRuntime(ctx, options, createRuntime)
if err != nil { panic(err) }
defer host.Dispose(ctx)

bind := func(session *agent.AgentSession) error {
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

All examples live in `agent/examples/` and run against the faux provider:

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
go run ./agent/examples/01_minimal/
```

Each program uses the faux provider, so it performs no network requests. To run
the full matrix without reading or writing a real pi configuration, point both
the home directory and agent directory at temporary paths while preserving the
Go module cache used by your toolchain.

## Serving Orb at scale

The layers below the CLI are built for many differently-configured instances in
one process (P1/P3): `engine/` and `ai/` hold no mutable package state beyond
what is listed here, read no environment implicitly in the embedding path, and
are transitively TUI-free (enforced by `internal/layering`). The contract for a
server embedder:

- **Stream function per instance.** Pass the stream function explicitly to
  every `engine.NewAgent`/loop call. `engine.SetDefaultStreamFn` is a
  process-wide fallback (a faithful port of upstream `setDefaultStreamFn`):
  never call it from multi-tenant code — one tenant's default would become
  every tenant's default.
- **Credentials per instance.** Inject provider credentials through the
  explicit resolution paths (request auth resolvers, `ai/auth` overrides).
  The environment-variable fallbacks in `ai/` exist for CLI parity; on a
  shared server they are a cross-tenant leak vector — a tenant request must
  never fall through to the process environment.
- **Sessions per instance.** Give each instance its own session directory or
  an in-memory session; never share the process working directory or the
  default `~/.pi/agent/sessions` layout between tenants.
- **Shared vs per-instance.** Safe to share process-wide (immutable or
  deliberately global): the builtin model catalog (`ai/models.Builtin`,
  `sync.OnceValue`), provider constructors, HTTP transports, terminal
  capability detection (`internal/termcaps` — one process, one terminal).
  Per-instance, always: the agent and its options, attachments (memory,
  tools), session storage, credentials, settings, permission policy.
- **Composition is code.** A server assembles by constructing Go values —
  the dsh-style "profile" is a `main` package. The `agent/assembly` package
  is the CLI's composition surface; embedders do not need it and must not
  gate tenant behavior on the CLI's settings files.


## Optional instance control and Bridge

Existing SDK constructors and subscriptions are unchanged. Importing `agent` does not import
Bridge or Tailcat and starts no bridge process. `connect/agent.Attach` adapts an existing
`*agent.AgentSessionRuntime`; the caller supplies the runtime lifetime, durable ledger store,
persistent InstanceID, and current authorization callback. Its `Close` releases observation
subscriptions without disposing the runtime. Multiple bridges can attach independently.

For in-process assembly, create `bridge.Open(metadataStore, create)`, enroll through the local
owner, and construct `connect/agent.Attach(ctx, runtime, Options{InstanceID: instance.ID,
Store: ledgerStore, Authorize: bridge.Authorize})`. Register
`connect.NewLocal(attachment.Invoke)` with `bridge.Attach`, then give the returned generation
to `attachment.SetGeneration` before exposing the bridge. Keep metadata and ledger stores
separate. `connect.Store` is the host-supplied persistence boundary; a successful `Save` must
mean durable replacement. The native implementation is `bridge/hosts/native.OpenStore`.

`bridge.Connect` accepts a caller-owned `net.Conn` and performs pinned mutual TLS and hello
negotiation. Native/Tailcat hosting is an explicit CLI assembly; the portable `connect`,
`connect/protocol`, and `bridge` packages compile for Wasm without providing a browser transport.
`bridge/agent.Extension` is an independently opt-in `bridge_call` tool; its caller must use the
source attachment's authenticated outbound route, which checks source grants before the
destination checks its own grants. Discovery never authorizes execution.

In Orb, open Bridge directly from Settings, Ctrl+P, or `/bridge`, then use its service switch.
The home page shows the service switch, **Add device**, and your devices. Select a device to
open its conversations; both lists update automatically. **Advanced** contains the optional
agent-call tool and your fingerprint. Groups, grants, scopes, and receipts stay in the CLI.
Opening Bridge before activation creates no profile or network service. `orb --bridge personal --instance work` explicitly attaches a named runtime. The service survives TUI exit; `orb bridge stop` remains effective until Start or
re-enable. `orb bridge view <peer-id> <instance-id>` opens the same focused conversation view
without constructing a local model or requiring provider credentials. Invitations contain a
private transport locator and one-use claim secret; exchange them with the intended device,
verify both displayed PeerIDs, and approve the exact directional grants locally.

To pair two Orbs, choose **Add device → Create an invitation** and copy it. On the other Orb
choose **Add device → Paste an invitation**, paste it, and confirm **Trust this Orb** after checking the
fingerprint. The sharing screen automatically asks its owner to confirm the joining identity.
Both Orbs then have full control of each other's current and future conversations, including
new groups. The joining device opens the shared conversation picker. Either action enables
Bridge when needed. Existing restricted grants remain unchanged and can be managed through the
CLI. Controller trust does not enable agent calls or remote Bridge administration.
Saved pairings survive stopping; **Connected**, **Offline**, and **Blocked** describe current
connection state. Open device panels retry after interruptions and show newly attached conversations
without reopening the picker. Closed conversations disappear automatically.

For a server you already access through SSH, choose **Add device → Connect a server** or run
`orb bridge connect-ssh user@host`. Your system SSH client must already connect without a
password or host-key prompt; SSH aliases and configuration are supported. Setup finds Orb on
PATH or in `~/.local/bin` and installs or updates it there if needed, using a checksum-verified
release for the server's platform. No sudo or remote shell edits are needed. The candidate must
support Bridge before it replaces an existing installation. Until a Bridge-enabled release is
published, development users need a compatible server build. The shortcut starts Bridge and
grants mutual full conversation access using your SSH login, then traffic uses Bridge.
`--remote-profile` and `--remote-orb` select a different remote profile or an existing executable;
`--profile` selects the local profile. Enable Bridge in a remote Orb conversation to attach it;
pairing can happen before any conversations exist.

Scripted administration uses `orb bridge status|instances|peers|grants|groups|scopes`,
`orb bridge pair invite`, `orb bridge pair join` (invitation JSON on stdin),
and `orb bridge pair approve <invitation-id> <claimant-peer-id>`. The low-level pair commands
retain their directional workflow; `orb bridge trust <peer-id>` grants the reverse full controller
access after approval, as the guided flows do automatically. `grant`, `revoke`, `group`,
`assign`, `scope`, `publish`, and `takeover` read their local-owner request JSON from stdin;
`revoke` takes `{ "grant_id": "…" }`. `orb bridge remote <peer-id> <method>` reads bounded
method parameters from stdin. Wire schemas and limits live in `ARCHITECTURE.md`; their tests
are independent of Pi fixtures.
