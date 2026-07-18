package harness

import (
	"encoding/json"
	"fmt"
	"time"
)

// SessionErrorCode is the stable failure classification used by harness
// storage, repositories, and tree operations.
type SessionErrorCode string

const (
	SessionErrorNotFound       SessionErrorCode = "not_found"
	SessionErrorInvalidSession SessionErrorCode = "invalid_session"
	SessionErrorInvalidEntry   SessionErrorCode = "invalid_entry"
	SessionErrorInvalidFork    SessionErrorCode = "invalid_fork_target"
	SessionErrorStorage        SessionErrorCode = "storage"
	SessionErrorUnknown        SessionErrorCode = "unknown"
)

// SessionError preserves upstream's machine-readable error code.
type SessionError struct {
	Code SessionErrorCode
	Err  error
}

func (failure *SessionError) Error() string {
	if failure == nil {
		return ""
	}
	if failure.Err != nil {
		return failure.Err.Error()
	}
	return string(failure.Code)
}

func (failure *SessionError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Err
}

func newSessionError(code SessionErrorCode, format string, arguments ...any) *SessionError {
	return &SessionError{Code: code, Err: fmt.Errorf(format, arguments...)}
}

func formatHarnessTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

// SessionMetadata is shared by memory and JSONL repositories. JSONL-only
// fields are omitted for memory sessions.
type SessionMetadata struct {
	ID                string          `json:"id"`
	CreatedAt         string          `json:"createdAt"`
	CWD               string          `json:"cwd,omitempty"`
	Path              string          `json:"path,omitempty"`
	ParentSessionPath *string         `json:"parentSessionPath,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// SessionTreeEntry is the v3 harness session union. Raw JSON members stay
// opaque so unknown application data can round-trip without coercion.
type SessionTreeEntry struct {
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
	TargetID         *string
	HasTargetID      bool
	Label            *string
	Name             string

	raw json.RawMessage
}

// RawJSON returns the original object for entries rehydrated from JSONL.
func (entry SessionTreeEntry) RawJSON() json.RawMessage {
	return cloneHarnessRaw(entry.raw)
}

func (entry SessionTreeEntry) clone() SessionTreeEntry {
	copy := entry
	copy.ParentID = cloneHarnessString(entry.ParentID)
	copy.ActiveToolNames = cloneHarnessStrings(entry.ActiveToolNames)
	copy.Message = cloneHarnessRaw(entry.Message)
	copy.Details = cloneHarnessRaw(entry.Details)
	copy.FromHook = cloneHarnessBool(entry.FromHook)
	copy.Data = cloneHarnessRaw(entry.Data)
	copy.Content = cloneHarnessRaw(entry.Content)
	copy.TargetID = cloneHarnessString(entry.TargetID)
	copy.Label = cloneHarnessString(entry.Label)
	copy.raw = cloneHarnessRaw(entry.raw)
	return copy
}

func cloneHarnessEntries(entries []SessionTreeEntry) []SessionTreeEntry {
	copy := make([]SessionTreeEntry, len(entries))
	for index := range entries {
		copy[index] = entries[index].clone()
	}
	return copy
}

func cloneHarnessString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneHarnessBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneHarnessRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneHarnessStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

// SessionStorage is the backend-neutral session tree contract.
type SessionStorage interface {
	Metadata() SessionMetadata
	LeafID() (*string, error)
	SetLeafID(*string) error
	CreateEntryID() (string, error)
	AppendEntry(SessionTreeEntry) error
	Entry(string) (*SessionTreeEntry, bool)
	EntriesByType(string) []SessionTreeEntry
	Label(string) (string, bool)
	PathToRoot(*string) ([]SessionTreeEntry, error)
	Entries() []SessionTreeEntry
}

// ByteSessionStorage exposes the exact serialized representation of a
// byte-backed session.
type ByteSessionStorage interface {
	SessionStorage
	Bytes() ([]byte, error)
	HeaderJSON() []byte
}

type ForkPosition string

const (
	ForkBefore ForkPosition = "before"
	ForkAt     ForkPosition = "at"
)

type SessionModel struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

type SessionContext struct {
	ThinkingLevel   string        `json:"thinkingLevel"`
	Model           *SessionModel `json:"model"`
	ActiveToolNames []string      `json:"activeToolNames"`
	Messages        []any         `json:"messages"`
}

// Session is the storage-backed facade used by embedders and repositories.
type Session struct {
	storage             SessionStorage
	contextBuildOptions SessionContextBuildOptions
}

func NewSession(storage SessionStorage, options ...SessionContextBuildOptions) *Session {
	var contextOptions SessionContextBuildOptions
	if len(options) > 0 {
		contextOptions = cloneSessionContextBuildOptions(options[0])
	}
	return &Session{storage: storage, contextBuildOptions: contextOptions}
}

func (session *Session) Storage() SessionStorage {
	if session == nil {
		return nil
	}
	return session.storage
}

func (session *Session) Metadata() SessionMetadata {
	if session == nil || session.storage == nil {
		return SessionMetadata{}
	}
	return session.storage.Metadata()
}

func (session *Session) Context(options ...SessionContextBuildOptions) (SessionContext, error) {
	if session == nil || session.storage == nil {
		return SessionContext{ThinkingLevel: "off", Messages: []any{}}, nil
	}
	leaf, err := session.storage.LeafID()
	if err != nil {
		return SessionContext{}, err
	}
	entries, err := session.storage.PathToRoot(leaf)
	if err != nil {
		return SessionContext{}, err
	}
	return BuildSessionContext(entries, mergeSessionContextBuildOptions(session.contextBuildOptions, options...)), nil
}

func (session *Session) ContextEntries(options ...SessionContextBuildOptions) ([]SessionTreeEntry, error) {
	if session == nil || session.storage == nil {
		return []SessionTreeEntry{}, nil
	}
	leaf, err := session.storage.LeafID()
	if err != nil {
		return nil, err
	}
	entries, err := session.storage.PathToRoot(leaf)
	if err != nil {
		return nil, err
	}
	return BuildContextEntries(entries, mergeSessionContextBuildOptions(session.contextBuildOptions, options...)), nil
}

func (session *Session) Name() (string, bool, error) {
	if session == nil || session.storage == nil {
		return "", false, nil
	}
	entries := session.storage.EntriesByType("session_info")
	if len(entries) == 0 {
		return "", false, nil
	}
	name := trimHarnessJSSpace(entries[len(entries)-1].Name)
	if name == "" {
		return "", false, nil
	}
	return name, true, nil
}
