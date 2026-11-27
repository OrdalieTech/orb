package filestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/plugins/memory"
)

type countingDocument struct {
	inner  document.Memory
	writes atomic.Int64
}

func (d *countingDocument) Read(ctx context.Context) ([]byte, error) {
	return d.inner.Read(ctx)
}

func (d *countingDocument) Update(ctx context.Context, fn func([]byte) ([]byte, error)) error {
	return d.inner.Update(ctx, func(raw []byte) ([]byte, error) {
		before := append([]byte(nil), raw...)
		next, err := fn(raw)
		if err == nil && !bytes.Equal(before, next) {
			d.writes.Add(1)
		}
		return next, err
	})
}

func testStores(t *testing.T) []struct {
	name  string
	store memory.TransactionalStore
} {
	t.Helper()
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return []struct {
		name  string
		store memory.TransactionalStore
	}{
		{name: "FileStore", store: fs},
		{name: "Journal", store: Journal{Document: &document.Memory{}}},
	}
}

func TestEntriesAndNodesAndProfile(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	for _, tc := range testStores(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store

			// Sequential AppendEntry
			e0, err := store.AppendEntry(ctx, memory.Entry{Kind: memory.KindUser, Text: "hello", Peer: "p1", Session: "s1", TS: base})
			if err != nil {
				t.Fatal(err)
			}
			e1, err := store.AppendEntry(ctx, memory.Entry{Kind: memory.KindTalk, Text: "world", Peer: "p1", Session: "s1", TS: base.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if e0.ID != 0 || e1.ID != 1 {
				t.Fatalf("sequential IDs = (%d, %d), want (0, 1)", e0.ID, e1.ID)
			}

			// GetEntry + ErrNotFound
			got, err := store.GetEntry(ctx, 1)
			if err != nil || got.Text != "world" {
				t.Fatalf("GetEntry(1) = %#v, %v", got, err)
			}
			if _, err := store.GetEntry(ctx, 99); !errors.Is(err, memory.ErrNotFound) {
				t.Fatalf("GetEntry(99) err = %v, want ErrNotFound", err)
			}

			// PutEntry idempotent + new ID
			if err := store.PutEntry(ctx, e0); err != nil {
				t.Fatal(err)
			}
			e2 := memory.Entry{ID: 2, Kind: memory.KindNote, Text: "note", TS: base.Add(2 * time.Minute)}
			if err := store.PutEntry(ctx, e2); err != nil {
				t.Fatal(err)
			}

			// EntryCount + ListEntries
			count, err := store.EntryCount(ctx)
			if err != nil || count != 3 {
				t.Fatalf("EntryCount = %d, %v; want 3", count, err)
			}
			list, err := store.ListEntries(ctx, 1, 1)
			if err != nil || len(list) != 1 || list[0].ID != 1 {
				t.Fatalf("ListEntries(1, 1) = %#v, %v", list, err)
			}
			all, err := store.ListEntries(ctx, 0, -1)
			if err != nil || len(all) != 3 || all[0].ID != 0 || all[1].ID != 1 || all[2].ID != 2 {
				t.Fatalf("ListEntries(0, -1) = %#v, %v", all, err)
			}

			// PutNode / GetNode / ListNodes
			n0 := memory.Node{Level: 1, Index: 0, Text: "summary 0+2", TS: base.Add(3 * time.Minute)}
			n1 := memory.Node{Level: 0, Index: 2, Text: "summary 2+1", TS: base.Add(4 * time.Minute)}
			if err := store.PutNode(ctx, n0); err != nil {
				t.Fatal(err)
			}
			if err := store.PutNode(ctx, n1); err != nil {
				t.Fatal(err)
			}
			if err := store.PutNode(ctx, n0); err != nil {
				t.Fatal(err)
			}
			gotNode, ok, err := store.GetNode(ctx, memory.NodeKey{Level: 1, Index: 0})
			if err != nil || !ok || gotNode.Text != "summary 0+2" {
				t.Fatalf("GetNode = %#v, %v, %v", gotNode, ok, err)
			}
			if _, ok, err := store.GetNode(ctx, memory.NodeKey{Level: 2, Index: 0}); err != nil || ok {
				t.Fatalf("missing GetNode = %v, %v", ok, err)
			}
			nodes, err := store.ListNodes(ctx)
			if err != nil || len(nodes) != 2 || nodes[0].Key() != n1.Key() || nodes[1].Key() != n0.Key() {
				t.Fatalf("ListNodes = %#v, %v", nodes, err)
			}

			// GetProfile / SaveProfile / tombstones / Purge
			p1 := memory.ProfileEntry{ID: "p1", Content: "likes Go", TS: base}
			p2 := memory.ProfileEntry{ID: "p2", Content: "uses Linux", TS: base.Add(time.Second)}
			if err := store.SaveProfile(ctx, []memory.ProfileEntry{p1, p2}); err != nil {
				t.Fatal(err)
			}
			prof, err := store.GetProfile(ctx)
			if err != nil || len(prof) != 2 {
				t.Fatalf("GetProfile = %#v, %v", prof, err)
			}

			// Remove p2 -> tombstone generated with jsonwire.Digest
			if err := store.SaveProfile(ctx, []memory.ProfileEntry{p1}); err != nil {
				t.Fatal(err)
			}
			rs := store.(memory.RemovalStore)
			removed, err := rs.Removed(ctx)
			if err != nil {
				t.Fatal(err)
			}
			p2Raw, _ := json.Marshal(p2)
			wantDigest := jsonwire.Digest(p2Raw)
			if removed["p/p2"] != wantDigest {
				t.Fatalf("removed[p/p2] = %q, want %q", removed["p/p2"], wantDigest)
			}

			// Re-adding p2 clears tombstone
			if err := store.SaveProfile(ctx, []memory.ProfileEntry{p1, p2}); err != nil {
				t.Fatal(err)
			}
			removed, err = rs.Removed(ctx)
			if err != nil || len(removed) != 0 {
				t.Fatalf("removed after re-add = %#v, %v", removed, err)
			}

			// Remove p2 again and Purge
			if err := store.SaveProfile(ctx, []memory.ProfileEntry{p1}); err != nil {
				t.Fatal(err)
			}
			if err := rs.Purge(ctx, "p/p2"); err != nil {
				t.Fatal(err)
			}
			removed, err = rs.Removed(ctx)
			if err != nil || len(removed) != 0 {
				t.Fatalf("removed after Purge = %#v, %v", removed, err)
			}
		})
	}
}

func TestZeroWritesOnNoOp(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	doc := &countingDocument{}
	j := Journal{Document: doc}

	e := memory.Entry{ID: 0, Kind: memory.KindUser, Text: "same", TS: base}
	n := memory.Node{Level: 0, Index: 0, Text: "sum", TS: base}
	p := []memory.ProfileEntry{{ID: "id1", Content: "fact", TS: base}}

	if err := j.PutEntry(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := j.PutNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := j.SaveProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	before := doc.writes.Load()
	if before != 3 {
		t.Fatalf("initial writes = %d, want 3", before)
	}

	// Identical PutEntry, PutNode, SaveProfile, no-op Purge, no-op Transact -> 0 writes!
	if err := j.PutEntry(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := j.PutNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := j.SaveProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := j.Purge(ctx, "p/nonexistent"); err != nil {
		t.Fatal(err)
	}
	if err := j.Transact(ctx, func(tx memory.Store) error {
		_, err := tx.EntryCount(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if after := doc.writes.Load(); after != before {
		t.Fatalf("no-op operations caused %d document writes, want 0", after-before)
	}

	// Same check for FileStore: file size must not grow on no-ops.
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.PutEntry(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := fs.PutNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := fs.SaveProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	statBefore, err := os.Stat(filepath.Join(dir, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.PutEntry(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := fs.PutNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := fs.SaveProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Transact(ctx, func(tx memory.Store) error { return nil }); err != nil {
		t.Fatal(err)
	}
	statAfter, err := os.Stat(filepath.Join(dir, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if statAfter.Size() != statBefore.Size() {
		t.Fatalf("FileStore grew from %d to %d bytes on no-ops", statBefore.Size(), statAfter.Size())
	}
}

func TestTransactAtomicityAndRollback(t *testing.T) {
	ctx := context.Background()
	for _, tc := range testStores(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := store.Transact(ctx, func(tx memory.Store) error {
						count, err := tx.EntryCount(ctx)
						if err != nil || count > 0 {
							return err
						}
						_, err = tx.AppendEntry(ctx, memory.Entry{Kind: memory.KindNote, Text: "once"})
						return err
					}); err != nil {
						t.Errorf("Transact: %v", err)
					}
				}()
			}
			wg.Wait()
			count, err := store.EntryCount(ctx)
			if err != nil || count != 1 {
				t.Fatalf("EntryCount = %d, %v; want 1", count, err)
			}

			sentinel := errors.New("abort")
			if err := store.Transact(ctx, func(tx memory.Store) error {
				if _, err := tx.AppendEntry(ctx, memory.Entry{Kind: memory.KindNote, Text: "rolled back"}); err != nil {
					return err
				}
				return sentinel
			}); !errors.Is(err, sentinel) {
				t.Fatalf("rollback err = %v, want %v", err, sentinel)
			}
			count, err = store.EntryCount(ctx)
			if err != nil || count != 1 {
				t.Fatalf("EntryCount after rollback = %d, %v; want 1", count, err)
			}
		})
	}
}

func TestCompactionAndParseJournal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc := &document.Memory{}
	j := Journal{Document: doc}

	base := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	for _, store := range []memory.TransactionalStore{fs, j} {
		if _, err := store.AppendEntry(ctx, memory.Entry{Kind: memory.KindUser, Text: "keep", TS: base}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutNode(ctx, memory.Node{Level: 0, Index: 0, Text: "keep node", TS: base}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveProfile(ctx, []memory.ProfileEntry{{ID: "gone", Content: "secret", TS: base}}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveProfile(ctx, nil); err != nil {
			t.Fatal(err)
		}
		tombBefore, err := store.(memory.RemovalStore).Removed(ctx)
		if err != nil || len(tombBefore) != 1 {
			t.Fatalf("tombBefore = %#v, %v", tombBefore, err)
		}

		// Churn profile updates to exceed compactAt (256 KiB).
		big := strings.Repeat("x", 1024)
		for i := range 320 {
			if err := store.SaveProfile(ctx, []memory.ProfileEntry{{ID: "keep", Content: big + strconv.Itoa(i), TS: base}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.SaveProfile(ctx, []memory.ProfileEntry{{ID: "keep", Content: "final", TS: base}}); err != nil {
			t.Fatal(err)
		}

		tombAfter, err := store.(memory.RemovalStore).Removed(ctx)
		if err != nil || !maps.Equal(tombAfter, tombBefore) {
			t.Fatalf("tombAfter = %#v, %v; want %#v", tombAfter, err, tombBefore)
		}
		entries, err := store.ListEntries(ctx, 0, -1)
		if err != nil || len(entries) != 1 || entries[0].Text != "keep" {
			t.Fatalf("entries after compaction = %#v, %v", entries, err)
		}
	}

	fileRaw, err := os.ReadFile(filepath.Join(dir, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	docRaw, err := doc.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fileRaw) >= compactAt || len(docRaw) >= compactAt {
		t.Fatalf("compacted sizes = (%d, %d), want < %d", len(fileRaw), len(docRaw), compactAt)
	}

	// ParseJournal round-trip
	entries, nodes, profile, removed, err := ParseJournal(fileRaw)
	if err != nil {
		t.Fatalf("ParseJournal: %v", err)
	}
	if len(entries) != 1 || len(nodes) != 1 || len(profile) != 1 || len(removed) != 1 {
		t.Fatalf("ParseJournal = (%d entries, %d nodes, %d profile, %d removed)", len(entries), len(nodes), len(profile), len(removed))
	}
	if _, _, _, _, err := ParseJournal([]byte("{broken json\n")); err == nil {
		t.Fatal("ParseJournal accepted malformed JSON")
	}
}

func TestMitosisSharedRoundTripAndSteadyStateZeroWrites(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC)

	docA := &countingDocument{}
	docB := &countingDocument{}
	storeA := Journal{Document: docA}
	storeB := Journal{Document: docB}

	if _, err := storeA.AppendEntry(ctx, memory.Entry{Kind: memory.KindUser, Text: "from A", TS: base}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.PutNode(ctx, memory.Node{Level: 0, Index: 0, Text: "node A", TS: base}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.SaveProfile(ctx, []memory.ProfileEntry{{ID: "pa", Content: "A fact", TS: base}}); err != nil {
		t.Fatal(err)
	}

	if _, err := storeB.AppendEntry(ctx, memory.Entry{Kind: memory.KindTalk, Text: "from B", TS: base.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := storeB.SaveProfile(ctx, []memory.ProfileEntry{{ID: "pb", Content: "B fact", TS: base.Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}

	sharedA := memory.Shared{Store: storeA}
	sharedB := memory.Shared{Store: storeB}

	// Owner (A) merges B with keepOwn=true, then follower (B) merges A with keepOwn=false.
	bSnap, err := sharedB.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedA.MergeShared(ctx, []map[string]json.RawMessage{bSnap}, true, nil); err != nil {
		t.Fatal(err)
	}

	aSnap, err := sharedA.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedB.MergeShared(ctx, []map[string]json.RawMessage{aSnap}, false, nil); err != nil {
		t.Fatal(err)
	}

	// Verify convergence.
	countA, _ := storeA.EntryCount(ctx)
	countB, _ := storeB.EntryCount(ctx)
	if countA != 2 || countB != 2 {
		t.Fatalf("converged EntryCount = (%d, %d), want (2, 2)", countA, countB)
	}

	// Steady-state merge must perform 0 writes on both sides.
	writesABefore := docA.writes.Load()
	writesBBefore := docB.writes.Load()

	aSnap2, err := sharedA.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bSnap2, err := sharedB.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}

	changedA, err := sharedA.MergeShared(ctx, []map[string]json.RawMessage{bSnap2}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	changedB, err := sharedB.MergeShared(ctx, []map[string]json.RawMessage{aSnap2}, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(changedA) != 0 || len(changedB) != 0 {
		t.Fatalf("steady-state changed = (%v, %v), want empty", changedA, changedB)
	}
	if docA.writes.Load() != writesABefore || docB.writes.Load() != writesBBefore {
		t.Fatalf("steady-state writes = (%d, %d), want (0, 0)", docA.writes.Load()-writesABefore, docB.writes.Load()-writesBBefore)
	}
}
