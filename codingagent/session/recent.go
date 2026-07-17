package session

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type recentSession struct {
	path     string
	modified time.Time
}

// FindMostRecentSession returns an empty string on discovery errors, matching
// upstream's best-effort continue behavior. When cwd is non-empty, old headers
// without cwd are excluded.
func FindMostRecentSession(sessionDir, cwd string) string {
	sessionDir = normalizePath(sessionDir)
	var resolvedCWD string
	if cwd != "" {
		var err error
		resolvedCWD, err = resolvePath(cwd)
		if err != nil {
			return ""
		}
	}
	directoryEntries, err := os.ReadDir(sessionDir)
	if err != nil {
		return ""
	}
	var sessions []recentSession
	for _, directoryEntry := range directoryEntries {
		if directoryEntry.IsDir() || !strings.HasSuffix(directoryEntry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(sessionDir, directoryEntry.Name())
		header := readSessionHeader(path)
		if header == nil {
			continue
		}
		if resolvedCWD != "" {
			if header.CWD == "" {
				continue
			}
			headerCWD, err := resolvePath(header.CWD)
			if err != nil || headerCWD != resolvedCWD {
				continue
			}
		}
		info, err := directoryEntry.Info()
		if err != nil {
			continue
		}
		sessions = append(sessions, recentSession{path: path, modified: info.ModTime()})
	}
	sort.SliceStable(sessions, func(left, right int) bool {
		return sessions[left].modified.After(sessions[right].modified)
	})
	if len(sessions) == 0 {
		return ""
	}
	return sessions[0].path
}

func readSessionHeader(path string) *SessionHeader {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 512)
	count, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return nil
	}
	firstLine := strings.SplitN(string(buffer[:count]), "\n", 2)[0]
	entry := parseSessionEntryLine(firstLine)
	if entry == nil || entry.Type != "session" || entry.Header == nil {
		return nil
	}
	if rawID, ok := entry.object.get("id"); !ok {
		return nil
	} else if _, valid := decodeString(rawID); !valid {
		return nil
	}
	return entry.Header
}
