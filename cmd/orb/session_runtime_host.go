package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

type cliSessionRuntimeHostOptions struct {
	// Args are the startup arguments every runtime is built from, the first
	// and each replacement alike; the interactive host clears its --api-key
	// on logout.
	Args          *CLIArgs
	Manager       *session.SessionManager
	Dependencies  cliDependencies
	Stderr        io.Writer
	ExtensionMode extensions.Mode
	// Created receives the inputs of each runtime built.
	Created func(runtimeInputs)
}

type cliPrintSession struct {
	ctx  context.Context
	host *agent.AgentSessionRuntime

	mu          sync.Mutex
	listener    func(any)
	unsubscribe func()
}

func newCLIPrintSession(ctx context.Context, host *agent.AgentSessionRuntime) *cliPrintSession {
	if ctx == nil {
		ctx = context.Background()
	}
	return &cliPrintSession{ctx: ctx, host: host}
}

func (session *cliPrintSession) Bind(replacement *agent.AgentSession) error {
	if replacement == nil {
		return fmt.Errorf("orb: print mode replacement returned no session")
	}
	if err := replacement.BindExtensions(session.ctx); err != nil {
		return err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.unsubscribeLocked()
	if session.listener != nil {
		session.unsubscribe = replacement.Subscribe(session.listener)
	}
	return nil
}

func (session *cliPrintSession) Prompt(ctx context.Context, input any, images ...*ai.ImageContent) error {
	current := session.host.Session()
	if current == nil {
		return fmt.Errorf("orb: print mode session is unavailable")
	}
	return current.Prompt(ctx, input, images...)
}

func (session *cliPrintSession) Abort() {
	if current := session.host.Session(); current != nil {
		current.Abort()
	}
}

func (session *cliPrintSession) State() engine.AgentState {
	if current := session.host.Session(); current != nil {
		return current.State()
	}
	return engine.AgentState{}
}

func (session *cliPrintSession) Subscribe(listener func(any)) func() {
	session.mu.Lock()
	session.unsubscribeLocked()
	session.listener = listener
	if current := session.host.Session(); current != nil && listener != nil {
		session.unsubscribe = current.Subscribe(listener)
	}
	session.mu.Unlock()
	return func() {
		session.mu.Lock()
		session.unsubscribeLocked()
		session.listener = nil
		session.mu.Unlock()
	}
}

func (session *cliPrintSession) unsubscribeLocked() {
	if session.unsubscribe != nil {
		session.unsubscribe()
		session.unsubscribe = nil
	}
}

// newCLISessionRuntimeHost builds the session the CLI runs in every mode, and
// rebuilds it for each replacement, resolving cwd-bound services, extensions,
// project trust and the model again for the replacement's session.
func newCLISessionRuntimeHost(ctx context.Context, options cliSessionRuntimeHostOptions) (*agent.AgentSessionRuntime, error) {
	stderr := cmp.Or[io.Writer](options.Stderr, io.Discard)
	if options.Args == nil {
		options.Args = &CLIArgs{}
	}
	factory := func(_ context.Context, runtimeOptions agent.AgentSessionOptions) (*agent.AgentSessionResult, error) {
		manager := runtimeOptions.SessionManager
		args := *options.Args
		if err := args.native.BindSession(manager); err != nil {
			return nil, err
		}
		contextState := manager.BuildSessionContext()
		if len(contextState.Messages) > 0 {
			applySessionDefaults(&args, contextState, manager.GetBranch())
		}
		if args.extensionsLoaded && runtimeOptions.ExtensionRegistry != nil {
			// A replacement gets fresh instances of the extensions preloaded at
			// startup (upstream re-runs factories per session); otherwise it
			// loads the extensions settings name now.
			args.extensionRegistry, args.extensionWarnings = runtimeOptions.ExtensionRegistry, nil
		}
		inputs, err := options.Dependencies.createRuntime(manager.GetCWD(), args, manager.ContextMessages())
		if err != nil {
			return nil, err
		}
		if err := appendInitialRuntimeState(manager, inputs.Agent.State(), contextState); err != nil {
			return nil, err
		}
		agentDir, err := config.GetAgentDir()
		if err != nil {
			return nil, err
		}
		settings := inputs.Settings
		if settings == nil {
			if settings, err = config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(agentDir)); err != nil {
				return nil, err
			}
		}
		sessionConfig := agent.SessionRuntimeConfig{
			Agent: inputs.Agent, SessionManager: manager, Settings: settings, StreamFn: inputs.StreamFn,
			GetAPIKey: inputs.GetAPIKey, GetRequestAuth: inputs.GetRequestAuth, GetModelHeaders: inputs.GetModelHeaders,
			AvailableModels: inputs.AvailableModels, ScopedModels: inputs.ScopedModels, SlashResolver: inputs.SlashResolver,
			ExtensionRegistry: inputs.Extensions, ExtensionMode: options.ExtensionMode,
			ExtensionErrorHandler: func(extensionError extensions.ExtensionError) {
				_, _ = fmt.Fprintf(stderr, "Extension error (%s, %s): %s\n", extensionError.ExtensionPath, extensionError.Event, extensionError.Error)
			},
			BaseTools: inputs.BaseTools, InitialActiveToolNames: inputs.ActiveToolNames,
			AllowedToolNames: inputs.AllowedTools, ExcludedToolNames: inputs.ExcludedTools, RebuildBaseTools: inputs.RebuildBaseTools,
			SystemPromptOptions: &inputs.PromptOptions, Clock: inputs.Clock, ResourceLoader: inputs.ResourceLoader,
			SessionStartEvent: runtimeOptions.SessionStartEvent, DeferExtensionStart: runtimeOptions.DeferExtensionStart,
		}
		if inputs.ModelRegistry != nil {
			sessionConfig.ModelRegistry = inputs.ModelRegistry
		}
		// Providers key affinity and prompt caches on the session id; upstream
		// createAgentSession passes sessionId into the Agent at construction.
		inputs.Agent.SetStreamSessionID(manager.GetSessionID())
		created, err := newSessionRuntime(sessionConfig)
		if err != nil {
			return nil, err
		}
		diagnostics := make([]agent.AgentSessionRuntimeDiagnostic, 0, len(inputs.Diagnostics))
		for _, diagnostic := range inputs.Diagnostics {
			message := startupDiagnosticText(diagnostic)
			diagnostics = append(diagnostics, agent.AgentSessionRuntimeDiagnostic{Type: "warning", Message: message})
			// The TUI shows its diagnostics itself.
			if options.ExtensionMode != extensions.ModeTUI {
				_, _ = fmt.Fprintln(stderr, "Warning: "+message)
			}
		}
		if options.Created != nil {
			options.Created(inputs)
		}
		return &agent.AgentSessionResult{
			Session: created, ExtensionRegistry: inputs.Extensions,
			Services: &agent.AgentSessionServices{
				CWD: manager.GetCWD(), AgentDir: agentDir, SettingsManager: settings,
				ModelRegistry: inputs.ModelRegistry, ExtensionRegistry: inputs.Extensions,
			},
			Diagnostics: diagnostics,
		}, nil
	}

	host, err := agent.NewAgentSessionRuntime(ctx, agent.AgentSessionOptions{
		CWD: options.Manager.GetCWD(), SessionManager: options.Manager, ExtensionRegistry: options.Args.extensionRegistry,
	}, factory)
	if err != nil {
		return nil, err
	}
	// A reload (/plugins, an extension's ctx.reload()) builds the session again, so the plugins
	// and options settings now name are the ones it runs, in every mode.
	host.SetReload(host.Rebuild)
	if options.Args.native != nil {
		host.SetSessionClaim(options.Args.native.ClaimSession)
	}
	return host, nil
}
