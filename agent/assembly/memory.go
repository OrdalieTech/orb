package assembly

import (
	"context"
	"path/filepath"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/memory"
	memoryextension "github.com/OrdalieTech/orb/plugins/memory/extension"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
	"github.com/OrdalieTech/orb/plugins/memtree"
)

// Storage is opened only when the product enables the memory plugin.
func memoryExtension(store memory.Store, agentDir string, onWrite func()) extensions.Factory {
	return func(api extensions.API) error {
		activeStore := store
		if activeStore == nil {
			dir := agentDir
			if dir == "" {
				var err error
				dir, err = config.GetAgentDir()
				if err != nil {
					return err
				}
			}
			var err error
			activeStore, err = filestore.NewFileStore(filepath.Join(dir, "memory"))
			if err != nil {
				return err
			}
		}
		if onWrite != nil {
			activeStore = wrapMemoryNotify(activeStore, onWrite)
		}
		return memoryextension.Extension(activeStore)(api)
	}
}

func wrapMemoryNotify(store memory.Store, onWrite func()) memory.Store {
	base := notifyingMemory{Store: store, onWrite: onWrite}
	if ts, ok := store.(memory.TransactionalStore); ok {
		return notifyingTransactionalMemory{notifyingMemory: base, tx: ts}
	}
	return base
}

type notifyingMemory struct {
	memory.Store
	onWrite func()
}

func (s notifyingMemory) Append(ctx context.Context, item memory.Item) (string, error) {
	id, err := s.Store.Append(ctx, item)
	if err == nil {
		s.onWrite()
	}
	return id, err
}

func (s notifyingMemory) Delete(ctx context.Context, id string) error {
	err := s.Store.Delete(ctx, id)
	if err == nil {
		s.onWrite()
	}
	return err
}

type notifyingTransactionalMemory struct {
	notifyingMemory
	tx memory.TransactionalStore
}

func (s notifyingTransactionalMemory) Transact(ctx context.Context, fn func(memory.Store) error) error {
	mutated := false
	err := s.tx.Transact(ctx, func(inner memory.Store) error {
		return fn(txTracker{Store: inner, mutated: &mutated})
	})
	if err == nil && mutated {
		s.onWrite()
	}
	return err
}

type txTracker struct {
	memory.Store
	mutated *bool
}

func (t txTracker) Append(ctx context.Context, item memory.Item) (string, error) {
	id, err := t.Store.Append(ctx, item)
	if err == nil {
		*t.mutated = true
	}
	return id, err
}

func (t txTracker) Delete(ctx context.Context, id string) error {
	err := t.Store.Delete(ctx, id)
	if err == nil {
		*t.mutated = true
	}
	return err
}

// memtreeExtension reads memtree's settings; the plugin keeps its summaries in the session.
func memtreeExtension(settings *config.SettingsManager) extensions.Factory {
	var configured map[string]any
	if settings != nil {
		configured = settings.GetPluginSettings("memtree")
	}
	return memtree.Extension(memtree.OptionsFrom(configured))
}
