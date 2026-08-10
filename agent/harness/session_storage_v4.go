package harness

import (
	"bytes"
	"encoding/json"
	"time"
)

// This bridge lets the v3-shaped SessionStorage surface consumed by the
// coding-agent session manager operate on v4-format JSONL sessions: v4
// mutations project into the v3 tree view, and v3-shaped writes append v4
// mutation lines.

func isV4HarnessHeaderLine(line []byte) bool {
	var probe struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(bytes.TrimSpace(line), &probe) == nil && probe.Kind == "header"
}

func rehydrateV4JSONLSession(content []byte, filePath string, appendLine func([]byte) error) (*JSONLSessionStorage, error) {
	rawLines := bytes.Split(content, []byte{'\n'})
	if len(rawLines) > 0 && len(bytes.TrimSpace(rawLines[len(rawLines)-1])) == 0 {
		rawLines = rawLines[:len(rawLines)-1]
	}
	header, err := ParseSessionV4Header(rawLines[0], filePath)
	if err != nil {
		return nil, err
	}
	v4state := newSessionV4State()
	for index := 1; index < len(rawLines); index++ {
		mutation, parseErr := ParseSessionV4Mutation(rawLines[index], filePath, index+1)
		if parseErr != nil {
			return nil, parseErr
		}
		lineNumber := index + 1
		if err := v4state.applyMutation(mutation, func(message string) error {
			return invalidV4File(filePath, lineNumber, "%s", message)
		}); err != nil {
			return nil, err
		}
	}
	metadata := SessionMetadata{
		ID: header.ID, CreatedAt: formatHarnessTimestamp(time.UnixMilli(header.CreatedAt)),
		CWD: header.CWD, Path: filePath, Metadata: cloneHarnessRaw(header.Metadata),
	}
	projected := make([]SessionTreeEntry, len(v4state.entries))
	for index := range v4state.entries {
		projected[index] = sessionTreeEntryFromV4(v4state.entries[index])
	}
	state, err := newSessionStorageState(metadata, projected, false)
	if err != nil {
		return nil, err
	}
	state.leafID = cloneHarnessString(v4state.lanes["main"])
	state.labels = make(map[string]string)
	for id, label := range v4state.labels {
		if trimmed := trimHarnessJSSpace(label); trimmed != "" {
			state.labels[id] = trimmed
		}
	}
	return &JSONLSessionStorage{
		state: state, version: 4, header: append([]byte(nil), rawLines[0]...),
		content: append([]byte(nil), content...), append: appendLine, v4: v4state,
	}, nil
}

func sessionTreeEntryFromV4(entry SessionV4Entry) SessionTreeEntry {
	return SessionTreeEntry{
		Type: entry.Type, ID: entry.ID, ParentID: cloneHarnessString(entry.ParentID),
		Timestamp: formatHarnessTimestamp(time.UnixMilli(entry.Timestamp)),
		Message:   cloneHarnessRaw(entry.Message), ThinkingLevel: entry.ThinkingLevel,
		Provider: entry.Provider, ModelID: entry.ModelID, ActiveToolNames: cloneHarnessStrings(entry.ActiveToolNames),
		Summary: entry.Summary, FromID: entry.FromID, TokensBefore: entry.TokensBefore,
		RetainedTail: cloneHarnessRawMessages(entry.RetainedTail), CustomType: entry.CustomType,
		Data: cloneHarnessRaw(entry.Data), Details: cloneHarnessRaw(entry.Details),
	}
}

func harnessTimestampMS(timestamp string) int64 {
	if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		return parsed.UnixMilli()
	}
	return sessionV4NowMS(nil)
}

// v4PayloadFromTreeEntry serializes a v3-shaped entry as a provisioned v4
// entry payload using upstream's construction member order.
func v4PayloadFromTreeEntry(entry SessionTreeEntry) ([]byte, error) {
	members := []harnessJSONMember{
		harnessStringMember("type", entry.Type),
		harnessStringMember("id", entry.ID),
	}
	switch entry.Type {
	case "message":
		members = append(members, harnessRawMember("message", entry.Message))
	case "thinking_level_change":
		members = append(members, harnessStringMember("thinkingLevel", entry.ThinkingLevel))
	case "model_change":
		members = append(members, harnessStringMember("provider", entry.Provider), harnessStringMember("modelId", entry.ModelID))
	case "active_tools_change":
		members = append(members, harnessJSONMember{name: "activeToolNames", value: mustHarnessJSON(cloneHarnessStrings(entry.ActiveToolNames))})
	case "compaction":
		tail := entry.RetainedTail
		if tail == nil {
			tail = []json.RawMessage{}
		}
		members = append(members,
			harnessStringMember("summary", entry.Summary),
			harnessJSONMember{name: "retainedTail", value: mustHarnessJSON(tail)},
			harnessJSONMember{name: "tokensBefore", value: mustHarnessJSON(entry.TokensBefore)},
		)
		if len(entry.Details) != 0 {
			members = append(members, harnessRawMember("details", entry.Details))
		}
		if entry.Usage != nil {
			members = append(members, harnessJSONMember{name: "usage", value: mustHarnessJSON(entry.Usage)})
		}
	case "branch_summary":
		members = append(members, harnessStringMember("fromId", entry.FromID), harnessStringMember("summary", entry.Summary))
		if len(entry.Details) != 0 {
			members = append(members, harnessRawMember("details", entry.Details))
		}
		if entry.Usage != nil {
			members = append(members, harnessJSONMember{name: "usage", value: mustHarnessJSON(entry.Usage)})
		}
	case "custom":
		members = append(members, harnessStringMember("customType", entry.CustomType))
		if len(entry.Data) != 0 {
			members = append(members, harnessRawMember("data", entry.Data))
		}
	default:
		return nil, newSessionError(SessionErrorInvalidEntry, "Entry type %s is not a v4 session entry", entry.Type)
	}
	return marshalHarnessMembers(members)
}

// appendV4MutationsLocked encodes, persists, and applies mutations while
// keeping the v3 projection in sync.
func (storage *JSONLSessionStorage) appendV4MutationsLocked(mutations ...SessionV4Mutation) error {
	for _, mutation := range mutations {
		encoded, err := MarshalSessionV4Mutation(mutation)
		if err != nil {
			return err
		}
		line := append(encoded, '\n')
		if storage.append != nil {
			if err := storage.append(line); err != nil {
				return newSessionError(SessionErrorStorage, "Failed to append session %s: %v", storage.state.metadata.Path, err)
			}
		}
		if err := storage.v4.applyMutation(mutation, nil); err != nil {
			return err
		}
		storage.content = append(storage.content, line...)
		switch mutation.Kind {
		case "entry":
			storage.state.append(sessionTreeEntryFromV4(*mutation.Entry))
		case "lane":
			if mutation.Lane == "main" {
				storage.state.leafID = cloneHarnessString(mutation.LeafID)
			}
		case "fact":
			if mutation.Fact == "label" {
				trimmed := ""
				if mutation.Label != nil {
					trimmed = trimHarnessJSSpace(*mutation.Label)
				}
				if trimmed == "" {
					delete(storage.state.labels, mutation.TargetID)
				} else {
					storage.state.labels[mutation.TargetID] = trimmed
				}
			}
		}
	}
	return nil
}

func (storage *JSONLSessionStorage) appendV4EntryLocked(entry SessionTreeEntry) error {
	switch entry.Type {
	case "session_info":
		return storage.appendV4MutationsLocked(SessionV4Mutation{
			Kind: "fact", Seq: storage.v4.nextSequence(), Fact: "name", Name: entry.Name,
		})
	case "label":
		targetID := ""
		if entry.TargetID != nil {
			targetID = *entry.TargetID
		}
		mutation, err := stageV4SetLabel(storage.v4, targetID, entry.Label)
		if err != nil {
			return err
		}
		return storage.appendV4MutationsLocked(mutation)
	case "leaf":
		return storage.setV4LeafLocked(entry.TargetID)
	}
	payload, err := v4PayloadFromTreeEntry(entry)
	if err != nil {
		return err
	}
	if err := storage.v4.validateUnusedID(entry.ID); err != nil {
		return err
	}
	timestamp := harnessTimestampMS(entry.Timestamp)
	if equalV4NullableID(entry.ParentID, storage.v4.lanes["main"]) {
		lane := "main"
		mutation, err := buildV4EntryMutation(storage.v4, payload, &lane, cloneHarnessString(entry.ParentID), timestamp)
		if err != nil {
			return err
		}
		return storage.appendV4MutationsLocked(mutation)
	}
	mutation, err := buildV4EntryMutation(storage.v4, payload, nil, cloneHarnessString(entry.ParentID), timestamp)
	if err != nil {
		return err
	}
	if err := storage.appendV4MutationsLocked(mutation); err != nil {
		return err
	}
	entryID := entry.ID
	return storage.appendV4MutationsLocked(SessionV4Mutation{
		Kind: "lane", Seq: storage.v4.nextSequence(), Lane: "main", LeafID: &entryID,
	})
}

func (storage *JSONLSessionStorage) setV4LeafLocked(leafID *string) error {
	mutation, err := stageV4MoveLane(storage.v4, "main", leafID)
	if err != nil {
		return err
	}
	return storage.appendV4MutationsLocked(mutation)
}
