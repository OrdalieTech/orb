package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithFileLockLeavesNoResidue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	err := withFileLock(path, func() error {
		info, statErr := os.Stat(path + ".lock")
		if statErr != nil {
			return statErr
		}
		// proper-lockfile compatibility: the lock is a directory, not a file.
		if !info.IsDir() {
			return errors.New("lock is not a directory")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock residue left after release: %v", err)
	}
}

func TestWithFileLockStealsStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lockPath := path + ".lock"
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := withFileLock(path, func() error { called = true; return nil }); err != nil {
		t.Fatalf("stale lock not stolen: %v", err)
	}
	if !called {
		t.Fatal("locked function not called")
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock residue left after stealing: %v", err)
	}
}

func TestWithFileLockExcludesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var active int32
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 5; iteration++ {
				err := withFileLock(path, func() error {
					if !atomic.CompareAndSwapInt32(&active, 0, 1) {
						return errors.New("two writers inside the lock")
					}
					time.Sleep(time.Millisecond)
					atomic.StoreInt32(&active, 0)
					return nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock residue left after contention: %v", err)
	}
}
