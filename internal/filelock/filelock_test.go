package filelock

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Upstream's proper-lockfile creates the lock with mkdir and treats any existing
// path as held, so the lock must be a directory and must be gone after release.
func TestAcquireUsesRemovableDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")

	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("lock is not a directory; proper-lockfile would never treat it as stale")
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock survived release: %v", err)
	}
}

// A zero-byte regular file is what an older pigo left behind with flock. It
// wedges upstream forever, so acquiring must reclaim it rather than honour it.
func TestAcquireReclaimsLeftoverRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("leftover flock file must be reclaimed: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// Jittered backoff exists so contending writers do not lose in lockstep; every
// writer must eventually get the lock, and only one may hold it at a time.
func TestAcquireSerializesContendingWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	var group sync.WaitGroup
	var mu sync.Mutex
	held, peak, failures := 0, 0, 0
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			release, err := Acquire(path)
			mu.Lock()
			if err != nil {
				failures++
				mu.Unlock()
				return
			}
			held++
			if held > peak {
				peak = held
			}
			mu.Unlock()

			mu.Lock()
			held--
			mu.Unlock()
			_ = release()
		}()
	}
	group.Wait()
	if failures != 0 {
		t.Fatalf("%d of 16 writers never acquired the lock", failures)
	}
	if peak != 1 {
		t.Fatalf("peak concurrent holders = %d, want 1", peak)
	}
}
