package models

import (
	"os"
	"path/filepath"
	"testing"
)

// Upstream's proper-lockfile creates the lock with mkdir and treats any existing
// path as held, so the lock must be a directory and must be removed on release.
// A leftover regular file from an older pigo has to be reclaimed, not honoured.
func TestStoreLockUsesRemovableDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")

	lock, err := acquireStoreLock(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("lock is not a directory; proper-lockfile would never treat it as stale")
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock survived release: %v", err)
	}
}

func TestStoreLockReclaimsLeftoverRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireStoreLock(path)
	if err != nil {
		t.Fatalf("a zero-byte lock left by an older pigo must be reclaimed: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}
