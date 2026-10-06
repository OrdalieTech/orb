package filelock

import (
	"os"
	"path/filepath"
	"runtime"
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

// A zero-byte regular file is what an older orb left behind with flock. It
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

// An update replaces the file atomically yet behaves like upstream's in-place
// writeFileSync: a dotfile-managed symlink keeps pointing at its target, and a
// mode the user narrowed is not widened back.
func TestFileUpdateKeepsSymlinkAndNarrowedMode(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target.json"), filepath.Join(dir, "settings.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	file := File{Path: link, Perm: 0o644}
	if err := file.Update(t.Context(), func([]byte) ([]byte, error) { return []byte(`{"a":1}`), nil }); err != nil {
		t.Fatal(err)
	}
	data, err := file.Read(t.Context())
	info, statErr := os.Lstat(link)
	if err != nil || string(data) != `{"a":1}` || statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("data %q, %v; link %v, %v", data, err, info, statErr)
	}
	if info, err := os.Stat(target); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode %v, %v", info, err)
	}
	if _, err := os.Stat(link + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock survived the update: %v", err)
	}
}
