package session

import (
	"errors"
	"path/filepath"

	"github.com/OrdalieTech/pigo/agent/harness"
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
	cwd, err = resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	persisted := false
	if durable, ok := storage.(interface{ IsPersistent() bool }); ok {
		persisted = durable.IsPersistent()
	}
	sessionFile, sessionDir := "", ""
	if persisted && metadata.Path != "" {
		sessionFile, err = resolvePath(metadata.Path)
		if err != nil {
			return nil, err
		}
		sessionDir = filepath.Dir(sessionFile)
	}
	manager := newManager(cwd, sessionDir, persisted && sessionFile != "", resolved)
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

func (manager *SessionManager) refreshHarnessLocked() error {
	if manager.harnessStorage == nil {
		return nil
	}
	metadata := manager.harnessStorage.Metadata()
	cwd := metadata.CWD
	if cwd == "" {
		cwd = manager.cwd
	}
	var header *FileEntry
	if byteStorage, ok := manager.harnessStorage.(harness.ByteSessionStorage); ok {
		parsed := ParseSessionEntries(string(byteStorage.HeaderJSON()))
		if len(parsed) == 1 && parsed[0] != nil && parsed[0].Header != nil {
			header = parsed[0]
		}
	}
	if header == nil {
		version := harnessSessionVersion(manager.harnessStorage)
		header = newHeaderRecord(SessionHeader{
			Type:          "session",
			Version:       version,
			ID:            metadata.ID,
			Timestamp:     metadata.CreatedAt,
			CWD:           cwd,
			ParentSession: cloneString(metadata.ParentSessionPath),
			Metadata:      cloneRaw(metadata.Metadata),
		})
	}
	entries := manager.harnessStorage.Entries()
	manager.fileEntries = make([]*FileEntry, 1, len(entries)+1)
	manager.fileEntries[0] = header
	for _, entry := range entries {
		converted := sessionEntryFromHarness(entry)
		if converted.object != nil {
			manager.fileEntries = append(manager.fileEntries, &FileEntry{
				Type: converted.Type, Entry: &converted, object: converted.object,
			})
			continue
		}
		manager.fileEntries = append(manager.fileEntries, newEntryRecord(converted))
	}
	manager.buildIndexLocked()
	manager.labelsByID = make(map[string]string)
	manager.labelTimestampsID = make(map[string]string)
	for _, entry := range entries {
		if entry.Type != "label" || entry.TargetID == nil {
			continue
		}
		label, ok := manager.harnessStorage.Label(*entry.TargetID)
		if !ok {
			delete(manager.labelsByID, *entry.TargetID)
			delete(manager.labelTimestampsID, *entry.TargetID)
			continue
		}
		manager.labelsByID[*entry.TargetID] = label
		manager.labelTimestampsID[*entry.TargetID] = entry.Timestamp
	}
	leaf, err := manager.harnessStorage.LeafID()
	if err != nil {
		return err
	}
	manager.leafID = cloneString(leaf)
	return nil
}

func sessionEntryFromHarness(entry harness.SessionTreeEntry) SessionEntry {
	if raw := entry.RawJSON(); len(raw) != 0 {
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
		ModelID: entry.ModelID, ActiveToolNames: cloneStringSlice(entry.ActiveToolNames),
		Summary: entry.Summary, FirstKeptEntryID: entry.FirstKeptEntryID, TokensBefore: entry.TokensBefore,
		Details: cloneRaw(entry.Details), Usage: cloneSessionUsage(entry.Usage), FromHook: cloneBool(entry.FromHook), FromID: entry.FromID,
		CustomType: entry.CustomType, Data: cloneRaw(entry.Data), Content: cloneRaw(entry.Content),
		Display: entry.Display, TargetID: targetID, LeafTargetID: cloneString(entry.TargetID),
		Label: cloneString(entry.Label), Name: entry.Name,
	}
}

func harnessEntryFromSession(entry SessionEntry) harness.SessionTreeEntry {
	var targetID *string
	switch entry.Type {
	case "leaf":
		targetID = cloneString(entry.LeafTargetID)
		if targetID == nil && entry.TargetID != "" {
			targetID = cloneString(&entry.TargetID)
		}
	case "label":
		targetID = cloneString(&entry.TargetID)
	}
	return harness.SessionTreeEntry{
		Type: entry.Type, ID: entry.ID, ParentID: cloneString(entry.ParentID), Timestamp: entry.Timestamp,
		Message: cloneRaw(entry.Message), ThinkingLevel: entry.ThinkingLevel, Provider: entry.Provider,
		ModelID: entry.ModelID, ActiveToolNames: cloneStringSlice(entry.ActiveToolNames),
		Summary: entry.Summary, FirstKeptEntryID: entry.FirstKeptEntryID, TokensBefore: entry.TokensBefore,
		Details: cloneRaw(entry.Details), Usage: cloneSessionUsage(entry.Usage), FromHook: cloneBool(entry.FromHook), FromID: entry.FromID,
		CustomType: entry.CustomType, Data: cloneRaw(entry.Data), Content: cloneRaw(entry.Content),
		Display: entry.Display, TargetID: targetID, HasTargetID: entry.Type == "leaf" || entry.Type == "label",
		Label: cloneString(entry.Label), Name: entry.Name,
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
