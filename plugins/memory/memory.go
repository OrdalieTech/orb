// Package memory defines orb's durable memory SDK seam. It is a
// orb-original addition with no upstream mirror and is importable standalone.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

type Item struct {
	ID      string            `json:"id"`
	Time    time.Time         `json:"time"`
	Content string            `json:"content"`
	Tags    []string          `json:"tags,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

type Filter struct {
	Tags     []string
	Since    time.Time
	Until    time.Time
	Contains string
	Limit    int
}

// Store is a tenant-scoped durable memory backend. Implementations must accept
// concurrent calls. Append stores an item under its ID, replacing any item with
// that ID, or under a new ID when it has none. Query returns newest items first,
// treats Tags as an AND filter, matches Contains case-insensitively, honors
// positive Limit values, and returns every match for a negative Limit.
type Store interface {
	Append(context.Context, Item) (string, error)
	Get(context.Context, string) (Item, error)
	Query(context.Context, Filter) ([]Item, error)
	Delete(context.Context, string) error
}

// TransactionalStore makes compound profile mutations atomic across runtime
// instances and processes. The callback must use only the supplied Store.
type TransactionalStore interface {
	Store
	Transact(context.Context, func(Store) error) error
}

// RemovalStore is an optional interface a Store may implement to persist digest tombstones for
// items deleted locally (for Mitosis sync) and drop those tombstones once the group applies them.
type RemovalStore interface {
	Removed(context.Context) (map[string]string, error)
	Purge(context.Context, string) error
}

// SemanticSearcher returns highest-scoring items first and honors positive
// limit values.
type SemanticSearcher interface {
	Search(context.Context, string, int) ([]Scored, error)
}

type Scored struct {
	Item
	Score float64
}

// Shared is a Store as a Mitosis sync class (mitosis.Class): its items by ID. An owner folding
// hand-ins takes only items it lacks; otherwise it takes the group's over its own and deletes those
// the group deleted.
type Shared struct{ Store Store }

func (s Shared) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	return shareable(ctx, s.Store)
}

func (s Shared) Removed(ctx context.Context) (map[string]string, error) {
	rs, ok := s.Store.(RemovalStore)
	if !ok {
		return nil, nil
	}
	return rs.Removed(ctx)
}

func shareable(ctx context.Context, store Store) (map[string]json.RawMessage, error) {
	items, err := store.Query(ctx, Filter{Limit: -1})
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(items))
	for _, item := range items {
		if out[item.ID], err = json.Marshal(item); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MergeShared folds in items a sync group shared, in one transaction when the store has them, and
// returns the IDs it changed. An entry that is not an item under its own ID is ignored.
func (s Shared) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	var changed []string
	merge := func(store Store) error {
		changed = nil
		have, err := shareable(ctx, store)
		if err != nil {
			return err
		}
		rs, hasRemoval := store.(RemovalStore)
		for _, entries := range incoming {
			for id, raw := range entries {
				var item Item
				if json.Unmarshal(raw, &item) != nil || item.ID != id || id == "" || len(id) > 128 {
					continue
				}
				next, _ := json.Marshal(item)
				if old, ok := have[id]; ok && (keepOwn || bytes.Equal(old, next)) {
					continue
				}
				if _, err = store.Append(ctx, item); err != nil {
					return err
				}
				have[id] = next
				changed = append(changed, id)
			}
		}
		if !keepOwn {
			for _, id := range removed {
				_, existed := have[id]
				if hasRemoval {
					if err = rs.Purge(ctx, id); err != nil {
						return err
					}
				} else if existed {
					if err = store.Delete(ctx, id); err != nil {
						return err
					}
				}
				if existed {
					delete(have, id)
					changed = append(changed, id)
				}
			}
		}
		return nil
	}
	if transactional, ok := s.Store.(TransactionalStore); ok {
		return changed, transactional.Transact(ctx, merge)
	}
	return changed, merge(s.Store)
}
