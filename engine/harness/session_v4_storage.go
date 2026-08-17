package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/internal/uuidv7"
)

// SessionV4Metadata identifies an in-memory v4 session.
type SessionV4Metadata struct {
	ID              string  `json:"id"`
	CreatedAt       int64   `json:"createdAt"`
	ParentSessionID *string `json:"parentSessionId,omitempty"`
}

// JSONLSessionV4Metadata identifies a JSONL-backed v4 session.
type JSONLSessionV4Metadata struct {
	ID                      string          `json:"id"`
	CreatedAt               int64           `json:"createdAt"`
	CWD                     string          `json:"cwd"`
	Path                    string          `json:"path"`
	ModifiedAt              float64         `json:"modifiedAt"`
	SourceFormat            int             `json:"sourceFormat"`
	ParentSessionID         *string         `json:"parentSessionId,omitempty"`
	LegacyParentSessionPath *string         `json:"legacyParentSessionPath,omitempty"`
	Metadata                json.RawMessage `json:"metadata,omitempty"`
}

// SessionV4Storage is the backend-neutral v4 session log contract.
type SessionV4Storage interface {
	Lanes() []SessionV4LanePointer
	CreateLane(lane string, at *string) error
	MoveLane(lane string, to *string) error
	AppendEntry(payload json.RawMessage, lane string) (SessionV4Entry, error)
	AppendRecord(payload json.RawMessage) (SessionV4Record, error)
	Entry(id string) (SessionV4Entry, bool)
	FindEntries(query SessionV4EntryQuery) ([]SessionV4Entry, error)
	FindEntriesOnBranch(query SessionV4BranchQuery) ([]SessionV4Entry, error)
	FindRecords(query SessionV4RecordQuery) ([]SessionV4Record, error)
	FindOpenOperations(lane string, limit *int) ([]SessionV4Record, error)
	Log(options SessionV4LogOptions) ([]SessionV4LogItem, error)
	Name() (string, bool)
	SetName(name string) error
	Label(id string) (string, bool)
	SetLabel(id string, label *string) error
	Stats() SessionStats
}

// SessionV4NameClearer is upstream's `setName(undefined)` half of the storage
// contract. It stays a separate interface so SessionV4Storage keeps its
// published method set for existing implementations.
type SessionV4NameClearer interface {
	ClearName() error
}

func sessionV4NowMS(now func() int64) int64 {
	if now != nil {
		return now()
	}
	return time.Now().UnixMilli()
}

func buildV4EntryMutation(
	state *sessionV4State,
	payload json.RawMessage,
	lane *string,
	parentID *string,
	timestamp int64,
) (SessionV4Mutation, error) {
	members, byName, decodeErr := parseV4Object(payload)
	if decodeErr != nil {
		return SessionV4Mutation{}, newSessionError(SessionErrorInvalidPayload, "Durable payload %s", decodeErr.Error())
	}
	seq := state.nextSequence()
	members = append(members,
		harnessNullableStringMember("parentId", parentID),
		harnessJSONMember{name: "seq", value: json.RawMessage(strconv.Itoa(seq))},
		harnessJSONMember{name: "timestamp", value: json.RawMessage(strconv.FormatInt(timestamp, 10))},
	)
	byName["parentId"] = members[len(members)-3].value
	byName["seq"] = members[len(members)-2].value
	byName["timestamp"] = members[len(members)-1].value
	entry, decodeErr := decodeV4EntryPayload(members, byName)
	if decodeErr != nil {
		return SessionV4Mutation{}, invalidV4FileCause("<payload>", 1, decodeErr)
	}
	return SessionV4Mutation{Kind: "entry", Seq: seq, EntryLane: lane, Entry: &entry}, nil
}

func buildV4RecordMutation(state *sessionV4State, payload json.RawMessage, timestamp int64) (SessionV4Mutation, error) {
	members, byName, decodeErr := parseV4Object(payload)
	if decodeErr != nil {
		return SessionV4Mutation{}, newSessionError(SessionErrorInvalidPayload, "Durable payload %s", decodeErr.Error())
	}
	seq := state.nextSequence()
	members = append(members,
		harnessJSONMember{name: "seq", value: json.RawMessage(strconv.Itoa(seq))},
		harnessJSONMember{name: "timestamp", value: json.RawMessage(strconv.FormatInt(timestamp, 10))},
	)
	byName["seq"] = members[len(members)-2].value
	byName["timestamp"] = members[len(members)-1].value
	record, decodeErr := decodeV4RecordPayload(members, byName)
	if decodeErr != nil {
		return SessionV4Mutation{}, invalidV4FileCause("<payload>", 1, decodeErr)
	}
	return SessionV4Mutation{Kind: "record", Seq: seq, Record: &record}, nil
}

func v4PayloadString(payload json.RawMessage, field string) string {
	var object map[string]json.RawMessage
	_ = json.Unmarshal(payload, &object)
	value, _ := v4String(object[field])
	return value
}

// stageV4EntryAppend runs the shared appendEntry validation and provisioning.
func stageV4EntryAppend(state *sessionV4State, payload json.RawMessage, lane string, timestamp int64) (SessionV4Mutation, error) {
	parentID, err := state.requireLane(lane)
	if err != nil {
		return SessionV4Mutation{}, err
	}
	if err := state.validateUnusedID(v4PayloadString(payload, "id")); err != nil {
		return SessionV4Mutation{}, err
	}
	laneName := lane
	return buildV4EntryMutation(state, payload, &laneName, parentID, timestamp)
}

// stageV4RecordAppend runs the shared appendRecord validation and provisioning.
func stageV4RecordAppend(state *sessionV4State, payload json.RawMessage, timestamp int64) (SessionV4Mutation, error) {
	lane := v4PayloadString(payload, "lane")
	if _, err := state.requireLane(lane); err != nil {
		return SessionV4Mutation{}, err
	}
	if err := state.validateUnusedID(v4PayloadString(payload, "id")); err != nil {
		return SessionV4Mutation{}, err
	}
	if v4PayloadString(payload, "type") == "operation_started" {
		open, err := state.findOpenOperations(lane, nil)
		if err != nil {
			return SessionV4Mutation{}, err
		}
		if len(open) > 0 {
			return SessionV4Mutation{}, newSessionError(
				SessionErrorStorage, "Lane %s already has an open operation %s", lane, open[0].ID,
			)
		}
	}
	return buildV4RecordMutation(state, payload, timestamp)
}

func stageV4CreateLane(state *sessionV4State, lane string, at *string) (SessionV4Mutation, error) {
	if err := state.validateNewLane(lane); err != nil {
		return SessionV4Mutation{}, err
	}
	if err := state.validateTarget(at); err != nil {
		return SessionV4Mutation{}, err
	}
	return SessionV4Mutation{Kind: "lane", Seq: state.nextSequence(), Lane: lane, LeafID: cloneHarnessString(at)}, nil
}

func stageV4MoveLane(state *sessionV4State, lane string, to *string) (SessionV4Mutation, error) {
	if _, err := state.requireLane(lane); err != nil {
		return SessionV4Mutation{}, err
	}
	if err := state.validateTarget(to); err != nil {
		return SessionV4Mutation{}, err
	}
	return SessionV4Mutation{Kind: "lane", Seq: state.nextSequence(), Lane: lane, LeafID: cloneHarnessString(to)}, nil
}

func stageV4SetLabel(state *sessionV4State, id string, label *string) (SessionV4Mutation, error) {
	if err := state.validateTarget(&id); err != nil {
		return SessionV4Mutation{}, err
	}
	return SessionV4Mutation{
		Kind: "fact", Seq: state.nextSequence(), Fact: "label", TargetID: id, Label: cloneHarnessString(label),
	}, nil
}

// InMemorySessionV4Storage keeps the whole mutation log in memory.
type InMemorySessionV4Storage struct {
	mu       sync.Mutex
	metadata SessionV4Metadata
	state    *sessionV4State

	// Now overrides the append timestamp clock (epoch milliseconds).
	Now func() int64
}

func NewInMemorySessionV4Storage(metadata SessionV4Metadata) *InMemorySessionV4Storage {
	metadata.ParentSessionID = cloneHarnessString(metadata.ParentSessionID)
	return &InMemorySessionV4Storage{metadata: metadata, state: newSessionV4State()}
}

func (storage *InMemorySessionV4Storage) fork(metadata SessionV4Metadata, options SessionV4ForkOptions) (*InMemorySessionV4Storage, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutations, err := storage.state.createForkMutations(options)
	if err != nil {
		return nil, err
	}
	forked := NewInMemorySessionV4Storage(metadata)
	forked.Now = storage.Now
	for _, mutation := range mutations {
		if err := forked.state.applyMutation(mutation, nil); err != nil {
			return nil, err
		}
	}
	return forked, nil
}

func (storage *InMemorySessionV4Storage) Metadata() SessionV4Metadata {
	metadata := storage.metadata
	metadata.ParentSessionID = cloneHarnessString(metadata.ParentSessionID)
	return metadata
}

func (storage *InMemorySessionV4Storage) Lanes() []SessionV4LanePointer {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.lanePointers()
}

func (storage *InMemorySessionV4Storage) CreateLane(lane string, at *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4CreateLane(storage.state, lane, at)
	if err != nil {
		return err
	}
	return storage.state.applyMutation(mutation, nil)
}

func (storage *InMemorySessionV4Storage) MoveLane(lane string, to *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4MoveLane(storage.state, lane, to)
	if err != nil {
		return err
	}
	return storage.state.applyMutation(mutation, nil)
}

func (storage *InMemorySessionV4Storage) AppendEntry(payload json.RawMessage, lane string) (SessionV4Entry, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4EntryAppend(storage.state, payload, lane, sessionV4NowMS(storage.Now))
	if err != nil {
		return SessionV4Entry{}, err
	}
	if err := storage.state.applyMutation(mutation, nil); err != nil {
		return SessionV4Entry{}, err
	}
	return mutation.Entry.clone(), nil
}

func (storage *InMemorySessionV4Storage) AppendRecord(payload json.RawMessage) (SessionV4Record, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4RecordAppend(storage.state, payload, sessionV4NowMS(storage.Now))
	if err != nil {
		return SessionV4Record{}, err
	}
	if err := storage.state.applyMutation(mutation, nil); err != nil {
		return SessionV4Record{}, err
	}
	return mutation.Record.clone(), nil
}

func (storage *InMemorySessionV4Storage) Entry(id string) (SessionV4Entry, bool) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.entry(id)
}

func (storage *InMemorySessionV4Storage) FindEntries(query SessionV4EntryQuery) ([]SessionV4Entry, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findEntries(query)
}

func (storage *InMemorySessionV4Storage) FindEntriesOnBranch(query SessionV4BranchQuery) ([]SessionV4Entry, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findEntriesOnBranch(query)
}

func (storage *InMemorySessionV4Storage) FindRecords(query SessionV4RecordQuery) ([]SessionV4Record, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findRecords(query)
}

func (storage *InMemorySessionV4Storage) FindOpenOperations(lane string, limit *int) ([]SessionV4Record, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findOpenOperations(lane, limit)
}

func (storage *InMemorySessionV4Storage) Log(options SessionV4LogOptions) ([]SessionV4LogItem, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.logItems(options)
}

func (storage *InMemorySessionV4Storage) Name() (string, bool) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.state.name == nil {
		return "", false
	}
	return *storage.state.name, true
}

func (storage *InMemorySessionV4Storage) SetName(name string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.applyMutation(SessionV4Mutation{
		Kind: "fact", Seq: storage.state.nextSequence(), Fact: "name", Name: name,
	}, nil)
}

// ClearName records upstream's `setName(undefined)`: the session name is
// dropped durably, and forks of this session start unnamed.
func (storage *InMemorySessionV4Storage) ClearName() error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.applyMutation(SessionV4Mutation{
		Kind: "fact", Seq: storage.state.nextSequence(), Fact: "name", NameCleared: true,
	}, nil)
}

func (storage *InMemorySessionV4Storage) Label(id string) (string, bool) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	label, ok := storage.state.labels[id]
	return label, ok
}

func (storage *InMemorySessionV4Storage) SetLabel(id string, label *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4SetLabel(storage.state, id, label)
	if err != nil {
		return err
	}
	return storage.state.applyMutation(mutation, nil)
}

func (storage *InMemorySessionV4Storage) Stats() SessionStats {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.stats
}

func fileV4Result(err error, format string, arguments ...any) error {
	if err == nil {
		return nil
	}
	code := SessionErrorStorage
	var fileError *FileError
	if errors.As(err, &fileError) && fileError.Code == FileErrorNotFound {
		code = SessionErrorNotFound
	}
	return &SessionError{Code: code, Err: fmt.Errorf(format+": %w", append(arguments, err)...)}
}

func jsonlV4Metadata(header SessionV4Header, path string, modifiedAt float64) JSONLSessionV4Metadata {
	return JSONLSessionV4Metadata{
		ID: header.ID, CreatedAt: header.CreatedAt, CWD: header.CWD, Path: path,
		ModifiedAt: modifiedAt, SourceFormat: 4,
		ParentSessionID:         cloneHarnessString(header.ParentSessionID),
		LegacyParentSessionPath: cloneHarnessString(header.LegacyParentSessionPath),
		Metadata:                cloneHarnessRaw(header.Metadata),
	}
}

// publishV4FileAtomically stages a complete sibling temp file and renames it
// over the destination so crashes never leave a torn session behind.
func publishV4FileAtomically(ctx context.Context, fs FileSystem, destination string, populate func(tempPath string) error) error {
	tempPath := destination + ".tmp"
	if err := populate(tempPath); err != nil {
		_ = fs.Remove(ctx, tempPath, false, true)
		return err
	}
	if err := fileV4Result(fs.RenameFile(ctx, tempPath, destination), "Failed to publish staged file %s", destination); err != nil {
		_ = fs.Remove(ctx, tempPath, false, true)
		return err
	}
	return nil
}

// JSONLSessionV4Storage appends every mutation to one v4 JSONL file.
type JSONLSessionV4Storage struct {
	mu       sync.Mutex
	fs       FileSystem
	metadata JSONLSessionV4Metadata
	state    *sessionV4State

	// Now overrides the append timestamp clock (epoch milliseconds).
	Now func() int64
}

// CreateJSONLSessionV4Storage initializes a new session file holding only the header.
func CreateJSONLSessionV4Storage(ctx context.Context, fs FileSystem, path string, header SessionV4Header) (*JSONLSessionV4Storage, error) {
	encoded, err := MarshalSessionV4Header(header)
	if err != nil {
		return nil, err
	}
	if err := fileV4Result(fs.WriteFile(ctx, path, append(encoded, '\n')), "Failed to initialize session %s", path); err != nil {
		return nil, err
	}
	info, err := fs.FileInfo(ctx, path)
	if err != nil {
		return nil, fileV4Result(err, "Failed to read session metadata %s", path)
	}
	return &JSONLSessionV4Storage{fs: fs, metadata: jsonlV4Metadata(header, path, info.MTimeMS), state: newSessionV4State()}, nil
}

// LoadJSONLSessionV4Storage replays an existing session file, repairing a torn
// or unterminated trailing line in place.
func LoadJSONLSessionV4Storage(ctx context.Context, fs FileSystem, path string) (*JSONLSessionV4Storage, error) {
	content, err := fs.ReadTextFile(ctx, path)
	if err != nil {
		return nil, fileV4Result(err, "Failed to read session %s", path)
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || lines[0] == "" {
		return nil, invalidV4FileCause(path, 1, decodeV4Error(SessionV4DecodeSchema, "is missing a header"))
	}
	header, err := ParseSessionV4Header([]byte(lines[0]), path)
	if err != nil {
		return nil, err
	}
	info, err := fs.FileInfo(ctx, path)
	if err != nil {
		return nil, fileV4Result(err, "Failed to read session metadata %s", path)
	}
	storage := &JSONLSessionV4Storage{fs: fs, metadata: jsonlV4Metadata(header, path, info.MTimeMS), state: newSessionV4State()}
	for index := 1; index < len(lines); index++ {
		line := lines[index]
		mutation, decodeErr := DecodeSessionV4Mutation([]byte(line))
		if decodeErr != nil {
			// Only an unparseable final line is an unacknowledged partial append;
			// a schema-valid-JSON failure is corruption and must not be repaired.
			if index != len(lines)-1 || decodeErr.Kind != SessionV4DecodeSyntax {
				return nil, invalidV4FileCause(path, index+1, decodeErr)
			}
			validPrefix := strings.Join(lines[:index], "\n") + "\n"
			if err := publishV4FileAtomically(ctx, fs, path, func(tempPath string) error {
				return fileV4Result(fs.WriteFile(ctx, tempPath, []byte(validPrefix)), "Failed to stage torn-tail repair %s", path)
			}); err != nil {
				return nil, err
			}
			return storage, nil
		}
		if err := storage.state.applyMutation(mutation, nil); err != nil {
			var mutationErr *SessionError
			if errors.As(err, &mutationErr) && mutationErr.Code == SessionErrorInvalidEntry {
				return nil, invalidV4FileCause(path, index+1, mutationErr)
			}
			return nil, err
		}
	}
	if !strings.HasSuffix(content, "\n") {
		if err := fileV4Result(fs.AppendFile(ctx, path, []byte("\n")), "Failed to repair unterminated session tail %s", path); err != nil {
			return nil, err
		}
	}
	return storage, nil
}

// Fork copies the selected slice of this session into a new session file.
func (storage *JSONLSessionV4Storage) Fork(ctx context.Context, path string, header SessionV4Header, options SessionV4ForkOptions) (*JSONLSessionV4Storage, error) {
	storage.mu.Lock()
	mutations, err := storage.state.createForkMutations(options)
	storage.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := publishV4FileAtomically(ctx, storage.fs, path, func(tempPath string) error {
		target, err := CreateJSONLSessionV4Storage(ctx, storage.fs, tempPath, header)
		if err != nil {
			return err
		}
		for _, mutation := range mutations {
			if err := target.appendMutation(ctx, mutation); err != nil {
				return err
			}
			if err := target.state.applyMutation(mutation, nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return LoadJSONLSessionV4Storage(ctx, storage.fs, path)
}

func (storage *JSONLSessionV4Storage) Metadata() JSONLSessionV4Metadata {
	metadata := storage.metadata
	metadata.ParentSessionID = cloneHarnessString(metadata.ParentSessionID)
	metadata.LegacyParentSessionPath = cloneHarnessString(metadata.LegacyParentSessionPath)
	metadata.Metadata = cloneHarnessRaw(metadata.Metadata)
	return metadata
}

func (storage *JSONLSessionV4Storage) appendMutation(ctx context.Context, mutation SessionV4Mutation) error {
	encoded, err := MarshalSessionV4Mutation(mutation)
	if err != nil {
		return err
	}
	return fileV4Result(
		storage.fs.AppendFile(ctx, storage.metadata.Path, append(encoded, '\n')),
		"Failed to append session %s", storage.metadata.Path,
	)
}

func (storage *JSONLSessionV4Storage) commit(mutation SessionV4Mutation) error {
	if err := storage.appendMutation(context.Background(), mutation); err != nil {
		return err
	}
	return storage.state.applyMutation(mutation, nil)
}

func (storage *JSONLSessionV4Storage) Lanes() []SessionV4LanePointer {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.lanePointers()
}

func (storage *JSONLSessionV4Storage) CreateLane(lane string, at *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4CreateLane(storage.state, lane, at)
	if err != nil {
		return err
	}
	return storage.commit(mutation)
}

func (storage *JSONLSessionV4Storage) MoveLane(lane string, to *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4MoveLane(storage.state, lane, to)
	if err != nil {
		return err
	}
	return storage.commit(mutation)
}

func (storage *JSONLSessionV4Storage) AppendEntry(payload json.RawMessage, lane string) (SessionV4Entry, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4EntryAppend(storage.state, payload, lane, sessionV4NowMS(storage.Now))
	if err != nil {
		return SessionV4Entry{}, err
	}
	if err := storage.commit(mutation); err != nil {
		return SessionV4Entry{}, err
	}
	return mutation.Entry.clone(), nil
}

func (storage *JSONLSessionV4Storage) AppendRecord(payload json.RawMessage) (SessionV4Record, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4RecordAppend(storage.state, payload, sessionV4NowMS(storage.Now))
	if err != nil {
		return SessionV4Record{}, err
	}
	if err := storage.commit(mutation); err != nil {
		return SessionV4Record{}, err
	}
	return mutation.Record.clone(), nil
}

func (storage *JSONLSessionV4Storage) Entry(id string) (SessionV4Entry, bool) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.entry(id)
}

func (storage *JSONLSessionV4Storage) FindEntries(query SessionV4EntryQuery) ([]SessionV4Entry, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findEntries(query)
}

func (storage *JSONLSessionV4Storage) FindEntriesOnBranch(query SessionV4BranchQuery) ([]SessionV4Entry, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findEntriesOnBranch(query)
}

func (storage *JSONLSessionV4Storage) FindRecords(query SessionV4RecordQuery) ([]SessionV4Record, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findRecords(query)
}

func (storage *JSONLSessionV4Storage) FindOpenOperations(lane string, limit *int) ([]SessionV4Record, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.findOpenOperations(lane, limit)
}

func (storage *JSONLSessionV4Storage) Log(options SessionV4LogOptions) ([]SessionV4LogItem, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.logItems(options)
}

func (storage *JSONLSessionV4Storage) Name() (string, bool) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.state.name == nil {
		return "", false
	}
	return *storage.state.name, true
}

func (storage *JSONLSessionV4Storage) SetName(name string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.commit(SessionV4Mutation{
		Kind: "fact", Seq: storage.state.nextSequence(), Fact: "name", Name: name,
	})
}

// ClearName records upstream's `setName(undefined)`: the session name is
// dropped durably, and forks of this session start unnamed.
func (storage *JSONLSessionV4Storage) ClearName() error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.commit(SessionV4Mutation{
		Kind: "fact", Seq: storage.state.nextSequence(), Fact: "name", NameCleared: true,
	})
}

func (storage *JSONLSessionV4Storage) Label(id string) (string, bool) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	label, ok := storage.state.labels[id]
	return label, ok
}

func (storage *JSONLSessionV4Storage) SetLabel(id string, label *string) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	mutation, err := stageV4SetLabel(storage.state, id, label)
	if err != nil {
		return err
	}
	return storage.commit(mutation)
}

func (storage *JSONLSessionV4Storage) Stats() SessionStats {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.state.stats
}

// SessionV4 is the thin tree facade over a v4 storage, mirroring upstream's
// Session class for the main lane plus per-lane views.
type SessionV4 struct {
	storage SessionV4Storage
	nextID  func() string
}

func NewSessionV4(storage SessionV4Storage, nextID ...func() string) *SessionV4 {
	session := &SessionV4{storage: storage}
	if len(nextID) > 0 && nextID[0] != nil {
		session.nextID = nextID[0]
	} else {
		session.nextID = func() string {
			id, err := uuidv7.Generate(time.Now())
			if err != nil {
				return ""
			}
			return id
		}
	}
	return session
}

func (session *SessionV4) LeafID(lane ...string) (*string, error) {
	name := "main"
	if len(lane) > 0 {
		name = lane[0]
	}
	for _, pointer := range session.storage.Lanes() {
		if pointer.Lane == name {
			return pointer.LeafID, nil
		}
	}
	return nil, newSessionError(SessionErrorInvalidLane, "Lane not found: %s", name)
}

func (session *SessionV4) AppendMessage(message json.RawMessage) (string, error) {
	payload, err := marshalHarnessMembers([]harnessJSONMember{
		harnessStringMember("type", "message"),
		harnessStringMember("id", session.nextID()),
		harnessRawMember("message", message),
	})
	if err != nil {
		return "", err
	}
	entry, err := session.storage.AppendEntry(payload, "main")
	if err != nil {
		return "", err
	}
	return entry.ID, nil
}

func (session *SessionV4) AppendCustomEntry(customType string, data json.RawMessage, lane ...string) (string, error) {
	name := "main"
	if len(lane) > 0 {
		name = lane[0]
	}
	members := []harnessJSONMember{
		harnessStringMember("type", "custom"),
		harnessStringMember("id", session.nextID()),
		harnessStringMember("customType", customType),
	}
	if len(data) != 0 {
		members = append(members, harnessRawMember("data", data))
	}
	payload, err := marshalHarnessMembers(members)
	if err != nil {
		return "", err
	}
	entry, err := session.storage.AppendEntry(payload, name)
	if err != nil {
		return "", err
	}
	return entry.ID, nil
}

func (session *SessionV4) FindEntryOnBranch(query SessionV4BranchQuery, lane ...string) (*SessionV4Entry, error) {
	name := "main"
	if len(lane) > 0 {
		name = lane[0]
	}
	if err := assertV4Limit(query.Limit); err != nil {
		return nil, err
	}
	if err := assertV4Cursor(query.AfterSeq); err != nil {
		return nil, err
	}
	if query.Start == "" {
		leaf, err := session.LeafID(name)
		if err != nil {
			return nil, err
		}
		if leaf == nil {
			return nil, nil
		}
		query.Start = *leaf
	}
	one := 1
	query.Limit = &one
	entries, err := session.storage.FindEntriesOnBranch(query)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return &entries[0], nil
}

func (session *SessionV4) Stats() SessionStats {
	return session.storage.Stats()
}
