package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
)

type resolvedLabel struct {
	targetID  string
	label     string
	timestamp string
}

// CreateBranchedSession replaces the manager with the root-to-leaf path in a
// new session, preserving resolved labels for retained entries.
func (manager *SessionManager) CreateBranchedSession(leafID string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	previousSessionFile := manager.sessionFile
	path := manager.getBranchLocked(leafID)
	if len(path) == 0 {
		return "", fmt.Errorf("Entry %s not found", leafID) //nolint:staticcheck // Upstream error capitalization is observable.
	}

	pathIDs := make(map[string]struct{}, len(path))
	pathRecords := make([]*FileEntry, 0, len(path))
	var parentID *string
	for index := range path {
		entry := &path[index]
		if entry.Type == "label" {
			continue
		}
		record, err := cloneEntryRecordWithParent(entry, parentID)
		if err != nil {
			return "", err
		}
		pathRecords = append(pathRecords, record)
		pathIDs[entry.ID] = struct{}{}
		parentID = cloneString(&entry.ID)
	}

	now := manager.clock()
	newSessionID, err := manager.sessionIDGenerator(now)
	if err != nil {
		return "", err
	}
	timestamp := formatTimestamp(now)
	version := CurrentVersion
	var parentSession *string
	if manager.persist {
		parentSession = cloneString(&previousSessionFile)
	}
	header := newHeaderRecord(SessionHeader{
		Type:          "session",
		Version:       &version,
		ID:            newSessionID,
		Timestamp:     timestamp,
		CWD:           manager.cwd,
		ParentSession: parentSession,
	})

	labels := manager.resolvedLabelsLocked()
	labelRecords := make([]*FileEntry, 0, len(labels))
	for _, label := range labels {
		if _, retained := pathIDs[label.targetID]; !retained {
			continue
		}
		labelID, generateErr := generateUniqueID(pathIDs, manager.entryIDGenerator)
		if generateErr != nil {
			return "", generateErr
		}
		labelValue := label.label
		labelRecord := newEntryRecord(SessionEntry{
			Type:      "label",
			ID:        labelID,
			ParentID:  cloneString(parentID),
			Timestamp: label.timestamp,
			TargetID:  label.targetID,
			Label:     &labelValue,
		})
		labelRecords = append(labelRecords, labelRecord)
		parentID = &labelID
	}

	manager.fileEntries = append([]*FileEntry{header}, pathRecords...)
	manager.fileEntries = append(manager.fileEntries, labelRecords...)
	manager.sessionID = newSessionID
	if manager.persist {
		filenameTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
		manager.sessionFile = filepath.Join(manager.sessionDir, filenameTimestamp+"_"+newSessionID+".jsonl")
	} else {
		manager.sessionFile = ""
	}
	manager.buildIndexLocked()

	if manager.persist && manager.hasAssistantLocked() {
		if err := manager.rewriteFileLocked(); err != nil {
			return "", err
		}
		manager.flushed = true
	} else {
		manager.flushed = false
	}
	return manager.sessionFile, nil
}

func cloneEntryRecordWithParent(entry *SessionEntry, parentID *string) (*FileEntry, error) {
	encoded, err := entry.MarshalJSON()
	if err != nil {
		return nil, err
	}
	normalized, err := ai.NormalizeJSONStringifyJSON(encoded)
	if err != nil {
		return nil, err
	}
	object, err := parseOrderedObject(normalized)
	if err != nil {
		return nil, err
	}
	parent := rawNull()
	if parentID != nil {
		parent = mustRawString(*parentID)
	}
	object.set("parentId", parent)
	return decodeFileEntry(object, nil), nil
}

func (manager *SessionManager) hasAssistantLocked() bool {
	for _, candidate := range manager.fileEntries {
		if candidate != nil && candidate.Entry != nil && candidate.Entry.Type == "message" && messageRole(candidate.Entry.Message) == "assistant" {
			return true
		}
	}
	return false
}

func (manager *SessionManager) resolvedLabelsLocked() []resolvedLabel {
	order := make([]string, 0)
	active := make(map[string]resolvedLabel)
	for _, fileEntry := range manager.fileEntries {
		if fileEntry == nil || fileEntry.Entry == nil || fileEntry.Entry.Type != "label" {
			continue
		}
		entry := fileEntry.Entry
		if entry.Label == nil || *entry.Label == "" {
			delete(active, entry.TargetID)
			for index, targetID := range order {
				if targetID == entry.TargetID {
					order = append(order[:index], order[index+1:]...)
					break
				}
			}
			continue
		}
		if _, exists := active[entry.TargetID]; !exists {
			order = append(order, entry.TargetID)
		}
		active[entry.TargetID] = resolvedLabel{
			targetID: entry.TargetID, label: *entry.Label, timestamp: entry.Timestamp,
		}
	}
	labels := make([]resolvedLabel, 0, len(order))
	for _, targetID := range order {
		if label, exists := active[targetID]; exists {
			labels = append(labels, label)
		}
	}
	return labels
}

// ForkFrom copies a complete session into a new persisted session rooted at
// targetCWD. Only the header is replaced; all other JSON values retain their
// JavaScript parse/stringify representation.
func ForkFrom(sourcePath, targetCWD, sessionDir string, options ...Option) (*SessionManager, error) {
	resolved := applyOptions(options)
	resolvedSource, err := resolvePath(sourcePath)
	if err != nil {
		return nil, err
	}
	resolvedTarget, err := resolvePath(targetCWD)
	if err != nil {
		return nil, err
	}
	sourceEntries, err := LoadEntriesFromFile(resolvedSource)
	if err != nil {
		return nil, err
	}
	if len(sourceEntries) == 0 {
		return nil, fmt.Errorf("Cannot fork: source session file is empty or invalid: %s", resolvedSource) //nolint:staticcheck // Upstream error capitalization is observable.
	}
	if findHeader(sourceEntries) == nil {
		return nil, fmt.Errorf("Cannot fork: source session has no header: %s", resolvedSource) //nolint:staticcheck // Upstream error capitalization is observable.
	}

	if sessionDir == "" {
		sessionDir, err = DefaultSessionDir(resolvedTarget, resolved.agentDir)
		if err != nil {
			return nil, err
		}
	} else {
		sessionDir = normalizePath(sessionDir)
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			return nil, err
		}
	}

	now := resolved.clock()
	newSessionID := ""
	if resolved.initialID != nil {
		if err := AssertValidSessionID(*resolved.initialID); err != nil {
			return nil, err
		}
		newSessionID = *resolved.initialID
	} else {
		newSessionID, err = resolved.sessionIDGenerator(now)
		if err != nil {
			return nil, err
		}
	}
	timestamp := formatTimestamp(now)
	filenameTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
	newSessionFile := filepath.Join(sessionDir, filenameTimestamp+"_"+newSessionID+".jsonl")
	version := CurrentVersion
	parent := resolvedSource
	entries := []*FileEntry{newHeaderRecord(SessionHeader{
		Type: "session", Version: &version, ID: newSessionID, Timestamp: timestamp,
		CWD: resolvedTarget, ParentSession: &parent,
	})}
	for _, sourceEntry := range sourceEntries {
		if sourceEntry == nil || sourceEntry.Type == "session" {
			continue
		}
		copied, copyErr := cloneJSONStringifiedFileEntry(sourceEntry)
		if copyErr != nil {
			return nil, copyErr
		}
		entries = append(entries, copied)
	}

	file, err := os.OpenFile(newSessionFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return nil, err
	}
	writeErr := writeEntries(file, entries)
	writeErr = errors.Join(writeErr, file.Close())
	if writeErr != nil {
		return nil, writeErr
	}

	manager := newManager(resolvedTarget, sessionDir, true, resolved)
	if err := manager.setSessionFileLocked(newSessionFile); err != nil {
		return nil, err
	}
	return manager, nil
}

func cloneJSONStringifiedFileEntry(entry *FileEntry) (*FileEntry, error) {
	encoded, err := entry.MarshalJSON()
	if err != nil {
		return nil, err
	}
	normalized, err := ai.NormalizeJSONStringifyJSON(encoded)
	if err != nil {
		return nil, err
	}
	copy := parseSessionEntryLine(string(normalized))
	if copy == nil {
		return nil, errors.New("session: forked entry could not be serialized")
	}
	return copy, nil
}
