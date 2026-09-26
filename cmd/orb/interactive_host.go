package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/ai/providers"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/uuidv7"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
	"github.com/OrdalieTech/orb/plugins/usage"
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
func sessionRuntimeConfig(inputs runtimeInputs, manager *session.SessionManager, options sessionRuntimeOptions) (agent.SessionRuntimeConfig, error) {
	settings := inputs.Settings
	if settings == nil {
		agentDir, err := config.GetAgentDir()
		if err != nil {
			return agent.SessionRuntimeConfig{}, err
		}
		settings, err = config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(agentDir))
		if err != nil {
			return agent.SessionRuntimeConfig{}, err
		}
	}
	var errorHandler func(extensions.ExtensionError)
	if options.errorWriter != nil {
		writer := options.errorWriter
		errorHandler = func(extensionError extensions.ExtensionError) {
			_, _ = fmt.Fprintf(writer, "Extension error (%s, %s): %s\n", extensionError.ExtensionPath, extensionError.Event, extensionError.Error)
		}
	}
	runtimeConfig := agent.SessionRuntimeConfig{
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
		Clock:               inputs.Clock,
		ResourceLoader:      inputs.ResourceLoader,
		SessionStart:        options.sessionStart,
		DeferSessionStart:   options.deferSessionStart,
	}
	if inputs.ModelRegistry != nil {
		runtimeConfig.ModelRegistry = inputs.ModelRegistry
	}
	return runtimeConfig, nil
}

func buildSessionRuntime(inputs runtimeInputs, manager *session.SessionManager, options sessionRuntimeOptions) (*agent.SessionRuntime, error) {
	runtimeConfig, err := sessionRuntimeConfig(inputs, manager, options)
	if err != nil {
		return nil, err
	}
	// Providers key affinity and prompt caches on the session id; upstream
	// createAgentSession passes sessionId into the Agent at construction.
	inputs.Agent.SetStreamSessionID(manager.GetSessionID())
	created, err := newSessionRuntime(runtimeConfig)
	return created, err
}

// createReplacementRuntime rebuilds the complete runtime for manager from the
// immutable startup args. AgentSessionRuntime tears down the old runtime before
// calling this factory, so failures propagate with no rollback.
func createReplacementRuntime(
	dependencies cliDependencies,
	args CLIArgs,
	manager *session.SessionManager,
	options sessionRuntimeOptions,
) (*agent.SessionRuntime, runtimeInputs, error) {
	contextState := manager.BuildSessionContext()
	if len(manager.GetEntries()) > 0 {
		applySessionDefaults(&args, contextState, manager.GetBranch())
	}
	if args.extensionsLoaded && args.extensionRegistry != nil {
		// Replacement runtimes need fresh extension instances (upstream re-runs
		// factories per session); the resource loader reuses whatever registry it
		// receives on its first load, so refresh the startup-preloaded one here.
		fresh, freshErr := args.extensionRegistry.Fresh(manager.GetCWD())
		if freshErr != nil {
			return nil, runtimeInputs{}, freshErr
		}
		args.extensionRegistry = fresh
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
	if repo := manager.HarnessRepo(); repo != nil {
		options := harness.SessionCreateOptions{CWD: manager.GetCWD()}
		if parentSession != "" {
			options.ParentSessionPath = &parentSession
		}
		created, err := repo.Create(context.Background(), options)
		if err != nil {
			return nil, err
		}
		return session.FromHarnessStorage(created.Storage(), session.WithHarnessRepo(repo))
	}
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

	if repo := manager.HarnessRepo(); repo != nil {
		metadata, _ := manager.HarnessMetadata()
		forked, err := repo.Fork(context.Background(), metadata, harness.SessionForkOptions{SessionCreateOptions: harness.SessionCreateOptions{CWD: manager.GetCWD()}, EntryID: entryID, Position: harness.ForkPosition(position)})
		if err != nil {
			return nil, "", err
		}
		replacement, err := session.FromHarnessStorage(forked.Storage(), session.WithHarnessRepo(repo))
		return replacement, selectedText, err
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
	bridgeControl      *agent.SessionControl
	bridgeFinish       func()
	bridgeObservers    map[uint64]func(*agent.AgentSession)
	bridgeNextObserver uint64

	usageCache        usage.Cache
	mu                sync.Mutex
	args              CLIArgs
	dependencies      cliDependencies
	agentDir          string
	errorWriter       io.Writer
	session           *agent.SessionRuntime
	inputs            runtimeInputs
	rebind            func(*agent.SessionRuntime) error
	beforeInvalidate  func()
	afterSessionStart func(*agent.SessionRuntime) error
	replacing         bool
	disposed          bool
}

func newInteractiveSessionHost(
	args CLIArgs,
	dependencies cliDependencies,
	runtime *agent.SessionRuntime,
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

func (host *interactiveSessionHost) Session() *agent.SessionRuntime {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.session
}

func (host *interactiveSessionHost) SetRebindSession(rebind func(*agent.SessionRuntime) error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.rebind = rebind
}

func (host *interactiveSessionHost) SetBeforeSessionInvalidate(beforeInvalidate func()) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.beforeInvalidate = beforeInvalidate
}

func (host *interactiveSessionHost) SetAfterSessionStart(afterSessionStart func(*agent.SessionRuntime) error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.afterSessionStart = afterSessionStart
}

// bindCommandActions makes extension command-context session operations run
// through the host replacement path.
func (host *interactiveSessionHost) bindCommandActions(runtime *agent.SessionRuntime) {
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

func (host *interactiveSessionHost) beginReplacement(ctx context.Context) (current *agent.SessionRuntime, err error) {
	var release func()
	defer func() {
		if err != nil && release != nil {
			release()
		}
	}()
	host.mu.Lock()
	control := host.bridgeControl
	host.mu.Unlock()
	if control != nil {
		finish, err := control.BeginTransition(ctx)
		if err != nil {
			return nil, err
		}
		host.mu.Lock()
		release = finish
		host.bridgeFinish = finish
		host.mu.Unlock()
	}
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
	finish := host.bridgeFinish
	host.bridgeFinish = nil
	host.mu.Unlock()
	if finish != nil {
		finish()
	}
}

func (host *interactiveSessionHost) currentSession() (*agent.SessionRuntime, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.disposed || host.session == nil {
		return nil, errors.New("interactive session host is disposed")
	}
	return host.session, nil
}

// emitSessionBeforeSwitch reports whether an extension cancelled the switch.
func emitSessionBeforeSwitch(ctx context.Context, runtime *agent.SessionRuntime, reason extensions.SessionSwitchReason, targetSessionFile *string) bool {
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
func emitSessionBeforeFork(ctx context.Context, runtime *agent.SessionRuntime, entryID string, position extensions.ForkPosition) bool {
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
	current *agent.SessionRuntime,
	reason extensions.SessionShutdownReason,
	manager *session.SessionManager,
	setup func(*session.SessionManager) error,
) (*agent.SessionRuntime, error) {
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
	releasePrevious, err := host.args.native.claimSession(manager)
	if err != nil {
		return nil, err
	}
	defer releasePrevious()
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
	observers := make([]func(*agent.AgentSession), 0, len(host.bridgeObservers))
	for _, f := range host.bridgeObservers {
		observers = append(observers, f)
	}
	host.mu.Unlock()
	for _, f := range observers {
		f(replacement)
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
func (host *interactiveSessionHost) finishReplacement(ctx context.Context, replacement *agent.SessionRuntime, withSession func(context.Context, extensions.ReplacedSessionContext) error) error {
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
	replacement, cancelled, err := func() (*agent.SessionRuntime, bool, error) {
		current, err := host.beginReplacement(ctx)
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
		if current.Agent().UsesSessionLoop() {
			if model := current.State().Model; model != nil {
				if _, err := manager.AppendModelChange(string(model.Provider), model.ID); err != nil {
					return nil, false, err
				}
			}
		}
		if options != nil && options.Prepare != nil {
			if err := options.Prepare(manager); err != nil {
				return nil, false, err
			}
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
	replacement, cancelled, err := func() (*agent.SessionRuntime, bool, error) {
		current, err := host.beginReplacement(ctx)
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
		var manager *session.SessionManager
		if host.args.native != nil {
			opened, openErr := host.args.native.sessions().OpenPath(ctx, sessionPath)
			err = openErr
			if err == nil {
				openOptions = append(openOptions, session.WithHarnessRepo(host.args.native.sessions()))
				manager, err = session.FromHarnessStorage(opened.Storage(), openOptions...)
			}
		} else {
			manager, err = session.Open(sessionPath, "", openOptions...)
		}
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
	replacement, selectedText, cancelled, err := func() (*agent.SessionRuntime, string, bool, error) {
		current, err := host.beginReplacement(ctx)
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

// importCopy writes inputPath's journal under a new session ID, for an import
// whose ID is already stored with other content.
func importCopy(inputPath string) (string, error) {
	path, err := resolveImportPath(inputPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	first, rest, _ := bytes.Cut(data, []byte("\n"))
	var header map[string]json.RawMessage
	if err := json.Unmarshal(first, &header); err != nil {
		return "", err
	}
	id, err := uuidv7.Generate(time.Now())
	if err != nil {
		return "", err
	}
	header["id"], _ = json.Marshal(id)
	if first, err = json.Marshal(header); err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "orb-import-*.jsonl")
	if err != nil {
		return "", err
	}
	_, err = file.Write(append(append(first, '\n'), rest...))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return file.Name(), err
}

func (host *interactiveSessionHost) ImportSession(ctx context.Context, inputPath, cwdOverride string) (extensions.SessionReplacementResult, error) {
	if host.args.native != nil {
		result, err := host.SwitchSession(ctx, inputPath, cwdOverride, nil)
		if !errors.Is(err, sqlite.ErrImportConflict) {
			return result, err
		}
		// An export of a conversation that went on since opens as a copy of its own.
		copied, copyErr := importCopy(inputPath)
		if copyErr != nil {
			return result, err
		}
		defer func() { _ = os.Remove(copied) }()
		return host.SwitchSession(ctx, copied, cwdOverride, nil)
	}
	runtime, cancelled, err := func() (*agent.SessionRuntime, bool, error) {
		current, err := host.beginReplacement(ctx)
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
	replacement, err := func() (*agent.SessionRuntime, error) {
		current, err := host.beginReplacement(ctx)
		if err != nil {
			return nil, err
		}
		defer host.endReplacement()
		return host.replace(current, extensions.SessionShutdownReload, current.Manager(), nil)
	}()
	if err != nil {
		return err
	}
	if err := host.finishReplacement(ctx, replacement, nil); err != nil {
		return err
	}
	return host.args.bridgeLink.configureBridge(host.args.BridgeProfile != "" || host.inputs.Settings.GetPlugins()["bridge"])
}

func (host *interactiveSessionHost) ListProjectSessions(onProgress session.SessionListProgress) []session.SessionInfo {
	current, err := host.currentSession()
	if err != nil {
		return nil
	}
	manager := current.Manager()
	if host.args.native != nil {
		rows, _ := host.args.native.sessions().ListInfo(context.Background(), manager.GetCWD(), nil)
		return rows
	}
	return session.List(manager.GetCWD(), manager.GetSessionDir(), onProgress, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) ListAllSessions(onProgress session.SessionListProgress) []session.SessionInfo {
	current, err := host.currentSession()
	if err != nil {
		return nil
	}
	if host.args.native != nil {
		rows, _ := host.args.native.sessions().ListInfo(context.Background(), "", nil)
		return rows
	}
	manager := current.Manager()
	sessionDir := manager.GetSessionDir()
	if manager.UsesDefaultSessionDir() {
		sessionDir = ""
	}
	return session.ListAll(sessionDir, onProgress, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) ListProjectSessionsContext(ctx context.Context, onUpdate session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
	current, err := host.currentSession()
	if err != nil {
		return nil, err
	}
	if host.args.native != nil {
		return host.args.native.sessions().ListInfo(ctx, current.Manager().GetCWD(), onUpdate)
	}
	manager := current.Manager()
	return session.ListContext(ctx, manager.GetCWD(), manager.GetSessionDir(), onUpdate, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) ListAllSessionsContext(ctx context.Context, onUpdate session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
	current, err := host.currentSession()
	if err != nil {
		return nil, err
	}
	if host.args.native != nil {
		return host.args.native.sessions().ListInfo(ctx, "", onUpdate)
	}
	manager := current.Manager()
	sessionDir := manager.GetSessionDir()
	if manager.UsesDefaultSessionDir() {
		sessionDir = ""
	}
	return session.ListAllContext(ctx, sessionDir, onUpdate, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) TrustState() (modes.InteractiveTrustState, error) {
	current, err := host.currentSession()
	if err != nil {
		return modes.InteractiveTrustState{}, err
	}
	cwd := current.Manager().GetCWD()
	trust, err := host.args.native.trust(host.agentDir)
	if err != nil {
		return modes.InteractiveTrustState{}, err
	}
	entry, err := trust.GetEntry(cwd)
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
	trust, err := host.args.native.trust(host.agentDir)
	if err != nil {
		return err
	}
	if err := trust.SetMany(updates); err != nil {
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
	return host.args.native.auth(host.agentDir)
}

func (host *interactiveSessionHost) authCredentials() (aiauth.CredentialStore, error) {
	host.mu.Lock()
	runtimeAuth := host.inputs.RuntimeAuth
	host.mu.Unlock()
	if runtimeAuth != nil {
		return runtimeAuth, nil
	}
	return host.authStorage()
}

func (host *interactiveSessionHost) refreshAuthState(_ context.Context, _ string) error {
	host.usageCache.Clear()
	if host.args.usageCache != nil {
		host.args.usageCache.Clear()
	}
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
		// Default-model selection after login belongs to the TUI
		// (completeProviderAuthentication, interactive-mode.ts:5033-5084);
		// the host only refreshes credentials and the current model
		// projection. RefreshCurrentModelFromRegistry is a no-op for the
		// unknown-model sentinel.
		current.RefreshCurrentModelFromRegistry(registry)
	}
	host.mu.Lock()
	extensionsRegistry := host.inputs.Extensions
	host.mu.Unlock()
	if extensionsRegistry != nil {
		extensionsRegistry.Events().Emit(context.Background(), "orb.accounts.changed", nil)
	}
	return nil
}

func (host *interactiveSessionHost) AuthOptions(ctx context.Context) (modes.InteractiveAuthOptions, error) {
	credentials, err := host.authCredentials()
	if err != nil {
		return modes.InteractiveAuthOptions{}, err
	}
	stored, err := credentials.List(ctx)
	if err != nil {
		return modes.InteractiveAuthOptions{}, err
	}
	storedTypes := make(map[string]aiauth.CredentialType, len(stored))
	for _, credential := range stored {
		storedTypes[credential.ProviderID] = credential.Type
	}
	host.mu.Lock()
	registry := host.inputs.ModelRegistry
	runtimeAuth := host.inputs.RuntimeAuth
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
		if runtimeAuth != nil && runtimeAuth.HasRuntimeAPIKey(id) {
			status = &modes.InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "runtime"}
		}
		if id == claudesessions.Name {
			// Claude signs in only through its subscription; the Claude Code login
			// it already has is the same kind of account, not an API key.
			if status != nil {
				status.Type = aiauth.AuthTypeOAuth
			}
			methods.APIKey = nil
		}
		if status == nil {
			// Registry-less fallback only: with a registry, the stored
			// credential already surfaces as the raw "stored" source above
			// (upstream getProviderAuthStatus).
			if storedType, exists := storedTypes[id]; exists {
				status = &modes.InteractiveAuthStatus{Type: aiauth.AuthType(storedType), Source: "stored"}
			}
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

// interactiveAuthStatusSource mirrors upstream getLoginProviderOptions
// (interactive-mode.ts:4795-4825): the label when present, otherwise the raw
// runtime source ("stored", "models_json_key", "runtime", ...).
func interactiveAuthStatusSource(status extensions.AuthStatus) string {
	if status.Label != "" {
		return status.Label
	}
	return status.Source
}

func (host *interactiveSessionHost) loginCredential(ctx context.Context, providerID string, authType aiauth.AuthType, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
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
		return nil, fmt.Errorf("provider %q does not support login", providerID)
	}
	var credential *aiauth.Credential
	var err error
	switch authType {
	case aiauth.AuthTypeOAuth:
		if methods.OAuth == nil {
			return nil, fmt.Errorf("provider %q does not support OAuth login", providerID)
		}
		credential, err = methods.OAuth.Login(ctx, interaction)
	case aiauth.AuthTypeAPIKey:
		login, ok := methods.APIKey.(aiauth.APIKeyLogin)
		if !ok {
			return nil, fmt.Errorf("provider %q API-key auth is configured outside orb", providerID)
		}
		credential, err = login.Login(ctx, interaction)
	default:
		return nil, fmt.Errorf("provider %q has unknown auth type %q", providerID, authType)
	}
	if err != nil {
		return nil, err
	}
	return credential, nil
}

func (host *interactiveSessionHost) Login(ctx context.Context, providerID string, authType aiauth.AuthType, interaction aiauth.AuthInteraction) error {
	credential, err := host.loginCredential(ctx, providerID, authType, interaction)
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
	credentials, err := host.authCredentials()
	if err != nil {
		return err
	}
	host.mu.Lock()
	runtimeAuth := host.inputs.RuntimeAuth
	wasRuntime := runtimeAuth != nil && runtimeAuth.HasRuntimeAPIKey(providerID)
	host.mu.Unlock()
	if err := credentials.Delete(ctx, providerID); err != nil {
		return err
	}
	if wasRuntime {
		host.mu.Lock()
		host.args.APIKey = nil
		host.mu.Unlock()
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
	if !manager.IsPersisted() {
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

func (host *interactiveSessionHost) accountStore() (*accounts.Store, error) {
	host.mu.Lock()
	store := host.inputs.Accounts
	host.mu.Unlock()
	if store != nil {
		return store, nil
	}
	base, err := host.authStorage()
	if err != nil {
		return nil, err
	}
	return host.args.native.accounts(host.agentDir, base), nil
}

func (host *interactiveSessionHost) ProviderAccounts(ctx context.Context) ([]accounts.Account, error) {
	store, err := host.accountStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	options, err := host.AuthOptions(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Provider] = true
	}
	claudeLogin := host.claudeAmbientAccount(ctx, rows)
	for _, option := range options.Login {
		if option.ID == claudesessions.Name || option.Status == nil || seen[option.ID] {
			continue
		}
		seen[option.ID] = true
		if option.Status.Source == "runtime" {
			continue
		}
		rows = append(rows, accounts.Account{ID: "ambient", Provider: option.ID, Name: option.Status.Source, Type: aiauth.CredentialType(option.Status.Type), Active: true})
	}
	if claudeLogin != nil {
		rows = append(rows, *claudeLogin)
	}
	host.mu.Lock()
	runtime := host.inputs.RuntimeAuth
	host.mu.Unlock()
	if runtime != nil {
		for provider := range seen {
			if runtime.HasRuntimeAPIKey(provider) {
				for i := range rows {
					if rows[i].Provider == provider {
						rows[i].Active = false
					}
				}
				rows = append(rows, accounts.Account{ID: "runtime", Provider: provider, Name: "Command-line API key", Type: aiauth.CredentialAPIKey, Active: true})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Provider < rows[j].Provider })
	return rows, nil
}

// ProviderName is a provider's display name, as /login shows it.
func (host *interactiveSessionHost) ProviderName(id string) string {
	host.mu.Lock()
	registry := host.inputs.ModelRegistry
	host.mu.Unlock()
	if registry == nil {
		return ""
	}
	return registry.ProviderDisplayName(id)
}

// claudeAmbientAccount is the user's own Claude Code login as an account row.
// Unlike other providers' ambient sources it stays listed beside added
// accounts, since selecting it is how Claude returns to that login.
func (host *interactiveSessionHost) claudeAmbientAccount(ctx context.Context, rows []accounts.Account) *accounts.Account {
	host.mu.Lock()
	settings := host.inputs.Settings
	host.mu.Unlock()
	if settings == nil || !settings.GetPlugins()[claudesessions.Name] {
		return nil
	}
	label, ok := claudesessions.AmbientAccount(ctx, settings, os.Environ())
	if !ok {
		return nil
	}
	active := !slices.ContainsFunc(rows, func(row accounts.Account) bool { return row.Provider == claudesessions.Name && row.Active })
	return &accounts.Account{ID: "ambient", Provider: claudesessions.Name, Name: label, Type: aiauth.CredentialOAuth, Active: active}
}

// forgetClaudeAccount signs out the Claude directory an account ran with once
// it is removed or replaced; other providers keep nothing outside the store.
func (host *interactiveSessionHost) forgetClaudeAccount(credential *aiauth.Credential) {
	host.mu.Lock()
	settings := host.inputs.Settings
	host.mu.Unlock()
	if settings != nil && credential != nil {
		claudesessions.ForgetAccount(settings, host.agentDir, os.Environ(), credential)
	}
}

func (host *interactiveSessionHost) accountChangeAllowed() error {
	host.mu.Lock()
	session := host.session
	host.mu.Unlock()
	if session != nil && session.State().IsStreaming {
		return errors.New("wait for the current response before changing accounts")
	}
	return nil
}

func (host *interactiveSessionHost) ChangeAccount(ctx context.Context, provider, id, action, name string) error {
	if err := host.accountChangeAllowed(); err != nil {
		return err
	}
	store, err := host.accountStore()
	if err != nil {
		return err
	}
	var removed *aiauth.Credential
	switch action {
	case "select":
		host.mu.Lock()
		overridden := host.inputs.RuntimeAuth != nil && host.inputs.RuntimeAuth.HasRuntimeAPIKey(provider)
		host.mu.Unlock()
		if overridden {
			return errors.New("restart without --api-key to switch this provider account")
		}
		if provider == claudesessions.Name && id == "ambient" {
			err = store.Deselect(ctx, provider)
			break
		}
		err = store.Select(ctx, provider, id)
	case "remove":
		if provider == claudesessions.Name {
			removed, _ = store.View(provider, id).Read(ctx, provider)
		}
		err = store.Remove(ctx, provider, id)
	case "rename":
		err = store.Rename(ctx, provider, id, name)
	default:
		return errors.New("unknown account action")
	}
	if err != nil {
		return err
	}
	host.forgetClaudeAccount(removed)
	return host.refreshAuthState(ctx, provider)
}

func (host *interactiveSessionHost) LoginAccount(ctx context.Context, provider string, kind aiauth.AuthType, id, name string, interaction aiauth.AuthInteraction) error {
	if err := host.accountChangeAllowed(); err != nil {
		return err
	}
	credential, err := host.loginCredential(ctx, provider, kind, interaction)
	if err != nil {
		return err
	}
	if err := host.accountChangeAllowed(); err != nil {
		return err
	}
	store, err := host.accountStore()
	if err != nil {
		return err
	}
	var replaced *aiauth.Credential
	if id == "" {
		_, err = store.Add(ctx, provider, name, credential)
	} else {
		_, err = store.View(provider, id).Modify(ctx, provider, func(previous *aiauth.Credential) (*aiauth.Credential, error) {
			replaced = previous
			return credential, nil
		})
	}
	if err != nil {
		return err
	}
	if provider == claudesessions.Name {
		host.forgetClaudeAccount(replaced)
	}
	return host.refreshAuthState(ctx, provider)
}

func (host *interactiveSessionHost) CachedAccountUsage(provider, id string) (usage.Snapshot, bool) {
	return host.usageCache.Peek(provider + "/" + id)
}
func (host *interactiveSessionHost) AccountUsage(ctx context.Context, provider, id string) (usage.Snapshot, error) {
	return host.usageCache.Fetch(ctx, provider+"/"+id, func(ctx context.Context) (usage.Snapshot, error) { return host.fetchAccountUsage(ctx, provider, id) })
}
func (host *interactiveSessionHost) fetchAccountUsage(ctx context.Context, provider, id string) (usage.Snapshot, error) {
	store, err := host.accountStore()
	if err != nil {
		return usage.Snapshot{}, err
	}
	if provider == claudesessions.Name {
		host.mu.Lock()
		settings := host.inputs.Settings
		host.mu.Unlock()
		var credential *aiauth.Credential
		if id != "ambient" {
			if credential, err = store.View(provider, id).Read(ctx, provider); err != nil {
				return usage.Snapshot{}, err
			}
		}
		return claudesessions.Usage(ctx, settings, host.agentDir, os.Environ(), credential)
	}
	host.mu.Lock()
	registry := host.inputs.ModelRegistry
	runtime := host.inputs.RuntimeAuth
	host.mu.Unlock()
	if registry == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	credentials := store.View(provider, id)
	if id == "ambient" || id == "runtime" {
		if runtime != nil {
			credentials = runtime
		} else {
			credentials = aiauth.NewMemoryStore(nil)
		}
	}
	if credentials == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := aiauth.ResolveProviderAuth(ctx, provider, registry.ProviderAuth(provider), credentials, aiauth.EnvironmentContext{}, nil)
	if err != nil {
		return usage.Snapshot{}, err
	}
	if result == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	return (usage.Client{Cache: host.args.usageCache}).Fetch(ctx, provider, result.Auth)
}

func (host *interactiveSessionHost) UsageEnabled() bool {
	host.mu.Lock()
	settings := host.inputs.Settings
	host.mu.Unlock()
	return settings != nil && settings.GetPlugins()["provider-usage"]
}
func (host *interactiveSessionHost) SetUsageEnabled(enabled bool) error {
	host.mu.Lock()
	settings := host.inputs.Settings
	host.mu.Unlock()
	if settings == nil {
		return errors.New("settings are unavailable")
	}
	settings.SetPluginEnabled("provider-usage", enabled)
	if errors := settings.DrainErrors(); len(errors) > 0 {
		return errors[0]
	}
	return nil
}

func (host *interactiveSessionHost) DeleteSession(reference string) (modes.SessionDeleteMethod, error) {
	if host.args.native == nil {
		// File-backed SDK hosts retain the selector's ordinary delete path.
		return modes.SessionDeleteUnlink, os.Remove(reference)
	}
	return host.args.native.deleteSession(reference)
}
