package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

type harnessSessionHeader struct {
	Version       float64
	ID            string
	Timestamp     string
	CWD           string
	ParentSession *string
	Metadata      json.RawMessage
}

func parseHarnessHeader(line []byte, filePath string) (harnessSessionHeader, error) {
	return parseHarnessHeaderVersion(line, filePath, func(version float64) bool { return version == 3 })
}

func parseRuntimeHarnessHeader(line []byte, filePath string) (harnessSessionHeader, error) {
	return parseHarnessHeaderVersion(line, filePath, func(version float64) bool { return version >= 3 })
}

func parseHarnessHeaderVersion(
	line []byte,
	filePath string,
	acceptVersion func(float64) bool,
) (harnessSessionHeader, error) {
	object, err := parseHarnessObject(line)
	if err != nil {
		return harnessSessionHeader{}, invalidHarnessSession(filePath, "first line is not a valid session header")
	}
	var entryType string
	if !decodeHarnessStringInto(object["type"], &entryType) || entryType != "session" {
		return harnessSessionHeader{}, invalidHarnessSession(filePath, "first line is not a valid session header")
	}
	var version float64
	if json.Unmarshal(object["version"], &version) != nil || !acceptVersion(version) {
		return harnessSessionHeader{}, invalidHarnessSession(filePath, "unsupported session version")
	}
	header := harnessSessionHeader{Version: version}
	if !decodeHarnessStringInto(object["id"], &header.ID) || header.ID == "" {
		return harnessSessionHeader{}, invalidHarnessSession(filePath, "session header is missing id")
	}
	if !decodeHarnessStringInto(object["timestamp"], &header.Timestamp) || header.Timestamp == "" {
		return harnessSessionHeader{}, invalidHarnessSession(filePath, "session header is missing timestamp")
	}
	if !decodeHarnessStringInto(object["cwd"], &header.CWD) || header.CWD == "" {
		return harnessSessionHeader{}, invalidHarnessSession(filePath, "session header is missing cwd")
	}
	if raw, ok := object["parentSession"]; ok {
		var parent string
		if !decodeHarnessStringInto(raw, &parent) {
			return harnessSessionHeader{}, invalidHarnessSession(filePath, "session header parentSession must be a string")
		}
		header.ParentSession = &parent
	}
	if raw, ok := object["metadata"]; ok {
		if !isHarnessJSONObject(raw) {
			return harnessSessionHeader{}, invalidHarnessSession(filePath, "session header metadata must be an object")
		}
		header.Metadata = cloneHarnessRaw(raw)
	}
	return header, nil
}

func parseHarnessObject(data []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("harness: JSON record is not an object")
	}
	if object, ok := jsonwire.Members(trimmed); ok {
		return object, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		if err == nil {
			err = fmt.Errorf("harness: JSON record is not an object")
		}
		return nil, err
	}
	return object, nil
}

func decodeHarnessEntryObject(object map[string]json.RawMessage) (SessionTreeEntry, error) {
	entry := SessionTreeEntry{}
	decodeHarnessStringInto(object["type"], &entry.Type)
	decodeHarnessStringInto(object["id"], &entry.ID)
	decodeHarnessStringInto(object["timestamp"], &entry.Timestamp)
	if raw, ok := object["parentId"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		var parent string
		if decodeHarnessStringInto(raw, &parent) {
			entry.ParentID = &parent
		}
	}
	entry.Message = cloneHarnessRaw(object["message"])
	decodeHarnessStringInto(object["thinkingLevel"], &entry.ThinkingLevel)
	decodeHarnessStringInto(object["provider"], &entry.Provider)
	decodeHarnessStringInto(object["modelId"], &entry.ModelID)
	if raw, ok := object["activeToolNames"]; ok {
		_ = json.Unmarshal(raw, &entry.ActiveToolNames)
	}
	decodeHarnessStringInto(object["summary"], &entry.Summary)
	decodeHarnessStringInto(object["firstKeptEntryId"], &entry.FirstKeptEntryID)
	if raw, ok := object["retainedTail"]; ok {
		_ = json.Unmarshal(raw, &entry.RetainedTail)
	}
	if raw, ok := object["tokensBefore"]; ok {
		var number float64
		if json.Unmarshal(raw, &number) == nil {
			entry.TokensBefore = number
		}
	}
	entry.Details = cloneHarnessRaw(object["details"])
	if raw, ok := object["usage"]; ok {
		var usage ai.Usage
		if json.Unmarshal(raw, &usage) == nil {
			entry.Usage = &usage
		}
	}
	if raw, ok := object["fromHook"]; ok {
		var value bool
		if json.Unmarshal(raw, &value) == nil {
			entry.FromHook = &value
		}
	}
	decodeHarnessStringInto(object["fromId"], &entry.FromID)
	decodeHarnessStringInto(object["customType"], &entry.CustomType)
	entry.Data = cloneHarnessRaw(object["data"])
	entry.Content = cloneHarnessRaw(object["content"])
	if raw, ok := object["display"]; ok {
		_ = json.Unmarshal(raw, &entry.Display)
	}
	if raw, ok := object["targetId"]; ok {
		entry.HasTargetID = true
		if string(bytes.TrimSpace(raw)) != "null" {
			var target string
			if decodeHarnessStringInto(raw, &target) {
				entry.TargetID = &target
			}
		}
	}
	if raw, ok := object["label"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		var label string
		if decodeHarnessStringInto(raw, &label) {
			entry.Label = &label
		}
	}
	decodeHarnessStringInto(object["name"], &entry.Name)
	entry.Replacement = cloneHarnessRaw(object["replacement"])
	return entry, nil
}

func decodeHarnessStringInto(raw []byte, target *string) bool {
	if len(raw) == 0 || target == nil {
		return false
	}
	value, err := jsonwire.UnmarshalString(bytes.TrimSpace(raw))
	if err != nil {
		return false
	}
	*target = value
	return true
}

func marshalHarnessHeader(metadata SessionMetadata) ([]byte, error) {
	members := jsonwire.RawObject{
		harnessString("type", "session"),
		{Name: "version", Value: json.RawMessage("3")},
		harnessString("id", metadata.ID),
		harnessString("timestamp", metadata.CreatedAt),
		harnessString("cwd", metadata.CWD),
	}
	if metadata.ParentSessionPath != nil {
		members = append(members, harnessString("parentSession", *metadata.ParentSessionPath))
	}
	if len(metadata.Metadata) != 0 {
		if !json.Valid(metadata.Metadata) {
			return nil, fmt.Errorf("harness: invalid header metadata JSON")
		}
		normalized, err := ai.NormalizeJSONStringifyJSON(metadata.Metadata)
		if err != nil {
			return nil, err
		}
		members = append(members, jsonwire.RawMember{Name: "metadata", Value: normalized})
	}
	return marshalHarnessMembers(members)
}

// MarshalSessionJSONL serializes a non-byte-backed session using the harness
// object insertion order. cwd supplies the coding-session context omitted by
// generic in-memory harness metadata.
func MarshalSessionJSONL(storage SessionStorage, cwd string) ([]byte, error) {
	if storage == nil {
		return nil, fmt.Errorf("harness: nil session storage")
	}
	metadata := storage.Metadata()
	if metadata.CWD == "" {
		metadata.CWD = cwd
	}
	if err := validateHarnessMetadata(metadata); err != nil {
		return nil, err
	}
	header, err := encodeHarnessHeader(metadata)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.Write(header)
	for _, entry := range storage.Entries() {
		encoded, marshalErr := marshalHarnessEntry(entry)
		if marshalErr != nil {
			return nil, marshalErr
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

// ParseSessionTreeEntry decodes one upstream session entry while retaining
// unknown members for a later wire-compatible marshal. Exported for module
// embedders with external session stores; not referenced inside this repo.
func ParseSessionTreeEntry(data []byte) (SessionTreeEntry, error) {
	return parseHarnessEntry(data, "<entry>", 1)
}

// MarshalSessionTreeEntry serializes one entry with upstream field order.
// Exported for module embedders; not referenced inside this repo.
func MarshalSessionTreeEntry(entry SessionTreeEntry) ([]byte, error) {
	return marshalHarnessEntry(entry)
}

func marshalHarnessEntry(entry SessionTreeEntry) ([]byte, error) {
	if len(entry.raw) != 0 {
		if !jsonwire.Valid(entry.raw) {
			return nil, fmt.Errorf("harness: invalid raw session entry")
		}
		return ai.NormalizeJSONStringifyJSON(entry.raw)
	}
	parent := json.RawMessage("null")
	if entry.ParentID != nil {
		parent = jsonwire.AppendString(nil, *entry.ParentID)
	}
	members := jsonwire.RawObject{
		harnessString("type", entry.Type),
		harnessString("id", entry.ID),
		{Name: "parentId", Value: parent},
		harnessString("timestamp", entry.Timestamp),
	}
	optional := func(name string, value json.RawMessage) {
		if len(value) != 0 {
			members = append(members, jsonwire.RawMember{Name: name, Value: value})
		}
	}
	summaryFields := func() {
		optional("details", entry.Details)
		if entry.Usage != nil {
			members = append(members, harnessJSON("usage", entry.Usage))
		}
		if entry.FromHook != nil {
			members = append(members, harnessJSON("fromHook", *entry.FromHook))
		}
	}
	target := json.RawMessage("null")
	if entry.TargetID != nil {
		target = jsonwire.AppendString(nil, *entry.TargetID)
	}
	switch entry.Type {
	case "message":
		members = append(members, jsonwire.RawMember{Name: "message", Value: entry.Message})
	case "thinking_level_change":
		members = append(members, harnessString("thinkingLevel", entry.ThinkingLevel))
	case "model_change":
		members = append(members, harnessString("provider", entry.Provider), harnessString("modelId", entry.ModelID))
	case "active_tools_change":
		encoded, err := jsonwire.Marshal(entry.ActiveToolNames)
		if err != nil {
			return nil, err
		}
		members = append(members, jsonwire.RawMember{Name: "activeToolNames", Value: encoded})
	case "compaction":
		members = append(members, harnessString("summary", entry.Summary))
		if entry.FirstKeptEntryID != "" {
			members = append(members, harnessString("firstKeptEntryId", entry.FirstKeptEntryID))
		}
		members = append(members, harnessJSON("tokensBefore", entry.TokensBefore))
		if entry.RetainedTail != nil {
			encoded, err := jsonwire.Marshal(entry.RetainedTail)
			if err != nil {
				return nil, err
			}
			members = append(members, jsonwire.RawMember{Name: "retainedTail", Value: encoded})
		}
		summaryFields()
	case "branch_summary":
		members = append(members, harnessString("fromId", entry.FromID), harnessString("summary", entry.Summary))
		summaryFields()
	case "custom":
		members = append(members, harnessString("customType", entry.CustomType))
		optional("data", entry.Data)
	case "custom_message":
		members = append(members, harnessString("customType", entry.CustomType),
			jsonwire.RawMember{Name: "content", Value: entry.Content}, harnessJSON("display", entry.Display))
		optional("details", entry.Details)
	case "label":
		members = append(members, jsonwire.RawMember{Name: "targetId", Value: target})
		if entry.Label != nil {
			members = append(members, harnessString("label", *entry.Label))
		}
	case "session_info":
		members = append(members, harnessString("name", entry.Name))
	case "context_edit":
		if entry.TargetID == nil {
			target = json.RawMessage(`""`)
		}
		members = append(members, jsonwire.RawMember{Name: "targetId", Value: target}, jsonwire.RawMember{Name: "replacement", Value: entry.Replacement})
	case "leaf":
		members = append(members, jsonwire.RawMember{Name: "targetId", Value: target})
	}
	return marshalHarnessMembers(members)
}

// marshalHarnessMembers checks the values callers supply raw, which the
// codec's own encodings need not be, before encoding the object.
func marshalHarnessMembers(members jsonwire.RawObject) ([]byte, error) {
	for _, member := range members {
		switch member.Name {
		case "message", "details", "data", "content", "replacement":
			if len(member.Value) != 0 && !jsonwire.Valid(member.Value) {
				return nil, fmt.Errorf("harness: invalid raw JSON member %s", member.Name)
			}
		}
	}
	return members.MarshalJSON()
}

func harnessString(name, value string) jsonwire.RawMember {
	return jsonwire.RawMember{Name: name, Value: jsonwire.AppendString(nil, value)}
}

func harnessJSON(name string, value any) jsonwire.RawMember {
	encoded, err := marshalHarnessValue(value)
	if err != nil {
		panic(err)
	}
	return jsonwire.RawMember{Name: name, Value: encoded}
}

func marshalHarnessValue(value any) ([]byte, error) {
	return ai.Marshal(normalizeHarnessJSONStringifyValue(value))
}

func normalizeHarnessJSONStringifyValue(value any) any {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil
		}
		if typed == 0 {
			return float64(0)
		}
		return typed
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil
		}
		if typed == 0 {
			return float32(0)
		}
		return typed
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeHarnessJSONStringifyValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeHarnessJSONStringifyValue(item)
		}
		return normalized
	default:
		return value
	}
}
