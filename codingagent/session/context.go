package session

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/OrdalieTech/pigo/ai"
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
	path := buildSessionPath(entries, leafID)
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
		if foundFirstKept {
			contextEntries = append(contextEntries, entry)
		}
	}
	contextEntries = append(contextEntries, path[compactionIndex+1:]...)
	return contextEntries
}

func BuildSessionContext(entries []SessionEntry, leafID *string) SessionContext {
	path := buildSessionPath(entries, leafID)
	context := SessionContext{ThinkingLevel: "off", Messages: []json.RawMessage{}}
	for _, entry := range path {
		switch entry.Type {
		case "thinking_level_change":
			context.ThinkingLevel = entry.ThinkingLevel
		case "model_change":
			context.Model = &SessionModel{Provider: entry.Provider, ModelID: entry.ModelID}
		case "active_tools_change":
			context.ActiveToolNames = cloneStringSlice(entry.ActiveToolNames)
		case "message":
			var header struct {
				Role     string `json:"role"`
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			if json.Unmarshal(entry.Message, &header) == nil && header.Role == "assistant" {
				context.Model = &SessionModel{Provider: header.Provider, ModelID: header.Model}
			}
		}
	}
	for _, entry := range BuildContextEntries(entries, leafID) {
		context.Messages = append(context.Messages, entryContextMessages(entry)...)
	}
	return context
}

func entryContextMessages(entry SessionEntry) []json.RawMessage {
	switch entry.Type {
	case "message":
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
		return []json.RawMessage{encoded}
	default:
		return nil
	}
}

func normalizeMessageContent(message json.RawMessage) json.RawMessage {
	var header struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &header) != nil || (header.Role != "user" && header.Role != "assistant" && header.Role != "toolResult") {
		return cloneRaw(message)
	}
	if len(header.Content) > 0 && !bytes.Equal(bytes.TrimSpace(header.Content), []byte("null")) {
		return cloneRaw(message)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil {
		return cloneRaw(message)
	}
	object["content"] = json.RawMessage("[]")
	encoded, err := ai.Marshal(object)
	if err != nil {
		return cloneRaw(message)
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
	if manager.harnessStorage != nil {
		branch := manager.GetBranch()
		leaf := manager.GetLeafID()
		return BuildContextEntries(branch, leaf)
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return BuildContextEntries(manager.entriesLocked(), manager.leafID)
}

func (manager *SessionManager) BuildSessionContext() SessionContext {
	if manager.harnessStorage != nil {
		branch := manager.GetBranch()
		leaf := manager.GetLeafID()
		return BuildSessionContext(branch, leaf)
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return BuildSessionContext(manager.entriesLocked(), manager.leafID)
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
