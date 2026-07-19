package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/modes"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

// sessionRuntimeOptions selects the mode-specific parts of the otherwise
// shared session runtime wiring.
type sessionRuntimeOptions struct {
	mode              extensions.Mode
	errorWriter       io.Writer
	sessionStart      *extensions.SessionStartEvent
	deferSessionStart bool
}

// sessionRuntimeConfig wires runtimeInputs into the full SessionRuntime
// configuration. Startup and every session replacement go through it so a
// replacement keeps auth, model headers, extensions, tools, and prompt state.
func sessionRuntimeConfig(inputs runtimeInputs, manager *session.SessionManager, options sessionRuntimeOptions) (codingagent.SessionRuntimeConfig, error) {
	settings := inputs.Settings
	if settings == nil {
		agentDir, err := config.GetAgentDir()
		if err != nil {
			return codingagent.SessionRuntimeConfig{}, err
		}
		settings, err = config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(agentDir))
		if err != nil {
			return codingagent.SessionRuntimeConfig{}, err
		}
	}
	var errorHandler func(extensions.ExtensionError)
	if options.errorWriter != nil {
		writer := options.errorWriter
		errorHandler = func(extensionError extensions.ExtensionError) {
			_, _ = fmt.Fprintf(writer, "Extension error (%s, %s): %s\n", extensionError.ExtensionPath, extensionError.Event, extensionError.Error)
		}
	}
	runtimeConfig := codingagent.SessionRuntimeConfig{
		Agent: inputs.Agent, SessionManager: manager, Settings: settings, StreamFn: inputs.StreamFn,
		GetAPIKey: inputs.GetAPIKey, GetRequestAuth: inputs.GetRequestAuth, GetModelHeaders: inputs.GetModelHeaders,
		AvailableModels:       inputs.AvailableModels,
		ScopedModels:          inputs.ScopedModels,
		SlashResolver:         inputs.SlashResolver,
		ExtensionRegistry:     inputs.Extensions,
		ExtensionMode:         options.mode,
		ExtensionErrorHandler: errorHandler,
		BaseTools:             inputs.BaseTools, InitialActiveToolNames: inputs.ActiveToolNames,
		AllowedToolNames: inputs.AllowedTools, ExcludedToolNames: inputs.ExcludedTools,
		SystemPromptOptions: &inputs.PromptOptions,
		ResourceLoader:      inputs.ResourceLoader,
		SessionStart:        options.sessionStart,
		DeferSessionStart:   options.deferSessionStart,
	}
	if inputs.ModelRegistry != nil {
		runtimeConfig.ModelRegistry = inputs.ModelRegistry
	}
	return runtimeConfig, nil
}

func buildSessionRuntime(inputs runtimeInputs, manager *session.SessionManager, options sessionRuntimeOptions) (*codingagent.SessionRuntime, error) {
	runtimeConfig, err := sessionRuntimeConfig(inputs, manager, options)
	if err != nil {
		return nil, err
	}
	// Providers key affinity and prompt caches on the session id; upstream
	// createAgentSession passes sessionId into the Agent at construction.
	inputs.Agent.SetStreamSessionID(manager.GetSessionID())
	return codingagent.NewSessionRuntime(runtimeConfig)
}

// createReplacementRuntime rebuilds the complete runtime for manager from the
// immutable startup args. AgentSessionRuntime tears down the old runtime before
// calling this factory, so failures propagate with no rollback.
func createReplacementRuntime(
	dependencies cliDependencies,
	args CLIArgs,
	manager *session.SessionManager,
	options sessionRuntimeOptions,
) (*codingagent.SessionRuntime, runtimeInputs, error) {
	contextState := manager.BuildSessionContext()
	if len(manager.GetEntries()) > 0 {
		applySessionDefaults(&args, contextState, manager.GetBranch())
	}
	inputs, err := dependencies.createRuntime(manager.GetCWD(), args, decodeSessionMessages(contextState.Messages))
	if err != nil {
		return nil, runtimeInputs{}, err
	}
	if err := appendInitialRuntimeState(manager, inputs.Agent.State(), contextState); err != nil {
		return nil, runtimeInputs{}, err
	}
	runtime, err := buildSessionRuntime(inputs, manager, options)
	if err != nil {
		return nil, runtimeInputs{}, err
	}
	return runtime, inputs, nil
}

// newSessionReplacementManager builds the manager for /new (upstream
// AgentSessionRuntime.newSession).
func newSessionReplacementManager(manager *session.SessionManager, parentSession string) (*session.SessionManager, error) {
	var replacement *session.SessionManager
	var err error
	if manager.IsPersisted() {
		replacement, err = session.Create(manager.GetCWD(), manager.GetSessionDir())
	} else {
		replacement, err = session.InMemory(manager.GetCWD())
	}
	if err != nil {
		return nil, err
	}
	if parentSession != "" {
		parent := parentSession
		if _, err := replacement.NewSession(session.NewSessionOptions{ParentSession: &parent}); err != nil {
			return nil, err
		}
	}
	return replacement, nil
}

// forkReplacementManager builds the manager for fork/clone and returns the
// forked-from user text for position "before" (upstream
// AgentSessionRuntime.fork).
//
//nolint:staticcheck // Error text matches upstream capitalization.
func forkReplacementManager(manager *session.SessionManager, entryID string, position extensions.ForkPosition) (*session.SessionManager, string, error) {
	selected := manager.GetEntry(entryID)
	if selected == nil {
		return nil, "", errors.New("Invalid entry ID for forking")
	}
	targetID := selected.ID
	selectedText := ""
	if position != extensions.ForkAt {
		role, text := rpcMessageRoleAndText(selected.Message)
		if selected.Type != "message" || role != "user" {
			return nil, "", errors.New("Invalid entry ID for forking")
		}
		selectedText = text
		if selected.ParentID == nil {
			targetID = ""
		} else {
			targetID = *selected.ParentID
		}
	}

	var replacement *session.SessionManager
	var err error
	if manager.IsPersisted() {
		currentFile := manager.GetSessionFile()
		if currentFile == "" {
			return nil, "", errors.New("Persisted session is missing a session file")
		}
		if targetID == "" {
			replacement, err = session.Create(manager.GetCWD(), manager.GetSessionDir())
			if err == nil {
				parent := currentFile
				_, err = replacement.NewSession(session.NewSessionOptions{ParentSession: &parent})
			}
		} else {
			if _, statErr := os.Stat(currentFile); statErr != nil {
				return nil, "", errors.New("This session has not been saved yet. Wait for the first assistant response before cloning or forking it.")
			}
			replacement, err = session.Open(currentFile, manager.GetSessionDir())
			if err == nil {
				_, err = replacement.CreateBranchedSession(targetID)
			}
		}
	} else {
		// In-memory forks mutate the live manager before the replacement
		// runtime exists, matching upstream's observable failure behavior.
		replacement = manager
		if targetID == "" {
			options := session.NewSessionOptions{}
			if currentFile := manager.GetSessionFile(); currentFile != "" {
				options.ParentSession = &currentFile
			}
			_, err = replacement.NewSession(options)
		} else {
			_, err = replacement.CreateBranchedSession(targetID)
		}
	}
	if err != nil {
		return nil, "", err
	}
	return replacement, selectedText, nil
}

// interactiveSessionHost owns the interactive TUI's SessionRuntime and its
// replacement lifecycle (upstream AgentSessionRuntime). Replacement order:
// session_before_switch/fork on the current runner, session_shutdown and
// synchronous UI invalidation, dispose the old runtime, create and apply the
// replacement, then rebind it before the deferred session_start.
type interactiveSessionHost struct {
	mu                sync.Mutex
	args              CLIArgs
	dependencies      cliDependencies
	agentDir          string
	errorWriter       io.Writer
	session           *codingagent.SessionRuntime
	inputs            runtimeInputs
	rebind            func(*codingagent.SessionRuntime) error
	beforeInvalidate  func()
	afterSessionStart func(*codingagent.SessionRuntime) error
	replacing         bool
	disposed          bool
}

func newInteractiveSessionHost(
	args CLIArgs,
	dependencies cliDependencies,
	runtime *codingagent.SessionRuntime,
	inputs runtimeInputs,
	agentDir string,
	errorWriter io.Writer,
) *interactiveSessionHost {
	host := &interactiveSessionHost{
		args: args, dependencies: dependencies, agentDir: agentDir,
		errorWriter: errorWriter, session: runtime, inputs: inputs,
	}
	host.bindCommandActions(runtime)
	return host
}

var _ modes.InteractiveSessionHost = (*interactiveSessionHost)(nil)

func (host *interactiveSessionHost) Session() *codingagent.SessionRuntime {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.session
}

func (host *interactiveSessionHost) SetRebindSession(rebind func(*codingagent.SessionRuntime) error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.rebind = rebind
}

func (host *interactiveSessionHost) SetBeforeSessionInvalidate(beforeInvalidate func()) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.beforeInvalidate = beforeInvalidate
}

func (host *interactiveSessionHost) SetAfterSessionStart(afterSessionStart func(*codingagent.SessionRuntime) error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.afterSessionStart = afterSessionStart
}

// bindCommandActions makes extension command-context session operations run
// through the host replacement path.
func (host *interactiveSessionHost) bindCommandActions(runtime *codingagent.SessionRuntime) {
	runtime.BindHostCommandActions(extensions.CommandActions{
		NewSession: host.NewSession,
		Fork: func(ctx context.Context, entryID string, options *extensions.ForkOptions) (extensions.SessionReplacementResult, error) {
			result, err := host.Fork(ctx, entryID, options)
			return extensions.SessionReplacementResult{Cancelled: result.Cancelled}, err
		},
		SwitchSession: func(ctx context.Context, sessionPath string, options *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error) {
			return host.SwitchSession(ctx, sessionPath, "", options)
		},
		Reload: func(ctx context.Context) error { return host.Reload(ctx) },
	})
}

func (host *interactiveSessionHost) beginReplacement() (*codingagent.SessionRuntime, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.disposed || host.session == nil {
		return nil, errors.New("interactive session host is disposed")
	}
	if host.replacing {
		return nil, errors.New("interactive session replacement is already in progress")
	}
	host.replacing = true
	return host.session, nil
}

func (host *interactiveSessionHost) endReplacement() {
	host.mu.Lock()
	host.replacing = false
	host.mu.Unlock()
}

func (host *interactiveSessionHost) currentSession() (*codingagent.SessionRuntime, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.disposed || host.session == nil {
		return nil, errors.New("interactive session host is disposed")
	}
	return host.session, nil
}

// emitSessionBeforeSwitch reports whether an extension cancelled the switch.
func emitSessionBeforeSwitch(ctx context.Context, runtime *codingagent.SessionRuntime, reason extensions.SessionSwitchReason, targetSessionFile *string) bool {
	runner := runtime.ExtensionRunner()
	if runner == nil || !runner.HasHandlers(extensions.EventSessionBeforeSwitch) {
		return false
	}
	raw := runner.Emit(ctx, extensions.SessionBeforeSwitchEvent{Reason: reason, TargetSessionFile: targetSessionFile})
	switch value := raw.(type) {
	case extensions.SessionBeforeSwitchResult:
		return value.Cancel
	case *extensions.SessionBeforeSwitchResult:
		return value != nil && value.Cancel
	}
	return false
}

// emitSessionBeforeFork reports whether an extension cancelled the fork.
func emitSessionBeforeFork(ctx context.Context, runtime *codingagent.SessionRuntime, entryID string, position extensions.ForkPosition) bool {
	runner := runtime.ExtensionRunner()
	if runner == nil || !runner.HasHandlers(extensions.EventSessionBeforeFork) {
		return false
	}
	raw := runner.Emit(ctx, extensions.SessionBeforeForkEvent{EntryID: entryID, Position: position})
	switch value := raw.(type) {
	case extensions.SessionBeforeForkResult:
		return value.Cancel
	case *extensions.SessionBeforeForkResult:
		return value != nil && value.Cancel
	}
	return false
}

// replace mirrors AgentSessionRuntime's teardown-first replacement. Factory,
// setup, and rebind failures propagate without restoring the disposed runtime.
func (host *interactiveSessionHost) replace(
	current *codingagent.SessionRuntime,
	reason extensions.SessionShutdownReason,
	manager *session.SessionManager,
	setup func(*session.SessionManager) error,
) (*codingagent.SessionRuntime, error) {
	var previousSessionFile *string
	// Upstream reload emits session_start without previousSessionFile.
	if file := current.Manager().GetSessionFile(); file != "" && reason != extensions.SessionShutdownReload {
		previousSessionFile = stringValue(file)
	}
	var targetSessionFile *string
	// AgentSession.reload has no replacement target even though this Go host
	// rebuilds the runtime through the shared replacement path.
	if file := manager.GetSessionFile(); file != "" && reason != extensions.SessionShutdownReload {
		targetSessionFile = stringValue(file)
	}
	host.mu.Lock()
	beforeInvalidate := host.beforeInvalidate
	host.mu.Unlock()
	current.ShutdownExtensions(reason, targetSessionFile)
	if beforeInvalidate != nil {
		beforeInvalidate()
	}
	current.Dispose()

	replacement, inputs, err := createReplacementRuntime(host.dependencies, host.args, manager, sessionRuntimeOptions{
		mode:              extensions.ModeTUI,
		errorWriter:       host.errorWriter,
		deferSessionStart: true,
		sessionStart: &extensions.SessionStartEvent{
			Reason:              extensions.SessionStartReason(reason),
			PreviousSessionFile: previousSessionFile,
		},
	})
	if err != nil {
		return nil, err
	}
	host.mu.Lock()
	if host.disposed {
		host.mu.Unlock()
		replacement.Dispose()
		return nil, errors.New("interactive session host is disposed")
	}
	host.session, host.inputs = replacement, inputs
	host.mu.Unlock()
	host.bindCommandActions(replacement)
	if setup != nil {
		if err := setup(replacement.Manager()); err != nil {
			return nil, err
		}
		replacement.SyncMessagesFromSession()
	}
	host.mu.Lock()
	rebind := host.rebind
	host.mu.Unlock()
	if rebind != nil {
		if err := rebind(replacement); err != nil {
			return nil, err
		}
	}
	return replacement, nil
}

// finishReplacement fires deferred session_start only after the host has
// committed and released its replacement guard, so extension handlers and
// withSession callbacks can safely call back into the host.
func (host *interactiveSessionHost) finishReplacement(ctx context.Context, replacement *codingagent.SessionRuntime, withSession func(context.Context, extensions.ReplacedSessionContext) error) error {
	replacement.StartExtensions()
	host.mu.Lock()
	afterSessionStart := host.afterSessionStart
	host.mu.Unlock()
	if afterSessionStart != nil {
		if err := afterSessionStart(replacement); err != nil {
			return err
		}
	}
	if withSession != nil {
		if runner := replacement.ExtensionRunner(); runner != nil {
			return withSession(ctx, runner.CreateReplacedSessionContext())
		}
	}
	return nil
}

func (host *interactiveSessionHost) NewSession(ctx context.Context, options *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error) {
	replacement, cancelled, err := func() (*codingagent.SessionRuntime, bool, error) {
		current, err := host.beginReplacement()
		if err != nil {
			return nil, false, err
		}
		defer host.endReplacement()
		if emitSessionBeforeSwitch(ctx, current, extensions.SessionSwitchNew, nil) {
			return nil, true, nil
		}
		parentSession := ""
		var setup func(*session.SessionManager) error
		if options != nil {
			parentSession, setup = options.ParentSession, options.Setup
		}
		manager, err := newSessionReplacementManager(current.Manager(), parentSession)
		if err != nil {
			return nil, false, err
		}
		replacement, err := host.replace(current, extensions.SessionShutdownNew, manager, setup)
		return replacement, false, err
	}()
	if err != nil || cancelled {
		return extensions.SessionReplacementResult{Cancelled: cancelled}, err
	}
	var withSession func(context.Context, extensions.ReplacedSessionContext) error
	if options != nil {
		withSession = options.WithSession
	}
	return extensions.SessionReplacementResult{}, host.finishReplacement(ctx, replacement, withSession)
}

func (host *interactiveSessionHost) SwitchSession(ctx context.Context, sessionPath, cwdOverride string, options *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error) {
	replacement, cancelled, err := func() (*codingagent.SessionRuntime, bool, error) {
		current, err := host.beginReplacement()
		if err != nil {
			return nil, false, err
		}
		defer host.endReplacement()
		if emitSessionBeforeSwitch(ctx, current, extensions.SessionSwitchResume, stringValue(sessionPath)) {
			return nil, true, nil
		}
		var openOptions []session.Option
		if cwdOverride != "" {
			openOptions = append(openOptions, session.WithCwdOverride(cwdOverride))
		}
		manager, err := session.Open(sessionPath, "", openOptions...)
		if err != nil {
			return nil, false, err
		}
		if err := assertSessionCwdExists(manager, current.Manager().GetCWD()); err != nil {
			return nil, false, err
		}
		replacement, err := host.replace(current, extensions.SessionShutdownResume, manager, nil)
		return replacement, false, err
	}()
	if err != nil || cancelled {
		return extensions.SessionReplacementResult{Cancelled: cancelled}, err
	}
	var withSession func(context.Context, extensions.ReplacedSessionContext) error
	if options != nil {
		withSession = options.WithSession
	}
	return extensions.SessionReplacementResult{}, host.finishReplacement(ctx, replacement, withSession)
}

func (host *interactiveSessionHost) Fork(ctx context.Context, entryID string, options *extensions.ForkOptions) (modes.InteractiveForkResult, error) {
	position := extensions.ForkBefore
	if options != nil && options.Position != "" {
		position = options.Position
	}
	replacement, selectedText, cancelled, err := func() (*codingagent.SessionRuntime, string, bool, error) {
		current, err := host.beginReplacement()
		if err != nil {
			return nil, "", false, err
		}
		defer host.endReplacement()
		if emitSessionBeforeFork(ctx, current, entryID, position) {
			return nil, "", true, nil
		}
		manager, selectedText, err := forkReplacementManager(current.Manager(), entryID, position)
		if err != nil {
			return nil, "", false, err
		}
		replacement, err := host.replace(current, extensions.SessionShutdownFork, manager, nil)
		return replacement, selectedText, false, err
	}()
	if err != nil || cancelled {
		return modes.InteractiveForkResult{Cancelled: cancelled}, err
	}
	var withSession func(context.Context, extensions.ReplacedSessionContext) error
	if options != nil {
		withSession = options.WithSession
	}
	return modes.InteractiveForkResult{SelectedText: selectedText}, host.finishReplacement(ctx, replacement, withSession)
}

func (host *interactiveSessionHost) ImportSession(ctx context.Context, inputPath, cwdOverride string) (extensions.SessionReplacementResult, error) {
	runtime, cancelled, err := func() (*codingagent.SessionRuntime, bool, error) {
		current, err := host.beginReplacement()
		if err != nil {
			return nil, false, err
		}
		defer host.endReplacement()
		resolvedPath, err := resolveImportPath(inputPath)
		if err != nil {
			return nil, false, err
		}
		if _, err := os.Stat(resolvedPath); err != nil {
			return nil, false, &modes.SessionImportFileNotFoundError{FilePath: resolvedPath}
		}
		manager := current.Manager()
		sessionDir := manager.GetSessionDir()
		if sessionDir == "" {
			sessionDir, err = session.DefaultSessionDir(manager.GetCWD(), host.agentDir)
			if err != nil {
				return nil, false, err
			}
		}
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			return nil, false, err
		}
		destinationPath := filepath.Join(sessionDir, filepath.Base(resolvedPath))
		if emitSessionBeforeSwitch(ctx, current, extensions.SessionSwitchResume, stringValue(destinationPath)) {
			return nil, true, nil
		}
		if absDestination, absErr := filepath.Abs(destinationPath); absErr != nil || absDestination != resolvedPath {
			content, readErr := os.ReadFile(resolvedPath)
			if readErr != nil {
				return nil, false, readErr
			}
			if writeErr := os.WriteFile(destinationPath, content, 0o644); writeErr != nil {
				return nil, false, writeErr
			}
		}
		var openOptions []session.Option
		if cwdOverride != "" {
			openOptions = append(openOptions, session.WithCwdOverride(cwdOverride))
		}
		replacementManager, err := session.Open(destinationPath, sessionDir, openOptions...)
		if err != nil {
			return nil, false, err
		}
		if err := assertSessionCwdExists(replacementManager, manager.GetCWD()); err != nil {
			return nil, false, err
		}
		replacement, err := host.replace(current, extensions.SessionShutdownResume, replacementManager, nil)
		return replacement, false, err
	}()
	if err != nil || cancelled {
		return extensions.SessionReplacementResult{Cancelled: cancelled}, err
	}
	return extensions.SessionReplacementResult{}, host.finishReplacement(ctx, runtime, nil)
}

// Reload replaces the runtime on the same session manager through the full
// creation path so settings, extensions, tools, resources, auth, and the
// model catalog are re-read (upstream AgentSession.reload).
func (host *interactiveSessionHost) Reload(ctx context.Context) error {
	replacement, err := func() (*codingagent.SessionRuntime, error) {
		current, err := host.beginReplacement()
		if err != nil {
			return nil, err
		}
		defer host.endReplacement()
		return host.replace(current, extensions.SessionShutdownReload, current.Manager(), nil)
	}()
	if err != nil {
		return err
	}
	return host.finishReplacement(ctx, replacement, nil)
}

func (host *interactiveSessionHost) ListProjectSessions(onProgress session.SessionListProgress) []session.SessionInfo {
	current, err := host.currentSession()
	if err != nil {
		return nil
	}
	manager := current.Manager()
	return session.List(manager.GetCWD(), manager.GetSessionDir(), onProgress, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) ListAllSessions(onProgress session.SessionListProgress) []session.SessionInfo {
	current, err := host.currentSession()
	if err != nil {
		return nil
	}
	manager := current.Manager()
	sessionDir := manager.GetSessionDir()
	if manager.UsesDefaultSessionDir() {
		sessionDir = ""
	}
	return session.ListAll(sessionDir, onProgress, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) TrustState() (modes.InteractiveTrustState, error) {
	current, err := host.currentSession()
	if err != nil {
		return modes.InteractiveTrustState{}, err
	}
	cwd := current.Manager().GetCWD()
	entry, err := config.NewProjectTrustStore(host.agentDir).GetEntry(cwd)
	if err != nil {
		return modes.InteractiveTrustState{}, err
	}
	host.mu.Lock()
	settings := host.inputs.Settings
	host.mu.Unlock()
	projectTrusted := false
	if settings != nil {
		projectTrusted = settings.IsProjectTrusted()
	}
	return modes.InteractiveTrustState{
		CWD:            cwd,
		ProjectTrusted: projectTrusted,
		SavedDecision:  entry,
		Options:        config.GetProjectTrustOptions(cwd, false),
	}, nil
}

func (host *interactiveSessionHost) SetProjectTrust(ctx context.Context, updates []config.ProjectTrustUpdate) error {
	if err := config.NewProjectTrustStore(host.agentDir).SetMany(updates); err != nil {
		return err
	}
	return host.Reload(ctx)
}

func (host *interactiveSessionHost) authStorage() (*config.AuthStorage, error) {
	host.mu.Lock()
	storage := host.inputs.Auth
	host.mu.Unlock()
	if storage != nil {
		return storage, nil
	}
	return config.NewAuthStorage(filepath.Join(host.agentDir, "auth.json"))
}

func (host *interactiveSessionHost) refreshAuthState(ctx context.Context, loggedInProvider string) error {
	host.mu.Lock()
	registry := host.inputs.ModelRegistry
	current := host.session
	host.mu.Unlock()
	if registry == nil {
		return nil
	}
	if err := registry.RefreshAuth(); err != nil {
		return err
	}
	if current != nil {
		if codingagent.IsUnknownModel(current.State().Model) && loggedInProvider != "" {
			if selected := codingagent.DefaultAvailableModel(loggedInProvider, registry.Available(nil)); selected != nil {
				return current.SetModel(ctx, *selected)
			}
			return nil
		}
		current.RefreshCurrentModelFromRegistry(registry)
	}
	return nil
}

func (host *interactiveSessionHost) AuthOptions(ctx context.Context) (modes.InteractiveAuthOptions, error) {
	storage, err := host.authStorage()
	if err != nil {
		return modes.InteractiveAuthOptions{}, err
	}
	stored, err := storage.List(ctx)
	if err != nil {
		return modes.InteractiveAuthOptions{}, err
	}
	storedTypes := make(map[string]aiauth.CredentialType, len(stored))
	for _, credential := range stored {
		storedTypes[credential.ProviderID] = credential.Type
	}
	host.mu.Lock()
	registry := host.inputs.ModelRegistry
	host.mu.Unlock()

	providerIDs := make([]string, 0)
	seen := make(map[string]struct{})
	appendProvider := func(id string) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		providerIDs = append(providerIDs, id)
	}
	options := modes.InteractiveAuthOptions{}
	if registry != nil {
		for _, id := range registry.ProviderIDs() {
			appendProvider(id)
		}
	} else {
		for _, provider := range providers.List() {
			appendProvider(string(provider.ID))
		}
	}
	for _, id := range providerIDs {
		name := id
		methods := aiauth.ProviderAuth{}
		var status *modes.InteractiveAuthStatus
		if registry != nil {
			name = registry.ProviderDisplayName(id)
			methods = registry.ProviderAuth(id)
			authStatus := registry.GetProviderAuthStatus(id, nil)
			if authStatus.Configured {
				authType := aiauth.AuthTypeAPIKey
				if registry.IsUsingOAuth(id) {
					authType = aiauth.AuthTypeOAuth
				}
				source := interactiveAuthStatusSource(authStatus)
				status = &modes.InteractiveAuthStatus{Type: authType, Source: source}
			}
		} else if provider, known := providers.Get(ai.ProviderID(id)); known {
			name = provider.Name
			methods = provider.Methods
		}
		if storedType, exists := storedTypes[id]; exists {
			source := "stored credential"
			if storedType == aiauth.CredentialOAuth {
				source = "OAuth"
			}
			status = &modes.InteractiveAuthStatus{Type: aiauth.AuthType(storedType), Source: source}
		}
		configured := status != nil
		if methods.OAuth != nil {
			loginLabel := ""
			if labeled, ok := methods.OAuth.(aiauth.OAuthLoginLabel); ok {
				loginLabel = labeled.LoginLabel()
			}
			options.Login = append(options.Login, modes.InteractiveAuthProvider{
				ID: id, Name: name, AuthType: aiauth.AuthTypeOAuth, MethodName: methods.OAuth.Name(),
				LoginLabel: loginLabel, Configured: configured, Status: status, LoginAvailable: true,
			})
		}
		if methods.APIKey != nil {
			_, loginAvailable := methods.APIKey.(aiauth.APIKeyLogin)
			options.Login = append(options.Login, modes.InteractiveAuthProvider{
				ID: id, Name: name, AuthType: aiauth.AuthTypeAPIKey, MethodName: methods.APIKey.Name(),
				Configured: configured, Status: status, LoginAvailable: loginAvailable,
			})
		}
	}
	sort.SliceStable(options.Login, func(left, right int) bool {
		return options.Login[left].Name < options.Login[right].Name
	})
	for _, credential := range stored {
		name := credential.ProviderID
		if registry != nil {
			name = registry.ProviderDisplayName(credential.ProviderID)
		} else if provider, known := providers.Get(ai.ProviderID(credential.ProviderID)); known {
			name = provider.Name
		}
		options.Logout = append(options.Logout, modes.InteractiveAuthProvider{
			ID: credential.ProviderID, Name: name, AuthType: aiauth.AuthType(credential.Type), Configured: true,
			Status: &modes.InteractiveAuthStatus{Type: aiauth.AuthType(credential.Type), Source: "stored credential"},
		})
	}
	sort.SliceStable(options.Logout, func(left, right int) bool { return options.Logout[left].Name < options.Logout[right].Name })
	return options, nil
}

func interactiveAuthStatusSource(status extensions.AuthStatus) string {
	if status.Label != "" {
		return status.Label
	}
	switch status.Source {
	case "models_json_key":
		return "key in models.json"
	case "models_json_command":
		return "command in models.json"
	case "stored":
		return "stored credential"
	default:
		return status.Source
	}
}

func (host *interactiveSessionHost) Login(ctx context.Context, providerID string, authType aiauth.AuthType, interaction aiauth.AuthInteraction) error {
	host.mu.Lock()
	registry := host.inputs.ModelRegistry
	host.mu.Unlock()
	methods := aiauth.ProviderAuth{}
	known := false
	if registry != nil {
		if _, exists := registry.Provider(providerID); exists {
			methods, known = registry.ProviderAuth(providerID), true
		}
	} else if definition, exists := providers.Get(ai.ProviderID(providerID)); exists {
		methods, known = definition.Methods, true
	}
	if !known {
		return fmt.Errorf("provider %q does not support login", providerID)
	}
	var credential *aiauth.Credential
	var err error
	switch authType {
	case aiauth.AuthTypeOAuth:
		if methods.OAuth == nil {
			return fmt.Errorf("provider %q does not support OAuth login", providerID)
		}
		credential, err = methods.OAuth.Login(ctx, interaction)
	case aiauth.AuthTypeAPIKey:
		login, ok := methods.APIKey.(aiauth.APIKeyLogin)
		if !ok {
			return fmt.Errorf("provider %q API-key auth is configured outside pi", providerID)
		}
		credential, err = login.Login(ctx, interaction)
	default:
		return fmt.Errorf("provider %q has unknown auth type %q", providerID, authType)
	}
	if err != nil {
		return err
	}
	storage, err := host.authStorage()
	if err != nil {
		return err
	}
	_, err = storage.Modify(ctx, providerID, func(*aiauth.Credential) (*aiauth.Credential, error) {
		return credential, nil
	})
	if err != nil {
		return err
	}
	return host.refreshAuthState(ctx, providerID)
}

func (host *interactiveSessionHost) Logout(ctx context.Context, providerID string) error {
	storage, err := host.authStorage()
	if err != nil {
		return err
	}
	if err := storage.Delete(ctx, providerID); err != nil {
		return err
	}
	return host.refreshAuthState(ctx, "")
}

func (host *interactiveSessionHost) Dispose() {
	host.mu.Lock()
	if host.disposed {
		host.mu.Unlock()
		return
	}
	host.disposed = true
	current := host.session
	beforeInvalidate := host.beforeInvalidate
	host.session = nil
	host.mu.Unlock()
	if current != nil {
		current.ShutdownExtensions(extensions.SessionShutdownQuit, nil)
		if beforeInvalidate != nil {
			beforeInvalidate()
		}
		current.Dispose()
	}
}

// assertSessionCwdExists mirrors upstream session-cwd.ts.
func assertSessionCwdExists(manager *session.SessionManager, fallbackCWD string) error {
	if manager.GetSessionFile() == "" {
		return nil
	}
	sessionCWD := manager.GetCWD()
	if sessionCWD == "" {
		return nil
	}
	if _, err := os.Stat(sessionCWD); err == nil {
		return nil
	}
	return &modes.MissingSessionCwdError{
		SessionFile: manager.GetSessionFile(),
		SessionCWD:  sessionCWD,
		FallbackCWD: fallbackCWD,
	}
}

// resolveImportPath expands ~ and makes the /import argument absolute
// (upstream utils/paths resolvePath).
func resolveImportPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path[1:], "/"))
	}
	return filepath.Abs(path)
}
