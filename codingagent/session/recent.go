package session

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxSessionHeaderScanBytes = 1024 * 1024

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
	reader := bufio.NewReaderSize(io.LimitReader(file, maxSessionHeaderScanBytes+1), 4096)
	scanned := 0
	for {
		line, readErr := reader.ReadString('\n')
		scanned += len(line)
		if scanned > maxSessionHeaderScanBytes {
			return nil
		}
		if entry := parseSessionEntryLine(line); entry != nil {
			if entry.Type != "session" || entry.Header == nil {
				return nil
			}
			rawID, ok := entry.object.get("id")
			if !ok {
				return nil
			}
			if _, valid := decodeString(rawID); !valid {
				return nil
			}
			return entry.Header
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil
			}
			return nil
		}
	}
}
