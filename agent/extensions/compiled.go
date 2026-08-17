package extensions

import "fmt"

type CompiledExtension struct {
	Name           string
	Factory        Factory
	Hidden         bool
	DefaultEnabled bool
}

type CompiledLoadError struct {
	Name string
	Err  error
}

func (loadError CompiledLoadError) Error() string {
	return fmt.Sprintf("load compiled extension %q: %v", loadError.Name, loadError.Err)
}

func LoadCompiled(
	cwd string,
	catalog []CompiledExtension,
	overrides map[string]bool,
	disableAll bool,
) (*Registry, []CompiledLoadError) {
	if disableAll || len(catalog) == 0 {
		return nil, nil
	}
	var registry *Registry
	var loadErrors []CompiledLoadError
	for _, entry := range catalog {
		enabled := entry.DefaultEnabled
		if override, exists := overrides[entry.Name]; exists {
			enabled = override
		}
		if !enabled {
			continue
		}
		if registry == nil {
			registry = NewRegistry(cwd)
		}
		if err := registry.Register("<inline:"+entry.Name+">", entry.Factory, WithHidden(entry.Hidden)); err != nil {
			loadErrors = append(loadErrors, CompiledLoadError{Name: entry.Name, Err: err})
		}
	}
	if registry != nil && registry.Len() == 0 {
		registry = nil
	}
	return registry, loadErrors
}

func (registry *Registry) Len() int {
	if registry == nil {
		return 0
	}
	registry.mu.RLock()
	length := len(registry.extensions)
	registry.mu.RUnlock()
	return length
}

func (registry *Registry) RegisteredFlags() []Flag {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	extensions := append([]*Extension(nil), registry.extensions...)
	registry.mu.RUnlock()
	seen := make(map[string]struct{})
	var flags []Flag
	for _, extension := range extensions {
		extension.mu.RLock()
		for _, name := range extension.flagOrder {
			if _, exists := seen[name]; exists {
				continue
			}
			flag, exists := extension.flags[name]
			if !exists {
				continue
			}
			seen[name] = struct{}{}
			flags = append(flags, flag)
		}
		extension.mu.RUnlock()
	}
	return flags
}

func (registry *Registry) SetFlagValue(name string, value any) {
	if registry != nil {
		registry.runtime.setFlag(name, value)
	}
}
