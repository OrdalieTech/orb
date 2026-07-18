package codingagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

// CreateAgentSessionRuntimeFactory recreates a session after new, resume,
// fork, and import operations. A nil factory uses [NewAgentSession].
type CreateAgentSessionRuntimeFactory func(context.Context, AgentSessionOptions) (*AgentSessionResult, error)

// AgentSessionRuntime owns the active [AgentSession] and replaces it for
// session lifecycle operations.
type AgentSessionRuntime struct {
	opMu sync.Mutex
	mu   sync.RWMutex

	session          *AgentSession
	result           *AgentSessionResult
	options          AgentSessionOptions
	create           CreateAgentSessionRuntimeFactory
	rebind           func(*AgentSession) error
	beforeInvalidate func()
	disposed         bool
}

// AgentSessionRuntimeSwitchOptions configures [AgentSessionRuntime.SwitchSession].
type AgentSessionRuntimeSwitchOptions struct {
	CWDOverride                string
	WithSession                func(context.Context, extensions.ReplacedSessionContext) error
	ProjectTrustContextFactory func(string) extensions.ProjectTrustContext
}

// AgentSessionRuntimeForkResult describes a fork result.
type AgentSessionRuntimeForkResult struct {
	Cancelled    bool
	SelectedText *string
}

// SessionImportFileNotFoundError reports a missing JSONL import source.
type SessionImportFileNotFoundError struct {
	FilePath string
}

func (failure *SessionImportFileNotFoundError) Error() string {
	return "File not found: " + failure.FilePath
}

// MissingSessionCWDError reports a persisted session whose working directory
// no longer exists.
type MissingSessionCWDError struct {
	SessionFile string
	SessionCWD  string
	FallbackCWD string
}

func (failure *MissingSessionCWDError) Error() string {
	sessionFile := ""
	if failure.SessionFile != "" {
		sessionFile = "\nSession file: " + failure.SessionFile
	}
	return fmt.Sprintf(
		"Stored session working directory does not exist: %s%s\nCurrent working directory: %s",
		failure.SessionCWD,
		sessionFile,
		failure.FallbackCWD,
	)
}

// NewAgentSessionRuntime creates a replaceable session host. The optional
// factory is reused so embedders can recreate cwd-bound services.
func NewAgentSessionRuntime(
	ctx context.Context,
	options AgentSessionOptions,
	factory ...CreateAgentSessionRuntimeFactory,
) (*AgentSessionRuntime, error) {
	create := CreateAgentSessionRuntimeFactory(func(_ context.Context, options AgentSessionOptions) (*AgentSessionResult, error) {
		return NewAgentSession(options)
	})
	if len(factory) > 0 && factory[0] != nil {
		create = factory[0]
	}
	options.DeferExtensionStart = true
	if options.SessionManager != nil {
		fallback := options.CWD
		if fallback == "" {
			fallback = options.SessionManager.GetCWD()
		}
		if err := assertRuntimeSessionCWD(options.SessionManager, fallback); err != nil {
			return nil, err
		}
	}
	result, err := create(runtimeContext(ctx), options)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Session == nil {
		return nil, errors.New("codingagent: session runtime factory returned no session")
	}
	if err := assertRuntimeSessionCWD(result.Session.Manager(), options.CWD); err != nil {
		result.Session.Dispose()
		return nil, err
	}
	options.ExtensionRegistry = result.ExtensionRegistry
	runtime := &AgentSessionRuntime{session: result.Session, result: result, options: options, create: create}
	runtime.bindSessionCommands(result.Session)
	return runtime, nil
}

// Session returns the active session.
func (runtime *AgentSessionRuntime) Session() *AgentSession {
	if runtime == nil {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.session
}

// Services returns the active session's cwd-bound services.
func (runtime *AgentSessionRuntime) Services() *AgentSessionServices {
	if runtime == nil {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.result == nil {
		return nil
	}
	return runtime.result.Services
}

// CWD returns the effective working directory of the active session.
func (runtime *AgentSessionRuntime) CWD() string {
	if services := runtime.Services(); services != nil {
		return services.CWD
	}
	if session := runtime.Session(); session != nil && session.Manager() != nil {
		return session.Manager().GetCWD()
	}
	return ""
}

// Diagnostics returns a snapshot of the active runtime's non-fatal issues.
func (runtime *AgentSessionRuntime) Diagnostics() []AgentSessionRuntimeDiagnostic {
	if runtime == nil {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.result == nil {
		return nil
	}
	return append([]AgentSessionRuntimeDiagnostic(nil), runtime.result.Diagnostics...)
}

// ModelFallbackMessage returns the current session's model-restoration warning.
func (runtime *AgentSessionRuntime) ModelFallbackMessage() string {
	if runtime == nil {
		return ""
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.result == nil {
		return ""
	}
	return runtime.result.ModelFallbackMessage
}

// SetRebindSession sets the callback run after each replacement is installed.
func (runtime *AgentSessionRuntime) SetRebindSession(rebind func(*AgentSession) error) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.rebind = rebind
	runtime.mu.Unlock()
}

// SetBeforeSessionInvalidate sets the synchronous callback run after
// session_shutdown and before the old extension context becomes stale.
func (runtime *AgentSessionRuntime) SetBeforeSessionInvalidate(callback func()) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.beforeInvalidate = callback
	runtime.mu.Unlock()
}

// NewSession replaces the active session with a fresh persisted or in-memory session.
func (runtime *AgentSessionRuntime) NewSession(
	ctx context.Context,
	options *extensions.NewSessionOptions,
) (extensions.SessionReplacementResult, error) {
	if runtime == nil {
		return extensions.SessionReplacementResult{}, errors.New("codingagent: nil agent session runtime")
	}
	runtime.opMu.Lock()
	locked := true
	defer func() {
		if locked {
			runtime.opMu.Unlock()
		}
	}()
	ctx = runtimeContext(ctx)
	current, err := runtime.current()
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	if runtimeSwitchCancelled(ctx, current, extensions.SessionBeforeSwitchEvent{Reason: extensions.SessionSwitchNew}) {
		return extensions.SessionReplacementResult{Cancelled: true}, nil
	}
	manager := current.Manager()
	var replacement *sessionstore.SessionManager
	if manager.IsPersisted() {
		replacement, err = sessionstore.Create(manager.GetCWD(), manager.GetSessionDir())
	} else {
		replacement, err = sessionstore.InMemory(manager.GetCWD())
	}
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	if options != nil && options.ParentSession != "" {
		parent := options.ParentSession
		if _, err := replacement.NewSession(sessionstore.NewSessionOptions{ParentSession: &parent}); err != nil {
			return extensions.SessionReplacementResult{}, err
		}
	}
	created, err := runtime.replace(ctx, current, replacement, extensions.SessionShutdownNew, extensions.SessionStartNew, nil)
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	if options != nil && options.Setup != nil {
		if err := options.Setup(replacement); err != nil {
			return extensions.SessionReplacementResult{}, err
		}
		contextState := replacement.BuildSessionContext()
		messages := make(agent.AgentMessages, 0, len(contextState.Messages))
		for _, raw := range contextState.Messages {
			messages = append(messages, decodeSessionMessage(raw))
		}
		created.agent.SetMessages(messages)
	}
	var withSession func(context.Context, extensions.ReplacedSessionContext) error
	if options != nil {
		withSession = options.WithSession
	}
	if err := runtime.rebindReplacement(created); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	locked = false
	runtime.opMu.Unlock()
	if err := runtime.runWithSession(ctx, created, withSession); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	return extensions.SessionReplacementResult{}, nil
}

// SwitchSession resumes a JSONL session and replaces the active session.
func (runtime *AgentSessionRuntime) SwitchSession(
	ctx context.Context,
	path string,
	options *AgentSessionRuntimeSwitchOptions,
) (extensions.SessionReplacementResult, error) {
	if runtime == nil {
		return extensions.SessionReplacementResult{}, errors.New("codingagent: nil agent session runtime")
	}
	runtime.opMu.Lock()
	locked := true
	defer func() {
		if locked {
			runtime.opMu.Unlock()
		}
	}()
	ctx = runtimeContext(ctx)
	current, err := runtime.current()
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	target := path
	if runtimeSwitchCancelled(ctx, current, extensions.SessionBeforeSwitchEvent{
		Reason: extensions.SessionSwitchResume, TargetSessionFile: &target,
	}) {
		return extensions.SessionReplacementResult{Cancelled: true}, nil
	}
	var openOptions []sessionstore.Option
	if options != nil && options.CWDOverride != "" {
		openOptions = append(openOptions, sessionstore.WithCwdOverride(options.CWDOverride))
	}
	replacement, err := sessionstore.Open(path, "", openOptions...)
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	if err := assertRuntimeSessionCWD(replacement, current.Manager().GetCWD()); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	var configure func(*AgentSessionOptions)
	if options != nil && options.ProjectTrustContextFactory != nil {
		trustContext := options.ProjectTrustContextFactory(replacement.GetCWD())
		configure = func(next *AgentSessionOptions) { next.ProjectTrustContext = trustContext }
	}
	created, err := runtime.replace(ctx, current, replacement, extensions.SessionShutdownResume, extensions.SessionStartResume, configure)
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	var withSession func(context.Context, extensions.ReplacedSessionContext) error
	if options != nil {
		withSession = options.WithSession
	}
	if err := runtime.rebindReplacement(created); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	locked = false
	runtime.opMu.Unlock()
	if err := runtime.runWithSession(ctx, created, withSession); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	return extensions.SessionReplacementResult{}, nil
}

// Fork replaces the active session with a branch rooted before or at entryID.
func (runtime *AgentSessionRuntime) Fork(
	ctx context.Context,
	entryID string,
	options *extensions.ForkOptions,
) (AgentSessionRuntimeForkResult, error) {
	if runtime == nil {
		return AgentSessionRuntimeForkResult{}, errors.New("codingagent: nil agent session runtime")
	}
	runtime.opMu.Lock()
	locked := true
	defer func() {
		if locked {
			runtime.opMu.Unlock()
		}
	}()
	ctx = runtimeContext(ctx)
	current, err := runtime.current()
	if err != nil {
		return AgentSessionRuntimeForkResult{}, err
	}
	position := extensions.ForkBefore
	if options != nil && options.Position != "" {
		position = options.Position
	}
	if runtimeForkCancelled(ctx, current, extensions.SessionBeforeForkEvent{EntryID: entryID, Position: position}) {
		return AgentSessionRuntimeForkResult{Cancelled: true}, nil
	}
	manager := current.Manager()
	selected := manager.GetEntry(entryID)
	if selected == nil {
		return AgentSessionRuntimeForkResult{}, errors.New("Invalid entry ID for forking") //nolint:staticcheck // Upstream text.
	}
	targetID := selected.ID
	var selectedText *string
	if position != extensions.ForkAt {
		role, text := messageRoleAndText(selected.Message)
		if selected.Type != "message" || role != "user" {
			return AgentSessionRuntimeForkResult{}, errors.New("Invalid entry ID for forking") //nolint:staticcheck // Upstream text.
		}
		selectedText = &text
		if selected.ParentID == nil {
			targetID = ""
		} else {
			targetID = *selected.ParentID
		}
	}

	var replacement *sessionstore.SessionManager
	if manager.IsPersisted() {
		currentFile := manager.GetSessionFile()
		if currentFile == "" {
			return AgentSessionRuntimeForkResult{}, errors.New("Persisted session is missing a session file") //nolint:staticcheck // Upstream text.
		}
		if targetID == "" {
			replacement, err = sessionstore.Create(manager.GetCWD(), manager.GetSessionDir())
			if err == nil {
				parent := currentFile
				_, err = replacement.NewSession(sessionstore.NewSessionOptions{ParentSession: &parent})
			}
		} else {
			if _, statErr := os.Stat(currentFile); statErr != nil {
				return AgentSessionRuntimeForkResult{}, errors.New("This session has not been saved yet. Wait for the first assistant response before cloning or forking it.") //nolint:staticcheck // Upstream text.
			}
			replacement, err = sessionstore.Open(currentFile, manager.GetSessionDir())
			if err == nil {
				var forked string
				forked, err = replacement.CreateBranchedSession(targetID)
				if err == nil && forked == "" {
					err = errors.New("Failed to create forked session") //nolint:staticcheck // Upstream text.
				}
			}
		}
	} else {
		replacement = manager
		if targetID == "" {
			_, err = replacement.NewSession()
		} else {
			_, err = replacement.CreateBranchedSession(targetID)
		}
	}
	if err != nil {
		return AgentSessionRuntimeForkResult{}, err
	}
	created, err := runtime.replace(ctx, current, replacement, extensions.SessionShutdownFork, extensions.SessionStartFork, nil)
	if err != nil {
		return AgentSessionRuntimeForkResult{}, err
	}
	var withSession func(context.Context, extensions.ReplacedSessionContext) error
	if options != nil {
		withSession = options.WithSession
	}
	if err := runtime.rebindReplacement(created); err != nil {
		return AgentSessionRuntimeForkResult{}, err
	}
	locked = false
	runtime.opMu.Unlock()
	if err := runtime.runWithSession(ctx, created, withSession); err != nil {
		return AgentSessionRuntimeForkResult{}, err
	}
	return AgentSessionRuntimeForkResult{SelectedText: selectedText}, nil
}

// ImportFromJSONL copies a session JSONL file into the active session directory
// and resumes it.
func (runtime *AgentSessionRuntime) ImportFromJSONL(
	ctx context.Context,
	inputPath string,
	cwdOverride string,
) (extensions.SessionReplacementResult, error) {
	if runtime == nil {
		return extensions.SessionReplacementResult{}, errors.New("codingagent: nil agent session runtime")
	}
	runtime.opMu.Lock()
	locked := true
	defer func() {
		if locked {
			runtime.opMu.Unlock()
		}
	}()
	ctx = runtimeContext(ctx)
	current, err := runtime.current()
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	resolvedPath, err := config.NormalizePath(inputPath)
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	resolvedPath, err = filepath.Abs(resolvedPath)
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	if _, err := os.Stat(resolvedPath); err != nil {
		if os.IsNotExist(err) {
			return extensions.SessionReplacementResult{}, &SessionImportFileNotFoundError{FilePath: resolvedPath}
		}
		return extensions.SessionReplacementResult{}, err
	}
	sessionDir := current.Manager().GetSessionDir()
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	destination := filepath.Join(sessionDir, filepath.Base(resolvedPath))
	target := destination
	if runtimeSwitchCancelled(ctx, current, extensions.SessionBeforeSwitchEvent{
		Reason: extensions.SessionSwitchResume, TargetSessionFile: &target,
	}) {
		return extensions.SessionReplacementResult{Cancelled: true}, nil
	}
	if filepath.Clean(destination) != filepath.Clean(resolvedPath) {
		if err := copyRuntimeSessionFile(resolvedPath, destination); err != nil {
			return extensions.SessionReplacementResult{}, err
		}
	}
	var openOptions []sessionstore.Option
	if cwdOverride != "" {
		openOptions = append(openOptions, sessionstore.WithCwdOverride(cwdOverride))
	}
	replacement, err := sessionstore.Open(destination, sessionDir, openOptions...)
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	if err := assertRuntimeSessionCWD(replacement, current.Manager().GetCWD()); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	created, err := runtime.replace(ctx, current, replacement, extensions.SessionShutdownResume, extensions.SessionStartResume, nil)
	if err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	if err := runtime.rebindReplacement(created); err != nil {
		return extensions.SessionReplacementResult{}, err
	}
	return extensions.SessionReplacementResult{}, nil
}

// Dispose emits the quit lifecycle event and tears down the active session.
func (runtime *AgentSessionRuntime) Dispose(ctx context.Context) {
	if runtime == nil {
		return
	}
	runtime.opMu.Lock()
	defer runtime.opMu.Unlock()
	current, err := runtime.current()
	if err != nil {
		return
	}
	runtime.mu.Lock()
	runtime.disposed = true
	runtime.mu.Unlock()
	runtime.teardown(runtimeContext(ctx), current, extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
}

func (runtime *AgentSessionRuntime) current() (*AgentSession, error) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.disposed || runtime.session == nil {
		return nil, errors.New("codingagent: agent session runtime is disposed")
	}
	return runtime.session, nil
}

func (runtime *AgentSessionRuntime) replace(
	ctx context.Context,
	current *AgentSession,
	replacement *sessionstore.SessionManager,
	shutdownReason extensions.SessionShutdownReason,
	startReason extensions.SessionStartReason,
	configure func(*AgentSessionOptions),
) (*AgentSession, error) {
	previousFile := current.Manager().GetSessionFile()
	targetFile := replacement.GetSessionFile()
	runtime.teardown(ctx, current, extensions.SessionShutdownEvent{
		Reason: shutdownReason, TargetSessionFile: optionalRuntimeString(targetFile),
	})
	nextOptions := runtime.options
	nextOptions.CWD = replacement.GetCWD()
	nextOptions.SessionManager = replacement
	nextOptions.DeferExtensionStart = true
	nextOptions.ProjectTrustContext = nil
	if configure != nil {
		configure(&nextOptions)
	}
	freshRegistry, err := nextOptions.ExtensionRegistry.Fresh(nextOptions.CWD)
	if err != nil {
		return nil, err
	}
	nextOptions.ExtensionRegistry = freshRegistry
	nextOptions.SessionStartEvent = &extensions.SessionStartEvent{
		Reason: startReason, PreviousSessionFile: optionalRuntimeString(previousFile),
	}
	result, err := runtime.create(ctx, nextOptions)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Session == nil {
		return nil, errors.New("codingagent: session runtime factory returned no session")
	}
	runtime.mu.Lock()
	runtime.session = result.Session
	runtime.result = result
	runtime.options = nextOptions
	runtime.mu.Unlock()
	runtime.bindSessionCommands(result.Session)
	return result.Session, nil
}

func (runtime *AgentSessionRuntime) rebindReplacement(created *AgentSession) error {
	runtime.mu.RLock()
	rebind := runtime.rebind
	runtime.mu.RUnlock()
	if rebind != nil {
		return rebind(created)
	}
	return nil
}

func (runtime *AgentSessionRuntime) runWithSession(
	ctx context.Context,
	created *AgentSession,
	withSession func(context.Context, extensions.ReplacedSessionContext) error,
) error {
	if withSession == nil {
		return nil
	}
	runner := created.ExtensionRunner()
	if runner == nil {
		return errors.New("codingagent: replacement session has no extension context")
	}
	return withSession(ctx, runner.CreateReplacedSessionContext())
}

func (runtime *AgentSessionRuntime) bindSessionCommands(created *AgentSession) {
	if created == nil || created.ExtensionRunner() == nil {
		return
	}
	created.setReloadLifecycle(
		func() error {
			runtime.opMu.Lock()
			current, err := runtime.current()
			if err != nil {
				runtime.opMu.Unlock()
				return err
			}
			if current != created {
				runtime.opMu.Unlock()
				return errors.New("codingagent: cannot reload a replaced session")
			}
			return nil
		},
		func() error {
			if err := runtime.refreshReloadResult(created); err != nil {
				return err
			}
			runtime.bindSessionCommands(created)
			return nil
		},
		runtime.opMu.Unlock,
	)
	created.ExtensionRunner().BindCommandContext(&extensions.CommandActions{
		WaitForIdle: created.WaitForIdle,
		NewSession:  runtime.NewSession,
		Fork: func(ctx context.Context, entryID string, options *extensions.ForkOptions) (extensions.SessionReplacementResult, error) {
			result, err := runtime.Fork(ctx, entryID, options)
			return extensions.SessionReplacementResult{Cancelled: result.Cancelled}, err
		},
		NavigateTree: func(ctx context.Context, targetID string, options *extensions.NavigateTreeOptions) (extensions.SessionReplacementResult, error) {
			resolved := NavigateTreeOptions{}
			if options != nil {
				resolved = NavigateTreeOptions{
					Summarize: options.Summarize, CustomInstructions: options.CustomInstructions,
					ReplaceInstructions: options.ReplaceInstructions, Label: options.Label,
				}
			}
			result, err := created.NavigateTree(ctx, targetID, resolved)
			return extensions.SessionReplacementResult{Cancelled: result.Cancelled || result.Aborted}, err
		},
		SwitchSession: func(ctx context.Context, path string, options *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error) {
			resolved := &AgentSessionRuntimeSwitchOptions{}
			if options != nil {
				resolved.WithSession = options.WithSession
			}
			return runtime.SwitchSession(ctx, path, resolved)
		},
		Reload: func(ctx context.Context) error {
			return created.Reload(ctx)
		},
	})
}

func (runtime *AgentSessionRuntime) refreshReloadResult(created *AgentSession) error {
	if runtime == nil || created == nil || created.extensionState == nil {
		return nil
	}
	created.extensionState.mu.Lock()
	registry := created.extensionState.config.ExtensionRegistry
	created.extensionState.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.session != created {
		return errors.New("codingagent: cannot publish reload from a replaced session")
	}
	runtime.options.ExtensionRegistry = registry
	if runtime.result != nil {
		runtime.result.ExtensionRegistry = registry
		if runtime.result.Services != nil {
			runtime.result.Services.ExtensionRegistry = registry
		}
	}
	return nil
}

func (runtime *AgentSessionRuntime) teardown(
	ctx context.Context,
	current *AgentSession,
	event extensions.SessionShutdownEvent,
) {
	runner := current.ExtensionRunner()
	extensions.EmitSessionShutdown(ctx, runner, event)
	runtime.mu.RLock()
	beforeInvalidate := runtime.beforeInvalidate
	runtime.mu.RUnlock()
	if beforeInvalidate != nil {
		beforeInvalidate()
	}
	current.disposeAfterExtensionShutdown()
}

func runtimeSwitchCancelled(ctx context.Context, current *AgentSession, event extensions.SessionBeforeSwitchEvent) bool {
	runner := current.ExtensionRunner()
	if runner == nil || !runner.HasHandlers(extensions.EventSessionBeforeSwitch) {
		return false
	}
	result := runner.Emit(ctx, event)
	switch typed := result.(type) {
	case extensions.SessionBeforeSwitchResult:
		return typed.Cancel
	case *extensions.SessionBeforeSwitchResult:
		return typed != nil && typed.Cancel
	default:
		return false
	}
}

func runtimeForkCancelled(ctx context.Context, current *AgentSession, event extensions.SessionBeforeForkEvent) bool {
	runner := current.ExtensionRunner()
	if runner == nil || !runner.HasHandlers(extensions.EventSessionBeforeFork) {
		return false
	}
	result := runner.Emit(ctx, event)
	switch typed := result.(type) {
	case extensions.SessionBeforeForkResult:
		return typed.Cancel
	case *extensions.SessionBeforeForkResult:
		return typed != nil && typed.Cancel
	default:
		return false
	}
}

func assertRuntimeSessionCWD(manager *sessionstore.SessionManager, fallbackCWD string) error {
	if manager == nil || manager.GetSessionFile() == "" {
		return nil
	}
	cwd := manager.GetCWD()
	if cwd == "" {
		return nil
	}
	if _, err := os.Stat(cwd); err == nil {
		return nil
	}
	return &MissingSessionCWDError{
		SessionFile: manager.GetSessionFile(), SessionCWD: cwd, FallbackCWD: fallbackCWD,
	}
}

func optionalRuntimeString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func copyRuntimeSessionFile(source, destination string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(destination, contents, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(destination, info.Mode().Perm())
}

func runtimeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
