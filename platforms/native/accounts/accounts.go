// Package accounts backs the named-credential store with a private JSON file.
package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/internal/filelock"
)

// NewStore performs no I/O; an unused capability creates no files.
func NewStore(path string, base auth.CredentialStore) *accounts.Store {
	return accounts.NewStoreWithDocument(file(path), base)
}

// file is an indented JSON document replaced atomically under a cross-process lock.
type file string

func (f file) Read(context.Context) ([]byte, error) {
	handle, err := os.Open(string(f))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()
	return io.ReadAll(io.LimitReader(handle, accounts.MaxSize+1))
}

func (f file) Update(ctx context.Context, change func([]byte) ([]byte, error)) error {
	path := string(f)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	release, err := filelock.Acquire(path)
	if err != nil {
		return err
	}
	defer func() { _ = release() }() // The mutation result is authoritative once rename has succeeded.
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := f.Read(ctx)
	if err != nil {
		return err
	}
	next, err := change(current)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var data bytes.Buffer
	if err := json.Indent(&data, next, "", "  "); err != nil {
		return err
	}
	if data.Len() > accounts.MaxSize {
		return errors.New("accounts file exceeds 1 MiB")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".accounts-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	_, err = temp.Write(append(data.Bytes(), '\n'))
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp.Name(), path)
}
