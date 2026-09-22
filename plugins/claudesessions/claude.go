// Package claudesessions adapts native Claude Agent SDK sessions to Orb events.
// Its host runs the official SDK and an unmodified Claude executable; Orb never
// reads, copies, refreshes or proxies Claude credentials.
package claudesessions

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/questions"
	"github.com/OrdalieTech/orb/sandbox"
)

const Name = "claude-sessions"
const SDKVersion = "0.3.278"

//go:embed host.mjs
var hostSource string

type Options struct {
	Sandbox sandbox.Mode
	// RenderText optionally supplies text components for SDK-hosted tool calls.
	RenderText        func(string) extensions.Component
	Node, SDK, Claude string
	Env               []string
	Manager           *session.SessionManager
	Ask               func(context.Context, string, []string) (string, error)
}

// Native SDK events may omit utilization; absence is never treated as zero use.
type limitWindow struct {
	Utilization *float64 `json:"utilization,omitempty"`
	ResetsAt    int64    `json:"resetsAt,omitempty"`
}
type subscriptionLimits struct {
	Status        string `json:"status"`
	RateLimitType string `json:"rateLimitType,omitempty"`
	limitWindow
	UnifiedWindows map[string]limitWindow `json:"unifiedWindows,omitempty"`
	ObservedAt     time.Time              `json:"observedAt"`
}

type checkpoint struct {
	Owner   string `json:"owner"`
	Session string `json:"session"`
	At      string `json:"at,omitempty"`
}
type Driver struct {
	options Options
	latest  checkpoint
}

func New(options Options) (*Driver, error) {
	if err := checkSandbox(options.Sandbox); err != nil {
		return nil, err
	}
	if options.Manager == nil || options.Node == "" || options.SDK == "" || options.Claude == "" || options.Env == nil {
		return nil, errors.New("claude sessions require explicit executables, SDK, environment and session storage")
	}
	if !filepath.IsAbs(options.Node) || !filepath.IsAbs(options.SDK) || !filepath.IsAbs(options.Claude) {
		return nil, errors.New("claude executable and SDK paths must be absolute")
	}
	options.Env = append([]string{}, options.Env...)
	d := &Driver{options: options}
	for _, entry := range options.Manager.GetEntries() {
		if entry.CustomType == Name {
			if err := json.Unmarshal(entry.Data, &d.latest); err != nil {
				return nil, fmt.Errorf("invalid Claude checkpoint: %w", err)
			}
		}
	}
	saved, err := d.checkpoint()
	if err != nil {
		return nil, err
	}
	if saved.Session == "" {
		for _, entry := range options.Manager.GetBranch() {
			if entry.Type == "message" {
				return nil, errors.New("this transcript has no native Claude session; start a new Claude conversation")
			}
		}
	}
	return d, nil
}

func (d *Driver) checkpoint() (checkpoint, error) {
	for entry := d.options.Manager.GetLeafEntry(); entry != nil; {
		if entry.CustomType == Name {
			var saved checkpoint
			if err := json.Unmarshal(entry.Data, &saved); err != nil {
				return saved, fmt.Errorf("invalid Claude checkpoint: %w", err)
			}
			if saved.Owner == d.options.Manager.GetSessionID() && d.latest.Session != "" && saved != d.latest {
				return saved, errors.New("native Claude history differs from this branch; fork the conversation before continuing")
			}
			return saved, nil
		}
		if entry.ParentID == nil {
			break
		}
		entry = d.options.Manager.GetEntry(*entry.ParentID)
	}
	return checkpoint{}, nil
}

// Loop owns the complete native turn. Orb queues are drained only between native
// turns, so no tool is executed twice by an outer Orb agent loop.
func (d *Driver) Loop(ctx context.Context, prompts engine.AgentMessages, state engine.AgentContext, config engine.AgentLoopConfig, emit engine.EventSink) error {
	users := make(engine.AgentMessages, 0, len(prompts))
	for _, prompt := range prompts {
		switch p := prompt.(type) {
		case *ai.UserMessage:
			users = append(users, p)
		case ai.UserMessage:
			users = append(users, &p)
		case *ai.SystemMessage, ai.SystemMessage: // The native session owns its system prompt.
		default:
			return fmt.Errorf("claude sessions cannot consume Orb context message %T", prompt)
		}
	}
	prompts = users
	if len(prompts) == 0 {
		return errors.New("claude sessions require a new prompt; interrupted work is never replayed automatically")
	}
	var generated engine.AgentMessages
	sink := emit
	emit = func(ctx context.Context, event engine.AgentEvent) error {
		if err := sink(ctx, event); err != nil {
			return err
		}
		if end, ok := event.(engine.MessageEndEvent); ok {
			generated = append(generated, end.Message)
		}
		return nil
	}
	if err := emit(ctx, engine.AgentStartEvent{}); err != nil {
		return err
	}
	for len(prompts) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(ctx, engine.TurnStartEvent{}); err != nil {
			return err
		}
		for _, prompt := range prompts {
			if _, ok := prompt.(*ai.UserMessage); !ok {
				return errors.New("claude sessions accept user prompts, not replacement history")
			}
			if err := emit(ctx, engine.MessageStartEvent{Message: prompt}); err != nil {
				return err
			}
			if err := emit(ctx, engine.MessageEndEvent{Message: prompt}); err != nil {
				return err
			}
		}
		if err := d.turn(ctx, prompts, config, emit); err != nil {
			return err
		}
		var err error
		prompts = nil
		if config.GetSteeringMessages != nil {
			prompts, err = config.GetSteeringMessages(ctx)
		}
		if err != nil {
			return err
		}
		if len(prompts) == 0 && config.GetFollowUpMessages != nil {
			prompts, err = config.GetFollowUpMessages(ctx)
		}
		if err != nil {
			return err
		}
	}
	return emit(ctx, engine.AgentEndEvent{Messages: generated})
}

func (d *Driver) turn(ctx context.Context, prompts engine.AgentMessages, config engine.AgentLoopConfig, emit engine.EventSink) error {
	model := config.Model
	saved, err := d.checkpoint()
	if err != nil {
		return err
	}
	id := d.options.Manager.GetSessionID()
	start := map[string]any{"type": "start", "sdk": d.options.SDK, "claude": d.options.Claude, "cwd": d.options.Manager.GetCWD(), "session": id, "model": model.ID}
	var info modelInfo
	_ = json.Unmarshal(model.Compat, &info)
	if info.Adaptive {
		start["thinking"] = "disabled"
		if config.Reasoning != nil {
			start["thinking"] = "adaptive"
		}
	}
	if config.Reasoning != nil && info.Effort {
		requested := string(*config.Reasoning)
		supported := false
		for _, level := range info.Levels {
			if requested == level {
				supported = true
				break
			}
		}
		if !supported {
			return errors.New("selected effort is not supported by this Claude model")
		}
		start["effort"] = requested
	}

	if saved.Session != "" {
		start["resume"] = saved.Session
		if saved.Owner != id {
			if saved.At == "" {
				return errors.New("claude fork has no confirmed native checkpoint")
			}
			start["fork"], start["at"] = true, saved.At
		}
	}
	var blocks []any
	for _, p := range prompts {
		raw, e := json.Marshal(p)
		if e != nil {
			return e
		}
		var m struct {
			Content json.RawMessage `json:"content"`
		}
		if e = json.Unmarshal(raw, &m); e != nil {
			return e
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			blocks = append(blocks, map[string]string{"type": "text", "text": text})
			continue
		}
		var content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}
		if e = json.Unmarshal(m.Content, &content); e != nil {
			return e
		}
		for _, block := range content {
			switch block.Type {
			case "text":
				blocks = append(blocks, map[string]string{"type": "text", "text": block.Text})
			case "image":
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": block.MimeType, "data": block.Data}})
			default:
				return errors.New("unsupported Claude user content")
			}
		}
	}
	start["content"] = blocks
	process := exec.CommandContext(ctx, d.options.Node, "--input-type=module", "-e", hostSource)
	process.Dir, process.Env, process.Stderr = d.options.Manager.GetCWD(), d.options.Env, io.Discard
	input, err := process.StdinPipe()
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := process.StdoutPipe()
	if err != nil {
		return err
	}
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return json.NewEncoder(input).Encode(value)
	}
	kill := isolate(process)
	process.Cancel = func() error { _ = write(map[string]string{"type": "cancel"}); return nil }
	process.WaitDelay = 5 * time.Second
	if err = process.Start(); err != nil {
		return fmt.Errorf("start Claude SDK: %w", err)
	}
	defer func() { _ = kill() }()
	if err = write(start); err != nil {
		_ = kill()
		_ = process.Wait()
		return err
	}
	translator := translation{driver: d, ctx: ctx, emit: emit, model: model.ID, tools: map[string]string{}, session: id, at: saved.At}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	var readErr error
	for scanner.Scan() {
		var frame struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Title     string          `json:"title"`
			Choices   []string        `json:"choices"`
			Event     json.RawMessage `json:"event"`
			Message   string          `json:"message"`
			Tool      string          `json:"tool"`
			ToolID    string          `json:"tool_id"`
			Args      map[string]any  `json:"args"`
			CWD       string          `json:"cwd"`
			Questions json.RawMessage `json:"questions"`
		}
		if readErr = json.Unmarshal(scanner.Bytes(), &frame); readErr != nil {
			break
		}
		switch frame.Type {
		case "sdk":
			readErr = translator.event(frame.Event)
		case "context":
			_, readErr = d.options.Manager.AppendCustomEntry(Name+".context", frame.Event)
		case "error":
			readErr = errors.New(frame.Message)
		case "tool":
			decision, reason := "", ""
			if config.BeforeToolCall != nil {
				name := frame.Tool
				switch name {
				case "Bash", "Read", "Write", "Edit", "Grep":
					name = strings.ToLower(name)
				case "Glob":
					name = "find"
				case "MultiEdit", "NotebookEdit":
					name = "edit"
				}
				cwd := frame.CWD
				if !filepath.IsAbs(cwd) {
					cwd = d.options.Manager.GetCWD()
				}
				for _, key := range []string{"file_path", "notebook_path", "path"} {
					if path, ok := frame.Args[key].(string); ok && path != "" {
						if !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/") {
							path = filepath.Join(cwd, path)
						}
						frame.Args[key] = path
					}
				}
				call := &ai.ToolCall{ID: frame.ToolID, Name: name, Arguments: frame.Args}
				result, err := config.BeforeToolCall(ctx, engine.BeforeToolCallContext{ToolCall: call, Args: frame.Args,
					AssistantMessage: &ai.AssistantMessage{Content: []ai.AssistantContentBlock{call}}, Context: &engine.AgentContext{Messages: prompts}})
				if err != nil {
					decision, reason = "deny", err.Error()
				} else if result != nil {
					reason = result.Reason
					if result.Block {
						decision = "deny"
					} else if result.Approved {
						decision = "allow"
					}
				}
			}
			readErr = write(map[string]any{"type": "reply", "id": frame.ID, "value": map[string]string{"decision": decision, "reason": reason}})
		case "questions":
			request, err := nativeQuestions(frame.Questions)
			result := questions.Result{Cancelled: true}
			if err == nil {
				result, err = questions.Ask(ctx, request, d.options.Ask)
			}
			if err != nil {
				result = questions.Result{Cancelled: true}
			}
			readErr = write(map[string]any{"type": "reply", "id": frame.ID, "value": result})
		case "input":
			value, cancelled := "", true
			if d.options.Ask != nil {
				var askErr error
				value, askErr = d.options.Ask(ctx, frame.Title, frame.Choices)
				cancelled = askErr != nil
			}
			if ctx.Err() != nil {
				readErr = ctx.Err()
			} else {
				readErr = write(map[string]any{"type": "reply", "id": frame.ID, "value": value, "cancelled": cancelled})
			}
		default:
			readErr = errors.New("invalid Claude SDK host frame")
		}
		if readErr != nil {
			break
		}
	}
	if readErr != nil || scanner.Err() != nil {
		_ = kill()
	}
	waitErr := process.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	if scanner.Err() != nil {
		return fmt.Errorf("read Claude SDK: %w", scanner.Err())
	}
	if waitErr != nil {
		return fmt.Errorf("claude SDK exited: %w", waitErr)
	}
	if !translator.result {
		return errors.New("claude SDK exited without a result; execution outcome is unknown")
	}
	return nil
}

type translation struct {
	driver             *Driver
	ctx                context.Context
	emit               engine.EventSink
	model, session, at string
	partial            *ai.AssistantMessage
	tools              map[string]string
	last               *ai.AssistantMessage
	results            []*ai.ToolResultMessage
	result             bool
}

func (t *translation) save(at string) error {
	if at != "" {
		t.at = at
	}
	saved := checkpoint{Owner: t.driver.options.Manager.GetSessionID(), Session: t.session, At: t.at}
	_, err := t.driver.options.Manager.AppendCustomEntry(Name, saved)
	if err == nil {
		t.driver.latest = saved
	}
	return err
}
func (t *translation) event(raw json.RawMessage) error {
	var e struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Session string          `json:"session_id"`
		UUID    string          `json:"uuid"`
		Parent  *string         `json:"parent_tool_use_id"`
		Event   json.RawMessage `json:"event"`
		Message json.RawMessage `json:"message"`
		IsError bool            `json:"is_error"`
		Errors  []string        `json:"errors"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if e.Parent != nil {
		return nil
	} // Native subagents retain their own transcripts.
	if e.Session != "" {
		t.session = e.Session
	}
	switch e.Type {
	case "rate_limit_event":
		var event struct {
			Limits subscriptionLimits `json:"rate_limit_info"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return err
		}
		if event.Limits.Status != "allowed" && event.Limits.Status != "allowed_warning" && event.Limits.Status != "rejected" {
			return nil
		}
		event.Limits.ObservedAt = time.Now()
		for key := range event.Limits.UnifiedWindows {
			if limitLabel(key) == "" {
				delete(event.Limits.UnifiedWindows, key)
			}
		}
		_, err := t.driver.options.Manager.AppendCustomEntry(Name+".limits", event.Limits)
		return err
	case "system":
		if e.Subtype == "init" {
			var metadata struct {
				Session        string   `json:"session_id"`
				Model          string   `json:"model"`
				Version        string   `json:"claude_code_version"`
				Auth           string   `json:"apiKeySource"`
				Tools          []string `json:"tools"`
				PermissionMode string   `json:"permissionMode"`
			}
			if err := json.Unmarshal(raw, &metadata); err != nil {
				return err
			}
			if _, err := t.driver.options.Manager.AppendCustomEntry(Name+".init", metadata); err != nil {
				return err
			}
			return t.save("")
		}
	case "stream_event":
		return t.stream(e.Event)
	case "assistant":
		m, err := t.assistant(e.Message)
		if err != nil {
			return err
		}
		if t.partial == nil {
			if err = t.nextTurn(); err != nil {
				return err
			}
			if err = t.emit(t.ctx, engine.MessageStartEvent{Message: m}); err != nil {
				return err
			}
		}
		if err = t.emit(t.ctx, engine.MessageEndEvent{Message: m}); err != nil {
			return err
		}
		t.partial = nil
		for _, b := range m.Content {
			if tool, ok := b.(*ai.ToolCall); ok {
				t.tools[tool.ID] = tool.Name
				if err = t.emit(t.ctx, engine.NewToolExecutionStartEvent(tool)); err != nil {
					return err
				}
			}
		}
		t.last = m
		return t.save(e.UUID)
	case "user":
		var m struct {
			Content []struct {
				Type    string          `json:"type"`
				ID      string          `json:"tool_use_id"`
				Content json.RawMessage `json:"content"`
				IsError bool            `json:"is_error"`
			} `json:"content"`
		}
		if json.Unmarshal(e.Message, &m) != nil {
			return nil
		}
		for _, block := range m.Content {
			if block.Type == "tool_result" {
				content := ai.ToolResultContent{}
				var text string
				if json.Unmarshal(block.Content, &text) == nil {
					content = append(content, &ai.TextContent{Text: text})
				} else {
					var blocks []struct{ Type, Text string }
					if err := json.Unmarshal(block.Content, &blocks); err != nil {
						return err
					}
					for _, b := range blocks {
						if b.Type == "text" {
							content = append(content, &ai.TextContent{Text: b.Text})
						}
					}
				}
				result := &ai.ToolResultMessage{ToolCallID: block.ID, ToolName: t.tools[block.ID], Content: content, IsError: block.IsError, Timestamp: time.Now().UnixMilli()}
				if err := t.emit(t.ctx, engine.ToolExecutionEndEvent{ToolCallID: block.ID, ToolName: result.ToolName, IsError: block.IsError, Result: engine.AgentToolResult{Content: result.Content}}); err != nil {
					return err
				}
				if err := t.emit(t.ctx, engine.MessageStartEvent{Message: result}); err != nil {
					return err
				}
				if err := t.emit(t.ctx, engine.MessageEndEvent{Message: result}); err != nil {
					return err
				}
				t.results = append(t.results, result)
				delete(t.tools, block.ID)
			}
		}
		if e.UUID != "" {
			return t.save(e.UUID)
		}
	case "result":
		t.result = true
		// Native cost is accounting metadata, never an asserted subscription charge.
		if _, err := t.driver.options.Manager.AppendCustomEntry(Name+".result", json.RawMessage(raw)); err != nil {
			return err
		}
		if e.IsError || e.Subtype != "success" {
			return fmt.Errorf("claude %s: %s", e.Subtype, strings.Join(e.Errors, "; "))
		}
		return t.endTurn()
	}
	return nil
}
func (t *translation) endTurn() error {
	if t.last == nil {
		return nil
	}
	err := t.emit(t.ctx, engine.TurnEndEvent{Message: t.last, ToolResults: t.results})
	t.last = nil
	t.results = nil
	return err
}
func (t *translation) nextTurn() error {
	if t.last == nil {
		return nil
	}
	if err := t.endTurn(); err != nil {
		return err
	}
	return t.emit(t.ctx, engine.TurnStartEvent{})
}

func (t *translation) assistant(raw json.RawMessage) (*ai.AssistantMessage, error) {
	var m struct {
		ID, Model string
		Content   []struct {
			Type, Text, Thinking, Signature, ID, Name string
			Input                                     map[string]any
		}
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
			Read   int64 `json:"cache_read_input_tokens"`
			Write  int64 `json:"cache_creation_input_tokens"`
		}
		Stop string `json:"stop_reason"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := &ai.AssistantMessage{API: Name, Provider: Name, Model: m.Model, Timestamp: time.Now().UnixMilli(), Content: ai.AssistantContent{}, StopReason: ai.StopReasonStop}
	if out.Model == "" {
		out.Model = t.model
	}
	out.Usage = ai.Usage{Input: m.Usage.Input, Output: m.Usage.Output, CacheRead: m.Usage.Read, CacheWrite: m.Usage.Write, TotalTokens: m.Usage.Input + m.Usage.Output + m.Usage.Read + m.Usage.Write}
	if m.ID != "" {
		out.ResponseID = &m.ID
	}
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			out.Content = append(out.Content, &ai.TextContent{Text: b.Text})
		case "thinking":
			thinking := &ai.ThinkingContent{Thinking: b.Thinking}
			if b.Signature != "" {
				thinking.ThinkingSignature = &b.Signature
			}
			out.Content = append(out.Content, thinking)
		case "tool_use":
			out.Content = append(out.Content, &ai.ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Input})
			out.StopReason = ai.StopReasonToolUse
		}
	}
	if m.Stop == "max_tokens" {
		out.StopReason = ai.StopReasonLength
	}
	return out, nil
}
func (t *translation) stream(raw json.RawMessage) error {
	var e struct {
		Type    string          `json:"type"`
		Index   int             `json:"index"`
		Message json.RawMessage `json:"message"`
		Block   struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Delta struct{ Type, Text, Thinking string } `json:"delta"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if e.Type == "message_start" {
		if err := t.nextTurn(); err != nil {
			return err
		}
		var err error
		t.partial, err = t.assistant(e.Message)
		if err != nil {
			return err
		}
		return t.emit(t.ctx, engine.MessageStartEvent{Message: t.partial})
	}
	if t.partial == nil {
		return nil
	}
	if e.Type == "content_block_start" {
		switch e.Block.Type {
		case "text":
			t.partial.Content = append(t.partial.Content, &ai.TextContent{})
		case "thinking":
			t.partial.Content = append(t.partial.Content, &ai.ThinkingContent{})
		default:
			t.partial.Content = append(t.partial.Content, &ai.TextContent{})
		}
	}
	if e.Type == "content_block_delta" && e.Index >= 0 && e.Index < len(t.partial.Content) {
		var delta ai.AssistantMessageEvent
		switch block := t.partial.Content[e.Index].(type) {
		case *ai.TextContent:
			if e.Delta.Type != "text_delta" {
				return nil
			}
			block.Text += e.Delta.Text
			delta = ai.TextDeltaEvent{ContentIndex: e.Index, Delta: e.Delta.Text, Partial: t.partial}
		case *ai.ThinkingContent:
			block.Thinking += e.Delta.Thinking
			delta = ai.ThinkingDeltaEvent{ContentIndex: e.Index, Delta: e.Delta.Thinking, Partial: t.partial}
		}
		if delta != nil {
			return t.emit(t.ctx, engine.MessageUpdateEvent{Message: t.partial, AssistantMessageEvent: delta})
		}
	}
	return nil
}

func nativeQuestions(raw json.RawMessage) (questions.Request, error) {
	var native []struct {
		Question    string             `json:"question"`
		Header      string             `json:"header"`
		Options     []questions.Option `json:"options"`
		MultiSelect bool               `json:"multiSelect"`
	}
	if err := json.Unmarshal(raw, &native); err != nil {
		return questions.Request{}, err
	}
	request := questions.Request{Questions: make([]questions.Question, len(native))}
	for i, q := range native {
		request.Questions[i] = questions.Question{ID: fmt.Sprint(i + 1), Question: q.Question, Header: q.Header, Options: q.Options, MultiSelect: q.MultiSelect}
	}
	return request, request.Validate()
}

func checkSandbox(mode sandbox.Mode) error {
	if mode != "" && mode != sandbox.ModeDangerFullAccess {
		return errors.New("claude sessions cannot enforce Orb filesystem containment; choose a standard Orb provider or explicitly configure danger-full-access")
	}
	return nil
}
