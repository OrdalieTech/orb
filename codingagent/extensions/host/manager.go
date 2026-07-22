package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

//go:embed host.mjs
var hostSource []byte

//go:embed loader.mjs
var loaderSource []byte

var (
	ErrNotRunning = errors.New("extension host is not running")
	ErrRestarting = errors.New("extension host is restarting")
)

type AgentInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	CWD      string `json:"cwd"`
	AgentDir string `json:"agentDir"`
}

type Options struct {
	AgentDir        string
	CWD             string
	Version         string
	Runtime         *Runtime
	PigoExecutable  string
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
	MaxRestarts     int
	BackoffBase     time.Duration
	BackoffMax      time.Duration
	Stderr          io.Writer
	OnDiagnostic    func(extensions.Diagnostic)
}

type LoadError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type LoadResult struct {
	Paths       []string
	Errors      []LoadError
	Diagnostics []extensions.Diagnostic
	Runtime     *Runtime
}

type Manager struct {
	options Options

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	runtime     *Runtime
	entries     []extensionEntry
	current     *generation
	states      map[string]*registrationState
	closed      bool
	started     bool
	restarting  bool
	lastError   error

	factoryMu     sync.Mutex
	primaryID     string
	primaryPrimed bool

	restartCount atomic.Int64
	providers    providerBridge
	stateHost    *stateHost
}

type extensionEntry struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	RuntimePath string `json:"runtimePath,omitempty"`
	SourceRoot  string `json:"sourceRoot,omitempty"`
	RuntimeRoot string `json:"runtimeRoot,omitempty"`
}

type registrationState struct {
	Path          string
	Tools         []wireToolDefinition
	Commands      []wireCommand
	Shortcuts     []wireShortcut
	Subscriptions []wireSubscription
	Renderers     []wireRendererRegistration
	Providers     []wireProviderRegistration
}

type wireToolDefinition struct {
	Name             string                  `json:"name"`
	Label            string                  `json:"label"`
	Description      string                  `json:"description"`
	PromptSnippet    string                  `json:"promptSnippet,omitempty"`
	PromptGuidelines []string                `json:"promptGuidelines,omitempty"`
	Parameters       json.RawMessage         `json:"parameters"`
	RenderShell      extensions.RenderShell  `json:"renderShell,omitempty"`
	ExecutionMode    agent.ToolExecutionMode `json:"executionMode,omitempty"`
}

type wireCommand struct {
	Name        string
	Description string
}

type wireShortcut struct {
	Shortcut    string
	Description string
}

type wireSubscription struct {
	ID    string
	Event extensions.EventType
}

type pendingResponse struct {
	result json.RawMessage
	err    error
}

type generation struct {
	manager *Manager
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	codec   *codec
	ui      *uiGeneration

	mu      sync.Mutex
	nextID  uint64
	pending map[string]chan pendingResponse
	updates map[string]func(json.RawMessage)

	registrationsMu sync.Mutex
	registrations   map[string]*registrationState

	handshake       chan error
	done            chan struct{}
	waitDone        chan struct{}
	failOnce        sync.Once
	expected        atomic.Bool
	ready           atomic.Bool
	restartEligible atomic.Bool
}

func NewManager(options Options) *Manager {
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = 2 * time.Second
	}
	if options.MaxRestarts <= 0 {
		options.MaxRestarts = 3
	}
	if options.BackoffBase <= 0 {
		options.BackoffBase = 100 * time.Millisecond
	}
	if options.BackoffMax <= 0 {
		options.BackoffMax = 2 * time.Second
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.CWD == "" {
		options.CWD = "."
	}
	if options.Version == "" {
		options.Version = "unknown"
	}
	return &Manager{options: options, states: make(map[string]*registrationState), stateHost: newStateHost(options)}
}

func (manager *Manager) RegisterInto(ctx context.Context, registry *extensions.Registry, paths []string) LoadResult {
	result := LoadResult{Paths: append([]string(nil), paths...)}
	if len(paths) == 0 {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if registry == nil {
		diagnostic := extensions.Diagnostic{Type: "error", Message: "extension host: nil extension registry", Path: "<extension-host>"}
		result.Diagnostics = append(result.Diagnostics, diagnostic)
		return result
	}

	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.mu.Lock()
	if manager.started || manager.closed {
		manager.mu.Unlock()
		diagnostic := extensions.Diagnostic{Type: "error", Message: "extension host manager already used", Path: "<extension-host>"}
		result.Diagnostics = append(result.Diagnostics, diagnostic)
		return result
	}
	manager.entries = makeEntries(paths)
	manager.mu.Unlock()

	runtime, err := manager.resolveRuntime(ctx)
	if err != nil {
		var unavailable *RuntimeUnavailableError
		if errors.As(err, &unavailable) {
			diagnostic := unavailable.Diagnostic()
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			manager.report(diagnostic)
		} else {
			diagnostic := extensions.Diagnostic{Type: "error", Message: err.Error(), Path: "<extension-host>"}
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			manager.report(diagnostic)
		}
		return result
	}
	result.Runtime = &runtime

	loaded, err := manager.startLocked(ctx)
	if err != nil {
		diagnostic := extensions.Diagnostic{Type: "error", Message: err.Error(), Path: "<extension-host>"}
		result.Diagnostics = append(result.Diagnostics, diagnostic)
		manager.report(diagnostic)
		return result
	}
	result.Errors = append(result.Errors, loaded.Errors...)

	manager.mu.Lock()
	manager.started = true
	manager.mu.Unlock()
	for _, entry := range manager.entries {
		if !loaded.success[entry.ID] {
			continue
		}
		if manager.primaryID == "" {
			manager.primaryID = entry.ID
		}
		if err := registry.Register(entry.Path, manager.factory(entry.ID)); err != nil {
			result.Errors = append(result.Errors, LoadError{Path: entry.Path, Error: stripRegistryPrefix(entry.Path, err)})
		}
	}
	return result
}

func (manager *Manager) Reload(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return errors.New("extension host is closed")
	}
	current := manager.current
	manager.mu.Unlock()
	if current != nil {
		manager.stopGeneration(current)
	}
	loaded, err := manager.startLocked(ctx)
	if err != nil {
		return err
	}
	for _, loadError := range loaded.Errors {
		manager.report(extensions.Diagnostic{Type: "error", Message: loadError.Error, Path: loadError.Path})
	}
	return nil
}

func (manager *Manager) Close() error {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	current := manager.current
	manager.current = nil
	manager.mu.Unlock()
	if current != nil {
		manager.stopGeneration(current)
	}
	manager.stateHost.close()
	return nil
}

func (manager *Manager) RestartCount() int64 { return manager.restartCount.Load() }

func (manager *Manager) Runtime() *Runtime {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.runtime == nil {
		return nil
	}
	copy := *manager.runtime
	copy.Args = append([]string(nil), copy.Args...)
	return &copy
}

type generationLoadResult struct {
	Errors  []LoadError
	success map[string]bool
}

func (manager *Manager) startLocked(ctx context.Context) (generationLoadResult, error) {
	var result generationLoadResult
	result.success = make(map[string]bool)
	runtime := manager.Runtime()
	if runtime == nil {
		return result, ErrNotRunning
	}
	scriptPath, err := materializeHost(manager.options.AgentDir)
	if err != nil {
		return result, fmt.Errorf("extension host: materialize script: %w", err)
	}
	commandArgs := append([]string(nil), runtime.Args...)
	if runtime.Name == "node" {
		loaderPath, loaderErr := materializeSource(manager.options.AgentDir, "loader", loaderSource)
		if loaderErr != nil {
			return result, fmt.Errorf("extension host: materialize loader: %w", loaderErr)
		}
		commandArgs = append(commandArgs, "--experimental-loader", loaderPath)
	}
	hostEnvironment, err := prepareHostEnvironment(manager.options.AgentDir, os.Environ(), manager.options.PigoExecutable)
	if err != nil {
		return result, err
	}
	entryFailures := make(map[string]bool)
	preparedEntries := append([]extensionEntry(nil), manager.entries...)
	for index, entry := range preparedEntries {
		requestContext, requestCancel := manager.timeoutContext(ctx)
		dependencyErr := materializeDependencies(requestContext, *runtime, entry.Path, hostEnvironment)
		requestCancel()
		if dependencyErr != nil {
			result.Errors = append(result.Errors, LoadError{Path: entry.Path, Error: dependencyErr.Error()})
			entryFailures[entry.ID] = true
			continue
		}
		prepared, prepareErr := prepareRuntimeEntry(manager.options.AgentDir, *runtime, entry)
		if prepareErr != nil {
			result.Errors = append(result.Errors, LoadError{Path: entry.Path, Error: prepareErr.Error()})
			entryFailures[entry.ID] = true
			continue
		}
		preparedEntries[index] = prepared
	}
	manager.mu.Lock()
	manager.entries = preparedEntries
	manager.mu.Unlock()
	manager.stateHost.setEnvironment(hostEnvironment)
	manager.stateHost.reset(manager.entries)
	command := exec.CommandContext(context.Background(), runtime.Path, append(commandArgs, scriptPath)...)
	command.Dir = manager.options.CWD
	command.Env = hostEnvironment
	command.Stderr = manager.options.Stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return result, fmt.Errorf("extension host: stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("extension host: stdout: %w", err)
	}
	if err := command.Start(); err != nil {
		return result, fmt.Errorf("extension host: start %s: %w", runtime.Name, err)
	}
	generation := &generation{
		manager:       manager,
		cmd:           command,
		stdin:         stdin,
		codec:         newCodec(stdout, stdin),
		pending:       make(map[string]chan pendingResponse),
		updates:       make(map[string]func(json.RawMessage)),
		registrations: make(map[string]*registrationState),
		handshake:     make(chan error, 1),
		done:          make(chan struct{}),
		waitDone:      make(chan struct{}),
	}
	generation.ui = newUIGeneration(generation)
	for _, entry := range manager.entries {
		generation.registrations[entry.ID] = &registrationState{Path: entry.Path}
	}
	manager.mu.Lock()
	manager.current = generation
	manager.mu.Unlock()
	go generation.readLoop()
	go generation.waitLoop()

	handshakeContext, cancel := manager.timeoutContext(ctx)
	defer cancel()
	select {
	case err := <-generation.handshake:
		if err != nil {
			manager.stopGeneration(generation)
			return result, err
		}
	case <-handshakeContext.Done():
		manager.stopGeneration(generation)
		return result, fmt.Errorf("extension host: handshake: %w", handshakeContext.Err())
	case <-generation.done:
		return result, generation.failure()
	}

	for _, entry := range manager.entries {
		if entryFailures[entry.ID] {
			continue
		}
		requestContext, requestCancel := manager.timeoutContext(ctx)
		request := struct {
			ExtensionID string `json:"extensionId"`
			Path        string `json:"path"`
		}{ExtensionID: entry.ID, Path: entry.Path}
		var response struct {
			ExtensionID string `json:"extensionId"`
			Path        string `json:"path"`
			Loaded      bool   `json:"loaded"`
		}
		raw, requestErr := generation.request(requestContext, "load_extension", request, nil)
		requestCancel()
		if requestErr == nil {
			requestErr = json.Unmarshal(raw, &response)
		}
		if requestErr != nil || !response.Loaded {
			message := "extension did not report a successful load"
			if requestErr != nil {
				message = requestErr.Error()
			}
			result.Errors = append(result.Errors, LoadError{Path: entry.Path, Error: message})
			continue
		}
		result.success[entry.ID] = true
	}
	select {
	case <-generation.done:
		return result, generation.failure()
	default:
	}

	states := make(map[string]*registrationState)
	generation.registrationsMu.Lock()
	for id := range result.success {
		states[id] = cloneRegistrationState(generation.registrations[id])
	}
	generation.registrationsMu.Unlock()
	manager.mu.Lock()
	manager.states = states
	manager.lastError = nil
	manager.current = generation
	manager.mu.Unlock()
	generation.ready.Store(true)
	generation.restartEligible.Store(true)
	manager.stateHost.rebindCaptured(manager)
	return result, nil
}

func (manager *Manager) resolveRuntime(ctx context.Context) (Runtime, error) {
	manager.mu.Lock()
	if manager.runtime != nil {
		value := *manager.runtime
		manager.mu.Unlock()
		return value, nil
	}
	manager.mu.Unlock()
	var runtime Runtime
	var err error
	if manager.options.Runtime != nil {
		runtime = *manager.options.Runtime
		runtime.Args = append([]string(nil), runtime.Args...)
	} else {
		runtime, err = DiscoverRuntime(ctx)
	}
	if err != nil {
		return Runtime{}, err
	}
	manager.mu.Lock()
	manager.runtime = &runtime
	manager.mu.Unlock()
	return runtime, nil
}

func (manager *Manager) timeoutContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if _, hasDeadline := parent.Deadline(); hasDeadline {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, manager.options.RequestTimeout)
}

func (manager *Manager) stopGeneration(generation *generation) {
	generation.expected.Store(true)
	if !generation.closed() {
		ctx, cancel := context.WithTimeout(context.Background(), manager.options.ShutdownTimeout)
		_, _ = generation.request(ctx, "shutdown", struct{}{}, nil)
		cancel()
	}
	_ = generation.stdin.Close()
	select {
	case <-generation.waitDone:
	case <-time.After(manager.options.ShutdownTimeout):
		_ = generation.cmd.Process.Kill()
		<-generation.waitDone
	}
	manager.mu.Lock()
	if manager.current == generation {
		manager.current = nil
	}
	manager.mu.Unlock()
}

func (manager *Manager) generationExited(generation *generation, err error) {
	if generation.expected.Load() || !generation.restartEligible.Load() {
		return
	}
	manager.mu.Lock()
	if manager.current != generation || manager.closed {
		manager.mu.Unlock()
		return
	}
	manager.current = nil
	manager.lastError = err
	if manager.restarting {
		manager.mu.Unlock()
		return
	}
	manager.restarting = true
	manager.mu.Unlock()
	go manager.restartLoop()
}

func (manager *Manager) restartLoop() {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	var lastErr error
	for attempt := 0; attempt < manager.options.MaxRestarts; attempt++ {
		delay := manager.options.BackoffBase << attempt
		if delay > manager.options.BackoffMax {
			delay = manager.options.BackoffMax
		}
		timer := time.NewTimer(delay)
		<-timer.C
		manager.mu.Lock()
		closed := manager.closed
		manager.mu.Unlock()
		if closed {
			return
		}
		loaded, err := manager.startLocked(context.Background())
		if err == nil {
			for _, loadError := range loaded.Errors {
				manager.report(extensions.Diagnostic{Type: "error", Message: loadError.Error, Path: loadError.Path})
			}
			manager.restartCount.Add(1)
			manager.mu.Lock()
			manager.restarting = false
			manager.mu.Unlock()
			return
		}
		lastErr = err
		manager.mu.Lock()
		current := manager.current
		manager.mu.Unlock()
		if current != nil {
			manager.stopGeneration(current)
		}
	}
	manager.mu.Lock()
	manager.restarting = false
	manager.lastError = lastErr
	manager.mu.Unlock()
	manager.report(extensions.Diagnostic{Type: "error", Message: fmt.Sprintf("extension host restart limit reached: %v", lastErr), Path: "<extension-host>"})
}

func (manager *Manager) factory(extensionID string) extensions.Factory {
	return func(api extensions.API) error {
		if extensionID == manager.primaryID {
			manager.factoryMu.Lock()
			if manager.primaryPrimed {
				if err := manager.Reload(context.Background()); err != nil {
					manager.factoryMu.Unlock()
					return err
				}
			} else {
				manager.primaryPrimed = true
			}
			manager.factoryMu.Unlock()
		}
		state := manager.state(extensionID)
		if state == nil {
			return fmt.Errorf("extension host: extension %s is unavailable", extensionID)
		}
		for _, definition := range state.Tools {
			api.RegisterTool(manager.tool(extensionID, definition))
		}
		for _, command := range state.Commands {
			api.RegisterCommand(command.Name, extensions.Command{
				Description: command.Description,
				Handler: func(ctx context.Context, arguments string, commandContext extensions.CommandContext) error {
					return manager.executeCommand(ctx, extensionID, command.Name, arguments, commandContext)
				},
			})
		}
		for _, shortcut := range state.Shortcuts {
			shortcut := shortcut
			api.RegisterShortcut(shortcut.Shortcut, extensions.Shortcut{
				Description: shortcut.Description,
				Handler: func(ctx context.Context, extensionContext extensions.Context) error {
					return manager.executeShortcut(ctx, extensionID, shortcut.Shortcut, extensionContext)
				},
			})
		}
		for _, subscription := range state.Subscriptions {
			subscription := subscription
			api.On(subscription.Event, func(ctx context.Context, event extensions.Event, extensionContext extensions.Context) (any, error) {
				return manager.emitEvent(ctx, extensionID, subscription, event, extensionContext)
			})
		}
		for _, renderer := range state.Renderers {
			switch renderer.Kind {
			case rendererMessage:
				api.RegisterMessageRenderer(renderer.CustomType, manager.messageRenderer(extensionID, renderer.CustomType))
			case rendererEntry:
				api.RegisterEntryRenderer(renderer.CustomType, manager.entryRenderer(extensionID, renderer.CustomType))
			}
		}
		manager.registerProviders(api, extensionID, state.Providers)
		return manager.stateHost.bind(manager, extensionID, api)
	}
}

func (manager *Manager) tool(extensionID string, definition wireToolDefinition) extensions.ToolDefinition {
	return extensions.ToolDefinition{
		Name:             definition.Name,
		Label:            definition.Label,
		Description:      definition.Description,
		PromptSnippet:    definition.PromptSnippet,
		PromptGuidelines: append([]string(nil), definition.PromptGuidelines...),
		Parameters:       ai.JSONSchema(append(json.RawMessage(nil), definition.Parameters...)),
		RenderShell:      definition.RenderShell,
		ExecutionMode:    definition.ExecutionMode,
		Execute: func(
			ctx context.Context,
			toolCallID string,
			params any,
			onUpdate agent.AgentToolUpdateCallback,
			extensionContext extensions.Context,
		) (agent.AgentToolResult, error) {
			finishState := manager.stateHost.beforeCallback(manager, extensionID, extensionContext)
			defer finishState()
			bound, err := manager.bindUIContext(extensionContext)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			defer bound.close()
			releaseSignal := manager.stateHost.bindContextSignal(bound.generation, extensionID, callbackSignal(nil, extensionContext), &bound.wire)
			defer releaseSignal()
			request := struct {
				ExtensionID string      `json:"extensionId"`
				ToolName    string      `json:"toolName"`
				ToolCallID  string      `json:"toolCallId"`
				Params      any         `json:"params"`
				Context     wireContext `json:"context"`
			}{extensionID, definition.Name, toolCallID, params, bound.wire}
			var update func(json.RawMessage)
			if onUpdate != nil {
				update = func(raw json.RawMessage) {
					var partial agent.AgentToolResult
					if json.Unmarshal(raw, &partial) == nil {
						onUpdate(partial)
					}
				}
			}
			raw, err := bound.request(ctx, "execute_tool", request, update)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			var result agent.AgentToolResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("extension host: decode tool %s result: %w", definition.Name, err)
			}
			return result, nil
		},
	}
}

type wireContext struct {
	CWD         string          `json:"cwd"`
	Mode        extensions.Mode `json:"mode"`
	HasUI       bool            `json:"hasUI"`
	UIContextID string          `json:"uiContextId,omitempty"`
	UI          *wireUISnapshot `json:"ui,omitempty"`
	Signal      *wireSignal     `json:"signal,omitempty"`
}

func newWireContext(value extensions.Context) wireContext {
	if value == nil {
		return wireContext{}
	}
	return wireContext{CWD: value.CWD(), Mode: value.Mode(), HasUI: value.HasUI()}
}

func (manager *Manager) executeCommand(
	ctx context.Context,
	extensionID, name, arguments string,
	commandContext extensions.CommandContext,
) error {
	finishState := manager.stateHost.beforeCallback(manager, extensionID, commandContext)
	defer finishState()
	bound, err := manager.bindUIContext(commandContext)
	if err != nil {
		return err
	}
	defer bound.close()
	releaseSignal := manager.stateHost.bindContextSignal(bound.generation, extensionID, callbackSignal(nil, commandContext), &bound.wire)
	defer releaseSignal()
	request := struct {
		ExtensionID string      `json:"extensionId"`
		CommandName string      `json:"commandName"`
		Arguments   string      `json:"arguments"`
		Context     wireContext `json:"context"`
	}{extensionID, name, arguments, bound.wire}
	_, err = bound.request(ctx, "execute_command", request, nil)
	return err
}

func (manager *Manager) executeShortcut(
	ctx context.Context,
	extensionID, shortcut string,
	extensionContext extensions.Context,
) error {
	finishState := manager.stateHost.beforeCallback(manager, extensionID, extensionContext)
	defer finishState()
	bound, err := manager.bindUIContext(extensionContext)
	if err != nil {
		return err
	}
	defer bound.close()
	releaseSignal := manager.stateHost.bindContextSignal(bound.generation, extensionID, callbackSignal(nil, extensionContext), &bound.wire)
	defer releaseSignal()
	request := struct {
		ExtensionID string      `json:"extensionId"`
		Shortcut    string      `json:"shortcut"`
		Context     wireContext `json:"context"`
	}{extensionID, shortcut, bound.wire}
	_, err = bound.request(ctx, "execute_shortcut", request, nil)
	return err
}

func (manager *Manager) emitEvent(
	ctx context.Context,
	extensionID string,
	subscription wireSubscription,
	event extensions.Event,
	extensionContext extensions.Context,
) (any, error) {
	finishState := manager.stateHost.beforeCallback(manager, extensionID, extensionContext)
	defer finishState()
	payload, err := wireValue(event)
	if err != nil {
		return nil, err
	}
	bound, err := manager.bindUIContext(extensionContext)
	if err != nil {
		return nil, err
	}
	defer bound.close()
	releaseSignal := manager.stateHost.bindContextSignal(bound.generation, extensionID, callbackSignal(event, extensionContext), &bound.wire)
	defer releaseSignal()
	request := struct {
		ExtensionID    string               `json:"extensionId"`
		SubscriptionID string               `json:"subscriptionId"`
		Event          extensions.EventType `json:"event"`
		Payload        any                  `json:"payload"`
		Context        wireContext          `json:"context"`
	}{extensionID, subscription.ID, subscription.Event, payload, bound.wire}
	raw, err := bound.request(ctx, "emit_event", request, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Value   json.RawMessage `json:"value"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if err := manager.stateHost.applyEventMutation(event, response.Payload); err != nil {
		return nil, err
	}
	if len(response.Value) == 0 {
		return nil, nil
	}
	return manager.stateHost.decodeEventResult(manager, extensionID, subscription.Event, response.Value)
}

func (manager *Manager) request(ctx context.Context, method string, params any, update func(json.RawMessage)) (json.RawMessage, error) {
	manager.mu.Lock()
	generation := manager.current
	restarting := manager.restarting
	manager.mu.Unlock()
	if generation == nil {
		if restarting {
			return nil, ErrRestarting
		}
		return nil, ErrNotRunning
	}
	if !generation.ready.Load() {
		return nil, ErrRestarting
	}
	requestContext, cancel := manager.timeoutContext(ctx)
	defer cancel()
	return generation.request(requestContext, method, params, update)
}

func (manager *Manager) state(extensionID string) *registrationState {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return cloneRegistrationState(manager.states[extensionID])
}

func (manager *Manager) handleHostRequest(generation *generation, value frame) (any, *protocolError) {
	switch value.Method {
	case "handshake":
		var params struct {
			Runtime      Runtime  `json:"runtime"`
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, &protocolError{Code: "invalid_handshake", Message: err.Error()}
		}
		runtime := manager.Runtime()
		if runtime == nil || params.Runtime.Name != runtime.Name {
			return nil, &protocolError{Code: "invalid_handshake", Message: "runtime identity mismatch"}
		}
		return struct {
			ExtensionEntries []extensionEntry `json:"extensionEntries"`
			Agent            AgentInfo        `json:"agent"`
			Capabilities     []string         `json:"capabilities"`
			StateSnapshot    stateSnapshot    `json:"stateSnapshot"`
		}{
			ExtensionEntries: append([]extensionEntry(nil), manager.entries...),
			Agent:            AgentInfo{Name: "pigo", Version: manager.options.Version, CWD: manager.options.CWD, AgentDir: manager.options.AgentDir},
			Capabilities:     []string{"tool_updates", "providers", "ui", "state_v1"},
			StateSnapshot:    manager.stateHost.handshakeSnapshot(),
		}, nil
	case "ui_request":
		return manager.handleUIRequest(generation, value)
	case "register_tool":
		var params struct {
			ExtensionID string             `json:"extensionId"`
			Definition  wireToolDefinition `json:"definition"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, invalidRegistration(err)
		}
		if params.Definition.Name == "" || len(params.Definition.Parameters) == 0 || !json.Valid(params.Definition.Parameters) {
			return nil, invalidRegistration(errors.New("tool requires a name and JSON parameters schema"))
		}
		state := generation.registration(params.ExtensionID)
		if state == nil {
			return nil, invalidRegistration(errors.New("unknown extension id"))
		}
		state.Tools = append(state.Tools, params.Definition)
		if api := manager.stateHost.api(params.ExtensionID); api != nil {
			if err := callStateAPI(func() { api.RegisterTool(manager.tool(params.ExtensionID, params.Definition)) }); err != nil {
				return nil, invalidRegistration(err)
			}
		}
		return map[string]bool{"accepted": true}, nil
	case "register_command":
		var params struct {
			ExtensionID string `json:"extensionId"`
			Name        string `json:"name"`
			Options     struct {
				Description string `json:"description"`
			} `json:"options"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil || params.Name == "" {
			if err == nil {
				err = errors.New("command requires a name")
			}
			return nil, invalidRegistration(err)
		}
		state := generation.registration(params.ExtensionID)
		if state == nil {
			return nil, invalidRegistration(errors.New("unknown extension id"))
		}
		state.Commands = append(state.Commands, wireCommand{Name: params.Name, Description: params.Options.Description})
		if api := manager.stateHost.api(params.ExtensionID); api != nil {
			command := wireCommand{Name: params.Name, Description: params.Options.Description}
			if err := callStateAPI(func() {
				api.RegisterCommand(command.Name, extensions.Command{
					Description: command.Description,
					Handler: func(ctx context.Context, arguments string, commandContext extensions.CommandContext) error {
						return manager.executeCommand(ctx, params.ExtensionID, command.Name, arguments, commandContext)
					},
				})
			}); err != nil {
				return nil, invalidRegistration(err)
			}
		}
		return map[string]bool{"accepted": true}, nil
	case "register_shortcut":
		var params struct {
			ExtensionID string `json:"extensionId"`
			Shortcut    string `json:"shortcut"`
			Options     struct {
				Description string `json:"description"`
			} `json:"options"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil || params.Shortcut == "" {
			if err == nil {
				err = errors.New("shortcut requires a key")
			}
			return nil, invalidRegistration(err)
		}
		state := generation.registration(params.ExtensionID)
		if state == nil {
			return nil, invalidRegistration(errors.New("unknown extension id"))
		}
		state.Shortcuts = append(state.Shortcuts, wireShortcut{Shortcut: params.Shortcut, Description: params.Options.Description})
		if api := manager.stateHost.api(params.ExtensionID); api != nil {
			shortcut := wireShortcut{Shortcut: params.Shortcut, Description: params.Options.Description}
			if err := callStateAPI(func() {
				api.RegisterShortcut(shortcut.Shortcut, extensions.Shortcut{
					Description: shortcut.Description,
					Handler: func(ctx context.Context, extensionContext extensions.Context) error {
						return manager.executeShortcut(ctx, params.ExtensionID, shortcut.Shortcut, extensionContext)
					},
				})
			}); err != nil {
				return nil, invalidRegistration(err)
			}
		}
		return map[string]bool{"accepted": true}, nil
	case "subscribe_event":
		var params struct {
			ExtensionID    string               `json:"extensionId"`
			SubscriptionID string               `json:"subscriptionId"`
			Event          extensions.EventType `json:"event"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil || params.SubscriptionID == "" || params.Event == "" {
			if err == nil {
				err = errors.New("event subscription requires id and event")
			}
			return nil, invalidRegistration(err)
		}
		state := generation.registration(params.ExtensionID)
		if state == nil {
			return nil, invalidRegistration(errors.New("unknown extension id"))
		}
		state.Subscriptions = append(state.Subscriptions, wireSubscription{ID: params.SubscriptionID, Event: params.Event})
		if api := manager.stateHost.api(params.ExtensionID); api != nil {
			subscription := wireSubscription{ID: params.SubscriptionID, Event: params.Event}
			if err := callStateAPI(func() {
				api.On(subscription.Event, func(ctx context.Context, event extensions.Event, extensionContext extensions.Context) (any, error) {
					return manager.emitEvent(ctx, params.ExtensionID, subscription, event, extensionContext)
				})
			}); err != nil {
				return nil, invalidRegistration(err)
			}
		}
		return map[string]bool{"accepted": true}, nil
	case "register_renderer":
		var params struct {
			ExtensionID string `json:"extensionId"`
			Kind        string `json:"kind"`
			CustomType  string `json:"customType"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil || params.CustomType == "" || params.Kind != rendererMessage && params.Kind != rendererEntry {
			if err == nil {
				err = errors.New("renderer requires a valid kind and custom type")
			}
			return nil, invalidRegistration(err)
		}
		state := generation.registration(params.ExtensionID)
		if state == nil {
			return nil, invalidRegistration(errors.New("unknown extension id"))
		}
		state.Renderers = append(state.Renderers, wireRendererRegistration{Kind: params.Kind, CustomType: params.CustomType})
		if api := manager.stateHost.api(params.ExtensionID); api != nil {
			if err := callStateAPI(func() {
				if params.Kind == rendererMessage {
					api.RegisterMessageRenderer(params.CustomType, manager.messageRenderer(params.ExtensionID, params.CustomType))
				} else {
					api.RegisterEntryRenderer(params.CustomType, manager.entryRenderer(params.ExtensionID, params.CustomType))
				}
			}); err != nil {
				return nil, invalidRegistration(err)
			}
		}
		return map[string]bool{"accepted": true}, nil
	default:
		if result, protocolErr, handled := manager.stateHost.handleRequest(manager, generation, value); handled {
			return result, protocolErr
		}
		if result, protocolErr, handled := manager.handleProviderHostRequest(generation, value); handled {
			return result, protocolErr
		}
		return nil, &protocolError{Code: "method_not_found", Message: "unknown host request method " + value.Method}
	}
}

func (manager *Manager) handleHostEvent(generation *generation, value frame) {
	switch value.Method {
	case "tool_update":
		var update struct {
			RequestID string          `json:"requestId"`
			Partial   json.RawMessage `json:"partial"`
		}
		if json.Unmarshal(value.Params, &update) == nil && update.RequestID != "" {
			generation.routeUpdate(update.RequestID, update.Partial)
		}
	case "log":
		var diagnostic struct {
			Level       string `json:"level"`
			Message     string `json:"message"`
			ExtensionID string `json:"extensionId"`
		}
		if json.Unmarshal(value.Params, &diagnostic) == nil {
			manager.report(extensions.Diagnostic{Type: diagnostic.Level, Message: diagnostic.Message, Path: diagnostic.ExtensionID})
		}
	case "ui_request":
		manager.handleUIEvent(generation, value.Params)
	case "ui_component_render":
		if generation.ui != nil {
			generation.ui.routeRender(value.Params)
		}
	default:
		manager.handleProviderHostEvent(generation, value)
	}
}

func (generation *generation) readLoop() {
	first := true
	for {
		value, err := generation.codec.read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = generation.cmd.Process.Kill()
			}
			return
		}
		if first {
			first = false
			if value.Kind != frameRequest || value.Method != "handshake" {
				generation.handshake <- errors.New("extension host: first frame is not handshake")
				_ = generation.cmd.Process.Kill()
				return
			}
		}
		switch value.Kind {
		case frameResponse:
			generation.routeResponse(value)
		case frameRequest:
			if value.Method == "ui_request" || generation.manager.stateHost.asyncRequest(value.Method) {
				go generation.respondHostRequest(value)
				continue
			}
			if !generation.respondHostRequest(value) {
				return
			}
		case frameEvent:
			generation.manager.handleHostEvent(generation, value)
		}
	}
}

func (generation *generation) respondHostRequest(value frame) bool {
	result, protocolErr := generation.manager.handleHostRequest(generation, value)
	var response frame
	if protocolErr != nil {
		response = errorFrame(value.ID, protocolErr.Code, protocolErr.Message)
	} else {
		var err error
		response, err = successFrame(value.ID, result)
		if err != nil {
			response = errorFrame(value.ID, "internal_error", err.Error())
		}
	}
	writeErr := generation.codec.write(response)
	if value.Method == "handshake" {
		if protocolErr != nil {
			generation.handshake <- protocolErr
		} else {
			generation.handshake <- writeErr
		}
	}
	if writeErr != nil {
		_ = generation.cmd.Process.Kill()
		return false
	}
	return true
}

func (generation *generation) waitLoop() {
	err := generation.cmd.Wait()
	generation.fail(fmt.Errorf("extension host exited: %w", processError(err)))
	close(generation.waitDone)
	generation.manager.generationExited(generation, err)
}

func (generation *generation) request(
	ctx context.Context,
	method string,
	params any,
	update func(json.RawMessage),
) (json.RawMessage, error) {
	generation.mu.Lock()
	if generation.closedLocked() {
		generation.mu.Unlock()
		return nil, generation.failure()
	}
	generation.nextID++
	id := fmt.Sprintf("pigo-%d", generation.nextID)
	response := make(chan pendingResponse, 1)
	generation.pending[id] = response
	if update != nil {
		generation.updates[id] = update
	}
	generation.mu.Unlock()
	value, err := requestFrame(id, method, params)
	if err == nil {
		err = generation.codec.write(value)
	}
	if err != nil {
		generation.remove(id)
		return nil, err
	}
	select {
	case resolved := <-response:
		return resolved.result, resolved.err
	case <-ctx.Done():
		generation.remove(id)
		return nil, ctx.Err()
	case <-generation.done:
		return nil, generation.failure()
	}
}

func (generation *generation) routeResponse(value frame) {
	generation.mu.Lock()
	waiter := generation.pending[value.ID]
	delete(generation.pending, value.ID)
	delete(generation.updates, value.ID)
	generation.mu.Unlock()
	if waiter == nil {
		return
	}
	if value.Error != nil {
		waiter <- pendingResponse{err: value.Error}
	} else {
		waiter <- pendingResponse{result: append(json.RawMessage(nil), value.Result...)}
	}
}

func (generation *generation) routeUpdate(id string, raw json.RawMessage) {
	generation.mu.Lock()
	update := generation.updates[id]
	generation.mu.Unlock()
	if update != nil {
		update(append(json.RawMessage(nil), raw...))
	}
}

func (generation *generation) remove(id string) {
	generation.mu.Lock()
	delete(generation.pending, id)
	delete(generation.updates, id)
	generation.mu.Unlock()
}

func (generation *generation) fail(err error) {
	generation.failOnce.Do(func() {
		generation.ui.close()
		generation.mu.Lock()
		pending := generation.pending
		generation.pending = make(map[string]chan pendingResponse)
		generation.updates = make(map[string]func(json.RawMessage))
		generation.mu.Unlock()
		for _, waiter := range pending {
			waiter <- pendingResponse{err: err}
		}
		generation.manager.mu.Lock()
		generation.manager.lastError = err
		generation.manager.mu.Unlock()
		select {
		case generation.handshake <- err:
		default:
		}
		close(generation.done)
	})
}

func (generation *generation) failure() error {
	generation.manager.mu.Lock()
	err := generation.manager.lastError
	generation.manager.mu.Unlock()
	if err == nil {
		return errors.New("extension host stopped")
	}
	return err
}

func (generation *generation) closed() bool {
	generation.mu.Lock()
	defer generation.mu.Unlock()
	return generation.closedLocked()
}

func (generation *generation) closedLocked() bool {
	select {
	case <-generation.done:
		return true
	default:
		return false
	}
}

func (generation *generation) registration(extensionID string) *registrationState {
	generation.registrationsMu.Lock()
	defer generation.registrationsMu.Unlock()
	return generation.registrations[extensionID]
}

func (manager *Manager) report(diagnostic extensions.Diagnostic) {
	if manager.options.OnDiagnostic != nil {
		manager.options.OnDiagnostic(diagnostic)
	}
}

func makeEntries(paths []string) []extensionEntry {
	entries := make([]extensionEntry, len(paths))
	for index, path := range paths {
		entries[index] = extensionEntry{ID: fmt.Sprintf("ext-%d", index+1), Path: path}
	}
	return entries
}

func cloneRegistrationState(value *registrationState) *registrationState {
	if value == nil {
		return nil
	}
	cloned := &registrationState{Path: value.Path}
	cloned.Tools = append([]wireToolDefinition(nil), value.Tools...)
	for index := range cloned.Tools {
		cloned.Tools[index].PromptGuidelines = append([]string(nil), cloned.Tools[index].PromptGuidelines...)
		cloned.Tools[index].Parameters = append(json.RawMessage(nil), cloned.Tools[index].Parameters...)
	}
	cloned.Commands = append([]wireCommand(nil), value.Commands...)
	cloned.Shortcuts = append([]wireShortcut(nil), value.Shortcuts...)
	cloned.Subscriptions = append([]wireSubscription(nil), value.Subscriptions...)
	cloned.Renderers = append([]wireRendererRegistration(nil), value.Renderers...)
	cloned.Providers = make([]wireProviderRegistration, len(value.Providers))
	for index := range value.Providers {
		cloned.Providers[index] = cloneWireProvider(value.Providers[index])
	}
	return cloned
}

func invalidRegistration(err error) *protocolError {
	return &protocolError{Code: "invalid_registration", Message: err.Error()}
}

func stripRegistryPrefix(path string, err error) string {
	prefix := "extensions: load " + path + ": "
	return strings.TrimPrefix(err.Error(), prefix)
}

func processError(err error) error {
	if err == nil {
		return errors.New("process exited")
	}
	return err
}

func materializeHost(agentDir string) (string, error) {
	return materializeSource(agentDir, "host", hostSource)
}

func materializeSource(agentDir, name string, source []byte) (string, error) {
	if agentDir == "" {
		return "", errors.New("agent directory is empty")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(source))
	directory := filepath.Join(agentDir, "host")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, name+"-"+hash+".mjs")
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, source) {
		return path, nil
	}
	temporary, err := os.CreateTemp(directory, "."+name+"-*.mjs")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(source); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func wireValue(value any) (any, error) {
	encoded, err := ai.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return lowerWireKeys(decoded), nil
}

func lowerWireKeys(value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = lowerWireKeys(typed[index])
		}
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, field := range typed {
			result[wireKey(key)] = lowerWireKeys(field)
		}
		return result
	}
	return value
}

func wireKey(value string) string {
	if value == "" {
		return value
	}
	replacements := []struct{ old, new string }{
		{"CWD", "Cwd"}, {"URL", "Url"}, {"UI", "Ui"}, {"API", "Api"}, {"LLM", "Llm"}, {"ID", "Id"},
	}
	for _, replacement := range replacements {
		value = strings.ReplaceAll(value, replacement.old, replacement.new)
	}
	runes := []rune(value)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func decodeEventResult(event extensions.EventType, raw json.RawMessage) (any, error) {
	var target any
	switch event {
	case extensions.EventProjectTrust:
		target = &extensions.ProjectTrustResult{}
	case extensions.EventResourcesDiscover:
		target = &extensions.ResourcesDiscoverResult{}
	case extensions.EventBeforeAgentStart:
		target = &extensions.BeforeAgentStartResult{}
	case extensions.EventToolCall:
		target = &extensions.ToolCallResult{}
	case extensions.EventToolResult:
		target = &extensions.ToolResultResult{}
	case extensions.EventInput:
		target = &extensions.InputResult{}
	case extensions.EventSessionBeforeSwitch:
		target = &extensions.SessionBeforeSwitchResult{}
	case extensions.EventSessionBeforeFork:
		target = &extensions.SessionBeforeForkResult{}
	case extensions.EventSessionBeforeCompact:
		target = &extensions.SessionBeforeCompactResult{}
	case extensions.EventSessionBeforeTree:
		target = &extensions.SessionBeforeTreeResult{}
	default:
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return value, nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, err
	}
	if reflect.ValueOf(target).Elem().IsZero() {
		return nil, nil
	}
	return target, nil
}
