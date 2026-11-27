package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/plugins/memory"
)

func openTestMemoryDB(t *testing.T) *DB {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), filepath.Join(root, "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMemoryEntriesNodesProfile(t *testing.T) {
	ctx := context.Background()
	db := openTestMemoryDB(t)
	store := db.Memory("personal")
	other := db.Memory("other")

	if n, err := store.EntryCount(ctx); err != nil || n != 0 {
		t.Fatalf("empty EntryCount = %d, %v", n, err)
	}
	if _, err := store.GetEntry(ctx, 0); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("GetEntry missing = %v, want ErrNotFound", err)
	}
	if err := store.PutEntry(ctx, memory.Entry{ID: -1}); !errors.Is(err, memory.ErrInvalidRef) {
		t.Fatalf("PutEntry(-1) = %v, want ErrInvalidRef", err)
	}

	t0 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	e0, err := store.AppendEntry(ctx, memory.Entry{Kind: memory.KindUser, Text: "hello", Peer: "alice", Session: "s1", TS: t0})
	if err != nil || e0.ID != 0 {
		t.Fatalf("AppendEntry 0 = %+v, %v", e0, err)
	}
	e1, err := store.AppendEntry(ctx, memory.Entry{Kind: memory.KindTalk, Text: "hi there"})
	if err != nil || e1.ID != 1 || e1.TS.IsZero() {
		t.Fatalf("AppendEntry 1 = %+v, %v", e1, err)
	}
	if n, err := store.EntryCount(ctx); err != nil || n != 2 {
		t.Fatalf("EntryCount = %d, %v", n, err)
	}

	// Namespace isolation.
	if n, err := other.EntryCount(ctx); err != nil || n != 0 {
		t.Fatalf("other namespace leaked: %d, %v", n, err)
	}

	// PutEntry idempotent no-op and update.
	if err := store.PutEntry(ctx, e0); err != nil {
		t.Fatalf("PutEntry no-op: %v", err)
	}
	e5 := memory.Entry{ID: 5, Kind: memory.KindNote, Text: "later", TS: t0.Add(time.Minute)}
	if err := store.PutEntry(ctx, e5); err != nil {
		t.Fatalf("PutEntry 5: %v", err)
	}
	if n, err := store.EntryCount(ctx); err != nil || n != 6 {
		t.Fatalf("EntryCount after PutEntry(5) = %d, %v; want 6", n, err)
	}
	list, err := store.ListEntries(ctx, 1, 1)
	if err != nil || len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("ListEntries(1, 1) = %+v, %v", list, err)
	}
	all, err := store.ListEntries(ctx, 0, -1)
	if err != nil || len(all) != 3 || all[0].ID != 0 || all[1].ID != 1 || all[2].ID != 5 {
		t.Fatalf("ListEntries(0, -1) = %+v, %v", all, err)
	}

	// Nodes: ordering and idempotent no-op.
	if _, ok, err := store.GetNode(ctx, memory.NodeKey{Level: 1, Index: 0}); err != nil || ok {
		t.Fatalf("GetNode missing = %v, %v", ok, err)
	}
	n10 := memory.Node{Level: 1, Index: 0, Text: "summary 0..1", TS: t0}
	n01 := memory.Node{Level: 0, Index: 1, Text: "leaf 1", TS: t0}
	n00 := memory.Node{Level: 0, Index: 0, Text: "leaf 0", TS: t0}
	for _, n := range []memory.Node{n10, n01, n00} {
		if err := store.PutNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	// Idempotent no-op put.
	if err := store.PutNode(ctx, n10); err != nil {
		t.Fatal(err)
	}
	gotNode, ok, err := store.GetNode(ctx, memory.NodeKey{Level: 1, Index: 0})
	if err != nil || !ok || gotNode.Text != "summary 0..1" {
		t.Fatalf("GetNode = %+v, %v, %v", gotNode, ok, err)
	}
	nodes, err := store.ListNodes(ctx)
	if err != nil || len(nodes) != 3 ||
		nodes[0].Key() != (memory.NodeKey{Level: 0, Index: 0}) ||
		nodes[1].Key() != (memory.NodeKey{Level: 0, Index: 1}) ||
		nodes[2].Key() != (memory.NodeKey{Level: 1, Index: 0}) {
		t.Fatalf("ListNodes order = %+v, %v", nodes, err)
	}

	// Profile: ordering by ts ASC, id ASC and idempotent SaveProfile.
	p2 := memory.ProfileEntry{ID: "b", Content: "second", TS: t0.Add(time.Second)}
	p1 := memory.ProfileEntry{ID: "a", Content: "first", TS: t0}
	if err := store.SaveProfile(ctx, []memory.ProfileEntry{p2, p1}); err != nil {
		t.Fatal(err)
	}
	// Idempotent no-op save.
	if err := store.SaveProfile(ctx, []memory.ProfileEntry{p1, p2}); err != nil {
		t.Fatal(err)
	}
	prof, err := store.GetProfile(ctx)
	if err != nil || len(prof) != 2 || prof[0].ID != "a" || prof[1].ID != "b" {
		t.Fatalf("GetProfile = %+v, %v", prof, err)
	}
}

func TestMemoryTransactAndConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	db := openTestMemoryDB(t)
	store := db.Memory("personal")

	boom := errors.New("rollback me")
	if err := store.Transact(ctx, func(tx memory.Store) error {
		if _, err := tx.AppendEntry(ctx, memory.Entry{Kind: memory.KindUser, Text: "rolled back"}); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Transact error = %v, want %v", err, boom)
	}
	if n, err := store.EntryCount(ctx); err != nil || n != 0 {
		t.Fatalf("EntryCount after rollback = %d, %v", n, err)
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 10 {
				if _, err := store.AppendEntry(ctx, memory.Entry{Kind: memory.KindWork, Text: "concurrent"}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if n, err := store.EntryCount(ctx); err != nil || n != 40 {
		t.Fatalf("EntryCount after concurrent appends = %d, %v; want 40", n, err)
	}
}

func TestMemorySharedMitosisSync(t *testing.T) {
	ctx := context.Background()
	dbA := openTestMemoryDB(t)
	dbB := openTestMemoryDB(t)
	storeA := dbA.Memory("personal")
	storeB := dbB.Memory("personal")

	ts := time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC)
	if _, err := storeA.AppendEntry(ctx, memory.Entry{Kind: memory.KindUser, Text: "sync me", TS: ts}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.PutNode(ctx, memory.Node{Level: 0, Index: 0, Text: "node 0", TS: ts}); err != nil {
		t.Fatal(err)
	}
	if err := storeA.SaveProfile(ctx, []memory.ProfileEntry{{ID: "p1", Content: "likes go", TS: ts}}); err != nil {
		t.Fatal(err)
	}

	sharedA := memory.Shared{Store: storeA}
	sharedB := memory.Shared{Store: storeB}

	outA, err := sharedA.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := sharedB.MergeShared(ctx, []map[string]json.RawMessage{outA}, false, nil)
	if err != nil || len(changed) != 3 {
		t.Fatalf("MergeShared A->B = %v, %v; want 3 changes", changed, err)
	}
	// Second merge is a no-op.
	changed, err = sharedB.MergeShared(ctx, []map[string]json.RawMessage{outA}, false, nil)
	if err != nil || len(changed) != 0 {
		t.Fatalf("idempotent MergeShared = %v, %v; want 0", changed, err)
	}

	// Delete profile entry on A and propagate removal to B.
	if err := storeA.SaveProfile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	remA, err := sharedA.Removed(ctx)
	if err != nil || remA["p/p1"] == "" {
		t.Fatalf("Removed on A = %+v, %v", remA, err)
	}
	changed, err = sharedB.MergeShared(ctx, nil, false, []string{"p/p1"})
	if err != nil || len(changed) != 1 {
		t.Fatalf("MergeShared removal A->B = %v, %v", changed, err)
	}
	profB, err := storeB.GetProfile(ctx)
	if err != nil || len(profB) != 0 {
		t.Fatalf("GetProfile on B after removal = %+v, %v", profB, err)
	}
}
