package mcp

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/agent"
	configpkg "github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/tui"
	mcpjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ServerState string

const (
	ServerConnecting ServerState = "connecting"
	ServerConnected  ServerState = "connected"
	ServerFailed     ServerState = "failed"
	ServerNeedsAuth  ServerState = "needs-auth"
	ServerDisabled   ServerState = "disabled"
	ServerStopped    ServerState = "stopped"
)

type ServerStatus struct {
	Name      string
	Transport string
	Target    string // the command line or URL the server was configured with
	Exposure  Exposure
	Scope     string
	State     ServerState
	Tools     []string
	Error     string
}

const (
	defaultTimeout = 60 * time.Second
	// startupWait bounds how long the first prompt waits for servers with direct tools.
	startupWait = 10 * time.Second
	// MCPServersSection is the system prompt section listing servers whose tools are not declared.
	MCPServersSection    = "mcp_servers"
	maxServerDescription = 250
	maxServersSection    = 4096
	maxToolName          = 64
)

type connectFunc func(
	context.Context,
	context.Context,
	ServerConfig,
	*mcpsdk.ClientOptions,
	progressTracker,
) (*mcpsdk.ClientSession, error)

type progressTracker struct {
	manager *Manager
	server  string
}

type serverConnection struct {
	connectMu sync.Mutex // serializes connect attempts for this server only

	entry        Entry
	session      *mcpsdk.ClientSession
	state        ServerState
	err          string
	instructions string
	// tools maps the tools the server offers to their registered names.
	tools map[string]string
	// definitions are the last definitions registered per name, kept to hide
	// tools the server stops offering.
	definitions map[string]extensions.ToolDefinition
	// ready is closed when the running connection attempt settles.
	ready chan struct{}
	// challenge is the server's last WWW-Authenticate challenge, which sign-in answers.
	challenge *challenge
	// tokensAtSignIn are the stored tokens when the server came to need a
	// sign-in, to notice one done in another process.
	tokensAtSignIn string
}

type progressRegistration struct {
	token     string
	update    engine.AgentToolUpdateCallback
	touch     func()
	pending   int
	unhandled int
	drained   chan struct{}
	sealed    bool
}

// Manager owns all MCP client sessions for one coding-agent session.
type Manager struct {
	cwd      string
	agentDir string
	order    []string
	servers  map[string]*serverConnection
	load     func(cwd string, projectTrusted bool) ([]Entry, []string)
	connect  connectFunc
	// startupWait bounds the first prompt's wait for servers with direct tools.
	startupWait time.Duration
	// providerToken resolves auth.provider tokens; set from the session's model registry.
	providerToken func(ctx context.Context, provider string) (string, error)
	openURL       func(string)

	ctx    context.Context
	cancel context.CancelFunc

	mu                sync.Mutex
	api               extensions.API
	closed            bool
	problems          []string
	waitedForStartup  bool
	nextProgressToken uint64
	progress          map[string]*progressRegistration
	// toolOwners maps registered names to "<server>\x00<tool>", so names stay
	// unique and stable across reconnects.
	toolOwners map[string]string
}

// Extension is the MCP integration. When a session starts it reads mcp.json
// (the agent directory's, and the project's once trusted), adds the servers its
// client supplied, and connects them in the background.
func Extension(agentDir string, supplied ...Entry) extensions.Factory {
	return func(api extensions.API) error {
		manager := newManager(agentDir, func(cwd string, trusted bool) ([]Entry, []string) {
			entries, problems := Load(agentDir, cwd, trusted)
			return append(entries, supplied...), problems
		})
		manager.register(api)
		return nil
	}
}

func newManager(agentDir string, load func(string, bool) ([]Entry, []string)) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		agentDir: agentDir, servers: map[string]*serverConnection{}, load: load, connect: defaultConnect, startupWait: startupWait,
		ctx: ctx, cancel: cancel, progress: map[string]*progressRegistration{}, toolOwners: map[string]string{}, openURL: agent.OpenBrowser,
	}
}

// configure replaces the configured servers; it runs before any connects.
func (manager *Manager) configure(cwd string, entries []Entry) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.cwd = cwd
	manager.order = nil
	manager.servers = make(map[string]*serverConnection, len(entries))
	for _, entry := range entries {
		entry.Config = cloneServerConfig(entry.Config)
		entry.Config.Command = expandHome(entry.Config.Command)
		for index, arg := range entry.Config.Args {
			entry.Config.Args[index] = expandHome(arg)
		}
		if entry.Config.Command != "" {
			entry.Config.CWD = resolveCommandCWD(cwd, expandHome(entry.Config.CWD))
		}
		state := ServerStopped
		if !entry.Config.IsEnabled() {
			state = ServerDisabled
		}
		manager.order = append(manager.order, entry.Name)
		manager.servers[entry.Name] = &serverConnection{entry: entry, state: state, tools: map[string]string{}, definitions: map[string]extensions.ToolDefinition{}}
	}
	sort.Strings(manager.order)
}

func cloneServerConfig(config ServerConfig) ServerConfig {
	config.Args = slices.Clone(config.Args)
	config.Env = maps.Clone(config.Env)
	config.Headers = maps.Clone(config.Headers)
	config.ToolExposure = slices.Clone(config.ToolExposure)
	return config
}

func (manager *Manager) register(api extensions.API) {
	manager.api = api
	api.RegisterCommand("mcp", extensions.Command{
		Description:            "Show MCP servers, or reconnect one",
		GetArgumentCompletions: manager.completeCommand,
		Handler:                manager.handleCommand,
	})
	api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
		entries, problems := manager.load(ctx.CWD(), ctx.IsProjectTrusted())
		manager.configure(ctx.CWD(), entries)
		manager.mu.Lock()
		manager.problems = problems
		if registry := ctx.ModelRegistry(); registry != nil {
			manager.providerToken = func(callCtx context.Context, provider string) (string, error) {
				key, err := registry.ResolveAPIKey(callCtx, provider, nil)
				if err != nil || key == nil {
					return "", err
				}
				return *key, nil
			}
		}
		manager.mu.Unlock()
		manager.ensureDiscoveryActive(ctx)
		// Connecting runs in the background: the first prompt waits only for
		// servers with direct tools, tool_search for the others.
		manager.startAll()
		go func() {
			manager.waitForServers(manager.ctx, manager.enabledNames(nil))
			manager.reportProblems(ctx)
		}()
		return nil, nil
	})
	api.On(extensions.EventBeforeAgentStart, func(callCtx context.Context, event extensions.Event, ctx extensions.Context) (any, error) {
		manager.waitForDirectServers(callCtx, ctx)
		start, ok := event.(extensions.BeforeAgentStartEvent)
		if !ok || start.SystemPromptOptions.Sections == nil {
			return nil, nil
		}
		if section := manager.serversSection(); section != "" {
			start.SystemPromptOptions.Sections[MCPServersSection] = section
		} else {
			delete(start.SystemPromptOptions.Sections, MCPServersSection)
		}
		return nil, nil
	})
	// tool_search reaches every server, so it waits for the ones still connecting.
	api.On(extensions.EventToolCall, func(callCtx context.Context, event extensions.Event, _ extensions.Context) (any, error) {
		if call, ok := event.(extensions.ToolCallEvent); ok && call.ToolName == ToolSearchName {
			manager.waitForServers(callCtx, manager.enabledNames(nil))
		}
		return nil, nil
	})
	// Sign-ins done outside the session, such as `orb mcp login` run by the
	// agent, are picked up on the next turn.
	api.On(extensions.EventTurnStart, func(callCtx context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
		for _, name := range manager.signedInElsewhere() {
			_ = manager.connectServer(callCtx, name, true)
		}
		return nil, nil
	})
	api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) {
		return nil, manager.Close()
	})
}

// startAll starts connecting every enabled server that is not connected.
func (manager *Manager) startAll() {
	for _, name := range manager.enabledNames(nil) {
		manager.mu.Lock()
		connection := manager.servers[name]
		start := connection.session == nil && connection.ready == nil
		if start {
			connection.ready = make(chan struct{})
			connection.state = ServerConnecting
		}
		manager.mu.Unlock()
		if start {
			go func() { _ = manager.connectServer(manager.ctx, name, false) }()
		}
	}
}

// enabledNames are the enabled servers, all or those matching keep.
func (manager *Manager) enabledNames(keep func(Entry) bool) []string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var names []string
	for _, name := range manager.order {
		entry := manager.servers[name].entry
		if entry.Config.IsEnabled() && (keep == nil || keep(entry)) {
			names = append(names, name)
		}
	}
	return names
}

// waitForServers waits until the named servers' connection attempts settle.
func (manager *Manager) waitForServers(ctx context.Context, names []string) {
	for _, name := range names {
		manager.mu.Lock()
		ready := manager.servers[name].ready
		manager.mu.Unlock()
		if ready == nil {
			continue
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return
		case <-manager.ctx.Done():
			return
		}
	}
}

// waitForDirectServers holds the first prompt until servers with direct tools
// connect, so their tools are in its first request, but not for longer than
// startupWait.
func (manager *Manager) waitForDirectServers(callCtx context.Context, ctx extensions.Context) {
	manager.mu.Lock()
	waited := manager.waitedForStartup
	manager.waitedForStartup = true
	manager.mu.Unlock()
	names := manager.enabledNames(func(entry Entry) bool { return slices.Contains(entry.Config.exposuresOf(), ExposureDirect) })
	if waited || len(names) == 0 {
		return
	}
	waitCtx, cancel := context.WithTimeout(callCtx, manager.startupWait)
	defer cancel()
	manager.waitForServers(waitCtx, names)
	if waitCtx.Err() != nil && callCtx.Err() == nil {
		ctx.UI().Notify("MCP servers are still connecting; their tools become available once connected.", extensions.NotifyInfo)
	}
}

// reportProblems tells the user once about config errors and failed servers.
func (manager *Manager) reportProblems(ctx extensions.Context) {
	manager.mu.Lock()
	lines := make([]string, 0, len(manager.problems))
	for _, problem := range manager.problems {
		lines = append(lines, "config: "+problem)
	}
	closed := manager.closed
	manager.mu.Unlock()
	for _, server := range manager.Status() {
		switch server.State {
		case ServerFailed:
			lines = append(lines, server.Name+": failed: "+firstLine(server.Error))
		case ServerNeedsAuth:
			lines = append(lines, server.Name+": needs sign-in, run /mcp login "+server.Name)
		}
	}
	if closed || len(lines) == 0 {
		return
	}
	ctx.UI().Notify("MCP servers need attention:\n  "+strings.Join(lines, "\n  ")+"\nRun /mcp to see them.", extensions.NotifyWarning)
}

// ensureDiscoveryActive activates tool_search when servers have tools only it
// reaches; it is decided from the config, before the servers connect.
func (manager *Manager) ensureDiscoveryActive(ctx extensions.Context) {
	indirect := manager.enabledNames(func(entry Entry) bool {
		exposures := entry.Config.exposuresOf()
		return slices.Contains(exposures, ExposureCodemode) || slices.Contains(exposures, ExposureDeferred)
	})
	if len(indirect) == 0 {
		return
	}
	tools, err := manager.api.GetAllTools()
	if err != nil || !slices.ContainsFunc(tools, func(tool extensions.ToolInfo) bool { return tool.Name == ToolSearchName }) {
		ctx.UI().Notify("MCP tools are only reachable through tool_search, which is not loaded; they cannot be called.", extensions.NotifyWarning)
		return
	}
	active, err := manager.api.GetActiveTools()
	if err == nil && !slices.Contains(active, ToolSearchName) {
		_ = manager.api.SetActiveTools(append(active, ToolSearchName))
	}
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
	if connection.ready == nil {
		connection.ready = make(chan struct{})
	}
	ready := connection.ready
	defer func() {
		manager.mu.Lock()
		connection.ready = nil
		manager.mu.Unlock()
		close(ready)
	}()
	if !replace && connection.session != nil && connection.state == ServerConnected {
		manager.mu.Unlock()
		return nil
	}
	previous := connection.session
	connection.session = nil
	connection.state = ServerConnecting
	connection.err = ""
	entry := connection.entry
	manager.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	connectCtx, cancelConnect := context.WithTimeout(ctx, entry.Config.timeout())
	stopClose := context.AfterFunc(manager.ctx, cancelConnect)
	defer func() {
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
	session, err := manager.connect(connectCtx, manager.ctx, entry.Config, options, progressTracker{manager: manager, server: name})
	if err != nil {
		manager.setServerFailed(name, nil, err)
		return err
	}
	tools, err := listTools(connectCtx, session)
	if err != nil {
		_ = session.Close()
		manager.setServerFailed(name, nil, err)
		return err
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		_ = session.Close()
		return errors.New("manager is closed")
	}
	connection.session = session
	connection.state = ServerConnected
	connection.err = ""
	if result := session.InitializeResult(); result != nil {
		connection.instructions = strings.TrimSpace(result.Instructions)
	}
	manager.mu.Unlock()
	manager.registerTools(name, tools)
	return nil
}

func (config ServerConfig) timeout() time.Duration {
	if config.Timeout > 0 {
		return time.Duration(config.Timeout * float64(time.Second))
	}
	return defaultTimeout
}

func defaultConnect(
	connectCtx, lifecycleCtx context.Context,
	config ServerConfig,
	options *mcpsdk.ClientOptions,
	tracker progressTracker,
) (*mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "orb", Version: "0.1.0"}, options)
	if config.IsHTTP() {
		headers, err := configpkg.ResolveHeadersOrThrow(config.Headers, "MCP server header", nil)
		if err != nil {
			return nil, err
		}
		authenticated, err := tracker.manager.authTransport(tracker.server, config, http.DefaultTransport)
		if err != nil {
			return nil, err
		}
		base := headerRoundTripper{base: authenticated, headers: headers}
		httpClient := &http.Client{Transport: progressRoundTripper{base: base, manager: tracker.manager}}
		return client.Connect(connectCtx, &mcpsdk.StreamableClientTransport{Endpoint: config.URL, HTTPClient: httpClient}, nil)
	}
	env := make(map[string]string, len(config.Env))
	for key, value := range config.Env {
		resolved, err := configpkg.ResolveConfigValueOrThrow(value, fmt.Sprintf("MCP server env %q", key), nil)
		if err != nil {
			return nil, err
		}
		env[key] = resolved
	}
	command := exec.CommandContext(lifecycleCtx, config.Command, config.Args...)
	if config.CWD != "" {
		command.Dir = config.CWD
	}
	command.Env = mergedEnvironment(env)
	// A process server opens with initialize: SDKs before the 2026-07-28
	// protocol (rmcp, for one) exit on the server/discover probe the SDK's
	// default sends first, and a dead process leaves nothing to fall back on.
	return client.Connect(connectCtx, tracker.wrapTransport(&mcpsdk.CommandTransport{Command: command}), &mcpsdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
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
	maps.Copy(values, overrides)
	result := make([]string, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
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

var nonIdentifier = regexp.MustCompile(`[^A-Za-z0-9_]`)

// toolName is mcp__<server>__<tool> with everything but [A-Za-z0-9_] as _,
// shortened with a hash suffix past 64 characters or when taken reports the
// name used by another tool.
func toolName(server, tool string, taken func(string) bool) string {
	name := nonIdentifier.ReplaceAllString("mcp__"+server+"__"+tool, "_")
	if len(name) <= maxToolName && !taken(name) {
		return name
	}
	digest := sha256.Sum256([]byte(server + "\x00" + tool))
	hash := hex.EncodeToString(digest[:4])
	return name[:min(len(name), maxToolName-len(hash)-1)] + "_" + hash
}

// registerTools registers the tools a server offers and hides those it no
// longer offers, since tools cannot be unregistered.
func (manager *Manager) registerTools(server string, tools []*mcpsdk.Tool) {
	manager.mu.Lock()
	connection := manager.servers[server]
	entry := connection.entry
	namespace := &extensions.ToolNamespace{Name: Namespace(server), Description: strings.TrimSpace(entry.Config.Description), Instructions: connection.instructions}
	// Every tool whose name sanitizes to a shared name gets the hash suffix, so
	// which keeps the plain name does not depend on the list's order.
	plain := map[string]int{}
	for _, tool := range tools {
		if tool != nil && tool.Name != "" {
			plain[toolName(server, tool.Name, func(string) bool { return false })]++
		}
	}
	current := map[string]string{}
	var definitions []extensions.ToolDefinition
	for _, tool := range tools {
		if tool == nil || tool.Name == "" || current[tool.Name] != "" {
			continue
		}
		owner := server + "\x00" + tool.Name
		name := toolName(server, tool.Name, func(candidate string) bool {
			existing, owned := manager.toolOwners[candidate]
			return owned && existing != owner || plain[candidate] > 1 || slices.Contains(slices.Collect(maps.Values(current)), candidate)
		})
		manager.toolOwners[name] = owner
		current[tool.Name] = name
		definition := manager.toolDefinition(server, name, entry.Config.ToolExposureOf(tool.Name), namespace, tool)
		connection.definitions[name] = definition
		definitions = append(definitions, definition)
	}
	for _, name := range connection.tools {
		if !slices.Contains(slices.Collect(maps.Values(current)), name) {
			hidden := connection.definitions[name]
			hidden.Exposure = extensions.ToolHidden
			definitions = append(definitions, hidden)
		}
	}
	connection.tools = current
	api := manager.api
	manager.mu.Unlock()
	for _, definition := range definitions {
		if api != nil {
			api.RegisterTool(definition)
		}
	}
}

func (manager *Manager) toolDefinition(server, name string, exposure Exposure, namespace *extensions.ToolNamespace, tool *mcpsdk.Tool) extensions.ToolDefinition {
	parameters := map[string]any{}
	if data, err := json.Marshal(tool.InputSchema); err == nil {
		_ = json.Unmarshal(data, &parameters)
	}
	// Tool schemas must be objects; servers may omit type, and some providers
	// reject object schemas without properties.
	if parameters == nil {
		parameters = map[string]any{}
	}
	if _, ok := parameters["type"]; !ok {
		parameters["type"] = "object"
	}
	if _, ok := parameters["properties"]; !ok {
		parameters["properties"] = map[string]any{}
	}
	parametersJSON, _ := json.Marshal(parameters)
	output := map[string]any{"content": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}
	if tool.OutputSchema != nil {
		output["structuredContent"] = tool.OutputSchema
	}
	output["isError"] = map[string]any{"type": "boolean"}
	output["_meta"] = map[string]any{"type": "object"}
	outputJSON, _ := json.Marshal(map[string]any{"type": "object", "properties": output, "required": []string{"content"}})
	title := tool.Title
	if title == "" && tool.Annotations != nil {
		title = tool.Annotations.Title
	}
	description := cmp.Or(strings.TrimSpace(tool.Description), title, fmt.Sprintf("MCP tool %s from server %s", tool.Name, server))
	original := tool.Name
	return extensions.ToolDefinition{
		Name: name, Label: server + "/" + tool.Name, Description: description,
		Parameters: jsonschema.Schema(parametersJSON), OutputSchema: jsonschema.Schema(outputJSON),
		Exposure: toolExposure(exposure), Namespace: namespace, Annotations: toolAnnotations(tool.Annotations),
		ExecutionMode: engine.ToolExecutionParallel,
		Execute: func(ctx context.Context, toolCallID string, args any, update engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
			return manager.execute(ctx, server, original, toolCallID, args, update)
		},
	}
}

// toolExposure maps a server exposure to a tool exposure; Orb has no
// codemode, so codemode tools are deferred.
func toolExposure(exposure Exposure) extensions.ToolExposure {
	switch exposure {
	case ExposureDirect:
		return extensions.ToolDirect
	case ExposureHidden:
		return extensions.ToolHidden
	}
	return extensions.ToolDeferred
}

func toolAnnotations(annotations *mcpsdk.ToolAnnotations) *extensions.ToolAnnotations {
	if annotations == nil {
		return nil
	}
	hint := func(value bool) *bool {
		if !value {
			return nil
		}
		return &value
	}
	result := &extensions.ToolAnnotations{ReadOnlyHint: hint(annotations.ReadOnlyHint), DestructiveHint: annotations.DestructiveHint, IdempotentHint: hint(annotations.IdempotentHint), OpenWorldHint: annotations.OpenWorldHint}
	if *result == (extensions.ToolAnnotations{}) {
		return nil
	}
	return result
}

func (manager *Manager) execute(
	ctx context.Context,
	server, tool, toolCallID string,
	args any,
	update engine.AgentToolUpdateCallback,
) (engine.AgentToolResult, error) {
	manager.waitForServers(ctx, []string{server})
	manager.mu.Lock()
	connection := manager.servers[server]
	if connection == nil {
		manager.mu.Unlock()
		return engine.AgentToolResult{}, fmt.Errorf("mcp: unknown server %q", server)
	}
	session := connection.session
	timeout := connection.entry.Config.timeout()
	manager.mu.Unlock()
	if session == nil {
		if err := manager.connectServer(ctx, server, true); err != nil {
			if errors.Is(err, errSignInRequired) {
				return engine.AgentToolResult{}, fmt.Errorf("MCP server %q needs sign-in: the user can run /mcp login %s", server, server)
			}
			return engine.AgentToolResult{}, err
		}
		manager.mu.Lock()
		session = connection.session
		manager.mu.Unlock()
	}
	manager.mu.Lock()
	_, available := connection.tools[tool]
	manager.mu.Unlock()
	if !available {
		return engine.AgentToolResult{}, fmt.Errorf("mcp: tool %q is no longer available from server %q", tool, server)
	}
	// The request times out after the server's timeout without progress.
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()
	manager.mu.Lock()
	manager.nextProgressToken++
	token := fmt.Sprintf("%s:%s:%d", server, toolCallID, manager.nextProgressToken)
	registration := &progressRegistration{token: token, update: update, touch: func() { timer.Reset(timeout) }}
	manager.progress[token] = registration
	manager.mu.Unlock()
	defer manager.dropProgress(registration)
	params := &mcpsdk.CallToolParams{Name: tool, Arguments: args}
	params.SetProgressToken(token)
	result, err := session.CallTool(callCtx, params)
	if err == nil {
		err = manager.waitForProgress(callCtx, registration)
	}
	if err != nil {
		if callCtx.Err() != nil && ctx.Err() == nil {
			err = fmt.Errorf("mcp: %s/%s timed out after %s without progress", server, tool, timeout)
		}
		if errors.Is(err, errSignInRequired) {
			manager.setServerFailed(server, session, err)
			return engine.AgentToolResult{}, fmt.Errorf("MCP server %q needs sign-in: the user can run /mcp login %s, or you can run `orb mcp login %s` for them to approve in the browser", server, server, server)
		}
		if isConnectionDead(err) {
			// The next call reconnects.
			manager.setServerFailed(server, session, err)
		}
		return engine.AgentToolResult{}, err
	}
	return mapToolResult(server, tool, result), nil
}

// isConnectionDead reports whether a tool call failed because the transport
// itself is gone (closed connection, EOF or broken pipe from a dead child).
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
	if registration.touch != nil {
		registration.touch()
	}
	if registration.update == nil {
		return
	}
	message := params.Message
	if message == "" {
		if params.Total > 0 {
			message = fmt.Sprintf("MCP progress: %g/%g", params.Progress, params.Total)
		} else {
			message = fmt.Sprintf("MCP progress: %g", params.Progress)
		}
	}
	registration.update(engine.AgentToolResult{
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
	connection := manager.servers[name]
	if manager.closed || connection == nil || connection.session == nil {
		manager.mu.Unlock()
		return
	}
	session := connection.session
	timeout := connection.entry.Config.timeout()
	manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(manager.ctx, timeout)
	defer cancel()
	tools, err := listTools(ctx, session)
	if err != nil {
		manager.mu.Lock()
		connection.err = err.Error()
		manager.mu.Unlock()
		return
	}
	manager.mu.Lock()
	current := !manager.closed && connection.session == session
	manager.mu.Unlock()
	if current {
		manager.registerTools(name, tools)
	}
}

// setServerFailed records a failed connection; with session set, only while
// that session is still the server's.
func (manager *Manager) setServerFailed(name string, session *mcpsdk.ClientSession, err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if connection := manager.servers[name]; connection != nil && !manager.closed && (session == nil || connection.session == session) {
		connection.session = nil
		connection.state = ServerFailed
		connection.err = err.Error()
		if errors.Is(err, errSignInRequired) {
			connection.state, connection.err = ServerNeedsAuth, ""
			connection.tokensAtSignIn = manager.storedTokens(name, connection.entry.Config)
		}
	}
}

// Probe connects each enabled server once, outside a session, and reports
// the servers with the tools they offer.
func Probe(ctx context.Context, cwd, agentDir string, entries []Entry) []ServerStatus {
	manager := newManager(agentDir, nil)
	manager.configure(cwd, entries)
	var group sync.WaitGroup
	for _, name := range manager.enabledNames(nil) {
		group.Go(func() { _ = manager.connectServer(ctx, name, false) })
	}
	group.Wait()
	status := manager.Status()
	_ = manager.Close()
	return status
}

// Reconnect closes and recreates one server session. An empty name reconnects
// all enabled servers in name order.
func (manager *Manager) Reconnect(ctx context.Context, name string) error {
	if name != "" {
		return manager.connectServer(ctx, name, true)
	}
	var failures []error
	for _, server := range manager.enabledNames(nil) {
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
	// After the sessions: cancelling first would kill stdio children mid-close.
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
		config := connection.entry.Config
		transport, target := "stdio", strings.TrimSpace(config.Command+" "+strings.Join(config.Args, " "))
		if config.IsHTTP() {
			transport, target = "http", config.URL
		}
		status = append(status, ServerStatus{
			Name: name, Transport: transport, Target: target, Exposure: config.ExposureOf(), Scope: connection.entry.Scope,
			State: connection.state, Tools: slices.Sorted(maps.Keys(connection.tools)), Error: connection.err,
		})
	}
	return status
}

// serversSection lists the enabled servers whose tools are not declared, so
// the model knows to load them with tool_search; empty when there are none.
func (manager *Manager) serversSection() string {
	manager.mu.Lock()
	type listing struct{ head, summary string }
	var listed []listing
	for _, name := range manager.order {
		connection := manager.servers[name]
		config := connection.entry.Config
		exposures := config.exposuresOf()
		if !config.IsEnabled() || !slices.Contains(exposures, ExposureCodemode) && !slices.Contains(exposures, ExposureDeferred) {
			continue
		}
		summary := firstLine(cmp.Or(strings.TrimSpace(config.Description), connection.instructions))
		listed = append(listed, listing{head: "- " + Namespace(name) + " (tool_search)", summary: strings.TrimSpace(summary)})
	}
	manager.mu.Unlock()
	if len(listed) == 0 {
		return ""
	}
	intro := "MCP servers whose tools are not declared to you. Load the tools of `tool_search` servers with `tool_search`."
	omitted := func(count int) []string {
		if count == 0 {
			return nil
		}
		return []string{fmt.Sprintf("- … %d more server%s; find their tools with tool_search", count, plural(count))}
	}
	size := func(kept int) int {
		lines := []string{intro}
		for _, item := range listed[:kept] {
			lines = append(lines, item.head)
		}
		return len([]rune(strings.Join(append(lines, omitted(len(listed)-kept)...), "\n")))
	}
	kept := len(listed)
	for kept > 0 && size(kept) > maxServersSection {
		kept--
	}
	perServer := 0
	if kept > 0 {
		// Each description also takes a ": " separator.
		perServer = min(maxServerDescription, (maxServersSection-size(kept))/kept-2)
	}
	lines := []string{intro}
	for _, item := range listed[:kept] {
		if summary := truncate(item.summary, perServer); summary != "" {
			lines = append(lines, item.head+": "+summary)
		} else {
			lines = append(lines, item.head)
		}
	}
	return strings.Join(append(lines, omitted(len(listed)-kept)...), "\n")
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return ""
	}
	return strings.TrimRight(string(runes[:limit-1]), " \t\n") + "…"
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func (manager *Manager) completeCommand(_ context.Context, prefix string) ([]extensions.AutocompleteItem, error) {
	fields := strings.Fields(prefix)
	if len(fields) <= 1 && !strings.HasSuffix(prefix, " ") {
		var items []extensions.AutocompleteItem
		for _, action := range []string{"login", "logout", "reconnect"} {
			if len(fields) == 0 || strings.HasPrefix(action, fields[0]) {
				items = append(items, extensions.AutocompleteItem{Value: action + " ", Label: action})
			}
		}
		return items, nil
	}
	action, partial := fields[0], ""
	if len(fields) > 1 {
		partial = fields[1]
	}
	var items []extensions.AutocompleteItem
	for _, server := range manager.Status() {
		eligible := server.State != ServerDisabled
		if action != "reconnect" {
			eligible = manager.usesOAuth(server.Name)
		}
		if eligible && strings.HasPrefix(server.Name, partial) {
			items = append(items, extensions.AutocompleteItem{Value: action + " " + server.Name, Label: server.Name, Description: string(server.State)})
		}
	}
	return items, nil
}

func (manager *Manager) usesOAuth(name string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	connection := manager.servers[name]
	return connection != nil && connection.entry.Config.IsEnabled() && connection.entry.Config.UsesOAuth()
}

// pickServer resolves a subcommand's server; without a name, the only
// eligible one, the only preferred one, or the user's choice.
func (manager *Manager) pickServer(ctx context.Context, name string, commandContext extensions.CommandContext, eligible func(ServerStatus) bool, preferred ServerState, none string) string {
	var candidates, favored []string
	for _, server := range manager.Status() {
		if !eligible(server) {
			continue
		}
		if name == "" || server.Name == name {
			candidates = append(candidates, server.Name)
		}
		if server.State == preferred {
			favored = append(favored, server.Name)
		}
	}
	switch {
	case name != "" && len(candidates) == 0:
		commandContext.UI().Notify(none, extensions.NotifyError)
		return ""
	case len(candidates) == 0:
		commandContext.UI().Notify(none, extensions.NotifyInfo)
		return ""
	case len(candidates) == 1:
		return candidates[0]
	case len(favored) == 1:
		return favored[0]
	}
	choice, ok, err := commandContext.UI().Select(ctx, "MCP server", candidates, nil)
	if err != nil || !ok {
		return ""
	}
	return choice
}

const mcpUsage = "Usage: /mcp, /mcp login [server], /mcp logout [server], /mcp reconnect [server]"

func (manager *Manager) handleCommand(ctx context.Context, args string, commandContext extensions.CommandContext) error {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		if commandContext.Mode() == extensions.ModeTUI && commandContext.HasUI() {
			return manager.statusWindow(ctx, commandContext)
		}
		commandContext.UI().Notify(manager.formatStatus(), extensions.NotifyInfo)
		return nil
	}
	if len(fields) > 2 {
		commandContext.UI().Notify(mcpUsage, extensions.NotifyWarning)
		return nil
	}
	name := ""
	if len(fields) == 2 {
		name = fields[1]
	}
	oauth := func(server ServerStatus) bool { return manager.usesOAuth(server.Name) }
	const noOAuth = "No enabled MCP server uses OAuth. Only HTTP servers without an Authorization header do."
	ui := commandContext.UI()
	switch fields[0] {
	case "login":
		if name = manager.pickServer(ctx, name, commandContext, oauth, ServerNeedsAuth, noOAuth); name == "" {
			return nil
		}
		if !commandContext.HasUI() {
			ui.Notify(fmt.Sprintf("Signing in to MCP server %q requires interactive mode; run orb mcp login %s.", name, name), extensions.NotifyError)
			return nil
		}
		err := manager.SignIn(ctx, name, signInPrompt{
			show: func(target string) {
				link := target
				if commandContext.Mode() == extensions.ModeTUI {
					// Long URLs wrap, which some terminals cannot open; the short link stays on one line.
					label := "Ctrl+click to open"
					if runtime.GOOS == "darwin" {
						label = "Cmd+click to open"
					}
					link = tui.Hyperlink(target, target) + "\n" + tui.Hyperlink(label, target)
				}
				ui.Notify(fmt.Sprintf("Sign in to MCP server %q in your browser:\n%s", name, link), extensions.NotifyInfo)
				manager.openURL(target)
			},
			paste: func(pasteCtx context.Context) (string, error) {
				placeholder := "http://127.0.0.1:.../callback?code=..."
				value, ok, err := ui.Input(pasteCtx, fmt.Sprintf("Waiting for sign-in to %q. If the browser cannot reach this machine, paste the URL it was redirected to.", name), &placeholder, &extensions.DialogOptions{Signal: pasteCtx})
				if !ok {
					return "", err
				}
				return value, err
			},
		})
		switch {
		case errors.Is(err, errSignInCancelled):
			ui.Notify("Sign-in cancelled.", extensions.NotifyInfo)
		case err != nil:
			ui.Notify("Sign-in failed: "+err.Error(), extensions.NotifyError)
		default:
			manager.ensureDiscoveryActive(commandContext)
			ui.Notify(fmt.Sprintf("Signed in to MCP server %q (%d tools).", name, len(manager.statusOf(name).Tools)), extensions.NotifyInfo)
		}
	case "logout":
		if name = manager.pickServer(ctx, name, commandContext, oauth, ServerConnected, noOAuth); name == "" {
			return nil
		}
		removed, err := manager.SignOut(name)
		switch {
		case err != nil:
			ui.Notify(err.Error(), extensions.NotifyError)
		case removed:
			ui.Notify(fmt.Sprintf("Signed out of MCP server %q.", name), extensions.NotifyInfo)
		default:
			ui.Notify(fmt.Sprintf("No stored credentials for MCP server %q.", name), extensions.NotifyInfo)
		}
	case "reconnect":
		if name = manager.pickServer(ctx, name, commandContext, func(server ServerStatus) bool { return server.State != ServerDisabled }, ServerFailed, "No enabled MCP server to reconnect."); name == "" {
			return nil
		}
		if err := manager.Reconnect(ctx, name); err != nil {
			ui.Notify(fmt.Sprintf("MCP server %q: %v", name, err), extensions.NotifyError)
			return nil
		}
		manager.ensureDiscoveryActive(commandContext)
		ui.Notify(fmt.Sprintf("Reconnected to MCP server %q (%d tools).", name, len(manager.statusOf(name).Tools)), extensions.NotifyInfo)
	default:
		ui.Notify(mcpUsage, extensions.NotifyWarning)
	}
	return nil
}

func (manager *Manager) statusOf(name string) ServerStatus {
	for _, server := range manager.Status() {
		if server.Name == name {
			return server
		}
	}
	return ServerStatus{}
}

func (manager *Manager) formatStatus() string {
	status := manager.Status()
	manager.mu.Lock()
	problems := slices.Clone(manager.problems)
	manager.mu.Unlock()
	if len(status) == 0 && len(problems) == 0 {
		return "No MCP servers configured. Add them with orb mcp add, or to mcp.json."
	}
	lines := make([]string, 0, len(status)+len(problems))
	for _, server := range status {
		line := fmt.Sprintf("%s: %s", server.Name, server.State)
		if server.State == ServerNeedsAuth {
			line = fmt.Sprintf("%s: needs sign-in, run /mcp login %s", server.Name, server.Name)
		}
		if server.State == ServerConnected {
			line += fmt.Sprintf(", %d tools", len(server.Tools))
		}
		line += " (" + string(server.Exposure) + ")"
		if server.Error != "" && server.State != ServerConnected {
			line += "\n    " + strings.ReplaceAll(server.Error, "\n", "\n    ")
		}
		lines = append(lines, line)
	}
	for _, problem := range problems {
		lines = append(lines, "config error: "+problem)
	}
	return strings.Join(lines, "\n")
}

func resolveCommandCWD(base, configured string) string {
	if configured == "" {
		return base
	}
	if filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(base, configured)
}

func expandHome(value string) string {
	if value == "~" || strings.HasPrefix(value, "~/") {
		if expanded, err := configpkg.NormalizePath(value); err == nil {
			return expanded
		}
	}
	return value
}
