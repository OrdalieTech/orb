package modes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

type RPCSessionHost interface {
	Session() *codingagent.SessionRuntime
	NewSession(parentSession string) (cancelled bool, err error)
	SwitchSession(sessionPath string) (cancelled bool, err error)
	Fork(entryID string, atEntry bool) (text string, cancelled bool, err error)
	Dispose()
}

type rpcSessionRebindHost interface {
	SetRebindSession(func(*codingagent.SessionRuntime) error)
}

type RPCModeOptions struct {
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	Commands        func() []RPCSlashCommand
	BindExtensionUI func(*RPCExtensionUI)
}

type rpcMode struct {
	ctx       context.Context
	cancel    context.CancelFunc
	host      RPCSessionHost
	options   RPCModeOptions
	output    *serializedOutput
	ui        *RPCExtensionUI
	mu        sync.Mutex
	unsub     func()
	disposed  bool
	promptMu  sync.Mutex
	prompting bool
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
	if options.BindExtensionUI != nil {
		options.BindExtensionUI(mode.ui)
	}
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

func (mode *rpcMode) bindReplacement(session *codingagent.SessionRuntime) error {
	if session == nil {
		return errors.New("rpc mode: session replacement returned nil")
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
	mode.unsub = session.Subscribe(mode.output.writeSessionEvent)
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
	typeName, err := rawString(raw["type"])
	if err != nil {
		_ = mode.writeObject(rpcError("", false, "", err.Error()))
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
	execute := func() {
		response := mode.handleCommand(session, command)
		if response != nil {
			_ = mode.writeObject(*response)
		}
	}
	if rpcCommandIsAsync(command.Type) {
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

func (mode *rpcMode) handleCommand(session *codingagent.SessionRuntime, command RPCCommand) *RPCResponse { //nolint:gocyclo,cyclop,funlen
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
		if err := session.PromptPreflight(mode.ctx); err != nil {
			mode.promptMu.Unlock()
			return failure(err)
		}
		mode.prompting = true
		mode.promptMu.Unlock()
		defer func() {
			mode.promptMu.Lock()
			mode.prompting = false
			mode.promptMu.Unlock()
		}()
		response := success()
		if err := mode.writeObject(*response); err != nil {
			return nil
		}
		_ = session.PromptAfterPreflight(mode.ctx, command.Message, command.Images...)
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
			Messages agent.AgentMessages `json:"messages"`
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
	case "set_steering_mode":
		session.SetSteeringMode(agent.QueueMode(command.Mode))
		return success()
	case "set_follow_up_mode":
		session.SetFollowUpMode(agent.QueueMode(command.Mode))
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
		result, err := session.ExecuteBash(mode.ctx, command.Command, command.ExcludeFromContext)
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
		name := strings.TrimFunc(command.Name, isJSTrimSpace)
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

func isJSTrimSpace(character rune) bool {
	return character == '\ufeff' || character == '\u00a0' || character == '\u1680' ||
		character >= '\u2000' && character <= '\u200a' || character == '\u2028' || character == '\u2029' ||
		character == '\u202f' || character == '\u205f' || character == '\u3000' ||
		character == '\t' || character == '\n' || character == '\v' || character == '\f' || character == '\r' || character == ' '
}
