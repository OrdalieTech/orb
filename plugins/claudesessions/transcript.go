package claudesessions

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
// Each Orb conversation is one Claude Code session under the same ID: Orb
// appends what that session lacks, and Claude resumes it at Orb's branch.

const transcriptEntry = Name + ".transcript"

// ErrNoClaudeCodeSession reports an ID that names no Claude Code session.
var ErrNoClaudeCodeSession = errors.New("no Claude Code session")

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

// record is one line of a Claude Code transcript: Claude's own, kept as it was
// written with only its links decoded, or one Orb writes for another model's turn.
type record struct {
	UUID   string `json:"uuid"`
	Parent string `json:"parentUuid"`
	raw    json.RawMessage
	orb    map[string]any
}

// line encodes r as the session file holds it, under id after parent.
func (r *record) line(id, parent, sessionID string) ([]byte, error) {
	fields := r.orb
	if fields == nil {
		if err := json.Unmarshal(r.raw, &fields); err != nil {
			return nil, err
		}
	}
	fields["uuid"], fields["parentUuid"], fields["sessionId"] = id, nil, sessionID
	if parent != "" {
		fields["parentUuid"] = parent
	}
	return json.Marshal(fields)
}

// rebuild is the transcript of the branch before its skip newest prompts.
func rebuild(manager extensions.ReadonlySessionManager, skip int) []*record {
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
			var lines []json.RawMessage
			_ = json.Unmarshal(entry.Data, &lines)
			for _, line := range lines {
				t.native(line)
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
	// Claude reads the chain back from the last record: a parent missing from
	// the rebuild (a record an older Orb did not mirror) links to the one before.
	present := map[string]bool{}
	for i, r := range t.records {
		if r.Parent != "" && !present[r.Parent] && i > 0 {
			r.Parent = t.records[i-1].UUID
		}
		present[r.UUID] = true
	}
	return t.records
}

type transcript struct {
	cwd     string
	records []*record
	seen    map[string]bool
	merged  bool // the last record was written by Orb and may take more content
}

func (t *transcript) native(line json.RawMessage) {
	r := &record{raw: line}
	if json.Unmarshal(line, r) != nil || r.UUID == "" || t.seen[r.UUID] {
		return
	}
	t.seen[r.UUID] = true
	t.records, t.merged = append(t.records, r), false
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
	if last := len(t.records) - 1; t.merged && t.records[last].orb["type"] == role {
		message := t.records[last].orb["message"].(map[string]any)
		message["content"] = append(message["content"].([]map[string]any), blocks...)
		// A record that took more content is another record: a synced session holds the shorter one.
		t.records[last].UUID = uuidFor(t.records[last].UUID + " " + entry.ID)
		return
	}
	id, parent := uuidFor(entry.ID), ""
	if len(t.records) > 0 {
		parent = t.records[len(t.records)-1].UUID
	}
	message := map[string]any{"role": role, "content": blocks}
	if role == "assistant" {
		message = map[string]any{"id": "msg_orb_" + strings.ReplaceAll(id, "-", ""), "type": "message", "role": role, "model": "orb", "content": blocks,
			"stop_reason": "end_turn", "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}
	}
	t.records = append(t.records, &record{UUID: id, Parent: parent, orb: map[string]any{"type": role, "timestamp": entry.Timestamp,
		"cwd": t.cwd, "isSidechain": false, "userType": "external", "message": message}})
	t.merged = true
}

// uuidFor derives a stable UUID from key.
func uuidFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	h := []byte(hex.EncodeToString(sum[:16]))
	h[12], h[16] = '4', '8'
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// nativeSessionID is the Claude Code session of an Orb conversation: its own
// ID, which is a UUID, so both name the same conversation.
func nativeSessionID(id string) string {
	if uuidPattern.MatchString(id) {
		return strings.ToLower(id)
	}
	return uuidFor(id)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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

// syncTranscript brings session's file up to records, the branch Orb holds,
// by appending only what the file lacks: other models' turns, Orb's summaries.
// A record whose parent in the branch differs from its parent in the file (the
// branch was compacted or repaired) is appended as a copy under a derived UUID.
// It returns the UUID Claude resumes at and the file's size once synced.
func syncTranscript(records []*record, sessionID, projects string) (at string, size int64, err error) {
	path := filepath.Join(projects, sessionID+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}
	missing, at, err := planTranscript(records, data, sessionID)
	size = int64(len(data))
	if err != nil || len(missing) == 0 {
		return at, size, err
	}
	if err := os.MkdirAll(projects, 0o700); err != nil {
		return "", 0, err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	_, err = file.Write(missing)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return at, size + int64(len(missing)), err
}

// planTranscript is what a session file (data) lacks of records, as lines to
// append, and the UUID the branch ends at once they are.
func planTranscript(records []*record, data []byte, sessionID string) (missing []byte, at string, err error) {
	parents := map[string]string{}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		var record struct {
			UUID   string `json:"uuid"`
			Parent string `json:"parentUuid"`
		}
		if json.Unmarshal(line, &record) == nil && record.UUID != "" {
			parents[record.UUID] = record.Parent
		}
	}
	renamed := map[string]string{}
	var buffer bytes.Buffer
	for _, r := range records {
		id, parent := r.UUID, r.Parent
		if name, ok := renamed[parent]; ok {
			parent = name
		}
		if filed, ok := parents[id]; ok && filed != parent {
			renamed[id], id = uuidFor(id+" "+parent), uuidFor(id+" "+parent)
		}
		at = id
		if filed, ok := parents[id]; ok && filed == parent {
			continue
		}
		line, err := r.line(id, parent, sessionID)
		if err != nil {
			return nil, "", err
		}
		buffer.Write(append(line, '\n'))
		parents[id] = parent
	}
	// Claude Code resumes the branch its last pointer names, as after a rewind.
	if buffer.Len() > 0 {
		pointer, _ := json.Marshal(map[string]any{"type": "last-prompt", "leafUuid": at, "sessionId": sessionID})
		buffer.Write(append(pointer, '\n'))
	}
	return buffer.Bytes(), at, nil
}

// mirror copies the conversation records Claude wrote to session since the
// last copy into the Orb journal, so Orb can rebuild them later. read holds how
// far each native transcript was read: it only grows, so a turn reads its own tail.
func mirror(manager *session.SessionManager, projects, sessionID string, mirrored map[string]bool, read map[string]int64) error {
	file, err := os.Open(filepath.Join(projects, sessionID+".jsonl"))
	if err != nil || sessionID == "" {
		return nil //nolint:nilerr // ponytail: a turn Claude did not record has nothing to mirror.
	}
	defer func() { _ = file.Close() }()
	// A rewritten transcript is read again; mirrored keeps records from repeating.
	if info, err := file.Stat(); err == nil && info.Size() < read[sessionID] {
		read[sessionID] = 0
	}
	data, err := io.ReadAll(io.NewSectionReader(file, read[sessionID], 1<<62))
	if err != nil {
		return err
	}
	// A line Claude is still writing waits for the next turn.
	data = data[:bytes.LastIndexByte(data, '\n')+1]
	read[sessionID] += int64(len(data))
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
	if len(records) > 0 {
		if _, err = manager.AppendCustomEntry(transcriptEntry, records); err != nil {
			return err
		}
	}
	return markSynced(manager, read[sessionID])
}

// syncedEntry records the size of the Claude Code session file when Orb last
// read or wrote it: a file of that size gained nothing outside Orb.
const syncedEntry = Name + ".synced"

func syncedSize(manager extensions.ReadonlySessionManager) int64 {
	var size int64
	if entry := onBranch(manager, func(entry *session.SessionEntry) bool { return entry.CustomType == syncedEntry }); entry != nil {
		_ = json.Unmarshal(entry.Data, &size)
	}
	return size
}

func markSynced(manager *session.SessionManager, size int64) error {
	if size == syncedSize(manager) {
		return nil
	}
	_, err := manager.AppendCustomEntry(syncedEntry, size)
	return err
}

// keptRecord is the conversation part of Claude's transcript: what it resumes
// from. Attachments (files, hook output) are links of the same parentUuid chain.
func keptRecord(kind string) bool {
	return kind == "user" || kind == "assistant" || kind == "system" || kind == "attachment" || kind == "summary"
}

// branchOf is the conversation a Claude Code session file holds: the chain
// back from its latest record, through compactions to what they summarize.
// Branches left by a rewind stay in the file but are not the conversation.
// Each parent is the one written before its child (the CLI writes some UUIDs
// again after a compaction). It returns the chain's lines, undecoded.
func branchOf(data []byte) []json.RawMessage {
	type link struct {
		UUID      string `json:"uuid"`
		Parent    string `json:"parentUuid"`
		Logical   string `json:"logicalParentUuid"`
		Type      string `json:"type"`
		Sidechain bool   `json:"isSidechain"`
	}
	lines := bytes.Split(data, []byte("\n"))
	links := make([]link, len(lines))
	written := map[string][]int{}
	leaf := -1
	for i, line := range lines {
		if json.Unmarshal(line, &links[i]) != nil || links[i].UUID == "" || links[i].Sidechain {
			continue
		}
		written[links[i].UUID] = append(written[links[i].UUID], i)
		if keptRecord(links[i].Type) {
			leaf = i
		}
	}
	var chain []json.RawMessage
	for i := leaf; i >= 0; {
		if keptRecord(links[i].Type) {
			chain = append(chain, lines[i])
		}
		parent, next := cmp.Or(links[i].Parent, links[i].Logical), -1
		for _, j := range written[parent] {
			if j < i {
				next = j
			}
		}
		// A compaction whose summarized record the file lacks goes on from the
		// conversation's last record before it: compaction ends the branch it summarizes.
		for j := i - 1; next < 0 && links[i].Parent == "" && links[i].Logical != "" && j >= 0; j-- {
			if links[j].UUID != "" && keptRecord(links[j].Type) {
				next = j
			}
		}
		i = next
	}
	slices.Reverse(chain)
	return chain
}

// claudeMessages reads Claude Code records as Orb messages, in the directory
// they were written in.
func claudeMessages(lines []json.RawMessage) (messages []ai.Message, cwd string) {
	var reply *ai.AssistantMessage
	tools := map[string]string{}
	for _, line := range lines {
		var record map[string]any
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		kind, _ := record["type"].(string)
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
	return messages, cwd
}

// catchUp takes into an Orb conversation the turns its Claude Code session
// gained outside Orb, as when it was continued with claude --resume: the
// records past the branch Orb holds, when the session's latest branch goes on
// from it. Orb's own turns the session lacks win: the next Claude turn syncs them.
func catchUp(manager *session.SessionManager, env []string) (added bool, err error) {
	base, _ := baseConfig(env)
	id := nativeSessionID(manager.GetSessionID())
	paths, _ := filepath.Glob(filepath.Join(base, "projects", "*", id+".jsonl"))
	if len(paths) == 0 {
		return false, nil
	}
	if info, err := os.Stat(paths[0]); err != nil || info.Size() == syncedSize(manager) {
		return false, err
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return false, err
	}
	defer func() {
		if err == nil {
			err = markSynced(manager, int64(len(data)))
		}
	}()
	missing, at, err := planTranscript(rebuild(manager, 0), data, id)
	if err != nil || len(missing) > 0 || at == "" {
		return false, err
	}
	chain := branchOf(data)
	for i, line := range chain {
		if i == len(chain)-1 || !bytes.Contains(line, []byte(`"uuid":"`+at+`"`)) {
			continue
		}
		records := chain[i+1:]
		messages, _ := claudeMessages(records)
		for _, message := range messages {
			if _, err := manager.AppendMessage(message); err != nil {
				return false, err
			}
		}
		_, err = manager.AppendCustomEntry(transcriptEntry, records)
		return err == nil, err
	}
	return false, nil
}

// ImportClaudeCode opens a Claude Code session in a new Orb conversation: its
// messages to read and continue with any model, and its records for Claude to
// resume from. create makes the conversation in the session's directory, under
// the session's own ID so opening it again finds the Orb conversation.
func ImportClaudeCode(id string, env []string, create func(cwd string) (*session.SessionManager, error)) (*session.SessionManager, error) {
	base, _ := baseConfig(env)
	paths, _ := filepath.Glob(filepath.Join(base, "projects", "*", filepath.Base(id)+".jsonl"))
	if len(paths) == 0 {
		return nil, ErrNoClaudeCodeSession
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, err
	}
	records := branchOf(data)
	messages, cwd := claudeMessages(records)
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
