package claudesessions

import (
	"bufio"
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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/jsonwire"
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

// lastMessage is the message the conversation ends at, before its skip newest
// prompts: those a turn is about to send.
func lastMessage(manager extensions.ReadonlySessionManager, skip int) string {
	for entry := manager.GetLeafEntry(); entry != nil; {
		if entry.Type == "message" {
			if skip == 0 || jsonwire.MessageRole(entry.Message) != "user" {
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
	// images resolves the images a kept record refers to (imageRef).
	images func(hash string) (string, bool)
}

// line encodes r as the session file holds it, under id after parent.
func (r *record) line(id, parent, sessionID string) ([]byte, error) {
	fields := r.orb
	if fields == nil {
		decoder := json.NewDecoder(bytes.NewReader(r.raw))
		decoder.UseNumber()
		if err := decoder.Decode(&fields); err != nil {
			return nil, err
		}
		if r.images != nil {
			eachImage(fields["message"], func(block map[string]any, source map[string]any) {
				data, _ := source["data"].(string)
				if hash, ok := strings.CutPrefix(data, imageRef); ok {
					if image, found := r.images(hash); found {
						source["data"] = image
					} else {
						clear(block)
						block["type"], block["text"] = "text", "[image]"
					}
				}
			})
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
		if jsonwire.MessageRole(branch[i].Message) != "user" {
			break
		}
		branch, skip = branch[:i], skip-1
	}
	t := transcript{cwd: manager.GetCWD(), seen: map[string]bool{}}
	var images map[string]string
	t.images = func(hash string) (string, bool) {
		if images == nil {
			images = sessionImages(manager.GetEntries())
		}
		image, ok := images[hash]
		return image, ok
	}
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
			for _, line := range jsonwire.Elements(entry.Data) {
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
	images  func(hash string) (string, bool)
}

func (t *transcript) native(line json.RawMessage) {
	l := readLinks(line)
	r := &record{UUID: l.UUID, Parent: l.Parent, raw: line, images: t.images}
	if r.UUID == "" || t.seen[r.UUID] {
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
	// Off Windows the CLI runs in the directory as getcwd resolves it (on macOS
	// /tmp is /private/tmp) and names the transcript's directory after that;
	// Windows keeps the path it was given, short names and all.
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil && runtime.GOOS != "windows" {
		cwd = resolved
	}
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
// known, when given, is what the last sync read or wrote of the file: a file
// still of that size gained nothing since, so it is not read again.
func syncTranscript(records []*record, sessionID, projects string, known *fileLinks) (at string, size int64, err error) {
	path := filepath.Join(projects, sessionID+".jsonl")
	if known == nil {
		known = &fileLinks{}
	}
	if info, err := os.Stat(path); err != nil || known.path != path || info.Size() != known.size {
		parents := map[string]string{}
		size, err := eachLine(path, func(line []byte, _ int64) {
			if l := readLinks(line); l.UUID != "" {
				parents[l.UUID] = l.Parent
			}
		})
		if err != nil {
			return "", 0, err
		}
		*known = fileLinks{path: path, size: size, parents: parents}
	}
	size = known.size
	// planTranscript records what it plans in known; a failure leaves it to read again.
	known.size = -1
	missing, at, err := planTranscript(records, known.parents, sessionID)
	if err != nil || len(missing) == 0 {
		if err == nil {
			known.size = size
		}
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
	if err == nil {
		known.size = size + int64(len(missing))
	}
	return at, size + int64(len(missing)), err
}

// fileLinks is what Orb last read or wrote of a Claude Code session file: its
// size then, and the parent of each record it holds.
type fileLinks struct {
	path    string
	size    int64
	parents map[string]string
}

// eachLine visits each line of the file at path, with its offset, a line at a
// time, as a Claude Code session file can be hundreds of megabytes, and returns
// the bytes read; a missing file has none. A line is only valid during its visit.
func eachLine(path string, visit func(line []byte, offset int64)) (size int64, err error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReaderSize(file, 1<<20)
	var long []byte
	var offset int64
	for {
		chunk, err := reader.ReadSlice('\n')
		size += int64(len(chunk))
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk...)
			continue
		}
		line := chunk
		if len(long) > 0 {
			line, long = append(long, chunk...), long[:0]
		}
		visit(bytes.TrimSuffix(line, []byte("\n")), offset)
		offset = size
		if errors.Is(err, io.EOF) {
			return size, nil
		}
		if err != nil {
			return size, err
		}
	}
}

// links is what Orb reads of a Claude Code record to place it: its UUID, its
// parent's, the record a compaction summarizes, its type and whether it is a
// subagent's.
type links struct {
	UUID, Parent, Logical, Type string
	Sidechain                   bool
}

// readLinks reads a record's links without decoding the rest of it; none for a
// line that is not JSON.
func readLinks(line []byte) (l links) {
	if !jsonwire.Valid(line) {
		return l
	}
	jsonwire.EachMember(line, func(name, value []byte) bool {
		switch string(name) {
		case "uuid":
			l.UUID, _ = jsonwire.UnmarshalString(value)
		case "parentUuid":
			l.Parent, _ = jsonwire.UnmarshalString(value)
		case "logicalParentUuid":
			l.Logical, _ = jsonwire.UnmarshalString(value)
		case "type":
			l.Type, _ = jsonwire.UnmarshalString(value)
		case "isSidechain":
			l.Sidechain = string(value) == "true"
		}
		return true
	})
	return l
}

// parentsIn maps each record of a session file's data to its parent.
func parentsIn(data []byte) map[string]string {
	parents := map[string]string{}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if l := readLinks(line); l.UUID != "" {
			parents[l.UUID] = l.Parent
		}
	}
	return parents
}

// planTranscript is what a session file lacks of records, as lines to append,
// and the UUID the branch ends at once they are. parents maps each record
// the file holds to its parent.
func planTranscript(records []*record, parents map[string]string, sessionID string) (missing []byte, at string, err error) {
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
		if err = appendRecords(manager, records); err != nil {
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

// imageRef stands in a kept record for an image the conversation's messages
// hold, by the hash of its base64 data: screenshots are most of a session, and
// Claude's records and Orb's messages carry the same ones.
const imageRef = "orb:sha256:"

// appendRecords keeps records in the Orb journal without their toolUseResult,
// Claude Code's own copy of a tool's output for its display, which the model
// never reads and which can be most of a session (whole files, images), and
// with the images the turn's messages hold as references to them.
func appendRecords(manager *session.SessionManager, records []json.RawMessage) error {
	var images map[string]string
	kept := make([]json.RawMessage, len(records))
	for i, line := range records {
		kept[i] = line
		display, image := bytes.Contains(line, []byte(`"toolUseResult"`)), bytes.Contains(line, []byte(`"base64"`))
		if !display && !image {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(line, &fields) != nil {
			continue
		}
		delete(fields, "toolUseResult")
		if image {
			if images == nil {
				images = sessionImages(turnEntries(manager))
			}
			decoder := json.NewDecoder(bytes.NewReader(fields["message"]))
			decoder.UseNumber()
			var message any
			if decoder.Decode(&message) == nil {
				eachImage(message, func(_ map[string]any, source map[string]any) {
					data, _ := source["data"].(string)
					if hash := imageHash(data); images[hash] != "" {
						source["data"] = imageRef + hash
					}
				})
				if encoded, err := json.Marshal(message); err == nil {
					fields["message"] = encoded
				}
			}
		}
		if line, err := json.Marshal(fields); err == nil {
			kept[i] = line
		}
	}
	_, err := manager.AppendCustomEntry(transcriptEntry, kept)
	return err
}

// turnEntries are the entries since the records last kept: the turn whose
// records are being kept, or the whole conversation before its first.
func turnEntries(manager *session.SessionManager) []session.SessionEntry {
	var entries []session.SessionEntry
	for entry := manager.GetLeafEntry(); entry != nil && entry.CustomType != transcriptEntry; {
		entries = append(entries, *entry)
		if entry.ParentID == nil {
			break
		}
		entry = manager.GetEntry(*entry.ParentID)
	}
	return entries
}

// sessionImages maps the hash of each image the messages among entries hold
// to its base64 data.
func sessionImages(entries []session.SessionEntry) map[string]string {
	images := map[string]string{}
	for i := range entries {
		if entries[i].Type != "message" {
			continue
		}
		message, err := entries[i].DecodedMessage()
		if err != nil {
			continue
		}
		var blocks []any
		switch message := message.(type) {
		case *ai.UserMessage:
			for _, block := range message.Content.Blocks {
				blocks = append(blocks, block)
			}
		case *ai.ToolResultMessage:
			for _, block := range message.Content {
				blocks = append(blocks, block)
			}
		}
		for _, block := range blocks {
			if image, ok := block.(*ai.ImageContent); ok && len(image.Data) >= 1024 {
				images[imageHash(image.Data)] = image.Data
			}
		}
	}
	return images
}

func imageHash(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// eachImage visits each base64 image block of a decoded Claude message, with
// its source, nested in tool results too.
func eachImage(value any, visit func(block, source map[string]any)) {
	switch value := value.(type) {
	case map[string]any:
		if source, ok := value["source"].(map[string]any); ok && value["type"] == "image" && source["type"] == "base64" {
			visit(value, source)
			return
		}
		for _, item := range value {
			eachImage(item, visit)
		}
	case []any:
		for _, item := range value {
			eachImage(item, visit)
		}
	}
}

// branchOf is the conversation a Claude Code session file holds, as chainOf
// reads it from the file's data: the chain's lines, undecoded.
func branchOf(data []byte) []json.RawMessage {
	lines := bytes.Split(data, []byte("\n"))
	read := make([]links, len(lines))
	for i, line := range lines {
		read[i] = readLinks(line)
	}
	var chain []json.RawMessage
	for _, i := range chainOf(read) {
		chain = append(chain, lines[i])
	}
	return chain
}

// chainOf is the conversation among a session file's records, by their index:
// the chain back from its latest record, through compactions to what they
// summarize. Branches left by a rewind stay in the file but are not the
// conversation. Each parent is the one written before its child (the CLI
// writes some UUIDs again after a compaction).
func chainOf(records []links) []int {
	written := map[string][]int{}
	leaf := -1
	for i, l := range records {
		if l.UUID == "" || l.Sidechain {
			continue
		}
		written[l.UUID] = append(written[l.UUID], i)
		if keptRecord(l.Type) {
			leaf = i
		}
	}
	var chain []int
	for i := leaf; i >= 0; {
		if keptRecord(records[i].Type) {
			chain = append(chain, i)
		}
		parent, next := cmp.Or(records[i].Parent, records[i].Logical), -1
		for _, j := range written[parent] {
			if j < i {
				next = j
			}
		}
		// A compaction whose summarized record the file lacks goes on from the
		// conversation's last record before it: compaction ends the branch it summarizes.
		for j := i - 1; next < 0 && records[i].Parent == "" && records[i].Logical != "" && j >= 0; j-- {
			if records[j].UUID != "" && keptRecord(records[j].Type) {
				next = j
			}
		}
		i = next
	}
	slices.Reverse(chain)
	return chain
}

// claudeMessages reads Claude Code records as Orb shows Claude's turns under
// model, in the directory they were written in: messages, and notices for what
// Orb shows as activity. What Claude Code hides stays hidden.
func claudeMessages(lines []json.RawMessage, model string) (messages []any, cwd string) {
	var reply *ai.AssistantMessage
	tools := map[string]string{}
	translator := translation{model: model}
	for _, line := range lines {
		var record struct {
			Type, Subtype, CWD string
			Timestamp          time.Time
			Meta               bool `json:"isMeta"`
			Summary            bool `json:"isCompactSummary"`
			Failed             bool `json:"isApiErrorMessage"`
			Compact            struct {
				Pre  int64  `json:"preTokens"`
				Post *int64 `json:"postTokens"`
			} `json:"compactMetadata"`
			Message json.RawMessage
		}
		// A compaction's summary is the boundary's notice: the records it summarizes are read too.
		if json.Unmarshal(line, &record) != nil || record.Meta || record.Summary {
			continue
		}
		cwd = cmp.Or(cwd, record.CWD)
		var at int64
		if !record.Timestamp.IsZero() {
			at = record.Timestamp.UnixMilli()
		}
		switch record.Type {
		case "system":
			if record.Subtype == "compact_boundary" {
				reply = nil
				messages = append(messages, activity(compactNotice(record.Compact.Pre, record.Compact.Post), at))
			}
		case "assistant":
			var m nativeMessage
			if json.Unmarshal(record.Message, &m) != nil {
				continue
			}
			next := translator.message(m)
			next.Timestamp = at
			// The CLI records one line per content block of the same API message.
			if reply != nil && reply.ResponseID != nil && next.ResponseID != nil && *reply.ResponseID == *next.ResponseID {
				reply.Content = append(reply.Content, next.Content...)
				m.Usage.apply(&reply.Usage)
				if next.StopReason == ai.StopReasonLength {
					reply.StopReason = next.StopReason
				}
			} else {
				reply = next
				messages = append(messages, reply)
			}
			if record.Failed {
				var text []string
				for _, block := range reply.Content {
					if content, ok := block.(*ai.TextContent); ok {
						text = append(text, content.Text)
					}
				}
				reason := strings.Join(text, "\n")
				reply.Content, reply.StopReason, reply.ErrorMessage = ai.AssistantContent{}, ai.StopReasonError, &reason
			}
			for _, block := range next.Content {
				if call, ok := block.(*ai.ToolCall); ok {
					tools[call.ID] = call.Name
					if reply.StopReason == ai.StopReasonStop {
						reply.StopReason = ai.StopReasonToolUse
					}
				}
			}
		case "user":
			reply = nil
			var m struct {
				Content json.RawMessage
			}
			_ = json.Unmarshal(record.Message, &m)
			var text string
			if json.Unmarshal(m.Content, &text) == nil {
				messages = append(messages, prompt(text, at)...)
				continue
			}
			var blocks []struct {
				Type, Text string
				ToolUseID  string          `json:"tool_use_id"`
				Content    json.RawMessage `json:"content"`
				IsError    bool            `json:"is_error"`
				Source     struct {
					MediaType string `json:"media_type"`
					Data      string
				}
			}
			_ = json.Unmarshal(m.Content, &blocks)
			var texts []string
			var input []ai.UserContentBlock
			for _, block := range blocks {
				switch block.Type {
				case "tool_result":
					content, _ := toolResultContent(block.Content)
					messages = append(messages, &ai.ToolResultMessage{ToolCallID: block.ToolUseID, ToolName: tools[block.ToolUseID], Content: content, IsError: block.IsError, Timestamp: at})
				case "text":
					// Orb shows an interrupted turn by its aborted reply, not by Claude Code's marker.
					if !strings.HasPrefix(block.Text, "[Request interrupted by user") {
						texts, input = append(texts, block.Text), append(input, &ai.TextContent{Text: block.Text})
					}
				case "image":
					input = append(input, &ai.ImageContent{Data: block.Source.Data, MimeType: block.Source.MediaType})
				}
			}
			if len(input) == 0 {
				continue
			}
			message := &ai.UserMessage{Content: ai.NewUserContent(input...), Timestamp: at}
			if len(texts) == len(input) {
				message.Content = ai.NewUserText(strings.Join(texts, "\n"))
			}
			messages = append(messages, message)
		}
	}
	return messages, cwd
}

// prompt reads a prompt the CLI recorded as text: what Orb shows as activity
// (task notifications, local command output) becomes a notice, and a typed
// command reads as typed.
func prompt(text string, at int64) []any {
	tag := func(name string) string {
		_, rest, _ := strings.Cut(text, "<"+name+">")
		inner, _, _ := strings.Cut(rest, "</"+name+">")
		return strings.TrimSpace(inner)
	}
	notice := func(text string) []any {
		if text == "" {
			return nil
		}
		return []any{activity(text, at)}
	}
	switch trimmed := strings.TrimSpace(text); {
	case strings.HasPrefix(trimmed, "<task-notification>"):
		return notice("Claude task " + tag("status") + ": " + tag("summary"))
	case strings.HasPrefix(trimmed, "<local-command-stdout>"):
		return notice(tag("local-command-stdout"))
	case strings.HasPrefix(trimmed, "<bash-stdout>"):
		return notice(strings.TrimSpace(tag("bash-stdout") + "\n" + tag("bash-stderr")))
	case strings.HasPrefix(trimmed, "<command-"):
		text = strings.TrimSpace(tag("command-name") + " " + tag("command-args"))
	case strings.HasPrefix(trimmed, "<bash-input>"):
		text = "!" + tag("bash-input")
	}
	return []any{&ai.UserMessage{Content: ai.NewUserText(text), Timestamp: at}}
}

func appendMessages(manager *session.SessionManager, messages []any) error {
	for _, message := range messages {
		var err error
		if notice, ok := message.(*harness.CustomMessage); ok {
			_, err = manager.AppendCustomMessageEntry(notice.CustomType, notice.Content, notice.Display)
		} else {
			_, err = manager.AppendMessage(message)
		}
		if err != nil {
			return err
		}
	}
	return nil
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
	// The file is read a line at a time for its links; only the records it gained
	// are read whole.
	var read []links
	var spans [][2]int64
	parents := map[string]string{}
	size, err := eachLine(paths[0], func(line []byte, offset int64) {
		l := readLinks(line)
		read, spans = append(read, l), append(spans, [2]int64{offset, int64(len(line))})
		if l.UUID != "" {
			parents[l.UUID] = l.Parent
		}
	})
	if err != nil {
		return false, err
	}
	defer func() {
		if err == nil {
			err = markSynced(manager, size)
		}
	}()
	missing, at, err := planTranscript(rebuild(manager, 0), parents, id)
	if err != nil || len(missing) > 0 || at == "" {
		return false, err
	}
	chain := chainOf(read)
	for i, index := range chain {
		if i == len(chain)-1 || read[index].UUID != at {
			continue
		}
		records, err := readSpans(paths[0], spans, chain[i+1:])
		if err != nil {
			return false, err
		}
		model := "default"
		if current := manager.BuildSessionContext().Model; current != nil && current.Provider == Name {
			model = current.ModelID
		}
		messages, _ := claudeMessages(records, model)
		if err := appendMessages(manager, messages); err != nil {
			return false, err
		}
		err = appendRecords(manager, records)
		return err == nil, err
	}
	return false, nil
}

// readSpans reads the lines at indexes of the file at path, by their spans.
func readSpans(path string, spans [][2]int64, indexes []int) ([]json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	lines := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		line := make([]byte, spans[index][1])
		if _, err := file.ReadAt(line, spans[index][0]); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
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
	messages, cwd := claudeMessages(records, "default")
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
	if err := appendMessages(manager, messages); err != nil {
		return nil, err
	}
	return manager, appendRecords(manager, records)
}
