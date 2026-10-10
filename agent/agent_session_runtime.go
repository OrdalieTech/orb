package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// CreateAgentSessionRuntimeFactory recreates a session after new, resume,
// fork, and import operations. A nil factory uses [NewAgentSession]. Its
// options carry fresh instances of the extension registry the runtime was
// created with, if any; a factory loading its own extensions loads them again.
type CreateAgentSessionRuntimeFactory func(context.Context, AgentSessionOptions) (*AgentSessionResult, error)

// AgentSessionRuntime owns the active [AgentSession] and replaces it for
// session lifecycle operations.
type AgentSessionRuntime struct {
	sessionObservers map[uint64]func(*AgentSession)
	nextObserver     uint64
	control          atomic.Pointer[SessionControl]
	opMu             sync.Mutex
	mu               sync.RWMutex

	session           *AgentSession
	result            *AgentSessionResult
	options           AgentSessionOptions
	create            CreateAgentSessionRuntimeFactory
	rebind            func(*AgentSession) error
	beforeInvalidate  func()
	claimSession      func(*sessionstore.SessionManager) (func(), error)
	afterSessionStart func(*AgentSession) error
	reload            func(context.Context) error
	disposed          bool
}

// AgentSessionRuntimeSwitchOptions configures [AgentSessionRuntime.SwitchSession].
type AgentSessionRuntimeSwitchOptions struct {
	CWDOverride string
	// Repo opens the session when the active one has no repository of its own.
	Repo                       harness.SessionRepo
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
		if err := AssertSessionCWD(options.SessionManager, fallback); err != nil {
			return nil, err
		}
	}
	result, err := create(runtimeContext(ctx), options)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Session == nil {
		return nil, errors.New("agent: session runtime factory returned no session")
	}
	if err := AssertSessionCWD(result.Session.Manager(), options.CWD); err != nil {
		result.Session.Dispose()
		return nil, err
	}
	runtime := &AgentSessionRuntime{session: result.Session, result: result, options: options, create: create}
	runtime.bindSessionCommands(result.Session)
	ReleaseMemory()
	return runtime, nil
}

// ReleaseMemory collects in the background and hands what is free back to the
// system. Reading a session leaves a few times its size behind as garbage,
// which an idle process would otherwise hold until the runtime's next forced
// cycle, minutes later.
func ReleaseMemory() {
	go debug.FreeOSMemory()
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

// SetSessionClaim installs host-owned session admission before abort or teardown.
// The returned release function retires the previous ownership after replacement.
func (runtime *AgentSessionRuntime) SetSessionClaim(claim func(*sessionstore.SessionManager) (func(), error)) {
	runtime.mu.Lock()
	runtime.claimSession = claim
	runtime.mu.Unlock()
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

// SetAfterSessionStart sets the callback run after a replacement's deferred
// session_start, once the replacement is committed and its lock released.
func (runtime *AgentSessionRuntime) SetAfterSessionStart(callback func(*AgentSession) error) {
	runtime.mu.Lock()
	runtime.afterSessionStart = callback
	runtime.mu.Unlock()
}

// SetReload replaces what an extension's ctx.reload() runs, for hosts whose
// reload rebuilds the session (see [AgentSessionRuntime.Rebuild]).
func (runtime *AgentSessionRuntime) SetReload(reload func(context.Context) error) {
	runtime.mu.Lock()
	runtime.reload = reload
	runtime.mu.Unlock()
}

// transition runs one replacement under the control guard and the operation
// lock. The replacement's session_start, the after-start callback and
// withSession run once both are released, so they may call back into the
// runtime. replace returns no session when the operation was cancelled.
func (runtime *AgentSessionRuntime) transition(
	ctx context.Context,
	withSession func(context.Context, extensions.ReplacedSessionContext) error,
	replace func(context.Context, *AgentSession) (*AgentSession, error),
) error {
	if runtime == nil {
		return errors.New("agent: nil agent session runtime")
	}
	ctx = runtimeContext(ctx)
	finishControl, err := runtime.control.Load().beginTransition(ctx)
	if err != nil {
		return err
	}
	created, err := func() (*AgentSession, error) {
		runtime.opMu.Lock()
		defer runtime.opMu.Unlock()
		defer finishControl()
		current, err := runtime.current()
		if err != nil {
			return nil, err
		}
		created, err := replace(ctx, current)
		if err != nil || created == nil {
			return nil, err
		}
		return created, runtime.rebindReplacement(created)
	}()
	if err != nil || created == nil {
		return err
	}
	if err := created.BindExtensions(ctx); err != nil {
		return err
	}
	runtime.mu.RLock()
	afterSessionStart := runtime.afterSessionStart
	runtime.mu.RUnlock()
	if afterSessionStart != nil {
		if err := afterSessionStart(created); err != nil {
			return err
		}
	}
	if withSession == nil {
		return nil
	}
	runner := created.ExtensionRunner()
	if runner == nil {
		return errors.New("agent: replacement session has no extension context")
	}
	return withSession(ctx, runner.CreateReplacedSessionContext())
}

// NewSession replaces the active session with a fresh persisted or in-memory session.
func (runtime *AgentSessionRuntime) NewSession(
	ctx context.Context,
	options *extensions.NewSessionOptions,
) (extensions.SessionReplacementResult, error) {
	if options == nil {
		options = &extensions.NewSessionOptions{}
	}
	cancelled := false
	err := runtime.transition(ctx, options.WithSession, func(ctx context.Context, current *AgentSession) (*AgentSession, error) {
		if runtimeSwitchCancelled(ctx, current, extensions.SessionBeforeSwitchEvent{Reason: extensions.SessionSwitchNew}) {
			cancelled = true
			return nil, nil
		}
		manager := current.Manager()
		var replacement *sessionstore.SessionManager
		var err error
		if repo := manager.HarnessRepo(); repo != nil {
			create := harness.SessionCreateOptions{CWD: manager.GetCWD()}
			if options.ParentSession != "" {
				parent := options.ParentSession
				create.ParentSessionPath = &parent
			}
			createdSession, createErr := repo.Create(ctx, create)
			if createErr != nil {
				return nil, createErr
			}
			replacement, err = sessionstore.FromHarnessStorage(
				createdSession.Storage(), sessionstore.WithHarnessRepo(repo), sessionstore.WithCwdOverride(manager.GetCWD()),
			)
		} else if manager.IsHarnessBacked() {
			return nil, fmt.Errorf("%w: new session", sessionstore.ErrHarnessStorageReplacement)
		} else if manager.IsPersisted() {
			replacement, err = sessionstore.Create(manager.GetCWD(), manager.GetSessionDir())
		} else {
			replacement, err = sessionstore.InMemory(manager.GetCWD())
		}
		if err != nil {
			return nil, err
		}
		if manager.HarnessRepo() == nil && options.ParentSession != "" {
			parent := options.ParentSession
			if _, err := replacement.NewSession(sessionstore.NewSessionOptions{ParentSession: &parent}); err != nil {
				return nil, err
			}
		}
		if current.Agent().UsesSessionLoop() {
			if model := current.State().Model; model != nil {
				if _, err := replacement.AppendModelChange(string(model.Provider), model.ID); err != nil {
					return nil, err
				}
			}
		}
		if options.Prepare != nil {
			if err := options.Prepare(replacement); err != nil {
				return nil, err
			}
		}
		created, err := runtime.replace(ctx, current, replacement, extensions.SessionShutdownNew, extensions.SessionStartNew, nil)
		if err != nil {
			return nil, err
		}
		if options.Setup != nil {
			if err := options.Setup(replacement); err != nil {
				return nil, err
			}
			created.RefreshContext()
		}
		return created, nil
	})
	return extensions.SessionReplacementResult{Cancelled: cancelled}, err
}

// SwitchSession resumes a JSONL session and replaces the active session.
func (runtime *AgentSessionRuntime) SwitchSession(
	ctx context.Context,
	path string,
	options *AgentSessionRuntimeSwitchOptions,
) (extensions.SessionReplacementResult, error) {
	if options == nil {
		options = &AgentSessionRuntimeSwitchOptions{}
	}
	cancelled := false
	err := runtime.transition(ctx, options.WithSession, func(ctx context.Context, current *AgentSession) (*AgentSession, error) {
		target := path
		if runtimeSwitchCancelled(ctx, current, extensions.SessionBeforeSwitchEvent{
			Reason: extensions.SessionSwitchResume, TargetSessionFile: &target,
		}) {
			cancelled = true
			return nil, nil
		}
		var openOptions []sessionstore.Option
		if options.CWDOverride != "" {
			openOptions = append(openOptions, sessionstore.WithCwdOverride(options.CWDOverride))
		}
		manager := current.Manager()
		var replacement *sessionstore.SessionManager
		var err error
		repo := manager.HarnessRepo()
		if repo == nil {
			repo = options.Repo
		}
		if repo != nil {
			var opened *harness.Session
			if opener, ok := repo.(harnessRuntimePathOpener); ok {
				resolvedPath, resolveErr := config.NormalizePath(path)
				if resolveErr == nil {
					resolvedPath, resolveErr = filepath.Abs(resolvedPath)
				}
				fallbackCWD := options.CWDOverride
				if fallbackCWD == "" && resolveErr == nil {
					fallbackCWD, resolveErr = os.Getwd()
				}
				if resolveErr == nil {
					if _, statErr := os.Stat(resolvedPath); statErr == nil {
						var prepared *sessionstore.SessionManager
						prepared, resolveErr = sessionstore.Open(resolvedPath, "", openOptions...)
						if resolveErr == nil {
							fallbackCWD = prepared.GetCWD()
						}
					} else if !os.IsNotExist(statErr) {
						resolveErr = statErr
					}
				}
				if resolveErr != nil {
					return nil, resolveErr
				}
				opened, err = opener.OpenRuntimePath(ctx, resolvedPath, fallbackCWD)
			} else if opener, ok := repo.(interface {
				OpenPath(context.Context, string) (*harness.Session, error)
			}); ok {
				opened, err = opener.OpenPath(ctx, path)
			} else {
				var metadata harness.SessionMetadata
				metadata, err = findHarnessSessionMetadata(ctx, repo, path)
				if err == nil {
					opened, err = repo.Open(ctx, metadata)
				}
			}
			if err != nil {
				return nil, err
			}
			openOptions = append(openOptions, sessionstore.WithHarnessRepo(repo))
			if options.CWDOverride == "" {
				openOptions = append(openOptions, sessionstore.WithCwdOverride(cmp.Or(opened.Metadata().CWD, manager.GetCWD())))
			}
			replacement, err = sessionstore.FromHarnessStorage(opened.Storage(), openOptions...)
		} else if manager.IsHarnessBacked() {
			return nil, fmt.Errorf("%w: switch session", sessionstore.ErrHarnessStorageReplacement)
		} else {
			replacement, err = sessionstore.Open(path, "", openOptions...)
		}
		if err != nil {
			return nil, err
		}
		if err := AssertSessionCWD(replacement, manager.GetCWD()); err != nil {
			return nil, err
		}
		var configure func(*AgentSessionOptions)
		if options.ProjectTrustContextFactory != nil {
			trustContext := options.ProjectTrustContextFactory(replacement.GetCWD())
			configure = func(next *AgentSessionOptions) { next.ProjectTrustContext = trustContext }
		}
		return runtime.replace(ctx, current, replacement, extensions.SessionShutdownResume, extensions.SessionStartResume, configure)
	})
	return extensions.SessionReplacementResult{Cancelled: cancelled}, err
}

// Fork replaces the active session with a branch rooted before or at entryID.
func (runtime *AgentSessionRuntime) Fork(
	ctx context.Context,
	entryID string,
	options *extensions.ForkOptions,
) (AgentSessionRuntimeForkResult, error) {
	if options == nil {
		options = &extensions.ForkOptions{}
	}
	position := extensions.ForkBefore
	if options.Position != "" {
		position = options.Position
	}
	var result AgentSessionRuntimeForkResult
	err := runtime.transition(ctx, options.WithSession, func(ctx context.Context, current *AgentSession) (*AgentSession, error) {
		if runtimeForkCancelled(ctx, current, extensions.SessionBeforeForkEvent{EntryID: entryID, Position: position}) {
			result.Cancelled = true
			return nil, nil
		}
		manager := current.Manager()
		selected := manager.GetEntry(entryID)
		if selected == nil {
			return nil, errors.New("Invalid entry ID for forking") //nolint:staticcheck // Upstream text.
		}
		targetID := selected.ID
		if position != extensions.ForkAt {
			role, text := jsonwire.MessageRoleAndText(selected.Message)
			if selected.Type != "message" || role != "user" {
				return nil, errors.New("Invalid entry ID for forking") //nolint:staticcheck // Upstream text.
			}
			result.SelectedText = &text
			if selected.ParentID == nil {
				targetID = ""
			} else {
				targetID = *selected.ParentID
			}
		}

		var replacement *sessionstore.SessionManager
		var err error
		if repo := manager.HarnessRepo(); repo != nil {
			if opener, ok := repo.(harnessRuntimePathOpener); ok {
				replacement, err = forkHarnessRuntimeSession(ctx, manager, repo, opener, targetID)
			} else {
				metadata, ok := manager.HarnessMetadata()
				if !ok {
					return nil, fmt.Errorf("%w: fork session metadata", sessionstore.ErrHarnessStorageReplacement)
				}
				harnessPosition := harness.ForkBefore
				if position == extensions.ForkAt {
					harnessPosition = harness.ForkAt
				}
				forkedSession, forkErr := repo.Fork(ctx, metadata, harness.SessionForkOptions{
					SessionCreateOptions: harness.SessionCreateOptions{CWD: manager.GetCWD()},
					EntryID:              entryID,
					Position:             harnessPosition,
				})
				if forkErr != nil {
					return nil, forkErr
				}
				replacement, err = sessionstore.FromHarnessStorage(
					forkedSession.Storage(), sessionstore.WithHarnessRepo(repo), sessionstore.WithCwdOverride(manager.GetCWD()),
				)
			}
		} else if manager.IsHarnessBacked() {
			return nil, fmt.Errorf("%w: fork session", sessionstore.ErrHarnessStorageReplacement)
		} else if manager.IsPersisted() {
			replacement, err = forkPersistedSession(manager, targetID)
		} else {
			replacement = manager
			if targetID == "" {
				_, err = replacement.NewSession()
			} else {
				_, err = replacement.CreateBranchedSession(targetID)
			}
		}
		if err != nil {
			return nil, err
		}
		return runtime.replace(ctx, current, replacement, extensions.SessionShutdownFork, extensions.SessionStartFork, nil)
	})
	if err != nil || result.Cancelled {
		return AgentSessionRuntimeForkResult{Cancelled: result.Cancelled}, err
	}
	return result, nil
}

// Rebuild recreates the active session on its own manager through the
// factory, so every cwd-bound service is read again, inside the reload
// lifecycle.
func (runtime *AgentSessionRuntime) Rebuild(ctx context.Context) error {
	return runtime.transition(ctx, nil, func(ctx context.Context, current *AgentSession) (*AgentSession, error) {
		return runtime.replace(ctx, current, current.Manager(), extensions.SessionShutdownReload, extensions.SessionStartReload, nil)
	})
}

// ImportFromJSONL copies a session JSONL file into the active session directory
// and resumes it.
func (runtime *AgentSessionRuntime) ImportFromJSONL(
	ctx context.Context,
	inputPath string,
	cwdOverride string,
) (extensions.SessionReplacementResult, error) {
	cancelled := false
	err := runtime.transition(ctx, nil, func(ctx context.Context, current *AgentSession) (*AgentSession, error) {
		resolvedPath, err := config.NormalizePath(inputPath)
		if err != nil {
			return nil, err
		}
		resolvedPath, err = filepath.Abs(resolvedPath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(resolvedPath); err != nil {
			if os.IsNotExist(err) {
				return nil, &SessionImportFileNotFoundError{FilePath: resolvedPath}
			}
			return nil, err
		}
		manager := current.Manager()
		sessionDir := manager.GetSessionDir()
		if services := runtime.Services(); sessionDir == "" && !manager.IsHarnessBacked() && services != nil && services.AgentDir != "" {
			// An in-memory session imports into its cwd's default session directory.
			if sessionDir, err = sessionstore.DefaultSessionDir(manager.GetCWD(), services.AgentDir); err != nil {
				return nil, err
			}
		}
		destination := resolvedPath
		if sessionDir != "" {
			if err := os.MkdirAll(sessionDir, 0o755); err != nil {
				return nil, err
			}
			destination = filepath.Join(sessionDir, filepath.Base(resolvedPath))
		}
		target := destination
		if runtimeSwitchCancelled(ctx, current, extensions.SessionBeforeSwitchEvent{
			Reason: extensions.SessionSwitchResume, TargetSessionFile: &target,
		}) {
			cancelled = true
			return nil, nil
		}
		var openOptions []sessionstore.Option
		if cwdOverride != "" {
			openOptions = append(openOptions, sessionstore.WithCwdOverride(cwdOverride))
		}
		var replacement *sessionstore.SessionManager
		if manager.IsHarnessBacked() {
			repo := manager.HarnessRepo()
			if repo == nil {
				return nil, fmt.Errorf("%w: import session", sessionstore.ErrHarnessStorageReplacement)
			}
			var imported *harness.Session
			if opener, ok := repo.(harnessRuntimePathOpener); ok {
				if filepath.Clean(destination) != filepath.Clean(resolvedPath) {
					if err := copyRuntimeSessionFile(resolvedPath, destination); err != nil {
						return nil, err
					}
				}
				prepared, err := sessionstore.Open(destination, sessionDir, openOptions...)
				if err != nil {
					return nil, err
				}
				if imported, err = opener.OpenRuntimePath(ctx, destination, prepared.GetCWD()); err != nil {
					return nil, err
				}
			} else {
				content, err := os.ReadFile(resolvedPath)
				if err != nil {
					if os.IsNotExist(err) {
						return nil, &SessionImportFileNotFoundError{FilePath: resolvedPath}
					}
					return nil, err
				}
				if imported, err = importHarnessRuntimeSession(ctx, repo, content, resolvedPath, destination); err != nil {
					return nil, err
				}
			}
			importedCWD := cmp.Or(cwdOverride, imported.Metadata().CWD, manager.GetCWD())
			if replacement, err = sessionstore.FromHarnessStorage(
				imported.Storage(), sessionstore.WithHarnessRepo(repo), sessionstore.WithCwdOverride(importedCWD),
			); err != nil {
				return nil, err
			}
		} else {
			if filepath.Clean(destination) != filepath.Clean(resolvedPath) {
				if err := copyRuntimeSessionFile(resolvedPath, destination); err != nil {
					return nil, err
				}
			}
			if replacement, err = sessionstore.Open(destination, sessionDir, openOptions...); err != nil {
				return nil, err
			}
		}
		if err := AssertSessionCWD(replacement, manager.GetCWD()); err != nil {
			return nil, err
		}
		return runtime.replace(ctx, current, replacement, extensions.SessionShutdownResume, extensions.SessionStartResume, nil)
	})
	return extensions.SessionReplacementResult{Cancelled: cancelled}, err
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
		return nil, errors.New("agent: agent session runtime is disposed")
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
	runtime.mu.RLock()
	claim := runtime.claimSession
	runtime.mu.RUnlock()
	if claim != nil {
		release, err := claim(replacement)
		if err != nil {
			return nil, err
		}
		if release != nil {
			defer release()
		}
	}
	previousFile, targetFile := "", ""
	// A reload stays on its session, so its lifecycle names no files.
	if shutdownReason != extensions.SessionShutdownReload {
		previousFile, targetFile = current.Manager().GetSessionFile(), replacement.GetSessionFile()
	}
	// Persist an active turn's aborted tool results before replacing its manager.
	current.Abort()
	_ = current.WaitForIdle(context.Background())
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
	if nextOptions.ExtensionRegistry != nil {
		freshRegistry, err := nextOptions.ExtensionRegistry.Fresh(nextOptions.CWD)
		if err != nil {
			return nil, err
		}
		nextOptions.ExtensionRegistry = freshRegistry
	}
	nextOptions.SessionStartEvent = &extensions.SessionStartEvent{
		Reason: startReason, PreviousSessionFile: optionalRuntimeString(previousFile),
	}
	result, err := runtime.create(ctx, nextOptions)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Session == nil {
		return nil, errors.New("agent: session runtime factory returned no session")
	}
	runtime.mu.Lock()
	runtime.session = result.Session
	runtime.result = result
	runtime.options = nextOptions
	runtime.mu.Unlock()
	runtime.bindSessionCommands(result.Session)
	ReleaseMemory()
	return result.Session, nil
}

func (runtime *AgentSessionRuntime) rebindReplacement(created *AgentSession) error {
	runtime.mu.RLock()
	observers := make([]func(*AgentSession), 0, len(runtime.sessionObservers))
	for _, observe := range runtime.sessionObservers {
		observers = append(observers, observe)
	}
	runtime.mu.RUnlock()
	for _, observe := range observers {
		observe(created)
	}
	runtime.mu.RLock()
	rebind := runtime.rebind
	runtime.mu.RUnlock()
	if rebind != nil {
		return rebind(created)
	}
	return nil
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
				return errors.New("agent: cannot reload a replaced session")
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
			runtime.mu.RLock()
			reload := runtime.reload
			runtime.mu.RUnlock()
			if reload != nil {
				return reload(ctx)
			}
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
		return errors.New("agent: cannot publish reload from a replaced session")
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

// AssertSessionCWD reports a persisted session whose working directory is
// gone as a [MissingSessionCWDError] naming fallbackCWD.
func AssertSessionCWD(manager *sessionstore.SessionManager, fallbackCWD string) error {
	if manager == nil || !manager.IsPersisted() {
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

type harnessRuntimePathOpener interface {
	OpenRuntimePath(context.Context, string, string) (*harness.Session, error)
}

type harnessRuntimeBytesOpener interface {
	OpenRuntimeBytes(context.Context, string, []byte) (*harness.Session, error)
}

// forkPersistedSession branches a JSONL session file: a new child session
// when targetID is empty, else a copy of the branch up to targetID.
func forkPersistedSession(current *sessionstore.SessionManager, targetID string) (*sessionstore.SessionManager, error) {
	currentFile := current.GetSessionFile()
	if currentFile == "" {
		return nil, errors.New("Persisted session is missing a session file") //nolint:staticcheck // Upstream text.
	}
	if targetID == "" {
		forked, err := sessionstore.Create(current.GetCWD(), current.GetSessionDir())
		if err == nil {
			parent := currentFile
			_, err = forked.NewSession(sessionstore.NewSessionOptions{ParentSession: &parent})
		}
		return forked, err
	}
	if _, err := os.Stat(currentFile); err != nil {
		return nil, errors.New("This session has not been saved yet. Wait for the first assistant response before cloning or forking it.") //nolint:staticcheck // Upstream text.
	}
	forked, err := sessionstore.Open(currentFile, current.GetSessionDir())
	if err != nil {
		return nil, err
	}
	if path, err := forked.CreateBranchedSession(targetID); err != nil || path == "" {
		return nil, cmp.Or(err, errors.New("Failed to create forked session")) //nolint:staticcheck // Upstream text.
	}
	return forked, nil
}

func forkHarnessRuntimeSession(
	ctx context.Context,
	current *sessionstore.SessionManager,
	repo harness.SessionRepo,
	opener harnessRuntimePathOpener,
	targetID string,
) (*sessionstore.SessionManager, error) {
	native, err := forkPersistedSession(current, targetID)
	if err != nil {
		return nil, err
	}
	forkedPath := native.GetSessionFile()
	var forked *harness.Session
	if _, statErr := os.Stat(forkedPath); statErr == nil {
		forked, err = opener.OpenRuntimePath(ctx, forkedPath, native.GetCWD())
	} else if !os.IsNotExist(statErr) {
		err = statErr
	} else {
		bytesOpener, ok := repo.(harnessRuntimeBytesOpener)
		if !ok {
			return nil, fmt.Errorf("%w: open unmaterialized fork", sessionstore.ErrHarnessStorageReplacement)
		}
		var content []byte
		content, err = native.JSONL()
		if err == nil {
			forked, err = bytesOpener.OpenRuntimeBytes(ctx, forkedPath, content)
		}
	}
	if err != nil {
		return nil, err
	}
	return sessionstore.FromHarnessStorage(
		forked.Storage(), sessionstore.WithHarnessRepo(repo), sessionstore.WithCwdOverride(native.GetCWD()),
	)
}

func findHarnessSessionMetadata(
	ctx context.Context,
	repo harness.SessionRepo,
	target string,
) (harness.SessionMetadata, error) {
	metadata, err := repo.List(ctx, harness.SessionListOptions{})
	if err != nil {
		return harness.SessionMetadata{}, err
	}
	for _, candidate := range metadata {
		if candidate.ID == target || candidate.Path == target {
			return candidate, nil
		}
		if candidate.Path != "" && filepath.Clean(candidate.Path) == filepath.Clean(target) {
			return candidate, nil
		}
	}
	return harness.SessionMetadata{}, fmt.Errorf("Session not found: %s", target) //nolint:staticcheck // Upstream text.
}

func importHarnessRuntimeSession(
	ctx context.Context,
	repo harness.SessionRepo,
	content []byte,
	sourcePath string,
	destinationPath string,
) (*harness.Session, error) {
	if importer, ok := repo.(interface {
		ImportJSONLAt(context.Context, []byte, string) (*harness.Session, error)
	}); ok {
		return importer.ImportJSONLAt(ctx, content, destinationPath)
	}
	if importer, ok := repo.(interface {
		ImportJSONL(context.Context, []byte) (*harness.Session, error)
	}); ok {
		return importer.ImportJSONL(ctx, content)
	}
	parsed, err := harness.RehydrateJSONLSession(content, sourcePath)
	if err != nil {
		return nil, err
	}
	metadata := parsed.Metadata()
	created, err := repo.Create(ctx, harness.SessionCreateOptions{
		ID: metadata.ID, CWD: metadata.CWD,
		ParentSessionPath: metadata.ParentSessionPath, Metadata: metadata.Metadata,
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range parsed.Entries() {
		if err := created.Storage().AppendEntry(entry); err != nil {
			return nil, err
		}
	}
	return created, nil
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
