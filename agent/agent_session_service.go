package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	extensionhost "github.com/OrdalieTech/orb/agent/extensions/host"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

// ExtensionAgentSessionService is the runtime behind the extension host's
// agent_session_v1 capability (and sdk_v1's sdk_resource_reload). It backs
// every SDK createAgentSession call with a real [NewAgentSession] child
// session:
//
//   - Callbacks fire synchronously from inside Prompt (the whole turn runs
//     inline in SessionRuntime), so every event the turn produces is delivered
//     before Prompt's terminal result (events-before-terminal ordering).
//   - Every session event is mirrored through OnEvent, and the messages/stats
//     mirrors are updated live during the turn via the delta callbacks.
//   - customTools that reference host-JS closures execute in the
//     host process through AgentSessionCreateRequest.ExecuteTool; their JSON
//     Schema parameters are validated (and coerced) by the agent loop before
//     execute (D14) and `terminate: true` in a tool result ends the turn.
//   - Provider quota/limit failures land in the mirrored assistant message as
//     stopReason "error" with the provider's verbatim errorMessage; Prompt
//     itself resolves.
//   - Options.ModelRuntime is resolved through the request's
//     ResolveModelRuntime seam and drives streaming, auth, and the available
//     model set of the child session.
//   - Options.Session.SessionInfoNames (appendSessionInfo calls queued before
//     the session existed) are applied right after create.
//   - Dispose aborts a running prompt before releasing the session.
//   - ReloadResources reloads the shared resource loader the next
//     CreateSession for the same cwd/agentDir/noExtensions key observes.
//   - Model resolution and fallback ride the create result
//     (ModelFallbackMessage + Model), never the event stream.
//
// Install it at product startup with Manager.SetAgentSessionService.
type ExtensionAgentSessionService struct {
	options ExtensionAgentSessionServiceOptions

	mu      sync.Mutex
	loaders map[extensionResourceLoaderKey]*DefaultResourceLoader
}

// ExtensionAgentSessionServiceOptions configures the child-session runtime.
type ExtensionAgentSessionServiceOptions struct {
	// CWD is the fallback working directory when a create request carries none.
	CWD string
	// AgentDir is the fallback agent directory (auth.json, models.json,
	// skills). Defaults to DefaultAgentDir().
	AgentDir string
	// StreamFn overrides the streaming backend for every child session (used
	// by tests and embedders with their own provider dispatch). When nil,
	// sessions stream through the resolved model runtime's registry —
	// extension-registered providers included — or the default HTTP
	// dispatcher.
	StreamFn engine.StreamFn
	// Clock supplies Date.now-compatible milliseconds for message, runtime,
	// and persisted-session timestamps of every child session. Defaults to the
	// wall clock; deterministic replay tests inject a fixed clock.
	Clock func() int64
}

var _ extensionhost.AgentSessionService = (*ExtensionAgentSessionService)(nil)

// NewExtensionAgentSessionService creates the NewAgentSession-backed
// implementation of the extension host's AgentSessionService seam.
func NewExtensionAgentSessionService(options ExtensionAgentSessionServiceOptions) *ExtensionAgentSessionService {
	return &ExtensionAgentSessionService{
		options: options,
		loaders: make(map[extensionResourceLoaderKey]*DefaultResourceLoader),
	}
}

type extensionResourceLoaderKey struct {
	cwd          string
	agentDir     string
	noExtensions bool
}

func (service *ExtensionAgentSessionService) resolvePath(value, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	if value == "" {
		value = "."
	}
	normalized, err := config.NormalizePath(value)
	if err != nil {
		return "", err
	}
	return filepath.Abs(normalized)
}

func (service *ExtensionAgentSessionService) defaultAgentDir() string {
	if service.options.AgentDir != "" {
		return service.options.AgentDir
	}
	return DefaultAgentDir()
}

func (service *ExtensionAgentSessionService) loaderKey(request extensionhost.AgentSessionResourceLoader, fallbackCWD string) (extensionResourceLoaderKey, error) {
	cwd, err := service.resolvePath(request.CWD, fallbackCWD)
	if err != nil {
		return extensionResourceLoaderKey{}, err
	}
	agentDir, err := service.resolvePath(request.AgentDir, service.defaultAgentDir())
	if err != nil {
		return extensionResourceLoaderKey{}, err
	}
	return extensionResourceLoaderKey{cwd: cwd, agentDir: agentDir, noExtensions: request.NoExtensions}, nil
}

// loaderFor returns the shared, memoized resource loader for one
// cwd/agentDir/noExtensions key, mirroring the plugin's per-run shared
// DefaultResourceLoader: worktree fan-out sessions reuse one loader (and one
// resource scan) while ReloadResources refreshes it in place.
func (service *ExtensionAgentSessionService) loaderFor(ctx context.Context, key extensionResourceLoaderKey) (*DefaultResourceLoader, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if loader := service.loaders[key]; loader != nil {
		return loader, nil
	}
	settings, err := config.NewSettingsManager(key.cwd, config.WithAgentDir(key.agentDir))
	if err != nil {
		return nil, err
	}
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD:      key.cwd,
		AgentDir: key.agentDir,
		// NoExtensions is the structural anti-recursion guarantee: the child
		// session loads no extensions while skills, prompt templates, and
		// AGENTS.md context still load.
		NoExtensions:    key.noExtensions,
		SettingsManager: settings,
	})
	if err != nil {
		return nil, err
	}
	if err := loader.Reload(ctx, nil); err != nil {
		return nil, err
	}
	service.loaders[key] = loader
	return loader, nil
}

// ReloadResources backs DefaultResourceLoader.reload() over sdk_v1: it
// reloads the real shared resource set (skills, prompts, AGENTS.md context —
// never extensions when NoExtensions) that the next CreateSession for the
// same key observes.
func (service *ExtensionAgentSessionService) ReloadResources(ctx context.Context, request extensionhost.AgentSessionResourceLoader) error {
	key, err := service.loaderKey(request, service.options.CWD)
	if err != nil {
		return err
	}
	loader, err := service.loaderFor(ctx, key)
	if err != nil {
		return err
	}
	return loader.Reload(ctx, nil)
}

// CreateSession maps one agent_session_create request onto NewAgentSession.
func (service *ExtensionAgentSessionService) CreateSession(
	ctx context.Context,
	request extensionhost.AgentSessionCreateRequest,
	callbacks extensionhost.AgentSessionCallbacks,
) (extensionhost.AgentSessionHandle, extensionhost.AgentSessionCreateResult, error) {
	options := request.Options
	cwd, err := service.resolvePath(options.CWD, service.options.CWD)
	if err != nil {
		return nil, extensionhost.AgentSessionCreateResult{}, err
	}
	agentDir, err := service.resolvePath(options.AgentDir, service.defaultAgentDir())
	if err != nil {
		return nil, extensionhost.AgentSessionCreateResult{}, err
	}

	sessionOptions := AgentSessionOptions{
		CWD:      cwd,
		AgentDir: agentDir,
		// Off-catalog synthesized models are accepted verbatim: NewAgentSession
		// routes by the model's own fields and never requires catalog
		// membership.
		Model:         options.Model,
		ThinkingLevel: options.ThinkingLevel,
		NoTools:       options.NoTools,
		ExcludeTools:  append([]string(nil), options.ExcludeTools...),
		StreamFn:      service.options.StreamFn,
		Clock:         service.options.Clock,
	}
	if len(options.ScopedModels) > 0 {
		var scoped []struct {
			Model         ai.Model               `json:"model"`
			ThinkingLevel *ai.ModelThinkingLevel `json:"thinkingLevel"`
		}
		if json.Unmarshal(options.ScopedModels, &scoped) == nil {
			for _, entry := range scoped {
				sessionOptions.ScopedModels = append(sessionOptions.ScopedModels, ScopedModel{
					Model: entry.Model, ThinkingLevel: entry.ThinkingLevel,
				})
			}
		}
	}

	// Tool wiring. Builtin markers (createCodingTools) become Go-native tools
	// bound to CWD; every other entry is a live host-JS closure reached through
	// ExecuteTool. A tool is never omitted or substituted: a callback entry
	// without a name fails the create.
	// sessionRef lets callback tools reach the live session once it exists:
	// the ordered provider-emitted tool-call JSON lives in its message state.
	sessionRef := &atomic.Pointer[AgentSession]{}
	for _, tool := range options.CustomTools {
		if tool.Builtin != "" {
			// SDK coding-tool markers carry upstream createCodingTools' prompt
			// contribution (bash only; the wrapper drops the rest), which
			// replaces Orb's interactive built-in metadata for this session.
			if sessionOptions.BuiltinToolPrompts == nil {
				sessionOptions.BuiltinToolPrompts = make(map[string]ToolPromptContribution)
			}
			sessionOptions.BuiltinToolPrompts[tool.Builtin] = ToolPromptContribution{
				Snippet:    tool.PromptSnippet,
				Guidelines: append([]string(nil), tool.PromptGuidelines...),
			}
			continue
		}
		if tool.Name == "" {
			return nil, extensionhost.AgentSessionCreateResult{}, &extensionhost.ServiceError{
				Code: "invalid_session_tool", Message: "agent_session_create: customTools entry has neither builtin marker nor name",
			}
		}
		sessionOptions.CustomTools = append(sessionOptions.CustomTools, service.callbackToolDefinition(request, tool, sessionRef))
	}
	if options.Tools != nil {
		sessionOptions.Tools = append([]string(nil), options.Tools...)
	}
	// Builtin markers never form an allowlist: upstream createAgentSession
	// always builds its default built-in tools, and same-named customTools
	// merely SHADOW them (registry and prompt contribution) — a caller that
	// filtered its coding-tool markers down to a subset still gets the full
	// default set, with only the shadowed names carrying marker metadata.
	// ExcludeTools remains the only subtraction (applied after Tools).

	// Model runtime resolution (contract point 5): the ref resolves to the
	// live registry view behind it — the extension's own ctx.modelRegistry or
	// a model_runtime_v1 handle — and that registry drives streaming (which
	// includes extension-registered providers), request auth, model headers,
	// and the available-model set.
	if request.ResolveModelRuntime != nil {
		registry, err := request.ResolveModelRuntime()
		if err != nil {
			return nil, extensionhost.AgentSessionCreateResult{}, err
		}
		if registry != nil {
			if concrete, ok := registry.(*config.ModelRegistry); ok {
				sessionOptions.ModelRegistry = concrete
			}
			sessionOptions.AvailableModels = func() []ai.Model { return registry.Available(nil) }
			if sessionOptions.StreamFn == nil {
				sessionOptions.StreamFn = registry.StreamSimple
				getRequestAuth := func(ctx context.Context, provider ai.ProviderID) (*engine.RequestAuth, error) {
					resolved, err := registry.ResolveProviderAuth(ctx, string(provider), nil)
					if err != nil || resolved == nil {
						return nil, err
					}
					return &engine.RequestAuth{
						APIKey:  resolved.Auth.APIKey,
						Headers: resolved.Auth.Headers,
						Env:     ai.ProviderEnv(resolved.Env),
						BaseURL: resolved.Auth.BaseURL,
					}, nil
				}
				sessionOptions.GetRequestAuth = getRequestAuth
				sessionOptions.GetAPIKey = func(ctx context.Context, provider ai.ProviderID) (*string, error) {
					resolved, err := getRequestAuth(ctx, provider)
					if err != nil || resolved == nil {
						return nil, err
					}
					return resolved.APIKey, nil
				}
				sessionOptions.GetModelHeaders = func(ctx context.Context, model *ai.Model, apiKey *string, env ai.ProviderEnv) (*map[string]string, error) {
					if model == nil {
						return nil, nil
					}
					return registry.ResolveModelHeaders(ctx, *model, map[string]string(env), apiKey)
				}
			}
		}
	}

	// Session storage: the SessionManager thin handle. Persisted sessions use
	// the directory the SDK already created and write-probed.
	if storage := options.Session; storage != nil {
		sessionCWD, err := service.resolvePath(storage.CWD, cwd)
		if err != nil {
			return nil, extensionhost.AgentSessionCreateResult{}, err
		}
		var storeOptions []sessionstore.Option
		if clock := service.options.Clock; clock != nil {
			storeOptions = append(storeOptions, sessionstore.WithClock(func() time.Time {
				return time.UnixMilli(clock()).UTC()
			}))
		}
		var manager *sessionstore.SessionManager
		if storage.Persisted {
			manager, err = sessionstore.Create(sessionCWD, storage.SessionDir, storeOptions...)
		} else {
			manager, err = sessionstore.InMemory(sessionCWD, storeOptions...)
		}
		if err != nil {
			return nil, extensionhost.AgentSessionCreateResult{}, err
		}
		sessionOptions.SessionManager = manager
	}

	// Settings: the inert SettingsManager handle carries only roots; the real
	// settings (default model, thinking level, blockImages, retries) are
	// applied here by constructing the actual manager for those roots.
	if settings := options.Settings; settings != nil {
		settingsCWD, err := service.resolvePath(settings.CWD, cwd)
		if err != nil {
			return nil, extensionhost.AgentSessionCreateResult{}, err
		}
		settingsAgentDir, err := service.resolvePath(settings.AgentDir, agentDir)
		if err != nil {
			return nil, extensionhost.AgentSessionCreateResult{}, err
		}
		manager, err := config.NewSettingsManager(settingsCWD, config.WithAgentDir(settingsAgentDir))
		if err != nil {
			return nil, extensionhost.AgentSessionCreateResult{}, err
		}
		sessionOptions.Settings = manager
	}

	// Resource loading: reuse the shared memoized loader (the plugin memoizes
	// one DefaultResourceLoader per run and passes it to every child). When no
	// loader crossed the wire, default to a NoExtensions loader — child
	// sessions never load extensions recursively either way, because the
	// session is always handed a fresh registry below.
	loaderRequest := extensionhost.AgentSessionResourceLoader{NoExtensions: true}
	if options.ResourceLoader != nil {
		loaderRequest = *options.ResourceLoader
	}
	loaderKey, err := service.loaderKey(loaderRequest, cwd)
	if err != nil {
		return nil, extensionhost.AgentSessionCreateResult{}, err
	}
	loader, err := service.loaderFor(ctx, loaderKey)
	if err != nil {
		return nil, extensionhost.AgentSessionCreateResult{}, err
	}
	sessionOptions.ResourceLoader = loader
	// A per-session registry keeps each child's customTools closures isolated
	// (concurrent fan-out sessions register same-named tools bound to
	// different handles) and structurally guarantees no extension — native or
	// JS — rides into the child through the shared loader.
	sessionOptions.ExtensionRegistry = extensions.NewRegistry(cwd)

	if len(options.SessionStartEvent) > 0 {
		var startEvent struct {
			Reason              string  `json:"reason"`
			PreviousSessionFile *string `json:"previousSessionFile"`
		}
		if json.Unmarshal(options.SessionStartEvent, &startEvent) == nil && startEvent.Reason != "" {
			sessionOptions.SessionStartEvent = &extensions.SessionStartEvent{
				Reason:              extensions.SessionStartReason(startEvent.Reason),
				PreviousSessionFile: startEvent.PreviousSessionFile,
			}
		}
	}

	result, err := NewAgentSession(sessionOptions)
	if err != nil {
		return nil, extensionhost.AgentSessionCreateResult{}, err
	}
	session := result.Session
	sessionRef.Store(session)

	// Pre-create appendSessionInfo names (contract point 6), best-effort like
	// upstream's try/catch around appendSessionInfo.
	if options.Session != nil {
		for _, name := range options.Session.SessionInfoNames {
			_, _ = session.Manager().AppendSessionInfo(name)
		}
	}

	handle := &extensionAgentSessionHandle{
		session:   session,
		result:    result,
		callbacks: callbacks,
	}
	state := session.State()
	handle.mirrorCount = len(state.Messages)
	if handle.mirrorCount > 0 {
		if raw, err := ai.Marshal(state.Messages); err == nil {
			callbacks.OnMessagesSnapshot(json.RawMessage(raw))
		}
	}
	// Subscribe before returning so every event of every later operation is
	// mirrored; emissions happen inline in the emitting goroutine, which is
	// what delivers turn events before Prompt's terminal result.
	handle.unsubscribe = session.Subscribe(handle.mirror)

	createResult := extensionhost.AgentSessionCreateResult{ModelFallbackMessage: result.ModelFallbackMessage}
	if state.Model != nil {
		model := *state.Model
		createResult.Model = &model
	}
	return handle, createResult, nil
}

// callbackToolDefinition wires one host-JS tool into the child session. The
// agent loop schema-validates (and coerces) params against Parameters before
// Execute runs (D14), so the JS closure receives exactly the schema-parsed
// value; prepareArguments already runs host-side inside execute_session_tool;
// `terminate: true` in the decoded result ends the turn via the loop.
func (service *ExtensionAgentSessionService) callbackToolDefinition(
	request extensionhost.AgentSessionCreateRequest,
	tool extensionhost.AgentSessionTool,
	sessionRef *atomic.Pointer[AgentSession],
) extensions.ToolDefinition {
	parameters := ai.JSONSchema(tool.Parameters)
	if len(parameters) == 0 {
		parameters = ai.JSONSchema(`{"type":"object"}`)
	}
	execute := request.ExecuteTool
	name := tool.Name
	return extensions.ToolDefinition{
		Name:             name,
		Label:            tool.Label,
		Description:      tool.Description,
		PromptSnippet:    tool.PromptSnippet,
		PromptGuidelines: append([]string(nil), tool.PromptGuidelines...),
		Parameters:       parameters,
		ExecutionMode:    tool.ExecutionMode,
		Execute: func(
			ctx context.Context,
			toolCallID string,
			params any,
			onUpdate engine.AgentToolUpdateCallback,
			_ extensions.Context,
		) (engine.AgentToolResult, error) {
			if execute == nil {
				return engine.AgentToolResult{}, fmt.Errorf("agent: session tool %s has no host transport", name)
			}
			var update func(json.RawMessage)
			if onUpdate != nil {
				update = func(raw json.RawMessage) {
					var partial engine.AgentToolResult
					if json.Unmarshal(raw, &partial) == nil {
						onUpdate(partial)
					}
				}
			}
			raw, err := execute(ctx, name, toolCallID, orderedSessionToolParams(sessionRef.Load(), toolCallID, params), update)
			if err != nil {
				return engine.AgentToolResult{}, err
			}
			var result engine.AgentToolResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return engine.AgentToolResult{}, fmt.Errorf("agent: decode session tool %s result: %w", name, err)
			}
			return result, nil
		},
	}
}

// orderedSessionToolParams returns the wire representation of a session
// tool's validated params, preserving the provider-emitted object member
// order when the validated value is structurally identical to the emitted
// arguments (upstream passes the parsed JS object straight through, so member
// order survives into the extension's tool). Coerced or transformed values
// fall back to canonical marshaling.
func orderedSessionToolParams(session *AgentSession, toolCallID string, params any) any {
	if session == nil || toolCallID == "" {
		return params
	}
	encoded, err := ai.Marshal(params)
	if err != nil {
		return params
	}
	state := session.State()
	messages := state.Messages
	if state.StreamingMessage != nil {
		messages = append(append(engine.AgentMessages(nil), messages...), state.StreamingMessage)
	}
	for index := len(messages) - 1; index >= 0; index-- {
		assistant, ok := messages[index].(*ai.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range assistant.Content {
			toolCall, ok := block.(*ai.ToolCall)
			if !ok || toolCall.ID != toolCallID {
				continue
			}
			raw, err := ai.MarshalToolCallArguments(toolCall)
			if err != nil {
				return params
			}
			var rawValue, paramsValue any
			if json.Unmarshal(raw, &rawValue) != nil || json.Unmarshal(encoded, &paramsValue) != nil {
				return params
			}
			if reflect.DeepEqual(rawValue, paramsValue) {
				return json.RawMessage(raw)
			}
			return params
		}
	}
	return params
}

// extensionAgentSessionHandle is one live child session behind a Go-minted
// protocol handle.
type extensionAgentSessionHandle struct {
	session   *AgentSession
	result    *AgentSessionResult
	callbacks extensionhost.AgentSessionCallbacks

	mu          sync.Mutex
	mirrorCount int
	disposed    bool
	unsubscribe func()
	disposeOnce sync.Once
}

var _ extensionhost.AgentSessionHandle = (*extensionAgentSessionHandle)(nil)

// mirror streams every session event into the SDK's local mirrors. Message
// deltas are emitted before the raw event so a subscribe listener observes an
// already-updated messages mirror, matching upstream subscriber semantics.
func (handle *extensionAgentSessionHandle) mirror(event any) {
	handle.mu.Lock()
	if handle.disposed {
		handle.mu.Unlock()
		return
	}
	switch typed := event.(type) {
	case engine.MessageStartEvent:
		handle.mirrorCount++
		handle.mu.Unlock()
		if raw, err := ai.Marshal(typed.Message); err == nil {
			handle.callbacks.OnMessageAppended(json.RawMessage(raw))
		}
	case engine.MessageUpdateEvent:
		index := handle.mirrorCount - 1
		handle.mu.Unlock()
		if index >= 0 {
			if raw, err := ai.Marshal(typed.Message); err == nil {
				handle.callbacks.OnMessageUpdated(index, json.RawMessage(raw))
			}
		}
	case engine.MessageEndEvent:
		index := handle.mirrorCount - 1
		handle.mu.Unlock()
		if index >= 0 {
			if raw, err := ai.Marshal(typed.Message); err == nil {
				handle.callbacks.OnMessageUpdated(index, json.RawMessage(raw))
			}
		}
		// Keep the stats mirror live during the turn, not only at settle.
		handle.callbacks.OnStats(handle.statsValue())
	case CompactionEndEvent:
		// Compaction rewrites history wholesale; resync the mirror.
		state := handle.session.State()
		handle.mirrorCount = len(state.Messages)
		handle.mu.Unlock()
		if raw, err := ai.Marshal(state.Messages); err == nil {
			handle.callbacks.OnMessagesSnapshot(json.RawMessage(raw))
		}
	default:
		handle.mu.Unlock()
	}
	if raw, err := MarshalSessionEvent(event); err == nil {
		handle.callbacks.OnEvent(json.RawMessage(raw))
	}
}

func (handle *extensionAgentSessionHandle) statsValue() extensionhost.AgentSessionStats {
	stats := handle.session.GetSessionStats()
	return extensionhost.AgentSessionStats{
		Tokens: extensionhost.AgentSessionTokens{
			Input:      stats.Tokens.Input,
			Output:     stats.Tokens.Output,
			CacheRead:  stats.Tokens.CacheRead,
			CacheWrite: stats.Tokens.CacheWrite,
			Total:      stats.Tokens.Total,
		},
		Cost: stats.Cost,
	}
}

func (handle *extensionAgentSessionHandle) checkLive() error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.disposed {
		return &extensionhost.ServiceError{Code: "session_disposed", Message: "agent session is disposed"}
	}
	return nil
}

// Prompt resolves when the turn settles. The turn runs inline (retries and
// auto-compaction included), so every callback it produced has already been
// delivered when this returns (contract point 1). Provider limit failures are
// not errors: they land in the message mirror as a final assistant message
// with stopReason "error" and the verbatim errorMessage (contract point 4). A
// service_cancel on the request context aborts the turn and surfaces as the
// structured "cancelled" rejection while the aborted turn stays mirrored.
func (handle *extensionAgentSessionHandle) Prompt(ctx context.Context, text string, rawOptions json.RawMessage) error {
	if err := handle.checkLive(); err != nil {
		return err
	}
	// Decoded prompt-option surface: expandPromptTemplates is the only
	// upstream PromptOptions field SDK extensions pass over this wire (the
	// AbortSignal never crosses — it mirrors as service_cancel — and
	// images/source/streamingBehavior have no createAgentSession callers).
	// Unknown fields are ignored, matching the protocol's additive rule;
	// widen this decode when a consumer appears.
	promptOptions := &PromptOptions{}
	if len(rawOptions) > 0 {
		var decoded struct {
			ExpandPromptTemplates *bool `json:"expandPromptTemplates"`
		}
		if json.Unmarshal(rawOptions, &decoded) == nil {
			promptOptions.ExpandPromptTemplates = decoded.ExpandPromptTemplates
		}
	}
	err := handle.session.PromptWithOptions(ctx, text, promptOptions)
	// Final stats flush before the terminal result so getSessionStats() is
	// exact at prompt resolution.
	handle.mu.Lock()
	disposed := handle.disposed
	handle.mu.Unlock()
	if !disposed {
		handle.callbacks.OnStats(handle.statsValue())
	}
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Messages returns the upstream-shaped message mirror, including the
// in-flight streaming assistant message when one exists.
func (handle *extensionAgentSessionHandle) Messages(context.Context) (json.RawMessage, error) {
	if err := handle.checkLive(); err != nil {
		return nil, err
	}
	state := handle.session.State()
	messages := state.Messages
	if state.StreamingMessage != nil {
		messages = append(append(engine.AgentMessages(nil), messages...), state.StreamingMessage)
	}
	if len(messages) == 0 {
		return json.RawMessage("[]"), nil
	}
	raw, err := ai.Marshal(messages)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Abort mirrors upstream session.abort(): fire-and-forget cancellation of the
// active turn; the running Prompt settles with the aborted message mirrored.
func (handle *extensionAgentSessionHandle) Abort(context.Context) error {
	if err := handle.checkLive(); err != nil {
		return err
	}
	handle.session.Abort()
	return nil
}

func (handle *extensionAgentSessionHandle) SessionStats(context.Context) (extensionhost.AgentSessionStats, error) {
	if err := handle.checkLive(); err != nil {
		return extensionhost.AgentSessionStats{}, err
	}
	return handle.statsValue(), nil
}

func (handle *extensionAgentSessionHandle) SetActiveToolsByName(_ context.Context, names []string) error {
	if err := handle.checkLive(); err != nil {
		return err
	}
	return handle.session.SetActiveToolsByName(names)
}

// AppendSessionInfo is best-effort session naming forwarded post-create.
func (handle *extensionAgentSessionHandle) AppendSessionInfo(_ context.Context, name string) error {
	if err := handle.checkLive(); err != nil {
		return err
	}
	_, err := handle.session.Manager().AppendSessionInfo(name)
	return err
}

// Dispose aborts any running prompt (contract point 7 — reset() bounds ctx at
// 2s), stops mirroring, and releases the session. Idempotent.
func (handle *extensionAgentSessionHandle) Dispose(ctx context.Context) error {
	handle.disposeOnce.Do(func() {
		handle.mu.Lock()
		handle.disposed = true
		unsubscribe := handle.unsubscribe
		handle.unsubscribe = nil
		handle.mu.Unlock()
		if unsubscribe != nil {
			unsubscribe()
		}
		handle.session.Abort()
		if ctx == nil {
			ctx = context.Background()
		}
		_ = handle.session.WaitForIdle(ctx)
		handle.session.Dispose()
	})
	return nil
}
