package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Shared adapts a Store to the Mitosis sync class contract (mitosis.Class).
// Keys use three namespaces:
//   - "e/<id>"       for verbatim Entry records
//   - "n/<l>/<i>"    for immutable summary Node records
//   - "p/<id>"       for ProfileEntry records
type Shared struct {
	Store Store
}

// Shareable emits all entries, summary nodes, and profile entries keyed by namespace.
func (s Shared) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	return shareable(ctx, s.Store)
}

// Removed delegates to RemovalStore for deleted profile tombstones if supported.
func (s Shared) Removed(ctx context.Context) (map[string]string, error) {
	rs, ok := s.Store.(RemovalStore)
	if !ok {
		return nil, nil
	}
	return rs.Removed(ctx)
}

func shareable(ctx context.Context, store Store) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage)

	entries, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		out[entrySyncKey(e.ID)] = raw
	}

	nodes, err := store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		raw, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		out[nodeSyncKey(n.Level, n.Index)] = raw
	}

	profile, err := store.GetProfile(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range profile {
		if p.ID == "" {
			continue
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		out[profileSyncKey(p.ID)] = raw
	}

	return out, nil
}

func entrySyncKey(id int) string          { return "e/" + strconv.Itoa(id) }
func nodeSyncKey(level, index int) string { return fmt.Sprintf("n/%d/%d", level, index) }
func profileSyncKey(id string) string     { return "p/" + id }

type entryFingerprint struct {
	tsNano  int64
	kind    EntryKind
	peer    string
	session string
	text    string
}

func fingerprintEntry(e Entry) entryFingerprint {
	return entryFingerprint{
		tsNano:  e.TS.UTC().UnixNano(),
		kind:    e.Kind,
		peer:    e.Peer,
		session: e.Session,
		text:    e.Text,
	}
}

// MergeShared folds incoming peer records into the store idempotently.
// It performs zero writes and returns nil when there are no actual mutations.
func (s Shared) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	hasIncoming := false
	for _, m := range incoming {
		if len(m) > 0 {
			hasIncoming = true
			break
		}
	}
	if !hasIncoming && len(removed) == 0 {
		return nil, nil
	}

	var changed []string
	merge := func(store Store) error {
		changed = nil

		entries, err := store.ListEntries(ctx, 0, -1)
		if err != nil {
			return err
		}
		entryRaw := make(map[int]json.RawMessage, len(entries))
		seenFP := make(map[entryFingerprint]struct{}, len(entries))
		for _, e := range entries {
			if raw, err := json.Marshal(e); err == nil {
				entryRaw[e.ID] = raw
			}
			seenFP[fingerprintEntry(e)] = struct{}{}
		}

		nodes, err := store.ListNodes(ctx)
		if err != nil {
			return err
		}
		nodeRaw := make(map[NodeKey]json.RawMessage, len(nodes))
		for _, n := range nodes {
			if raw, err := json.Marshal(n); err == nil {
				nodeRaw[n.Key()] = raw
			}
		}

		profile, err := store.GetProfile(ctx)
		if err != nil {
			return err
		}
		profileIdx := make(map[string]int, len(profile))
		profileRaw := make(map[string]json.RawMessage, len(profile))
		for i, p := range profile {
			profileIdx[p.ID] = i
			if raw, err := json.Marshal(p); err == nil {
				profileRaw[p.ID] = raw
			}
		}
		profileDirty := false

		// Collect deterministic sorted keys per incoming map.
		for _, batch := range incoming {
			if len(batch) == 0 {
				continue
			}
			keys := make([]string, 0, len(batch))
			for k := range batch {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				raw := batch[k]
				switch {
				case strings.HasPrefix(k, "e/"):
					idStr := strings.TrimPrefix(k, "e/")
					keyID, err := strconv.Atoi(idStr)
					if err != nil || keyID < 0 {
						continue
					}
					var e Entry
					if json.Unmarshal(raw, &e) != nil || e.ID != keyID {
						continue
					}
					canon, err := json.Marshal(e)
					if err != nil {
						continue
					}
					if keepOwn {
						if old, ok := entryRaw[e.ID]; ok && bytes.Equal(old, canon) {
							continue
						}
						fp := fingerprintEntry(e)
						if _, dup := seenFP[fp]; dup {
							continue
						}
						// Owner assigns next canonical sequential ID so entries stay contiguous.
						stored, err := store.AppendEntry(ctx, e)
						if err != nil {
							return err
						}
						if storedRaw, err := json.Marshal(stored); err == nil {
							entryRaw[stored.ID] = storedRaw
						}
						seenFP[fp] = struct{}{}
						changed = append(changed, entrySyncKey(stored.ID))
					} else {
						if old, ok := entryRaw[e.ID]; ok && bytes.Equal(old, canon) {
							continue
						}
						if err := store.PutEntry(ctx, e); err != nil {
							return err
						}
						entryRaw[e.ID] = canon
						seenFP[fingerprintEntry(e)] = struct{}{}
						changed = append(changed, entrySyncKey(e.ID))
					}

				case strings.HasPrefix(k, "n/"):
					parts := strings.Split(strings.TrimPrefix(k, "n/"), "/")
					if len(parts) != 2 {
						continue
					}
					level, err1 := strconv.Atoi(parts[0])
					idx, err2 := strconv.Atoi(parts[1])
					if err1 != nil || err2 != nil || level < 0 || idx < 0 {
						continue
					}
					var n Node
					if json.Unmarshal(raw, &n) != nil || n.Level != level || n.Index != idx {
						continue
					}
					canon, err := json.Marshal(n)
					if err != nil {
						continue
					}
					nk := n.Key()
					if old, ok := nodeRaw[nk]; ok {
						if keepOwn || bytes.Equal(old, canon) {
							continue
						}
					}
					if err := store.PutNode(ctx, n); err != nil {
						return err
					}
					nodeRaw[nk] = canon
					changed = append(changed, nodeSyncKey(n.Level, n.Index))

				case strings.HasPrefix(k, "p/"):
					id := strings.TrimPrefix(k, "p/")
					if id == "" || len(id) > 128 {
						continue
					}
					var p ProfileEntry
					if json.Unmarshal(raw, &p) != nil || p.ID != id {
						continue
					}
					canon, err := json.Marshal(p)
					if err != nil {
						continue
					}
					if old, ok := profileRaw[id]; ok {
						if keepOwn || bytes.Equal(old, canon) {
							continue
						}
						profile[profileIdx[id]] = p
					} else {
						profileIdx[id] = len(profile)
						profile = append(profile, p)
					}
					profileRaw[id] = canon
					profileDirty = true
					changed = append(changed, profileSyncKey(id))
				}
			}
		}

		if !keepOwn && len(removed) > 0 {
			rs, hasRemoval := store.(RemovalStore)
			for _, remKey := range removed {
				if !strings.HasPrefix(remKey, "p/") {
					continue
				}
				id := strings.TrimPrefix(remKey, "p/")
				if hasRemoval {
					if err := rs.Purge(ctx, remKey); err != nil {
						return err
					}
				}
				if _, existed := profileRaw[id]; existed {
					var removedOK bool
					profile, removedOK = ProfileRemove(profile, id)
					if removedOK {
						delete(profileRaw, id)
						// Rebuild index map after removal.
						profileIdx = make(map[string]int, len(profile))
						for i, p := range profile {
							profileIdx[p.ID] = i
						}
						profileDirty = true
						changed = append(changed, remKey)
					}
				}
			}
		}

		if profileDirty {
			if err := store.SaveProfile(ctx, profile); err != nil {
				return err
			}
		}
		return nil
	}

	if transactional, ok := s.Store.(TransactionalStore); ok {
		return changed, transactional.Transact(ctx, merge)
	}
	return changed, merge(s.Store)
}
