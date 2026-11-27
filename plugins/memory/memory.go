// Package memory defines orb's durable, SDK-ready OptChat memory core.
// It is pure Go with zero third-party dependencies and is importable standalone
// (including by external Postgres/vault backends).
package memory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/internal/uuidv7"
)

const (
	// NodeBytes is the maximum UTF-8 byte length of a single summary node.
	NodeBytes = 512

	// DefaultViewBytes is the default high-water mark for the rendered memory view.
	DefaultViewBytes = 24 * 1024
	// DefaultViewLowBytes is the default low-water mark (2/3 of DefaultViewBytes)
	// targeted during a batch merge.
	DefaultViewLowBytes = 16 * 1024
	// MinViewBytes is the minimum allowed high-water view budget.
	MinViewBytes = 4 * 1024
	// MaxViewBytes is the maximum allowed high-water view budget.
	MaxViewBytes = 64 * 1024

	// ProfileMaxChars is the maximum total rune count of the rendered user profile.
	ProfileMaxChars = 1375

	// CompactContextLines is the maximum number of preceding view lines provided
	// as context to the compactor LLM.
	CompactContextLines = 15
	// CompactMaxTries is the maximum number of cut-at-limit retries before falling
	// back to clean UTF-8 truncation.
	CompactMaxTries = 5
)

// Sentinel errors returned by memory operations.
var (
	ErrNotFound        = errors.New("memory: not found")
	ErrInvalidRef      = errors.New("memory: invalid reference")
	ErrProfileTooLarge = errors.New("memory: profile exceeds character limit")
	ErrEmptyContent    = errors.New("memory: empty content")
)

// EntryKind classifies a verbatim timeline entry.
type EntryKind string

const (
	KindUser EntryKind = "user"
	KindTalk EntryKind = "talk"
	KindWork EntryKind = "work"
	KindNote EntryKind = "note"
)

// Entry is an immutable, sequentially numbered verbatim timeline event.
type Entry struct {
	ID      int       `json:"id"`
	Kind    EntryKind `json:"kind"`
	Text    string    `json:"text"`
	Peer    string    `json:"peer,omitempty"`
	Session string    `json:"session,omitempty"`
	TS      time.Time `json:"ts"`
}

// NodeKey identifies a node in the binary summary tree by level and index.
// Level 0 covers a single entry (span 1); level L covers 2^L entries starting
// at Index * 2^L.
type NodeKey struct {
	Level int `json:"level"`
	Index int `json:"index"`
}

// Start returns the first entry ID covered by this node.
func (k NodeKey) Start() int { return k.Index << k.Level }

// Span returns the number of entries covered by this node (2^Level).
func (k NodeKey) Span() int { return 1 << k.Level }

// End returns the exclusive upper bound entry ID covered by this node.
func (k NodeKey) End() int { return k.Start() + k.Span() }

// Name returns the canonical "id+n" reference string for this node.
func (k NodeKey) Name() string {
	return strconv.Itoa(k.Start()) + "+" + strconv.Itoa(k.Span())
}

// ParseRef parses a canonical "id+n" reference string into (id, n, true).
// Returns (0, 0, false) if ref is malformed or has id < 0 or n < 1.
func ParseRef(ref string) (id int, n int, ok bool) {
	ref = strings.TrimSpace(ref)
	plus := strings.IndexByte(ref, '+')
	if plus <= 0 || plus >= len(ref)-1 {
		return 0, 0, false
	}
	idVal, err1 := strconv.Atoi(ref[:plus])
	nVal, err2 := strconv.Atoi(ref[plus+1:])
	if err1 != nil || err2 != nil || idVal < 0 || nVal < 1 {
		return 0, 0, false
	}
	return idVal, nVal, true
}

// Node is an immutable summary node in the binary summary tree.
type Node struct {
	Level int       `json:"level"`
	Index int       `json:"index"`
	Text  string    `json:"text"`
	TS    time.Time `json:"ts"`
}

// Key returns the NodeKey for n.
func (n Node) Key() NodeKey { return NodeKey{Level: n.Level, Index: n.Index} }

// ProfileEntry is a single curated fact in the user's durable profile.
type ProfileEntry struct {
	ID      string    `json:"id"`
	Content string    `json:"content"`
	TS      time.Time `json:"ts"`
}

// SearchHit is a single lexical match returned by Search.
type SearchHit struct {
	Ref     string    `json:"ref"`
	ID      int       `json:"id"`
	Span    int       `json:"span"`
	Kind    EntryKind `json:"kind,omitempty"`
	Snippet string    `json:"snippet"`
	TS      time.Time `json:"ts"`
	Peer    string    `json:"peer,omitempty"`
	Session string    `json:"session,omitempty"`
}

// Config configures the memory view budget and optional compactor model.
type Config struct {
	ViewBytes      int    `json:"view_bytes,omitempty"`
	CompactorModel string `json:"compactor_model,omitempty"`
}

// NormalizeConfig clamps cfg.ViewBytes to [MinViewBytes, MaxViewBytes]
// (defaulting to DefaultViewBytes when <= 0) and computes low = high * 2 / 3.
func NormalizeConfig(cfg Config) (high int, low int) {
	high = cfg.ViewBytes
	if high <= 0 {
		high = DefaultViewBytes
	} else if high < MinViewBytes {
		high = MinViewBytes
	} else if high > MaxViewBytes {
		high = MaxViewBytes
	}
	low = high * 2 / 3
	return high, low
}

// Store is the minimal, concurrent-safe durable backend contract for OptChat memory.
type Store interface {
	AppendEntry(ctx context.Context, e Entry) (Entry, error)
	PutEntry(ctx context.Context, e Entry) error
	GetEntry(ctx context.Context, id int) (Entry, error)
	ListEntries(ctx context.Context, fromID, limit int) ([]Entry, error)
	EntryCount(ctx context.Context) (int, error)

	PutNode(ctx context.Context, n Node) error
	GetNode(ctx context.Context, key NodeKey) (Node, bool, error)
	ListNodes(ctx context.Context) ([]Node, error)

	GetProfile(ctx context.Context) ([]ProfileEntry, error)
	SaveProfile(ctx context.Context, entries []ProfileEntry) error
}

// TransactionalStore is an optional Store extension for atomic multi-step mutations.
// The callback must use only the supplied Store.
type TransactionalStore interface {
	Store
	Transact(ctx context.Context, fn func(Store) error) error
}

// RemovalStore is an optional Store extension for persisting deletion tombstones
// (e.g. deleted profile entries) for Mitosis sync.
type RemovalStore interface {
	Removed(ctx context.Context) (map[string]string, error)
	Purge(ctx context.Context, key string) error
}

// RenderProfile renders profile entries into a compact newline-delimited block.
func RenderProfile(entries []ProfileEntry) string {
	var b strings.Builder
	for _, e := range entries {
		c := strings.TrimSpace(e.Content)
		if c == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(c)
	}
	return b.String()
}

// ProfileAdd appends a new deduplicated profile entry, enforcing ProfileMaxChars.
// If an entry with the same trimmed content already exists, it returns the
// existing slice and entry without error.
func ProfileAdd(entries []ProfileEntry, content string, now time.Time) ([]ProfileEntry, ProfileEntry, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return entries, ProfileEntry{}, ErrEmptyContent
	}
	for _, e := range entries {
		if strings.EqualFold(strings.TrimSpace(e.Content), content) {
			return entries, e, nil
		}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	id, err := uuidv7.Generate(now)
	if err != nil {
		return entries, ProfileEntry{}, fmt.Errorf("memory: generate profile id: %w", err)
	}
	added := ProfileEntry{ID: id, Content: content, TS: now.UTC()}
	next := make([]ProfileEntry, len(entries)+1)
	copy(next, entries)
	next[len(entries)] = added
	if utf8.RuneCountInString(RenderProfile(next)) > ProfileMaxChars {
		return entries, ProfileEntry{}, ErrProfileTooLarge
	}
	return next, added, nil
}

// ProfileSet updates an existing profile entry by ID, or appends it with that ID
// if not found, enforcing ProfileMaxChars and deduplication.
func ProfileSet(entries []ProfileEntry, id, content string, now time.Time) ([]ProfileEntry, ProfileEntry, error) {
	id = strings.TrimSpace(id)
	content = strings.TrimSpace(content)
	if id == "" || content == "" {
		return entries, ProfileEntry{}, ErrEmptyContent
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	updated := ProfileEntry{ID: id, Content: content, TS: now.UTC()}
	next := make([]ProfileEntry, len(entries))
	copy(next, entries)

	found := false
	for i, e := range next {
		if e.ID == id {
			next[i] = updated
			found = true
			break
		}
	}
	if !found {
		for _, e := range next {
			if strings.EqualFold(strings.TrimSpace(e.Content), content) {
				return entries, e, nil
			}
		}
		next = append(next, updated)
	}
	if utf8.RuneCountInString(RenderProfile(next)) > ProfileMaxChars {
		return entries, ProfileEntry{}, ErrProfileTooLarge
	}
	return next, updated, nil
}

// ProfileRemove removes the profile entry with the given ID.
func ProfileRemove(entries []ProfileEntry, id string) ([]ProfileEntry, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return entries, false
	}
	for i, e := range entries {
		if e.ID == id {
			next := make([]ProfileEntry, 0, len(entries)-1)
			next = append(next, entries[:i]...)
			next = append(next, entries[i+1:]...)
			return next, true
		}
	}
	return entries, false
}
