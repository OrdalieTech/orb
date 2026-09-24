// Package native supplies explicit filesystem and IPC adapters for Orb Bridge.
package native

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/OrdalieTech/orb/host"
	"github.com/gofrs/flock"
)

// groupOrOtherAccess reports POSIX group or other permission bits. Windows
// has none (Go reports 0666/0777 there); the profile directory's ACL is what
// keeps the bridge state private on that platform.
func groupOrOtherAccess(mode os.FileMode) bool {
	return runtime.GOOS != "windows" && mode.Perm()&0o077 != 0
}

type Store struct {
	document       host.Document
	mu             sync.Mutex
	path           string
	quota          int
	lock           *flock.Flock
	closed, failed bool
}

func OpenStore(path string, quota int) (*Store, error) {
	if !filepath.IsAbs(path) || quota < 1 {
		return nil, errors.New("absolute store path and positive quota required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || groupOrOtherAccess(info.Mode()) {
		return nil, errors.New("bridge directory must be private")
	}
	for _, name := range []string{path, path + ".lock"} {
		if info, err = os.Lstat(name); err == nil {
			if !info.Mode().IsRegular() || groupOrOtherAccess(info.Mode()) {
				return nil, errors.New("bridge store must be a private regular file")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	lock := flock.New(path + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("bridge profile already in use")
	}
	return &Store{path: path, quota: quota, lock: lock}, nil
}

// OpenStoreWithDocument keeps native single-owner locking and quotas while
// the supplied document owns persistence. Closing it never closes the database.
func OpenStoreWithDocument(path string, quota int, document host.Document) (*Store, error) {
	if document == nil {
		return nil, errors.New("bridge document required")
	}
	store, err := OpenStore(path, quota)
	if err == nil {
		store.document = document
	}
	return store, err
}
func (s *Store) Load() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return nil, errors.New("store unavailable")
	}
	if s.document != nil {
		data, err := s.document.Read(context.Background())
		if len(data) > s.quota {
			return nil, errors.New("store quota exceeded")
		}
		return data, err
	}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, int64(s.quota)+1))
	if len(b) > s.quota {
		return nil, errors.New("store quota exceeded")
	}
	return b, err
}
func (s *Store) Save(b []byte) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return errors.New("store unavailable")
	}
	if len(b) > s.quota {
		return errors.New("store quota exceeded")
	}
	if s.document != nil {
		err = s.document.Update(context.Background(), func([]byte) ([]byte, error) { return b, nil })
		if err != nil {
			s.failed = true
		}
		return err
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".bridge-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	defer func() { _ = f.Close() }()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	// Windows cannot flush a directory opened read-only (FlushFileBuffers needs
	// GENERIC_WRITE); NTFS journals the rename itself.
	if runtime.GOOS == "windows" {
		return nil
	}
	// After rename, a failed barrier has an ambiguous durable outcome. Refuse
	// all subsequent access until a new owner reopens and reconciles the store.
	d, err := os.Open(dir)
	if err != nil {
		s.failed = true
		return err
	}
	err = d.Sync()
	closeErr := d.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		s.failed = true
	}
	return err
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.lock.Close()
}
