// Package filelock takes locks that upstream pi can interoperate with, and
// keeps the JSON documents those locks guard.
//
// Upstream guards its shared state files with npm proper-lockfile, which creates
// the lock as a DIRECTORY via mkdir and refreshes its mtime while held. A POSIX
// flock on the same path leaves a zero-byte regular file behind instead, and
// proper-lockfile never treats a plain file as stale, so it then fails with
// EEXIST forever. One orb write is enough to wedge the TypeScript runtime
// permanently. Every path both runtimes touch must lock through here.
package filelock

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	stale     = 10 * time.Second
	heartbeat = 5 * time.Second
	budget    = 10 * time.Second // Windows delete-pending retries
	minDelay  = time.Millisecond
	maxDelay  = 50 * time.Millisecond
	// AsyncStale is the window upstream's async auth lock refreshes against
	// (every 15 s), so a live upstream lock is never reclaimed.
	AsyncStale = 30 * time.Second
)

// Acquire locks path+".lock" and returns the release.
func Acquire(path string) (func() error, error) {
	held, err := acquire(context.Background(), path, stale)
	if err != nil {
		return nil, err
	}
	return held.release, nil
}

// acquire waits as long as a dead lock takes to go stale, or until ctx ends.
// Contending writers back off with jitter: lockstep retries let the same loser
// keep losing, which dropped a double-digit percentage of concurrent settings
// writes before it was jittered.
func acquire(ctx context.Context, path string, staleAfter time.Duration) (*lock, error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(staleAfter)
	delay := minDelay
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			info, statErr := os.Stat(lockPath)
			if statErr != nil {
				_ = os.Remove(lockPath)
				return nil, statErr
			}
			lock := &lock{path: lockPath, info: info, stop: make(chan struct{}), done: make(chan struct{})}
			go lock.beat()
			return lock, nil
		}
		if !errors.Is(err, os.ErrExist) && !deletePending(err) {
			return nil, err
		}
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Stat(lockPath)
			switch {
			case errors.Is(statErr, os.ErrNotExist):
				continue
			case deletePending(statErr):
			case statErr != nil:
				return nil, statErr
			// A regular file is a lock an older orb took with flock and never
			// removed; reclaim it so the two runtimes stop deadlocking on it.
			case !info.IsDir(), time.Since(info.ModTime()) > staleAfter:
				if removeErr := os.Remove(lockPath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
					continue
				}
			}
		}
		if time.Now().After(deadline) {
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
			return nil, fmt.Errorf("lock is already held: %s", lockPath)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay/2 + rand.N(delay/2+1)):
		}
		if delay < maxDelay {
			delay *= 2
		}
	}
}

type lock struct {
	path string
	info os.FileInfo
	stop chan struct{}
	done chan struct{}
}

func (lock *lock) beat() {
	defer close(lock.done)
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			if lock.owned() != nil {
				return
			}
			_ = os.Chtimes(lock.path, now, now)
		case <-lock.stop:
			return
		}
	}
}

// owned fails once another process reclaimed the lock as stale.
func (lock *lock) owned() error {
	current, err := os.Stat(lock.path)
	if err == nil && !os.SameFile(lock.info, current) {
		err = errors.New("replaced")
	}
	if err != nil {
		return fmt.Errorf("lock was compromised: %s: %w", lock.path, err)
	}
	return nil
}

func (lock *lock) release() error {
	close(lock.stop)
	<-lock.done
	if err := lock.owned(); err != nil {
		return err
	}
	return retry(func() error {
		if err := os.Remove(lock.path); !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// retry repeats op while Windows keeps the path delete-pending.
func retry(op func() error) error {
	deadline := time.Now().Add(budget)
	for {
		err := op()
		if !deletePending(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(maxDelay)
	}
}

// GroupOrOtherAccess reports POSIX group or other permission bits. Windows
// has none (Go reports 0666/0777 there); the profile directory's ACL is what
// keeps private state private on that platform.
func GroupOrOtherAccess(mode os.FileMode) bool {
	return runtime.GOOS != "windows" && mode.Perm()&0o077 != 0
}

// File is a Document kept in the file at Path. Update holds the lock upstream
// takes on the same path; Read takes no lock, so it can nest inside an Update
// and never blocks on one.
type File struct {
	Path string
	Perm os.FileMode
	// Stale is how long a dead holder's lock takes to expire: AsyncStale for
	// auth.json, whose upstream lock heartbeats; 10 s when zero.
	Stale time.Duration
	// Atomic replaces the file through a renamed temporary (WriteFile).
	// Otherwise it is rewritten in place, as upstream writes auth, settings
	// and trust files, which keeps their owner, umask, symlinks and
	// single-file bind mounts; a private Perm is then enforced on the file.
	Atomic bool
}

func (f File) Read(context.Context) ([]byte, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// Update leaves an unchanged file untouched and deletes it when change returns
// nil. ctx bounds the wait for the lock only: once change has run (it may have
// rotated a credential), its result is written.
func (f File) Update(ctx context.Context, change func([]byte) ([]byte, error)) error {
	// A private file gets a private directory: 0600 creates 0700, 0644 creates 0755.
	if err := os.MkdirAll(filepath.Dir(f.Path), f.Perm|f.Perm>>2&0o111); err != nil {
		return err
	}
	held, err := acquire(ctx, f.Path, cmp.Or(f.Stale, stale))
	if err != nil {
		return err
	}
	// Upstream proper-lockfile suppresses unlock failures; the write stands.
	defer func() { _ = held.release() }()
	current, err := f.Read(ctx)
	if err != nil {
		return err
	}
	next, err := change(current)
	if err != nil || bytes.Equal(next, current) {
		return err
	}
	if err := held.owned(); err != nil {
		return err
	}
	switch {
	case next == nil:
		if err := os.Remove(f.Path); !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	case f.Atomic:
		return WriteFile(f.Path, next, f.Perm)
	}
	if err := os.WriteFile(f.Path, next, f.Perm); err != nil {
		return err
	}
	if f.Perm&0o077 == 0 {
		return os.Chmod(f.Path, f.Perm)
	}
	return nil
}

// WriteFile replaces path with data so a reader sees the old file or the new
// one, never a torn write. A symlinked path keeps its link, and an existing
// file keeps permission bits narrower than perm, as an in-place write would.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	if info, err := os.Stat(path); err == nil {
		perm &= info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	err = temp.Chmod(perm)
	if err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = retry(func() error { return os.Rename(temp.Name(), path) })
	}
	// Windows cannot flush a directory opened read-only and NTFS journals the
	// rename itself; elsewhere the directory sync is best effort, since some
	// filesystems refuse fsync on a directory.
	if err != nil || runtime.GOOS == "windows" {
		return err
	}
	if handle, openErr := os.Open(dir); openErr == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
