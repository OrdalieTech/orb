package modes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/jstrim"
)

type RPCSessionHost interface {
	Session() *agent.SessionRuntime
	NewSession(parentSession string) (cancelled bool, err error)
	SwitchSession(sessionPath string) (cancelled bool, err error)
	Fork(entryID string, atEntry bool) (text string, cancelled bool, err error)
	Dispose()
}

type rpcSessionRebindHost interface {
	SetRebindSession(func(*agent.SessionRuntime) error)
}

type RPCModeOptions struct {
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Commands func() []RPCSlashCommand
}

type rpcMode struct {
	ctx      context.Context
	cancel   context.CancelFunc
	host     RPCSessionHost
	options  RPCModeOptions
	output   *serializedOutput
	ui       *RPCExtensionUI
	mu       sync.Mutex
	unsub    func()
	disposed bool
	// shutdownRequested is set by extension ctx.shutdown() and honored after
	// the current command or agent_settled (upstream rpc-mode.ts:85,344-346).
	shutdownRequested bool
	promptMu          sync.Mutex
	prompting         bool
	promptSession     *agent.SessionRuntime
	promptPreflight   chan struct{}
}

// requestExtensionShutdown is the RPC shutdownHandler for extension
// ctx.shutdown(): it only records the request (rpc-mode.ts:344-346); the
// shutdown itself runs after the in-flight command settles.
func (mode *rpcMode) requestExtensionShutdown() {
	mode.mu.Lock()
	mode.shutdownRequested = true
	mode.mu.Unlock()
}

// checkShutdownRequested mirrors rpc-mode.ts:727-730: once a shutdown was
// requested, cancel the serve loop, which disposes, flushes stdout, and
// returns exit code 0 like upstream's shutdown().
func (mode *rpcMode) checkShutdownRequested() {
	mode.mu.Lock()
	requested := mode.shutdownRequested
	cancel := mode.cancel
	mode.mu.Unlock()
	if requested && cancel != nil {
		cancel()
	}
}

// RunRPCMode serves upstream's strict-LF, bidirectional JSONL protocol until
// stdin closes, the context is cancelled, or a shutdown signal arrives.
func RunRPCMode(ctx context.Context, host RPCSessionHost, options RPCModeOptions) int {
	if host == nil || host.Session() == nil {
		if options.Stderr != nil {
			_, _ = fmt.Fprintln(options.Stderr, "rpc mode: nil session host")
		}
		return 1
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	rpcContext, cancel := context.WithCancel(ctx)
	mode := &rpcMode{ctx: rpcContext, cancel: cancel, host: host, options: options, output: newSerializedOutput(options.Stdout)}
	mode.ui = newRPCExtensionUI(mode.writeObject)
	if rebindHost, ok := host.(rpcSessionRebindHost); ok {
		rebindHost.SetRebindSession(mode.bindReplacement)
	}
	if err := mode.bindSession(); err != nil {
		mode.dispose()
		_ = mode.output.closeAndWait()
		_, _ = fmt.Fprintln(options.Stderr, err)
		return 1
	}

	lines := make(chan []byte)
	readErrors := make(chan error, 1)
	go readStrictJSONLines(options.Stdin, lines, readErrors)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, printModeSignals()...)
	defer signal.Stop(signals)

	var commands sync.WaitGroup
	for {
		select {
		case line, open := <-lines:
			if !open {
				readErr := <-readErrors
				mode.dispose()
				commands.Wait()
				if readErr != nil {
					_ = mode.output.closeAndWait()
					_, _ = fmt.Fprintln(options.Stderr, readErr)
					return 1
				}
				if err := mode.output.closeAndWait(); err != nil {
					_, _ = fmt.Fprintln(options.Stderr, err)
					return 1
				}
				return 0
			}
			mode.handleLine(line, &commands)
		case received := <-signals:
			tools.KillTrackedDetachedChildren()
			mode.dispose()
			commands.Wait()
			_ = mode.output.closeAndWait()
			return printModeSignalExitCode(received)
		case <-ctx.Done():
			mode.dispose()
			commands.Wait()
			if err := mode.output.closeAndWait(); err != nil {
				_, _ = fmt.Fprintln(options.Stderr, err)
				return 1
			}
			return 0
		}
	}
}

func (mode *rpcMode) bindSession() error {
	session := mode.host.Session()
	if session == nil {
		return errors.New("rpc mode: session replacement returned nil")
	}
	return mode.bindReplacement(session)
}

func (mode *rpcMode) bindReplacement(session *agent.SessionRuntime) error {
	if session == nil {
		return errors.New("rpc mode: session replacement returned nil")
	}
	// Upstream rebindSession passes the RPC uiContext into bindExtensions on
	// every rebind (rpc-mode.ts:311-320) so extensions get a live UI seam.
	session.BindExtensionUI(newRPCExtensionUIAdapter(mode.ui), extensions.ModeRPC)
	// Upstream binds the RPC shutdownHandler in the same rebind
	// (rpc-mode.ts:344-346). The runtime setter is asserted optionally so the
	// modes-side wiring stands alone until SessionRuntime exposes the seam.
	if binder, ok := any(session).(interface{ SetExtensionShutdownHandler(func()) }); ok {
		binder.SetExtensionShutdownHandler(mode.requestExtensionShutdown)
	}
	if err := session.BindExtensions(mode.ctx); err != nil {
		return err
	}
	mode.mu.Lock()
	defer mode.mu.Unlock()
	if mode.disposed {
		return errors.New("rpc mode is disposed")
	}
	if mode.unsub != nil {
		mode.unsub()
	}
	mode.unsub = session.Subscribe(func(event any) {
		mode.output.writeSessionEvent(event)
		// Upstream re-checks extension shutdown requests on agent_settled
		// (rpc-mode.ts:353-358).
		if _, settled := event.(agent.AgentSettledEvent); settled {
			mode.checkShutdownRequested()
		}
	})
	return nil
}

func (mode *rpcMode) dispose() {
	mode.mu.Lock()
	if mode.disposed {
		mode.mu.Unlock()
		return
	}
	mode.disposed = true
	unsub := mode.unsub
	mode.unsub = nil
	cancel := mode.cancel
	mode.cancel = nil
	mode.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if unsub != nil {
		unsub()
	}
	mode.ui.close()
	mode.host.Dispose()
}

func (mode *rpcMode) writeObject(value any) error {
	encoded, err := ai.Marshal(value)
	if err != nil {
		return err
	}
	mode.output.writeLine(encoded)
	return nil
}

func (mode *rpcMode) handleLine(line []byte, commands *sync.WaitGroup) {
	var raw rawRPCObject
	if err := json.Unmarshal(line, &raw); err != nil {
		_ = mode.writeObject(rpcError("", false, "parse", "Failed to parse command: "+javascriptParseError(line, err)))
		return
	}
	typeRaw, hasType := raw["type"]
	typeName, err := rawString(typeRaw)
	// json.Unmarshal leaves the target untouched for JSON null, so route the
	// literal null explicitly alongside missing and non-string members.
	if err != nil || !hasType || string(typeRaw) == "null" {
		// Upstream dispatches untyped: a missing or non-string type member
		// reaches handleCommand's default and answers Unknown command with
		// the raw id/type values echoed (rpc-mode.ts:695-698,735-770).
		_ = mode.writeObject(rpcUnknownCommandResponse(raw))
		// Upstream checks after every handled command, including the unknown
		// default (rpc-mode.ts:766-771).
		mode.checkShutdownRequested()
		return
	}
	if typeName == "extension_ui_response" {
		var response RPCExtensionUIResponse
		if err := json.Unmarshal(line, &response); err == nil {
			mode.ui.HandleResponse(response)
		}
		return
	}
	var command RPCCommand
	if err := json.Unmarshal(line, &command); err != nil {
		idRaw, hasID := raw["id"]
		id, _ := rawString(idRaw)
		_ = mode.writeObject(rpcError(id, hasID, typeName, err.Error()))
		return
	}
	_, command.HasID = raw["id"]
	session := mode.host.Session()
	var preflight <-chan struct{}
	// JS drains the replacement handler's promise continuation before the next
	// stdin event; keep that state barrier asynchronous so UI replies stay live.
	if command.Type == "get_state" {
		mode.promptMu.Lock()
		if mode.promptSession != session {
			preflight = mode.promptPreflight
		}
		mode.promptMu.Unlock()
	}
	execute := func() {
		if preflight != nil {
			select {
			case <-preflight:
			case <-mode.ctx.Done():
			}
		}
		response := mode.handleCommand(session, command)
		if response != nil {
			_ = mode.writeObject(*response)
		}
		// Upstream checks after every handled command (rpc-mode.ts:764-771).
		mode.checkShutdownRequested()
	}
	if preflight != nil || rpcCommandIsAsync(command.Type) {
		commands.Add(1)
		go func() {
			defer commands.Done()
			execute()
		}()
	} else {
		execute()
	}
}

func rpcCommandIsAsync(command string) bool {
	switch command {
	case "prompt", "steer", "follow_up", "abort", "new_session", "set_model", "cycle_model", "get_available_models", "compact", "bash",
		"export_html", "switch_session", "fork", "clone":
		return true
	default:
		return false
	}
}

func (mode *rpcMode) handleCommand(session *agent.SessionRuntime, command RPCCommand) *RPCResponse { //nolint:gocyclo,cyclop,funlen
	if session == nil {
		response := rpcError(command.ID, command.HasID, command.Type, "Session is unavailable")
		return &response
	}
	success := func(data ...any) *RPCResponse {
		response := rpcSuccess(command.ID, command.HasID, command.Type)
		if len(data) > 0 {
			response.Data, response.HasData = data[0], true
		}
		return &response
	}
	failure := func(err error) *RPCResponse {
		response := rpcError(command.ID, command.HasID, command.Type, err.Error())
		return &response
	}

	switch command.Type {
	case "prompt":
		mode.promptMu.Lock()
		state := session.State()
		if state.IsStreaming || mode.prompting {
			switch command.StreamingBehavior {
			case "steer":
				if err := session.SteerImages(command.Message, command.Images); err != nil {
					mode.promptMu.Unlock()
					return failure(err)
				}
			case "followUp":
				if err := session.FollowUpImages(command.Message, command.Images); err != nil {
					mode.promptMu.Unlock()
					return failure(err)
				}
			default:
				mode.promptMu.Unlock()
				return failure(errors.New("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.")) //nolint:staticcheck // Upstream RPC error text.
			}
			mode.promptMu.Unlock()
			return success()
		}
		mode.prompting = true
		mode.promptSession = session
		preflight := make(chan struct{})
		mode.promptPreflight = preflight
		mode.promptMu.Unlock()
		finishPreflight := sync.OnceFunc(func() { close(preflight) })
		defer func() {
			finishPreflight()
			mode.promptMu.Lock()
			mode.prompting = false
			mode.promptSession = nil
			mode.promptPreflight = nil
			mode.promptMu.Unlock()
		}()
		// Upstream dispatches extension commands before any model/API-key
		// validation and emits the authoritative response from preflightResult
		// (agent-session.ts:1102-1117, rpc-mode.ts:393-414).
		responded := false
		err := session.PromptWithOptions(mode.ctx, command.Message, &agent.PromptOptions{
			Images: command.Images,
			Source: extensions.InputRPC,
			PreflightResult: func(succeeded bool) {
				if !succeeded {
					return
				}
				responded = true
				_ = mode.writeObject(*success())
				finishPreflight()
			},
		})
		if err != nil && !responded {
			_ = mode.writeObject(*failure(err))
			finishPreflight()
		}
		return nil
	case "steer":
		if err := session.SteerImages(command.Message, command.Images); err != nil {
			return failure(err)
		}
		return success()
	case "follow_up":
		if err := session.FollowUpImages(command.Message, command.Images); err != nil {
			return failure(err)
		}
		return success()
	case "abort":
		session.Abort()
		_ = session.WaitForIdle(mode.ctx)
		return success()
	case "clear_queue":
		cleared := session.ClearQueue()
		return success(struct {
			Steering []string `json:"steering"`
			FollowUp []string `json:"followUp"`
		}{cleared.Steering, cleared.FollowUp})
	case "new_session":
		cancelled, err := mode.host.NewSession(command.ParentSession)
		if err != nil {
			return failure(err)
		}
		if !cancelled {
			if err := mode.bindSession(); err != nil {
				return failure(err)
			}
		}
		return success(struct {
			Cancelled bool `json:"cancelled"`
		}{cancelled})
	case "get_state":
		state := session.State()
		manager := session.Manager()
		name := manager.GetSessionName()
		result := RPCSessionState{
			Model: state.Model, ThinkingLevel: state.ThinkingLevel, IsStreaming: state.IsStreaming,
			IsCompacting: session.IsCompacting(), SteeringMode: string(session.SteeringMode()),
			FollowUpMode: string(session.FollowUpMode()), SessionFile: manager.GetSessionFile(),
			SessionID: manager.GetSessionID(), AutoCompactionEnabled: session.AutoCompactionEnabled(),
			MessageCount: len(state.Messages), PendingMessageCount: session.PendingMessageCount(),
		}
		if name != nil {
			value := *name
			result.SessionName = &value
		}
		return success(result)
	case "get_messages":
		return success(struct {
			Messages engine.AgentMessages `json:"messages"`
		}{session.State().Messages})
	case "set_model":
		models := session.AvailableModels()
		index := slices.IndexFunc(models, func(model ai.Model) bool {
			return string(model.Provider) == command.Provider && model.ID == command.ModelID
		})
		if index < 0 {
			return failure(errors.New("Model not found: " + command.Provider + "/" + command.ModelID))
		}
		if err := session.SetModel(mode.ctx, models[index]); err != nil {
			return failure(err)
		}
		return success(models[index])
	case "cycle_model":
		result, err := session.CycleModel(mode.ctx)
		if err != nil {
			return failure(err)
		}
		return success(result)
	case "get_available_models":
		return success(struct {
			Models []ai.Model `json:"models"`
		}{session.AvailableModels()})
	case "set_thinking_level":
		if err := session.SetThinkingLevel(ai.ModelThinkingLevel(command.Level)); err != nil {
			return failure(err)
		}
		return success()
	case "cycle_thinking_level":
		level, err := session.CycleThinkingLevel()
		if err != nil {
			return failure(err)
		}
		if level == nil {
			return success(nil)
		}
		return success(struct {
			Level ai.ModelThinkingLevel `json:"level"`
		}{*level})
	case "get_available_thinking_levels":
		return success(RPCThinkingLevels{Levels: session.AvailableThinkingLevels()})
	case "set_steering_mode":
		session.SetSteeringMode(engine.QueueMode(command.Mode))
		return success()
	case "set_follow_up_mode":
		session.SetFollowUpMode(engine.QueueMode(command.Mode))
		return success()
	case "compact":
		result, err := session.Compact(mode.ctx, command.CustomInstructions)
		if err != nil {
			return failure(err)
		}
		return success(result)
	case "set_auto_compaction":
		enabled := true
		if command.Enabled != nil {
			enabled = *command.Enabled
		}
		session.SetAutoCompactionEnabled(enabled)
		return success()
	case "set_auto_retry":
		enabled := true
		if command.Enabled != nil {
			enabled = *command.Enabled
		}
		session.SetAutoRetryEnabled(enabled)
		return success()
	case "abort_retry":
		session.AbortRetry()
		return success()
	case "bash":
		var requestID *string
		if command.HasID {
			requestID = &command.ID
		}
		result, err := session.ExecuteUserBashWithID(mode.ctx, command.Command, command.ExcludeFromContext, requestID)
		if err != nil {
			return failure(err)
		}
		return success(result)
	case "abort_bash":
		session.AbortBash()
		return success()
	case "get_session_stats":
		return success(session.GetSessionStats())
	case "export_html":
		path, err := session.ExportHTML(command.OutputPath)
		if err != nil {
			return failure(err)
		}
		return success(struct {
			Path string `json:"path"`
		}{path})
	case "switch_session":
		cancelled, err := mode.host.SwitchSession(command.SessionPath)
		if err != nil {
			return failure(err)
		}
		if !cancelled {
			if err := mode.bindSession(); err != nil {
				return failure(err)
			}
		}
		return success(struct {
			Cancelled bool `json:"cancelled"`
		}{cancelled})
	case "fork":
		text, cancelled, err := mode.host.Fork(command.EntryID, false)
		if err != nil {
			return failure(err)
		}
		if !cancelled {
			if err := mode.bindSession(); err != nil {
				return failure(err)
			}
		}
		return success(struct {
			Text      string `json:"text"`
			Cancelled bool   `json:"cancelled"`
		}{text, cancelled})
	case "clone":
		leafID := session.Manager().GetLeafID()
		if leafID == nil {
			return failure(errors.New("Cannot clone session: no current entry selected")) //nolint:staticcheck // Upstream RPC error text.
		}
		_, cancelled, err := mode.host.Fork(*leafID, true)
		if err != nil {
			return failure(err)
		}
		if !cancelled {
			if err := mode.bindSession(); err != nil {
				return failure(err)
			}
		}
		return success(struct {
			Cancelled bool `json:"cancelled"`
		}{cancelled})
	case "get_fork_messages":
		return success(struct {
			Messages any `json:"messages"`
		}{session.GetUserMessagesForForking()})
	case "get_entries":
		entries := session.Manager().GetEntries()
		if command.Since != nil {
			index := slices.IndexFunc(entries, func(entry sessionstore.SessionEntry) bool { return entry.ID == *command.Since })
			if index < 0 {
				return failure(errors.New("Entry not found: " + *command.Since))
			}
			entries = entries[index+1:]
		}
		return success(struct {
			Entries any     `json:"entries"`
			LeafID  *string `json:"leafId"`
		}{entries, session.Manager().GetLeafID()})
	case "get_tree":
		tree := session.Manager().GetTree()
		if tree == nil {
			tree = []*sessionstore.SessionTreeNode{}
		}
		return success(struct {
			Tree   any     `json:"tree"`
			LeafID *string `json:"leafId"`
		}{tree, session.Manager().GetLeafID()})
	case "get_last_assistant_text":
		return success(struct {
			Text *string `json:"text,omitempty"`
		}{session.GetLastAssistantText()})
	case "set_session_name":
		name := strings.TrimFunc(command.Name, jstrim.IsSpace)
		if name == "" {
			return failure(errors.New("Session name cannot be empty")) //nolint:staticcheck // Wire error matches upstream.
		}
		if err := session.SetSessionName(name); err != nil {
			return failure(err)
		}
		return success()
	case "get_commands":
		commands := []RPCSlashCommand{}
		if mode.options.Commands != nil {
			commands = mode.options.Commands()
			if commands == nil {
				commands = []RPCSlashCommand{}
			}
		}
		return success(struct {
			Commands []RPCSlashCommand `json:"commands"`
		}{commands})
	default:
		return failure(errors.New("Unknown command: " + command.Type))
	}
}

func rpcSuccess(id string, hasID bool, command string) RPCResponse {
	return RPCResponse{ID: id, Type: "response", Command: command, Success: true, HasID: hasID}
}

func rpcError(id string, hasID bool, command, message string) RPCResponse {
	return RPCResponse{ID: id, Type: "response", Command: command, Error: message, HasID: hasID}
}

// rpcRawResponse echoes id/command JSON for the untyped dispatch path. Field
// order matches RPCResponse (id, type, command, success, error);
// JSON.stringify drops absent members, hence omitempty.
type rpcRawResponse struct {
	ID      json.RawMessage `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command json.RawMessage `json:"command,omitempty"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
}

func rpcUnknownCommandResponse(raw rawRPCObject) rpcRawResponse {
	return rpcRawResponse{
		ID:      canonicalRawJSON(raw["id"]),
		Type:    "response",
		Command: canonicalRawJSON(raw["type"]),
		Success: false,
		Error:   "Unknown command: " + jsDisplayString(raw["type"]),
	}
}

// canonicalRawJSON re-serializes an echoed member the way JSON.stringify
// renders JSON.parse's value: JS number formatting (5.0 -> 5, -0 -> 0),
// insertion-ordered object keys, and no interior whitespace
// (rpc-mode.ts:695-698 echoes the parsed id/type values).
func canonicalRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeOrderedJSONValue(decoder)
	if err != nil {
		return raw
	}
	encoded, err := jsonwire.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

func decodeOrderedJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := jsonwire.OrderedObject{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := nameToken.(string)
				if !ok {
					return nil, fmt.Errorf("unexpected object member name %v", nameToken)
				}
				value, err := decodeOrderedJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				// JSON.parse keeps a duplicate key at its first position with
				// its last value, matching OrderedObject.Set.
				object.Set(name, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			values := []any{}
			for decoder.More() {
				value, err := decodeOrderedJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return values, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", typed)
	case json.Number:
		value, err := typed.Float64()
		if err != nil {
			// JSON.parse overflows to Infinity, which JSON.stringify
			// renders as null.
			if parsed, parseErr := strconv.ParseFloat(typed.String(), 64); parseErr != nil && math.IsInf(parsed, 0) {
				return nil, nil
			}
			return nil, err
		}
		return value, nil
	default:
		return token, nil
	}
}

// jsDisplayString renders a decoded JSON value the way a JS template literal
// does (String(value)); an absent member renders "undefined".
func jsDisplayString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "undefined"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return jsValueString(value)
}

func jsValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case string:
		return typed
	case float64:
		return jsNumberString(typed)
	case []any:
		parts := make([]string, len(typed))
		for index, element := range typed {
			if element == nil {
				continue // Array.prototype.join renders null empty.
			}
			parts[index] = jsValueString(element)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

func jsNumberString(value float64) string {
	if value == 0 {
		return "0" // String(-0) === "0"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	text := string(encoded)
	// encoding/json zero-pads exponents (1e-07); Number#toString does not.
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		return text[:index+2] + strings.TrimLeft(text[index+2:], "0")
	}
	return text
}

func rawString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func readStrictJSONLines(reader io.Reader, lines chan<- []byte, readErrors chan<- error) {
	defer close(lines)
	buffer := bufio.NewReader(reader)
	for {
		line, err := buffer.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			lines <- append([]byte(nil), line...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			readErrors <- err
			return
		}
	}
}

func javascriptParseError(line []byte, parseError error) string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return "Unexpected end of JSON input"
	}
	message := parseError.Error()
	if strings.Contains(message, "unexpected end") {
		if len(trimmed) == 1 && trimmed[0] == '{' {
			position := len(line)
			return fmt.Sprintf("Expected property name or '}' in JSON at position %d (line 1 column %d)", position, position+1)
		}
		return "Unexpected end of JSON input"
	}
	var syntaxError *json.SyntaxError
	if errors.As(parseError, &syntaxError) && strings.HasPrefix(message, "invalid character ") {
		offset := int(syntaxError.Offset) - 1
		if offset >= 0 && offset < len(line) {
			invalid, _ := utf8.DecodeRune(line[offset:])
			return fmt.Sprintf("Unexpected token '%c', \"%s\" is not valid JSON", invalid, line)
		}
	}
	return message
}
