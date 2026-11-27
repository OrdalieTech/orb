package filestore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/gofrs/flock"
)

// compactAt is the journal size from which a write rewrites it to a compact
// snapshot once the file is more than 4x the live compacted size.
const compactAt = 256 << 10

type record struct {
	Entry          *memory.Entry          `json:"entry,omitempty"`
	Node           *memory.Node           `json:"node,omitempty"`
	Profile        *[]memory.ProfileEntry `json:"profile,omitempty"`
	ProfileRemoved map[string]string      `json:"profile_removed,omitempty"`
	Purge          string                 `json:"purge,omitempty"`
	Batch          []record               `json:"batch,omitempty"`
}

type journalState struct {
	entries map[int]memory.Entry
	nodes   map[memory.NodeKey]memory.Node
	profile []memory.ProfileEntry
	removed map[string]string
}

func newJournalState() *journalState {
	return &journalState{
		entries: make(map[int]memory.Entry),
		nodes:   make(map[memory.NodeKey]memory.Node),
		removed: make(map[string]string),
	}
}

type journalTx struct {
	state   *journalState
	records []record
}

var _ memory.Store = (*journalTx)(nil)
var _ memory.RemovalStore = (*journalTx)(nil)

func (tx *journalTx) AppendEntry(ctx context.Context, e memory.Entry) (memory.Entry, error) {
	if err := ctx.Err(); err != nil {
		return memory.Entry{}, err
	}
	nextID := 0
	for id := range tx.state.entries {
		if id >= nextID {
			nextID = id + 1
		}
	}
	e.ID = nextID
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	tx.state.entries[e.ID] = e
	entryCopy := e
	tx.records = append(tx.records, record{Entry: &entryCopy})
	return e, nil
}

func (tx *journalTx) PutEntry(ctx context.Context, e memory.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.ID < 0 {
		return fmt.Errorf("memory: invalid entry id %d", e.ID)
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if existing, ok := tx.state.entries[e.ID]; ok && entriesEqual(existing, e) {
		return nil
	}
	tx.state.entries[e.ID] = e
	entryCopy := e
	tx.records = append(tx.records, record{Entry: &entryCopy})
	return nil
}

func (tx *journalTx) GetEntry(ctx context.Context, id int) (memory.Entry, error) {
	if err := ctx.Err(); err != nil {
		return memory.Entry{}, err
	}
	e, ok := tx.state.entries[id]
	if !ok {
		return memory.Entry{}, memory.ErrNotFound
	}
	return e, nil
}

func (tx *journalTx) ListEntries(ctx context.Context, fromID, limit int) ([]memory.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return listEntries(tx.state.entries, fromID, limit), nil
}

func (tx *journalTx) EntryCount(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return len(tx.state.entries), nil
}

func (tx *journalTx) PutNode(ctx context.Context, n memory.Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n.Level < 0 || n.Index < 0 {
		return fmt.Errorf("memory: invalid node key (%d, %d)", n.Level, n.Index)
	}
	if n.TS.IsZero() {
		n.TS = time.Now().UTC()
	}
	key := n.Key()
	if existing, ok := tx.state.nodes[key]; ok && nodesEqual(existing, n) {
		return nil
	}
	tx.state.nodes[key] = n
	nodeCopy := n
	tx.records = append(tx.records, record{Node: &nodeCopy})
	return nil
}

func (tx *journalTx) GetNode(ctx context.Context, key memory.NodeKey) (memory.Node, bool, error) {
	if err := ctx.Err(); err != nil {
		return memory.Node{}, false, err
	}
	n, ok := tx.state.nodes[key]
	return n, ok, nil
}

func (tx *journalTx) ListNodes(ctx context.Context) ([]memory.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return listNodes(tx.state.nodes), nil
}

func (tx *journalTx) GetProfile(ctx context.Context) ([]memory.ProfileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cloneProfile(tx.state.profile), nil
}

func (tx *journalTx) SaveProfile(ctx context.Context, entries []memory.ProfileEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if profilesEqual(tx.state.profile, entries) {
		return nil
	}

	nextIDs := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.ID != "" {
			nextIDs[e.ID] = struct{}{}
		}
	}

	var addedRemoved map[string]string
	for _, old := range tx.state.profile {
		if old.ID == "" {
			continue
		}
		if _, kept := nextIDs[old.ID]; !kept {
			raw, err := json.Marshal(old)
			if err != nil {
				return err
			}
			key := "p/" + old.ID
			digest := jsonwire.Digest(raw)
			if tx.state.removed == nil {
				tx.state.removed = make(map[string]string)
			}
			tx.state.removed[key] = digest
			if addedRemoved == nil {
				addedRemoved = make(map[string]string)
			}
			addedRemoved[key] = digest
		}
	}
	for id := range nextIDs {
		delete(tx.state.removed, "p/"+id)
	}

	next := make([]memory.ProfileEntry, len(entries))
	copy(next, entries)
	tx.state.profile = next
	tx.records = append(tx.records, record{
		Profile:        &next,
		ProfileRemoved: addedRemoved,
	})
	return nil
}

func (tx *journalTx) Removed(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cloneRemoved(tx.state.removed), nil
}

func (tx *journalTx) Purge(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return nil
	}
	if _, ok := tx.state.removed[key]; !ok {
		return nil
	}
	delete(tx.state.removed, key)
	tx.records = append(tx.records, record{Purge: key})
	return nil
}

func entriesEqual(a, b memory.Entry) bool {
	return a.ID == b.ID &&
		a.Kind == b.Kind &&
		a.Text == b.Text &&
		a.Peer == b.Peer &&
		a.Session == b.Session &&
		a.TS.Equal(b.TS)
}

func nodesEqual(a, b memory.Node) bool {
	return a.Level == b.Level &&
		a.Index == b.Index &&
		a.Text == b.Text &&
		a.TS.Equal(b.TS)
}

func profilesEqual(a, b []memory.ProfileEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Content != b[i].Content || !a[i].TS.Equal(b[i].TS) {
			return false
		}
	}
	return true
}

func listEntries(entries map[int]memory.Entry, fromID, limit int) []memory.Entry {
	out := make([]memory.Entry, 0, len(entries))
	for _, e := range entries {
		if e.ID >= fromID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func listNodes(nodes map[memory.NodeKey]memory.Node) []memory.Node {
	out := make([]memory.Node, 0, len(nodes))
	for _, n := range nodes {
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

func cloneProfile(p []memory.ProfileEntry) []memory.ProfileEntry {
	if len(p) == 0 {
		return nil
	}
	out := make([]memory.ProfileEntry, len(p))
	copy(out, p)
	return out
}

func cloneRemoved(m map[string]string) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func applyRecord(state *journalState, rec record) {
	records := rec.Batch
	if len(records) == 0 {
		records = []record{rec}
	}
	for _, cur := range records {
		if len(cur.Batch) > 0 {
			applyRecord(state, cur)
			continue
		}
		if cur.Entry != nil {
			state.entries[cur.Entry.ID] = *cur.Entry
		}
		if cur.Node != nil {
			state.nodes[cur.Node.Key()] = *cur.Node
		}
		if len(cur.ProfileRemoved) > 0 {
			for k, v := range cur.ProfileRemoved {
				state.removed[k] = v
			}
		}
		if cur.Profile != nil {
			next := make([]memory.ProfileEntry, len(*cur.Profile))
			copy(next, *cur.Profile)
			for _, p := range next {
				if p.ID != "" {
					delete(state.removed, "p/"+p.ID)
				}
			}
			state.profile = next
		}
		if cur.Purge != "" {
			delete(state.removed, cur.Purge)
		}
	}
}

func readJournalState(r io.Reader) (*journalState, error) {
	state := newJournalState()
	reader := bufio.NewReader(r)
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var rec record
			if json.Unmarshal(line, &rec) == nil {
				applyRecord(state, rec)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return state, nil
}

// ParseJournal parses an append-only JSONL memory journal into its live state.
// Unlike runtime reads, ParseJournal returns an error on malformed lines.
func ParseJournal(data []byte) (entries []memory.Entry, nodes []memory.Node, profile []memory.ProfileEntry, removed map[string]string, err error) {
	state := newJournalState()
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("filestore: invalid journal record at line %d: %w", i+1, err)
		}
		applyRecord(state, rec)
	}
	return listEntries(state.entries, 0, -1), listNodes(state.nodes), cloneProfile(state.profile), cloneRemoved(state.removed), nil
}

func journalLine(records []record) ([]byte, error) {
	rec := records[0]
	if len(records) > 1 {
		rec = record{Batch: records}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

func compacted(raw []byte) ([]byte, error) {
	state, err := readJournalState(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, e := range listEntries(state.entries, 0, -1) {
		entryCopy := e
		line, err := journalLine([]record{{Entry: &entryCopy}})
		if err != nil {
			return nil, err
		}
		out = append(out, line...)
	}
	for _, n := range listNodes(state.nodes) {
		nodeCopy := n
		line, err := journalLine([]record{{Node: &nodeCopy}})
		if err != nil {
			return nil, err
		}
		out = append(out, line...)
	}
	if len(state.profile) > 0 || len(state.removed) > 0 {
		rec := record{}
		if len(state.profile) > 0 {
			p := cloneProfile(state.profile)
			rec.Profile = &p
		}
		if len(state.removed) > 0 {
			rec.ProfileRemoved = cloneRemoved(state.removed)
		}
		line, err := journalLine([]record{rec})
		if err != nil {
			return nil, err
		}
		out = append(out, line...)
	}
	if len(out)*4 > len(raw) {
		return nil, nil
	}
	return out, nil
}

// FileStore is an append-only JSONL file-backed memory store coordinated with flock.
type FileStore struct {
	path     string
	lockPath string
}

var _ memory.TransactionalStore = (*FileStore)(nil)
var _ memory.RemovalStore = (*FileStore)(nil)

// NewFileStore creates a FileStore rooted at dir, storing records in <dir>/memory.jsonl.
func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("memory: directory is required")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "memory.jsonl")
	return &FileStore{path: path, lockPath: path + ".lock"}, nil
}

func (s *FileStore) locked(ctx context.Context, shared bool, fn func() error) error {
	lock := flock.New(s.lockPath)
	try := lock.TryLockContext
	if shared {
		try = lock.TryRLockContext
	}
	ok, err := try(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return ctx.Err()
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func (s *FileStore) readStateLocked(ctx context.Context) (*journalState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return newJournalState(), nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readJournalState(f)
}

func (s *FileStore) readState(ctx context.Context) (state *journalState, err error) {
	err = s.locked(ctx, true, func() error {
		state, err = s.readStateLocked(ctx)
		return err
	})
	return state, err
}

func (s *FileStore) appendRecordsLocked(ctx context.Context, records []record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	line, err := journalLine(records)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(line); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	info, statErr := os.Stat(s.path)
	if statErr != nil || info.Size() < compactAt {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	live, err := compacted(raw)
	if live == nil || err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".memory-*")
	if err != nil {
		return err
	}
	if _, err = temp.Write(live); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), s.path)
	}
	if err != nil {
		_ = os.Remove(temp.Name())
	}
	return err
}

func (s *FileStore) Transact(ctx context.Context, fn func(memory.Store) error) error {
	if fn == nil {
		return errors.New("memory: transaction callback is required")
	}
	return s.locked(ctx, false, func() error {
		state, err := s.readStateLocked(ctx)
		if err != nil {
			return err
		}
		tx := &journalTx{state: state}
		if err := fn(tx); err != nil {
			return err
		}
		if len(tx.records) == 0 {
			return nil
		}
		return s.appendRecordsLocked(ctx, tx.records)
	})
}

func (s *FileStore) AppendEntry(ctx context.Context, e memory.Entry) (out memory.Entry, err error) {
	err = s.Transact(ctx, func(tx memory.Store) error {
		out, err = tx.AppendEntry(ctx, e)
		return err
	})
	return out, err
}

func (s *FileStore) PutEntry(ctx context.Context, e memory.Entry) error {
	return s.Transact(ctx, func(tx memory.Store) error {
		return tx.PutEntry(ctx, e)
	})
}

func (s *FileStore) GetEntry(ctx context.Context, id int) (memory.Entry, error) {
	state, err := s.readState(ctx)
	if err != nil {
		return memory.Entry{}, err
	}
	e, ok := state.entries[id]
	if !ok {
		return memory.Entry{}, memory.ErrNotFound
	}
	return e, nil
}

func (s *FileStore) ListEntries(ctx context.Context, fromID, limit int) ([]memory.Entry, error) {
	state, err := s.readState(ctx)
	if err != nil {
		return nil, err
	}
	return listEntries(state.entries, fromID, limit), nil
}

func (s *FileStore) EntryCount(ctx context.Context) (int, error) {
	state, err := s.readState(ctx)
	if err != nil {
		return 0, err
	}
	return len(state.entries), nil
}

func (s *FileStore) PutNode(ctx context.Context, n memory.Node) error {
	return s.Transact(ctx, func(tx memory.Store) error {
		return tx.PutNode(ctx, n)
	})
}

func (s *FileStore) GetNode(ctx context.Context, key memory.NodeKey) (memory.Node, bool, error) {
	state, err := s.readState(ctx)
	if err != nil {
		return memory.Node{}, false, err
	}
	n, ok := state.nodes[key]
	return n, ok, nil
}

func (s *FileStore) ListNodes(ctx context.Context) ([]memory.Node, error) {
	state, err := s.readState(ctx)
	if err != nil {
		return nil, err
	}
	return listNodes(state.nodes), nil
}

func (s *FileStore) GetProfile(ctx context.Context) ([]memory.ProfileEntry, error) {
	state, err := s.readState(ctx)
	if err != nil {
		return nil, err
	}
	return cloneProfile(state.profile), nil
}

func (s *FileStore) SaveProfile(ctx context.Context, entries []memory.ProfileEntry) error {
	return s.Transact(ctx, func(tx memory.Store) error {
		return tx.SaveProfile(ctx, entries)
	})
}

func (s *FileStore) Removed(ctx context.Context) (map[string]string, error) {
	state, err := s.readState(ctx)
	if err != nil {
		return nil, err
	}
	return cloneRemoved(state.removed), nil
}

func (s *FileStore) Purge(ctx context.Context, key string) error {
	return s.Transact(ctx, func(tx memory.Store) error {
		return tx.(memory.RemovalStore).Purge(ctx, key)
	})
}

// Journal is the same append-only JSONL journal stored in a single document.Document,
// for hosts whose storage is documents (e.g. Cloudflare Workers, browsers, tests).
type Journal struct {
	Document document.Document
}

var _ memory.TransactionalStore = Journal{}
var _ memory.RemovalStore = Journal{}

func (j Journal) readState(ctx context.Context) (*journalState, error) {
	raw, err := j.Document.Read(ctx)
	if err != nil {
		return nil, err
	}
	return readJournalState(bytes.NewReader(raw))
}

func (j Journal) Transact(ctx context.Context, fn func(memory.Store) error) error {
	if fn == nil {
		return errors.New("memory: transaction callback is required")
	}
	return j.Document.Update(ctx, func(raw []byte) ([]byte, error) {
		state, err := readJournalState(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		tx := &journalTx{state: state}
		if err := fn(tx); err != nil || len(tx.records) == 0 {
			return raw, err
		}
		line, err := journalLine(tx.records)
		if err != nil {
			return nil, err
		}
		next := make([]byte, len(raw)+len(line))
		copy(next, raw)
		copy(next[len(raw):], line)
		if len(next) < compactAt {
			return next, nil
		}
		live, err := compacted(next)
		if live == nil || err != nil {
			return next, err
		}
		return live, nil
	})
}

func (j Journal) AppendEntry(ctx context.Context, e memory.Entry) (out memory.Entry, err error) {
	err = j.Transact(ctx, func(tx memory.Store) error {
		out, err = tx.AppendEntry(ctx, e)
		return err
	})
	return out, err
}

func (j Journal) PutEntry(ctx context.Context, e memory.Entry) error {
	return j.Transact(ctx, func(tx memory.Store) error {
		return tx.PutEntry(ctx, e)
	})
}

func (j Journal) GetEntry(ctx context.Context, id int) (memory.Entry, error) {
	state, err := j.readState(ctx)
	if err != nil {
		return memory.Entry{}, err
	}
	e, ok := state.entries[id]
	if !ok {
		return memory.Entry{}, memory.ErrNotFound
	}
	return e, nil
}

func (j Journal) ListEntries(ctx context.Context, fromID, limit int) ([]memory.Entry, error) {
	state, err := j.readState(ctx)
	if err != nil {
		return nil, err
	}
	return listEntries(state.entries, fromID, limit), nil
}

func (j Journal) EntryCount(ctx context.Context) (int, error) {
	state, err := j.readState(ctx)
	if err != nil {
		return 0, err
	}
	return len(state.entries), nil
}

func (j Journal) PutNode(ctx context.Context, n memory.Node) error {
	return j.Transact(ctx, func(tx memory.Store) error {
		return tx.PutNode(ctx, n)
	})
}

func (j Journal) GetNode(ctx context.Context, key memory.NodeKey) (memory.Node, bool, error) {
	state, err := j.readState(ctx)
	if err != nil {
		return memory.Node{}, false, err
	}
	n, ok := state.nodes[key]
	return n, ok, nil
}

func (j Journal) ListNodes(ctx context.Context) ([]memory.Node, error) {
	state, err := j.readState(ctx)
	if err != nil {
		return nil, err
	}
	return listNodes(state.nodes), nil
}

func (j Journal) GetProfile(ctx context.Context) ([]memory.ProfileEntry, error) {
	state, err := j.readState(ctx)
	if err != nil {
		return nil, err
	}
	return cloneProfile(state.profile), nil
}

func (j Journal) SaveProfile(ctx context.Context, entries []memory.ProfileEntry) error {
	return j.Transact(ctx, func(tx memory.Store) error {
		return tx.SaveProfile(ctx, entries)
	})
}

func (j Journal) Removed(ctx context.Context) (map[string]string, error) {
	state, err := j.readState(ctx)
	if err != nil {
		return nil, err
	}
	return cloneRemoved(state.removed), nil
}

func (j Journal) Purge(ctx context.Context, key string) error {
	return j.Transact(ctx, func(tx memory.Store) error {
		return tx.(memory.RemovalStore).Purge(ctx, key)
	})
}
