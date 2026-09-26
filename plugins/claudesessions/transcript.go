package claudesessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
)

// Orb holds each conversation's Claude transcript. Claude's own records are
// mirrored into the Orb journal as it writes them; whenever a native session
// must start (another model answered, the branch moved, the account changed,
// the host exited), Orb rebuilds the transcript Claude resumes from: its own
// records where it answered, every other turn rewritten as plain messages.

const transcriptEntry = Name + ".transcript"

func messageRole(entry *session.SessionEntry) string {
	var message struct{ Role string }
	_ = json.Unmarshal(entry.Message, &message)
	return message.Role
}

// lastMessage is the message the conversation ends at, before its skip newest
// prompts: those a turn is about to send.
func lastMessage(manager extensions.ReadonlySessionManager, skip int) string {
	for entry := manager.GetLeafEntry(); entry != nil; {
		if entry.Type == "message" {
			if skip == 0 || messageRole(entry) != "user" {
				return entry.ID
			}
			skip--
		}
		if entry.ParentID == nil {
			break
		}
		entry = manager.GetEntry(*entry.ParentID)
	}
	return ""
}

// rebuild is the transcript of the branch before its skip newest prompts.
func rebuild(manager extensions.ReadonlySessionManager, skip int) []map[string]any {
	branch := manager.GetBranch()
	for i := len(branch) - 1; i >= 0 && skip > 0; i-- {
		if branch[i].Type != "message" {
			continue
		}
		if messageRole(&branch[i]) != "user" {
			break
		}
		branch, skip = branch[:i], skip-1
	}
	t := transcript{cwd: manager.GetCWD(), seen: map[string]bool{}}
	// Orb's own compaction replaces what came before with its summary.
	for i := len(branch) - 1; i >= 0; i-- {
		if branch[i].Type == "compaction" {
			kept := i
			for k := range branch[:i] {
				if branch[k].ID == branch[i].FirstKeptEntryID {
					kept = k
				}
			}
			t.text(branch[i], "user", "Summary of the conversation so far:\n"+branch[i].Summary)
			branch = append(branch[kept:i:i], branch[i+1:]...)
			break
		}
	}
	// A turn is Claude's when Claude answered it; its records stand for its
	// messages once mirrored, and its messages stand for themselves until then.
	native := make([]bool, len(branch))
	answered := false
	for i := len(branch) - 1; i >= 0; i-- {
		if branch[i].Type != "message" {
			continue
		}
		var message struct{ Role, Provider string }
		_ = json.Unmarshal(branch[i].Message, &message)
		if message.Role == "assistant" {
			answered = message.Provider == Name
		}
		native[i] = answered
	}
	var pending []session.SessionEntry
	flush := func() {
		for _, entry := range pending {
			t.message(entry)
		}
		pending = nil
	}
	for i, entry := range branch {
		switch {
		case entry.CustomType == transcriptEntry:
			pending = nil
			var records []map[string]any
			_ = json.Unmarshal(entry.Data, &records)
			for _, record := range records {
				t.native(record)
			}
		case entry.Type == "message" && native[i]:
			pending = append(pending, entry)
		case entry.Type == "message":
			flush()
			t.message(entry)
		case entry.Type == "branch_summary" && entry.Summary != "":
			flush()
			t.text(entry, "user", "Summary of another branch of this conversation:\n"+entry.Summary)
		}
	}
	flush()
	return t.records
}

type transcript struct {
	cwd     string
	records []map[string]any
	seen    map[string]bool
	merged  bool // the last record was written by Orb and may take more content
}

func (t *transcript) native(record map[string]any) {
	if id, _ := record["uuid"].(string); id != "" {
		if t.seen[id] {
			return
		}
		t.seen[id] = true
	}
	t.records, t.merged = append(t.records, record), false
}

// message rewrites an Orb message as Claude reads another model's turn: its
// text, and its tool calls and results as text, since Claude has other tools.
func (t *transcript) message(entry session.SessionEntry) {
	message, err := ai.UnmarshalMessage(entry.Message)
	if err != nil {
		return
	}
	var blocks []map[string]any
	role := "user"
	switch m := message.(type) {
	case *ai.UserMessage:
		if m.Content.Text != nil {
			blocks = append(blocks, map[string]any{"type": "text", "text": *m.Content.Text})
		}
		for _, block := range m.Content.Blocks {
			switch b := block.(type) {
			case *ai.TextContent:
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case *ai.ImageContent:
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": b.MimeType, "data": b.Data}})
			}
		}
	case *ai.AssistantMessage:
		role = "assistant"
		for _, block := range m.Content {
			switch b := block.(type) {
			case *ai.TextContent:
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case *ai.ToolCall:
				args, _ := json.Marshal(b.Arguments)
				blocks = append(blocks, map[string]any{"type": "text", "text": fmt.Sprintf("[%s %s]", b.Name, args)})
			}
		}
	case *ai.ToolResultMessage:
		var text []string
		for _, block := range m.Content {
			if b, ok := block.(*ai.TextContent); ok {
				text = append(text, b.Text)
			}
		}
		label := m.ToolName + " result"
		if m.IsError {
			label = m.ToolName + " error"
		}
		blocks = append(blocks, map[string]any{"type": "text", "text": "[" + label + "]\n" + strings.Join(text, "\n")})
	}
	t.write(entry, role, blocks)
}

func (t *transcript) text(entry session.SessionEntry, role, text string) {
	t.write(entry, role, []map[string]any{{"type": "text", "text": text}})
}

// write appends a record Orb wrote, joining one that has the same role so
// turns alternate as Claude expects. Its UUID derives from the Orb entry, so
// records Claude later chains to it rebuild identically.
func (t *transcript) write(entry session.SessionEntry, role string, blocks []map[string]any) {
	if len(blocks) == 0 {
		return
	}
	if last := len(t.records) - 1; t.merged && t.records[last]["type"] == role {
		message := t.records[last]["message"].(map[string]any)
		message["content"] = append(message["content"].([]map[string]any), blocks...)
		return
	}
	sum := sha256.Sum256([]byte(entry.ID))
	h := []byte(hex.EncodeToString(sum[:16]))
	h[12], h[16] = '4', '8'
	id := fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
	var parent any
	if len(t.records) > 0 {
		parent = t.records[len(t.records)-1]["uuid"]
	}
	message := map[string]any{"role": role, "content": blocks}
	if role == "assistant" {
		message = map[string]any{"id": "msg_orb_" + strings.ReplaceAll(id, "-", ""), "type": "message", "role": role, "model": "orb", "content": blocks,
			"stop_reason": "end_turn", "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}
	}
	t.records = append(t.records, map[string]any{"type": role, "uuid": id, "parentUuid": parent, "timestamp": entry.Timestamp,
		"cwd": t.cwd, "isSidechain": false, "userType": "external", "message": message})
	t.merged = true
}

// projectDir is where Claude Code keeps the transcripts of sessions run in
// cwd: every character but ASCII letters and digits becomes a dash, and a name
// over 200 characters is cut and suffixed with its path's hash, as the CLI does.
func projectDir(configDir, cwd string) string {
	units := utf16.Encode([]rune(cwd))
	name := make([]byte, len(units))
	var hash int32
	for i, unit := range units {
		hash = hash*31 + int32(unit)
		name[i] = '-'
		if unit < 128 && (unicode.IsLetter(rune(unit)) || unicode.IsDigit(rune(unit))) {
			name[i] = byte(unit)
		}
	}
	if len(name) > 200 {
		return filepath.Join(configDir, "projects", string(name[:200])+"-"+strconv.FormatInt(max(int64(hash), -int64(hash)), 36))
	}
	return filepath.Join(configDir, "projects", string(name))
}

// writeTranscript saves records where Claude resumes session sessionID from.
func writeTranscript(records []map[string]any, sessionID, projects string) error {
	if err := os.MkdirAll(projects, 0o700); err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for _, record := range records {
		record["sessionId"] = sessionID
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(projects, sessionID+".jsonl"), buffer.Bytes(), 0o600)
}

// mirror copies the conversation records Claude wrote to session since the
// last copy into the Orb journal, so Orb can rebuild them later.
func mirror(manager *session.SessionManager, projects, sessionID string, mirrored map[string]bool) error {
	data, err := os.ReadFile(filepath.Join(projects, sessionID+".jsonl"))
	if err != nil || sessionID == "" {
		return nil //nolint:nilerr // ponytail: a turn Claude did not record has nothing to mirror.
	}
	var records []json.RawMessage
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		var record struct {
			Type, UUID string
			Sidechain  bool `json:"isSidechain"`
		}
		if json.Unmarshal(line, &record) != nil || record.Sidechain || mirrored[record.UUID] || !keptRecord(record.Type) {
			continue
		}
		if record.UUID != "" {
			mirrored[record.UUID] = true
		}
		records = append(records, line)
	}
	if len(records) == 0 {
		return nil
	}
	_, err = manager.AppendCustomEntry(transcriptEntry, records)
	return err
}

// keptRecord is the conversation part of Claude's transcript: what it resumes from.
func keptRecord(kind string) bool {
	return kind == "user" || kind == "assistant" || kind == "system" || kind == "summary"
}

// ImportClaudeCode opens a Claude Code session in a new Orb conversation: its
// messages to read and continue with any model, and its records for Claude to
// resume from. create makes the conversation in the session's directory.
func ImportClaudeCode(id string, env []string, create func(cwd string) (*session.SessionManager, error)) (*session.SessionManager, error) {
	base, _ := baseConfig(env)
	paths, _ := filepath.Glob(filepath.Join(base, "projects", "*", filepath.Base(id)+".jsonl"))
	if len(paths) == 0 {
		return nil, errors.New("no Claude Code session " + id)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, err
	}
	var records []map[string]any
	var messages []ai.Message
	var reply *ai.AssistantMessage
	tools, cwd := map[string]string{}, ""
	for line := range strings.SplitSeq(string(data), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		kind, _ := record["type"].(string)
		if sidechain, _ := record["isSidechain"].(bool); sidechain || !keptRecord(kind) {
			continue
		}
		records = append(records, record)
		if cwd == "" {
			cwd, _ = record["cwd"].(string)
		}
		raw, _ := json.Marshal(record["message"])
		var m struct {
			ID, Model string
			Content   json.RawMessage
		}
		if meta, _ := record["isMeta"].(bool); meta || json.Unmarshal(raw, &m) != nil {
			continue
		}
		var blocks []struct {
			Type, Text, ID, Name string
			Input                map[string]any
			ToolUseID            string          `json:"tool_use_id"`
			Content              json.RawMessage `json:"content"`
			IsError              bool            `json:"is_error"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			var text string
			if json.Unmarshal(m.Content, &text) == nil && kind == "user" {
				reply = nil
				messages = append(messages, &ai.UserMessage{Content: ai.NewUserText(text)})
			}
			continue
		}
		switch kind {
		case "assistant":
			// The CLI records one line per content block of the same API message.
			if reply == nil || reply.ResponseID == nil || *reply.ResponseID != m.ID {
				responseID := m.ID
				reply = &ai.AssistantMessage{API: Name, Provider: Name, Model: m.Model, ResponseID: &responseID, StopReason: ai.StopReasonStop}
				messages = append(messages, reply)
			}
			for _, block := range blocks {
				switch block.Type {
				case "text":
					reply.Content = append(reply.Content, &ai.TextContent{Text: block.Text})
				case "tool_use":
					tools[block.ID] = block.Name
					reply.Content = append(reply.Content, &ai.ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
					reply.StopReason = ai.StopReasonToolUse
				}
			}
		case "user":
			reply = nil
			var text []string
			for _, block := range blocks {
				switch block.Type {
				case "text":
					text = append(text, block.Text)
				case "tool_result":
					content, _ := toolResultContent(block.Content)
					messages = append(messages, &ai.ToolResultMessage{ToolCallID: block.ToolUseID, ToolName: tools[block.ToolUseID], Content: content, IsError: block.IsError})
				}
			}
			if len(text) > 0 {
				messages = append(messages, &ai.UserMessage{Content: ai.NewUserText(strings.Join(text, "\n"))})
			}
		}
	}
	if len(records) == 0 {
		return nil, errors.New("Claude Code session " + id + " is empty") //nolint:staticcheck // Product name.
	}
	manager, err := create(cwd)
	if err != nil {
		return nil, err
	}
	if _, err := manager.AppendModelChange(Name, "default"); err != nil {
		return nil, err
	}
	for _, message := range messages {
		if _, err := manager.AppendMessage(message); err != nil {
			return nil, err
		}
	}
	_, err = manager.AppendCustomEntry(transcriptEntry, records)
	return manager, err
}
