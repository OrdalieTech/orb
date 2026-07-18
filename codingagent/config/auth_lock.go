package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	authLockStale = 30 * time.Second
	// TS reload uses proper-lockfile's 10-second default stale window.
	authLockHeartbeat = 5 * time.Second
)

type authDirectoryLock struct {
	path string
	info os.FileInfo
	stop chan struct{}
	done chan struct{}

	mu  sync.Mutex
	err error
}

func acquireAuthDirectoryLock(ctx context.Context, authPath string) (*authDirectoryLock, error) {
	lockPath := authPath + ".lock"
	delay := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			info, statErr := os.Stat(lockPath)
			if statErr != nil {
				_ = os.Remove(lockPath)
				return nil, statErr
			}
			lock := &authDirectoryLock{path: lockPath, info: info, stop: make(chan struct{}), done: make(chan struct{})}
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
		case time.Since(info.ModTime()) > authLockStale:
			if removeErr := os.Remove(lockPath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		if attempt >= 10 {
			return nil, fmt.Errorf("auth storage lock is already held: %s", lockPath)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay < 10*time.Second {
			delay *= 2
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
		}
	}
}

func (lock *authDirectoryLock) heartbeat() {
	defer close(lock.done)
	ticker := time.NewTicker(authLockHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-lock.stop:
			return
		case <-ticker.C:
			if err := lock.checkOwnership(); err != nil {
				lock.setError(err)
				return
			}
			now := time.Now()
			if err := os.Chtimes(lock.path, now, now); err != nil {
				lock.setError(fmt.Errorf("auth storage lock heartbeat failed: %w", err))
				return
			}
		}
	}
}

func (lock *authDirectoryLock) Check() error {
	lock.mu.Lock()
	err := lock.err
	lock.mu.Unlock()
	if err != nil {
		return err
	}
	return lock.checkOwnership()
}

func (lock *authDirectoryLock) Release() error {
	close(lock.stop)
	<-lock.done
	if err := lock.Check(); err != nil {
		return err
	}
	return os.Remove(lock.path)
}

func (lock *authDirectoryLock) checkOwnership() error {
	current, err := os.Stat(lock.path)
	if err != nil {
		return fmt.Errorf("auth storage lock was compromised: %w", err)
	}
	if !os.SameFile(lock.info, current) {
		return errors.New("auth storage lock was replaced")
	}
	return nil
}

func (lock *authDirectoryLock) setError(err error) {
	lock.mu.Lock()
	if lock.err == nil {
		lock.err = err
	}
	lock.mu.Unlock()
}
