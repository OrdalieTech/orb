package session

import (
	"encoding/json"
	"strings"
)

const CurrentVersion = 3

type SessionHeader struct {
	Type          string
	Version       *int
	ID            string
	Timestamp     string
	CWD           string
	ParentSession *string
	Metadata      json.RawMessage

	object *orderedObject
}

type SessionEntry struct {
	Type      string
	ID        string
	ParentID  *string
	Timestamp string

	Message          json.RawMessage
	ThinkingLevel    string
	Provider         string
	ModelID          string
	ActiveToolNames  []string
	Summary          string
	FirstKeptEntryID string
	TokensBefore     float64
	Details          json.RawMessage
	FromHook         *bool
	FromID           string
	CustomType       string
	Data             json.RawMessage
	Content          json.RawMessage
	Display          bool
	TargetID         string
	LeafTargetID     *string
	Label            *string
	Name             string

	object *orderedObject
}

// FileEntry is either a session header, an ordinary tree entry, or another
// valid JSON value. Raw valid values are retained so parsing remains as
// permissive as upstream.
type FileEntry struct {
	Type   string
	Header *SessionHeader
	Entry  *SessionEntry

	object *orderedObject
	raw    json.RawMessage
}

type SessionTreeNode struct {
	Entry          SessionEntry       `json:"entry"`
	Children       []*SessionTreeNode `json:"children"`
	Label          *string            `json:"label,omitempty"`
	LabelTimestamp *string            `json:"labelTimestamp,omitempty"`
}

func (entry *FileEntry) MarshalJSON() ([]byte, error) {
	if entry == nil {
		return []byte("null"), nil
	}
	if entry.object != nil {
		return entry.object.marshal()
	}
	if len(entry.raw) != 0 {
		return cloneRaw(entry.raw), nil
	}
	return []byte("null"), nil
}

func (header SessionHeader) MarshalJSON() ([]byte, error) {
	if header.object != nil {
		return header.object.marshal()
	}
	return newHeaderRecord(header).MarshalJSON()
}

func (entry SessionEntry) MarshalJSON() ([]byte, error) {
	if entry.object != nil {
		return entry.object.marshal()
	}
	return newEntryRecord(entry).MarshalJSON()
}

// MarshalJSONWithParent preserves the entry's original member order and
// unknown fields while replacing parentId, matching object spread followed by
// a parentId override in upstream JSONL branch export.
func (entry SessionEntry) MarshalJSONWithParent(parentID *string) ([]byte, error) {
	if entry.object == nil {
		entry.ParentID = cloneString(parentID)
		return entry.MarshalJSON()
	}
	object := &orderedObject{members: make([]jsonMember, len(entry.object.members))}
	for index, value := range entry.object.members {
		object.members[index] = member(value.name, value.value)
	}
	parent := rawNull()
	if parentID != nil {
		parent = mustRawString(*parentID)
	}
	object.set("parentId", parent)
	return object.marshal()
}

func (entry *FileEntry) Raw() ([]byte, error) {
	return entry.MarshalJSON()
}

func decodeFileEntry(object *orderedObject, raw json.RawMessage) *FileEntry {
	fileEntry := &FileEntry{object: object, raw: cloneRaw(raw)}
	if object == nil {
		return fileEntry
	}
	typeRaw, _ := object.get("type")
	fileEntry.Type, _ = decodeString(typeRaw)
	if fileEntry.Type == "session" {
		header := &SessionHeader{Type: "session", object: object}
		if value, ok := object.get("version"); ok {
			if version, valid := decodeInt(value); valid {
				converted := int(version)
				header.Version = &converted
			}
		}
		header.ID, _ = stringMember(object, "id")
		header.Timestamp, _ = stringMember(object, "timestamp")
		header.CWD, _ = stringMember(object, "cwd")
		if value, ok := object.get("parentSession"); ok {
			if parent, valid := decodeString(value); valid {
				header.ParentSession = &parent
			}
		}
		header.Metadata, _ = object.get("metadata")
		fileEntry.Header = header
		return fileEntry
	}

	entry := &SessionEntry{Type: fileEntry.Type, object: object}
	entry.ID, _ = stringMember(object, "id")
	if value, ok := object.get("parentId"); ok && string(value) != "null" {
		if parent, valid := decodeString(value); valid {
			entry.ParentID = &parent
		}
	}
	entry.Timestamp, _ = stringMember(object, "timestamp")
	entry.Message, _ = object.get("message")
	entry.ThinkingLevel, _ = stringMember(object, "thinkingLevel")
	entry.Provider, _ = stringMember(object, "provider")
	entry.ModelID, _ = stringMember(object, "modelId")
	if value, ok := object.get("activeToolNames"); ok {
		_ = json.Unmarshal(value, &entry.ActiveToolNames)
	}
	entry.Summary, _ = stringMember(object, "summary")
	entry.FirstKeptEntryID, _ = stringMember(object, "firstKeptEntryId")
	if value, ok := object.get("tokensBefore"); ok {
		entry.TokensBefore, _ = decodeNumber(value)
	}
	entry.Details, _ = object.get("details")
	if value, ok := object.get("fromHook"); ok {
		entry.FromHook, _ = decodeBool(value)
	}
	entry.FromID, _ = stringMember(object, "fromId")
	entry.CustomType, _ = stringMember(object, "customType")
	entry.Data, _ = object.get("data")
	entry.Content, _ = object.get("content")
	if value, ok := object.get("display"); ok {
		if display, valid := decodeBool(value); valid {
			entry.Display = *display
		}
	}
	entry.TargetID, _ = stringMember(object, "targetId")
	if entry.Type == "leaf" {
		if value, ok := object.get("targetId"); ok {
			if targetID, valid := decodeString(value); valid {
				entry.LeafTargetID = &targetID
			}
		}
	}
	if value, ok := object.get("label"); ok {
		if label, valid := decodeString(value); valid {
			entry.Label = &label
		}
	}
	entry.Name, _ = stringMember(object, "name")
	fileEntry.Entry = entry
	return fileEntry
}

func stringMember(object *orderedObject, name string) (string, bool) {
	raw, ok := object.get(name)
	if !ok {
		return "", false
	}
	return decodeString(raw)
}

func newHeaderRecord(header SessionHeader) *FileEntry {
	members := []jsonMember{
		member("type", mustRawString("session")),
	}
	if header.Version != nil {
		members = append(members, member("version", rawInt(int64(*header.Version))))
	}
	members = append(members,
		member("id", mustRawString(header.ID)),
		member("timestamp", mustRawString(header.Timestamp)),
		member("cwd", mustRawString(header.CWD)),
	)
	if header.ParentSession != nil {
		members = append(members, member("parentSession", mustRawString(*header.ParentSession)))
	}
	if header.Metadata != nil {
		members = append(members, member("metadata", header.Metadata))
	}
	object := newOrderedObject(members...)
	return decodeFileEntry(object, nil)
}

func newEntryRecord(entry SessionEntry) *FileEntry {
	parent := rawNull()
	if entry.ParentID != nil {
		parent = mustRawString(*entry.ParentID)
	}
	base := []jsonMember{
		member("type", mustRawString(entry.Type)),
		member("id", mustRawString(entry.ID)),
		member("parentId", parent),
		member("timestamp", mustRawString(entry.Timestamp)),
	}
	var members []jsonMember
	switch entry.Type {
	case "message":
		members = append(base, member("message", entry.Message))
	case "thinking_level_change":
		members = append(base, member("thinkingLevel", mustRawString(entry.ThinkingLevel)))
	case "model_change":
		members = append(base,
			member("provider", mustRawString(entry.Provider)),
			member("modelId", mustRawString(entry.ModelID)),
		)
	case "active_tools_change":
		members = append(base, member("activeToolNames", rawStringArray(entry.ActiveToolNames)))
	case "compaction":
		members = append(base,
			member("summary", mustRawString(entry.Summary)),
			member("firstKeptEntryId", mustRawString(entry.FirstKeptEntryID)),
			member("tokensBefore", rawNumber(entry.TokensBefore)),
		)
		if entry.Details != nil {
			members = append(members, member("details", entry.Details))
		}
		if entry.FromHook != nil {
			members = append(members, member("fromHook", rawBool(*entry.FromHook)))
		}
	case "branch_summary":
		members = append(base,
			member("fromId", mustRawString(entry.FromID)),
			member("summary", mustRawString(entry.Summary)),
		)
		if entry.Details != nil {
			members = append(members, member("details", entry.Details))
		}
		if entry.FromHook != nil {
			members = append(members, member("fromHook", rawBool(*entry.FromHook)))
		}
	case "custom":
		members = []jsonMember{
			member("type", mustRawString("custom")),
			member("customType", mustRawString(entry.CustomType)),
		}
		if entry.Data != nil {
			members = append(members, member("data", entry.Data))
		}
		members = append(members, base[1:]...)
	case "custom_message":
		members = []jsonMember{
			member("type", mustRawString("custom_message")),
			member("customType", mustRawString(entry.CustomType)),
			member("content", entry.Content),
			member("display", rawBool(entry.Display)),
		}
		if entry.Details != nil {
			members = append(members, member("details", entry.Details))
		}
		members = append(members, base[1:]...)
	case "label":
		members = append(base, member("targetId", mustRawString(entry.TargetID)))
		if entry.Label != nil {
			members = append(members, member("label", mustRawString(*entry.Label)))
		}
	case "leaf":
		targetID := rawNull()
		if entry.LeafTargetID != nil {
			targetID = mustRawString(*entry.LeafTargetID)
		} else if entry.TargetID != "" {
			targetID = mustRawString(entry.TargetID)
		}
		members = append(base, member("targetId", targetID))
	case "session_info":
		members = append(base, member("name", mustRawString(entry.Name)))
	default:
		members = base
	}
	object := newOrderedObject(members...)
	return decodeFileEntry(object, nil)
}

func sanitizeSessionName(name string) string {
	var output strings.Builder
	output.Grow(len(name))
	inBreak := false
	for _, character := range name {
		if character == '\r' || character == '\n' {
			if !inBreak {
				output.WriteByte(' ')
				inBreak = true
			}
			continue
		}
		inBreak = false
		output.WriteRune(character)
	}
	return trimJSSpace(output.String())
}

func trimJSSpace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch {
		case character >= '\t' && character <= '\r':
			return true
		case character == ' ', character == '\u00a0', character == '\u1680', character == '\u2028', character == '\u2029', character == '\u202f', character == '\u205f', character == '\u3000', character == '\ufeff':
			return true
		case character >= '\u2000' && character <= '\u200a':
			return true
		default:
			return false
		}
	})
}
