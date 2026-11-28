package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

func TestStressSQLiteConcurrentHammerAndReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "orb.db")
	db, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store := db.Memory("stress")
	shared := memory.Shared{Store: store}

	const workers = 10
	const appendsPerWorker = 25
	const totalAppends = workers * appendsPerWorker

	var idMu sync.Mutex
	seenIDs := make([]int, 0, totalAppends)
	var errCount atomic.Int64
	var firstErrMu sync.Mutex
	var firstErr error
	recordErr := func(err error) {
		if err == nil {
			return
		}
		errCount.Add(1)
		firstErrMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		firstErrMu.Unlock()
	}

	var wg sync.WaitGroup
	// 1. Concurrent AppendEntry goroutines verifying monotonic sequential IDs 0..N-1.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for i := 0; i < appendsPerWorker; i++ {
				e, err := store.AppendEntry(ctx, memory.Entry{
					Kind:    memory.KindWork,
					Text:    fmt.Sprintf("worker-%d entry-%d 日本語🚀", wid, i),
					Peer:    fmt.Sprintf("peer-%d", wid),
					Session: "stress-sess",
				})
				if err != nil {
					recordErr(fmt.Errorf("AppendEntry(w=%d,i=%d): %w", wid, i, err))
					return
				}
				idMu.Lock()
				seenIDs = append(seenIDs, e.ID)
				idMu.Unlock()
			}
		}(w)
	}

	// 2. Concurrent PutNode, SaveProfile, Transact, and Shared.MergeShared goroutines.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := store.PutNode(ctx, memory.Node{
					Level: 0,
					Index: wid*20 + i,
					Text:  fmt.Sprintf("node-%d-%d 🚀", wid, i),
					TS:    time.Now().UTC(),
				}); err != nil {
					recordErr(fmt.Errorf("PutNode: %w", err))
				}
				if err := store.Transact(ctx, func(tx memory.Store) error {
					p, err := tx.GetProfile(ctx)
					if err != nil {
						return err
					}
					next, _, err := memory.ProfileSet(p, fmt.Sprintf("prof-%d", wid), fmt.Sprintf("val-%d-%d", wid, i), time.Now().UTC())
					if err != nil {
						return err
					}
					return tx.SaveProfile(ctx, next)
				}); err != nil {
					recordErr(fmt.Errorf("Transact SaveProfile: %w", err))
				}
				// Shared.MergeShared with a remote node.
				rawNode, _ := json.Marshal(memory.Node{
					Level: 1,
					Index: wid*20 + i,
					Text:  fmt.Sprintf("merged-node-%d-%d", wid, i),
					TS:    time.Now().UTC(),
				})
				if _, err := shared.MergeShared(ctx, []map[string]json.RawMessage{{
					fmt.Sprintf("n/1/%d", wid*20+i): rawNode,
				}}, false, nil); err != nil {
					recordErr(fmt.Errorf("MergeShared: %w", err))
				}
			}
		}(w)
	}
	wg.Wait()

	if errCount.Load() > 0 {
		t.Fatalf("concurrent SQLite hammer had %d errors; first: %v", errCount.Load(), firstErr)
	}

	// Verify monotonic sequential IDs 0..totalAppends-1 with zero gaps or duplicates.
	sort.Ints(seenIDs)
	if len(seenIDs) != totalAppends {
		t.Fatalf("seenIDs len=%d, want %d", len(seenIDs), totalAppends)
	}
	for i, id := range seenIDs {
		if id != i {
			t.Fatalf("ID gap/duplicate at index %d: got %d", i, id)
		}
	}

	// Create a tombstone by removing one profile entry, then snapshot state before close.
	profBefore, err := store.GetProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profBefore) > 0 {
		trimmed, ok := memory.ProfileRemove(profBefore, profBefore[0].ID)
		if !ok {
			t.Fatal("ProfileRemove failed")
		}
		if err := store.SaveProfile(ctx, trimmed); err != nil {
			t.Fatal(err)
		}
	}

	entriesBefore, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	nodesBefore, err := store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profBefore, err = store.GetProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removedBefore, err := store.Removed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(removedBefore) == 0 {
		t.Fatalf("expected >=1 tombstone after ProfileRemove, got 0")
	}

	// Close and reopen SQLite DB.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	store2 := db2.Memory("stress")

	entriesAfter, err := store2.ListEntries(ctx, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	nodesAfter, err := store2.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profAfter, err := store2.GetProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removedAfter, err := store2.Removed(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("reopen entries len=%d, want %d", len(entriesAfter), len(entriesBefore))
	}
	for i := range entriesBefore {
		if entriesAfter[i].ID != entriesBefore[i].ID || entriesAfter[i].Text != entriesBefore[i].Text {
			t.Fatalf("reopen entry %d mismatch", i)
		}
	}
	if len(nodesAfter) != len(nodesBefore) {
		t.Fatalf("reopen nodes len=%d, want %d", len(nodesAfter), len(nodesBefore))
	}
	if len(profAfter) != len(profBefore) {
		t.Fatalf("reopen profile len=%d, want %d", len(profAfter), len(profBefore))
	}
	if len(removedAfter) != len(removedBefore) {
		t.Fatalf("reopen removed len=%d, want %d", len(removedAfter), len(removedBefore))
	}
	t.Logf("SQLite hammer & reopen verified: entries=%d nodes=%d profile=%d tombstones=%d",
		len(entriesAfter), len(nodesAfter), len(profAfter), len(removedAfter))
}

func TestStressFileStoreAndJournalCompaction(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fs, err := filestore.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Generate >300 KiB of churn on FileStore (repeatedly updating profile + entries + nodes).
	payload := strings.Repeat("churn-payload-日本語-🚀-", 40) // ~1 KiB
	for i := 0; i < 60; i++ {
		if _, err := fs.AppendEntry(ctx, memory.Entry{
			Kind: memory.KindWork,
			Text: fmt.Sprintf("fs-entry-%d %s", i, payload),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Heavy profile overwrite churn (>300 KiB total journal growth).
	for i := 0; i < 320; i++ {
		if err := fs.SaveProfile(ctx, []memory.ProfileEntry{
			{ID: "p1", Content: fmt.Sprintf("churn-v%d: %s", i, strings.Repeat("x", 800)), TS: time.Now().UTC()},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Verify 0 writes on steady-state idempotent no-op calls.
	journalPath := filepath.Join(dir, "memory.jsonl")
	stBefore, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	e0, err := fs.GetEntry(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.PutEntry(ctx, e0); err != nil {
		t.Fatal(err)
	}
	pCur, err := fs.GetProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.SaveProfile(ctx, pCur); err != nil {
		t.Fatal(err)
	}
	stAfter, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if stAfter.Size() != stBefore.Size() {
		t.Fatalf("idempotent no-op calls wrote to journal: before=%d after=%d", stBefore.Size(), stAfter.Size())
	}

	// Reopen FileStore from disk and verify zero data loss.
	fs2, err := filestore.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cnt, err := fs2.EntryCount(ctx)
	if err != nil || cnt != 60 {
		t.Fatalf("reopen FileStore EntryCount=%d err=%v, want 60", cnt, err)
	}
	p2, err := fs2.GetProfile(ctx)
	if err != nil || len(p2) != 1 || !strings.HasPrefix(p2[0].Content, "churn-v319:") {
		t.Fatalf("reopen FileStore profile=%+v err=%v", p2, err)
	}

	// Also test Journal adapter over an in-memory counting document.
	doc := &countingDoc{}
	j := filestore.Journal{Document: doc}
	for i := 0; i < 40; i++ {
		if _, err := j.AppendEntry(ctx, memory.Entry{Kind: memory.KindNote, Text: fmt.Sprintf("j-%d %s", i, payload)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 300; i++ {
		if err := j.SaveProfile(ctx, []memory.ProfileEntry{{ID: "jp1", Content: fmt.Sprintf("jv%d %s", i, strings.Repeat("y", 900)), TS: time.Now().UTC()}}); err != nil {
			t.Fatal(err)
		}
	}
	writesBeforeNoop := doc.writes.Load()
	je0, err := j.GetEntry(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.PutEntry(ctx, je0); err != nil {
		t.Fatal(err)
	}
	jp, err := j.GetProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.SaveProfile(ctx, jp); err != nil {
		t.Fatal(err)
	}
	if doc.writes.Load() != writesBeforeNoop {
		t.Fatalf("Journal idempotent no-op triggered %d writes, want 0", doc.writes.Load()-writesBeforeNoop)
	}
	t.Logf("FileStore & Journal compaction verified: fileSize=%dB journalSize=%dB noopWrites=0", stAfter.Size(), len(doc.data))
}

type countingDoc struct {
	mu     sync.Mutex
	data   []byte
	writes atomic.Int64
}

func (d *countingDoc) Read(context.Context) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]byte, len(d.data))
	copy(cp, d.data)
	return cp, nil
}

func (d *countingDoc) Update(_ context.Context, fn func([]byte) ([]byte, error)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]byte, len(d.data))
	copy(cp, d.data)
	next, err := fn(cp)
	if err != nil {
		return err
	}
	if bytes.Equal(next, cp) {
		return nil
	}
	d.writes.Add(1)
	d.data = append(d.data[:0], next...)
	return nil
}
