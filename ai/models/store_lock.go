package models

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Upstream guards the models store with npm `proper-lockfile`, which creates the
// lock as a DIRECTORY via mkdir and refreshes its mtime. A POSIX flock on
// "<path>.lock" instead leaves a zero-byte regular file behind, and
// proper-lockfile then fails with EEXIST forever ("Lock file is already being
// held") because it never treats a plain file as stale. That permanently breaks
// `pi update --models` on any machine where pigo has refreshed the catalog, so
// the protocol has to match rather than merely be correct in isolation.
const (
	storeLockStale     = 10 * time.Second
	storeLockHeartbeat = 5 * time.Second
	storeLockAttempts  = 10
)

type storeLock struct {
	path string
	stop chan struct{}
	done chan struct{}
}

// ponytail: third mkdir-lock in the tree (auth_lock.go, settings_write.go are
// the others) because ai/ cannot import codingagent/config without inverting the
// layering. Extract to internal/ when a fourth caller appears.
func acquireStoreLock(path string) (*storeLock, error) {
	lockPath := path + ".lock"
	delay := 20 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			lock := &storeLock{path: lockPath, stop: make(chan struct{}), done: make(chan struct{})}
			go lock.heartbeat()
			return lock, nil
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
		// A regular file here is a lock left by an older pigo, which never
		// removed it; reclaim it so the two runtimes stop deadlocking.
		case !info.IsDir(), time.Since(info.ModTime()) > storeLockStale:
			if removeErr := os.Remove(lockPath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		if attempt >= storeLockAttempts {
			return nil, fmt.Errorf("models store lock is already held: %s", lockPath)
		}
		time.Sleep(delay)
		if delay < time.Second {
			delay *= 2
		}
	}
}

func (lock *storeLock) heartbeat() {
	defer close(lock.done)
	ticker := time.NewTicker(storeLockHeartbeat)
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

func (lock *storeLock) release() error {
	close(lock.stop)
	<-lock.done
	if err := os.Remove(lock.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
