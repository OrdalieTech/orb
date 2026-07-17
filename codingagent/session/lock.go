package session

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

func withFileLock(path string, fn func() error) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lock.Unlock())
	}()
	return fn()
}
