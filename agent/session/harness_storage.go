package session

import (
	"bytes"
	"cmp"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/nodepath"
)

// ErrHarnessStorageReplacement prevents lifecycle operations from silently
// detaching a runtime from its harness repository.
var ErrHarnessStorageReplacement = errors.New("session: harness-backed session replacement requires a SessionRepo")

func WithHarnessRepo(repo harness.SessionRepo) Option {
	return func(options *managerOptions) { options.harnessRepo = repo }
}

// FromHarnessStorage exposes one harness session through the coding-agent
// SessionManager API. Both views retain the same storage as their source of
// truth; no snapshot is made.
func FromHarnessStorage(storage harness.SessionStorage, options ...Option) (*SessionManager, error) {
	if storage == nil {
		return nil, errors.New("session: nil harness storage")
	}
	metadata := storage.Metadata()
	if metadata.ID == "" || metadata.CreatedAt == "" {
		return nil, errors.New("session: harness storage metadata is incomplete")
	}
	resolved := applyOptions(options)
	cwd := resolved.cwdOverride
	if cwd == "" {
		cwd = metadata.CWD
	}
	var err error
	if cwd == "" {
		return nil, errors.New("session: harness storage metadata is missing cwd")
	}
	cwd, err = resolveHarnessPath(cwd)
	if err != nil {
		return nil, err
	}
	persisted := false
	if durable, ok := storage.(interface{ IsPersistent() bool }); ok {
		persisted = durable.IsPersistent()
	}
	sessionFile, sessionDir := "", ""
	if persisted && metadata.Path != "" {
		sessionFile, err = resolveHarnessPath(metadata.Path)
		if err != nil {
			return nil, err
		}
		sessionDir = filepath.Dir(sessionFile)
		if virtualHarnessPath(sessionFile) {
			sessionDir = path.Dir(sessionFile)
		}
	}
	manager := newManager(cwd, sessionDir, persisted, resolved)
	manager.sessionID = metadata.ID
	manager.sessionFile = sessionFile
	manager.harnessStorage = storage
	manager.harnessRepo = resolved.harnessRepo
	if err := manager.refreshHarnessLocked(); err != nil {
		return nil, err
	}
	manager.flushed = true
	return manager, nil
}

// virtualHarnessPath reports a rooted POSIX path that is not native: on win32 it
// comes from a host FS port's own namespace, not from a Git Bash drive path.
func virtualHarnessPath(value string) bool {
	return strings.HasPrefix(value, "/") && !filepath.IsAbs(nodepath.NormalizeShellPath(value))
}

// resolveHarnessPath keeps a host FS port's paths in its namespace: a virtual
// POSIX tree must not pick up the process drive on win32 (DECISIONS.md P10).
func resolveHarnessPath(value string) (string, error) {
	if virtualHarnessPath(value) {
		return path.Clean(value), nil
	}
	return resolvePath(value)
}

func (manager *SessionManager) HarnessRepo() harness.SessionRepo {
	if manager == nil {
		return nil
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.harnessRepo
}

func (manager *SessionManager) HarnessMetadata() (harness.SessionMetadata, bool) {
	if manager == nil {
		return harness.SessionMetadata{}, false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.harnessStorage == nil {
		return harness.SessionMetadata{}, false
	}
	return manager.harnessStorage.Metadata(), true
}

// IsHarnessBacked reports whether mutations are delegated to a harness store.
func (manager *SessionManager) IsHarnessBacked() bool {
	if manager == nil {
		return false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.harnessStorage != nil
}

// refreshHarnessLocked brings the index up to date with the harness store.
func (manager *SessionManager) refreshHarnessLocked() error {
	journal := manager.harnessStorage
	if journal == nil {
		return nil
	}
	// Orb's own stores are append-only. Refresh only their tail; rescanning a
	// long transcript here made every message O(history).
	switch journal.(type) {
	case *harness.JSONLSessionStorage, *harness.InMemorySessionStorage:
	default:
		manager.fileEntries = nil
	}
	if len(manager.fileEntries) == 0 {
		manager.fileEntries = []*FileEntry{manager.harnessHeaderLocked()}
		manager.buildIndexLocked()
	}
	entries := journal.Entries(harness.SessionEntryCursorOptions{AfterEntrySeq: len(manager.fileEntries) - 1})
	for _, entry := range entries {
		// The index shares the manager's parse: entries are never changed in place.
		converted := manager.parsedEntry(entry)
		record := &FileEntry{Type: converted.Type, Entry: converted, object: converted.object}
		if converted.object == nil {
			record = newEntryRecord(*converted)
		}
		manager.fileEntries = append(manager.fileEntries, record)
		if record.Entry != nil && record.Type != "session" {
			manager.byID[entry.ID] = record.Entry
			manager.addAggregateEntryLocked(record.Entry)
		}
		if entry.Type == "label" && entry.TargetID != nil {
			if label, exists := journal.Label(*entry.TargetID); exists {
				manager.labelsByID[*entry.TargetID] = label
				manager.labelTimestampsID[*entry.TargetID] = entry.Timestamp
			} else {
				delete(manager.labelsByID, *entry.TargetID)
				delete(manager.labelTimestampsID, *entry.TargetID)
			}
		}
	}
	leaf, err := journal.LeafID()
	if err != nil {
		return err
	}
	manager.leafID = cloneString(leaf)
	if len(entries) > 0 {
		manager.revision++
	}
	return nil
}

// harnessHeaderLocked is the store's own header record, or one built from its
// metadata.
func (manager *SessionManager) harnessHeaderLocked() *FileEntry {
	if byteStorage, ok := manager.harnessStorage.(harness.ByteSessionStorage); ok {
		parsed := ParseSessionEntries(string(byteStorage.HeaderJSON()))
		if len(parsed) == 1 && parsed[0] != nil && parsed[0].Header != nil {
			return parsed[0]
		}
	}
	metadata := manager.harnessStorage.Metadata()
	return newHeaderRecord(SessionHeader{
		Type: "session", Version: harnessSessionVersion(manager.harnessStorage), ID: metadata.ID,
		Timestamp: metadata.CreatedAt, CWD: cmp.Or(metadata.CWD, manager.cwd),
		ParentSession: cloneString(metadata.ParentSessionPath), Metadata: cloneRaw(metadata.Metadata),
	})
}

func sessionEntryFromHarness(entry harness.SessionTreeEntry) SessionEntry {
	if raw := entry.RawJSON(); len(raw) != 0 {
		// A record the harness wrote parses from its own copy; anything that
		// does not yield an entry takes the general path.
		if utf8.Valid(raw) && bytes.IndexByte(raw, '\n') < 0 {
			if parsed := parseSessionEntryRaw(raw); parsed != nil && parsed.Entry != nil {
				return *parsed.Entry
			}
		}
		parsed := ParseSessionEntries(string(raw))
		if len(parsed) == 1 && parsed[0] != nil && parsed[0].Entry != nil {
			return *parsed[0].Entry
		}
	}
	var targetID string
	if entry.TargetID != nil {
		targetID = *entry.TargetID
	}
	return SessionEntry{
		Type: entry.Type, ID: entry.ID, ParentID: cloneString(entry.ParentID), Timestamp: entry.Timestamp,
		Message: cloneRaw(entry.Message), ThinkingLevel: entry.ThinkingLevel, Provider: entry.Provider,
		ModelID: entry.ModelID, ActiveToolNames: slices.Clone(entry.ActiveToolNames),
		Summary: entry.Summary, FirstKeptEntryID: entry.FirstKeptEntryID, TokensBefore: entry.TokensBefore,
		Details: cloneRaw(entry.Details), Usage: cloneSessionUsage(entry.Usage), FromHook: cloneBool(entry.FromHook), FromID: entry.FromID,
		CustomType: entry.CustomType, Data: cloneRaw(entry.Data), Content: cloneRaw(entry.Content),
		Display: entry.Display, TargetID: targetID, LeafTargetID: cloneString(entry.TargetID),
		Label: cloneString(entry.Label), Name: entry.Name, Replacement: cloneRaw(entry.Replacement),
	}
}

// harnessEntryFromSession shares entry's values: the harness stores copies.
func harnessEntryFromSession(entry SessionEntry) harness.SessionTreeEntry {
	var targetID *string
	switch entry.Type {
	case "leaf":
		targetID = entry.LeafTargetID
		if targetID == nil && entry.TargetID != "" {
			targetID = &entry.TargetID
		}
	case "label", "context_edit":
		targetID = &entry.TargetID
	}
	return harness.SessionTreeEntry{
		Type: entry.Type, ID: entry.ID, ParentID: entry.ParentID, Timestamp: entry.Timestamp,
		Message: entry.Message, ThinkingLevel: entry.ThinkingLevel, Provider: entry.Provider,
		ModelID: entry.ModelID, ActiveToolNames: entry.ActiveToolNames,
		Summary: entry.Summary, FirstKeptEntryID: entry.FirstKeptEntryID, TokensBefore: entry.TokensBefore,
		Details: entry.Details, Usage: entry.Usage, FromHook: entry.FromHook, FromID: entry.FromID,
		CustomType: entry.CustomType, Data: entry.Data, Content: entry.Content,
		Display: entry.Display, TargetID: targetID, HasTargetID: entry.Type == "leaf" || entry.Type == "label" || entry.Type == "context_edit",
		Label: entry.Label, Name: entry.Name, Replacement: entry.Replacement,
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (manager *SessionManager) harnessJSONLLocked() ([]byte, error) {
	if byteStorage, ok := manager.harnessStorage.(harness.ByteSessionStorage); ok {
		return byteStorage.Bytes()
	}
	return harness.MarshalSessionJSONL(manager.harnessStorage, manager.cwd)
}

func harnessSessionVersion(storage harness.SessionStorage) *int {
	version := float64(CurrentVersion)
	if versioned, ok := storage.(interface{ SessionVersion() float64 }); ok {
		version = versioned.SessionVersion()
	}
	integer := int(version)
	if float64(integer) != version {
		return nil
	}
	return &integer
}
