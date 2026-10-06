// Package accounts backs the named-credential store with a private JSON file.
package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/internal/filelock"
)

// NewStore performs no I/O; an unused capability creates no files.
func NewStore(path string, base auth.CredentialStore) *accounts.Store {
	return accounts.NewStoreWithDocument(file{filelock.File{Path: path, Perm: 0o600, Atomic: true}}, base)
}

// file keeps the document indented for people who read it.
type file struct{ filelock.File }

func (f file) Update(ctx context.Context, change func([]byte) ([]byte, error)) error {
	return f.File.Update(ctx, func(current []byte) ([]byte, error) {
		next, err := change(current)
		if err != nil || next == nil {
			return next, err
		}
		var data bytes.Buffer
		if err := json.Indent(&data, next, "", "  "); err != nil {
			return nil, err
		}
		if data.Len() > accounts.MaxSize {
			return nil, errors.New("accounts file exceeds 1 MiB")
		}
		return append(data.Bytes(), '\n'), nil
	})
}
