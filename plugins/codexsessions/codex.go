// Package codexsessions opens Codex CLI threads as Orb conversations, to read
// and continue with any model, and takes in the turns Codex adds afterwards.
//
// Codex indexes its threads in state_5.sqlite; each thread's rollout JSONL is
// the record its own resume reads, so it is the source here: the items the
// model saw, with reasoning still encrypted for replay.
package codexsessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
)

// Name is the plugin switch that enables opening Codex threads.
const Name = "codex-sessions"

// rolloutEntry marks how many bytes of the thread's own rollout the
// conversation holds.
const rolloutEntry = Name + ".rollout"

// ErrNoCodexSession reports an ID that names no Codex thread.
var ErrNoCodexSession = errors.New("no Codex session")

func codexHome(env []string) string {
	for _, item := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(item, "CODEX_HOME="); ok && value != "" {
			return value
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

type thread struct{ rollout, cwd, name string }

// history is a thread's complete rollout lines, after the lines of the thread
// it was forked from while it shares their history; own is how many bytes of
// its own rollout they hold. end limits the rollout read, or -1.
func history(ctx context.Context, home, path string, end int64) (lines []byte, own int64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	var reader io.Reader = file
	if end >= 0 {
		reader = io.LimitReader(file, end)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, 0, err
	}
	data = data[:bytes.LastIndexByte(data, '\n')+1]
	first, _, _ := bytes.Cut(data, []byte("\n"))
	var meta struct {
		Type    string
		Payload struct {
			Base *struct {
				ThreadID string `json:"thread_id"`
				End      int64  `json:"end_byte_offset"`
			} `json:"history_base"`
		}
	}
	if json.Unmarshal(first, &meta) != nil || meta.Type != "session_meta" || meta.Payload.Base == nil {
		return data, int64(len(data)), nil
	}
	parent, err := lookup(ctx, home, meta.Payload.Base.ThreadID)
	if err != nil {
		return nil, 0, err
	}
	inherited, _, err := history(ctx, home, parent.rollout, meta.Payload.Base.End)
	return append(inherited, data...), int64(len(data)), err
}

// ImportCodex opens a Codex thread in a new Orb conversation. create makes the
// conversation in the thread's directory, under the thread's own ID so opening
// it again finds the Orb conversation.
func ImportCodex(id string, env []string, create func(cwd string) (*session.SessionManager, error)) (*session.SessionManager, error) {
	ctx := context.Background()
	home := codexHome(env)
	found, err := lookup(ctx, home, filepath.Base(id))
	if err != nil {
		return nil, err
	}
	data, own, err := history(ctx, home, found.rollout, -1)
	if err != nil {
		return nil, err
	}
	converted := convert(data, 0)
	if len(converted.out) == 0 {
		return nil, errors.New("Codex session " + id + " is empty") //nolint:staticcheck // Product name.
	}
	manager, err := create(found.cwd)
	if err != nil {
		return nil, err
	}
	if found.name != "" {
		if _, err := manager.AppendSessionInfo(found.name); err != nil {
			return nil, err
		}
	}
	return manager, converted.write(manager, own)
}

// catchUp takes into an imported conversation the turns its Codex thread
// gained since, as when it went on in Codex. Once the conversation went on in
// Orb, the two have diverged and Orb's turns win.
func catchUp(ctx context.Context, manager *session.SessionManager, env []string) error {
	var marker *session.SessionEntry
	for entry := manager.GetLeafEntry(); entry != nil && marker == nil; {
		switch {
		case entry.Type == "custom" && entry.CustomType == rolloutEntry:
			marker = entry
		case entry.Type == "message" || entry.Type == "compaction" || entry.ParentID == nil:
			return nil
		default:
			entry = manager.GetEntry(*entry.ParentID)
		}
	}
	if marker == nil {
		return nil
	}
	var held int64
	if err := json.Unmarshal(marker.Data, &held); err != nil {
		return err
	}
	home := codexHome(env)
	found, err := lookup(ctx, home, manager.GetSessionID())
	if err != nil {
		return err
	}
	if info, err := os.Stat(found.rollout); err != nil || info.Size() <= held {
		return err
	}
	data, own, err := history(ctx, home, found.rollout, -1)
	if err != nil || own <= held {
		return err
	}
	return convert(data, len(data)-int(own-held)).write(manager, own)
}

// Extension catches imported conversations up with their Codex threads when
// they open.
func Extension(env []string) extensions.Factory {
	return func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, extension extensions.Context) (any, error) {
			if manager, ok := extension.SessionManager().(*session.SessionManager); ok {
				// ponytail: a catch-up that fails leaves the conversation as Orb holds it.
				_ = catchUp(ctx, manager, env)
			}
			return nil, nil
		})
		return nil
	}
}

type output struct {
	message ai.Message
	summary string
}

type conversion struct {
	provider ai.ProviderID
	api      ai.API
	model    string
	reply    *ai.AssistantMessage
	calls    map[string]*ai.ToolCall
	out      []output
	skipped  map[string]int
}

// convert reads rollout lines as Orb messages; lines before from only set the
// state the later ones are read in.
func convert(data []byte, from int) *conversion {
	c := &conversion{calls: map[string]*ai.ToolCall{}, skipped: map[string]int{}}
	for offset := 0; offset < len(data); {
		line, _, _ := bytes.Cut(data[offset:], []byte("\n"))
		c.record(line)
		if offset += len(line) + 1; offset <= from {
			c.reply, c.out, c.skipped = nil, nil, map[string]int{}
		}
	}
	return c
}

func (c *conversion) record(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var record struct {
		Timestamp string
		Type      string
		Payload   json.RawMessage
	}
	if json.Unmarshal(line, &record) != nil {
		c.skipped["unreadable line"]++
		return
	}
	at, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
	switch record.Type {
	case "session_meta":
		var meta struct {
			Provider string `json:"model_provider"`
		}
		_ = json.Unmarshal(record.Payload, &meta)
		c.provider, c.api = ai.ProviderID(meta.Provider), Name
		if meta.Provider == "openai" {
			c.provider, c.api = "openai-codex", ai.APIOpenAICodexResponses
		}
	case "turn_context":
		var turn struct{ Model string }
		_ = json.Unmarshal(record.Payload, &turn)
		c.model = turn.Model
	case "response_item":
		c.item(record.Payload, at.UnixMilli())
	case "compacted":
		c.compacted(record.Payload)
	case "event_msg", "world_state", "token_usage_record", "inter_agent_communication_metadata":
		// Codex's interface events and accounting; the conversation is in response items.
	default:
		c.skipped[record.Type]++
	}
}

type part struct {
	Type     string
	Text     string
	ImageURL string `json:"image_url"`
}

type item struct {
	Type, ID, Role, Name, Phase string
	Namespace                   *string
	CallID                      string `json:"call_id"`
	Arguments                   json.RawMessage
	Input                       string
	Content                     []part
	Summary                     []part
	Output                      json.RawMessage
	Action                      struct{ Query string }
}

func (c *conversion) item(raw json.RawMessage, at int64) {
	var it item
	if json.Unmarshal(raw, &it) != nil {
		c.skipped["unreadable item"]++
		return
	}
	switch it.Type {
	case "message":
		switch it.Role {
		case "user":
			if blocks := userBlocks(it.Content); len(blocks) > 0 {
				c.add(&ai.UserMessage{Content: ai.NewUserContent(blocks...), Timestamp: at})
			}
		case "assistant":
			for _, p := range it.Content {
				if p.Type == "output_text" {
					// The signature Orb's Responses providers write and replay.
					signature, _ := json.Marshal(struct {
						Version int    `json:"v"`
						ID      string `json:"id"`
						Phase   string `json:"phase,omitempty"`
					}{1, it.ID, it.Phase})
					c.assistant(at).Content = append(c.assistant(at).Content, &ai.TextContent{Text: p.Text, TextSignature: new(string(signature))})
				}
			}
		}
	case "reasoning":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		delete(fields, "internal_chat_message_metadata_passthrough")
		signature, _ := json.Marshal(fields)
		var summary []string
		for _, p := range it.Summary {
			summary = append(summary, p.Text)
		}
		reply := c.assistant(at)
		reply.Content = append(reply.Content, &ai.ThinkingContent{Thinking: strings.Join(summary, "\n\n"), ThinkingSignature: new(string(signature))})
	case "function_call":
		var encoded string
		arguments := map[string]any{}
		_ = json.Unmarshal(it.Arguments, &encoded)
		_ = json.Unmarshal([]byte(encoded), &arguments)
		name := it.Name
		if command, ok := arguments["cmd"].(string); ok && name == "exec_command" {
			if dir, _ := arguments["workdir"].(string); dir != "" {
				command = "cd " + dir + " && " + command
			}
			name, arguments = "bash", map[string]any{"command": command}
		}
		c.call(&ai.ToolCall{ID: it.CallID + "|" + it.ID, Name: name, Arguments: arguments, Namespace: it.Namespace}, it.CallID, at)
	case "custom_tool_call":
		c.call(&ai.ToolCall{ID: it.CallID + "|" + it.ID, Name: it.Name, Arguments: map[string]any{"input": it.Input}, Namespace: it.Namespace}, it.CallID, at)
	case "function_call_output", "custom_tool_call_output":
		call := c.calls[it.CallID]
		if call == nil {
			c.skipped["output without its call"]++
			return
		}
		c.add(&ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: resultContent(it.Output), Timestamp: at})
	case "web_search_call":
		c.call(&ai.ToolCall{ID: it.ID, Name: "web_search", Arguments: map[string]any{"query": it.Action.Query}}, it.ID, at)
		c.add(&ai.ToolResultMessage{ToolCallID: it.ID, ToolName: "web_search", Content: ai.ToolResultContent{&ai.TextContent{Text: "Searched the web for " + it.Action.Query}}, Timestamp: at})
	case "agent_message", "tool_search_call", "tool_search_output":
		// Multi-agent mail and tool loading: Orb has no counterpart to replay them to.
	default:
		c.skipped[it.Type]++
	}
}

func (c *conversion) add(message ai.Message) {
	c.reply = nil
	c.out = append(c.out, output{message: message})
}

// assistant is the reply the current item belongs to: Codex records each
// output item of one response on its own line.
func (c *conversion) assistant(at int64) *ai.AssistantMessage {
	if c.reply == nil {
		c.reply = &ai.AssistantMessage{API: c.api, Provider: c.provider, Model: c.model, StopReason: ai.StopReasonStop, Timestamp: at}
		c.out = append(c.out, output{message: c.reply})
	}
	return c.reply
}

func (c *conversion) call(call *ai.ToolCall, callID string, at int64) {
	reply := c.assistant(at)
	reply.Content = append(reply.Content, call)
	reply.StopReason = ai.StopReasonToolUse
	c.calls[callID] = call
}

// compacted keeps, as Orb's compaction summary, the messages Codex kept when
// it compacted; its own summary is encrypted for OpenAI alone.
func (c *conversion) compacted(raw json.RawMessage) {
	var compaction struct {
		Replacement []item `json:"replacement_history"`
	}
	_ = json.Unmarshal(raw, &compaction)
	var kept []string
	for _, it := range compaction.Replacement {
		if it.Type != "message" {
			continue
		}
		for _, block := range userBlocks(it.Content) {
			if text, ok := block.(*ai.TextContent); ok && it.Role == "user" {
				kept = append(kept, "User: "+text.Text)
			}
		}
		for _, p := range it.Content {
			if p.Type == "output_text" && it.Role == "assistant" {
				kept = append(kept, "Assistant: "+p.Text)
			}
		}
	}
	if len(kept) > 0 {
		c.reply = nil
		c.out = append(c.out, output{summary: "Codex compacted the conversation here and kept these messages:\n\n" + strings.Join(kept, "\n\n")})
	}
}

// userBlocks is what the user wrote: Codex injects its context (AGENTS.md,
// environment, notices, image labels) as tagged text blocks of user messages.
func userBlocks(parts []part) ai.UserContentBlocks {
	var blocks ai.UserContentBlocks
	for _, p := range parts {
		text := strings.TrimSpace(p.Text)
		switch {
		case p.Type == "input_image":
			if image := dataImage(p.ImageURL); image != nil {
				blocks = append(blocks, image)
			}
		case p.Type != "input_text", strings.HasPrefix(text, "# AGENTS.md instructions"), strings.HasPrefix(text, "<") && strings.HasSuffix(text, ">"):
		default:
			blocks = append(blocks, &ai.TextContent{Text: p.Text})
		}
	}
	return blocks
}

func dataImage(url string) *ai.ImageContent {
	header, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ";base64,")
	if !ok || !strings.HasPrefix(url, "data:") {
		return nil
	}
	return &ai.ImageContent{Data: data, MimeType: header}
}

func resultContent(raw json.RawMessage) ai.ToolResultContent {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return ai.ToolResultContent{&ai.TextContent{Text: text}}
	}
	var parts []part
	_ = json.Unmarshal(raw, &parts)
	content := ai.ToolResultContent{}
	for _, p := range parts {
		switch p.Type {
		case "input_text":
			content = append(content, &ai.TextContent{Text: p.Text})
		case "input_image":
			if image := dataImage(p.ImageURL); image != nil {
				content = append(content, image)
			}
		}
	}
	return content
}

func (c *conversion) write(manager *session.SessionManager, own int64) error {
	// Only the latest compaction shapes the context, and each one costs Orb a
	// rebuild of it: a long thread compacted hundreds of times.
	last := -1
	for i, o := range c.out {
		if o.message == nil {
			last = i
		}
	}
	for i, o := range c.out {
		var err error
		switch {
		case o.message != nil:
			_, err = manager.AppendMessage(o.message)
		case i == last:
			_, err = manager.AppendCompaction(o.summary, "", 0)
		}
		if err != nil {
			return err
		}
	}
	if len(c.skipped) > 0 {
		kinds, count := make([]string, 0, len(c.skipped)), 0
		for kind, n := range c.skipped {
			kinds, count = append(kinds, kind), count+n
		}
		slices.Sort(kinds)
		notice := fmt.Sprintf("Skipped %d Codex records Orb can't read: %s.", count, strings.Join(kinds, ", "))
		if _, err := manager.AppendCustomMessageEntry(Name, notice, true); err != nil {
			return err
		}
	}
	_, err := manager.AppendCustomEntry(rolloutEntry, own)
	return err
}
