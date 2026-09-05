package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/partialjson"
	"github.com/OrdalieTech/orb/internal/uuidv7"
)

type SessionV4Address struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Kind      string `json:"kind"`
}
type SessionV4StoredValue struct {
	Address SessionV4Address `json:"address"`
	Value   json.RawMessage  `json:"value"`
	Seq     int64            `json:"seq"`
}
type SessionV4ListElement struct {
	Seq   int64           `json:"seq"`
	Value json.RawMessage `json:"value"`
}
type SessionV4TransactionStats struct {
	MessageCount int      `json:"messageCount"`
	Usage        ai.Usage `json:"usage"`
}
type SessionV4CommitResult struct {
	SessionV4TransactionResult
	Stats SessionV4TransactionStats `json:"stats"`
}
type SessionV4Scan struct {
	Order          string
	Type           string
	CustomType     string
	FromSeq, ToSeq *int64
	Limit          *int
}
type SessionV4ListReadOptions struct {
	Order  string
	Cursor *int64
	Limit  *int
}
type SessionV4BranchScan struct {
	Start                string
	Order                string
	StopAtID, StopAtType string
	Type, CustomType     string
	Cursor               *int64
	Limit                *int
}

// SessionV4TransactionStorage is a companion contract: upstream's transaction
// model does not widen SessionV4Storage's published method set (D37).
type SessionV4TransactionStorage interface {
	Commit(context.Context, []json.RawMessage) (SessionV4CommitResult, error)
	GetEntries([]string) (map[string]json.RawMessage, error)
	GetValue(SessionV4Address) (SessionV4StoredValue, bool, error)
	ScanValues(SessionV4Address) ([]SessionV4StoredValue, error)
	ReadList(SessionV4Address, SessionV4ListReadOptions) ([]SessionV4ListElement, error)
	ScanEntries(SessionV4Scan) ([]json.RawMessage, error)
	ScanUsage(SessionV4Scan) ([]json.RawMessage, error)
	ScanBranch(SessionV4BranchScan) ([]json.RawMessage, error)
	TransactionStats() (SessionV4TransactionStats, error)
	Close(context.Context) error
}

type transactionState struct {
	nextSeq    int64
	entries    map[string]json.RawMessage
	entryOrder []string
	usage      map[string]json.RawMessage
	usageOrder []string
	values     map[string]SessionV4StoredValue
	valueOrder []string
	lists      map[string][]SessionV4ListElement
	stats      SessionV4TransactionStats
}

func newTransactionState() *transactionState {
	return &transactionState{nextSeq: 1, entries: map[string]json.RawMessage{}, usage: map[string]json.RawMessage{}, values: map[string]SessionV4StoredValue{}, lists: map[string][]SessionV4ListElement{}}
}
func (state *transactionState) validate(writes []json.RawMessage) error {
	return ValidateSessionV4Transaction(writes, state.nextSeq, func(id string) bool { return state.entries[id] != nil || state.usage[id] != nil }, func(id string) bool { return state.entries[id] != nil })
}
func transactionFields(raw json.RawMessage) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	return fields
}
func transactionString(fields map[string]json.RawMessage, key string) string {
	value, _ := v4String(fields[key])
	return value
}
func transactionSeq(fields map[string]json.RawMessage) int64 {
	value, _ := v4SafeInteger(fields["seq"])
	return value
}
func transactionKey(address SessionV4Address) string { return address.Namespace + "\x00" + address.Key }
func (state *transactionState) apply(writes []json.RawMessage) {
	for _, raw := range writes {
		fields := transactionFields(raw)
		seq := transactionSeq(fields)
		kind := transactionString(fields, "kind")
		id := transactionString(fields, "id")
		switch kind {
		case "entry", "usage":
			members, _, _ := parseV4Object(raw)
			retained := make([]harnessJSONMember, 0, len(members)-1)
			for _, member := range members {
				if member.name != "kind" {
					retained = append(retained, member)
				}
			}
			value, _ := marshalHarnessMembers(retained)
			if kind == "entry" {
				state.entries[id] = value
				state.entryOrder = append(state.entryOrder, id)
				if transactionString(fields, "type") == "message" {
					state.stats.MessageCount++
				}
			} else {
				state.usage[id] = value
				state.usageOrder = append(state.usageOrder, id)
				var usage ai.Usage
				_ = json.Unmarshal(fields["usage"], &usage)
				state.stats.Usage = addTransactionUsage(state.stats.Usage, usage)
			}
		case "value", "list":
			address := SessionV4Address{Namespace: transactionString(fields, "namespace"), Key: transactionString(fields, "key"), Kind: kind}
			key := transactionKey(address)
			if transactionString(fields, "op") == "delete" {
				if kind == "value" {
					delete(state.values, key)
					for i, k := range state.valueOrder {
						if k == key {
							state.valueOrder = append(state.valueOrder[:i], state.valueOrder[i+1:]...)
							break
						}
					}
				} else {
					delete(state.lists, key)
				}
			} else if kind == "value" {
				if _, exists := state.values[key]; !exists {
					state.valueOrder = append(state.valueOrder, key)
				}
				state.values[key] = SessionV4StoredValue{Address: address, Value: bytes.Clone(fields["value"]), Seq: seq}
			} else {
				state.lists[key] = append(state.lists[key], SessionV4ListElement{Seq: seq, Value: bytes.Clone(fields["value"])})
			}
		}
		state.nextSeq = seq + 1
	}
}
func addTransactionUsage(left, right ai.Usage) ai.Usage {
	left.Input += right.Input
	left.Output += right.Output
	left.CacheRead += right.CacheRead
	left.CacheWrite += right.CacheWrite
	left.TotalTokens += right.TotalTokens
	left.Cost.Input += right.Cost.Input
	left.Cost.Output += right.Cost.Output
	left.Cost.CacheRead += right.Cost.CacheRead
	left.Cost.CacheWrite += right.Cost.CacheWrite
	left.Cost.Total += right.Cost.Total
	add := func(a, b *int64) *int64 {
		if a == nil && b == nil {
			return nil
		}
		var value int64
		if a != nil {
			value += *a
		}
		if b != nil {
			value += *b
		}
		return &value
	}
	left.CacheWrite1h = add(left.CacheWrite1h, right.CacheWrite1h)
	left.Reasoning = add(left.Reasoning, right.Reasoning)
	return left
}

// TransactionSessionV4Storage is the in-memory or JSONL-backed released storage.
// Its lock serializes complete validation/persistence/publication transactions.
type TransactionSessionV4Storage struct {
	mu        sync.Mutex
	state     *transactionState
	fs        FileSystem
	path      string
	header    SessionV4TransactionHeader
	legacy    *transactionMigration
	headerRaw json.RawMessage
	closed    bool
	Now       func() int64
}

func NewMemorySessionV4TransactionStorage() *TransactionSessionV4Storage {
	return &TransactionSessionV4Storage{state: newTransactionState()}
}
func (storage *TransactionSessionV4Storage) assertOpen() error {
	if storage.closed {
		if storage.fs == nil {
			return fmt.Errorf("MemoryStorage is closed")
		}
		return fmt.Errorf("JsonlStorage is closed")
	}
	return nil
}

//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func (storage *TransactionSessionV4Storage) Commit(ctx context.Context, writes []json.RawMessage) (SessionV4CommitResult, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return SessionV4CommitResult{}, err
	}
	now := time.Now().UnixMilli()
	if storage.Now != nil {
		now = storage.Now()
	}
	if storage.legacy != nil && len(writes) > 0 {
		return storage.upgradeLegacy(ctx, writes, now)
	}
	prepared, err := PrepareSessionV4Transaction(writes, storage.state.nextSeq, now)
	if err != nil {
		return SessionV4CommitResult{}, err
	}
	if err = storage.state.validate(prepared.Writes); err != nil {
		return SessionV4CommitResult{}, err
	}
	if len(writes) > 0 && storage.fs != nil {
		line, marshalErr := MarshalSessionV4Transaction(prepared.Writes)
		if marshalErr != nil {
			return SessionV4CommitResult{}, marshalErr
		}
		if err = storage.fs.AppendFile(ctx, storage.path, line); err != nil {
			return SessionV4CommitResult{}, fmt.Errorf("Failed to append JSONL storage %s: %w", storage.path, err)
		}
	}
	storage.state.apply(prepared.Writes)
	return SessionV4CommitResult{SessionV4TransactionResult: prepared.Result, Stats: storage.currentStats()}, nil
}
func (storage *TransactionSessionV4Storage) GetEntries(ids []string) (map[string]json.RawMessage, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return nil, err
	}
	found := map[string]json.RawMessage{}
	for _, id := range ids {
		if raw, ok := storage.state.entries[id]; ok {
			found[id] = bytes.Clone(raw)
		}
	}
	return found, nil
}
func (storage *TransactionSessionV4Storage) GetValue(address SessionV4Address) (SessionV4StoredValue, bool, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return SessionV4StoredValue{}, false, err
	}
	value, found := storage.state.values[transactionKey(address)]
	value.Value = bytes.Clone(value.Value)
	return value, found, nil
}
func (storage *TransactionSessionV4Storage) ScanValues(prefix SessionV4Address) ([]SessionV4StoredValue, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return nil, err
	}
	values := []SessionV4StoredValue{}
	for _, value := range storage.state.values {
		if value.Address.Namespace == prefix.Namespace && strings.HasPrefix(value.Address.Key, prefix.Key) {
			value.Value = bytes.Clone(value.Value)
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Address.Key < values[j].Address.Key })
	return values, nil
}
func (storage *TransactionSessionV4Storage) ReadList(address SessionV4Address, options SessionV4ListReadOptions) ([]SessionV4ListElement, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return nil, err
	}
	limit := 1000
	if options.Limit != nil {
		limit = *options.Limit
	}
	if limit <= 0 || int64(limit) > 1<<53-1 {
		return nil, fmt.Errorf("List read limit must be a positive safe integer")
	}
	if limit > 10000 {
		limit = 10000
	}
	elements := storage.state.lists[transactionKey(address)]
	result := []SessionV4ListElement{}
	for offset := range elements {
		index := offset
		if options.Order == "desc" {
			index = len(elements) - 1 - offset
		}
		element := elements[index]
		if options.Cursor != nil && ((options.Order == "desc" && element.Seq >= *options.Cursor) || (options.Order != "desc" && element.Seq <= *options.Cursor)) {
			continue
		}
		element.Value = bytes.Clone(element.Value)
		result = append(result, element)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}
func transactionScan(rows map[string]json.RawMessage, order []string, query SessionV4Scan) []json.RawMessage {
	result := []json.RawMessage{}
	for offset := range order {
		if query.Limit != nil && len(result) >= *query.Limit {
			break
		}
		index := offset
		if query.Order == "desc" {
			index = len(order) - 1 - offset
		}
		raw := rows[order[index]]
		fields := transactionFields(raw)
		seq := transactionSeq(fields)
		if (query.Type != "" && transactionString(fields, "type") != query.Type) || (query.CustomType != "" && transactionString(fields, "customType") != query.CustomType) || (query.FromSeq != nil && seq < *query.FromSeq) || (query.ToSeq != nil && seq > *query.ToSeq) {
			continue
		}
		result = append(result, bytes.Clone(raw))
	}
	return result
}
func (storage *TransactionSessionV4Storage) ScanEntries(query SessionV4Scan) ([]json.RawMessage, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return nil, err
	}
	return transactionScan(storage.state.entries, storage.state.entryOrder, query), nil
}
func (storage *TransactionSessionV4Storage) ScanUsage(query SessionV4Scan) ([]json.RawMessage, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return nil, err
	}
	query.Type = ""
	query.CustomType = ""
	return transactionScan(storage.state.usage, storage.state.usageOrder, query), nil
}

//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func (storage *TransactionSessionV4Storage) ScanBranch(query SessionV4BranchScan) ([]json.RawMessage, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return nil, err
	}
	raw, found := storage.state.entries[query.Start]
	if !found {
		return nil, fmt.Errorf("Unknown branch start: %s", query.Start)
	}
	path := []json.RawMessage{}
	for {
		path = append(path, raw)
		fields := transactionFields(raw)
		if bytes.Equal(fields["parentId"], []byte("null")) {
			break
		}
		raw, found = storage.state.entries[transactionString(fields, "parentId")]
		if !found {
			return nil, fmt.Errorf("Corrupt branch: missing parent")
		}
	}
	if query.Order == "oldestFirst" {
		for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
	}
	result := []json.RawMessage{}
	for _, raw := range path {
		fields := transactionFields(raw)
		seq := transactionSeq(fields)
		matches := (query.Type == "" || transactionString(fields, "type") == query.Type) && (query.CustomType == "" || transactionString(fields, "customType") == query.CustomType) && (query.Cursor == nil || (query.Order == "oldestFirst" && seq > *query.Cursor) || (query.Order != "oldestFirst" && seq < *query.Cursor))
		if matches && (query.Limit == nil || len(result) < *query.Limit) {
			result = append(result, bytes.Clone(raw))
		}
		if transactionString(fields, "id") == query.StopAtID || transactionString(fields, "type") == query.StopAtType {
			break
		}
	}
	return result, nil
}
func (storage *TransactionSessionV4Storage) TransactionStats() (SessionV4TransactionStats, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if err := storage.assertOpen(); err != nil {
		return SessionV4TransactionStats{}, err
	}
	stats := storage.currentStats()
	stats.Usage = *cloneHarnessUsage(&stats.Usage)
	return stats, nil
}
func (storage *TransactionSessionV4Storage) Close(_ context.Context) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	storage.closed = true
	return nil
}

//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func publishTransactionFile(ctx context.Context, fs FileSystem, path string, content []byte) error {
	temp := path + ".tmp"
	if err := fs.WriteFile(ctx, temp, content); err != nil {
		_ = fs.Remove(ctx, temp, false, true)
		return fmt.Errorf("Failed to stage JSONL storage %s: %w", path, err)
	}
	if err := fs.RenameFile(ctx, temp, path); err != nil {
		_ = fs.Remove(ctx, temp, false, true)
		return fmt.Errorf("Failed to publish JSONL storage %s: %w", path, err)
	}
	return nil
}

func CreateJSONLSessionV4TransactionStorage(ctx context.Context, fs FileSystem, path string, header SessionV4TransactionHeader, initialWrites []json.RawMessage, now func() int64) (*TransactionSessionV4Storage, error) {
	storage := NewMemorySessionV4TransactionStorage()
	storage.fs = fs
	storage.path = path
	storage.header = header
	storage.Now = now
	timestamp := time.Now().UnixMilli()
	if now != nil {
		timestamp = now()
	}
	prepared, err := PrepareSessionV4Transaction(initialWrites, 1, timestamp)
	if err != nil {
		return nil, err
	}
	if err = storage.state.validate(prepared.Writes); err != nil {
		return nil, err
	}
	encoded, err := marshalHarnessValue(header)
	if err != nil {
		return nil, err
	}
	content := append(encoded, '\n')
	if len(initialWrites) > 0 {
		line, lineErr := MarshalSessionV4Transaction(prepared.Writes)
		if lineErr != nil {
			return nil, lineErr
		}
		content = append(content, line...)
	}
	if err = publishTransactionFile(ctx, fs, path, content); err != nil {
		return nil, err
	}
	storage.state.apply(prepared.Writes)
	return storage, nil
}

//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func parseSessionV4Transaction(line []byte) ([]json.RawMessage, error) {
	var writes []json.RawMessage
	trimmed := bytes.TrimSpace(line)
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("Invalid JSONL transaction: not valid JSON")
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &writes); err != nil {
			return nil, err
		}
	} else {
		writes = []json.RawMessage{bytes.Clone(trimmed)}
	}
	for index, write := range writes {
		normalized, normalizeErr := partialjson.StringifyStreamingJSON(string(write))
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		writes[index] = normalized
		_, fields, err := parseV4Object(write)
		if err != nil {
			return nil, fmt.Errorf("Invalid JSONL transaction write")
		}
		if _, ok := transactionInteger(fields["seq"], 1); !ok {
			return nil, fmt.Errorf("Invalid JSONL write seq")
		}
		kind := transactionString(fields, "kind")
		switch kind {
		case "entry":
			if _, ok := transactionInteger(fields["timestamp"], 0); !ok {
				return nil, fmt.Errorf("Invalid JSONL entry timestamp")
			}
		case "usage":
		case "value", "list":
			op := transactionString(fields, "op")
			if op != "delete" && ((kind == "value" && op != "set") || (kind == "list" && op != "append")) {
				return nil, fmt.Errorf("Invalid JSONL %s operation: %s", kind, op)
			}
		default:
			return nil, fmt.Errorf("Invalid JSONL write kind: %s", kind)
		}
	}
	return writes, nil
}

//nolint:staticcheck // Error capitalization matches the upstream storage protocol.
func OpenJSONLSessionV4TransactionStorage(ctx context.Context, fs FileSystem, path string, now func() int64) (*TransactionSessionV4Storage, error) {
	text, err := fs.ReadTextFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("Failed to read JSONL storage %s: %w", path, err)
	}
	content := []byte(text)
	last := bytes.LastIndexByte(content, '\n')
	if last < 0 {
		return nil, fmt.Errorf("Invalid JSONL storage %s: missing header", path)
	}
	torn := last != len(content)-1
	lines := bytes.Split(content[:last], []byte{'\n'})
	if len(lines[0]) == 0 {
		return nil, fmt.Errorf("Invalid JSONL storage %s: missing header", path)
	}
	if legacyHeader, ok := parseTransactionLegacyHeader(lines[0]); ok {
		return openTransactionLegacyV3(ctx, fs, path, legacyHeader, lines[1:], now)
	}
	header, err := DecodeSessionV4TransactionHeader(lines[0])
	if err != nil {
		return nil, &transactionStorageError{message: fmt.Sprintf("Invalid JSONL storage %s: invalid header", path), cause: err}
	}
	if header.StorageVersion != 1 {
		return nil, fmt.Errorf("Session %s uses unsupported storage version %d", header.ID, header.StorageVersion)
	}
	storage := NewMemorySessionV4TransactionStorage()
	storage.fs = fs
	storage.path = path
	storage.header = header
	storage.Now = now
	for index, line := range lines[1:] {
		writes, parseErr := parseSessionV4Transaction(line)
		if parseErr == nil {
			parseErr = storage.state.validate(writes)
		}
		if parseErr != nil {
			return nil, &transactionStorageError{message: fmt.Sprintf("Invalid JSONL storage %s: line %d", path, index+2), cause: parseErr}
		}
		storage.state.apply(writes)
	}
	if header.NextSeq != nil && *header.NextSeq > storage.state.nextSeq {
		storage.state.nextSeq = *header.NextSeq
	}
	if torn {
		if err = publishTransactionFile(ctx, fs, path, content[:last+1]); err != nil {
			return nil, err
		}
	}
	return storage, nil
}

func (storage *TransactionSessionV4Storage) currentStats() SessionV4TransactionStats {
	stats := storage.state.stats
	if storage.legacy != nil {
		stats.Usage = storage.legacy.ImportedUsage
	}
	return stats
}

type transactionLegacyHeader struct {
	ID            string  `json:"id"`
	Timestamp     string  `json:"timestamp"`
	CWD           string  `json:"cwd"`
	ParentSession *string `json:"parentSession,omitempty"`
}

func parseTransactionLegacyHeader(raw []byte) (transactionLegacyHeader, bool) {
	var header transactionLegacyHeader
	fields := transactionFields(raw)
	version, ok := transactionInteger(fields["version"], 0)
	_, idOK := v4String(fields["id"])
	_, cwdOK := v4String(fields["cwd"])
	timestamp, timeOK := v4String(fields["timestamp"])
	_, parseErr := time.Parse(time.RFC3339Nano, timestamp)
	valid := ok && version == 3 && transactionString(fields, "type") == "session" && idOK && cwdOK && timeOK && parseErr == nil
	if parent, found := fields["parentSession"]; found {
		_, parentOK := v4String(parent)
		valid = valid && parentOK
	}
	if !valid {
		return header, false
	}
	if json.Unmarshal(raw, &header) != nil {
		return header, false
	}
	return header, true
}
func openTransactionLegacyV3(ctx context.Context, fs FileSystem, path string, legacyHeader transactionLegacyHeader, lines [][]byte, now func() int64) (*TransactionSessionV4Storage, error) {
	migration, err := normalizeTransactionLegacyV3(lines, mintTransactionLegacyID)
	if err != nil {
		return nil, err
	}
	stamp, _ := time.Parse(time.RFC3339Nano, legacyHeader.Timestamp)
	header := SessionV4TransactionHeader{V: 4, Kind: "header", ID: legacyHeader.ID, StorageVersion: 1, CreatedAt: stamp.UnixMilli(), CWD: legacyHeader.CWD, NextSeq: &migration.NextSeq}
	if legacyHeader.ParentSession != nil {
		parentText, readErr := fs.ReadTextFile(ctx, *legacyHeader.ParentSession)
		if readErr == nil {
			line := strings.SplitN(parentText, "\n", 2)[0]
			if parent, decodeErr := DecodeSessionV4TransactionHeader([]byte(line)); decodeErr == nil {
				header.ParentSessionID = &parent.ID
			} else if parent, ok := parseTransactionLegacyHeader([]byte(line)); ok {
				header.ParentSessionID = &parent.ID
			}
		}
		if header.ParentSessionID == nil {
			header.LegacyParentSessionPath = cloneHarnessString(legacyHeader.ParentSession)
		}
	}
	pairs := []any{"v", 4, "kind", "header", "id", header.ID, "createdAt", header.CreatedAt, "storageVersion", 1, "cwd", header.CWD}
	if header.ParentSessionID != nil {
		pairs = append(pairs, "parentSessionId", header.ParentSessionID)
	}
	if header.LegacyParentSessionPath != nil {
		pairs = append(pairs, "legacyParentSessionPath", header.LegacyParentSessionPath)
	}
	pairs = append(pairs, "nextSeq", migration.NextSeq)
	storage := NewMemorySessionV4TransactionStorage()
	storage.fs = fs
	storage.path = path
	storage.header = header
	storage.headerRaw = transactionObject(pairs...)
	storage.Now = now
	storage.legacy = &migration
	if err = storage.state.validate(migration.Writes); err != nil {
		return nil, err
	}
	storage.state.apply(migration.Writes)
	return storage, nil
}
func (storage *TransactionSessionV4Storage) upgradeLegacy(ctx context.Context, writes []json.RawMessage, timestamp int64) (SessionV4CommitResult, error) {
	id, err := uuidv7.Generate(time.UnixMilli(timestamp))
	if err != nil {
		return SessionV4CommitResult{}, err
	}
	adjustment := transactionObject("kind", "usage", "row", transactionObject("id", id, "usage", storage.legacy.ImportedUsage, "adjustment", true, "details", transactionObject("source", "v3-import")))
	combined := append([]json.RawMessage{adjustment}, writes...)
	prepared, err := PrepareSessionV4Transaction(combined, storage.state.nextSeq, timestamp)
	if err != nil {
		return SessionV4CommitResult{}, err
	}
	if err = storage.state.validate(prepared.Writes); err != nil {
		return SessionV4CommitResult{}, err
	}
	nextSeq := prepared.Result.FirstSeq + int64(len(prepared.Writes))
	members, _, _ := parseV4Object(storage.headerRaw)
	members = transactionSetMember(members, harnessJSONMember{name: "nextSeq", value: mustHarnessJSON(nextSeq)})
	header, err := marshalHarnessMembers(members)
	if err != nil {
		return SessionV4CommitResult{}, err
	}
	content := append(header, '\n')
	for _, write := range storage.legacy.Writes {
		content = append(content, write...)
		content = append(content, '\n')
	}
	line, err := MarshalSessionV4Transaction(prepared.Writes)
	if err != nil {
		return SessionV4CommitResult{}, err
	}
	content = append(content, line...)
	if err = publishTransactionFile(ctx, storage.fs, storage.path, content); err != nil {
		return SessionV4CommitResult{}, err
	}
	storage.state.apply(prepared.Writes)
	storage.legacy = nil
	prepared.Result.FirstSeq++
	prepared.Result.Seqs = prepared.Result.Seqs[1:]
	return SessionV4CommitResult{SessionV4TransactionResult: prepared.Result, Stats: storage.state.stats}, nil
}

type transactionStorageError struct {
	message string
	cause   error
}

func (err *transactionStorageError) Error() string { return err.message }
func (err *transactionStorageError) Unwrap() error { return err.cause }
