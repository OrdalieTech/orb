package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SessionV4ForkOptions selects what a fork copies: the main branch up to a
// message entry, or the whole tree.
type SessionV4ForkOptions struct {
	Scope    string
	EntryID  *string
	Position ForkPosition
}

// JSONLSessionV4Metadata identifies a JSONL-backed v4 session.
type JSONLSessionV4Metadata struct {
	StorageVersion          int64           `json:"storageVersion"`
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

func jsonlV4Metadata(header SessionV4Header, path string, modifiedAt float64) JSONLSessionV4Metadata {
	return JSONLSessionV4Metadata{
		StorageVersion: 1, ID: header.ID, CreatedAt: header.CreatedAt, CWD: header.CWD, Path: path,
		ModifiedAt: modifiedAt, SourceFormat: 4,
		ParentSessionID:         clonePointer(header.ParentSessionID),
		LegacyParentSessionPath: clonePointer(header.LegacyParentSessionPath),
		Metadata:                cloneHarnessRaw(header.Metadata),
	}
}

// JSONLSessionV4Storage appends every mutation to one v4 JSONL file.
type JSONLSessionV4Storage struct {
	*TransactionSessionV4Storage
	onClose  func()
	mu       sync.Mutex
	fs       FileSystem
	metadata JSONLSessionV4Metadata

	// Now overrides the append timestamp clock (epoch milliseconds).
	Now func() int64
}

// CreateJSONLSessionV4Storage initializes a new session file holding only the header.
func CreateJSONLSessionV4Storage(ctx context.Context, fs FileSystem, path string, header SessionV4Header) (*JSONLSessionV4Storage, error) {
	releasedHeader := SessionV4TransactionHeader{V: 4, Kind: "header", ID: header.ID, CreatedAt: header.CreatedAt, StorageVersion: 1, CWD: header.CWD, ParentSessionID: header.ParentSessionID, LegacyParentSessionPath: header.LegacyParentSessionPath}
	released, err := CreateJSONLSessionV4TransactionStorage(ctx, fs, path, releasedHeader, nil, nil)
	if err != nil {
		return nil, err
	}
	info, err := fs.FileInfo(ctx, path)
	if err != nil {
		_ = released.Close(ctx)
		_ = fs.Remove(ctx, path, false, true)
		return nil, err
	}
	return &JSONLSessionV4Storage{TransactionSessionV4Storage: released, fs: fs, metadata: jsonlV4Metadata(header, path, info.MTimeMS)}, nil
}

// LoadJSONLSessionV4Storage replays an existing session file, repairing a torn
// or unterminated trailing line in place.
func LoadJSONLSessionV4Storage(ctx context.Context, fs FileSystem, path string) (*JSONLSessionV4Storage, error) {
	released, err := OpenJSONLSessionV4TransactionStorage(ctx, fs, path, nil)
	if err != nil {
		return nil, err
	}
	info, err := fs.FileInfo(ctx, path)
	if err != nil {
		return nil, err
	}
	header := SessionV4Header{ID: released.header.ID, CreatedAt: released.header.CreatedAt, CWD: released.header.CWD, ParentSessionID: released.header.ParentSessionID, LegacyParentSessionPath: released.header.LegacyParentSessionPath}
	return &JSONLSessionV4Storage{TransactionSessionV4Storage: released, fs: fs, metadata: jsonlV4Metadata(header, path, info.MTimeMS)}, nil
}

// Fork copies the selected slice of this session into a new session file.
func (storage *JSONLSessionV4Storage) Fork(ctx context.Context, path string, header SessionV4Header, options SessionV4ForkOptions) (*JSONLSessionV4Storage, error) {
	releasedHeader := SessionV4TransactionHeader{V: 4, Kind: "header", ID: header.ID, CreatedAt: header.CreatedAt, StorageVersion: 1, CWD: header.CWD, ParentSessionID: header.ParentSessionID, LegacyParentSessionPath: header.LegacyParentSessionPath}
	_, err := storage.TransactionSessionV4Storage.Fork(ctx, storage.fs, path, releasedHeader, SessionV4TransactionForkOptions{Scope: options.Scope, Branch: "main", EntryID: options.EntryID, Position: string(options.Position)})
	if err != nil {
		return nil, err
	}
	return LoadJSONLSessionV4Storage(ctx, storage.fs, path)
}

func (storage *JSONLSessionV4Storage) Metadata() JSONLSessionV4Metadata {
	metadata := storage.metadata
	metadata.ParentSessionID = clonePointer(metadata.ParentSessionID)
	metadata.LegacyParentSessionPath = clonePointer(metadata.LegacyParentSessionPath)
	metadata.Metadata = cloneHarnessRaw(metadata.Metadata)
	return metadata
}

// AppendEntry commits one tree entry and returns the stored row.
func (storage *JSONLSessionV4Storage) AppendEntry(payload json.RawMessage, lane string) (json.RawMessage, error) {
	fields := transactionFields(payload)
	if _, ok := fields["parentId"]; !ok {
		return nil, fmt.Errorf("AppendEntry requires an explicit parentId in transaction storage")
	}
	switch kind := transactionString(fields, "type"); kind {
	case "message", "custom", "compaction", "branch_summary":
	default:
		return nil, fmt.Errorf("Entry type %s is not a v4 session entry", kind)
	}
	if _, err := storage.Commit(context.Background(), []json.RawMessage{transactionObject("kind", "entry", "entry", payload)}); err != nil {
		return nil, err
	}
	id := transactionString(fields, "id")
	entries, err := storage.GetEntries([]string{id})
	if err != nil {
		return nil, err
	}
	return entries[id], nil
}

func (storage *JSONLSessionV4Storage) AppendRecord(json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("AppendRecord is no longer supported by transaction storage")
}

func (storage *JSONLSessionV4Storage) Commit(ctx context.Context, writes []json.RawMessage) (SessionV4CommitResult, error) {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	storage.TransactionSessionV4Storage.Now = storage.Now
	return storage.TransactionSessionV4Storage.Commit(ctx, writes)
}

func (storage *JSONLSessionV4Storage) Close(ctx context.Context) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	err := storage.TransactionSessionV4Storage.Close(ctx)
	if storage.onClose != nil {
		storage.onClose()
		storage.onClose = nil
	}
	return err
}

func (metadata JSONLSessionV4Metadata) MarshalJSON() ([]byte, error) {
	fields := []any{"id", metadata.ID, "createdAt", metadata.CreatedAt, "storageVersion", metadata.StorageVersion, "cwd", metadata.CWD, "path", metadata.Path, "modifiedAt", metadata.ModifiedAt}
	if metadata.ParentSessionID != nil {
		fields = append(fields, "parentSessionId", metadata.ParentSessionID)
	}
	if metadata.LegacyParentSessionPath != nil {
		fields = append(fields, "legacyParentSessionPath", metadata.LegacyParentSessionPath)
	}
	return transactionObject(fields...), nil
}

func sessionV4NowMS(now func() int64) int64 {
	if now != nil {
		return now()
	}
	return time.Now().UnixMilli()
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
