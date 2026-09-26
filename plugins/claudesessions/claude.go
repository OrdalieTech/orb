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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/platforms/native/sandbox"
	"github.com/OrdalieTech/orb/plugins/questions"
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
	// Headless approves native asks that no UI can answer, as Orb runs its own tools.
	Headless bool
	// Context is Orb's instructions that Claude does not load itself.
	Context string
	Ask     func(context.Context, string, []string) (string, error)
	// Account resolves the selected Claude account's directory through Orb's
	// auth pipeline; empty runs with the user's own Claude Code login.
	Account func(context.Context) string
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

type Driver struct {
	options Options
	mu      sync.Mutex
	host    *host
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
	return &Driver{options: options}, nil
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

// host is one live native session. It serves every turn while the start
// configuration stays the same and the conversation still ends where its last
// turn did; anything else (another model's turn, a /tree move) starts a host
// that resumes from the transcript Orb rebuilds.
type host struct {
	key  string
	last string
	// projects is where Claude keeps this conversation's transcript; mirrored
	// holds the records Orb already has.
	projects string
	mirrored map[string]bool
	input    io.WriteCloser
	mu       sync.Mutex
	frames   chan hostFrame
	exited   chan struct{}
	kill     func() error
	// tasks outlives a turn: a background task may finish while a later one runs.
	tasks map[string]*nativeTask
}

// nativeTask is a native task Orb may announce; shown once it is a subagent or in the background.
type nativeTask struct {
	description string
	shown       bool
}

type hostFrame struct {
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
	Ruled       bool            `json:"ruled"`
	Questions   json.RawMessage `json:"questions"`
	Elicitation json.RawMessage `json:"elicitation"`
	// Session is the native session a settled turn wrote to.
	Session string `json:"session"`
}

func (h *host) write(value any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return json.NewEncoder(h.input).Encode(value)
}

// alive reports whether the host process still runs; an idle host exits on its own.
func (h *host) alive() bool {
	select {
	case <-h.exited:
		return false
	default:
		return true
	}
}

// close lets the host end its native session, and kills it if it lingers.
func (h *host) close() {
	_ = h.input.Close()
	time.AfterFunc(5*time.Second, func() { _ = h.kill() })
}

func (d *Driver) spawn(start map[string]any, env []string) (*host, error) {
	process := exec.Command(d.options.Node, "--input-type=module", "-e", hostSource)
	process.Dir, process.Env, process.Stderr = d.options.Manager.GetCWD(), env, io.Discard
	input, err := process.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := process.StdoutPipe()
	if err != nil {
		return nil, err
	}
	h := &host{input: input, frames: make(chan hostFrame, 64), exited: make(chan struct{}), kill: isolate(process), tasks: map[string]*nativeTask{}}
	if err = process.Start(); err != nil {
		return nil, fmt.Errorf("start Claude SDK: %w", err)
	}
	go func() {
		defer close(h.frames)
		defer func() { _ = h.kill(); _ = process.Wait(); close(h.exited) }()
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			var frame hostFrame
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				frame = hostFrame{Type: "error", Message: "invalid Claude SDK host frame"}
			}
			h.frames <- frame
		}
		if scanner.Err() != nil {
			h.frames <- hostFrame{Type: "error", Message: "read Claude SDK: " + scanner.Err().Error()}
		}
	}()
	if err = h.write(start); err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}

// Close ends the live native session, if any.
func (d *Driver) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.host != nil {
		d.host.close()
		d.host = nil
	}
}

func (d *Driver) turn(ctx context.Context, prompts engine.AgentMessages, config engine.AgentLoopConfig, emit engine.EventSink) error {
	model := config.Model
	content, err := nativeContent(ctx, prompts, config.ConvertToLLM)
	if err != nil {
		return err
	}
	start := map[string]any{"type": "start", "sdk": d.options.SDK, "claude": d.options.Claude, "cwd": d.options.Manager.GetCWD(), "model": model.ID, "permissionMode": nativeMode(d.options.Manager), "append": d.options.Context}
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
	env := d.options.Env
	if d.options.Account != nil {
		if dir := d.options.Account(ctx); dir != "" {
			if err := prepareAccount(dir, env); err != nil {
				return err
			}
			env = withConfigDir(env, dir)
			// An account change restarts the host, which resumes the same conversation.
			start["account"] = dir
		}
	}
	key, _ := json.Marshal(start)
	last := lastMessage(d.options.Manager, len(prompts))
	// acquire reuses the live host, or starts one on the transcript Orb rebuilds.
	acquire := func() (h *host, reused bool, err error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		h = d.host
		if h != nil && (h.key != string(key) || h.last != last || !h.alive()) {
			h.close()
			h = nil
		}
		if h != nil {
			return h, true, nil
		}
		configDir, _ := baseConfig(env)
		projects := projectDir(configDir, d.options.Manager.GetCWD())
		mirrored := map[string]bool{}
		if records := rebuild(d.options.Manager, len(prompts)); len(records) > 0 {
			start["resume"] = newUUID()
			if err = writeTranscript(records, start["resume"].(string), projects); err != nil {
				return nil, false, err
			}
			for _, record := range records {
				mirrored[fmt.Sprint(record["uuid"])] = true
			}
		}
		start["sessionUpdates"] = d.approved
		if h, err = d.spawn(start, env); err != nil {
			return nil, false, err
		}
		h.key, h.projects, h.mirrored = string(key), projects, mirrored
		d.host = h
		return h, false, nil
	}
	prompt := map[string]any{"type": "prompt", "uuid": newUUID(), "content": content}
	h, reused, err := acquire()
	if err == nil {
		err = h.write(prompt)
	}
	if err != nil && reused {
		// The idle host exited just as this prompt arrived; a fresh one resumes.
		d.Close()
		if h, _, err = acquire(); err == nil {
			err = h.write(prompt)
		}
	}
	if err != nil {
		d.Close()
		return err
	}

	translator := translation{driver: d, ctx: ctx, emit: emit, model: model.ID, tools: map[string]string{}, tasks: h.tasks}
	done, grace := ctx.Done(), (<-chan time.Time)(nil)
	for {
		select {
		case frame, open := <-h.frames:
			if !open {
				d.Close()
				if ctx.Err() != nil {
					return translator.abort()
				}
				return errors.New("claude SDK exited without a result; execution outcome is unknown")
			}
			if frame.Type == "settled" {
				if err := mirror(d.options.Manager, h.projects, frame.Session, h.mirrored); err != nil {
					return err
				}
				h.last = lastMessage(d.options.Manager, 0)
				if ctx.Err() != nil {
					return translator.abort()
				}
				return nil
			}
			if err = d.handle(ctx, frame, &translator, h, prompts, config); err == nil && translator.answered && ctx.Err() == nil {
				translator.answered = false
				err = d.steer(ctx, &translator, h, config)
			}
			if err != nil {
				d.Close()
				if ctx.Err() != nil {
					return translator.abort()
				}
				return err
			}
		case <-done:
			done, grace = nil, time.After(5*time.Second)
			translator.cancelled = true
			_ = h.write(map[string]string{"type": "cancel"})
		case <-grace:
			d.Close()
			return translator.abort()
		}
	}
}

// steer hands queued steering messages to the running native turn after its tool
// results, as Orb's own loop does.
func (d *Driver) steer(ctx context.Context, t *translation, h *host, config engine.AgentLoopConfig) error {
	if config.GetSteeringMessages == nil {
		return nil
	}
	messages, err := config.GetSteeringMessages(ctx)
	if err != nil || len(messages) == 0 {
		return err
	}
	content, err := nativeContent(ctx, messages, config.ConvertToLLM)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if err = t.emit(ctx, engine.MessageStartEvent{Message: message}); err != nil {
			return err
		}
		if err = t.emit(ctx, engine.MessageEndEvent{Message: message}); err != nil {
			return err
		}
	}
	return h.write(map[string]any{"type": "prompt", "uuid": newUUID(), "content": content})
}

func (d *Driver) handle(ctx context.Context, frame hostFrame, translator *translation, h *host, prompts engine.AgentMessages, config engine.AgentLoopConfig) error {
	switch frame.Type {
	case "sdk":
		return translator.event(frame.Event)
	case "context":
		_, err := d.options.Manager.AppendCustomEntry(Name+".context", frame.Event)
		return err
	case "session":
		var updates []json.RawMessage
		err := json.Unmarshal(frame.Event, &updates)
		d.approved = append(d.approved, updates...)
		return err
	case "error":
		return errors.New(frame.Message)
	case "tool":
		if err := translator.startEarly(frame.ToolID); err != nil {
			return err
		}
		decision, reason := d.approve(ctx, frame.Tool, frame.ToolID, frame.CWD, frame.Args, prompts, config.BeforeToolCall)
		return h.write(map[string]any{"type": "reply", "id": frame.ID, "value": map[string]string{"decision": decision, "reason": reason}})
	case "elicitation":
		return h.write(map[string]any{"type": "reply", "id": frame.ID, "value": d.elicit(ctx, frame.Elicitation)})
	case "questions":
		request, err := nativeQuestions(frame.Questions)
		result := questions.Result{Cancelled: true}
		if err == nil {
			result, err = questions.Ask(ctx, request, d.options.Ask)
		}
		if err != nil {
			result = questions.Result{Cancelled: true}
		}
		return h.write(map[string]any{"type": "reply", "id": frame.ID, "value": result})
	case "input":
		value, cancelled := "", true
		if d.options.Ask != nil {
			var err error
			value, err = d.options.Ask(ctx, frame.Title, frame.Choices)
			cancelled = err != nil
			// ponytail: matches the runtime's no-UI error text; a changed text falls back to deny.
			if err != nil && d.options.Headless && !frame.Ruled && slices.Contains(frame.Choices, "y approve once") && strings.Contains(err.Error(), "requires an interactive UI") {
				value, cancelled = "y approve once", false
			}
		}
		return h.write(map[string]any{"type": "reply", "id": frame.ID, "value": value, "cancelled": cancelled})
	}
	return errors.New("invalid Claude SDK host frame")
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
	driver          *Driver
	ctx             context.Context
	emit            engine.EventSink
	model           string
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
	cancelled       bool // Orb asked to stop; replies still finishing end as aborted
	stopped         bool // a reply already ended as aborted
	answered        bool // tool results arrived, so steering can join the turn
	progress        map[string]int
	tasks           map[string]*nativeTask
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
		// tool_use_result is an object for most tools but a plain string
		// when a tool fails, so only an object is read for its patch.
		Tool json.RawMessage `json:"tool_use_result"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if e.Parent != nil {
		return nil
	} // Native subagents retain their own transcripts.
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
			SkipTranscript                        bool   `json:"skip_transcript"`
			TaskID                                string `json:"task_id"`
			TaskType                              string `json:"task_type"`
			Backgrounded                          bool   `json:"is_backgrounded"`
			Attempt                               int
			MaxRetries                            int `json:"max_retries"`
			RetryDelay                            int `json:"retry_delay_ms"`
			Compact                               struct {
				Pre  int64  `json:"pre_tokens"`
				Post *int64 `json:"post_tokens"`
			} `json:"compact_metadata"`
			Patch struct {
				Backgrounded bool `json:"is_backgrounded"`
			}
		}
		if err := json.Unmarshal(raw, &activity); err != nil {
			return err
		}
		if activity.PermissionMode != "" {
			mode := activity.PermissionMode
			if !slices.Contains(nativeModes[:], mode) {
				mode = "default"
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
		// A foreground task already has its tool row; only subagents and background work get notices.
		case "task_started":
			if activity.Ambient || activity.SkipTranscript {
				return nil
			}
			if t.tasks == nil {
				t.tasks = map[string]*nativeTask{}
			}
			task := &nativeTask{description: activity.Description, shown: activity.TaskType == "local_agent" || activity.Backgrounded}
			if len(t.tasks) < 1024 {
				t.tasks[activity.TaskID] = task
			}
			if task.shown {
				return t.notice("Claude task started: " + activity.Description)
			}
		case "task_updated":
			if task := t.tasks[activity.TaskID]; task != nil && !task.shown && activity.Patch.Backgrounded {
				task.shown = true
				return t.notice("Claude task moved to background: " + task.description)
			}
		case "task_notification":
			task := t.tasks[activity.TaskID]
			delete(t.tasks, activity.TaskID)
			if task != nil && task.shown {
				return t.notice("Claude task " + activity.Status + ": " + activity.Summary)
			}
		case "init":
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
			return nil
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
			var details json.RawMessage
			if diff := patchDiff(toolPatch(e.Tool)); diff != "" {
				details, _ = json.Marshal(map[string]string{"diff": diff})
			}
			if err = t.toolResult(block.ID, content, block.IsError, details); err != nil {
				return err
			}
			t.answered = true
		}
	case "result":
		t.result = true
		if err := t.finish(); err != nil {
			return err
		}
		// A failure shows as an error reply, as Orb's own loop ends a failed turn;
		// one already rendered as an error, or a turn Orb stopped, is not reported again.
		if (e.IsError || e.Subtype != "success") && !t.errored && !t.cancelled {
			reason := strings.Join(e.Errors, "; ")
			if reason == "" {
				reason = e.Result
			}
			if reason == "" {
				reason = "Claude ended the turn: " + e.Subtype
			}
			if err := t.begin(t.message(nativeMessage{}), ""); err != nil {
				return err
			}
			t.partial.StopReason, t.partial.ErrorMessage, t.errored = ai.StopReasonError, &reason, true
			if err := t.finish(); err != nil {
				return err
			}
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

type patchHunk struct {
	OldStart int `json:"oldStart"`
	NewStart int `json:"newStart"`
	Lines    []string
}

// patchDiff renders a native structured patch in Orb's numbered edit-diff format.
func patchDiff(hunks []patchHunk) string {
	width := 1
	for _, hunk := range hunks {
		width = max(width, len(strconv.Itoa(max(hunk.OldStart, hunk.NewStart)+len(hunk.Lines))))
	}
	var out []string
	for i, hunk := range hunks {
		if i > 0 {
			out = append(out, " "+strings.Repeat(" ", width)+" ...")
		}
		before, after := hunk.OldStart, hunk.NewStart
		for _, line := range hunk.Lines {
			if line == "" {
				continue
			}
			switch line[0] {
			case '+':
				out = append(out, fmt.Sprintf("+%*d %s", width, after, line[1:]))
				after++
			case '-':
				out = append(out, fmt.Sprintf("-%*d %s", width, before, line[1:]))
				before++
			default:
				out = append(out, fmt.Sprintf(" %*d %s", width, before, line[1:]))
				before, after = before+1, after+1
			}
		}
	}
	return strings.Join(out, "\n")
}

func (t *translation) toolResult(id string, content ai.ToolResultContent, isError bool, details json.RawMessage) error {
	result := &ai.ToolResultMessage{ToolCallID: id, ToolName: t.tools[id], Content: content, Details: details, IsError: isError, Timestamp: time.Now().UnixMilli()}
	for _, event := range []engine.AgentEvent{
		engine.ToolExecutionEndEvent{ToolCallID: id, ToolName: result.ToolName, IsError: isError, Result: engine.AgentToolResult{Content: content, Details: details}},
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
	if t.cancelled && m.StopReason != ai.StopReasonError {
		reason := "Request was aborted"
		m.StopReason, m.ErrorMessage = ai.StopReasonAborted, &reason
	}
	t.stopped = m.StopReason == ai.StopReasonAborted
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
		if err := t.toolResult(id, ai.ToolResultContent{&ai.TextContent{Text: "Operation aborted"}}, true, nil); err != nil {
			return err
		}
	}
	t.cancelled = true
	if t.partial == nil && t.stopped {
		return cause
	}
	if t.partial == nil {
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

// toolPatch reads an Edit's structuredPatch from tool_use_result, which is a
// string rather than an object when the tool failed.
func toolPatch(raw json.RawMessage) []patchHunk {
	var result struct {
		Patch []patchHunk `json:"structuredPatch"`
	}
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return result.Patch
}
