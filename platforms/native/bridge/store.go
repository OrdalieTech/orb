// Package bridge supplies explicit filesystem and IPC adapters for Orb Bridge.
package bridge

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/gofrs/flock"
)

// groupOrOtherAccess reports POSIX group or other permission bits. Windows
// has none (Go reports 0666/0777 there); the profile directory's ACL is what
// keeps the bridge state private on that platform.
func groupOrOtherAccess(mode os.FileMode) bool {
	return runtime.GOOS != "windows" && mode.Perm()&0o077 != 0
}

// Store is one Bridge document behind the profile's single-owner lock: the
// supplied document when native storage is open, else a private file. The
// lock makes this process the only writer, so the last value read or written
// stays authoritative and is served from memory.
type Store struct {
	document               host.Document
	mu                     sync.Mutex
	path                   string
	quota                  int
	lock                   *flock.Flock
	cached                 []byte
	loaded, closed, failed bool
}

// OpenStore takes the lock beside path; closing the store never closes document.
func OpenStore(path string, quota int, document host.Document) (*Store, error) {
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
	return &Store{document: document, path: path, quota: quota, lock: lock}, nil
}

func (s *Store) Read(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(ctx)
}

func (s *Store) readLocked(ctx context.Context) ([]byte, error) {
	if s.closed || s.failed {
		return nil, errors.New("store unavailable")
	}
	if s.loaded {
		return s.cached, nil
	}
	var b []byte
	var err error
	if s.document != nil {
		b, err = s.document.Read(ctx)
	} else if f, openErr := os.Open(s.path); openErr == nil {
		b, err = io.ReadAll(io.LimitReader(f, int64(s.quota)+1))
		_ = f.Close()
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return nil, openErr
	}
	if len(b) > s.quota {
		return nil, errors.New("store quota exceeded")
	}
	s.cached, s.loaded = b, err == nil
	return b, err
}

// Update commits change's result; nil removes the document. A failed write
// refuses all later access until a new owner reopens and reconciles the store.
func (s *Store) Update(ctx context.Context, change func([]byte) ([]byte, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.readLocked(ctx)
	if err != nil {
		return err
	}
	b, err := change(current)
	if err != nil {
		return err
	}
	if len(b) > s.quota {
		return errors.New("store quota exceeded")
	}
	if s.document != nil {
		err = document.Replace(ctx, s.document, b)
	} else {
		err = s.writeFile(b)
	}
	s.failed = err != nil
	s.cached = b
	return err
}

func (s *Store) writeFile(b []byte) error {
	if b == nil {
		if err := os.Remove(s.path); !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
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
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
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
