package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

// RPCClientOptions configure the pi process spawned by [NewRPCClient].
type RPCClientOptions struct {
	CLIPath  string
	CWD      string
	Env      map[string]string
	Provider string
	Model    string
	Args     []string
}

type RPCModelInfo struct {
	Provider      ai.ProviderID `json:"provider"`
	ID            string        `json:"id"`
	ContextWindow float64       `json:"contextWindow"`
	Reasoning     bool          `json:"reasoning"`
}

type RPCModelSelection struct {
	Provider ai.ProviderID `json:"provider"`
	ID       string        `json:"id"`
}

type RPCModelCycleResult struct {
	Model         RPCModelSelection     `json:"model"`
	ThinkingLevel ai.ModelThinkingLevel `json:"thinkingLevel"`
	IsScoped      bool                  `json:"isScoped"`
}

type RPCThinkingLevelResult struct {
	Level ai.ModelThinkingLevel `json:"level"`
}

type RPCSessionReplacementResult struct {
	Cancelled bool `json:"cancelled"`
}

type RPCForkResult struct {
	Text      string `json:"text"`
	Cancelled bool   `json:"cancelled"`
}

type RPCForkMessage struct {
	EntryID string `json:"entryId"`
	Text    string `json:"text"`
}

type RPCEntriesResult struct {
	Entries []sessionstore.SessionEntry `json:"entries"`
	LeafID  *string                     `json:"leafId"`
}

type RPCTreeResult struct {
	Tree   []*sessionstore.SessionTreeNode `json:"tree"`
	LeafID *string                         `json:"leafId"`
}

type RPCExportResult struct {
	Path string `json:"path"`
}

// RPCEvent retains an event's exact JSON while exposing its discriminant.
type RPCEvent struct {
	Type string
	JSON json.RawMessage
}

type RPCEventListener func(RPCEvent)

type rpcClientProcess struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	dispatcher *rpcClientDispatcher
	waitDone   chan struct{}
	stdoutDone chan struct{}
}

type rpcClientResult struct {
	response RPCResponse
	data     json.RawMessage
	err      error
}

type rpcClientCommand struct {
	Type               string              `json:"type"`
	Message            *string             `json:"message,omitempty"`
	Images             *[]*ai.ImageContent `json:"images,omitempty"`
	StreamingBehavior  *string             `json:"streamingBehavior,omitempty"`
	ParentSession      *string             `json:"parentSession,omitempty"`
	Provider           *string             `json:"provider,omitempty"`
	ModelID            *string             `json:"modelId,omitempty"`
	Level              *string             `json:"level,omitempty"`
	Mode               *string             `json:"mode,omitempty"`
	CustomInstructions *string             `json:"customInstructions,omitempty"`
	Enabled            *bool               `json:"enabled,omitempty"`
	Command            *string             `json:"command,omitempty"`
	ExcludeFromContext *bool               `json:"excludeFromContext,omitempty"`
	OutputPath         *string             `json:"outputPath,omitempty"`
	SessionPath        *string             `json:"sessionPath,omitempty"`
	EntryID            *string             `json:"entryId,omitempty"`
	Since              *string             `json:"since,omitempty"`
	Name               *string             `json:"name,omitempty"`
	ID                 string              `json:"id"`
}

type rpcClientListener struct {
	id int
	fn RPCEventListener
}

type rpcClientDispatch struct {
	event     RPCEvent
	listeners []rpcClientListener
}

type rpcClientDispatcher struct {
	mu      sync.Mutex
	ready   *sync.Cond
	queue   []rpcClientDispatch
	stopped bool
}

func newRPCClientDispatcher() *rpcClientDispatcher {
	dispatcher := &rpcClientDispatcher{}
	dispatcher.ready = sync.NewCond(&dispatcher.mu)
	go dispatcher.run()
	return dispatcher
}

func (dispatcher *rpcClientDispatcher) enqueue(event RPCEvent, listeners []rpcClientListener) {
	dispatcher.mu.Lock()
	if !dispatcher.stopped {
		dispatcher.queue = append(dispatcher.queue, rpcClientDispatch{event, listeners})
		dispatcher.ready.Signal()
	}
	dispatcher.mu.Unlock()
}

func (dispatcher *rpcClientDispatcher) stop(drain bool) {
	dispatcher.mu.Lock()
	dispatcher.stopped = true
	if !drain {
		dispatcher.queue = nil
	}
	dispatcher.ready.Broadcast()
	dispatcher.mu.Unlock()
}

func (dispatcher *rpcClientDispatcher) run() {
	for {
		dispatcher.mu.Lock()
		for len(dispatcher.queue) == 0 && !dispatcher.stopped {
			dispatcher.ready.Wait()
		}
		if len(dispatcher.queue) == 0 {
			dispatcher.mu.Unlock()
			return
		}
		next := dispatcher.queue[0]
		dispatcher.queue[0] = rpcClientDispatch{}
		dispatcher.queue = dispatcher.queue[1:]
		dispatcher.mu.Unlock()
		dispatchRPCEvent(next)
	}
}

func dispatchRPCEvent(next rpcClientDispatch) {
	defer func() { _ = recover() }() // Upstream swallows listener throws per event.
	for _, listener := range next.listeners {
		listener.fn(next.event)
	}
}

// RPCClient spawns pi in RPC mode and routes its strict-JSONL responses and events.
type RPCClient struct {
	options RPCClientOptions

	mu             sync.Mutex
	process        *rpcClientProcess
	pending        map[string]chan rpcClientResult
	listeners      []rpcClientListener
	nextListener   int
	requestID      int
	exitError      error
	requestTimeout time.Duration
	stopTimeout    time.Duration

	writeMu  sync.Mutex
	stderrMu sync.Mutex
	stderr   strings.Builder
}

func NewRPCClient(options RPCClientOptions) *RPCClient {
	return &RPCClient{
		options: options, pending: make(map[string]chan rpcClientResult),
		requestTimeout: 30 * time.Second, stopTimeout: time.Second,
	}
}

func (client *RPCClient) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	client.mu.Lock()
	if client.process != nil {
		client.mu.Unlock()
		return errors.New("Client already started") //nolint:staticcheck // Upstream text.
	}
	client.exitError = nil
	path := client.options.CLIPath
	if path == "" {
		path = "pi"
	}
	args := []string{"--mode", "rpc"}
	if client.options.Provider != "" {
		args = append(args, "--provider", client.options.Provider)
	}
	if client.options.Model != "" {
		args = append(args, "--model", client.options.Model)
	}
	args = append(args, client.options.Args...)
	command := exec.Command(path, args...)
	command.Dir = client.options.CWD
	command.Env = os.Environ()
	for key, value := range client.options.Env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stderr = io.MultiWriter(rpcClientStderrWriter{client}, os.Stderr)
	stdin, err := command.StdinPipe()
	if err != nil {
		client.mu.Unlock()
		return fmt.Errorf("Agent process error: %v. Stderr: %s", err, client.GetStderr()) //nolint:staticcheck // Upstream text.
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		client.mu.Unlock()
		return fmt.Errorf("Agent process error: %v. Stderr: %s", err, client.GetStderr()) //nolint:staticcheck // Upstream text.
	}
	process := &rpcClientProcess{cmd: command, stdin: stdin, stdout: stdout, waitDone: make(chan struct{}), stdoutDone: make(chan struct{})}
	if err := command.Start(); err != nil {
		client.mu.Unlock()
		return fmt.Errorf("Agent process error: %v. Stderr: %s", err, client.GetStderr()) //nolint:staticcheck // Upstream text.
	}
	process.dispatcher = newRPCClientDispatcher()
	client.process = process
	client.mu.Unlock()
	go client.readRPCOutput(process)
	go client.waitRPCProcess(process)
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-process.waitDone:
		client.mu.Lock()
		exitErr := client.exitError
		client.mu.Unlock()
		return exitErr
	case <-ctx.Done():
		_ = client.Stop()
		return ctx.Err()
	}
}

func (client *RPCClient) Stop() error {
	client.mu.Lock()
	process := client.process
	timeout := client.stopTimeout
	client.mu.Unlock()
	if process == nil {
		return nil
	}
	_ = process.cmd.Process.Signal(syscall.SIGTERM)
	if timeout <= 0 {
		timeout = time.Second
	}
	select {
	case <-process.waitDone:
	case <-time.After(timeout):
		_ = process.cmd.Process.Kill()
	}
	client.mu.Lock()
	if client.process == process {
		client.process = nil
		client.pending = make(map[string]chan rpcClientResult)
	}
	client.mu.Unlock()
	process.dispatcher.stop(false)
	return nil
}

func (client *RPCClient) OnEvent(listener RPCEventListener) func() {
	client.mu.Lock()
	client.nextListener++
	id := client.nextListener
	client.listeners = append(client.listeners, rpcClientListener{id, listener})
	client.mu.Unlock()
	return func() {
		client.mu.Lock()
		client.listeners = slices.DeleteFunc(client.listeners, func(listener rpcClientListener) bool { return listener.id == id })
		client.mu.Unlock()
	}
}

func (client *RPCClient) GetStderr() string {
	client.stderrMu.Lock()
	defer client.stderrMu.Unlock()
	return client.stderr.String()
}

func (client *RPCClient) Prompt(ctx context.Context, message string, images []*ai.ImageContent) error {
	return client.sendOnly(ctx, RPCCommand{Type: "prompt", Message: message, Images: images})
}

func (client *RPCClient) Steer(ctx context.Context, message string, images []*ai.ImageContent) error {
	return client.sendOnly(ctx, RPCCommand{Type: "steer", Message: message, Images: images})
}

func (client *RPCClient) FollowUp(ctx context.Context, message string, images []*ai.ImageContent) error {
	return client.sendOnly(ctx, RPCCommand{Type: "follow_up", Message: message, Images: images})
}

func (client *RPCClient) Abort(ctx context.Context) error {
	return client.sendOnly(ctx, RPCCommand{Type: "abort"})
}

func (client *RPCClient) NewSession(ctx context.Context, parentSession *string) (RPCSessionReplacementResult, error) {
	command := RPCCommand{Type: "new_session"}
	if parentSession != nil {
		command.ParentSession, command.parentSessionSet = *parentSession, true
	}
	return rpcClientData[RPCSessionReplacementResult](ctx, client, command)
}

func (client *RPCClient) GetState(ctx context.Context) (RPCSessionState, error) {
	return rpcClientData[RPCSessionState](ctx, client, RPCCommand{Type: "get_state"})
}

func (client *RPCClient) SetModel(ctx context.Context, provider ai.ProviderID, modelID string) (RPCModelSelection, error) {
	return rpcClientData[RPCModelSelection](ctx, client, RPCCommand{Type: "set_model", Provider: string(provider), ModelID: modelID})
}

func (client *RPCClient) CycleModel(ctx context.Context) (*RPCModelCycleResult, error) {
	return rpcClientData[*RPCModelCycleResult](ctx, client, RPCCommand{Type: "cycle_model"})
}

func (client *RPCClient) GetAvailableModels(ctx context.Context) ([]RPCModelInfo, error) {
	response, err := rpcClientData[struct {
		Models []RPCModelInfo `json:"models"`
	}](ctx, client, RPCCommand{Type: "get_available_models"})
	return response.Models, err
}

func (client *RPCClient) SetThinkingLevel(ctx context.Context, level ai.ModelThinkingLevel) error {
	return client.sendOnly(ctx, RPCCommand{Type: "set_thinking_level", Level: string(level)})
}

func (client *RPCClient) CycleThinkingLevel(ctx context.Context) (*RPCThinkingLevelResult, error) {
	return rpcClientData[*RPCThinkingLevelResult](ctx, client, RPCCommand{Type: "cycle_thinking_level"})
}

func (client *RPCClient) GetAvailableThinkingLevels(ctx context.Context) ([]ai.ModelThinkingLevel, error) {
	response, err := rpcClientData[RPCThinkingLevels](ctx, client, RPCCommand{Type: "get_available_thinking_levels"})
	return response.Levels, err
}

func (client *RPCClient) SetSteeringMode(ctx context.Context, mode agent.QueueMode) error {
	return client.sendOnly(ctx, RPCCommand{Type: "set_steering_mode", Mode: string(mode)})
}

func (client *RPCClient) SetFollowUpMode(ctx context.Context, mode agent.QueueMode) error {
	return client.sendOnly(ctx, RPCCommand{Type: "set_follow_up_mode", Mode: string(mode)})
}

func (client *RPCClient) Compact(ctx context.Context, customInstructions *string) (harness.CompactionResult, error) {
	command := RPCCommand{Type: "compact"}
	if customInstructions != nil {
		command.CustomInstructions, command.customInstructionsSet = *customInstructions, true
	}
	return rpcClientData[harness.CompactionResult](ctx, client, command)
}

func (client *RPCClient) SetAutoCompaction(ctx context.Context, enabled bool) error {
	return client.sendOnly(ctx, RPCCommand{Type: "set_auto_compaction", Enabled: &enabled})
}

func (client *RPCClient) SetAutoRetry(ctx context.Context, enabled bool) error {
	return client.sendOnly(ctx, RPCCommand{Type: "set_auto_retry", Enabled: &enabled})
}

func (client *RPCClient) AbortRetry(ctx context.Context) error {
	return client.sendOnly(ctx, RPCCommand{Type: "abort_retry"})
}

func (client *RPCClient) Bash(ctx context.Context, command string) (tools.BashResult, error) {
	return rpcClientData[tools.BashResult](ctx, client, RPCCommand{Type: "bash", Command: command})
}

func (client *RPCClient) AbortBash(ctx context.Context) error {
	return client.sendOnly(ctx, RPCCommand{Type: "abort_bash"})
}

func (client *RPCClient) GetSessionStats(ctx context.Context) (codingagent.SessionStats, error) {
	return rpcClientData[codingagent.SessionStats](ctx, client, RPCCommand{Type: "get_session_stats"})
}

func (client *RPCClient) ExportHTML(ctx context.Context, outputPath *string) (RPCExportResult, error) {
	command := RPCCommand{Type: "export_html"}
	if outputPath != nil {
		command.OutputPath, command.outputPathSet = *outputPath, true
	}
	return rpcClientData[RPCExportResult](ctx, client, command)
}

func (client *RPCClient) SwitchSession(ctx context.Context, sessionPath string) (RPCSessionReplacementResult, error) {
	return rpcClientData[RPCSessionReplacementResult](ctx, client, RPCCommand{Type: "switch_session", SessionPath: sessionPath})
}

func (client *RPCClient) Fork(ctx context.Context, entryID string) (RPCForkResult, error) {
	return rpcClientData[RPCForkResult](ctx, client, RPCCommand{Type: "fork", EntryID: entryID})
}

func (client *RPCClient) Clone(ctx context.Context) (RPCSessionReplacementResult, error) {
	return rpcClientData[RPCSessionReplacementResult](ctx, client, RPCCommand{Type: "clone"})
}

func (client *RPCClient) GetForkMessages(ctx context.Context) ([]RPCForkMessage, error) {
	response, err := rpcClientData[struct {
		Messages []RPCForkMessage `json:"messages"`
	}](ctx, client, RPCCommand{Type: "get_fork_messages"})
	return response.Messages, err
}

func (client *RPCClient) GetEntries(ctx context.Context, since *string) (RPCEntriesResult, error) {
	return rpcClientData[RPCEntriesResult](ctx, client, RPCCommand{Type: "get_entries", Since: since})
}

func (client *RPCClient) GetTree(ctx context.Context) (RPCTreeResult, error) {
	return rpcClientData[RPCTreeResult](ctx, client, RPCCommand{Type: "get_tree"})
}

func (client *RPCClient) GetLastAssistantText(ctx context.Context) (*string, error) {
	response, err := rpcClientData[struct {
		Text *string `json:"text"`
	}](ctx, client, RPCCommand{Type: "get_last_assistant_text"})
	return response.Text, err
}

func (client *RPCClient) SetSessionName(ctx context.Context, name string) error {
	return client.sendOnly(ctx, RPCCommand{Type: "set_session_name", Name: name})
}

func (client *RPCClient) GetMessages(ctx context.Context) (agent.AgentMessages, error) {
	response, err := rpcClientData[struct {
		Messages agent.AgentMessages `json:"messages"`
	}](ctx, client, RPCCommand{Type: "get_messages"})
	return response.Messages, err
}

func (client *RPCClient) GetCommands(ctx context.Context) ([]RPCSlashCommand, error) {
	response, err := rpcClientData[struct {
		Commands []RPCSlashCommand `json:"commands"`
	}](ctx, client, RPCCommand{Type: "get_commands"})
	return response.Commands, err
}

func (client *RPCClient) WaitForIdle(ctx context.Context) error {
	ctx, cancel := rpcClientEventContext(ctx)
	defer cancel()
	done := make(chan struct{})
	var once sync.Once
	unsubscribe := client.OnEvent(func(event RPCEvent) {
		if event.Type == string(codingagent.EventAgentSettled) {
			once.Do(func() { close(done) })
		}
	})
	defer unsubscribe()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("Timeout waiting for agent to become idle. Stderr: %s: %w", client.GetStderr(), ctx.Err()) //nolint:staticcheck // Upstream text.
	}
}

func (client *RPCClient) CollectEvents(ctx context.Context) ([]RPCEvent, error) {
	return client.collectEvents(ctx, nil)
}

func (client *RPCClient) PromptAndWait(ctx context.Context, message string, images []*ai.ImageContent) ([]RPCEvent, error) {
	return client.collectEvents(ctx, func() error { return client.Prompt(ctx, message, images) })
}

func (client *RPCClient) collectEvents(ctx context.Context, start func() error) ([]RPCEvent, error) {
	ctx, cancel := rpcClientEventContext(ctx)
	defer cancel()
	done := make(chan struct{})
	var mu sync.Mutex
	var once sync.Once
	events := make([]RPCEvent, 0)
	unsubscribe := client.OnEvent(func(event RPCEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		if event.Type == string(codingagent.EventAgentSettled) {
			once.Do(func() { close(done) })
		}
	})
	defer unsubscribe()
	if start != nil {
		if err := start(); err != nil {
			return nil, err
		}
	}
	select {
	case <-done:
		mu.Lock()
		defer mu.Unlock()
		return append([]RPCEvent(nil), events...), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("Timeout collecting events. Stderr: %s: %w", client.GetStderr(), ctx.Err()) //nolint:staticcheck // Upstream text.
	}
}

func rpcClientEventContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, set := ctx.Deadline(); set {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Minute)
}

func (client *RPCClient) send(ctx context.Context, command RPCCommand) (rpcClientResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client.mu.Lock()
	process := client.process
	if process == nil {
		client.mu.Unlock()
		return rpcClientResult{}, errors.New("Client not started") //nolint:staticcheck // Upstream text.
	}
	if client.exitError != nil {
		err := client.exitError
		client.mu.Unlock()
		return rpcClientResult{}, err
	}
	client.requestID++
	command.ID = fmt.Sprintf("req_%d", client.requestID)
	command.HasID = true
	result := make(chan rpcClientResult, 1)
	client.pending[command.ID] = result
	timeout := client.requestTimeout
	client.mu.Unlock()

	encoded, err := marshalRPCClientCommand(command)
	if err == nil {
		encoded = append(encoded, '\n')
		client.writeMu.Lock()
		_, err = process.stdin.Write(encoded)
		client.writeMu.Unlock()
	}
	if err != nil {
		client.failRPCProcess(process, fmt.Errorf("Agent process stdin error: %v. Stderr: %s", err, client.GetStderr())) //nolint:staticcheck // Upstream text.
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-result:
		return response, response.err
	case <-timer.C:
		client.removeRPCPending(command.ID)
		return rpcClientResult{}, fmt.Errorf("Timeout waiting for response to %s. Stderr: %s", command.Type, client.GetStderr()) //nolint:staticcheck // Upstream text.
	case <-ctx.Done():
		client.removeRPCPending(command.ID)
		return rpcClientResult{}, ctx.Err()
	}
}

func marshalRPCClientCommand(command RPCCommand) ([]byte, error) { //nolint:cyclop // The wire union is discriminated by type upstream.
	wire := rpcClientCommand{Type: command.Type, ID: command.ID}
	switch command.Type {
	case "prompt", "steer", "follow_up":
		wire.Message = &command.Message
		if command.Images != nil {
			wire.Images = &command.Images
		}
		if command.StreamingBehavior != "" {
			wire.StreamingBehavior = &command.StreamingBehavior
		}
	case "new_session":
		if command.parentSessionSet {
			wire.ParentSession = &command.ParentSession
		}
	case "set_model":
		wire.Provider, wire.ModelID = &command.Provider, &command.ModelID
	case "set_thinking_level":
		wire.Level = &command.Level
	case "set_steering_mode", "set_follow_up_mode":
		wire.Mode = &command.Mode
	case "compact":
		if command.customInstructionsSet {
			wire.CustomInstructions = &command.CustomInstructions
		}
	case "set_auto_compaction", "set_auto_retry":
		wire.Enabled = command.Enabled
	case "bash":
		wire.Command, wire.ExcludeFromContext = &command.Command, command.ExcludeFromContext
	case "export_html":
		if command.outputPathSet {
			wire.OutputPath = &command.OutputPath
		}
	case "switch_session":
		wire.SessionPath = &command.SessionPath
	case "fork":
		wire.EntryID = &command.EntryID
	case "get_entries":
		wire.Since = command.Since
	case "set_session_name":
		wire.Name = &command.Name
	}
	return ai.Marshal(wire)
}

func (client *RPCClient) sendOnly(ctx context.Context, command RPCCommand) error {
	_, err := client.send(ctx, command)
	return err
}

func rpcClientData[T any](ctx context.Context, client *RPCClient, command RPCCommand) (T, error) {
	var value T
	result, err := client.send(ctx, command)
	if err != nil {
		return value, err
	}
	if !result.response.Success {
		return value, errors.New(result.response.Error)
	}
	if err := json.Unmarshal(result.data, &value); err != nil {
		return value, err
	}
	return value, nil
}

func (client *RPCClient) readRPCOutput(process *rpcClientProcess) {
	defer close(process.stdoutDone)
	lines := make(chan []byte)
	readErrors := make(chan error, 1)
	go readStrictJSONLines(process.stdout, lines, readErrors)
	for line := range lines {
		client.handleRPCLine(process, line)
	}
	<-readErrors
}

func (client *RPCClient) handleRPCLine(process *rpcClientProcess, line []byte) {
	if !json.Valid(line) {
		return
	}
	var header struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Command string          `json:"command"`
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	_ = json.Unmarshal(line, &header)
	if header.Type == "response" && header.ID != "" {
		client.mu.Lock()
		pending := client.pending[header.ID]
		if pending != nil {
			delete(client.pending, header.ID)
		}
		client.mu.Unlock()
		if pending != nil {
			pending <- rpcClientResult{response: RPCResponse{ID: header.ID, Type: header.Type, Command: header.Command, Success: header.Success, Error: header.Error, HasID: true, HasData: header.Data != nil}, data: header.Data}
			return
		}
	}
	event := RPCEvent{Type: header.Type, JSON: append(json.RawMessage(nil), line...)}
	client.mu.Lock()
	listeners := append([]rpcClientListener(nil), client.listeners...)
	client.mu.Unlock()
	process.dispatcher.enqueue(event, listeners)
}

type rpcClientStderrWriter struct{ client *RPCClient }

func (writer rpcClientStderrWriter) Write(data []byte) (int, error) {
	writer.client.stderrMu.Lock()
	defer writer.client.stderrMu.Unlock()
	return writer.client.stderr.Write(data)
}

func (client *RPCClient) waitRPCProcess(process *rpcClientProcess) {
	_ = process.cmd.Wait()
	<-process.stdoutDone
	process.dispatcher.stop(true)
	client.failRPCProcess(process, rpcClientExitError(process.cmd.ProcessState, client.GetStderr()))
	close(process.waitDone)
}

func (client *RPCClient) failRPCProcess(process *rpcClientProcess, err error) {
	client.mu.Lock()
	if client.process != process {
		client.mu.Unlock()
		return
	}
	client.exitError = err
	pending := client.pending
	client.pending = make(map[string]chan rpcClientResult)
	client.mu.Unlock()
	for _, request := range pending {
		request <- rpcClientResult{err: err}
	}
}

func (client *RPCClient) removeRPCPending(id string) {
	client.mu.Lock()
	delete(client.pending, id)
	client.mu.Unlock()
}

func rpcClientExitError(state *os.ProcessState, stderr string) error {
	code, signal := "null", "null"
	if state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal = rpcClientSignalName(status.Signal())
		} else {
			code = fmt.Sprint(state.ExitCode())
		}
	}
	return fmt.Errorf("Agent process exited (code=%s signal=%s). Stderr: %s", code, signal, stderr) //nolint:staticcheck // Upstream text.
}

func rpcClientSignalName(signal syscall.Signal) string {
	switch signal {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGINT:
		return "SIGINT"
	default:
		return fmt.Sprintf("SIG%d", signal)
	}
}
