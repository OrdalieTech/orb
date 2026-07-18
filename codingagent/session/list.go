package session

import (
	"bytes"
	"encoding/json"
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

// List returns sessions for cwd. A custom flat session directory is filtered
// by the cwd stored in each header.
func List(cwd, sessionDir string, onProgress SessionListProgress, options ...Option) []SessionInfo {
	resolved := applyOptions(options)
	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return nil
	}
	explicitDir := sessionDir != ""
	if !explicitDir {
		sessionDir, err = DefaultSessionDir(resolvedCWD, resolved.agentDir)
		if err != nil {
			return nil
		}
	} else {
		sessionDir = normalizePath(sessionDir)
	}
	defaultDir, err := DefaultSessionDirPath(resolvedCWD, resolved.agentDir)
	if err != nil {
		return nil
	}
	filterCWD := explicitDir && sessionDir != defaultDir
	sessions := listSessionsFromDir(sessionDir, onProgress)
	if filterCWD {
		filtered := sessions[:0]
		for _, info := range sessions {
			infoCWD, resolveErr := resolvePath(info.CWD)
			if resolveErr == nil && info.CWD != "" && infoCWD == resolvedCWD {
				filtered = append(filtered, info)
			}
		}
		sessions = filtered
	}
	sortSessionInfos(sessions)
	return sessions
}

// ListAll returns every session in a custom flat directory, or all project
// directories below the configured agent sessions directory.
func ListAll(sessionDir string, onProgress SessionListProgress, options ...Option) []SessionInfo {
	resolved := applyOptions(options)
	if sessionDir != "" {
		sessions := listSessionsFromDir(normalizePath(sessionDir), onProgress)
		sortSessionInfos(sessions)
		return sessions
	}
	agentDir := resolved.agentDir
	var err error
	if agentDir == "" {
		agentDir, err = defaultAgentDir()
	} else {
		agentDir, err = resolvePath(agentDir)
	}
	if err != nil {
		return nil
	}
	root := filepath.Join(agentDir, "sessions")
	directories, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var files []string
	for _, directory := range directories {
		if !directory.IsDir() {
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
	sessions := buildSessionInfos(files, onProgress)
	sortSessionInfos(sessions)
	return sessions
}

func listSessionsFromDir(dir string, onProgress SessionListProgress) []SessionInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return buildSessionInfos(files, onProgress)
}

func buildSessionInfos(files []string, onProgress SessionListProgress) []SessionInfo {
	results := make([]*SessionInfo, len(files))
	jobs := make(chan int)
	workerCount := min(len(files), maxConcurrentSessionInfoLoads)
	var workers sync.WaitGroup
	var progress sync.Mutex
	loaded := 0
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = buildSessionInfo(files[index])
				progress.Lock()
				loaded++
				if onProgress != nil {
					onProgress(loaded, len(files))
				}
				progress.Unlock()
			}
		}()
	}
	for index := range files {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	sessions := make([]SessionInfo, 0, len(files))
	for _, info := range results {
		if info != nil {
			sessions = append(sessions, *info)
		}
	}
	return sessions
}

func buildSessionInfo(path string) *SessionInfo {
	stat, err := os.Stat(path)
	if err != nil {
		return nil
	}
	entries, err := LoadEntriesFromFile(path)
	if err != nil || len(entries) == 0 || entries[0].Header == nil {
		return nil
	}
	header := entries[0].Header
	result := &SessionInfo{
		Path: path, ID: header.ID, CWD: header.CWD,
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
