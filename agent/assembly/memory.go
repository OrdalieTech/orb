package assembly

import (
	"path/filepath"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/memory"
	memoryextension "github.com/OrdalieTech/orb/plugins/memory/extension"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
	"github.com/OrdalieTech/orb/plugins/memtree"
)

// Storage is opened only when the product enables the memory plugin.
func memoryExtension(store memory.Store, agentDir string) extensions.Factory {
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
		return memoryextension.Extension(activeStore)(api)
	}
}

// memtreeExtension keeps one node file per session under the agent dir.
func memtreeExtension(agentDir string, settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		if agentDir == "" {
			var err error
			if agentDir, err = config.GetAgentDir(); err != nil {
				return err
			}
		}
		var configured map[string]any
		if settings != nil {
			configured = settings.GetPluginSettings("memtree")
		}
		return memtree.Extension(filepath.Join(agentDir, "memtree"), configured)(api)
	}
}
