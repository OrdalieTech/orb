package session

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

func GetLatestCompactionEntry(entries []SessionEntry) *SessionEntry {
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Type == "compaction" {
			entry := entries[index]
			return &entry
		}
	}
	return nil
}

func buildSessionPath(entries []SessionEntry, leafID *string) []SessionEntry {
	if leafID == nil {
		return nil
	}
	index := make(map[string]*SessionEntry, len(entries))
	for entryIndex := range entries {
		entry := &entries[entryIndex]
		index[entry.ID] = entry
	}
	leaf := index[*leafID]
	if leaf == nil && len(entries) > 0 {
		leaf = &entries[len(entries)-1]
	}
	var path []SessionEntry
	seen := make(map[string]struct{})
	for current := leaf; current != nil; {
		if _, exists := seen[current.ID]; exists {
			break
		}
		seen[current.ID] = struct{}{}
		path = append(path, *cloneEntry(current))
		if current.ParentID == nil {
			break
		}
		current = index[*current.ParentID]
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

func BuildContextEntries(entries []SessionEntry, leafID *string) []SessionEntry {
	return contextEntries(buildSessionPath(entries, leafID))
}

func contextEntries(path []SessionEntry) []SessionEntry {
	var compaction *SessionEntry
	for index := range path {
		if path[index].Type == "compaction" {
			compaction = &path[index]
		}
	}
	if compaction == nil {
		return path
	}
	compactionIndex := -1
	for index := range path {
		if path[index].ID == compaction.ID {
			compactionIndex = index
			break
		}
	}
	if compactionIndex < 0 {
		return path
	}
	contextEntries := []SessionEntry{*cloneEntry(compaction)}
	foundFirstKept := false
	for index := 0; index < compactionIndex; index++ {
		entry := path[index]
		if entry.ID == compaction.FirstKeptEntryID {
			foundFirstKept = true
		}
		if foundFirstKept && !isSystemMessageEntry(entry) {
			contextEntries = append(contextEntries, entry)
		}
	}
	contextEntries = append(contextEntries, path[compactionIndex+1:]...)
	return contextEntries
}

func BuildSessionContext(entries []SessionEntry, leafID *string) SessionContext {
	projection := buildContextProjection(entries, leafID)
	return projection.context
}

// contextProjection is BuildSessionContext for one leaf with every message
// decoded once. An entry appended on that leaf extends it in place; any other
// change rebuilds it, so a turn costs the new entries, not the history.
type contextProjection struct {
	context SessionContext
	// decoded holds context.Messages through ai.UnmarshalMessage, the raw
	// message for other roles. It is shared with callers and never modified.
	decoded   []any
	system    ai.MessageList
	pathTools []string
	// compaction is the timestamp of the path's latest compaction, if any.
	compaction    string
	hasCompaction bool
	edits         bool
	leaf          *string
	// records and generation pin the manager state the projection covers.
	records    int
	generation uint64
	valid      bool
}

func buildContextProjection(entries []SessionEntry, leafID *string) contextProjection {
	path := buildSessionPath(entries, leafID)
	projection := contextProjection{context: SessionContext{ThinkingLevel: "off", Messages: []json.RawMessage{}}, leaf: clonePointer(leafID)}
	for index := range path {
		projection.addFields(&path[index])
	}
	kept := contextEntries(path)
	edits := map[string]json.RawMessage{}
	for _, entry := range kept {
		if entry.Type == "context_edit" {
			edits[entry.TargetID] = entry.Replacement
		}
	}
	for index, entry := range kept {
		// An older compaction retained inside the newest kept range contributes
		// nothing; only the newest one, at index zero, adds its summary.
		if entry.Type == "compaction" && index > 0 {
			continue
		}
		messages := entryContextMessages(entry)
		if replacement, edited := edits[entry.ID]; edited {
			messages = applyContextEdit(messages, replacement)
		}
		projection.addMessages(&entry, messages)
	}
	projection.settleTools()
	return projection
}

// extend appends entry, whose parent is the projection's leaf. It reports
// false for entries that change earlier context, which need a rebuild.
func (projection *contextProjection) extend(entry *SessionEntry) bool {
	switch entry.Type {
	case "compaction", "branch_summary", "context_edit", "leaf":
		return false
	}
	systems := len(projection.system)
	projection.addFields(entry)
	projection.addMessages(entry, entryContextMessages(*entry))
	if entry.Type == "active_tools_change" || len(projection.system) != systems {
		projection.settleTools()
	}
	projection.leaf = &entry.ID
	return true
}

func (projection *contextProjection) addFields(entry *SessionEntry) {
	switch entry.Type {
	case "thinking_level_change":
		projection.context.ThinkingLevel = entry.ThinkingLevel
	case "model_change":
		projection.context.Model = &SessionModel{Provider: entry.Provider, ModelID: entry.ModelID}
	case "active_tools_change":
		projection.pathTools = slices.Clone(entry.ActiveToolNames)
	case "compaction":
		projection.compaction, projection.hasCompaction = entry.Timestamp, true
	case "context_edit":
		projection.edits = true
	case "message":
		// An appended entry carries its decoded message; a read one is
		// decoded for its header alone.
		if entry.decoded != nil {
			if assistant, ok := entry.decoded.(*ai.AssistantMessage); ok {
				projection.context.Model = &SessionModel{Provider: string(assistant.Provider), ModelID: assistant.Model}
			}
			return
		}
		var header struct {
			Role     string `json:"role"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		}
		if json.Unmarshal(entry.Message, &header) == nil && header.Role == "assistant" {
			projection.context.Model = &SessionModel{Provider: header.Provider, ModelID: header.Model}
		}
	}
}

// addMessages adds entry's context messages; one that is the entry's own
// message reuses the entry's decode.
func (projection *contextProjection) addMessages(entry *SessionEntry, messages []json.RawMessage) {
	for _, raw := range messages {
		projection.context.Messages = append(projection.context.Messages, raw)
		var decoded any
		if len(raw) > 0 {
			decode := ai.UnmarshalMessage
			if len(raw) == len(entry.Message) && &raw[0] == &entry.Message[0] {
				decode = func([]byte) (ai.Message, error) { return entry.decodedMessage() }
			}
			if message, err := decode(raw); err == nil {
				decoded = message
				if system, ok := message.(*ai.SystemMessage); ok {
					projection.system = append(projection.system, system)
				}
			} else {
				decoded = raw
			}
		}
		projection.decoded = append(projection.decoded, decoded)
	}
}

// settleTools applies the path's tool selection, which the transcript's
// current system message overrides when there is one.
func (projection *contextProjection) settleTools() {
	names := slices.Clone(projection.pathTools)
	if current := ai.CurrentSystemMessage(projection.system); current != nil {
		names = names[:0]
		for _, tool := range current.ToolsAdded {
			names = append(names, tool.Name)
		}
	}
	projection.context.ActiveToolNames = names
}

func entryContextMessages(entry SessionEntry) []json.RawMessage {
	switch entry.Type {
	case "message":
		if hasMessageContent(entry.decoded) {
			return []json.RawMessage{entry.Message}
		}
		return []json.RawMessage{normalizeMessageContent(entry.Message)}
	case "custom_message":
		content := entry.Content
		if len(content) == 0 || bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
			content = json.RawMessage("[]")
		}
		message := struct {
			Role       json.RawMessage `json:"role"`
			CustomType json.RawMessage `json:"customType"`
			Content    json.RawMessage `json:"content"`
			Display    bool            `json:"display"`
			Details    json.RawMessage `json:"details,omitempty"`
			Timestamp  int64           `json:"timestamp"`
		}{mustRawString("custom"), mustRawString(entry.CustomType), content, entry.Display, entry.Details, timestampMillis(entry.Timestamp)}
		encoded, _ := ai.Marshal(message)
		return []json.RawMessage{encoded}
	case "branch_summary":
		if entry.Summary == "" {
			return nil
		}
		message := struct {
			Role      json.RawMessage `json:"role"`
			Summary   json.RawMessage `json:"summary"`
			FromID    json.RawMessage `json:"fromId"`
			Timestamp int64           `json:"timestamp"`
		}{mustRawString("branchSummary"), mustRawString(entry.Summary), mustRawString(entry.FromID), timestampMillis(entry.Timestamp)}
		encoded, _ := ai.Marshal(message)
		return []json.RawMessage{encoded}
	case "compaction":
		message := struct {
			Role         json.RawMessage `json:"role"`
			Summary      json.RawMessage `json:"summary"`
			TokensBefore float64         `json:"tokensBefore"`
			Timestamp    int64           `json:"timestamp"`
		}{mustRawString("compactionSummary"), mustRawString(entry.Summary), entry.TokensBefore, timestampMillis(entry.Timestamp)}
		encoded, _ := ai.Marshal(message)
		if len(entry.SystemMessage) > 0 {
			return []json.RawMessage{cloneRaw(entry.SystemMessage), encoded}
		}
		return []json.RawMessage{encoded}
	default:
		return nil
	}
}

// ApplyContextEdits applies each entry's latest context edit on the branch to
// its message or custom content, and reports the entries an edit omitted.
func ApplyContextEdits(entries []SessionEntry) ([]SessionEntry, map[string]bool) {
	edits := map[string]json.RawMessage{}
	for _, entry := range entries {
		if entry.Type == "context_edit" {
			edits[entry.TargetID] = entry.Replacement
		}
	}
	omitted := map[string]bool{}
	edited := make([]SessionEntry, len(entries))
	for index, entry := range entries {
		edited[index] = entry
		replacement, ok := edits[entry.ID]
		if !ok || entry.Type != "message" && entry.Type != "custom_message" {
			continue
		}
		messages := applyContextEdit(entryContextMessages(entry), replacement)
		switch {
		case len(messages) == 0:
			omitted[entry.ID] = true
		case entry.Type == "message":
			edited[index].Message = messages[0]
		default:
			var custom struct {
				Content json.RawMessage `json:"content"`
			}
			_ = json.Unmarshal(messages[0], &custom)
			edited[index].Content = custom.Content
		}
	}
	return edited, omitted
}

// applyContextEdit applies a context_edit replacement to an entry's model
// messages: null omits them, {"content": ...} replaces only their content (a
// string becomes one text block for assistant and tool-result messages).
func applyContextEdit(messages []json.RawMessage, replacement json.RawMessage) []json.RawMessage {
	var edit struct {
		Content json.RawMessage `json:"content"`
	}
	if len(replacement) == 0 || bytes.Equal(bytes.TrimSpace(replacement), []byte("null")) || json.Unmarshal(replacement, &edit) != nil {
		return nil
	}
	edited := make([]json.RawMessage, 0, len(messages))
	for _, raw := range messages {
		object := parseObject(raw)
		role, _ := stringMember(object, "role")
		if object == nil || role != "user" && role != "assistant" && role != "toolResult" && role != "custom" {
			edited = append(edited, raw)
			continue
		}
		content := edit.Content
		if text, isString := decodeString(content); isString && (role == "assistant" || role == "toolResult") {
			content, _ = ai.Marshal([]map[string]string{{"type": "text", "text": text}})
		}
		object.Set("content", content)
		encoded, _ := object.MarshalJSON()
		edited = append(edited, encoded)
	}
	return edited
}

func isSystemMessageEntry(entry SessionEntry) bool {
	return entry.Type == "message" && jsonwire.MessageRole(entry.Message) == "system"
}

// normalizeMessageContent gives a message without content an empty one. An
// unchanged message keeps sharing its entry's bytes, which are never modified.
// hasMessageContent reports a decoded message normalizeMessageContent would
// leave alone: one with text or blocks, which only content present and not
// null decodes to, or one that is not a user, assistant or tool result
// message. Null decodes to empty content, so empty content is checked.
func hasMessageContent(message ai.Message) bool {
	switch typed := message.(type) {
	case nil:
		return false
	case *ai.UserMessage:
		return typed.Content.Text != nil || len(typed.Content.Blocks) > 0
	case *ai.AssistantMessage:
		return len(typed.Content) > 0
	case *ai.ToolResultMessage:
		return len(typed.Content) > 0
	default:
		return true
	}
}

func normalizeMessageContent(message json.RawMessage) json.RawMessage {
	var header struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &header) != nil || (header.Role != "user" && header.Role != "assistant" && header.Role != "toolResult") {
		return message
	}
	if len(header.Content) > 0 && !bytes.Equal(bytes.TrimSpace(header.Content), []byte("null")) {
		return message
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil {
		return message
	}
	object["content"] = json.RawMessage("[]")
	encoded, err := ai.Marshal(object)
	if err != nil {
		return message
	}
	return encoded
}

func timestampMillis(timestamp string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func (manager *SessionManager) BuildContextEntries() []SessionEntry {
	unlock, fresh := manager.lockIndex()
	defer unlock()
	switch {
	case manager.harnessStorage == nil:
		return BuildContextEntries(manager.entriesLocked(), manager.leafID)
	case !fresh:
		return nil
	}
	return BuildContextEntries(manager.harnessBranchLocked(manager.leafID), manager.leafID)
}

func (manager *SessionManager) BuildSessionContext() SessionContext {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	context := manager.projectionLocked().context
	messages := make([]json.RawMessage, len(context.Messages))
	for index, raw := range context.Messages {
		messages[index] = cloneRaw(raw)
	}
	context.Messages = messages
	context.ActiveToolNames = slices.Clone(context.ActiveToolNames)
	if context.Model != nil {
		model := *context.Model
		context.Model = &model
	}
	return context
}

// ContextMessages returns BuildSessionContext's messages decoded: standard
// roles as ai messages, other roles as their raw JSON. The messages are shared
// with the session and must not be modified.
func (manager *SessionManager) ContextMessages() []any {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	decoded := manager.projectionLocked().decoded
	return append(make([]any, 0, len(decoded)), decoded...)
}

// HasContextEdits reports whether the current branch holds a context edit.
func (manager *SessionManager) HasContextEdits() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.projectionLocked().edits
}

// projectionLocked brings the cached context projection to the current leaf,
// extending it with the entries appended since when they continue its leaf.
func (manager *SessionManager) projectionLocked() *contextProjection {
	projection := &manager.projection
	if manager.refreshHarnessLocked() != nil {
		*projection = contextProjection{context: SessionContext{ThinkingLevel: "off", Messages: []json.RawMessage{}}}
		return projection
	}
	if projection.valid && projection.generation == manager.generation && projection.records <= len(manager.fileEntries) {
		for _, record := range manager.fileEntries[projection.records:] {
			if record == nil || record.Entry == nil || record.Type == "session" ||
				!sameID(record.Entry.ParentID, projection.leaf) || !projection.extend(record.Entry) {
				projection.valid = false
				break
			}
		}
		if projection.valid && sameID(projection.leaf, manager.leafID) {
			projection.records = len(manager.fileEntries)
			return projection
		}
	}
	if manager.harnessStorage != nil {
		*projection = buildContextProjection(manager.harnessBranchLocked(manager.leafID), manager.leafID)
	} else {
		*projection = buildContextProjection(manager.entriesLocked(), manager.leafID)
	}
	projection.valid, projection.generation, projection.records = true, manager.generation, len(manager.fileEntries)
	return projection
}

func sameID(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// harnessBranchLocked is the branch ending at leaf in the index
// refreshHarnessLocked keeps in step with the store: a missing ancestor
// empties the branch.
func (manager *SessionManager) harnessBranchLocked(leaf *string) []SessionEntry {
	path := []SessionEntry{}
	for id := leaf; id != nil && *id != ""; {
		entry := manager.byID[*id]
		if entry == nil || len(path) > len(manager.byID) {
			return []SessionEntry{}
		}
		path = append(path, *cloneEntry(entry))
		id = entry.ParentID
	}
	slices.Reverse(path)
	return path
}

func (manager *SessionManager) entriesLocked() []SessionEntry {
	entries := make([]SessionEntry, 0, len(manager.fileEntries)-1)
	for _, fileEntry := range manager.fileEntries {
		if fileEntry != nil && fileEntry.Entry != nil && fileEntry.Type != "session" {
			entries = append(entries, *cloneEntry(fileEntry.Entry))
		}
	}
	return entries
}
