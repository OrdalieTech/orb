// Package claudesessions adapts native Claude Agent SDK sessions to Orb events.
// Its host runs the official SDK and an unmodified Claude executable; Orb never
// reads, copies, refreshes or proxies Claude credentials.
package claudesessions

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/plugins/questions"
	"github.com/OrdalieTech/orb/sandbox"
)

const Name = "claude-sessions"
const SDKVersion = "0.3.280"

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

// checkpoint is the native resume point recorded on an Orb branch: At is the
// native chain entry that branch's latest Orb entry corresponds to.
type checkpoint struct {
	Owner   string `json:"owner"`
	Session string `json:"session"`
	At      string `json:"at,omitempty"`
}
type Driver struct {
	options Options
	// ponytail: approvals "for this session" live as long as this Orb runtime,
	// like Orb's own session approvals.
	approved []json.RawMessage
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
	saved, err := d.checkpoint()
	if err != nil {
		return nil, err
	}
	if saved.Session == "" && onBranch(options.Manager, func(entry *session.SessionEntry) bool { return entry.Type == "message" }) != nil {
		return nil, errors.New("this transcript has no native Claude session; start a new Claude conversation")
	}
	return d, nil
}

// onBranch returns the entry nearest the leaf that match accepts.
func onBranch(manager extensions.ReadonlySessionManager, match func(*session.SessionEntry) bool) *session.SessionEntry {
	for entry := manager.GetLeafEntry(); entry != nil; {
		if match(entry) {
			return entry
		}
		if entry.ParentID == nil {
			return nil
		}
		entry = manager.GetEntry(*entry.ParentID)
	}
	return nil
}

// tail is the newest point this Orb session recorded in a native session.
func (d *Driver) tail(native string) string {
	at := ""
	for _, entry := range d.options.Manager.GetEntries() {
		var point checkpoint
		if entry.CustomType == Name && json.Unmarshal(entry.Data, &point) == nil && point.Session == native {
			at = point.At
		}
	}
	return at
}

func (d *Driver) checkpoint() (checkpoint, error) {
	var saved checkpoint
	entry := onBranch(d.options.Manager, func(entry *session.SessionEntry) bool { return entry.CustomType == Name })
	if entry == nil {
		return saved, nil
	}
	if err := json.Unmarshal(entry.Data, &saved); err != nil {
		return saved, fmt.Errorf("invalid Claude checkpoint: %w", err)
	}
	return saved, nil
}

// Loop owns the complete native turn. Orb queues are drained only between native
// turns, so no tool is executed twice by an outer Orb agent loop.
func (d *Driver) Loop(ctx context.Context, prompts engine.AgentMessages, _ engine.AgentContext, config engine.AgentLoopConfig, emit engine.EventSink) error {
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
	for len(prompts) > 0 && ctx.Err() == nil {
		if err := emit(ctx, engine.TurnStartEvent{}); err != nil {
			return err
		}
		for _, prompt := range prompts {
			if err := emit(ctx, engine.MessageStartEvent{Message: prompt}); err != nil {
				return err
			}
			if err := emit(ctx, engine.MessageEndEvent{Message: prompt}); err != nil {
				return err
			}
		}
		// An interrupted turn settles its own aborted reply; Loop then ends normally.
		if err := d.turn(ctx, prompts, config, emit); err != nil && ctx.Err() == nil {
			return err
		}
		var err error
		prompts = nil
		if ctx.Err() == nil && config.GetSteeringMessages != nil {
			prompts, err = config.GetSteeringMessages(ctx)
		}
		if err == nil && ctx.Err() == nil && len(prompts) == 0 && config.GetFollowUpMessages != nil {
			prompts, err = config.GetFollowUpMessages(ctx)
		}
		if err != nil {
			return err
		}
	}
	return emit(context.WithoutCancel(ctx), engine.AgentEndEvent{Messages: generated})
}

func (d *Driver) turn(ctx context.Context, prompts engine.AgentMessages, config engine.AgentLoopConfig, emit engine.EventSink) error {
	model := config.Model
	saved, err := d.checkpoint()
	if err != nil {
		return err
	}
	content, err := nativeContent(ctx, prompts, config.ConvertToLLM)
	if err != nil {
		return err
	}
	id := d.options.Manager.GetSessionID()
	start := map[string]any{"type": "start", "sdk": d.options.SDK, "claude": d.options.Claude, "cwd": d.options.Manager.GetCWD(), "model": model.ID, "permissionMode": nativeMode(d.options.Manager), "content": content, "sessionUpdates": d.approved, "uuid": newUUID()}
	// Continuing from the newest native point resumes in place. Any other point
	// (a /tree move, a withdrawn prompt, a copied session) forks a native session
	// cut exactly there, which also reaches history before a native compaction.
	if saved.Session != "" {
		start["resume"], start["fork"] = saved.Session, saved.Owner != id || saved.At != d.tail(saved.Session)
		start["at"] = saved.At
	}
	var info modelInfo
	_ = json.Unmarshal(model.Compat, &info)
	level := ai.ModelThinkingOff
	if config.Reasoning != nil {
		level = ai.ClampThinkingLevel(model, ai.ModelThinkingLevel(*config.Reasoning))
	}
	if info.Adaptive {
		start["thinking"] = "disabled"
		if level != ai.ModelThinkingOff {
			start["thinking"] = "adaptive"
		}
	}
	if slices.Contains(info.Levels, string(level)) {
		start["effort"] = level
	}

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
	translator := translation{driver: d, ctx: ctx, emit: emit, model: model.ID, tools: map[string]string{}, session: saved.Session, at: saved.At, prompt: start["uuid"].(string)}
	if start["fork"] == true {
		translator.session = ""
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	var readErr error
	for scanner.Scan() {
		var frame struct {
			Type        string          `json:"type"`
			ID          string          `json:"id"`
			Title       string          `json:"title"`
			Choices     []string        `json:"choices"`
			Event       json.RawMessage `json:"event"`
			Message     string          `json:"message"`
			Tool        string          `json:"tool"`
			ToolID      string          `json:"tool_id"`
			Args        map[string]any  `json:"args"`
			CWD         string          `json:"cwd"`
			Questions   json.RawMessage `json:"questions"`
			Elicitation json.RawMessage `json:"elicitation"`
		}
		if readErr = json.Unmarshal(scanner.Bytes(), &frame); readErr != nil {
			break
		}
		switch frame.Type {
		case "sdk":
			readErr = translator.event(frame.Event)
		case "context":
			_, readErr = d.options.Manager.AppendCustomEntry(Name+".context", frame.Event)
		case "session":
			var updates []json.RawMessage
			if readErr = json.Unmarshal(frame.Event, &updates); readErr == nil {
				d.approved = append(d.approved, updates...)
			}
		case "error":
			readErr = errors.New(frame.Message)
		case "tool":
			if readErr = translator.startEarly(frame.ToolID); readErr != nil {
				break
			}
			decision, reason := d.approve(ctx, frame.Tool, frame.ToolID, frame.CWD, frame.Args, prompts, config.BeforeToolCall)
			readErr = write(map[string]any{"type": "reply", "id": frame.ID, "value": map[string]string{"decision": decision, "reason": reason}})
		case "elicitation":
			response := d.elicit(ctx, frame.Elicitation)
			readErr = write(map[string]any{"type": "reply", "id": frame.ID, "value": response})
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
		return translator.abort()
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
	return translator.failure
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// nativeContent converts Orb prompts, including extension messages, into the
// user content blocks of one native turn. The native session owns its system prompt.
func nativeContent(ctx context.Context, prompts engine.AgentMessages, convert engine.ConvertToLLMFunc) ([]any, error) {
	var source any = prompts
	if convert != nil {
		converted, err := convert(ctx, prompts)
		if err != nil {
			return nil, err
		}
		source = converted
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err = json.Unmarshal(raw, &messages); err != nil {
		return nil, err
	}
	var blocks []any
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			blocks = append(blocks, map[string]string{"type": "text", "text": text})
			continue
		}
		var content []struct{ Type, Text, Data, MimeType string }
		if err = json.Unmarshal(m.Content, &content); err != nil {
			return nil, err
		}
		for _, block := range content {
			switch block.Type {
			case "text":
				blocks = append(blocks, map[string]string{"type": "text", "text": block.Text})
			case "image":
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": block.MimeType, "data": block.Data}})
			default:
				return nil, errors.New("unsupported Claude user content")
			}
		}
	}
	if len(blocks) == 0 {
		return nil, errors.New("claude sessions accept user content only")
	}
	return blocks, nil
}

// approve asks Orb's tool policy about a native call, using Orb's tool names.
func (d *Driver) approve(ctx context.Context, tool, toolID, cwd string, args map[string]any, prompts engine.AgentMessages, before engine.BeforeToolCallFunc) (decision, reason string) {
	if before == nil {
		return "", ""
	}
	name := tool
	switch name {
	case "Bash", "Read", "Write", "Edit", "Grep":
		name = strings.ToLower(name)
	case "Glob":
		name = "find"
	case "MultiEdit", "NotebookEdit":
		name = "edit"
	}
	if !filepath.IsAbs(cwd) {
		cwd = d.options.Manager.GetCWD()
	}
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if path, ok := args[key].(string); ok && path != "" {
			if !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/") {
				path = filepath.Join(cwd, path)
			}
			args[key] = path
		}
	}
	call := &ai.ToolCall{ID: toolID, Name: name, Arguments: args}
	result, err := before(ctx, engine.BeforeToolCallContext{ToolCall: call, Args: args,
		AssistantMessage: &ai.AssistantMessage{Content: []ai.AssistantContentBlock{call}}, Context: &engine.AgentContext{Messages: prompts}})
	switch {
	case err != nil:
		return "deny", err.Error()
	case result == nil:
		return "", ""
	case result.Block:
		return "deny", result.Reason
	case result.Approved:
		return "allow", result.Reason
	}
	return "", result.Reason
}

// translation projects one native turn onto Orb events. The SDK emits one
// assistant record per content block; the raw stream of the same API message
// is authoritative, so each API message becomes exactly one Orb message.
type translation struct {
	driver             *Driver
	ctx                context.Context
	emit               engine.EventSink
	model, session, at string
	// prompt is this turn's native prompt UUID, recorded once Claude answers it,
	// so an interrupted turn still resumes with the prompt Orb shows.
	prompt          string
	partial         *ai.AssistantMessage // the API message being streamed
	partialID       string
	streamed        string      // the last streamed message.id
	blocks          map[int]int // API content index -> partial content index
	args            map[int]*strings.Builder
	tools           map[string]string // started, unanswered tool calls
	seen            map[string]bool   // every started tool call
	last            *ai.AssistantMessage
	results         []*ai.ToolResultMessage
	result, errored bool
	failure         error
	progress        map[string]int
}

type nativeUsage struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
	Read   int64 `json:"cache_read_input_tokens"`
	Write  int64 `json:"cache_creation_input_tokens"`
}

// apply overlays reported counters, which only grow within one API message.
func (u nativeUsage) apply(usage *ai.Usage) {
	usage.Input, usage.Output = max(usage.Input, u.Input), max(usage.Output, u.Output)
	usage.CacheRead, usage.CacheWrite = max(usage.CacheRead, u.Read), max(usage.CacheWrite, u.Write)
	usage.TotalTokens = usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

type nativeBlock struct {
	Type, Text, Thinking, Signature, ID, Name string
	Input                                     map[string]any
}

type nativeMessage struct {
	ID, Model string
	Content   []nativeBlock
	Usage     nativeUsage
	Stop      string `json:"stop_reason"`
}

func (t *translation) save(at string) error {
	if at != "" {
		t.at = at
	}
	_, err := t.driver.options.Manager.AppendCustomEntry(Name, checkpoint{Owner: t.driver.options.Manager.GetSessionID(), Session: t.session, At: t.at})
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
		Error   string          `json:"error"`
		IsError bool            `json:"is_error"`
		Errors  []string        `json:"errors"`
		Result  string          `json:"result"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if e.Parent != nil {
		return nil
	} // Native subagents retain their own transcripts.
	if e.Session != "" && e.Subtype != "init" {
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
		var activity struct {
			PermissionMode string `json:"permissionMode"`
			ToolID         string `json:"tool_use_id"`
			Usage          struct {
				Duration float64 `json:"duration_ms"`
			}
			Status, Description, Summary, Content string
			Ambient                               bool
			SkipTranscript                        bool `json:"skip_transcript"`
			Attempt                               int
			MaxRetries                            int `json:"max_retries"`
			RetryDelay                            int `json:"retry_delay_ms"`
			Compact                               struct {
				Pre  int64  `json:"pre_tokens"`
				Post *int64 `json:"post_tokens"`
			} `json:"compact_metadata"`
		}
		if err := json.Unmarshal(raw, &activity); err != nil {
			return err
		}
		if activity.PermissionMode != "" {
			mode := "default"
			if activity.PermissionMode == "plan" {
				mode = "plan"
			}
			if mode != nativeMode(t.driver.options.Manager) {
				if _, err := t.driver.options.Manager.AppendCustomEntry(Name+".mode", mode); err != nil {
					return err
				}
			}
		}
		switch e.Subtype {
		case "compact_boundary":
			if _, err := t.driver.options.Manager.AppendCustomEntry(Name+".context", nil); err != nil {
				return err
			}
			text := fmt.Sprintf("Claude compacted context from %d tokens", activity.Compact.Pre)
			if activity.Compact.Post != nil {
				text += fmt.Sprintf(" to %d", *activity.Compact.Post)
			}
			return t.notice(text)
		case "informational", "local_command_output":
			return t.notice(activity.Content)
		case "api_retry":
			return t.notice(fmt.Sprintf("Claude retry %d/%d in %.1fs", activity.Attempt, activity.MaxRetries, float64(activity.RetryDelay)/1000))
		case "status":
			if activity.Status == "compacting" {
				return t.notice("Claude is compacting context…")
			}
		case "task_progress":
			text := activity.Summary
			if text == "" {
				text = activity.Description
			}
			return t.toolProgress(activity.ToolID, activity.Usage.Duration/1000, text)
		case "task_started", "task_notification":
			if activity.Ambient || activity.SkipTranscript {
				return nil
			}
			if e.Subtype == "task_started" {
				return t.notice("Claude task started: " + activity.Description)
			}
			return t.notice("Claude task " + activity.Status + ": " + activity.Summary)
		case "init":
			if t.session != e.Session {
				t.at = "" // A new or forked native session has its own UUIDs.
			}
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
			t.session = e.Session
			return t.save("")
		}
	case "tool_progress":
		var progress struct {
			ID      string  `json:"tool_use_id"`
			Elapsed float64 `json:"elapsed_time_seconds"`
		}
		if err := json.Unmarshal(raw, &progress); err != nil {
			return err
		}
		return t.toolProgress(progress.ID, progress.Elapsed, fmt.Sprintf("Running · %.0fs", progress.Elapsed))
	case "stream_event":
		return t.stream(e.Event)
	case "assistant":
		var m nativeMessage
		if err := json.Unmarshal(e.Message, &m); err != nil {
			return err
		}
		if err := t.save(e.UUID); err != nil {
			return err
		}
		if m.ID != "" && m.ID == t.partialID {
			return nil
		}
		// A fast tool can answer before its message ends; a call streamed after
		// that answer arrives only here and starts as a follow-up message.
		if m.ID != "" && m.ID == t.streamed {
			m.Content = slices.DeleteFunc(m.Content, func(block nativeBlock) bool { return block.Type != "tool_use" || t.seen[block.ID] })
			if len(m.Content) == 0 {
				return nil
			}
		}
		// Synthetic records (API errors, notices) arrive without a stream.
		if err := t.begin(t.message(m), ""); err != nil {
			return err
		}
		if e.Error != "" {
			var text []string
			for _, block := range t.partial.Content {
				if content, ok := block.(*ai.TextContent); ok {
					text = append(text, content.Text)
				}
			}
			reason := strings.Join(text, "\n")
			t.partial.Content, t.partial.StopReason, t.partial.ErrorMessage, t.errored = ai.AssistantContent{}, ai.StopReasonError, &reason, true
		}
		return t.finish()
	case "user":
		if err := t.finish(); err != nil {
			return err
		}
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
			if block.Type != "tool_result" {
				continue
			}
			content, err := toolResultContent(block.Content)
			if err != nil {
				return err
			}
			if err = t.toolResult(block.ID, content, block.IsError); err != nil {
				return err
			}
		}
		if e.UUID != "" {
			return t.save(e.UUID)
		}
	case "result":
		t.result = true
		if err := t.finish(); err != nil {
			return err
		}
		// A reply already rendered as an error is not reported twice.
		if (e.IsError || e.Subtype != "success") && !t.errored {
			reason := strings.Join(e.Errors, "; ")
			if reason == "" {
				reason = e.Result
			}
			t.failure = fmt.Errorf("claude %s: %s", e.Subtype, reason)
		}
		return t.endTurn()
	}
	return nil
}

func toolResultContent(raw json.RawMessage) (ai.ToolResultContent, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return ai.ToolResultContent{&ai.TextContent{Text: text}}, nil
	}
	var blocks []struct {
		Type, Text string
		Source     struct {
			MediaType string `json:"media_type"`
			Data      string
		}
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	content := ai.ToolResultContent{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			content = append(content, &ai.TextContent{Text: b.Text})
		case "image":
			content = append(content, &ai.ImageContent{Data: b.Source.Data, MimeType: b.Source.MediaType})
		}
	}
	return content, nil
}

func (t *translation) toolResult(id string, content ai.ToolResultContent, isError bool) error {
	result := &ai.ToolResultMessage{ToolCallID: id, ToolName: t.tools[id], Content: content, IsError: isError, Timestamp: time.Now().UnixMilli()}
	for _, event := range []engine.AgentEvent{
		engine.ToolExecutionEndEvent{ToolCallID: id, ToolName: result.ToolName, IsError: isError, Result: engine.AgentToolResult{Content: content}},
		engine.MessageStartEvent{Message: result},
		engine.MessageEndEvent{Message: result},
	} {
		if err := t.emit(t.ctx, event); err != nil {
			return err
		}
	}
	t.results = append(t.results, result)
	delete(t.tools, id)
	delete(t.progress, id)
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

// begin closes any open message and turn, then starts streaming m.
func (t *translation) begin(m *ai.AssistantMessage, id string) error {
	if err := t.finish(); err != nil {
		return err
	}
	if t.prompt != "" {
		if err := t.save(t.prompt); err != nil {
			return err
		}
		t.prompt = ""
	}
	if t.last != nil {
		if err := t.endTurn(); err != nil {
			return err
		}
		if err := t.emit(t.ctx, engine.TurnStartEvent{}); err != nil {
			return err
		}
	}
	t.partial, t.partialID, t.blocks, t.args = m, id, map[int]int{}, map[int]*strings.Builder{}
	return t.emit(t.ctx, engine.MessageStartEvent{Message: m})
}

// finish ends the open message and starts its tool calls, as Orb's own loop does.
func (t *translation) finish() error {
	m := t.partial
	if m == nil {
		return nil
	}
	if t.partialID != "" {
		t.streamed = t.partialID
	}
	// Calls still streaming are dropped here and arrive later as records.
	for index := range t.args {
		unfinished := m.Content[t.blocks[index]]
		m.Content = slices.DeleteFunc(m.Content, func(block ai.AssistantContentBlock) bool { return block == unfinished })
	}
	t.partial, t.partialID, t.args = nil, "", nil
	var calls []*ai.ToolCall
	for _, block := range m.Content {
		if call, ok := block.(*ai.ToolCall); ok {
			calls = append(calls, call)
		}
	}
	if len(calls) > 0 && m.StopReason == ai.StopReasonStop {
		m.StopReason = ai.StopReasonToolUse
	}
	if err := t.emit(t.ctx, engine.MessageEndEvent{Message: m}); err != nil {
		return err
	}
	t.last = m
	if m.StopReason == ai.StopReasonAborted {
		return nil
	}
	for _, call := range calls {
		if err := t.start(call); err != nil {
			return err
		}
	}
	return nil
}

func (t *translation) start(call *ai.ToolCall) error {
	if t.seen[call.ID] {
		return nil
	}
	if t.seen == nil {
		t.seen = map[string]bool{}
	}
	t.tools[call.ID], t.seen[call.ID] = call.Name, true
	return t.emit(t.ctx, engine.NewToolExecutionStartEvent(call))
}

// startEarly shows a completed call before its message ends: the native loop
// asks for approval as soon as the call's block is complete.
func (t *translation) startEarly(id string) error {
	if t.partial == nil {
		return nil
	}
	for index, i := range t.blocks {
		if call, ok := t.partial.Content[i].(*ai.ToolCall); ok && call.ID == id && t.args[index] == nil {
			return t.start(call)
		}
	}
	return nil
}

// abort settles an interrupted turn the way Orb's loop does: open tools and the
// reply end as aborted, and nothing is left streaming.
func (t *translation) abort() error {
	cause := t.ctx.Err()
	t.ctx = context.WithoutCancel(t.ctx)
	pending := slices.Sorted(maps.Keys(t.tools))
	for _, id := range pending {
		if err := t.toolResult(id, ai.ToolResultContent{&ai.TextContent{Text: "Operation aborted"}}, true); err != nil {
			return err
		}
	}
	if t.partial == nil && t.result {
		return cause
	}
	if t.partial == nil {
		t.prompt = "" // Nothing was answered, so the native prompt may not exist.
		if err := t.begin(t.message(nativeMessage{}), ""); err != nil {
			return err
		}
	}
	reason := "Request was aborted"
	t.partial.StopReason, t.partial.ErrorMessage = ai.StopReasonAborted, &reason
	if err := t.finish(); err != nil {
		return err
	}
	if err := t.endTurn(); err != nil {
		return err
	}
	return cause
}

func (t *translation) message(m nativeMessage) *ai.AssistantMessage {
	// Model stays the selected alias so resume keeps it; the resolved name is the response model.
	out := &ai.AssistantMessage{API: Name, Provider: Name, Model: t.model, Timestamp: time.Now().UnixMilli(), Content: ai.AssistantContent{}, StopReason: ai.StopReasonStop}
	if m.Model != "" && m.Model != t.model {
		out.ResponseModel = &m.Model
	}
	m.Usage.apply(&out.Usage)
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
		}
	}
	if m.Stop == "max_tokens" {
		out.StopReason = ai.StopReasonLength
	}
	return out
}

func (t *translation) stream(raw json.RawMessage) error {
	var e struct {
		Type    string        `json:"type"`
		Index   int           `json:"index"`
		Message nativeMessage `json:"message"`
		Block   struct {
			Type, ID, Name string
		} `json:"content_block"`
		Delta struct {
			Type, Text, Thinking, Signature string
			JSON                            string `json:"partial_json"`
			Stop                            string `json:"stop_reason"`
		} `json:"delta"`
		Usage nativeUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if e.Type == "message_start" {
		return t.begin(t.message(e.Message), e.Message.ID)
	}
	if t.partial == nil {
		return nil
	}
	switch e.Type {
	case "content_block_start":
		var block ai.AssistantContentBlock
		switch e.Block.Type {
		case "text":
			block = &ai.TextContent{}
		case "thinking":
			block = &ai.ThinkingContent{}
		case "tool_use":
			block = &ai.ToolCall{ID: e.Block.ID, Name: e.Block.Name, Arguments: map[string]any{}}
			t.args[e.Index] = &strings.Builder{}
		default:
			return nil
		}
		t.blocks[e.Index] = len(t.partial.Content)
		t.partial.Content = append(t.partial.Content, block)
	case "content_block_delta":
		i, ok := t.blocks[e.Index]
		if !ok {
			return nil
		}
		var delta ai.AssistantMessageEvent
		switch block := t.partial.Content[i].(type) {
		case *ai.TextContent:
			if e.Delta.Type != "text_delta" {
				return nil
			}
			block.Text += e.Delta.Text
			delta = ai.TextDeltaEvent{ContentIndex: i, Delta: e.Delta.Text, Partial: t.partial}
		case *ai.ThinkingContent:
			switch e.Delta.Type {
			case "signature_delta":
				signature := e.Delta.Signature
				block.ThinkingSignature = &signature
				return nil
			case "thinking_delta":
				block.Thinking += e.Delta.Thinking
				delta = ai.ThinkingDeltaEvent{ContentIndex: i, Delta: e.Delta.Thinking, Partial: t.partial}
			default:
				return nil
			}
		case *ai.ToolCall:
			t.args[e.Index].WriteString(e.Delta.JSON)
			return nil
		}
		return t.emit(t.ctx, engine.MessageUpdateEvent{Message: t.partial, AssistantMessageEvent: delta})
	case "content_block_stop":
		if args := t.args[e.Index]; args != nil {
			delete(t.args, e.Index)
			if args.Len() > 0 {
				call := t.partial.Content[t.blocks[e.Index]].(*ai.ToolCall)
				if err := json.Unmarshal([]byte(args.String()), &call.Arguments); err != nil {
					return fmt.Errorf("claude tool arguments: %w", err)
				}
			}
		}
	case "message_delta":
		e.Usage.apply(&t.partial.Usage)
		if e.Delta.Stop == "max_tokens" {
			t.partial.StopReason = ai.StopReasonLength
		}
	case "message_stop":
		return t.finish()
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

func (t *translation) toolProgress(id string, elapsed float64, text string) error {
	name, ok := t.tools[id]
	if !ok {
		return nil
	}
	if t.progress == nil {
		t.progress = map[string]int{}
	}
	bucket := int(elapsed / 5)
	if bucket <= t.progress[id] {
		return nil
	}
	t.progress[id] = bucket
	return t.emit(t.ctx, engine.ToolExecutionUpdateEvent{ToolCallID: id, ToolName: name, PartialResult: engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: fmt.Sprintf("%.512s", strings.Join(strings.Fields(text), " "))}}}})
}

func (t *translation) notice(text string) error {
	message := &harness.CustomMessage{Role: "custom", CustomType: Name + ".activity", Content: fmt.Sprintf("%.512s", strings.Join(strings.Fields(text), " ")), Display: true, Timestamp: time.Now().UnixMilli()}
	if err := t.emit(t.ctx, engine.MessageStartEvent{Message: message}); err != nil {
		return err
	}
	return t.emit(t.ctx, engine.MessageEndEvent{Message: message})
}

// Elicitation answers stay on the request pipe, never in Orb's transcript metadata.
func (d *Driver) elicit(ctx context.Context, raw json.RawMessage) map[string]any {
	cancel := map[string]any{"action": "cancel"}
	var request struct {
		ServerName, Message, Mode, URL string
		Schema                         json.RawMessage `json:"requestedSchema"`
	}
	if len(raw) > 64<<10 || json.Unmarshal(raw, &request) != nil {
		return cancel
	}
	if request.Mode == "url" {
		link, err := url.Parse(request.URL)
		if err != nil || (link.Scheme != "https" && link.Scheme != "http") || link.Host == "" || link.User != nil {
			return cancel
		}
		answer, err := questions.Ask(ctx, questions.Request{Questions: []questions.Question{{ID: "url", Header: fmt.Sprintf("%.128s", request.ServerName), Question: fmt.Sprintf("%.1500s\n\nOpen this link in your browser, complete the request, then continue:\n%s", request.Message, request.URL), Options: []questions.Option{{Label: "Continue"}, {Label: "Decline"}}}}}, d.options.Ask)
		if err != nil || answer.Cancelled {
			return cancel
		}
		if slices.Equal(answer.Answers[0].Selected, []string{"Continue"}) {
			return map[string]any{"action": "accept"}
		}
		return map[string]any{"action": "decline"}
	}
	if request.Mode != "" && request.Mode != "form" {
		return cancel
	}
	var schema struct {
		Type       string
		Properties map[string]json.RawMessage
		Required   []string
	}
	if json.Unmarshal(request.Schema, &schema) != nil || schema.Type != "object" || len(schema.Properties) > 32 {
		return cancel
	}
	if len(schema.Properties) == 0 {
		answer, err := questions.Ask(ctx, questions.Request{Questions: []questions.Question{{ID: "confirm", Header: fmt.Sprintf("%.128s", request.ServerName), Question: fmt.Sprintf("%.4000s", request.Message), Options: []questions.Option{{Label: "Continue"}, {Label: "Decline"}}}}}, d.options.Ask)
		if err != nil || answer.Cancelled {
			return cancel
		}
		if slices.Equal(answer.Answers[0].Selected, []string{"Continue"}) {
			return map[string]any{"action": "accept", "content": map[string]any{}}
		}
		return map[string]any{"action": "decline"}
	}
	keys := make([]string, 0, len(schema.Properties))
	for key := range schema.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	content := map[string]any{}
	for _, key := range keys {
		var field struct {
			Type, Title, Description string
			Enum                     []string
			Items                    struct{ Enum []string }
		}
		if json.Unmarshal(schema.Properties[key], &field) != nil {
			return cancel
		}
		label := field.Title
		if label == "" {
			label = key
		}
		q := questions.Question{ID: "field", Header: fmt.Sprintf("%.128s", request.ServerName), Question: fmt.Sprintf("%.800s\n\n%.256s\n%.1500s", request.Message, label, field.Description)}
		enum := field.Enum
		if field.Type == "boolean" {
			enum = []string{"true", "false"}
		}
		if field.Type == "array" && len(field.Items.Enum) > 0 && len(field.Items.Enum) <= 7 {
			enum = field.Items.Enum
			q.MultiSelect = true
		}
		if len(enum) <= 7 {
			for _, option := range enum {
				q.Options = append(q.Options, questions.Option{Label: option})
			}
		}
		skip := "Skip this field"
		for slices.Contains(enum, skip) {
			skip += " ·"
		}
		optional := !slices.Contains(schema.Required, key)
		if optional {
			q.Options = append(q.Options, questions.Option{Label: skip})
		}
		if field.Type == "array" || field.Type == "object" {
			q.Question += "\nFor a custom answer, enter JSON."
		}
		base := q.Question
		for {
			if ctx.Err() != nil {
				return cancel
			}
			answer, err := questions.Ask(ctx, questions.Request{Questions: []questions.Question{q}}, d.options.Ask)
			if err != nil || answer.Cancelled {
				return cancel
			}
			a := answer.Answers[0]
			if optional && slices.Equal(a.Selected, []string{skip}) {
				break
			}
			var value any = a.Custom
			if q.MultiSelect && a.Custom == "" {
				value = a.Selected
			} else if len(a.Selected) == 1 {
				value = a.Selected[0]
			} else if field.Type == "array" || field.Type == "object" {
				_ = json.Unmarshal([]byte(a.Custom), &value)
			}
			validated, err := jsonschema.Validate(jsonschema.Schema(schema.Properties[key]), value)
			if err != nil {
				q.Question = base + "\n\nInvalid answer: " + fmt.Sprintf("%.256s", err.Error())
				continue
			}
			content[key] = validated
			break
		}
	}
	validated, err := jsonschema.Validate(jsonschema.Schema(request.Schema), content)
	if err != nil {
		return cancel
	}
	return map[string]any{"action": "accept", "content": validated}
}
