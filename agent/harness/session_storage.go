package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/internal/uuidv7"
)

type sessionStorageState struct {
	metadata SessionMetadata
	entries  []SessionTreeEntry
	byID     map[string]SessionTreeEntry
	labels   map[string]string
	leafID   *string
}

func newSessionStorageState(metadata SessionMetadata, entries []SessionTreeEntry, validateLeaf bool) (*sessionStorageState, error) {
	state := &sessionStorageState{
		metadata: cloneHarnessMetadata(metadata),
		entries:  cloneHarnessEntries(entries),
		byID:     make(map[string]SessionTreeEntry, len(entries)),
		labels:   make(map[string]string),
	}
	for _, entry := range state.entries {
		state.byID[entry.ID] = entry
		state.updateLabel(entry)
		state.leafID = leafAfterHarnessEntry(entry)
	}
	if validateLeaf && state.leafID != nil {
		if _, ok := state.byID[*state.leafID]; !ok {
			return nil, newSessionError(SessionErrorInvalidSession, "Entry %s not found", *state.leafID)
		}
	}
	return state, nil
}

func (state *sessionStorageState) updateLabel(entry SessionTreeEntry) {
	if entry.Type != "label" || entry.TargetID == nil {
		return
	}
	label := ""
	if entry.Label != nil {
		label = trimHarnessJSSpace(*entry.Label)
	}
	if label == "" {
		delete(state.labels, *entry.TargetID)
		return
	}
	state.labels[*entry.TargetID] = label
}

func leafAfterHarnessEntry(entry SessionTreeEntry) *string {
	if entry.Type == "leaf" {
		return cloneHarnessString(entry.TargetID)
	}
	return cloneHarnessString(&entry.ID)
}

func (state *sessionStorageState) metadataValue() SessionMetadata {
	return cloneHarnessMetadata(state.metadata)
}

func (state *sessionStorageState) leafValue() (*string, error) {
	if state.leafID != nil {
		if _, ok := state.byID[*state.leafID]; !ok {
			return nil, newSessionError(SessionErrorInvalidSession, "Entry %s not found", *state.leafID)
		}
	}
	return cloneHarnessString(state.leafID), nil
}

func (state *sessionStorageState) createEntryID() (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		id, err := uuidv7.EntryCandidate()
		if err != nil {
			return "", err
		}
		if _, exists := state.byID[id]; !exists {
			return id, nil
		}
	}
	return uuidv7.Generate(time.Now())
}

func (state *sessionStorageState) append(entry SessionTreeEntry) {
	entry = entry.clone()
	state.entries = append(state.entries, entry)
	state.byID[entry.ID] = entry
	state.updateLabel(entry)
	state.leafID = leafAfterHarnessEntry(entry)
}

func (state *sessionStorageState) entry(id string) (*SessionTreeEntry, bool) {
	entry, ok := state.byID[id]
	if !ok {
		return nil, false
	}
	copy := entry.clone()
	return &copy, true
}

func (state *sessionStorageState) entriesByType(entryType string) []SessionTreeEntry {
	result := make([]SessionTreeEntry, 0)
	for _, entry := range state.entries {
		if entry.Type == entryType {
			result = append(result, entry.clone())
		}
	}
	return result
}

func (state *sessionStorageState) label(id string) (string, bool) {
	label, ok := state.labels[id]
	return label, ok
}

func (state *sessionStorageState) sessionName() (string, bool) {
	for index := len(state.entries) - 1; index >= 0; index-- {
		if state.entries[index].Type != "session_info" {
			continue
		}
		name := trimHarnessJSSpace(state.entries[index].Name)
		return name, name != ""
	}
	return "", false
}

func (state *sessionStorageState) sessionStats() SessionStats {
	var stats SessionStats
	for _, entry := range state.entries {
		if entry.Type == "message" {
			stats.MessageCount++
		}
		input, output, cacheRead, cacheWrite, costTotal, ok := harnessEntryUsage(entry)
		if !ok {
			continue
		}
		stats.CachedTokens += cacheRead
		stats.UncachedTokens += input + cacheWrite
		stats.TotalTokens += input + output + cacheRead + cacheWrite
		stats.CostTotal += costTotal
	}
	return stats
}

func harnessEntryUsage(entry SessionTreeEntry) (input, output, cacheRead, cacheWrite, costTotal float64, ok bool) {
	var raw json.RawMessage
	switch entry.Type {
	case "message":
		var message struct {
			Role  string          `json:"role"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(entry.Message, &message) != nil || message.Role != "assistant" {
			return 0, 0, 0, 0, 0, false
		}
		raw = message.Usage
	case "compaction", "branch_summary":
		if len(entry.raw) != 0 {
			var object map[string]json.RawMessage
			if json.Unmarshal(entry.raw, &object) != nil {
				return 0, 0, 0, 0, 0, false
			}
			raw = object["usage"]
		} else {
			if entry.Usage == nil {
				return 0, 0, 0, 0, 0, false
			}
			raw, _ = json.Marshal(entry.Usage)
		}
	default:
		return 0, 0, 0, 0, 0, false
	}
	var usage struct {
		Input      *float64 `json:"input"`
		Output     *float64 `json:"output"`
		CacheRead  *float64 `json:"cacheRead"`
		CacheWrite *float64 `json:"cacheWrite"`
		Cost       *struct {
			Total *float64 `json:"total"`
		} `json:"cost"`
	}
	if json.Unmarshal(raw, &usage) != nil || usage.Input == nil || usage.Output == nil || usage.CacheRead == nil ||
		usage.CacheWrite == nil || usage.Cost == nil || usage.Cost.Total == nil {
		return 0, 0, 0, 0, 0, false
	}
	return *usage.Input, *usage.Output, *usage.CacheRead, *usage.CacheWrite, *usage.Cost.Total, true
}

func (state *sessionStorageState) pathToRootOrCompaction(leafID *string) ([]SessionTreeEntry, error) {
	return state.pathToRootWithCompaction(leafID, true)
}

func (state *sessionStorageState) pathToRootWithCompaction(leafID *string, stopAtCompaction bool) ([]SessionTreeEntry, error) {
	if leafID == nil {
		return []SessionTreeEntry{}, nil
	}
	current, ok := state.byID[*leafID]
	if !ok {
		return nil, newSessionError(SessionErrorNotFound, "Entry %s not found", *leafID)
	}
	path := make([]SessionTreeEntry, 0)
	stopAtEntryID := ""
	for {
		path = append(path, current.clone())
		if stopAtEntryID != "" && current.ID == stopAtEntryID {
			break
		}
		if stopAtCompaction && current.Type == "compaction" {
			if current.RetainedTail != nil {
				break
			}
			stopAtEntryID = current.FirstKeptEntryID
		}
		if current.ParentID == nil || *current.ParentID == "" {
			break
		}
		parent, exists := state.byID[*current.ParentID]
		if !exists {
			return nil, newSessionError(SessionErrorInvalidSession, "Entry %s not found", *current.ParentID)
		}
		current = parent
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path, nil
}

func (state *sessionStorageState) entriesWithCursor(options ...SessionEntryCursorOptions) []SessionTreeEntry {
	if len(options) == 0 {
		return cloneHarnessEntries(state.entries)
	}
	option := options[0]
	start := normalizeHarnessSliceIndex(option.AfterEntrySeq, len(state.entries))
	end := len(state.entries)
	if option.Limit != nil {
		end = normalizeHarnessSliceIndex(option.AfterEntrySeq+*option.Limit, len(state.entries))
		if end < start {
			end = start
		}
	}
	return cloneHarnessEntries(state.entries[start:end])
}

func normalizeHarnessSliceIndex(index, length int) int {
	if index < 0 {
		index = length + index
		if index < 0 {
			return 0
		}
	}
	if index > length {
		return length
	}
	return index
}

// InMemorySessionStorage stores the complete physical entry log in memory.
type InMemorySessionStorage struct {
	mu    sync.RWMutex
	state *sessionStorageState
}

func NewInMemorySessionStorage(entries []SessionTreeEntry, metadata SessionMetadata) (*InMemorySessionStorage, error) {
	state, err := newSessionStorageState(metadata, entries, true)
	if err != nil {
		return nil, err
	}
	return &InMemorySessionStorage{state: state}, nil
}

func (storage *InMemorySessionStorage) Metadata() SessionMetadata {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.metadataValue()
}

func (storage *InMemorySessionStorage) LeafID() (*string, error) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.leafValue()
}

func (storage *InMemorySessionStorage) SetLeafID(leafID *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.setLeafLocked(leafID)
}

func (storage *InMemorySessionStorage) setLeafLocked(leafID *string) error {
	if leafID != nil {
		if _, ok := storage.state.byID[*leafID]; !ok {
			return newSessionError(SessionErrorNotFound, "Entry %s not found", *leafID)
		}
	}
	id, err := storage.state.createEntryID()
	if err != nil {
		return err
	}
	storage.state.append(SessionTreeEntry{
		Type: "leaf", ID: id, ParentID: cloneHarnessString(storage.state.leafID),
		Timestamp: formatHarnessTimestamp(time.Now()), TargetID: cloneHarnessString(leafID), HasTargetID: true,
	})
	return nil
}

func (storage *InMemorySessionStorage) CreateEntryID() (string, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.createEntryID()
}

func (storage *InMemorySessionStorage) AppendEntry(entry SessionTreeEntry) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	storage.state.append(entry)
	return nil
}

func (storage *InMemorySessionStorage) Entry(id string) (*SessionTreeEntry, bool) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.entry(id)
}

func (storage *InMemorySessionStorage) EntriesByType(entryType string) []SessionTreeEntry {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.entriesByType(entryType)
}

func (storage *InMemorySessionStorage) Label(id string) (string, bool) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.label(id)
}

func (storage *InMemorySessionStorage) SessionName() (string, bool) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.sessionName()
}

func (storage *InMemorySessionStorage) SessionStats() SessionStats {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.sessionStats()
}

func (storage *InMemorySessionStorage) PathToRootOrCompaction(leafID *string) ([]SessionTreeEntry, error) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.pathToRootOrCompaction(leafID)
}

func (storage *InMemorySessionStorage) Entries(options ...SessionEntryCursorOptions) []SessionTreeEntry {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.entriesWithCursor(options...)
}

// JSONLSessionStorage retains the exact input bytes until a mutation appends
// the same JSON line that upstream would append.
type JSONLSessionStorage struct {
	mu      sync.RWMutex
	state   *sessionStorageState
	version float64
	header  []byte
	content []byte
	append  func([]byte) error
}

// RehydrateJSONLSession opens an upstream v3 JSONL session directly from
// bytes without first materializing a temporary file.
func RehydrateJSONLSession(content []byte, filePath string) (*JSONLSessionStorage, error) {
	return rehydrateJSONLSession(content, filePath, nil)
}

func rehydrateJSONLSession(content []byte, filePath string, appendLine func([]byte) error) (*JSONLSessionStorage, error) {
	return rehydrateJSONLSessionWithHeader(content, filePath, appendLine, parseHarnessHeader, false)
}

func rehydrateRuntimeJSONLSession(content []byte, filePath string, appendLine func([]byte) error) (*JSONLSessionStorage, error) {
	return rehydrateJSONLSessionWithHeader(content, filePath, appendLine, parseRuntimeHarnessHeader, true)
}

func rehydrateJSONLSessionWithHeader(
	content []byte,
	filePath string,
	appendLine func([]byte) error,
	parseHeader func([]byte, string) (harnessSessionHeader, error),
	skipMalformedJSON bool,
) (*JSONLSessionStorage, error) {
	lines := nonBlankHarnessLines(content)
	if len(lines) == 0 {
		return nil, invalidHarnessSession(filePath, "missing session header")
	}
	header, err := parseHeader(lines[0], filePath)
	if err != nil {
		return nil, err
	}
	entries := make([]SessionTreeEntry, 0, len(lines)-1)
	for index := 1; index < len(lines); index++ {
		entry, parseErr := parseHarnessEntry(lines[index], filePath, index+1)
		if parseErr != nil {
			if skipMalformedJSON && !json.Valid(lines[index]) {
				continue
			}
			return nil, parseErr
		}
		entries = append(entries, entry)
	}
	metadata := SessionMetadata{
		ID: header.ID, CreatedAt: header.Timestamp, CWD: header.CWD, Path: filePath,
		ParentSessionPath: cloneHarnessString(header.ParentSession), Metadata: cloneHarnessRaw(header.Metadata),
	}
	state, err := newSessionStorageState(metadata, entries, false)
	if err != nil {
		return nil, err
	}
	return &JSONLSessionStorage{
		state: state, version: header.Version, header: append([]byte(nil), lines[0]...),
		content: append([]byte(nil), content...), append: appendLine,
	}, nil
}

func nonBlankHarnessLines(content []byte) [][]byte {
	rawLines := bytes.Split(content, []byte{'\n'})
	lines := make([][]byte, 0, len(rawLines))
	for _, line := range rawLines {
		if trimHarnessJSSpace(string(line)) != "" {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines
}

func invalidHarnessSession(filePath, message string) *SessionError {
	return newSessionError(SessionErrorInvalidSession, "Invalid JSONL session file %s: %s", filePath, message)
}

func invalidHarnessEntry(filePath string, line int, message string) *SessionError {
	return newSessionError(SessionErrorInvalidEntry, "Invalid JSONL session file %s: line %d %s", filePath, line, message)
}

func isHarnessJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(trimmed, &object) == nil && object != nil
}

func parseHarnessEntry(line []byte, filePath string, lineNumber int) (SessionTreeEntry, error) {
	if !json.Valid(line) {
		return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "is not valid JSON")
	}
	object, err := parseHarnessObject(line)
	if err != nil {
		return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "is not a valid session entry")
	}
	entry, err := decodeHarnessEntryObject(object)
	if err != nil {
		return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "is not a valid session entry")
	}
	if raw, ok := object["type"]; !ok || !decodeHarnessStringInto(raw, &entry.Type) {
		return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "is missing entry type")
	}
	if raw, ok := object["id"]; !ok || !decodeHarnessStringInto(raw, &entry.ID) || entry.ID == "" {
		return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "is missing entry id")
	}
	if raw, ok := object["parentId"]; !ok || !isHarnessNullableString(raw) {
		return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "has invalid parentId")
	}
	if raw, ok := object["timestamp"]; !ok || !decodeHarnessStringInto(raw, &entry.Timestamp) || entry.Timestamp == "" {
		return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "is missing timestamp")
	}
	if entry.Type == "leaf" {
		if raw, ok := object["targetId"]; !ok || !isHarnessNullableString(raw) {
			return SessionTreeEntry{}, invalidHarnessEntry(filePath, lineNumber, "has invalid targetId")
		}
	}
	entry.raw = append(json.RawMessage(nil), line...)
	return entry, nil
}

func isHarnessNullableString(raw []byte) bool {
	if string(bytes.TrimSpace(raw)) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func (storage *JSONLSessionStorage) Metadata() SessionMetadata {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.metadataValue()
}

func (storage *JSONLSessionStorage) LeafID() (*string, error) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.leafValue()
}

func (storage *JSONLSessionStorage) SetLeafID(leafID *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if leafID != nil {
		if _, ok := storage.state.byID[*leafID]; !ok {
			return newSessionError(SessionErrorNotFound, "Entry %s not found", *leafID)
		}
	}
	id, err := storage.state.createEntryID()
	if err != nil {
		return err
	}
	return storage.appendLockedWithLabel(SessionTreeEntry{
		Type: "leaf", ID: id, ParentID: cloneHarnessString(storage.state.leafID),
		Timestamp: formatHarnessTimestamp(time.Now()), TargetID: cloneHarnessString(leafID), HasTargetID: true,
	}, "session leaf")
}

func (storage *JSONLSessionStorage) CreateEntryID() (string, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.createEntryID()
}

func (storage *JSONLSessionStorage) AppendEntry(entry SessionTreeEntry) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.appendLocked(entry)
}

func (storage *JSONLSessionStorage) appendLocked(entry SessionTreeEntry) error {
	return storage.appendLockedWithLabel(entry, "session entry")
}

func (storage *JSONLSessionStorage) appendLockedWithLabel(entry SessionTreeEntry, label string) error {
	encoded, err := marshalHarnessEntry(entry)
	if err != nil {
		return err
	}
	line := append(append([]byte(nil), encoded...), '\n')
	if storage.append != nil {
		if err := storage.append(line); err != nil {
			return newSessionError(SessionErrorStorage, "Failed to append %s %s: %v", label, entry.ID, err)
		}
	}
	storage.content = append(storage.content, line...)
	entry.raw = append(json.RawMessage(nil), encoded...)
	storage.state.append(entry)
	return nil
}

func (storage *JSONLSessionStorage) Entry(id string) (*SessionTreeEntry, bool) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.entry(id)
}

func (storage *JSONLSessionStorage) EntriesByType(entryType string) []SessionTreeEntry {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.entriesByType(entryType)
}

func (storage *JSONLSessionStorage) Label(id string) (string, bool) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.label(id)
}

func (storage *JSONLSessionStorage) SessionName() (string, bool) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.sessionName()
}

func (storage *JSONLSessionStorage) SessionStats() SessionStats {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.sessionStats()
}

func (storage *JSONLSessionStorage) PathToRootOrCompaction(leafID *string) ([]SessionTreeEntry, error) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.pathToRootOrCompaction(leafID)
}

func (storage *JSONLSessionStorage) Entries(options ...SessionEntryCursorOptions) []SessionTreeEntry {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.state.entriesWithCursor(options...)
}

func (storage *JSONLSessionStorage) Bytes() ([]byte, error) {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return append([]byte(nil), storage.content...), nil
}

func (storage *JSONLSessionStorage) IsPersistent() bool {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.append != nil
}

// SessionVersion exposes the original byte-backed header version to adapters.
func (storage *JSONLSessionStorage) SessionVersion() float64 {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return storage.version
}

func (storage *JSONLSessionStorage) HeaderJSON() []byte {
	storage.mu.RLock()
	defer storage.mu.RUnlock()
	return append([]byte(nil), storage.header...)
}

func cloneHarnessMetadata(metadata SessionMetadata) SessionMetadata {
	copy := metadata
	copy.ParentSessionPath = cloneHarnessString(metadata.ParentSessionPath)
	copy.Metadata = cloneHarnessRaw(metadata.Metadata)
	return copy
}

func encodeHarnessHeader(metadata SessionMetadata) ([]byte, error) {
	encoded, err := marshalHarnessHeader(metadata)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func validateHarnessMetadata(metadata SessionMetadata) error {
	if metadata.ID == "" || metadata.CreatedAt == "" || metadata.CWD == "" {
		return fmt.Errorf("harness: JSONL session metadata requires id, createdAt, and cwd")
	}
	if len(metadata.Metadata) != 0 && !isHarnessJSONObject(metadata.Metadata) {
		return fmt.Errorf("harness: JSONL session metadata must be an object")
	}
	return nil
}
