package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/partialjson"
	"github.com/OrdalieTech/orb/internal/uuidv7"
)

type transactionMigration struct {
	Writes        []json.RawMessage `json:"writes"`
	ImportedUsage ai.Usage          `json:"importedUsage"`
	NextSeq       int64             `json:"nextSeq"`
}

func transactionObject(pairs ...any) json.RawMessage {
	members := make([]harnessJSONMember, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		if raw, ok := pairs[i+1].(json.RawMessage); ok && len(raw) == 0 {
			continue
		}
		members = append(members, harnessJSONMember{name: pairs[i].(string), value: mustHarnessJSON(pairs[i+1])})
	}
	result, _ := marshalHarnessMembers(members)
	normalized, _ := partialjson.StringifyStreamingJSON(string(result))
	return normalized
}
func transactionTimestamp(fields map[string]json.RawMessage) int64 {
	stamp, _ := time.Parse(time.RFC3339Nano, transactionString(fields, "timestamp"))
	return stamp.UnixMilli()
}

//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func normalizeTransactionLegacyV3(lines [][]byte, idGenerator func(string, int64) (string, error)) (transactionMigration, error) {
	result := transactionMigration{Writes: []json.RawMessage{}}
	entries := make([]map[string]json.RawMessage, 0, len(lines))
	byID := map[string]map[string]json.RawMessage{}
	reminted := map[string]string{}
	retained := func(kind string) bool {
		return kind == "message" || kind == "custom" || kind == "custom_message" || kind == "compaction" || kind == "branch_summary"
	}
	for index, line := range lines {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(line, &entry); err != nil {
			return result, fmt.Errorf("Invalid legacy v3 JSONL record at line %d: not valid JSON", index+2)
		}
		kind := transactionString(entry, "type")
		if !retained(kind) && kind != "model_change" && kind != "thinking_level_change" && kind != "active_tools_change" && kind != "session_info" && kind != "label" {
			return result, fmt.Errorf("Unsupported legacy v3 record type at line %d: %s", index+2, kind)
		}
		id := transactionString(entry, "id")
		if byID[id] != nil {
			return result, fmt.Errorf("Duplicate legacy v3 entry id: %s", id)
		}
		byID[id] = entry
		entries = append(entries, entry)
		if retained(kind) {
			minted, err := idGenerator(id, transactionTimestamp(entry))
			if err != nil {
				return result, err
			}
			reminted[id] = minted
		}
	}
	resolve := func(reference json.RawMessage) (*string, error) {
		seen := map[string]bool{}
		for !bytes.Equal(reference, []byte("null")) {
			id := transactionString(map[string]json.RawMessage{"id": reference}, "id")
			if minted, ok := reminted[id]; ok {
				return &minted, nil
			}
			if seen[id] {
				return nil, fmt.Errorf("Cycle in legacy v3 parent chain at entry: %s", id)
			}
			seen[id] = true
			entry := byID[id]
			if entry == nil {
				return nil, fmt.Errorf("Missing legacy v3 entry reference: %s", id)
			}
			reference = entry["parentId"]
		}
		return nil, nil
	}
	fromID := func(entry map[string]json.RawMessage) (*string, error) {
		if transactionString(entry, "fromId") == "root" {
			return nil, nil
		}
		return resolve(entry["fromId"])
	}
	customMessage := func(entry map[string]json.RawMessage) json.RawMessage {
		return transactionObject("role", "custom", "customType", entry["customType"], "content", entry["content"], "details", entry["details"], "display", entry["display"], "timestamp", transactionTimestamp(entry))
	}
	project := func(entry map[string]json.RawMessage) (json.RawMessage, error) {
		switch transactionString(entry, "type") {
		case "message":
			return entry["message"], nil
		case "custom_message":
			return customMessage(entry), nil
		case "compaction":
			return transactionObject("role", "compactionSummary", "summary", entry["summary"], "tokensBefore", entry["tokensBefore"], "timestamp", transactionTimestamp(entry)), nil
		case "branch_summary":
			if transactionString(entry, "summary") == "" {
				return nil, nil
			}
			id, err := fromID(entry)
			if err != nil {
				return nil, err
			}
			return transactionObject("role", "branchSummary", "summary", entry["summary"], "fromId", id, "timestamp", transactionTimestamp(entry)), nil
		}
		return nil, nil
	}
	for _, entry := range entries {
		kind := transactionString(entry, "type")
		var usageRaw json.RawMessage
		switch kind {
		case "message":
			message := transactionFields(entry["message"])
			role := transactionString(message, "role")
			if role == "assistant" || role == "toolResult" {
				usageRaw = message["usage"]
			}
		case "compaction", "branch_summary":
			usageRaw = entry["usage"]
		}
		if len(usageRaw) > 0 {
			var usage ai.Usage
			_ = json.Unmarshal(usageRaw, &usage)
			result.ImportedUsage = addTransactionUsage(result.ImportedUsage, usage)
		}
		if !retained(kind) {
			continue
		}
		parent, err := resolve(entry["parentId"])
		if err != nil {
			return result, err
		}
		pairs := []any{"kind", "entry", "id", reminted[transactionString(entry, "id")], "parentId", parent, "seq", len(result.Writes) + 1, "timestamp", transactionTimestamp(entry)}
		switch kind {
		case "message":
			pairs = append(pairs, "type", "message", "message", entry["message"])
		case "custom_message":
			pairs = append(pairs, "type", "message", "message", customMessage(entry))
		case "custom":
			pairs = append(pairs, "type", "custom", "customType", entry["customType"], "data", entry["data"])
		case "branch_summary":
			id, err := fromID(entry)
			if err != nil {
				return result, err
			}
			hook := entry["fromHook"]
			if len(hook) == 0 || bytes.Equal(hook, []byte("null")) {
				hook = json.RawMessage("false")
			}
			pairs = append(pairs, "type", kind, "fromId", id, "summary", entry["summary"], "details", entry["details"], "usage", entry["usage"], "fromHook", hook)
		case "compaction":
			tail := []map[string]json.RawMessage{}
			reference := entry["parentId"]
			seen := map[string]bool{}
			found := false
			for !bytes.Equal(reference, []byte("null")) {
				id, _ := v4String(reference)
				if seen[id] {
					return result, fmt.Errorf("Cycle in legacy v3 parent chain at entry: %s", id)
				}
				seen[id] = true
				node := byID[id]
				if node == nil {
					return result, fmt.Errorf("Missing legacy v3 parent entry: %s", id)
				}
				tail = append(tail, node)
				if id == transactionString(entry, "firstKeptEntryId") {
					found = true
					break
				}
				reference = node["parentId"]
			}
			if !found {
				return result, fmt.Errorf("Legacy v3 compaction %s firstKeptEntryId is not on its parent branch: %s", transactionString(entry, "id"), transactionString(entry, "firstKeptEntryId"))
			}
			messages := []json.RawMessage{}
			for i := len(tail) - 1; i >= 0; i-- {
				message, err := project(tail[i])
				if err != nil {
					return result, err
				}
				if len(message) > 0 {
					messages = append(messages, message)
				}
			}
			hook := entry["fromHook"]
			if len(hook) == 0 || bytes.Equal(hook, []byte("null")) {
				hook = json.RawMessage("false")
			}
			pairs = append(pairs, "type", kind, "summary", entry["summary"], "retainedTail", messages, "tokensBefore", entry["tokensBefore"], "details", entry["details"], "usage", entry["usage"], "fromHook", hook)
		}
		result.Writes = append(result.Writes, transactionObject(pairs...))
	}
	store := func(namespace, key string, value any) {
		result.Writes = append(result.Writes, transactionObject("kind", "value", "op", "set", "seq", len(result.Writes)+1, "namespace", namespace, "key", key, "value", value))
	}
	var latestName json.RawMessage
	labels := map[string]string{}
	labelOrder := []string{}
	for _, entry := range entries {
		switch transactionString(entry, "type") {
		case "session_info":
			latestName = entry["name"]
		case "label":
			id, err := resolve(entry["targetId"])
			if err != nil {
				return result, err
			}
			if id == nil {
				continue
			}
			label := transactionString(entry, "label")
			if label == "" {
				delete(labels, *id)
				for i, key := range labelOrder {
					if key == *id {
						labelOrder = append(labelOrder[:i], labelOrder[i+1:]...)
						break
					}
				}
			} else {
				if _, ok := labels[*id]; !ok {
					labelOrder = append(labelOrder, *id)
				}
				labels[*id] = label
			}
		}
	}
	if name, _ := v4String(latestName); name != "" {
		store("pi.session.name", "", latestName)
	}
	for _, id := range labelOrder {
		store("pi.entry.label", id, labels[id])
	}
	selected := json.RawMessage("null")
	if len(entries) > 0 {
		selected = entries[len(entries)-1]["id"]
	}
	tip, err := resolve(selected)
	if err != nil {
		return result, err
	}
	store("pi.branch.tip", "main", tip)
	var model, thinking, tools json.RawMessage
	sawModel, sawThinking, sawTools := false, false, false
	seen := map[string]bool{}
	for !bytes.Equal(selected, []byte("null")) && (!sawModel || !sawThinking || !sawTools) {
		id, _ := v4String(selected)
		if seen[id] {
			return result, fmt.Errorf("Cycle in legacy v3 parent chain at entry: %s", id)
		}
		seen[id] = true
		entry := byID[id]
		if entry == nil {
			return result, fmt.Errorf("Missing legacy v3 entry reference: %s", id)
		}
		switch transactionString(entry, "type") {
		case "model_change":
			if !sawModel {
				sawModel = true
				if transactionString(entry, "provider") != "" && transactionString(entry, "modelId") != "" {
					model = transactionObject("provider", entry["provider"], "modelId", entry["modelId"])
				}
			}
		case "thinking_level_change":
			if !sawThinking {
				sawThinking = true
				switch transactionString(entry, "thinkingLevel") {
				case "off", "minimal", "low", "medium", "high", "xhigh", "max":
					thinking = entry["thinkingLevel"]
				}
			}
		case "active_tools_change":
			if !sawTools {
				sawTools = true
				var names []string
				if json.Unmarshal(entry["activeToolNames"], &names) == nil && names != nil {
					tools = entry["activeToolNames"]
				}
			}
		}
		selected = entry["parentId"]
	}
	if model != nil && thinking != nil {
		if tools == nil {
			tools = json.RawMessage("[]")
		}
		store("pi.lane.config", "main", transactionObject("model", model, "thinkingLevel", thinking, "activeToolNames", tools))
		store("pi.lane.state", "main", transactionObject("currentOperationId", nil, "lastOperationId", nil, "inbox", []any{}))
	}
	result.NextSeq = int64(len(result.Writes) + 1)
	return result, nil
}
func mintTransactionLegacyID(_ string, timestamp int64) (string, error) {
	return uuidv7.Generate(time.UnixMilli(timestamp))
}
