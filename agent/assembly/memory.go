package assembly

import (
	"path/filepath"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/memory"
	memoryextension "github.com/OrdalieTech/orb/plugins/memory/extension"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
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
