package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// MemoryStore is a thread-safe in-memory implementation of Store,
// TransactionalStore, and RemovalStore.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[int]Entry
	nextID  int
	nodes   map[NodeKey]Node
	profile []ProfileEntry
	removed map[string]string
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[int]Entry),
		nodes:   make(map[NodeKey]Node),
		removed: make(map[string]string),
	}
}

func (m *MemoryStore) AppendEntry(ctx context.Context, e Entry) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendEntryLocked(e), nil
}

func (m *MemoryStore) appendEntryLocked(e Entry) Entry {
	e.ID = m.nextID
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	m.entries[e.ID] = e
	m.nextID++
	return e
}

func (m *MemoryStore) PutEntry(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.ID < 0 {
		return ErrInvalidRef
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putEntryLocked(e)
	return nil
}

func (m *MemoryStore) putEntryLocked(e Entry) {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	m.entries[e.ID] = e
	if e.ID >= m.nextID {
		m.nextID = e.ID + 1
	}
}

func (m *MemoryStore) GetEntry(ctx context.Context, id int) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getEntryLocked(id)
}

func (m *MemoryStore) getEntryLocked(id int) (Entry, error) {
	e, ok := m.entries[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

func (m *MemoryStore) ListEntries(ctx context.Context, fromID, limit int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listEntriesLocked(fromID, limit), nil
}

func (m *MemoryStore) listEntriesLocked(fromID, limit int) []Entry {
	if fromID < 0 {
		fromID = 0
	}
	ids := make([]int, 0, len(m.entries))
	for id := range m.entries {
		if id >= fromID {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]Entry, len(ids))
	for i, id := range ids {
		out[i] = m.entries[id]
	}
	return out
}

func (m *MemoryStore) EntryCount(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nextID, nil
}

func (m *MemoryStore) PutNode(ctx context.Context, n Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putNodeLocked(n)
	return nil
}

func (m *MemoryStore) putNodeLocked(n Node) {
	if n.TS.IsZero() {
		n.TS = time.Now().UTC()
	}
	m.nodes[n.Key()] = n
}

func (m *MemoryStore) GetNode(ctx context.Context, key NodeKey) (Node, bool, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[key]
	return n, ok, nil
}

func (m *MemoryStore) ListNodes(ctx context.Context) ([]Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listNodesLocked(), nil
}

func (m *MemoryStore) listNodesLocked() []Node {
	out := make([]Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level < out[j].Level
		}
		return out[i].Index < out[j].Index
	})
	return out
}

func (m *MemoryStore) GetProfile(ctx context.Context) ([]ProfileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ProfileEntry, len(m.profile))
	copy(out, m.profile)
	return out, nil
}

func (m *MemoryStore) SaveProfile(ctx context.Context, entries []ProfileEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveProfileLocked(entries)
	return nil
}

func (m *MemoryStore) saveProfileLocked(entries []ProfileEntry) {
	nextSet := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		nextSet[e.ID] = struct{}{}
		delete(m.removed, profileSyncKey(e.ID))
	}
	for _, old := range m.profile {
		if _, kept := nextSet[old.ID]; !kept && old.ID != "" {
			raw, _ := json.Marshal(old)
			sum := sha256.Sum256(raw)
			m.removed[profileSyncKey(old.ID)] = hex.EncodeToString(sum[:])
		}
	}
	cp := make([]ProfileEntry, len(entries))
	copy(cp, entries)
	m.profile = cp
}

func (m *MemoryStore) Removed(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.removed))
	for k, v := range m.removed {
		out[k] = v
	}
	return out, nil
}

func (m *MemoryStore) Purge(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.removed, key)
	return nil
}

// Transact executes fn atomically under the store's write lock, rolling back
// all mutations if fn returns a non-nil error.
func (m *MemoryStore) Transact(ctx context.Context, fn func(Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Snapshot for rollback on error.
	snapEntries := make(map[int]Entry, len(m.entries))
	for k, v := range m.entries {
		snapEntries[k] = v
	}
	snapNextID := m.nextID
	snapNodes := make(map[NodeKey]Node, len(m.nodes))
	for k, v := range m.nodes {
		snapNodes[k] = v
	}
	snapProfile := make([]ProfileEntry, len(m.profile))
	copy(snapProfile, m.profile)
	snapRemoved := make(map[string]string, len(m.removed))
	for k, v := range m.removed {
		snapRemoved[k] = v
	}

	if err := fn(&memTx{m: m}); err != nil {
		m.entries = snapEntries
		m.nextID = snapNextID
		m.nodes = snapNodes
		m.profile = snapProfile
		m.removed = snapRemoved
		return err
	}
	return nil
}

// memTx exposes Store + RemovalStore without re-acquiring m.mu inside Transact.
type memTx struct {
	m *MemoryStore
}

func (t *memTx) AppendEntry(ctx context.Context, e Entry) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	return t.m.appendEntryLocked(e), nil
}

func (t *memTx) PutEntry(ctx context.Context, e Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.ID < 0 {
		return ErrInvalidRef
	}
	t.m.putEntryLocked(e)
	return nil
}

func (t *memTx) GetEntry(ctx context.Context, id int) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	return t.m.getEntryLocked(id)
}

func (t *memTx) ListEntries(ctx context.Context, fromID, limit int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return t.m.listEntriesLocked(fromID, limit), nil
}

func (t *memTx) EntryCount(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return t.m.nextID, nil
}

func (t *memTx) PutNode(ctx context.Context, n Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.m.putNodeLocked(n)
	return nil
}

func (t *memTx) GetNode(ctx context.Context, key NodeKey) (Node, bool, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, false, err
	}
	n, ok := t.m.nodes[key]
	return n, ok, nil
}

func (t *memTx) ListNodes(ctx context.Context) ([]Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return t.m.listNodesLocked(), nil
}

func (t *memTx) GetProfile(ctx context.Context) ([]ProfileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]ProfileEntry, len(t.m.profile))
	copy(out, t.m.profile)
	return out, nil
}

func (t *memTx) SaveProfile(ctx context.Context, entries []ProfileEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.m.saveProfileLocked(entries)
	return nil
}

func (t *memTx) Removed(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(t.m.removed))
	for k, v := range t.m.removed {
		out[k] = v
	}
	return out, nil
}

func (t *memTx) Purge(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(t.m.removed, key)
	return nil
}
