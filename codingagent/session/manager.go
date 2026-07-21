package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
)

type Clock func() time.Time
type SessionIDGenerator func(time.Time) (string, error)

type NewSessionOptions struct {
	ID            *string
	ParentSession *string
}

type OptionalEntryFields struct {
	Details    any
	HasDetails bool
	Usage      *ai.Usage
	FromHook   *bool
}

type SessionModel struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

type SessionContext struct {
	Messages        []json.RawMessage
	ThinkingLevel   string
	Model           *SessionModel
	ActiveToolNames []string
}

type managerOptions struct {
	clock              Clock
	sessionIDGenerator SessionIDGenerator
	entryIDGenerator   IDGenerator
	agentDir           string
	cwdOverride        string
	initialID          *string
	parentSession      *string
	harnessRepo        harness.SessionRepo
}

type Option func(*managerOptions)

func WithClock(clock Clock) Option {
	return func(options *managerOptions) {
		if clock != nil {
			options.clock = clock
		}
	}
}

func WithSessionIDGenerator(generator SessionIDGenerator) Option {
	return func(options *managerOptions) {
		if generator != nil {
			options.sessionIDGenerator = generator
		}
	}
}

func WithEntryIDGenerator(generator IDGenerator) Option {
	return func(options *managerOptions) {
		if generator != nil {
			options.entryIDGenerator = generator
		}
	}
}

func WithAgentDir(path string) Option {
	return func(options *managerOptions) { options.agentDir = path }
}

func WithCwdOverride(path string) Option {
	return func(options *managerOptions) { options.cwdOverride = path }
}

func WithSessionID(id string) Option {
	return func(options *managerOptions) { options.initialID = &id }
}

func WithParentSession(path string) Option {
	return func(options *managerOptions) { options.parentSession = &path }
}

type SessionManager struct {
	mu sync.RWMutex

	sessionID   string
	sessionFile string
	sessionDir  string
	cwd         string
	persist     bool
	flushed     bool

	fileEntries       []*FileEntry
	byID              map[string]*SessionEntry
	labelsByID        map[string]string
	labelTimestampsID map[string]string
	leafID            *string

	clock              Clock
	sessionIDGenerator SessionIDGenerator
	entryIDGenerator   IDGenerator
	agentDir           string
	harnessStorage     harness.SessionStorage
	harnessRepo        harness.SessionRepo
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func AssertValidSessionID(id string) error {
	if !sessionIDPattern.MatchString(id) {
		return errors.New("Session id must be non-empty, contain only alphanumeric characters, '-', '_', and '.', and start and end with an alphanumeric character") //nolint:staticcheck // Upstream error capitalization is observable.
	}
	return nil
}

func defaultManagerOptions() managerOptions {
	return managerOptions{
		clock:              time.Now,
		sessionIDGenerator: randomUUIDv7,
		entryIDGenerator:   randomEntryCandidate,
	}
}

func applyOptions(options []Option) managerOptions {
	resolved := defaultManagerOptions()
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}
	return resolved
}

func Create(cwd, sessionDir string, options ...Option) (*SessionManager, error) {
	resolved := applyOptions(options)
	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	if sessionDir == "" {
		sessionDir, err = DefaultSessionDir(resolvedCWD, resolved.agentDir)
		if err != nil {
			return nil, err
		}
	} else {
		sessionDir = normalizePath(sessionDir)
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			return nil, err
		}
	}
	manager := newManager(resolvedCWD, sessionDir, true, resolved)
	if _, err := manager.newSessionLocked(&NewSessionOptions{ID: resolved.initialID, ParentSession: resolved.parentSession}); err != nil {
		return nil, err
	}
	return manager, nil
}

func Open(path, sessionDir string, options ...Option) (*SessionManager, error) {
	resolved := applyOptions(options)
	resolvedPath, err := resolvePath(path)
	if err != nil {
		return nil, err
	}
	loaded, err := loadSessionFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	cwd := resolved.cwdOverride
	if cwd == "" {
		if header := findHeader(loaded.entries); header != nil && header.Header != nil {
			cwd = header.Header.CWD
		}
	}
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err = resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	if sessionDir == "" {
		sessionDir = filepath.Dir(resolvedPath)
	} else {
		sessionDir = normalizePath(sessionDir)
	}
	manager := newManager(cwd, sessionDir, true, resolved)
	if err := manager.setLoadedSessionFileLocked(resolvedPath, loaded); err != nil {
		return nil, err
	}
	return manager, nil
}

func ContinueRecent(cwd, sessionDir string, options ...Option) (*SessionManager, error) {
	resolved := applyOptions(options)
	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	explicitDir := sessionDir != ""
	if !explicitDir {
		sessionDir, err = DefaultSessionDir(resolvedCWD, resolved.agentDir)
		if err != nil {
			return nil, err
		}
	} else {
		sessionDir = normalizePath(sessionDir)
	}
	defaultDir, err := DefaultSessionDirPath(resolvedCWD, resolved.agentDir)
	if err != nil {
		return nil, err
	}
	var filter string
	if explicitDir && sessionDir != defaultDir {
		filter = resolvedCWD
	}
	recent := FindMostRecentSession(sessionDir, filter)
	if recent != "" {
		manager := newManager(resolvedCWD, sessionDir, true, resolved)
		if err := manager.setSessionFileLocked(recent); err != nil {
			return nil, err
		}
		return manager, nil
	}
	return Create(resolvedCWD, sessionDir, options...)
}

func InMemory(cwd string, options ...Option) (*SessionManager, error) {
	resolved := applyOptions(options)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	manager := newManager(resolvedCWD, "", false, resolved)
	if _, err := manager.newSessionLocked(&NewSessionOptions{ID: resolved.initialID, ParentSession: resolved.parentSession}); err != nil {
		return nil, err
	}
	return manager, nil
}

func newManager(cwd, sessionDir string, persist bool, options managerOptions) *SessionManager {
	return &SessionManager{
		cwd:                cwd,
		sessionDir:         normalizePath(sessionDir),
		persist:            persist,
		byID:               make(map[string]*SessionEntry),
		labelsByID:         make(map[string]string),
		labelTimestampsID:  make(map[string]string),
		clock:              options.clock,
		sessionIDGenerator: options.sessionIDGenerator,
		entryIDGenerator:   options.entryIDGenerator,
		agentDir:           options.agentDir,
	}
}

func (manager *SessionManager) SetSessionFile(path string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.harnessStorage != nil {
		return ErrHarnessStorageReplacement
	}
	return manager.setSessionFileLocked(path)
}

func (manager *SessionManager) setSessionFileLocked(path string) error {
	resolved, err := resolvePath(path)
	if err != nil {
		return err
	}
	loaded, err := loadSessionFile(resolved)
	if err != nil {
		return err
	}
	return manager.setLoadedSessionFileLocked(resolved, loaded)
}

func (manager *SessionManager) setLoadedSessionFileLocked(resolved string, loaded loadedSessionFile) error {
	manager.sessionFile = resolved
	if !loaded.exists {
		if _, err := manager.newSessionLocked(nil); err != nil {
			return err
		}
		manager.sessionFile = resolved
		return nil
	}

	entries := loaded.entries
	if len(entries) == 0 {
		if loaded.size > 0 {
			return fmt.Errorf("Session file is not a valid pi session: %s", resolved) //nolint:staticcheck // Upstream error capitalization is observable.
		}
		if _, err := manager.newSessionLocked(nil); err != nil {
			return err
		}
		manager.sessionFile = resolved
		if err := manager.rewriteFileLocked(); err != nil {
			return err
		}
		manager.flushed = true
		return nil
	}

	manager.fileEntries = entries
	header := findHeader(entries)
	if header != nil && header.Header != nil {
		manager.sessionID = header.Header.ID
	}
	if manager.sessionID == "" {
		generated, err := manager.sessionIDGenerator(manager.clock())
		if err != nil {
			return err
		}
		manager.sessionID = generated
	}
	migrated, err := MigrateSessionEntries(manager.fileEntries, manager.entryIDGenerator)
	if err != nil {
		return err
	}
	if migrated {
		if err := manager.rewriteFileLocked(); err != nil {
			return err
		}
	}
	manager.buildIndexLocked()
	manager.flushed = true
	return nil
}

func (manager *SessionManager) NewSession(options ...NewSessionOptions) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.harnessStorage != nil {
		return "", ErrHarnessStorageReplacement
	}
	var resolved *NewSessionOptions
	if len(options) > 0 {
		resolved = &options[0]
	}
	return manager.newSessionLocked(resolved)
}

func (manager *SessionManager) newSessionLocked(options *NewSessionOptions) (string, error) {
	now := manager.clock()
	var err error
	if options != nil && options.ID != nil {
		if err := AssertValidSessionID(*options.ID); err != nil {
			return "", err
		}
		manager.sessionID = *options.ID
	} else {
		manager.sessionID, err = manager.sessionIDGenerator(now)
		if err != nil {
			return "", err
		}
	}
	timestamp := formatTimestamp(now)
	version := CurrentVersion
	header := newHeaderRecord(SessionHeader{
		Type:          "session",
		Version:       &version,
		ID:            manager.sessionID,
		Timestamp:     timestamp,
		CWD:           manager.cwd,
		ParentSession: parentSession(options),
	})
	manager.fileEntries = []*FileEntry{header}
	manager.byID = make(map[string]*SessionEntry)
	manager.labelsByID = make(map[string]string)
	manager.labelTimestampsID = make(map[string]string)
	manager.leafID = nil
	manager.flushed = false
	if manager.persist {
		filenameTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
		manager.sessionFile = filepath.Join(manager.sessionDir, filenameTimestamp+"_"+manager.sessionID+".jsonl")
	} else {
		manager.sessionFile = ""
	}
	return manager.sessionFile, nil
}

func parentSession(options *NewSessionOptions) *string {
	if options == nil {
		return nil
	}
	return options.ParentSession
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (manager *SessionManager) buildIndexLocked() {
	manager.byID = make(map[string]*SessionEntry)
	manager.labelsByID = make(map[string]string)
	manager.labelTimestampsID = make(map[string]string)
	manager.leafID = nil
	for _, fileEntry := range manager.fileEntries {
		if fileEntry == nil || fileEntry.Entry == nil || fileEntry.Type == "session" {
			continue
		}
		entry := fileEntry.Entry
		manager.byID[entry.ID] = entry
		id := entry.ID
		manager.leafID = &id
		if entry.Type == "label" {
			if entry.Label != nil && *entry.Label != "" {
				manager.labelsByID[entry.TargetID] = *entry.Label
				manager.labelTimestampsID[entry.TargetID] = entry.Timestamp
			} else {
				delete(manager.labelsByID, entry.TargetID)
				delete(manager.labelTimestampsID, entry.TargetID)
			}
		}
	}
}

func (manager *SessionManager) rewriteFileLocked() error {
	if !manager.persist || manager.sessionFile == "" {
		return nil
	}
	return withFileLock(manager.sessionFile, func() error {
		file, err := os.OpenFile(manager.sessionFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
		if err != nil {
			return err
		}
		writeErr := writeEntries(file, manager.fileEntries)
		return errors.Join(writeErr, file.Close())
	})
}

func writeEntries(writer io.Writer, entries []*FileEntry) error {
	for _, entry := range entries {
		encoded, err := entry.MarshalJSON()
		if err != nil {
			return err
		}
		line := append(append([]byte(nil), encoded...), '\n')
		if _, err := io.Copy(writer, bytes.NewReader(line)); err != nil {
			return err
		}
	}
	return nil
}

func MarshalJSONL(entries []*FileEntry) ([]byte, error) {
	var output bytes.Buffer
	if err := writeEntries(&output, entries); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (manager *SessionManager) JSONL() ([]byte, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.harnessStorage != nil {
		return manager.harnessJSONLLocked()
	}
	return MarshalJSONL(manager.fileEntries)
}

func (manager *SessionManager) persistEntryLocked(entry *FileEntry) error {
	if !manager.persist || manager.sessionFile == "" {
		return nil
	}
	hasAssistant := false
	for _, candidate := range manager.fileEntries {
		if candidate != nil && candidate.Entry != nil && candidate.Entry.Type == "message" && messageRole(candidate.Entry.Message) == "assistant" {
			hasAssistant = true
			break
		}
	}
	if !hasAssistant {
		if manager.flushed {
			return manager.appendFileEntryLocked(entry)
		}
		manager.flushed = false
		return nil
	}
	if !manager.flushed {
		err := withFileLock(manager.sessionFile, func() error {
			file, err := os.OpenFile(manager.sessionFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
			if err != nil {
				return err
			}
			writeErr := writeEntries(file, manager.fileEntries)
			return errors.Join(writeErr, file.Close())
		})
		if err != nil {
			return err
		}
		manager.flushed = true
		return nil
	}
	return manager.appendFileEntryLocked(entry)
}

func (manager *SessionManager) appendFileEntryLocked(entry *FileEntry) error {
	return withFileLock(manager.sessionFile, func() error {
		file, err := os.OpenFile(manager.sessionFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
		if err != nil {
			return err
		}
		writeErr := writeEntries(file, []*FileEntry{entry})
		return errors.Join(writeErr, file.Close())
	})
}

func messageRole(message json.RawMessage) string {
	var header struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(message, &header)
	return header.Role
}

func (manager *SessionManager) appendEntryLocked(entry SessionEntry) (string, error) {
	if manager.harnessStorage != nil {
		id, err := manager.harnessStorage.CreateEntryID()
		if err != nil {
			return "", err
		}
		harnessEntry := harnessEntryFromSession(entry)
		harnessEntry.ID = id
		if entry.Type != "branch_summary" {
			harnessEntry.ParentID, err = manager.harnessStorage.LeafID()
			if err != nil {
				return "", err
			}
		}
		harnessEntry.Timestamp = formatTimestamp(manager.clock())
		if err := manager.harnessStorage.AppendEntry(harnessEntry); err != nil {
			return "", err
		}
		if err := manager.refreshHarnessLocked(); err != nil {
			return "", err
		}
		return id, nil
	}
	record := newEntryRecord(entry)
	manager.fileEntries = append(manager.fileEntries, record)
	manager.byID[entry.ID] = record.Entry
	id := entry.ID
	manager.leafID = &id
	if err := manager.persistEntryLocked(record); err != nil {
		return "", err
	}
	return entry.ID, nil
}

func (manager *SessionManager) newEntryBaseLocked(entryType string) (SessionEntry, error) {
	if manager.harnessStorage != nil {
		return SessionEntry{Type: entryType}, nil
	}
	id, err := findUniqueID(manager.byID, manager.entryIDGenerator)
	if err != nil {
		return SessionEntry{}, err
	}
	return SessionEntry{
		Type:      entryType,
		ID:        id,
		ParentID:  cloneString(manager.leafID),
		Timestamp: formatTimestamp(manager.clock()),
	}, nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func (manager *SessionManager) AppendMessage(message any) (string, error) {
	raw, err := rawValue(message)
	if err != nil {
		return "", err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("message")
	if err != nil {
		return "", err
	}
	entry.Message = raw
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) AppendThinkingLevelChange(level string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("thinking_level_change")
	if err != nil {
		return "", err
	}
	entry.ThinkingLevel = level
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) AppendModelChange(provider, modelID string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("model_change")
	if err != nil {
		return "", err
	}
	entry.Provider = provider
	entry.ModelID = modelID
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) AppendActiveToolsChange(activeToolNames []string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("active_tools_change")
	if err != nil {
		return "", err
	}
	entry.ActiveToolNames = cloneStringSlice(activeToolNames)
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) AppendCompaction(
	summary string,
	firstKeptEntryID string,
	tokensBefore int64,
	options ...OptionalEntryFields,
) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("compaction")
	if err != nil {
		return "", err
	}
	entry.Summary = summary
	entry.FirstKeptEntryID = firstKeptEntryID
	entry.TokensBefore = float64(tokensBefore)
	if err := applyOptionalEntryFields(&entry, options); err != nil {
		return "", err
	}
	return manager.appendEntryLocked(entry)
}

func applyOptionalEntryFields(entry *SessionEntry, options []OptionalEntryFields) error {
	if len(options) == 0 {
		return nil
	}
	option := options[0]
	if option.HasDetails {
		details, err := rawValue(option.Details)
		if err != nil {
			return err
		}
		entry.Details = details
	}
	entry.FromHook = option.FromHook
	entry.Usage = cloneSessionUsage(option.Usage)
	return nil
}

func (manager *SessionManager) AppendCustomEntry(customType string, data ...any) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("custom")
	if err != nil {
		return "", err
	}
	entry.CustomType = customType
	if len(data) > 0 {
		entry.Data, err = rawValue(data[0])
		if err != nil {
			return "", err
		}
	}
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) AppendSessionInfo(name string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("session_info")
	if err != nil {
		return "", err
	}
	entry.Name = sanitizeSessionName(name)
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) AppendCustomMessageEntry(customType string, content any, display bool, details ...any) (string, error) {
	rawContent, err := rawValue(content)
	if err != nil {
		return "", err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, err := manager.newEntryBaseLocked("custom_message")
	if err != nil {
		return "", err
	}
	entry.CustomType = customType
	entry.Content = rawContent
	entry.Display = display
	if len(details) > 0 {
		entry.Details, err = rawValue(details[0])
		if err != nil {
			return "", err
		}
	}
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) AppendLabelChange(targetID string, label *string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := manager.refreshHarnessLocked(); err != nil {
		return "", err
	}
	if _, ok := manager.byID[targetID]; !ok {
		return "", fmt.Errorf("Entry %s not found", targetID) //nolint:staticcheck // Upstream error capitalization is observable.
	}
	entry, err := manager.newEntryBaseLocked("label")
	if err != nil {
		return "", err
	}
	entry.TargetID = targetID
	entry.Label = cloneString(label)
	id, err := manager.appendEntryLocked(entry)
	if err != nil {
		return "", err
	}
	if manager.harnessStorage != nil {
		return id, nil
	}
	if label != nil && *label != "" {
		manager.labelsByID[targetID] = *label
		manager.labelTimestampsID[targetID] = entry.Timestamp
	} else {
		delete(manager.labelsByID, targetID)
		delete(manager.labelTimestampsID, targetID)
	}
	return id, nil
}

func (manager *SessionManager) IsPersisted() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.persist
}

func (manager *SessionManager) GetCWD() string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.cwd
}

func (manager *SessionManager) GetCwd() string {
	return manager.GetCWD()
}

func (manager *SessionManager) GetSessionDir() string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.sessionDir
}

func (manager *SessionManager) UsesDefaultSessionDir() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	defaultDir, err := DefaultSessionDirPath(manager.cwd, manager.agentDir)
	return err == nil && manager.sessionDir == defaultDir
}

func (manager *SessionManager) GetSessionID() string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.sessionID
}

func (manager *SessionManager) GetSessionFile() string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.sessionFile
}

func (manager *SessionManager) GetLeafID() *string {
	if manager.harnessStorage != nil {
		leaf, err := manager.harnessStorage.LeafID()
		if err != nil {
			return nil
		}
		return cloneString(leaf)
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneString(manager.leafID)
}

func (manager *SessionManager) GetLeafEntry() *SessionEntry {
	if manager.harnessStorage != nil {
		leaf, err := manager.harnessStorage.LeafID()
		if err != nil || leaf == nil {
			return nil
		}
		entry, ok := manager.harnessStorage.Entry(*leaf)
		if !ok || entry == nil {
			return nil
		}
		converted := sessionEntryFromHarness(*entry)
		return &converted
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.leafID == nil {
		return nil
	}
	return cloneEntry(manager.byID[*manager.leafID])
}

func (manager *SessionManager) GetEntry(id string) *SessionEntry {
	if manager.harnessStorage != nil {
		entry, ok := manager.harnessStorage.Entry(id)
		if !ok || entry == nil {
			return nil
		}
		converted := sessionEntryFromHarness(*entry)
		return &converted
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneEntry(manager.byID[id])
}

func cloneEntry(entry *SessionEntry) *SessionEntry {
	if entry == nil {
		return nil
	}
	copy := *entry
	copy.ParentID = cloneString(entry.ParentID)
	copy.LeafTargetID = cloneString(entry.LeafTargetID)
	copy.Label = cloneString(entry.Label)
	copy.ActiveToolNames = cloneStringSlice(entry.ActiveToolNames)
	copy.Message = cloneRaw(entry.Message)
	copy.Details = cloneRaw(entry.Details)
	copy.Usage = cloneSessionUsage(entry.Usage)
	copy.Data = cloneRaw(entry.Data)
	copy.Content = cloneRaw(entry.Content)
	return &copy
}

func (manager *SessionManager) GetEntries() []SessionEntry {
	if manager.harnessStorage != nil {
		entries := manager.harnessStorage.Entries()
		converted := make([]SessionEntry, len(entries))
		for index := range entries {
			converted[index] = sessionEntryFromHarness(entries[index])
		}
		return converted
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	entries := make([]SessionEntry, 0, len(manager.fileEntries)-1)
	for _, fileEntry := range manager.fileEntries {
		if fileEntry != nil && fileEntry.Entry != nil && fileEntry.Type != "session" {
			entries = append(entries, *cloneEntry(fileEntry.Entry))
		}
	}
	return entries
}

func (manager *SessionManager) GetHeader() *SessionHeader {
	if manager.harnessStorage != nil {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if manager.refreshHarnessLocked() != nil {
			return nil
		}
		for _, entry := range manager.fileEntries {
			if entry != nil && entry.Header != nil && entry.Type == "session" {
				copy := *entry.Header
				copy.ParentSession = cloneString(entry.Header.ParentSession)
				copy.Metadata = cloneRaw(entry.Header.Metadata)
				return &copy
			}
		}
		return nil
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	for _, entry := range manager.fileEntries {
		if entry != nil && entry.Header != nil && entry.Type == "session" {
			copy := *entry.Header
			copy.ParentSession = cloneString(entry.Header.ParentSession)
			copy.Metadata = cloneRaw(entry.Header.Metadata)
			return &copy
		}
	}
	return nil
}

func (manager *SessionManager) GetSessionName() *string {
	if manager.harnessStorage != nil {
		entries := manager.harnessStorage.EntriesByType("session_info")
		if len(entries) == 0 {
			return nil
		}
		name := trimJSSpace(entries[len(entries)-1].Name)
		if name == "" {
			return nil
		}
		return &name
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	for index := len(manager.fileEntries) - 1; index >= 0; index-- {
		entry := manager.fileEntries[index]
		if entry != nil && entry.Entry != nil && entry.Type == "session_info" {
			name := trimJSSpace(entry.Entry.Name)
			if name == "" {
				return nil
			}
			return &name
		}
	}
	return nil
}

func (manager *SessionManager) GetLabel(id string) *string {
	if manager.harnessStorage != nil {
		label, ok := manager.harnessStorage.Label(id)
		if !ok {
			return nil
		}
		return &label
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	label, ok := manager.labelsByID[id]
	if !ok {
		return nil
	}
	return &label
}

func (manager *SessionManager) GetChildren(parentID string) []SessionEntry {
	if manager.harnessStorage != nil {
		entries := manager.GetEntries()
		children := make([]SessionEntry, 0)
		for _, entry := range entries {
			if entry.ParentID != nil && *entry.ParentID == parentID {
				children = append(children, entry)
			}
		}
		return children
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	var children []SessionEntry
	for _, fileEntry := range manager.fileEntries {
		if fileEntry == nil || fileEntry.Entry == nil {
			continue
		}
		entry := fileEntry.Entry
		if entry.ParentID != nil && *entry.ParentID == parentID {
			children = append(children, *cloneEntry(entry))
		}
	}
	return children
}

func (manager *SessionManager) GetBranch(fromID ...string) []SessionEntry {
	if manager.harnessStorage != nil {
		var leaf *string
		if len(fromID) > 0 {
			leaf = cloneString(&fromID[0])
		} else {
			var err error
			leaf, err = manager.harnessStorage.LeafID()
			if err != nil {
				return nil
			}
		}
		entries, err := manager.harnessStorage.PathToRoot(leaf)
		if err != nil {
			return nil
		}
		converted := make([]SessionEntry, len(entries))
		for index := range entries {
			converted[index] = sessionEntryFromHarness(entries[index])
		}
		return converted
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.getBranchLocked(fromID...)
}

func (manager *SessionManager) GetLatestCompactionTimestamp() (string, bool) {
	if manager.harnessStorage != nil {
		latest := GetLatestCompactionEntry(manager.GetBranch())
		if latest != nil {
			return latest.Timestamp, true
		}
		return "", false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.leafID == nil {
		return "", false
	}
	for current := manager.byID[*manager.leafID]; current != nil; {
		if current.Type == "compaction" {
			return current.Timestamp, true
		}
		if current.ParentID == nil {
			break
		}
		current = manager.byID[*current.ParentID]
	}
	return "", false
}

func (manager *SessionManager) getBranchLocked(fromID ...string) []SessionEntry {
	var start string
	if len(fromID) > 0 {
		start = fromID[0]
	} else if manager.leafID != nil {
		start = *manager.leafID
	}
	var branch []SessionEntry
	current := manager.byID[start]
	for current != nil {
		branch = append(branch, *cloneEntry(current))
		if current.ParentID == nil {
			break
		}
		current = manager.byID[*current.ParentID]
	}
	for left, right := 0, len(branch)-1; left < right; left, right = left+1, right-1 {
		branch[left], branch[right] = branch[right], branch[left]
	}
	return branch
}

func (manager *SessionManager) Branch(id string) error {
	if manager.harnessStorage != nil {
		if err := manager.harnessStorage.SetLeafID(&id); err != nil {
			return err
		}
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return manager.refreshHarnessLocked()
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, ok := manager.byID[id]; !ok {
		return fmt.Errorf("Entry %s not found", id) //nolint:staticcheck // Upstream error capitalization is observable.
	}
	manager.leafID = &id
	return nil
}

func (manager *SessionManager) ResetLeaf() {
	if manager.harnessStorage != nil {
		if manager.harnessStorage.SetLeafID(nil) == nil {
			manager.mu.Lock()
			_ = manager.refreshHarnessLocked()
			manager.mu.Unlock()
		}
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.leafID = nil
}

func (manager *SessionManager) BranchWithSummary(
	branchFromID *string,
	summary string,
	options ...OptionalEntryFields,
) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.harnessStorage != nil {
		if branchFromID != nil {
			if _, ok := manager.harnessStorage.Entry(*branchFromID); !ok {
				return "", fmt.Errorf("Entry %s not found", *branchFromID) //nolint:staticcheck // Upstream error capitalization is observable.
			}
		}
		if err := manager.harnessStorage.SetLeafID(branchFromID); err != nil {
			return "", err
		}
	} else {
		if branchFromID != nil {
			if _, ok := manager.byID[*branchFromID]; !ok {
				return "", fmt.Errorf("Entry %s not found", *branchFromID) //nolint:staticcheck // Upstream error capitalization is observable.
			}
		}
		manager.leafID = cloneString(branchFromID)
	}
	entry, err := manager.newEntryBaseLocked("branch_summary")
	if err != nil {
		return "", err
	}
	entry.ParentID = cloneString(branchFromID)
	entry.FromID = "root"
	if branchFromID != nil {
		entry.FromID = *branchFromID
	}
	entry.Summary = summary
	if err := applyOptionalEntryFields(&entry, options); err != nil {
		return "", err
	}
	return manager.appendEntryLocked(entry)
}

func (manager *SessionManager) GetTree() []*SessionTreeNode {
	if manager.harnessStorage != nil {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if manager.refreshHarnessLocked() != nil {
			return nil
		}
		return manager.getTreeLocked()
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.getTreeLocked()
}

func (manager *SessionManager) getTreeLocked() []*SessionTreeNode {
	nodes := make(map[string]*SessionTreeNode, len(manager.byID))
	var ordered []*SessionEntry
	for _, fileEntry := range manager.fileEntries {
		if fileEntry == nil || fileEntry.Entry == nil {
			continue
		}
		entry := fileEntry.Entry
		ordered = append(ordered, entry)
		node := &SessionTreeNode{Entry: *cloneEntry(entry), Children: []*SessionTreeNode{}}
		if label, ok := manager.labelsByID[entry.ID]; ok {
			node.Label = &label
		}
		if timestamp, ok := manager.labelTimestampsID[entry.ID]; ok {
			node.LabelTimestamp = &timestamp
		}
		nodes[entry.ID] = node
	}
	var roots []*SessionTreeNode
	for _, entry := range ordered {
		node := nodes[entry.ID]
		if entry.ParentID == nil || *entry.ParentID == entry.ID {
			roots = append(roots, node)
			continue
		}
		parent := nodes[*entry.ParentID]
		if parent == nil {
			roots = append(roots, node)
		} else {
			parent.Children = append(parent.Children, node)
		}
	}
	stack := append([]*SessionTreeNode(nil), roots...)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sort.SliceStable(node.Children, func(left, right int) bool {
			leftTime, leftErr := time.Parse(time.RFC3339Nano, node.Children[left].Entry.Timestamp)
			rightTime, rightErr := time.Parse(time.RFC3339Nano, node.Children[right].Entry.Timestamp)
			if leftErr != nil || rightErr != nil {
				return false
			}
			return leftTime.Before(rightTime)
		})
		stack = append(stack, node.Children...)
	}
	return roots
}
