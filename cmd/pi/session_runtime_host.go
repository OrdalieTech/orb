package main

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

type cliSessionRuntimeHostOptions struct {
	BaseArgs      CLIArgs
	Manager       *session.SessionManager
	Dependencies  cliDependencies
	Streams       cliStreams
	ExtensionMode extensions.Mode
	// DeferSessionStart holds session_start until the mode binds its extension
	// UI, so extensions see a live ctx.ui on session_start. RPC mode sets this
	// (it binds the RPC UI in bindReplacement); the TUI path defers separately.
	DeferSessionStart bool
}

type cliPrintSession struct {
	ctx  context.Context
	host *codingagent.AgentSessionRuntime

	mu          sync.Mutex
	listener    func(any)
	unsubscribe func()
}

func newCLIPrintSession(ctx context.Context, host *codingagent.AgentSessionRuntime) *cliPrintSession {
	if ctx == nil {
		ctx = context.Background()
	}
	return &cliPrintSession{ctx: ctx, host: host}
}

func (session *cliPrintSession) Bind(replacement *codingagent.AgentSession) error {
	if replacement == nil {
		return fmt.Errorf("pi: print mode replacement returned no session")
	}
	if err := replacement.BindExtensions(session.ctx); err != nil {
		return err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.unsubscribe != nil {
		session.unsubscribe()
		session.unsubscribe = nil
	}
	if session.listener != nil {
		session.unsubscribe = replacement.Subscribe(session.listener)
	}
	return nil
}

func (session *cliPrintSession) Prompt(ctx context.Context, input any, images ...*ai.ImageContent) error {
	current := session.host.Session()
	if current == nil {
		return fmt.Errorf("pi: print mode session is unavailable")
	}
	return current.Prompt(ctx, input, images...)
}

func (session *cliPrintSession) Abort() {
	if current := session.host.Session(); current != nil {
		current.Abort()
	}
}

func (session *cliPrintSession) State() agent.AgentState {
	if current := session.host.Session(); current != nil {
		return current.State()
	}
	return agent.AgentState{}
}

func (session *cliPrintSession) Subscribe(listener func(any)) func() {
	session.mu.Lock()
	if session.unsubscribe != nil {
		session.unsubscribe()
		session.unsubscribe = nil
	}
	session.listener = listener
	if current := session.host.Session(); current != nil && listener != nil {
		session.unsubscribe = current.Subscribe(listener)
	}
	session.mu.Unlock()
	return func() {
		session.mu.Lock()
		if session.unsubscribe != nil {
			session.unsubscribe()
			session.unsubscribe = nil
		}
		session.listener = nil
		session.mu.Unlock()
	}
}

func newCLISessionRuntimeHost(
	ctx context.Context,
	options cliSessionRuntimeHostOptions,
) (*codingagent.AgentSessionRuntime, error) {
	if options.Manager == nil {
		return nil, fmt.Errorf("pi: session runtime host requires a session manager")
	}
	stderr := options.Streams.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	factory := func(_ context.Context, runtimeOptions codingagent.AgentSessionOptions) (*codingagent.AgentSessionResult, error) {
		manager := runtimeOptions.SessionManager
		if manager == nil {
			return nil, fmt.Errorf("pi: replacement runtime requires a session manager")
		}
		args := options.BaseArgs
		contextState := manager.BuildSessionContext()
		if len(manager.GetEntries()) > 0 {
			applySessionDefaults(&args, contextState, manager.GetBranch())
		}
		if runtimeOptions.ExtensionRegistry != nil {
			args.extensionRegistry = runtimeOptions.ExtensionRegistry
			args.extensionsLoaded = true
			args.extensionWarnings = nil
		}

		inputs, err := options.Dependencies.createRuntime(
			manager.GetCWD(), args, decodeSessionMessages(contextState.Messages),
		)
		if err != nil {
			return nil, err
		}
		if runtimeOptions.ExtensionRegistry != nil {
			inputs.Extensions = runtimeOptions.ExtensionRegistry
		}
		if err := appendInitialRuntimeState(manager, inputs.Agent.State(), contextState); err != nil {
			return nil, err
		}
		settings := inputs.Settings
		if settings == nil {
			agentDir, settingsErr := config.GetAgentDir()
			if settingsErr != nil {
				return nil, settingsErr
			}
			settings, settingsErr = config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(agentDir))
			if settingsErr != nil {
				return nil, settingsErr
			}
		}
		sessionConfig := codingagent.SessionRuntimeConfig{
			Agent: inputs.Agent, SessionManager: manager, Settings: settings, StreamFn: inputs.StreamFn,
			GetAPIKey: inputs.GetAPIKey, GetRequestAuth: inputs.GetRequestAuth, GetModelHeaders: inputs.GetModelHeaders,
			AvailableModels:   inputs.AvailableModels,
			ScopedModels:      inputs.ScopedModels,
			SlashResolver:     inputs.SlashResolver,
			ExtensionRegistry: inputs.Extensions,
			ExtensionMode:     options.ExtensionMode,
			ExtensionErrorHandler: func(extensionError extensions.ExtensionError) {
				_, _ = fmt.Fprintf(stderr, "Extension error (%s, %s): %s\n", extensionError.ExtensionPath, extensionError.Event, extensionError.Error)
			},
			BaseTools: inputs.BaseTools, InitialActiveToolNames: inputs.ActiveToolNames,
			AllowedToolNames: inputs.AllowedTools, ExcludedToolNames: inputs.ExcludedTools,
			RebuildBaseTools:    inputs.RebuildBaseTools,
			SystemPromptOptions: &inputs.PromptOptions,
			SessionStartEvent:   runtimeOptions.SessionStartEvent,
			DeferExtensionStart: runtimeOptions.DeferExtensionStart,
			DeferSessionStart:   options.DeferSessionStart,
		}
		if inputs.ModelRegistry != nil {
			sessionConfig.ModelRegistry = inputs.ModelRegistry
		}
		// Providers key affinity and prompt caches on the session id; upstream
		// createAgentSession passes sessionId into the Agent at construction.
		inputs.Agent.SetStreamSessionID(manager.GetSessionID())
		created, err := codingagent.NewSessionRuntime(sessionConfig)
		if err != nil {
			return nil, err
		}
		diagnostics := make([]codingagent.AgentSessionRuntimeDiagnostic, 0, len(inputs.Diagnostics))
		for _, message := range inputs.Diagnostics {
			diagnostics = append(diagnostics, codingagent.AgentSessionRuntimeDiagnostic{Type: "warning", Message: message})
			_, _ = fmt.Fprintln(stderr, "Warning: "+message)
		}
		agentDir, err := config.GetAgentDir()
		if err != nil {
			created.Dispose()
			return nil, err
		}
		return &codingagent.AgentSessionResult{
			Session: created, ExtensionRegistry: inputs.Extensions,
			Services: &codingagent.AgentSessionServices{
				CWD: manager.GetCWD(), AgentDir: agentDir, SettingsManager: settings,
				ModelRegistry: inputs.ModelRegistry, ExtensionRegistry: inputs.Extensions,
			},
			Diagnostics: diagnostics,
		}, nil
	}

	return codingagent.NewAgentSessionRuntime(ctx, codingagent.AgentSessionOptions{
		CWD: options.Manager.GetCWD(), SessionManager: options.Manager,
	}, factory)
}
