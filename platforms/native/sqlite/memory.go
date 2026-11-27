package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/plugins/memory"
)

type sqlConnection interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Memory struct {
	db         *DB
	connection sqlConnection
	namespace  string
}

func (db *DB) Memory(namespace string) *Memory { return &Memory{db, autocommit{db}, namespace} }

var (
	_ memory.Store              = (*Memory)(nil)
	_ memory.TransactionalStore = (*Memory)(nil)
	_ memory.RemovalStore       = (*Memory)(nil)
)

const (
	memoryRemovedKey = "memory/removed.json"
	memoryTimeFormat = "2006-01-02T15:04:05.000000000Z"
)

func (store *Memory) inTx() bool {
	_, auto := store.connection.(autocommit)
	return !auto
}

func formatMemoryTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(memoryTimeFormat)
}

func parseMemoryTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func (store *Memory) loadRemoved(ctx context.Context) (map[string]string, error) {
	var raw []byte
	err := store.connection.QueryRowContext(ctx, "SELECT content FROM documents WHERE namespace=? AND key=?", store.namespace, memoryRemovedKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || len(raw) == 0 {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

func (store *Memory) saveRemoved(ctx context.Context, removed map[string]string) error {
	if len(removed) == 0 {
		_, err := store.connection.ExecContext(ctx, "DELETE FROM documents WHERE namespace=? AND key=?", store.namespace, memoryRemovedKey)
		return err
	}
	data, err := json.Marshal(removed)
	if err != nil {
		return err
	}
	_, err = store.connection.ExecContext(ctx, "INSERT OR REPLACE INTO documents VALUES(?,?,?)", store.namespace, memoryRemovedKey, data)
	return err
}

func (store *Memory) AppendEntry(ctx context.Context, e memory.Entry) (memory.Entry, error) {
	if !store.inTx() {
		var out memory.Entry
		err := store.Transact(ctx, func(tx memory.Store) error {
			var err error
			out, err = tx.AppendEntry(ctx, e)
			return err
		})
		return out, err
	}
	var nextID int
	if err := store.connection.QueryRowContext(ctx, "SELECT coalesce(max(id)+1, 0) FROM memory_entries WHERE namespace=?", store.namespace).Scan(&nextID); err != nil {
		return memory.Entry{}, err
	}
	e.ID = nextID
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if _, err := store.connection.ExecContext(
		ctx,
		"INSERT INTO memory_entries(namespace, id, kind, text, peer, session, ts) VALUES(?,?,?,?,?,?,?)",
		store.namespace, e.ID, string(e.Kind), e.Text, e.Peer, e.Session, formatMemoryTime(e.TS),
	); err != nil {
		return memory.Entry{}, err
	}
	return e, nil
}

func (store *Memory) PutEntry(ctx context.Context, e memory.Entry) error {
	if e.ID < 0 {
		return memory.ErrInvalidRef
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	existing, err := store.GetEntry(ctx, e.ID)
	if err == nil {
		if existing.Kind == e.Kind &&
			existing.Text == e.Text &&
			existing.Peer == e.Peer &&
			existing.Session == e.Session &&
			existing.TS.UTC().Equal(e.TS.UTC()) {
			return nil
		}
	} else if !errors.Is(err, memory.ErrNotFound) {
		return err
	}
	if !store.inTx() {
		return store.Transact(ctx, func(tx memory.Store) error {
			return tx.PutEntry(ctx, e)
		})
	}
	_, err = store.connection.ExecContext(
		ctx,
		"INSERT OR REPLACE INTO memory_entries(namespace, id, kind, text, peer, session, ts) VALUES(?,?,?,?,?,?,?)",
		store.namespace, e.ID, string(e.Kind), e.Text, e.Peer, e.Session, formatMemoryTime(e.TS),
	)
	return err
}

func (store *Memory) GetEntry(ctx context.Context, id int) (memory.Entry, error) {
	var e memory.Entry
	var kind, ts string
	err := store.connection.QueryRowContext(
		ctx,
		"SELECT id, kind, text, peer, session, ts FROM memory_entries WHERE namespace=? AND id=?",
		store.namespace, id,
	).Scan(&e.ID, &kind, &e.Text, &e.Peer, &e.Session, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Entry{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Entry{}, err
	}
	e.Kind = memory.EntryKind(kind)
	e.TS, err = parseMemoryTime(ts)
	if err != nil {
		return memory.Entry{}, err
	}
	return e, nil
}

func (store *Memory) ListEntries(ctx context.Context, fromID, limit int) ([]memory.Entry, error) {
	if fromID < 0 {
		fromID = 0
	}
	query := "SELECT id, kind, text, peer, session, ts FROM memory_entries WHERE namespace=? AND id>=? ORDER BY id ASC"
	args := []any{store.namespace, fromID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := store.connection.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []memory.Entry{}
	for rows.Next() {
		var e memory.Entry
		var kind, ts string
		if err = rows.Scan(&e.ID, &kind, &e.Text, &e.Peer, &e.Session, &ts); err != nil {
			return nil, err
		}
		e.Kind = memory.EntryKind(kind)
		if e.TS, err = parseMemoryTime(ts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (store *Memory) EntryCount(ctx context.Context) (int, error) {
	var count int
	err := store.connection.QueryRowContext(ctx, "SELECT coalesce(max(id)+1, 0) FROM memory_entries WHERE namespace=?", store.namespace).Scan(&count)
	return count, err
}

func (store *Memory) PutNode(ctx context.Context, n memory.Node) error {
	existing, ok, err := store.GetNode(ctx, n.Key())
	if err != nil {
		return err
	}
	if ok && existing.Level == n.Level && existing.Index == n.Index && existing.Text == n.Text {
		return nil
	}
	if n.TS.IsZero() {
		n.TS = time.Now().UTC()
	}
	if !store.inTx() {
		return store.Transact(ctx, func(tx memory.Store) error {
			return tx.PutNode(ctx, n)
		})
	}
	_, err = store.connection.ExecContext(
		ctx,
		"INSERT OR REPLACE INTO memory_nodes(namespace, level, idx, text, ts) VALUES(?,?,?,?,?)",
		store.namespace, n.Level, n.Index, n.Text, formatMemoryTime(n.TS),
	)
	return err
}

func (store *Memory) GetNode(ctx context.Context, key memory.NodeKey) (memory.Node, bool, error) {
	var n memory.Node
	var ts string
	err := store.connection.QueryRowContext(
		ctx,
		"SELECT level, idx, text, ts FROM memory_nodes WHERE namespace=? AND level=? AND idx=?",
		store.namespace, key.Level, key.Index,
	).Scan(&n.Level, &n.Index, &n.Text, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Node{}, false, nil
	}
	if err != nil {
		return memory.Node{}, false, err
	}
	if n.TS, err = parseMemoryTime(ts); err != nil {
		return memory.Node{}, false, err
	}
	return n, true, nil
}

func (store *Memory) ListNodes(ctx context.Context) ([]memory.Node, error) {
	rows, err := store.connection.QueryContext(
		ctx,
		"SELECT level, idx, text, ts FROM memory_nodes WHERE namespace=? ORDER BY level ASC, idx ASC",
		store.namespace,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []memory.Node{}
	for rows.Next() {
		var n memory.Node
		var ts string
		if err = rows.Scan(&n.Level, &n.Index, &n.Text, &ts); err != nil {
			return nil, err
		}
		if n.TS, err = parseMemoryTime(ts); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (store *Memory) GetProfile(ctx context.Context) ([]memory.ProfileEntry, error) {
	rows, err := store.connection.QueryContext(
		ctx,
		"SELECT id, content, ts FROM memory_profile WHERE namespace=? ORDER BY ts ASC, id ASC",
		store.namespace,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []memory.ProfileEntry{}
	for rows.Next() {
		var p memory.ProfileEntry
		var ts string
		if err = rows.Scan(&p.ID, &p.Content, &ts); err != nil {
			return nil, err
		}
		if p.TS, err = parseMemoryTime(ts); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func profileEqual(a, b []memory.ProfileEntry) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]memory.ProfileEntry, len(a))
	for _, p := range a {
		byID[p.ID] = p
	}
	if len(byID) != len(a) {
		return false
	}
	for _, p := range b {
		old, ok := byID[p.ID]
		if !ok || old.Content != p.Content || !old.TS.UTC().Equal(p.TS.UTC()) {
			return false
		}
	}
	return true
}

func (store *Memory) SaveProfile(ctx context.Context, entries []memory.ProfileEntry) error {
	current, err := store.GetProfile(ctx)
	if err != nil {
		return err
	}
	if profileEqual(current, entries) {
		return nil
	}
	if !store.inTx() {
		return store.Transact(ctx, func(tx memory.Store) error {
			return tx.SaveProfile(ctx, entries)
		})
	}
	nextSet := make(map[string]struct{}, len(entries))
	removed, err := store.loadRemoved(ctx)
	if err != nil {
		return err
	}
	removedDirty := false
	for _, e := range entries {
		nextSet[e.ID] = struct{}{}
		key := "p/" + e.ID
		if _, had := removed[key]; had {
			delete(removed, key)
			removedDirty = true
		}
	}
	for _, old := range current {
		if _, kept := nextSet[old.ID]; !kept && old.ID != "" {
			raw, err := json.Marshal(old)
			if err != nil {
				return err
			}
			removed["p/"+old.ID] = jsonwire.Digest(raw)
			removedDirty = true
		}
	}
	if _, err := store.connection.ExecContext(ctx, "DELETE FROM memory_profile WHERE namespace=?", store.namespace); err != nil {
		return err
	}
	for _, e := range entries {
		if e.TS.IsZero() {
			e.TS = time.Now().UTC()
		}
		if _, err := store.connection.ExecContext(
			ctx,
			"INSERT OR REPLACE INTO memory_profile(namespace, id, content, ts) VALUES(?,?,?,?)",
			store.namespace, e.ID, e.Content, formatMemoryTime(e.TS),
		); err != nil {
			return err
		}
	}
	if removedDirty {
		return store.saveRemoved(ctx, removed)
	}
	return nil
}

func (store *Memory) Removed(ctx context.Context) (map[string]string, error) {
	removed, err := store.loadRemoved(ctx)
	if err != nil {
		return nil, err
	}
	if len(removed) == 0 {
		return nil, nil
	}
	live, err := store.GetProfile(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range live {
		delete(removed, "p/"+p.ID)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	return removed, nil
}

func (store *Memory) Purge(ctx context.Context, key string) error {
	if !store.inTx() {
		return store.Transact(ctx, func(tx memory.Store) error {
			return tx.(memory.RemovalStore).Purge(ctx, key)
		})
	}
	if strings.HasPrefix(key, "p/") {
		id := strings.TrimPrefix(key, "p/")
		if _, err := store.connection.ExecContext(ctx, "DELETE FROM memory_profile WHERE namespace=? AND id=?", store.namespace, id); err != nil {
			return err
		}
	}
	removed, err := store.loadRemoved(ctx)
	if err != nil {
		return err
	}
	if _, had := removed[key]; had {
		delete(removed, key)
		return store.saveRemoved(ctx, removed)
	}
	return nil
}

func (store *Memory) Transact(ctx context.Context, fn func(memory.Store) error) error {
	if fn == nil {
		return errors.New("memory transaction callback required")
	}
	if store.inTx() {
		return fn(store)
	}
	tx, err := store.db.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = fn(&Memory{store.db, tx, store.namespace}); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Memory) importJournal(ctx context.Context, data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var entries []memory.Entry
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e memory.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	return store.Transact(ctx, func(tx memory.Store) error {
		for _, e := range entries {
			if err := tx.PutEntry(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
}
