package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Shared adapts a Store to the Mitosis sync class contract (mitosis.Class).
// Keys use three collision-free namespaces:
//   - "ef/<fp>"                  for immutable verbatim entries (keyed by content fingerprint, without local ID)
//   - "nf/<l>/<startFp>/<endFp>" for immutable summary nodes anchored by boundary entry fingerprints
//   - "p/<id>"                   for curated ProfileEntry records (with deletion tombstones)
type Shared struct {
	Store Store
}

// wireEntry is the ID-free canonical representation of an Entry on the sync wire.
// Omitting local sequential ID guarantees identical bytes across all peers and
// prevents silent overwrites when multiple peers append concurrently offline.
type wireEntry struct {
	Kind    EntryKind `json:"kind"`
	Text    string    `json:"text"`
	Peer    string    `json:"peer,omitempty"`
	Session string    `json:"session,omitempty"`
	TS      time.Time `json:"ts"`
}

type wireNode struct {
	Level   int       `json:"l"`
	StartFP string    `json:"start_fp"`
	EndFP   string    `json:"end_fp"`
	Text    string    `json:"text"`
	TS      time.Time `json:"ts,omitempty"`
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
	fpByID := make(map[int]string, len(entries))
	for _, e := range entries {
		we := toWireEntry(e)
		fp := entryFP(we)
		fpByID[e.ID] = fp
		raw, err := json.Marshal(we)
		if err != nil {
			return nil, err
		}
		out[entrySyncKey(fp)] = raw
	}

	nodes, err := store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if strings.TrimSpace(n.Text) == "" {
			continue
		}
		k := n.Key()
		startFP, okStart := fpByID[k.Start()]
		endFP, okEnd := fpByID[k.End()-1]
		if !okStart || !okEnd {
			continue
		}
		wn := wireNode{
			Level:   n.Level,
			StartFP: startFP,
			EndFP:   endFP,
			Text:    n.Text,
			TS:      n.TS.UTC(),
		}
		raw, err := json.Marshal(wn)
		if err != nil {
			return nil, err
		}
		out[nodeSyncKey(n.Level, startFP, endFP)] = raw
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

func toWireEntry(e Entry) wireEntry {
	return wireEntry{
		Kind:    e.Kind,
		Text:    e.Text,
		Peer:    e.Peer,
		Session: e.Session,
		TS:      e.TS.UTC(),
	}
}

func entryFP(we wireEntry) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s", we.TS.UTC().UnixNano(), we.Kind, we.Peer, we.Session, we.Text)
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func entrySyncKey(fp string) string { return "ef/" + fp }
func nodeSyncKey(level int, start, end string) string {
	return fmt.Sprintf("nf/%d/%s/%s", level, start, end)
}
func profileSyncKey(id string) string { return "p/" + id }

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
		seenFP := make(map[string]int, len(entries)) // fp -> local entry ID
		fpByID := make(map[int]string, len(entries))
		for _, e := range entries {
			fp := entryFP(toWireEntry(e))
			seenFP[fp] = e.ID
			fpByID[e.ID] = fp
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

		// Collect all incoming entries first so unseen entries are appended in
		// deterministic (TS, fp) order before aligning any incoming summary nodes.
		type pendingEntry struct {
			fp string
			we wireEntry
		}
		var newEntries []pendingEntry
		queuedEntryFP := make(map[string]struct{})

		type pendingNode struct {
			key string
			wn  wireNode
		}
		var pendingNodes []pendingNode

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
				case strings.HasPrefix(k, "ef/"):
					fpKey := strings.TrimPrefix(k, "ef/")
					var we wireEntry
					if json.Unmarshal(raw, &we) != nil {
						continue
					}
					fp := entryFP(we)
					if fp != fpKey {
						continue
					}
					if _, exists := seenFP[fp]; exists {
						continue
					}
					if _, queued := queuedEntryFP[fp]; queued {
						continue
					}
					queuedEntryFP[fp] = struct{}{}
					newEntries = append(newEntries, pendingEntry{fp: fp, we: we})

				case strings.HasPrefix(k, "nf/"):
					parts := strings.Split(strings.TrimPrefix(k, "nf/"), "/")
					if len(parts) != 3 {
						continue
					}
					level, err := strconv.Atoi(parts[0])
					if err != nil || level < 0 {
						continue
					}
					var wn wireNode
					if json.Unmarshal(raw, &wn) != nil || wn.Level != level || wn.StartFP != parts[1] || wn.EndFP != parts[2] {
						continue
					}
					if strings.TrimSpace(wn.Text) == "" {
						continue
					}
					pendingNodes = append(pendingNodes, pendingNode{key: k, wn: wn})

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

		// Append unseen entries in deterministic timestamp + fingerprint order.
		sort.Slice(newEntries, func(i, j int) bool {
			if !newEntries[i].we.TS.Equal(newEntries[j].we.TS) {
				return newEntries[i].we.TS.Before(newEntries[j].we.TS)
			}
			return newEntries[i].fp < newEntries[j].fp
		})
		for _, pe := range newEntries {
			stored, err := store.AppendEntry(ctx, Entry{
				Kind:    pe.we.Kind,
				Text:    pe.we.Text,
				Peer:    pe.we.Peer,
				Session: pe.we.Session,
				TS:      pe.we.TS,
			})
			if err != nil {
				return err
			}
			seenFP[pe.fp] = stored.ID
			fpByID[stored.ID] = pe.fp
			changed = append(changed, entrySyncKey(pe.fp))
		}

		// Apply summary nodes whose boundary fingerprints align with a local power-of-2 node range.
		for _, pn := range pendingNodes {
			startID, okStart := seenFP[pn.wn.StartFP]
			endID, okEnd := seenFP[pn.wn.EndFP]
			if !okStart || !okEnd {
				continue
			}
			span := 1 << pn.wn.Level
			if span <= 0 || startID%span != 0 || endID != startID+span-1 {
				continue
			}
			idx := startID / span
			n := Node{
				Level: pn.wn.Level,
				Index: idx,
				Text:  pn.wn.Text,
				TS:    pn.wn.TS,
			}
			canon, err := json.Marshal(n)
			if err != nil {
				return err
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
			changed = append(changed, pn.key)
		}

		if !keepOwn && len(removed) > 0 {
			rs, hasRemoval := store.(RemovalStore)
			removedSet := make(map[string]struct{}, len(removed))
			for _, k := range removed {
				if !strings.HasPrefix(k, "p/") {
					continue
				}
				id := strings.TrimPrefix(k, "p/")
				if _, existed := profileRaw[id]; existed {
					removedSet[id] = struct{}{}
					changed = append(changed, k)
				}
				if hasRemoval {
					if err := rs.Purge(ctx, k); err != nil {
						return err
					}
				}
			}
			if len(removedSet) > 0 {
				kept := make([]ProfileEntry, 0, len(profile))
				for _, p := range profile {
					if _, drop := removedSet[p.ID]; !drop {
						kept = append(kept, p)
					}
				}
				profile = kept
				profileDirty = true
			}
		}

		if profileDirty {
			if err := store.SaveProfile(ctx, profile); err != nil {
				return err
			}
		}
		return nil
	}

	if ts, ok := s.Store.(TransactionalStore); ok {
		return changed, ts.Transact(ctx, merge)
	}
	return changed, merge(s.Store)
}
