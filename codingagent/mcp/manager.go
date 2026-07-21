package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
	mcpjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ServerState string

const (
	ServerConnecting ServerState = "connecting"
	ServerConnected  ServerState = "connected"
	ServerError      ServerState = "error"
	ServerStopped    ServerState = "stopped"
)

type ServerStatus struct {
	Name      string
	Transport string
	State     ServerState
	Tools     []string
	Error     string
}

type connectFunc func(
	context.Context,
	context.Context,
	ServerConfig,
	*mcpsdk.ClientOptions,
	progressTracker,
) (*mcpsdk.ClientSession, error)

type progressTracker struct {
	manager *Manager
}

type serverConnection struct {
	connectMu sync.Mutex // serializes connect attempts for this server only

	config      ServerConfig
	session     *mcpsdk.ClientSession
	state       ServerState
	err         string
	tools       map[string]string
	definitions []extensions.ToolDefinition
}

type progressRegistration struct {
	token     string
	update    agent.AgentToolUpdateCallback
	pending   int
	unhandled int
	drained   chan struct{}
	sealed    bool
}

// Manager owns all MCP client sessions for one coding-agent session.
type Manager struct {
	cwd     string
	order   []string
	servers map[string]*serverConnection
	connect connectFunc

	ctx    context.Context
	cancel context.CancelFunc

	mu                sync.Mutex
	activeMu          sync.Mutex // serializes read-modify-write of the active tool list
	warnOutput        io.Writer
	api               extensions.API
	closed            bool
	nextProgressToken uint64
	progress          map[string]*progressRegistration
}

func NewManager(cwd string, configs []ServerConfig) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		cwd: cwd, servers: make(map[string]*serverConnection, len(configs)), connect: defaultConnect,
		ctx: ctx, cancel: cancel, progress: make(map[string]*progressRegistration), warnOutput: os.Stderr,
	}
	for _, config := range configs {
		copy := cloneServerConfig(config)
		if copy.TimeoutMS == 0 {
			copy.TimeoutMS = defaultConnectTimeoutMS
		}
		copy.CWD = resolveCommandCWD(cwd, copy.CWD)
		manager.order = append(manager.order, copy.Name)
		manager.servers[copy.Name] = &serverConnection{config: copy, state: ServerStopped, tools: make(map[string]string)}
	}
	sort.Strings(manager.order)
	return manager
}

func cloneServerConfig(config ServerConfig) ServerConfig {
	config.Args = append([]string(nil), config.Args...)
	config.Env = cloneStrings(config.Env)
	config.Headers = cloneStrings(config.Headers)
	if config.MaxRetries != nil {
		value := *config.MaxRetries
		config.MaxRetries = &value
	}
	return config
}

// Extension returns the compiled extension factory for this manager. A
// manager with no configured servers registers nothing and performs no MCP
// connection work.
func (manager *Manager) Extension() extensions.Factory {
	return func(api extensions.API) error {
		if len(manager.order) == 0 {
			return nil
		}
		manager.mu.Lock()
		manager.api = api
		manager.mu.Unlock()
		api.RegisterCommand("mcp", extensions.Command{
			Description:            "Show MCP server status or reconnect a server",
			GetArgumentCompletions: manager.completeCommand,
			Handler:                manager.handleCommand,
		})
		api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, manager.Close()
		})
		if err := manager.Start(context.Background()); err != nil {
			for _, line := range strings.Split(err.Error(), "\n") {
				_, _ = fmt.Fprintf(manager.warnOutput, "Warning: mcp: %s\n", line)
			}
		}
		return nil
	}
}

// Start connects every configured server concurrently, each bounded by its own
// timeoutMs, and registers the tools available from the successful sessions.
// Per-server failures remain visible in Status and do not prevent other
// servers or the coding agent from starting. Calling Start again re-registers
// the tools of already-connected servers against the currently bound API, so a
// factory re-run against a fresh registry keeps every MCP tool exposed.
func (manager *Manager) Start(ctx context.Context) error {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return errors.New("mcp: manager is closed")
	}
	if manager.api == nil && len(manager.order) > 0 {
		manager.mu.Unlock()
		return errors.New("mcp: extension is not registered")
	}
	names := append([]string(nil), manager.order...)
	manager.mu.Unlock()
	failures := make([]error, len(names))
	var group sync.WaitGroup
	for index, name := range names {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := manager.connectServer(ctx, name, false); err != nil {
				failures[index] = fmt.Errorf("%s: %w", name, err)
			}
		}()
	}
	group.Wait()
	return errors.Join(failures...)
}

func (manager *Manager) connectServer(ctx context.Context, name string, replace bool) error {
	manager.mu.Lock()
	connection, exists := manager.servers[name]
	manager.mu.Unlock()
	if !exists {
		return fmt.Errorf("unknown server %q", name)
	}
	connection.connectMu.Lock()
	defer connection.connectMu.Unlock()

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return errors.New("manager is closed")
	}
	if !replace && connection.session != nil && connection.state == ServerConnected {
		// A factory re-run rebinds manager.api to a fresh registry, so the
		// already-discovered tools must be registered again or they would only
		// exist in the discarded previous registry.
		definitions := append([]extensions.ToolDefinition(nil), connection.definitions...)
		api := manager.api
		manager.mu.Unlock()
		for _, definition := range definitions {
			api.RegisterTool(definition)
		}
		return nil
	}
	previous := connection.session
	previousTools := cloneStrings(connection.tools)
	connection.session = nil
	connection.state = ServerConnecting
	connection.err = ""
	config := cloneServerConfig(connection.config)
	api := manager.api
	manager.mu.Unlock()
	if replace && previous != nil {
		_ = previous.Close()
	}

	connectCtx, cancelConnect := context.WithCancel(ctx)
	stopClose := context.AfterFunc(manager.ctx, cancelConnect)
	cancelTimeout := func() {}
	if config.TimeoutMS > 0 {
		connectCtx, cancelTimeout = context.WithTimeout(connectCtx, time.Duration(config.TimeoutMS)*time.Millisecond)
	}
	defer func() {
		cancelTimeout()
		cancelConnect()
		stopClose()
	}()
	options := &mcpsdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) {
			go manager.refreshServerTools(name)
		},
		ProgressNotificationHandler: func(_ context.Context, request *mcpsdk.ProgressNotificationClientRequest) {
			manager.handleProgress(request.Params)
		},
	}
	session, err := manager.connect(connectCtx, manager.ctx, config, options, progressTracker{manager: manager})
	if err != nil {
		manager.setServerUnavailable(name, err)
		if replace {
			manager.replaceActiveTools(previousTools, nil)
		}
		return err
	}
	tools, err := listTools(connectCtx, session)
	if err != nil {
		_ = session.Close()
		manager.setServerUnavailable(name, err)
		if replace {
			manager.replaceActiveTools(previousTools, nil)
		}
		return err
	}
	definitions, names, err := manager.toolDefinitions(name, tools)
	if err != nil {
		_ = session.Close()
		manager.setServerUnavailable(name, err)
		if replace {
			manager.replaceActiveTools(previousTools, nil)
		}
		return err
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		_ = session.Close()
		return errors.New("manager is closed")
	}
	connection = manager.servers[name]
	connection.session = session
	connection.state = ServerConnected
	connection.err = ""
	connection.tools = names
	connection.definitions = definitions
	manager.mu.Unlock()
	for _, definition := range definitions {
		api.RegisterTool(definition)
	}
	if replace {
		manager.replaceActiveTools(previousTools, names)
	}
	return nil
}

func defaultConnect(
	connectCtx, lifecycleCtx context.Context,
	config ServerConfig,
	options *mcpsdk.ClientOptions,
	tracker progressTracker,
) (*mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pigo", Version: "0.1.0"}, options)
	var transport mcpsdk.Transport
	if config.Command != "" {
		command := exec.CommandContext(lifecycleCtx, config.Command, config.Args...)
		if config.CWD != "" {
			command.Dir = config.CWD
		}
		command.Env = mergedEnvironment(config.Env)
		transport = &mcpsdk.CommandTransport{Command: command}
	} else {
		base := headerRoundTripper{base: http.DefaultTransport, headers: config.Headers}
		httpClient := &http.Client{Transport: progressRoundTripper{base: base, manager: tracker.manager}}
		transport = &mcpsdk.StreamableClientTransport{Endpoint: config.URL, HTTPClient: httpClient, MaxRetries: sdkMaxRetries(config.MaxRetries)}
		return client.Connect(connectCtx, transport, nil)
	}
	return client.Connect(connectCtx, tracker.wrapTransport(transport), nil)
}

// sdkMaxRetries translates the configured maxRetries into the go-sdk field,
// where 0 means "use the default of 5" and only a negative value disables
// retries. A user's explicit 0 therefore maps to the disabled sentinel.
func sdkMaxRetries(configured *int) int {
	if configured == nil {
		return 0 // SDK default
	}
	if *configured <= 0 {
		return -1 // fail fast: no reconnect retries
	}
	return *configured
}

type progressTransport struct {
	transport mcpsdk.Transport
	manager   *Manager
}

func (transport progressTransport) Connect(ctx context.Context) (mcpsdk.Connection, error) {
	connection, err := transport.transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &progressConnection{Connection: connection, manager: transport.manager}, nil
}

type progressConnection struct {
	mcpsdk.Connection
	manager *Manager
}

func (connection *progressConnection) Read(ctx context.Context) (mcpjsonrpc.Message, error) {
	message, err := connection.Connection.Read(ctx)
	if err != nil {
		return nil, err
	}
	connection.manager.observeProgressMessage(message)
	return message, nil
}

func (manager *Manager) observeProgressMessage(message mcpjsonrpc.Message) {
	request, ok := message.(*mcpjsonrpc.Request)
	if !ok || request.IsCall() || request.Method != "notifications/progress" {
		return
	}
	var params mcpsdk.ProgressNotificationParams
	if err := json.Unmarshal(request.Params, &params); err == nil {
		manager.progressRead(fmt.Sprint(params.ProgressToken))
	}
}

func (tracker progressTracker) wrapTransport(transport mcpsdk.Transport) mcpsdk.Transport {
	if _, streamable := transport.(*mcpsdk.StreamableClientTransport); streamable {
		return transport
	}
	return progressTransport{transport: transport, manager: tracker.manager}
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (transport headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	for name, value := range transport.headers {
		copy.Header.Set(name, value)
	}
	return transport.base.RoundTrip(copy)
}

type progressRoundTripper struct {
	base    http.RoundTripper
	manager *Manager
}

func (transport progressRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || response.Body == nil {
		return response, err
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		response.Body = &progressEventBody{ReadCloser: response.Body, manager: transport.manager}
	}
	return response, nil
}

type progressEventBody struct {
	io.ReadCloser
	manager      *Manager
	line         []byte
	eventName    string
	eventData    []byte
	eventInvalid bool
}

func (body *progressEventBody) Read(buffer []byte) (int, error) {
	read, err := body.ReadCloser.Read(buffer)
	body.observe(buffer[:read], errors.Is(err, io.EOF))
	return read, err
}

func (body *progressEventBody) observe(chunk []byte, eof bool) {
	body.line = append(body.line, chunk...)
	for {
		end := bytes.IndexByte(body.line, '\n')
		if end < 0 {
			break
		}
		body.observeLine(body.line[:end])
		body.line = body.line[end+1:]
	}
	if !eof {
		return
	}
	if len(body.line) > 0 {
		body.observeLine(body.line)
		body.line = nil
	}
	body.observeEvent()
}

func (body *progressEventBody) observeLine(line []byte) {
	// This must match go-sdk's scanEvents field parsing or observer counts can
	// diverge from the notifications the SDK actually dispatches.
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 {
		body.observeEvent()
		return
	}
	field, value, found := bytes.Cut(line, []byte{':'})
	if !found {
		body.eventInvalid = true
		return
	}
	value = bytes.TrimSpace(value)
	switch string(field) {
	case "event":
		body.eventName = string(value)
	case "data":
		if body.eventData != nil {
			body.eventData = append(body.eventData, '\n')
		}
		body.eventData = append(body.eventData, value...)
	}
}

func (body *progressEventBody) observeEvent() {
	if !body.eventInvalid && len(body.eventData) > 0 && (body.eventName == "" || body.eventName == "message") {
		if message, err := mcpjsonrpc.DecodeMessage(body.eventData); err == nil {
			body.manager.observeProgressMessage(message)
		}
	}
	body.eventName = ""
	body.eventData = nil
	body.eventInvalid = false
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if index := strings.IndexByte(entry, '='); index >= 0 {
			values[entry[:index]] = entry[index+1:]
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func listTools(ctx context.Context, session *mcpsdk.ClientSession) ([]*mcpsdk.Tool, error) {
	var tools []*mcpsdk.Tool
	cursor := ""
	for {
		result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		cursor = result.NextCursor
	}
}

func (manager *Manager) toolDefinitions(server string, tools []*mcpsdk.Tool) ([]extensions.ToolDefinition, map[string]string, error) {
	definitions := make([]extensions.ToolDefinition, 0, len(tools))
	names := make(map[string]string, len(tools))
	for _, tool := range tools {
		if tool == nil || tool.Name == "" {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, nil, fmt.Errorf("tool %q input schema: %w", tool.Name, err)
		}
		if string(schema) == "null" {
			schema = []byte("{}")
		}
		registered := registeredToolName(server, tool.Name)
		original := tool.Name
		label := tool.Title
		if label == "" && tool.Annotations != nil {
			label = tool.Annotations.Title
		}
		if label == "" {
			label = original
		}
		definitions = append(definitions, extensions.ToolDefinition{
			Name: registered, Label: label, Description: tool.Description, Parameters: jsonschema.Schema(schema),
			ExecutionMode: agent.ToolExecutionParallel,
			Execute: func(ctx context.Context, toolCallID string, args any, update agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
				return manager.execute(ctx, server, original, toolCallID, args, update)
			},
		})
		names[original] = registered
	}
	return definitions, names, nil
}

func registeredToolName(server, tool string) string {
	digest := sha256.Sum256([]byte(server + "\x00" + tool))
	return "mcp__" + toolPart(server, 16) + "__" + toolPart(tool, 25) + "_" + hex.EncodeToString(digest[:4])
}

func toolPart(value string, limit int) string {
	var result strings.Builder
	for _, r := range value {
		if result.Len() >= limit {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			result.WriteRune(r)
		} else {
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "server"
	}
	return result.String()
}

func (manager *Manager) execute(
	ctx context.Context,
	server, tool, toolCallID string,
	args any,
	update agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	manager.mu.Lock()
	connection := manager.servers[server]
	if connection == nil {
		manager.mu.Unlock()
		return agent.AgentToolResult{}, fmt.Errorf("mcp: unknown server %q", server)
	}
	session := connection.session
	manager.mu.Unlock()
	if session == nil {
		if err := manager.connectServer(ctx, server, true); err != nil {
			return agent.AgentToolResult{}, err
		}
		manager.mu.Lock()
		connection = manager.servers[server]
		session = connection.session
		manager.mu.Unlock()
	}
	manager.mu.Lock()
	_, available := connection.tools[tool]
	manager.mu.Unlock()
	if !available {
		return agent.AgentToolResult{}, fmt.Errorf("mcp: tool %q is no longer available from server %q", tool, server)
	}
	manager.mu.Lock()
	manager.nextProgressToken++
	token := fmt.Sprintf("%s:%s:%d", server, toolCallID, manager.nextProgressToken)
	var registration *progressRegistration
	if update != nil {
		registration = &progressRegistration{token: token, update: update}
		manager.progress[token] = registration
	}
	manager.mu.Unlock()
	if registration != nil {
		defer manager.dropProgress(registration)
	}
	params := &mcpsdk.CallToolParams{Name: tool, Arguments: args}
	params.SetProgressToken(token)
	result, err := session.CallTool(ctx, params)
	if registration != nil && err == nil {
		if waitErr := manager.waitForProgress(ctx, registration); waitErr != nil {
			err = waitErr
		}
	}
	if err != nil {
		if isConnectionDead(err) {
			manager.mu.Lock()
			previousTools := cloneStrings(connection.tools)
			manager.mu.Unlock()
			manager.setServerUnavailable(server, err)
			manager.replaceActiveTools(previousTools, nil)
		} else {
			manager.setServerError(server, err)
		}
		return agent.AgentToolResult{}, err
	}
	mapped := mapToolResult(server, tool, result)
	if result != nil && result.IsError {
		return agent.AgentToolResult{}, errors.New(toolResultText(mapped.Content))
	}
	return mapped, nil
}

// isConnectionDead reports whether a tool call failed because the transport
// itself is gone (closed connection, EOF or broken pipe from a dead child), in
// which case the server's tools are deactivated immediately instead of only
// after a second failing call.
func isConnectionDead(err error) bool {
	var exitError *exec.ExitError
	return errors.Is(err, mcpsdk.ErrConnectionClosed) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) || errors.As(err, &exitError)
}

func (manager *Manager) handleProgress(params *mcpsdk.ProgressNotificationParams) {
	if params == nil {
		return
	}
	token := fmt.Sprint(params.ProgressToken)
	manager.mu.Lock()
	registration := manager.progress[token]
	if registration == nil || registration.unhandled == 0 {
		manager.mu.Unlock()
		return
	}
	registration.unhandled--
	manager.mu.Unlock()
	defer manager.progressHandled(registration)
	message := params.Message
	if message == "" {
		if params.Total > 0 {
			message = fmt.Sprintf("MCP progress: %g/%g", params.Progress, params.Total)
		} else {
			message = fmt.Sprintf("MCP progress: %g", params.Progress)
		}
	}
	registration.update(agent.AgentToolResult{
		Content: textToolContent(message),
		Details: map[string]any{"progress": params.Progress, "total": params.Total, "message": params.Message},
	})
}

func (manager *Manager) progressRead(token string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	registration := manager.progress[token]
	if registration == nil || registration.sealed {
		return
	}
	if registration.pending == 0 {
		registration.drained = make(chan struct{})
	}
	registration.pending++
	registration.unhandled++
}

func (manager *Manager) progressHandled(registration *progressRegistration) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if registration.pending == 0 {
		return
	}
	registration.pending--
	if registration.pending == 0 {
		close(registration.drained)
		if registration.sealed && manager.progress[registration.token] == registration {
			delete(manager.progress, registration.token)
		}
	}
}

func (manager *Manager) waitForProgress(ctx context.Context, registration *progressRegistration) error {
	manager.mu.Lock()
	// JSON responses and standalone SSE have no cross-stream barrier, so only
	// notifications observed before settlement belong to this tool execution.
	registration.sealed = true
	if registration.pending == 0 {
		if manager.progress[registration.token] == registration {
			delete(manager.progress, registration.token)
		}
		manager.mu.Unlock()
		return nil
	}
	drained := registration.drained
	manager.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-manager.ctx.Done():
		return manager.ctx.Err()
	}
}

func (manager *Manager) dropProgress(registration *progressRegistration) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.progress[registration.token] == registration {
		delete(manager.progress, registration.token)
	}
}

func (manager *Manager) refreshServerTools(name string) {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return
	}
	connection := manager.servers[name]
	session := connection.session
	previous := cloneStrings(connection.tools)
	api := manager.api
	manager.mu.Unlock()
	if session == nil || api == nil {
		return
	}
	ctx, cancel := context.WithTimeout(manager.ctx, time.Duration(connection.config.TimeoutMS)*time.Millisecond)
	defer cancel()
	tools, err := listTools(ctx, session)
	if err != nil {
		manager.setServerError(name, err)
		return
	}
	definitions, names, err := manager.toolDefinitions(name, tools)
	if err != nil {
		manager.setServerError(name, err)
		return
	}
	manager.mu.Lock()
	if manager.closed || manager.servers[name].session != session {
		manager.mu.Unlock()
		return
	}
	manager.servers[name].tools = names
	manager.servers[name].state = ServerConnected
	manager.servers[name].err = ""
	manager.servers[name].definitions = definitions
	manager.mu.Unlock()
	for _, definition := range definitions {
		api.RegisterTool(definition)
	}
	manager.replaceActiveTools(previous, names)
}

func (manager *Manager) replaceActiveTools(previous, current map[string]string) {
	manager.mu.Lock()
	api := manager.api
	manager.mu.Unlock()
	if api == nil {
		return
	}
	manager.activeMu.Lock()
	defer manager.activeMu.Unlock()
	active, err := api.GetActiveTools()
	if err != nil {
		return
	}
	remove := make(map[string]struct{}, len(previous))
	for _, name := range previous {
		remove[name] = struct{}{}
	}
	updated := make([]string, 0, len(active)+len(current))
	seen := make(map[string]struct{}, len(active)+len(current))
	for _, name := range active {
		if _, removed := remove[name]; removed {
			continue
		}
		if _, exists := seen[name]; !exists {
			seen[name] = struct{}{}
			updated = append(updated, name)
		}
	}
	currentNames := make([]string, 0, len(current))
	for _, name := range current {
		currentNames = append(currentNames, name)
	}
	sort.Strings(currentNames)
	for _, name := range currentNames {
		if _, exists := seen[name]; !exists {
			seen[name] = struct{}{}
			updated = append(updated, name)
		}
	}
	_ = api.SetActiveTools(updated)
}

func (manager *Manager) setServerError(name string, err error) {
	manager.mu.Lock()
	if connection := manager.servers[name]; connection != nil && !manager.closed {
		connection.state = ServerError
		connection.err = err.Error()
	}
	manager.mu.Unlock()
}

func (manager *Manager) setServerUnavailable(name string, err error) {
	manager.mu.Lock()
	if connection := manager.servers[name]; connection != nil && !manager.closed {
		connection.session = nil
		connection.state = ServerError
		connection.err = err.Error()
		connection.tools = make(map[string]string)
		connection.definitions = nil
	}
	manager.mu.Unlock()
}

// Reconnect closes and recreates one server session. An empty name reconnects
// all configured servers in deterministic order.
func (manager *Manager) Reconnect(ctx context.Context, name string) error {
	if name != "" {
		return manager.connectServer(ctx, name, true)
	}
	manager.mu.Lock()
	names := append([]string(nil), manager.order...)
	manager.mu.Unlock()
	var failures []error
	for _, server := range names {
		if err := manager.connectServer(ctx, server, true); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", server, err))
		}
	}
	return errors.Join(failures...)
}

// Close stops every MCP session. It is idempotent.
func (manager *Manager) Close() error {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	var sessions []*mcpsdk.ClientSession
	for _, connection := range manager.servers {
		if connection.session != nil {
			sessions = append(sessions, connection.session)
			connection.session = nil
		}
		connection.state = ServerStopped
	}
	manager.progress = make(map[string]*progressRegistration)
	manager.mu.Unlock()
	var failures []error
	for _, session := range sessions {
		if err := session.Close(); err != nil && !isChildExit(err) {
			failures = append(failures, err)
		}
	}
	manager.cancel()
	return errors.Join(failures...)
}

// isChildExit reports whether err only describes the exit status of a stdio
// child terminated during shutdown (for example "signal: terminated" after the
// SDK's kill grace). Reporting those as session_shutdown extension errors
// would turn every intentional stop of a stdio server into a diagnostic.
func isChildExit(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}

func (manager *Manager) Status() []ServerStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	status := make([]ServerStatus, 0, len(manager.order))
	for _, name := range manager.order {
		connection := manager.servers[name]
		tools := make([]string, 0, len(connection.tools))
		for _, registered := range connection.tools {
			tools = append(tools, registered)
		}
		sort.Strings(tools)
		transport := "stdio"
		if connection.config.URL != "" {
			transport = "http"
		}
		status = append(status, ServerStatus{Name: name, Transport: transport, State: connection.state, Tools: tools, Error: connection.err})
	}
	return status
}

func (manager *Manager) completeCommand(_ context.Context, prefix string) ([]extensions.AutocompleteItem, error) {
	manager.mu.Lock()
	names := append([]string(nil), manager.order...)
	manager.mu.Unlock()
	prefix = strings.TrimSpace(prefix)
	items := make([]extensions.AutocompleteItem, 0, len(names)+1)
	if prefix == "" || strings.HasPrefix("reconnect", prefix) {
		items = append(items, extensions.AutocompleteItem{Value: "reconnect", Label: "reconnect", Description: "Reconnect all MCP servers"})
	}
	for _, name := range names {
		value := "reconnect " + name
		if prefix == "" || strings.HasPrefix(value, prefix) {
			items = append(items, extensions.AutocompleteItem{Value: value, Label: value})
		}
	}
	return items, nil
}

func (manager *Manager) handleCommand(ctx context.Context, args string, commandContext extensions.CommandContext) error {
	fields := strings.Fields(args)
	if len(fields) > 0 && fields[0] == "reconnect" {
		name := ""
		if len(fields) > 1 {
			name = fields[1]
		}
		if len(fields) > 2 {
			commandContext.UI().Notify("Usage: /mcp reconnect [server]", extensions.NotifyError)
			return nil
		}
		if err := manager.Reconnect(ctx, name); err != nil {
			commandContext.UI().Notify("MCP reconnect failed: "+err.Error(), extensions.NotifyError)
			return nil
		}
		commandContext.UI().Notify("MCP servers reconnected", extensions.NotifyInfo)
		return nil
	}
	if len(fields) > 0 {
		commandContext.UI().Notify("Usage: /mcp [reconnect [server]]", extensions.NotifyError)
		return nil
	}
	commandContext.UI().Notify(formatStatus(manager.Status()), extensions.NotifyInfo)
	return nil
}

func formatStatus(status []ServerStatus) string {
	if len(status) == 0 {
		return "MCP is not configured"
	}
	lines := make([]string, 0, len(status))
	for _, server := range status {
		line := fmt.Sprintf("%s: %s (%s, %d tools)", server.Name, server.State, server.Transport, len(server.Tools))
		if server.Error != "" {
			line += ": " + server.Error
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func resolveCommandCWD(base, configured string) string {
	if configured == "" || filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(base, configured)
}
