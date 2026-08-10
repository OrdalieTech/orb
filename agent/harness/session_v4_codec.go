package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/OrdalieTech/orb/ai"
)

// The v4 session JSONL format stores one header line followed by a strictly
// sequenced mutation log (entries, lane records, lane pointers, and facts).
// This codec mirrors packages/agent/src/harness/session/jsonl/codec.ts.

var sessionV4EntryTypes = map[string]bool{
	"message": true, "model_change": true, "thinking_level_change": true, "active_tools_change": true,
	"compaction": true, "branch_summary": true, "custom": true,
}

var sessionV4RecordTypes = map[string]bool{
	"operation_started": true, "abort_requested": true, "operation_finished": true, "step_attempt": true,
	"tool_started": true, "queue_enqueued": true, "queue_cancelled": true, "write_deferred": true, "usage": true,
}

var sessionV4OperationKinds = map[string]bool{"run": true, "compaction": true, "navigation": true}

func invalidV4File(path string, line int, format string, arguments ...any) *SessionError {
	message := fmt.Sprintf(format, arguments...)
	return newSessionError(SessionErrorInvalidEntry, "Invalid JSONL v4 session %s: line %d %s", path, line, message)
}

// SessionV4Header mirrors upstream JsonlV4Header.
type SessionV4Header struct {
	ID                      string
	CreatedAt               int64
	CWD                     string
	ParentSessionID         *string
	LegacyParentSessionPath *string
	Metadata                json.RawMessage
}

// SessionV4Entry is one immutable tree entry of a v4 session. Its serialized
// member order is retained for wire-compatible re-encoding.
type SessionV4Entry struct {
	Type      string
	ID        string
	ParentID  *string
	Seq       int
	Timestamp int64

	Message         json.RawMessage
	ThinkingLevel   string
	Provider        string
	ModelID         string
	ActiveToolNames []string
	Summary         string
	FromID          string
	TokensBefore    float64
	RetainedTail    []json.RawMessage
	CustomType      string
	Data            json.RawMessage
	Details         json.RawMessage

	members []harnessJSONMember
}

func (entry SessionV4Entry) MarshalJSON() ([]byte, error) {
	return marshalHarnessMembers(entry.members)
}

func (entry SessionV4Entry) clone() SessionV4Entry {
	copied := entry
	copied.ParentID = cloneHarnessString(entry.ParentID)
	copied.Message = cloneHarnessRaw(entry.Message)
	copied.ActiveToolNames = cloneHarnessStrings(entry.ActiveToolNames)
	copied.RetainedTail = cloneHarnessRawMessages(entry.RetainedTail)
	copied.Data = cloneHarnessRaw(entry.Data)
	copied.Details = cloneHarnessRaw(entry.Details)
	copied.members = cloneHarnessMembers(entry.members)
	return copied
}

func (entry SessionV4Entry) withSeq(seq int) SessionV4Entry {
	copied := entry.clone()
	copied.Seq = seq
	for index := range copied.members {
		if copied.members[index].name == "seq" {
			copied.members[index].value = json.RawMessage(strconv.Itoa(seq))
		}
	}
	return copied
}

// SessionV4Usage carries the token accounting consumed by session stats.
type SessionV4Usage struct {
	Input       float64
	Output      float64
	CacheRead   float64
	CacheWrite  float64
	TotalTokens float64
	CostTotal   float64
}

// SessionV4Record is one lane record (operation, queue, usage, ...) of a v4
// session log.
type SessionV4Record struct {
	Type      string
	ID        string
	Lane      string
	Seq       int
	Timestamp int64

	RunID      string
	IntentKind string
	Usage      *SessionV4Usage

	members []harnessJSONMember
}

func (record SessionV4Record) MarshalJSON() ([]byte, error) {
	return marshalHarnessMembers(record.members)
}

func (record SessionV4Record) clone() SessionV4Record {
	copied := record
	if record.Usage != nil {
		usage := *record.Usage
		copied.Usage = &usage
	}
	copied.members = cloneHarnessMembers(record.members)
	return copied
}

// SessionV4LanePointer names one lane and its current leaf entry.
type SessionV4LanePointer struct {
	Lane   string  `json:"lane"`
	LeafID *string `json:"leafId"`
}

// SessionV4LogItem is one replayed mutation of the session log.
type SessionV4LogItem struct {
	Kind string
	Seq  int

	Entry  *SessionV4Entry
	Record *SessionV4Record

	Lane   string
	LeafID *string

	Fact     string
	Name     string
	TargetID string
	Label    *string
}

func (item SessionV4LogItem) MarshalJSON() ([]byte, error) {
	members := []harnessJSONMember{
		harnessStringMember("kind", item.Kind),
		{name: "seq", value: json.RawMessage(strconv.Itoa(item.Seq))},
	}
	switch item.Kind {
	case "entry":
		encoded, err := item.Entry.MarshalJSON()
		if err != nil {
			return nil, err
		}
		members = append(members, harnessJSONMember{name: "entry", value: encoded})
	case "record":
		encoded, err := item.Record.MarshalJSON()
		if err != nil {
			return nil, err
		}
		members = append(members, harnessJSONMember{name: "record", value: encoded})
	case "lane":
		members = append(members, harnessStringMember("lane", item.Lane), harnessNullableStringMember("leafId", item.LeafID))
	case "fact":
		members = append(members, harnessStringMember("fact", item.Fact))
		if item.Fact == "name" {
			members = append(members, harnessStringMember("name", item.Name))
		} else {
			members = append(members, harnessStringMember("targetId", item.TargetID))
			if item.Label != nil {
				members = append(members, harnessStringMember("label", *item.Label))
			}
		}
	}
	return marshalHarnessMembers(members)
}

// SessionV4Mutation is one decoded JSONL mutation line.
type SessionV4Mutation struct {
	Kind string

	EntryLane *string
	Entry     *SessionV4Entry

	Record *SessionV4Record

	Seq    int
	Lane   string
	LeafID *string

	Fact     string
	Name     string
	TargetID string
	Label    *string
}

func harnessNullableStringMember(name string, value *string) harnessJSONMember {
	if value == nil {
		return harnessJSONMember{name: name, value: json.RawMessage("null")}
	}
	return harnessStringMember(name, *value)
}

func cloneHarnessMembers(members []harnessJSONMember) []harnessJSONMember {
	copied := make([]harnessJSONMember, len(members))
	for index := range members {
		copied[index] = harnessJSONMember{name: members[index].name, value: cloneHarnessRaw(members[index].value)}
	}
	return copied
}

// parseV4Object decodes a JSON object preserving top-level member order.
func parseV4Object(data []byte) ([]harnessJSONMember, map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if !json.Valid(trimmed) {
		return nil, nil, fmt.Errorf("is not valid JSON")
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, nil, fmt.Errorf("is not a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if _, err := decoder.Token(); err != nil {
		return nil, nil, fmt.Errorf("is not a JSON object")
	}
	members := make([]harnessJSONMember, 0, 8)
	byName := make(map[string]json.RawMessage, 8)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("is not a JSON object")
		}
		name, ok := token.(string)
		if !ok {
			return nil, nil, fmt.Errorf("is not a JSON object")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, fmt.Errorf("is not a JSON object")
		}
		members = append(members, harnessJSONMember{name: name, value: value})
		byName[name] = value
	}
	return members, byName, nil
}

func v4String(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	ok := decodeHarnessStringInto(raw, &value)
	return value, ok
}

func v4SafeInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	if value != math.Trunc(value) || math.Abs(value) > float64(1<<53-1) {
		return 0, false
	}
	return int64(value), true
}

func v4NullableID(raw json.RawMessage, path string, line int, field string) (*string, error) {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		if len(raw) == 0 {
			return nil, invalidV4File(path, line, "has invalid %s", field)
		}
		return nil, nil
	}
	value, ok := v4String(raw)
	if !ok {
		return nil, invalidV4File(path, line, "has invalid %s", field)
	}
	return &value, nil
}

func requireV4String(raw json.RawMessage, path string, line int, field string) (string, error) {
	value, ok := v4String(raw)
	if !ok {
		return "", invalidV4File(path, line, "has invalid %s", field)
	}
	return value, nil
}

func requireV4Sequence(raw json.RawMessage, path string, line int) (int, error) {
	value, ok := v4SafeInteger(raw)
	if !ok || value <= 0 {
		return 0, invalidV4File(path, line, "has invalid seq")
	}
	return int(value), nil
}

func requireV4Timestamp(raw json.RawMessage, path string, line int) (int64, error) {
	value, ok := v4SafeInteger(raw)
	if !ok || value < 0 {
		return 0, invalidV4File(path, line, "has invalid timestamp")
	}
	return value, nil
}

// ParseSessionV4Header decodes and validates the first line of a v4 session.
func ParseSessionV4Header(line []byte, path string) (SessionV4Header, error) {
	_, byName, err := parseV4Object(line)
	if err != nil {
		return SessionV4Header{}, invalidV4File(path, 1, "%s", err.Error())
	}
	if kind, _ := v4String(byName["kind"]); kind != "header" {
		return SessionV4Header{}, invalidV4File(path, 1, "is not a header")
	}
	if version, ok := v4SafeInteger(byName["version"]); !ok || version != 4 {
		return SessionV4Header{}, invalidV4File(path, 1, "has unsupported session version")
	}
	header := SessionV4Header{}
	if raw, ok := byName["parentSessionId"]; ok {
		value, isString := v4String(raw)
		if !isString {
			return SessionV4Header{}, invalidV4File(path, 1, "has invalid parentSessionId")
		}
		header.ParentSessionID = &value
	}
	if raw, ok := byName["legacyParentSessionPath"]; ok {
		value, isString := v4String(raw)
		if !isString {
			return SessionV4Header{}, invalidV4File(path, 1, "has invalid legacyParentSessionPath")
		}
		header.LegacyParentSessionPath = &value
	}
	if header.ParentSessionID != nil && header.LegacyParentSessionPath != nil {
		return SessionV4Header{}, invalidV4File(path, 1, "has both parentSessionId and legacyParentSessionPath")
	}
	if raw, ok := byName["metadata"]; ok {
		if !isHarnessJSONObject(raw) {
			return SessionV4Header{}, invalidV4File(path, 1, "has invalid metadata")
		}
		header.Metadata = cloneHarnessRaw(raw)
	}
	if header.ID, err = requireV4String(byName["id"], path, 1, "id"); err != nil {
		return SessionV4Header{}, err
	}
	if header.CreatedAt, err = requireV4Timestamp(byName["createdAt"], path, 1); err != nil {
		return SessionV4Header{}, err
	}
	if header.CWD, err = requireV4String(byName["cwd"], path, 1, "cwd"); err != nil {
		return SessionV4Header{}, err
	}
	return header, nil
}

// MarshalSessionV4Header serializes a header with upstream's member order.
func MarshalSessionV4Header(header SessionV4Header) ([]byte, error) {
	members := []harnessJSONMember{
		harnessStringMember("kind", "header"),
		{name: "version", value: json.RawMessage("4")},
		harnessStringMember("id", header.ID),
		{name: "createdAt", value: json.RawMessage(strconv.FormatInt(header.CreatedAt, 10))},
		harnessStringMember("cwd", header.CWD),
	}
	if header.ParentSessionID != nil {
		members = append(members, harnessStringMember("parentSessionId", *header.ParentSessionID))
	}
	if header.LegacyParentSessionPath != nil {
		members = append(members, harnessStringMember("legacyParentSessionPath", *header.LegacyParentSessionPath))
	}
	if len(header.Metadata) != 0 {
		normalized, err := ai.NormalizeJSONStringifyJSON(header.Metadata)
		if err != nil {
			return nil, err
		}
		members = append(members, harnessJSONMember{name: "metadata", value: normalized})
	}
	return marshalHarnessMembers(members)
}

func decodeV4EntryPayload(members []harnessJSONMember, byName map[string]json.RawMessage, path string, line int) (SessionV4Entry, error) {
	entry := SessionV4Entry{members: members}
	var err error
	if entry.ID, err = requireV4String(byName["id"], path, line, "id"); err != nil {
		return SessionV4Entry{}, err
	}
	if entry.Type, err = requireV4String(byName["type"], path, line, "entry type"); err != nil {
		return SessionV4Entry{}, err
	}
	if !sessionV4EntryTypes[entry.Type] {
		return SessionV4Entry{}, invalidV4File(path, line, "has unknown entry type %s", entry.Type)
	}
	if entry.ParentID, err = v4NullableID(byName["parentId"], path, line, "parentId"); err != nil {
		return SessionV4Entry{}, err
	}
	if entry.Seq, err = requireV4Sequence(byName["seq"], path, line); err != nil {
		return SessionV4Entry{}, err
	}
	if entry.Timestamp, err = requireV4Timestamp(byName["timestamp"], path, line); err != nil {
		return SessionV4Entry{}, err
	}
	if entry.Type == "custom" {
		if entry.CustomType, err = requireV4String(byName["customType"], path, line, "customType"); err != nil {
			return SessionV4Entry{}, err
		}
	}
	entry.Message = cloneHarnessRaw(byName["message"])
	decodeHarnessStringInto(byName["thinkingLevel"], &entry.ThinkingLevel)
	decodeHarnessStringInto(byName["provider"], &entry.Provider)
	decodeHarnessStringInto(byName["modelId"], &entry.ModelID)
	if raw, ok := byName["activeToolNames"]; ok {
		_ = json.Unmarshal(raw, &entry.ActiveToolNames)
	}
	decodeHarnessStringInto(byName["summary"], &entry.Summary)
	decodeHarnessStringInto(byName["fromId"], &entry.FromID)
	if raw, ok := byName["tokensBefore"]; ok {
		_ = json.Unmarshal(raw, &entry.TokensBefore)
	}
	if raw, ok := byName["retainedTail"]; ok {
		_ = json.Unmarshal(raw, &entry.RetainedTail)
	}
	entry.Data = cloneHarnessRaw(byName["data"])
	entry.Details = cloneHarnessRaw(byName["details"])
	return entry, nil
}

func decodeV4RecordPayload(members []harnessJSONMember, byName map[string]json.RawMessage, path string, line int) (SessionV4Record, error) {
	record := SessionV4Record{members: members}
	var err error
	if record.ID, err = requireV4String(byName["id"], path, line, "id"); err != nil {
		return SessionV4Record{}, err
	}
	if record.Lane, err = requireV4String(byName["lane"], path, line, "lane"); err != nil {
		return SessionV4Record{}, err
	}
	if record.Type, err = requireV4String(byName["type"], path, line, "record type"); err != nil {
		return SessionV4Record{}, err
	}
	if !sessionV4RecordTypes[record.Type] {
		return SessionV4Record{}, invalidV4File(path, line, "has unknown record type %s", record.Type)
	}
	if record.Seq, err = requireV4Sequence(byName["seq"], path, line); err != nil {
		return SessionV4Record{}, err
	}
	if record.Timestamp, err = requireV4Timestamp(byName["timestamp"], path, line); err != nil {
		return SessionV4Record{}, err
	}
	if record.Type == "operation_started" {
		if !isHarnessJSONObject(byName["intent"]) {
			return SessionV4Record{}, invalidV4File(path, line, "has invalid intent")
		}
		var intent struct {
			Kind json.RawMessage `json:"kind"`
		}
		_ = json.Unmarshal(byName["intent"], &intent)
		if record.IntentKind, err = requireV4String(intent.Kind, path, line, "operation kind"); err != nil {
			return SessionV4Record{}, err
		}
		if !sessionV4OperationKinds[record.IntentKind] {
			return SessionV4Record{}, invalidV4File(path, line, "has unknown operation kind %s", record.IntentKind)
		}
	}
	if record.Type == "operation_finished" {
		if _, err = requireV4String(byName["runId"], path, line, "runId"); err != nil {
			return SessionV4Record{}, err
		}
	}
	decodeHarnessStringInto(byName["runId"], &record.RunID)
	if raw, ok := byName["usage"]; ok {
		var usage struct {
			Input       float64 `json:"input"`
			Output      float64 `json:"output"`
			CacheRead   float64 `json:"cacheRead"`
			CacheWrite  float64 `json:"cacheWrite"`
			TotalTokens float64 `json:"totalTokens"`
			Cost        struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		}
		if json.Unmarshal(raw, &usage) == nil {
			record.Usage = &SessionV4Usage{
				Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead,
				CacheWrite: usage.CacheWrite, TotalTokens: usage.TotalTokens, CostTotal: usage.Cost.Total,
			}
		}
	}
	return record, nil
}

func membersWithout(members []harnessJSONMember, names ...string) []harnessJSONMember {
	kept := make([]harnessJSONMember, 0, len(members))
	for _, member := range members {
		skip := false
		for _, name := range names {
			if member.name == name {
				skip = true
				break
			}
		}
		if !skip {
			kept = append(kept, member)
		}
	}
	return kept
}

// ParseSessionV4Entry decodes one bare entry object (no kind/lane wrapper),
// as used for context-building inputs.
func ParseSessionV4Entry(data []byte) (SessionV4Entry, error) {
	members, byName, err := parseV4Object(data)
	if err != nil {
		return SessionV4Entry{}, invalidV4File("<entry>", 1, "%s", err.Error())
	}
	return decodeV4EntryPayload(members, byName, "<entry>", 1)
}

// ParseSessionV4Mutation decodes one v4 mutation line.
func ParseSessionV4Mutation(line []byte, path string, lineNumber int) (SessionV4Mutation, error) {
	members, byName, err := parseV4Object(line)
	if err != nil {
		return SessionV4Mutation{}, invalidV4File(path, lineNumber, "%s", err.Error())
	}
	seq, err := requireV4Sequence(byName["seq"], path, lineNumber)
	if err != nil {
		return SessionV4Mutation{}, err
	}
	kind, _ := v4String(byName["kind"])
	switch kind {
	case "entry":
		mutation := SessionV4Mutation{Kind: "entry", Seq: seq}
		if raw, ok := byName["lane"]; ok {
			lane, err := requireV4String(raw, path, lineNumber, "lane")
			if err != nil {
				return SessionV4Mutation{}, err
			}
			mutation.EntryLane = &lane
		}
		entry, err := decodeV4EntryPayload(membersWithout(members, "kind", "lane"), byName, path, lineNumber)
		if err != nil {
			return SessionV4Mutation{}, err
		}
		mutation.Entry = &entry
		return mutation, nil
	case "record":
		record, err := decodeV4RecordPayload(membersWithout(members, "kind"), byName, path, lineNumber)
		if err != nil {
			return SessionV4Mutation{}, err
		}
		return SessionV4Mutation{Kind: "record", Seq: seq, Record: &record}, nil
	case "lane":
		lane, err := requireV4String(byName["lane"], path, lineNumber, "lane")
		if err != nil {
			return SessionV4Mutation{}, err
		}
		leafID, err := v4NullableID(byName["leafId"], path, lineNumber, "leafId")
		if err != nil {
			return SessionV4Mutation{}, err
		}
		return SessionV4Mutation{Kind: "lane", Seq: seq, Lane: lane, LeafID: leafID}, nil
	case "fact":
		fact, _ := v4String(byName["fact"])
		if fact == "name" {
			name, err := requireV4String(byName["name"], path, lineNumber, "name")
			if err != nil {
				return SessionV4Mutation{}, err
			}
			return SessionV4Mutation{Kind: "fact", Seq: seq, Fact: "name", Name: name}, nil
		}
		if fact == "label" {
			mutation := SessionV4Mutation{Kind: "fact", Seq: seq, Fact: "label"}
			if raw, ok := byName["label"]; ok {
				label, isString := v4String(raw)
				if !isString {
					return SessionV4Mutation{}, invalidV4File(path, lineNumber, "has invalid label")
				}
				mutation.Label = &label
			}
			if mutation.TargetID, err = requireV4String(byName["targetId"], path, lineNumber, "targetId"); err != nil {
				return SessionV4Mutation{}, err
			}
			return mutation, nil
		}
		return SessionV4Mutation{}, invalidV4File(path, lineNumber, "has unknown fact type")
	default:
		return SessionV4Mutation{}, invalidV4File(path, lineNumber, "has unknown mutation kind")
	}
}

// MarshalSessionV4Mutation serializes one mutation with upstream member order.
func MarshalSessionV4Mutation(mutation SessionV4Mutation) ([]byte, error) {
	switch mutation.Kind {
	case "entry":
		members := []harnessJSONMember{harnessStringMember("kind", "entry")}
		if mutation.EntryLane != nil {
			members = append(members, harnessStringMember("lane", *mutation.EntryLane))
		}
		return marshalHarnessMembers(append(members, mutation.Entry.members...))
	case "record":
		members := []harnessJSONMember{harnessStringMember("kind", "record")}
		return marshalHarnessMembers(append(members, mutation.Record.members...))
	case "lane":
		return marshalHarnessMembers([]harnessJSONMember{
			harnessStringMember("kind", "lane"),
			{name: "seq", value: json.RawMessage(strconv.Itoa(mutation.Seq))},
			harnessStringMember("lane", mutation.Lane),
			harnessNullableStringMember("leafId", mutation.LeafID),
		})
	case "fact":
		members := []harnessJSONMember{
			harnessStringMember("kind", "fact"),
			{name: "seq", value: json.RawMessage(strconv.Itoa(mutation.Seq))},
			harnessStringMember("fact", mutation.Fact),
		}
		if mutation.Fact == "name" {
			members = append(members, harnessStringMember("name", mutation.Name))
		} else {
			members = append(members, harnessStringMember("targetId", mutation.TargetID))
			if mutation.Label != nil {
				members = append(members, harnessStringMember("label", *mutation.Label))
			}
		}
		return marshalHarnessMembers(members)
	}
	return nil, fmt.Errorf("harness: unknown v4 mutation kind %q", mutation.Kind)
}
