// Package filelock takes locks that upstream pi can interoperate with.
//
// Upstream guards its shared state files with npm proper-lockfile, which creates
// the lock as a DIRECTORY via mkdir and refreshes its mtime while held. A POSIX
// flock on the same path leaves a zero-byte regular file behind instead, and
// proper-lockfile never treats a plain file as stale, so it then fails with
// EEXIST forever. One pigo write is enough to wedge the TypeScript runtime
// permanently. Every path both runtimes touch must lock through here.
package filelock

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"time"
)

const (
	stale     = 10 * time.Second
	heartbeat = 5 * time.Second
	budget    = 10 * time.Second
	minDelay  = time.Millisecond
	maxDelay  = 50 * time.Millisecond
)

// Acquire locks path+".lock" and returns the release. Contending writers back off
// with jitter: lockstep retries let the same loser keep losing, which dropped a
// double-digit percentage of concurrent settings writes before it was jittered.
func Acquire(path string) (func() error, error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(budget)
	delay := minDelay
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			lock := &lock{path: lockPath, stop: make(chan struct{}), done: make(chan struct{})}
			go lock.beat()
			return lock.release, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(lockPath)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			continue
		case statErr != nil:
			return nil, statErr
		// A regular file is a lock an older pigo took with flock and never
		// removed; reclaim it so the two runtimes stop deadlocking on it.
		case !info.IsDir(), time.Since(info.ModTime()) > stale:
			if removeErr := os.Remove(lockPath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("lock is already held: %s", lockPath)
		}
		time.Sleep(delay/2 + rand.N(delay/2+1))
		if delay < maxDelay {
			delay *= 2
		}
	}
}

type lock struct {
	path string
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
			_ = os.Chtimes(lock.path, now, now)
		case <-lock.stop:
			return
		}
	}
}

func (lock *lock) release() error {
	close(lock.stop)
	<-lock.done
	if err := os.Remove(lock.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
