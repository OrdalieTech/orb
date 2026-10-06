// Package acp serves the Agent Client Protocol (agentclientprotocol.com):
// newline-delimited JSON-RPC 2.0 through which a client, an editor or a chat
// platform's agent harness, drives any number of Orb sessions in one process.
// An ACP session id is the Orb session id, so a stored session reopens by id.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
)

// Host opens the sessions a client drives; close disposes one.
type Host interface {
	Open(ctx context.Context, options Options) (session *agent.AgentSessionRuntime, close func(), err error)
}

// Options open one session: a new one in CWD, or the stored session ID.
type Options struct {
	ID  string
	CWD string
	// SystemPrompt replaces Orb's base prompt, as --system-prompt does; Append
	// adds to it, as --append-system-prompt does.
	SystemPrompt *string
	Append       string
	MCPServers   []MCPServer
}

// MCPServer is an ACP McpServer: stdio (command) or remote (type and url).
type MCPServer struct {
	Name    string     `json:"name"`
	Type    string     `json:"type,omitempty"`
	Command string     `json:"command,omitempty"`
	Args    []string   `json:"args,omitempty"`
	Env     []Variable `json:"env,omitempty"`
	URL     string     `json:"url,omitempty"`
	Headers []Variable `json:"headers,omitempty"`
}

type Variable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// maxLive bounds the sessions kept running per connection; an idle one past it
// is disposed and reopens from the store, with its options, on its next prompt.
const maxLive = 8

const (
	codeParse          = -32700
	codeInvalidParams  = -32602
	codeMethodNotFound = -32601
	codeInternal       = -32603
)

type server struct {
	ctx     context.Context
	host    Host
	version string

	writeMu sync.Mutex
	out     *bufio.Writer

	mu       sync.Mutex
	live     []*live // most recently used last
	options  map[string]Options
	protocol int
}

type live struct {
	id      string
	runtime *agent.AgentSessionRuntime
	close   func()
	turn    sync.Mutex // one prompt at a time
	mu      sync.Mutex
	busy    bool
	cancel  bool
}

// Serve answers one client until in ends or ctx is done.
func Serve(ctx context.Context, in io.Reader, out io.Writer, host Host, version string) error {
	ctx, stop := context.WithCancel(ctx)
	s := &server{ctx: ctx, host: host, version: version, out: bufio.NewWriter(out), options: map[string]Options{}}
	var requests sync.WaitGroup
	defer func() {
		stop()
		requests.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, session := range s.live {
			session.close()
		}
	}()
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var message struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			switch {
			case json.Unmarshal(line, &message) != nil:
				s.fail(nil, codeParse, "parse error")
			case message.Method == "":
				// A response to a request Orb never sends.
			case message.ID == nil:
				s.notification(message.Method, message.Params)
			default:
				requests.Add(1)
				go func() {
					defer requests.Done()
					result, err := s.request(message.Method, message.Params)
					var rpc *rpcError
					switch {
					case errors.As(err, &rpc):
						s.fail(message.ID, rpc.code, rpc.message)
					case err != nil:
						s.fail(message.ID, codeInternal, err.Error())
					default:
						s.reply(message.ID, result)
					}
				}()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

type rpcError struct {
	code    int
	message string
}

func (e *rpcError) Error() string { return e.message }

func invalid(format string, args ...any) error {
	return &rpcError{code: codeInvalidParams, message: fmt.Sprintf(format, args...)}
}

func (s *server) request(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalid("initialize: %v", err)
		}
		// Protocol version 2, still provisional, moves a client's harness
		// prompt into session/new's systemPrompt instead of every user message.
		s.mu.Lock()
		s.protocol = min(max(p.ProtocolVersion, 1), 2)
		s.mu.Unlock()
		return map[string]any{
			"protocolVersion": s.protocol,
			"agentCapabilities": map[string]any{
				"loadSession":        true,
				"promptCapabilities": map[string]bool{"image": true, "audio": false, "embeddedContext": true},
				"mcpCapabilities":    map[string]bool{"http": true, "sse": false},
			},
			"agentInfo":   map[string]string{"name": "orb", "title": "Orb", "version": s.version},
			"authMethods": []any{},
		}, nil
	case "authenticate":
		return map[string]any{}, nil
	case "session/new", "session/load":
		var p struct {
			SessionID    string          `json:"sessionId"`
			CWD          string          `json:"cwd"`
			MCPServers   []MCPServer     `json:"mcpServers"`
			SystemPrompt json.RawMessage `json:"systemPrompt"`
			Meta         struct {
				SystemPrompt json.RawMessage `json:"systemPrompt"`
				SessionTitle string          `json:"sessionTitle"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalid("%s: %v", method, err)
		}
		if !filepath.IsAbs(p.CWD) {
			return nil, invalid("%s: cwd must be an absolute path", method)
		}
		options := Options{ID: p.SessionID, CWD: p.CWD, MCPServers: p.MCPServers}
		if method == "session/load" && p.SessionID == "" {
			return nil, invalid("session/load: sessionId is required")
		}
		for _, raw := range []json.RawMessage{p.SystemPrompt, p.Meta.SystemPrompt} {
			if err := options.prompt(raw); err != nil {
				return nil, err
			}
		}
		session, err := s.open(options)
		if err != nil {
			return nil, err
		}
		runtime := session.runtime.Session()
		if title := strings.TrimSpace(p.Meta.SessionTitle); title != "" {
			if err := runtime.SetSessionName(title); err != nil {
				return nil, err
			}
		}
		if method == "session/load" {
			s.replay(session)
			return nil, nil
		}
		// Notifications for a session the client has not seen yet are dropped
		// by some clients, so the commands follow the response.
		go s.update(session.id, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": commands(runtime)})
		return map[string]any{"sessionId": session.id, "configOptions": configOptions(runtime)}, nil
	case "session/prompt":
		var p struct {
			SessionID string            `json:"sessionId"`
			Prompt    []json.RawMessage `json:"prompt"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalid("session/prompt: %v", err)
		}
		session, err := s.session(p.SessionID)
		if err != nil {
			return nil, err
		}
		text, images := promptInput(p.Prompt)
		return s.prompt(session, text, images)
	case "session/set_config_option", "session/set_model":
		var p struct {
			SessionID string `json:"sessionId"`
			ConfigID  string `json:"configId"`
			Value     string `json:"value"`
			ModelID   string `json:"modelId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalid("%s: %v", method, err)
		}
		if method == "session/set_model" {
			p.ConfigID, p.Value = "model", p.ModelID
		}
		session, err := s.session(p.SessionID)
		if err != nil {
			return nil, err
		}
		runtime := session.runtime.Session()
		if err := configure(s.ctx, runtime, p.ConfigID, p.Value); err != nil {
			return nil, err
		}
		if method == "session/set_model" {
			return map[string]any{}, nil
		}
		return map[string]any{"configOptions": configOptions(runtime)}, nil
	}
	return nil, &rpcError{code: codeMethodNotFound, message: "method not found: " + method}
}

func (s *server) notification(method string, params json.RawMessage) {
	if method != "session/cancel" {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	s.mu.Lock()
	index := slices.IndexFunc(s.live, func(session *live) bool { return session.id == p.SessionID })
	var session *live
	if index >= 0 {
		session = s.live[index]
	}
	s.mu.Unlock()
	if session == nil {
		return
	}
	session.mu.Lock()
	if session.busy {
		session.cancel = true
	}
	session.mu.Unlock()
	session.runtime.Session().Abort()
}

// prompt parses the harness prompt a session/new carries: a string replaces
// Orb's base prompt, {"append": text} or {"replace": text} says which.
func (options *Options) prompt(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.TrimSpace(text) != "" {
			options.SystemPrompt = &text
		}
		return nil
	}
	var object map[string]string
	if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
		return invalid("systemPrompt must be a string or one append or replace string")
	}
	for mode, text := range object {
		switch mode {
		case "append":
			options.Append = text
		case "replace":
			options.SystemPrompt = &text
		default:
			return invalid("systemPrompt must be a string or one append or replace string")
		}
	}
	return nil
}

// open starts a session and makes it the most recent live one, disposing the
// least recently used idle session past maxLive.
func (s *server) open(options Options) (*live, error) {
	runtime, close, err := s.host.Open(s.ctx, options)
	if err != nil {
		return nil, err
	}
	session := &live{id: runtime.Session().Manager().GetSessionID(), runtime: runtime, close: close}
	options.ID = session.id
	s.mu.Lock()
	defer s.mu.Unlock()
	s.options[session.id] = options
	s.live = append(s.live, session)
	for len(s.live) > maxLive {
		index := slices.IndexFunc(s.live, func(candidate *live) bool { return !candidate.running() })
		if index < 0 || index == len(s.live)-1 {
			break
		}
		evicted := s.live[index]
		s.live = slices.Delete(s.live, index, index+1)
		go evicted.close()
	}
	return session, nil
}

func (session *live) running() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.busy
}

// session finds a live session, or reopens it: with the options it was opened
// with on this connection, or as stored when another connection opened it.
func (s *server) session(id string) (*live, error) {
	if id == "" {
		return nil, invalid("sessionId is required")
	}
	s.mu.Lock()
	if index := slices.IndexFunc(s.live, func(session *live) bool { return session.id == id }); index >= 0 {
		session := s.live[index]
		s.live = append(slices.Delete(s.live, index, index+1), session)
		s.mu.Unlock()
		return session, nil
	}
	options, known := s.options[id]
	s.mu.Unlock()
	if !known {
		options = Options{ID: id}
	}
	session, err := s.open(options)
	if err != nil {
		return nil, invalid("unknown session %s: %v", id, err)
	}
	return session, nil
}

// prompt runs one turn and answers once the session settles.
func (s *server) prompt(session *live, text string, images []*ai.ImageContent) (any, error) {
	session.turn.Lock()
	defer session.turn.Unlock()
	session.mu.Lock()
	session.busy, session.cancel = true, false
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.busy = false
		session.mu.Unlock()
	}()
	runtime := session.runtime.Session()
	if handled, err := s.command(session, runtime, text); handled {
		if err != nil {
			return nil, err
		}
		return map[string]string{"stopReason": "end_turn"}, nil
	}
	tools := map[string]bool{}
	unsubscribe := runtime.Subscribe(func(event any) {
		if update := translate(event, tools); update != nil {
			s.update(session.id, update)
		}
	})
	before := len(runtime.State().Messages)
	err := runtime.PromptWithOptions(s.ctx, text, &agent.PromptOptions{Images: images})
	if err == nil {
		err = runtime.WaitForIdle(s.ctx)
	}
	unsubscribe()
	s.usage(session.id, runtime)
	session.mu.Lock()
	cancelled := session.cancel
	session.mu.Unlock()
	if cancelled {
		return map[string]string{"stopReason": "cancelled"}, nil
	}
	if err != nil {
		return nil, err
	}
	messages := runtime.State().Messages
	for index := len(messages) - 1; index >= before && index >= 0; index-- {
		assistant, ok := messages[index].(*ai.AssistantMessage)
		if !ok {
			continue
		}
		switch assistant.StopReason {
		case ai.StopReasonError:
			message := "the model call failed"
			if assistant.ErrorMessage != nil {
				message = *assistant.ErrorMessage
			}
			return nil, errors.New(message)
		case ai.StopReasonAborted:
			return map[string]string{"stopReason": "cancelled"}, nil
		case ai.StopReasonLength:
			return map[string]string{"stopReason": "max_tokens"}, nil
		}
		break
	}
	return map[string]string{"stopReason": "end_turn"}, nil
}

// usage reports the session's cumulative usage as the unstable
// _goose/unstable/session/update extension other ACP agents also send, which
// clients turn into per-turn metrics.
func (s *server) usage(id string, runtime *agent.AgentSession) {
	stats := runtime.GetSessionStats()
	update := map[string]any{
		"sessionUpdate":                "usage_update",
		"accumulatedInputTokens":       stats.Tokens.Input + stats.Tokens.CacheRead + stats.Tokens.CacheWrite,
		"accumulatedOutputTokens":      stats.Tokens.Output,
		"accumulatedCachedInputTokens": stats.Tokens.CacheRead,
		"accumulatedCacheWriteTokens":  stats.Tokens.CacheWrite,
		"accumulatedCost":              stats.Cost,
	}
	if usage := stats.ContextUsage; usage != nil {
		if usage.Tokens != nil {
			update["used"] = *usage.Tokens
		}
		update["contextLimit"] = usage.ContextWindow
	}
	s.send(map[string]any{"jsonrpc": "2.0", "method": "_goose/unstable/session/update", "params": map[string]any{"sessionId": id, "update": update}})
}

func (s *server) update(id string, update map[string]any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": id, "update": update}})
}

func (s *server) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *server) fail(id json.RawMessage, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *server) send(frame any) {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.out.Write(append(encoded, '\n'))
	_ = s.out.Flush()
}
