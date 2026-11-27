package assembly

import (
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	memoryextension "github.com/OrdalieTech/orb/plugins/memory/extension"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
	"github.com/OrdalieTech/orb/plugins/memtree"
)

// Storage is opened only when the product enables the memory plugin.
func memoryExtension(options CatalogOptions) extensions.Factory {
	return func(api extensions.API) error {
		activeStore := options.Memory
		if activeStore == nil {
			dir := options.AgentDir
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
		viewBytes, compactorModel := memorySettings(options.Settings)
		opts := memoryextension.Options{
			ViewBytes:      viewBytes,
			CompactorModel: compactorModel,
			StreamFn:       options.StreamFn,
			OnWrite:        options.OnMemoryWrite,
		}
		return memoryextension.Extension(activeStore, opts)(api)
	}
}

func memorySettings(settings *config.SettingsManager) (viewBytes int, compactorModel string) {
	if settings == nil {
		return 0, ""
	}
	readMap := func(m map[string]any) {
		if m == nil {
			return
		}
		switch v := m["viewBytes"].(type) {
		case int:
			if v > 0 {
				viewBytes = v
			}
		case int64:
			if v > 0 {
				viewBytes = int(v)
			}
		case float64:
			if v > 0 {
				viewBytes = int(v)
			}
		}
		if s, ok := m["compactorModel"].(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				compactorModel = trimmed
			}
		}
	}
	if top, ok := settings.GetSettings()["memory"].(map[string]any); ok {
		readMap(top)
	}
	readMap(settings.GetPluginSettings("memory"))
	return viewBytes, compactorModel
}

// memtreeExtension reads memtree's settings; the plugin keeps its summaries in the session.
func memtreeExtension(settings *config.SettingsManager) extensions.Factory {
	var configured map[string]any
	if settings != nil {
		configured = settings.GetPluginSettings("memtree")
	}
	return memtree.Extension(memtree.OptionsFrom(configured))
}
