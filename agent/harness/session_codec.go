package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonwire"
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

type harnessJSONMember struct {
	name  string
	value json.RawMessage
}

func marshalHarnessHeader(metadata SessionMetadata) ([]byte, error) {
	members := []harnessJSONMember{
		harnessStringMember("type", "session"),
		{name: "version", value: json.RawMessage("3")},
		harnessStringMember("id", metadata.ID),
		harnessStringMember("timestamp", metadata.CreatedAt),
		harnessStringMember("cwd", metadata.CWD),
	}
	if metadata.ParentSessionPath != nil {
		members = append(members, harnessStringMember("parentSession", *metadata.ParentSessionPath))
	}
	if len(metadata.Metadata) != 0 {
		if !json.Valid(metadata.Metadata) {
			return nil, fmt.Errorf("harness: invalid header metadata JSON")
		}
		normalized, err := ai.NormalizeJSONStringifyJSON(metadata.Metadata)
		if err != nil {
			return nil, err
		}
		members = append(members, harnessJSONMember{name: "metadata", value: normalized})
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

func marshalHarnessEntry(entry SessionTreeEntry) ([]byte, error) {
	if len(entry.raw) != 0 {
		if !json.Valid(entry.raw) {
			return nil, fmt.Errorf("harness: invalid raw session entry")
		}
		return ai.NormalizeJSONStringifyJSON(entry.raw)
	}
	parent := json.RawMessage("null")
	if entry.ParentID != nil {
		parent = harnessRawString(*entry.ParentID)
	}
	members := []harnessJSONMember{
		harnessStringMember("type", entry.Type),
		harnessStringMember("id", entry.ID),
		{name: "parentId", value: parent},
		harnessStringMember("timestamp", entry.Timestamp),
	}
	switch entry.Type {
	case "message":
		members = append(members, harnessRawMember("message", entry.Message))
	case "thinking_level_change":
		members = append(members, harnessStringMember("thinkingLevel", entry.ThinkingLevel))
	case "model_change":
		members = append(members, harnessStringMember("provider", entry.Provider), harnessStringMember("modelId", entry.ModelID))
	case "active_tools_change":
		encoded, err := jsonwire.Marshal(entry.ActiveToolNames)
		if err != nil {
			return nil, err
		}
		members = append(members, harnessJSONMember{name: "activeToolNames", value: encoded})
	case "compaction":
		members = append(members, harnessStringMember("summary", entry.Summary))
		if entry.FirstKeptEntryID != "" {
			members = append(members, harnessStringMember("firstKeptEntryId", entry.FirstKeptEntryID))
		}
		members = append(members, harnessJSONMember{name: "tokensBefore", value: mustHarnessJSON(entry.TokensBefore)})
		if entry.RetainedTail != nil {
			encoded, err := jsonwire.Marshal(entry.RetainedTail)
			if err != nil {
				return nil, err
			}
			members = append(members, harnessJSONMember{name: "retainedTail", value: encoded})
		}
		if len(entry.Details) != 0 {
			members = append(members, harnessRawMember("details", entry.Details))
		}
		if entry.Usage != nil {
			members = append(members, harnessJSONMember{name: "usage", value: mustHarnessJSON(entry.Usage)})
		}
		if entry.FromHook != nil {
			members = append(members, harnessJSONMember{name: "fromHook", value: mustHarnessJSON(*entry.FromHook)})
		}
	case "branch_summary":
		members = append(members, harnessStringMember("fromId", entry.FromID), harnessStringMember("summary", entry.Summary))
		if len(entry.Details) != 0 {
			members = append(members, harnessRawMember("details", entry.Details))
		}
		if entry.Usage != nil {
			members = append(members, harnessJSONMember{name: "usage", value: mustHarnessJSON(entry.Usage)})
		}
		if entry.FromHook != nil {
			members = append(members, harnessJSONMember{name: "fromHook", value: mustHarnessJSON(*entry.FromHook)})
		}
	case "custom":
		members = append(members, harnessStringMember("customType", entry.CustomType))
		if len(entry.Data) != 0 {
			members = append(members, harnessRawMember("data", entry.Data))
		}
	case "custom_message":
		members = append(members,
			harnessStringMember("customType", entry.CustomType),
			harnessRawMember("content", entry.Content),
			harnessJSONMember{name: "display", value: mustHarnessJSON(entry.Display)},
		)
		if len(entry.Details) != 0 {
			members = append(members, harnessRawMember("details", entry.Details))
		}
	case "label":
		target := json.RawMessage("null")
		if entry.TargetID != nil {
			target = harnessRawString(*entry.TargetID)
		}
		members = append(members, harnessJSONMember{name: "targetId", value: target})
		if entry.Label != nil {
			members = append(members, harnessStringMember("label", *entry.Label))
		}
	case "session_info":
		members = append(members, harnessStringMember("name", entry.Name))
	case "leaf":
		target := json.RawMessage("null")
		if entry.TargetID != nil {
			target = harnessRawString(*entry.TargetID)
		}
		members = append(members, harnessJSONMember{name: "targetId", value: target})
	}
	return marshalHarnessMembers(members)
}

func harnessStringMember(name, value string) harnessJSONMember {
	return harnessJSONMember{name: name, value: harnessRawString(value)}
}

func harnessRawString(value string) json.RawMessage {
	encoded, err := jsonwire.MarshalString(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func harnessRawMember(name string, value json.RawMessage) harnessJSONMember {
	if len(value) == 0 {
		value = json.RawMessage("null")
	}
	return harnessJSONMember{name: name, value: cloneHarnessRaw(value)}
}

func mustHarnessJSON(value any) json.RawMessage {
	encoded, err := marshalHarnessValue(value)
	if err != nil {
		panic(err)
	}
	return encoded
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

func marshalHarnessMembers(members []harnessJSONMember) ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, member := range members {
		if !json.Valid(member.value) {
			return nil, fmt.Errorf("harness: invalid raw JSON member %s", member.name)
		}
		if index > 0 {
			output.WriteByte(',')
		}
		name, err := jsonwire.MarshalString(member.name)
		if err != nil {
			return nil, err
		}
		output.Write(name)
		output.WriteByte(':')
		output.Write(member.value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}
