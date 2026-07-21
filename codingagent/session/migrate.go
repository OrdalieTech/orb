package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
	textunicode "golang.org/x/text/encoding/unicode"
)

const sessionReadBufferSize = 1024 * 1024

type IDGenerator func() (string, error)

type loadedSessionFile struct {
	entries []*FileEntry
	exists  bool
	size    int64
}

// ParseSessionEntries parses valid JSON lines and silently skips blank or
// malformed lines. It does not validate the session header or run migrations.
func ParseSessionEntries(content string) []*FileEntry {
	trimmed := strings.TrimFunc(content, isJSTrimSpace)
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	entries := make([]*FileEntry, 0, len(lines))
	for _, line := range lines {
		if entry := parseSessionEntryLine(line); entry != nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

func parseSessionEntryLine(line string) *FileEntry {
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes([]byte(line))
	line = string(decoded)
	if strings.TrimFunc(line, isJSTrimSpace) == "" || !json.Valid([]byte(line)) {
		return nil
	}
	raw := json.RawMessage(line)
	object, err := parseOrderedObject(raw)
	if err != nil {
		return decodeFileEntry(nil, raw)
	}
	return decodeFileEntry(object, raw)
}

func isJSTrimSpace(character rune) bool {
	switch {
	case character >= '\t' && character <= '\r':
		return true
	case character == ' ', character == '\u00a0', character == '\u1680', character == '\u2028', character == '\u2029', character == '\u202f', character == '\u205f', character == '\u3000', character == '\ufeff':
		return true
	case character >= '\u2000' && character <= '\u200a':
		return true
	default:
		return false
	}
}

// LoadEntriesFromFile streams a session without imposing a maximum line size.
// A valid file starts with a session record whose id is a JSON string.
func LoadEntriesFromFile(path string) ([]*FileEntry, error) {
	loaded, err := loadSessionFile(path)
	return loaded.entries, err
}

func loadSessionFile(path string) (loadedSessionFile, error) {
	path = normalizePath(path)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return loadedSessionFile{}, nil
	}
	if err != nil {
		return loadedSessionFile{}, err
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReaderSize(file, sessionReadBufferSize)
	var entries []*FileEntry
	for {
		line, readErr := reader.ReadString('\n')
		if entry := parseSessionEntryLine(line); entry != nil {
			entries = append(entries, entry)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return loadedSessionFile{}, readErr
			}
			break
		}
	}
	info, err := file.Stat()
	if err != nil {
		return loadedSessionFile{}, err
	}
	if !validSessionHeader(entries) {
		entries = nil
	}
	return loadedSessionFile{entries: entries, exists: true, size: info.Size()}, nil
}

func validSessionHeader(entries []*FileEntry) bool {
	if len(entries) == 0 || entries[0] == nil || entries[0].object == nil || entries[0].Type != "session" {
		return false
	}
	id, _ := entries[0].object.get("id")
	_, valid := decodeString(id)
	return valid
}

// MigrateSessionEntries upgrades v1/v2 records to v3 in place. The optional
// generator supplies entry-id candidates; collisions are retried exactly as in
// upstream.
func MigrateSessionEntries(entries []*FileEntry, generators ...IDGenerator) (bool, error) {
	generator := IDGenerator(randomEntryCandidate)
	if len(generators) > 0 && generators[0] != nil {
		generator = generators[0]
	}
	header := findHeader(entries)
	version := 1
	if header != nil && header.Header != nil && header.Header.Version != nil {
		version = *header.Header.Version
	}
	if version >= CurrentVersion {
		return false, nil
	}
	if version < 2 {
		if err := migrateV1ToV2(entries, generator); err != nil {
			return false, err
		}
	}
	if version < 3 {
		migrateV2ToV3(entries)
	}
	if err := normalizeMigratedEntries(entries); err != nil {
		return false, err
	}
	return true, nil
}

func normalizeMigratedEntries(entries []*FileEntry) error {
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		encoded, err := entry.MarshalJSON()
		if err != nil {
			return err
		}
		normalized, err := ai.NormalizeJSONStringifyJSON(encoded)
		if err != nil {
			return err
		}
		object, objectErr := parseOrderedObject(normalized)
		if objectErr == nil {
			*entry = *decodeFileEntry(object, nil)
			continue
		}
		*entry = *decodeFileEntry(nil, normalized)
	}
	return nil
}

func migrateV1ToV2(entries []*FileEntry, generator IDGenerator) error {
	ids := make(map[string]struct{})
	var previousID *string
	for _, fileEntry := range entries {
		if fileEntry == nil || fileEntry.object == nil {
			continue
		}
		if fileEntry.Type == "session" {
			fileEntry.object.set("version", rawInt(2))
			syncFileEntry(fileEntry)
			continue
		}
		id, err := generateUniqueID(ids, generator)
		if err != nil {
			return err
		}
		fileEntry.object.set("id", mustRawString(id))
		if previousID == nil {
			fileEntry.object.set("parentId", rawNull())
		} else {
			fileEntry.object.set("parentId", mustRawString(*previousID))
		}
		currentID := id
		previousID = &currentID

		if fileEntry.Type == "compaction" {
			if rawIndex, ok := fileEntry.object.get("firstKeptEntryIndex"); ok {
				if index, valid := decodeInt(rawIndex); valid && index >= 0 && index < int64(len(entries)) {
					target := entries[index]
					if target != nil && target.Type != "session" && target.object != nil {
						if targetID, ok := stringMember(target.object, "id"); ok {
							fileEntry.object.set("firstKeptEntryId", mustRawString(targetID))
						}
					}
				}
				fileEntry.object.delete("firstKeptEntryIndex")
			}
		}
		syncFileEntry(fileEntry)
	}
	return nil
}

func migrateV2ToV3(entries []*FileEntry) {
	for _, fileEntry := range entries {
		if fileEntry == nil || fileEntry.object == nil {
			continue
		}
		if fileEntry.Type == "session" {
			fileEntry.object.set("version", rawInt(CurrentVersion))
			syncFileEntry(fileEntry)
			continue
		}
		if fileEntry.Type != "message" {
			continue
		}
		messageRaw, ok := fileEntry.object.get("message")
		if !ok || len(messageRaw) == 0 || bytes.TrimSpace(messageRaw)[0] != '{' {
			continue
		}
		message, err := parseOrderedObject(messageRaw)
		if err != nil {
			continue
		}
		roleRaw, ok := message.get("role")
		role, valid := decodeString(roleRaw)
		if !ok || !valid || role != "hookMessage" {
			continue
		}
		message.set("role", mustRawString("custom"))
		encoded, err := message.marshal()
		if err != nil {
			continue
		}
		fileEntry.object.set("message", encoded)
		syncFileEntry(fileEntry)
	}
}

func findHeader(entries []*FileEntry) *FileEntry {
	for _, entry := range entries {
		if entry != nil && entry.Type == "session" {
			return entry
		}
	}
	return nil
}

func syncFileEntry(entry *FileEntry) {
	decoded := decodeFileEntry(entry.object, nil)
	entry.Type = decoded.Type
	entry.Header = decoded.Header
	entry.Entry = decoded.Entry
	entry.raw = nil
}

func findUniqueID[V any](existing map[string]V, generator IDGenerator) (string, error) {
	for range 100 {
		id, err := generator()
		if err != nil {
			return "", err
		}
		if _, found := existing[id]; !found {
			return id, nil
		}
	}
	return randomUUID()
}

func generateUniqueID[V any](existing map[string]V, generator IDGenerator) (string, error) {
	id, err := findUniqueID(existing, generator)
	if err != nil {
		return "", err
	}
	var reserved V
	existing[id] = reserved
	return id, nil
}
