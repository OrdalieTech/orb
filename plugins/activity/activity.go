// Package activity normalizes session-local work without owning its execution.
package activity

import (
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
)

const Channel = "orb:activity"

type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Waiting   State = "waiting"
	Completed State = "completed"
	Failed    State = "failed"
	Cancelled State = "cancelled"
	Unknown   State = "unknown"
)

func (s State) Terminal() bool { return s == Completed || s == Failed || s == Cancelled }

type Kind string

const (
	Agent   Kind = "agent"
	Process Kind = "process"
)

// Record is a full observation, not a patch. IDs belong to the publisher, not tool rows.
type Record struct {
	SessionID string
	Source    string
	ID        string
	Kind      Kind
	Title     string
	Detail    string
	State     State
	Sequence  uint64
	Started   time.Time
	Updated   time.Time
}

// Publisher fences delayed callbacks to their original session and gives each
// attachment its own ID namespace. The bus resolver may follow an extension reload.
func Publisher(bus func() extensions.EventBus, sessionID, source string) func(Record) {
	namespace := rand.Text()
	var sequence atomic.Uint64
	return func(record Record) {
		if record.ID == "" {
			return
		}
		record.SessionID, record.Source = sessionID, source
		record.ID = namespace + "/" + record.ID
		record.Sequence = sequence.Add(1)
		record.Updated = time.Now()
		// Extension callbacks can outlive disposal; only the bus boundary is guarded.
		defer func() { _ = recover() }()
		if events := bus(); events != nil {
			events.Emit(context.Background(), Channel, record)
		}
	}
}

type identity struct{ source, id string }

type Store struct {
	mu        sync.Mutex
	sessionID string
	records   map[identity]Record
	dismissed map[identity]bool
}

func NewStore(sessionID string) *Store {
	return &Store{sessionID: sessionID, records: make(map[identity]Record), dismissed: make(map[identity]bool)}
}

func (s *Store) Apply(record Record) bool {
	if record.SessionID != s.sessionID || record.ID == "" || record.Source == "" || record.Title == "" || record.Sequence == 0 {
		return false
	}
	if record.Kind != Agent && record.Kind != Process {
		return false
	}
	switch record.State {
	case Queued, Running, Waiting, Completed, Failed, Cancelled, Unknown:
	default:
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record.Title = boundedText(record.Title, 160)
	record.Detail = boundedText(record.Detail, 512)
	key := identity{record.Source, record.ID}
	old, exists := s.records[key]
	if exists && (old.Sequence >= record.Sequence || old.State.Terminal()) {
		return false
	}
	if !exists && len(s.records) >= 1024 {
		return false
	}
	if record.Updated.IsZero() {
		record.Updated = time.Now()
	}
	if exists {
		record.Started = old.Started
	} else if record.Started.IsZero() {
		record.Started = record.Updated
	}
	s.records[key] = record
	// Keep a bounded recent history, without ever evicting live work.
	var recent []Record
	for _, r := range s.records {
		if r.State.Terminal() {
			recent = append(recent, r)
		}
	}
	slices.SortFunc(recent, func(a, b Record) int {
		if n := b.Updated.Compare(a.Updated); n != 0 {
			return n
		}
		if n := strings.Compare(a.Source, b.Source); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, r := range recent[min(16, len(recent)):] {
		key := identity{r.Source, r.ID}
		delete(s.records, key)
		delete(s.dismissed, key)
	}
	return true
}

func (s *Store) Snapshot() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]Record, 0, len(s.records))
	for key, r := range s.records {
		if !s.dismissed[key] {
			records = append(records, r)
		}
	}
	slices.SortFunc(records, func(a, b Record) int {
		rank := func(r Record) int {
			if r.State.Terminal() {
				return 2
			}
			if r.State == Waiting || r.State == Unknown {
				return 0
			}
			return 1
		}
		if n := rank(a) - rank(b); n != 0 {
			return n
		}
		if a.State.Terminal() {
			if n := b.Updated.Compare(a.Updated); n != 0 {
				return n
			}
		}
		if n := a.Started.Compare(b.Started); n != 0 {
			return n
		}
		if a.Source < b.Source || a.Source == b.Source && a.ID < b.ID {
			return -1
		}
		if a.Source == b.Source && a.ID == b.ID {
			return 0
		}
		return 1
	})
	return records
}

func boundedText(text string, limit int) string {
	count := 0
	for i := range text {
		if count == limit {
			return strings.Clone(text[:i])
		}
		count++
	}
	return text
}

func (s *Store) ClearCompleted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, r := range s.records {
		if r.State.Terminal() {
			s.dismissed[key] = true
		}
	}
}
