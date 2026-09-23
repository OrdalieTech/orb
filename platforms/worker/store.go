package worker

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"sync"

	"github.com/OrdalieTech/orb/storage"
)

// Store is the Store port over a persisted tree of its own, out of reach of
// the file tools, so settings, credentials and model catalogs survive
// restarts without being readable by the model.
type Store struct {
	files *FileSystem
	// defaults serve a document, keyed by path, while the object has no copy
	// of its own; the first update starts from it.
	defaults map[string][]byte
	mu       sync.Mutex
}

func NewStore(files *FileSystem, defaults map[string][]byte) *Store {
	return &Store{files: files, defaults: defaults}
}

func (s *Store) Document(name string) storage.Document { return document{s, name} }

type document struct {
	store *Store
	name  string
}

func (d document) Read(ctx context.Context) ([]byte, error) {
	data, err := d.store.files.ReadBinaryFile(ctx, d.name)
	if errors.Is(err, fs.ErrNotExist) {
		return slices.Clone(d.store.defaults[d.name]), nil
	}
	return data, err
}

func (d document) Update(ctx context.Context, update func([]byte) ([]byte, error)) error {
	d.store.mu.Lock()
	defer d.store.mu.Unlock()
	current, err := d.Read(ctx)
	if err != nil {
		return err
	}
	next, err := update(current)
	switch {
	case err != nil:
		return err
	case next == nil:
		return d.store.files.Remove(ctx, d.name, false, true)
	default:
		return d.store.files.WriteFile(ctx, d.name, next)
	}
}

// Env is the Env port over the Worker's bindings: a lookup of secrets and
// vars by name. There are no credential files.
type Env func(name string) (string, bool)

func (lookup Env) Env(_ context.Context, name string) (string, bool) {
	if lookup == nil {
		return "", false
	}
	return lookup(name)
}

func (Env) FileExists(context.Context, string) bool { return false }
