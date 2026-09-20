package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxConcurrentSessionInfoLoads = 10

type SessionInfo struct {
	Path              string
	ID                string
	CWD               string
	Name              *string
	ParentSessionPath *string
	Created           time.Time
	Modified          time.Time
	MessageCount      int
	FirstMessage      string
	AllMessagesText   string
}

type SessionListProgress func(loaded, total int)

// SessionListUpdate is a progressive, context-aware listing update. Sessions
// is populated periodically and is always sorted by activity when present.
type SessionListUpdate struct {
	Loaded   int
	Total    int
	Sessions []SessionInfo
}

// SessionListUpdateFunc receives count updates and periodic partial results.
type SessionListUpdateFunc func(SessionListUpdate)

const (
	currentSessionPublishInterval = 10
	allSessionPublishInterval     = 100
)

// FindByID finds an exact project session ID by reading bounded headers only.
// A custom flat session directory is filtered by the cwd stored in each header.
func FindByID(cwd, id, sessionDir string, options ...Option) string {
	resolved := applyOptions(options)
	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return ""
	}
	explicitDir := sessionDir != ""
	if !explicitDir {
		sessionDir, err = DefaultSessionDir(resolvedCWD, resolved.agentDir)
		if err != nil {
			return ""
		}
	} else {
		sessionDir = normalizePath(sessionDir)
	}
	defaultDir, err := DefaultSessionDirPath(resolvedCWD, resolved.agentDir)
	if err != nil {
		return ""
	}
	filterCWD := explicitDir && sessionDir != defaultDir
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(sessionDir, entry.Name())
		header := readSessionHeader(path)
		if header == nil || header.ID != id {
			continue
		}
		if filterCWD {
			headerCWD, resolveErr := resolvePath(header.CWD)
			if header.CWD == "" || resolveErr != nil || headerCWD != resolvedCWD {
				continue
			}
		}
		return path
	}
	return ""
}

// List returns sessions for cwd. A custom flat session directory is filtered
// by the cwd stored in each header.
func List(cwd, sessionDir string, onProgress SessionListProgress, options ...Option) []SessionInfo {
	sessions, _ := ListContext(context.Background(), cwd, sessionDir, func(update SessionListUpdate) {
		if onProgress != nil {
			onProgress(update.Loaded, update.Total)
		}
	}, options...)
	return sessions
}

// ListContext is the cancellable, progressive companion to List. The legacy
// List API remains a background-context adapter with count-only progress.
func ListContext(ctx context.Context, cwd, sessionDir string, onUpdate SessionListUpdateFunc, options ...Option) ([]SessionInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved := applyOptions(options)
	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return nil, nil
	}
	explicitDir := sessionDir != ""
	if !explicitDir {
		sessionDir, err = DefaultSessionDir(resolvedCWD, resolved.agentDir)
		if err != nil {
			return nil, nil
		}
	} else {
		sessionDir = normalizePath(sessionDir)
	}
	defaultDir, err := DefaultSessionDirPath(resolvedCWD, resolved.agentDir)
	if err != nil {
		return nil, nil
	}
	filterCWD := explicitDir && sessionDir != defaultDir
	include := func(info SessionInfo) bool {
		if !filterCWD {
			return true
		}
		infoCWD, resolveErr := resolvePath(info.CWD)
		return resolveErr == nil && info.CWD != "" && infoCWD == resolvedCWD
	}
	updates := onUpdate
	if onUpdate != nil && filterCWD {
		updates = func(update SessionListUpdate) {
			if update.Sessions != nil {
				filtered := make([]SessionInfo, 0, len(update.Sessions))
				for _, info := range update.Sessions {
					if include(info) {
						filtered = append(filtered, info)
					}
				}
				update.Sessions = filtered
			}
			onUpdate(update)
		}
	}
	sessions, err := listSessionsFromDirContext(ctx, sessionDir, updates, currentSessionPublishInterval)
	if err != nil {
		return nil, err
	}
	if filterCWD {
		filtered := sessions[:0]
		for _, info := range sessions {
			if include(info) {
				filtered = append(filtered, info)
			}
		}
		sessions = filtered
	}
	sortSessionInfos(sessions)
	return sessions, nil
}

// ListAll returns every session in a custom flat directory, or all project
// directories below the configured agent sessions directory.
func ListAll(sessionDir string, onProgress SessionListProgress, options ...Option) []SessionInfo {
	sessions, _ := ListAllContext(context.Background(), sessionDir, func(update SessionListUpdate) {
		if onProgress != nil {
			onProgress(update.Loaded, update.Total)
		}
	}, options...)
	return sessions
}

// ListAllContext is the cancellable, progressive companion to ListAll.
func ListAllContext(ctx context.Context, sessionDir string, onUpdate SessionListUpdateFunc, options ...Option) ([]SessionInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved := applyOptions(options)
	if sessionDir != "" {
		sessions, err := listSessionsFromDirContext(ctx, normalizePath(sessionDir), onUpdate, currentSessionPublishInterval)
		if err != nil {
			return nil, err
		}
		sortSessionInfos(sessions)
		return sessions, nil
	}
	agentDir := resolved.agentDir
	var err error
	if agentDir == "" {
		agentDir, err = defaultAgentDir()
	} else {
		agentDir, err = resolvePath(agentDir)
	}
	if err != nil {
		return nil, nil
	}
	root := filepath.Join(agentDir, "sessions")
	directories, err := os.ReadDir(root)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	var files []string
	for _, directory := range directories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !directory.IsDir() && directory.Type()&os.ModeSymlink == 0 {
			continue
		}
		dir := filepath.Join(root, directory.Name())
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".jsonl") {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	candidates := make([]sessionFileCandidate, 0, len(files))
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, statErr := os.Stat(path)
		candidates = append(candidates, sessionFileCandidate{path: path, info: info, statFailed: statErr != nil})
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftInfo := candidates[left].info
		rightInfo := candidates[right].info
		if leftInfo == nil && rightInfo == nil {
			return filepath.Base(candidates[left].path) > filepath.Base(candidates[right].path)
		}
		if leftInfo == nil {
			return false
		}
		if rightInfo == nil {
			return true
		}
		leftModified := leftInfo.ModTime()
		rightModified := rightInfo.ModTime()
		if leftModified.Equal(rightModified) {
			return filepath.Base(candidates[left].path) > filepath.Base(candidates[right].path)
		}
		return leftModified.After(rightModified)
	})
	sessions, err := buildSessionInfosContext(ctx, candidates, onUpdate, allSessionPublishInterval, true)
	if err != nil {
		return nil, err
	}
	sortSessionInfos(sessions)
	return sessions, nil
}

type sessionFileCandidate struct {
	path       string
	info       os.FileInfo
	statFailed bool
}

func listSessionsFromDirContext(ctx context.Context, dir string, onUpdate SessionListUpdateFunc, publishInterval int) ([]SessionInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	candidates := make([]sessionFileCandidate, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			candidates = append(candidates, sessionFileCandidate{path: filepath.Join(dir, entry.Name())})
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return filepath.Base(candidates[left].path) > filepath.Base(candidates[right].path)
	})
	return buildSessionInfosContext(ctx, candidates, onUpdate, publishInterval, false)
}

func buildSessionInfosContext(ctx context.Context, files []sessionFileCandidate, onUpdate SessionListUpdateFunc, publishInterval int, waitForFirstCandidate bool) ([]SessionInfo, error) {
	if publishInterval < 1 {
		publishInterval = 1
	}
	results := make([]*SessionInfo, len(files))
	jobs := make(chan int)
	workerCount := min(len(files), maxConcurrentSessionInfoLoads)
	var workers sync.WaitGroup
	var progress sync.Mutex
	loaded := 0
	firstCandidateLoaded := false
	partial := make([]SessionInfo, 0, len(files))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					continue
				}
				results[index] = buildSessionInfoContext(ctx, files[index])
				if ctx.Err() != nil {
					continue
				}
				progress.Lock()
				loaded++
				if index == 0 {
					firstCandidateLoaded = true
				}
				if results[index] != nil {
					partial = append(partial, *results[index])
				}
				if onUpdate != nil {
					update := SessionListUpdate{Loaded: loaded, Total: len(files)}
					publish := loaded == 1 || loaded%publishInterval == 0 || loaded == len(files)
					if waitForFirstCandidate {
						publish = firstCandidateLoaded && (index == 0 || loaded%publishInterval == 0 || loaded == len(files))
					}
					if publish {
						update.Sessions = append([]SessionInfo(nil), partial...)
						sortSessionInfos(update.Sessions)
					}
					onUpdate(update)
				}
				progress.Unlock()
			}
		}()
	}
	for index := range files {
		if err := ctx.Err(); err != nil {
			close(jobs)
			workers.Wait()
			return nil, err
		}
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sessions := make([]SessionInfo, 0, len(files))
	for _, info := range results {
		if info != nil {
			sessions = append(sessions, *info)
		}
	}
	return sessions, nil
}

func buildSessionInfo(path string) *SessionInfo {
	return buildSessionInfoContext(context.Background(), sessionFileCandidate{path: path})
}

func buildSessionInfoContext(ctx context.Context, candidate sessionFileCandidate) *SessionInfo {
	stat := candidate.info
	var err error
	if stat == nil && !candidate.statFailed {
		stat, err = os.Stat(candidate.path)
	}
	if err != nil || stat == nil {
		return nil
	}
	file, err := os.Open(candidate.path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReaderSize(file, 64*1024)
	var entries []*FileEntry
	var line []byte
	for {
		if ctx.Err() != nil {
			return nil
		}
		fragment, readErr := reader.ReadSlice('\n')
		line = append(line, fragment...)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if entry := parseSessionEntryLine(string(line)); entry != nil {
			entries = append(entries, entry)
		}
		line = line[:0]
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil
			}
			break
		}
	}
	if len(entries) == 0 || entries[0].Header == nil {
		return nil
	}
	header := entries[0].Header
	result := &SessionInfo{
		Path: candidate.path, ID: header.ID, CWD: header.CWD,
		ParentSessionPath: cloneString(header.ParentSession),
		FirstMessage:      "(no messages)",
		Modified:          truncateToJSMilliseconds(stat.ModTime()),
	}
	if created, parseErr := time.Parse(time.RFC3339Nano, header.Timestamp); parseErr == nil {
		result.Created = truncateToJSMilliseconds(created)
		result.Modified = result.Created
	}
	var firstMessage string
	var allMessages []string
	var lastActivityMilliseconds float64
	for _, fileEntry := range entries[1:] {
		if fileEntry == nil || fileEntry.Entry == nil {
			continue
		}
		entry := fileEntry.Entry
		if entry.Type == "session_info" {
			name, valid := listedSessionName(entry)
			if !valid {
				return nil
			}
			result.Name = name
		}
		if entry.Type != "message" {
			continue
		}
		result.MessageCount++
		message, valid := listedSessionMessage(entry)
		if !valid {
			return nil
		}
		if message.role != "user" && message.role != "assistant" {
			continue
		}
		if message.hasActivity {
			lastActivityMilliseconds = math.Max(lastActivityMilliseconds, message.activityMilliseconds)
		}
		if message.text == "" {
			continue
		}
		allMessages = append(allMessages, message.text)
		if firstMessage == "" && message.role == "user" {
			firstMessage = message.text
		}
	}
	if firstMessage != "" {
		result.FirstMessage = firstMessage
	}
	result.AllMessagesText = strings.Join(allMessages, " ")
	if lastActivityMilliseconds > 0 {
		result.Modified = time.UnixMilli(int64(lastActivityMilliseconds)).UTC()
	}
	return result
}

func listedSessionName(entry *SessionEntry) (*string, bool) {
	if entry.object == nil {
		return nil, true
	}
	raw, exists := entry.object.get("name")
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	name, valid := decodeString(raw)
	if !valid {
		return nil, false
	}
	name = trimJSSpace(name)
	if name == "" {
		return nil, true
	}
	return &name, true
}

type listedMessage struct {
	role                 string
	text                 string
	activityMilliseconds float64
	hasActivity          bool
}

func listedSessionMessage(entry *SessionEntry) (listedMessage, bool) {
	raw := bytes.TrimSpace(entry.Message)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return listedMessage{}, false
	}
	if raw[0] != '{' {
		return listedMessage{}, true
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return listedMessage{}, false
	}
	role, roleIsString := decodeString(object["role"])
	if !roleIsString {
		return listedMessage{}, true
	}
	content, hasContent := object["content"]
	if !hasContent {
		return listedMessage{role: role}, true
	}
	message := listedMessage{role: role}
	if role != "user" && role != "assistant" {
		return message, true
	}
	message.activityMilliseconds, message.hasActivity = listedMessageActivity(object["timestamp"], entry.Timestamp)
	text, valid := listedMessageText(content)
	if !valid {
		return listedMessage{}, false
	}
	message.text = text
	return message, true
}

func listedMessageActivity(timestamp json.RawMessage, entryTimestamp string) (float64, bool) {
	if len(timestamp) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(timestamp))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err == nil {
			if number, ok := value.(json.Number); ok {
				milliseconds, numberErr := number.Float64()
				if numberErr == nil && !math.IsNaN(milliseconds) {
					return milliseconds, true
				}
			}
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, entryTimestamp)
	if err != nil {
		return 0, false
	}
	return float64(parsed.UnixMilli()), true
}

func listedMessageText(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false
	}
	if trimmed[0] == '"' {
		text, valid := decodeString(trimmed)
		return text, valid
	}
	if trimmed[0] != '[' {
		return "", false
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return "", false
	}
	texts := make([]string, 0, len(blocks))
	for _, rawBlock := range blocks {
		block := bytes.TrimSpace(rawBlock)
		if len(block) == 0 || bytes.Equal(block, []byte("null")) {
			return "", false
		}
		if block[0] != '{' {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(block, &object); err != nil || object == nil {
			return "", false
		}
		blockType, typeIsString := decodeString(object["type"])
		if !typeIsString || blockType != "text" {
			continue
		}
		text, textIsString := decodeString(object["text"])
		if !textIsString {
			text = ""
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, " "), true
}

func truncateToJSMilliseconds(value time.Time) time.Time {
	return time.UnixMilli(value.UnixMilli()).In(value.Location())
}

func sortSessionInfos(sessions []SessionInfo) {
	sort.SliceStable(sessions, func(left, right int) bool {
		return sessions[left].Modified.After(sessions[right].Modified)
	})
}
