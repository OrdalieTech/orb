package session

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/orb/internal/filelock"
)

func withFileLock(path string, fn func() error) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	release, err := filelock.Acquire(path)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, release())
	}()
	return fn()
}
