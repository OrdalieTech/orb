package extensions

import "fmt"

type CompiledExtension struct {
	Name    string
	Factory Factory
	Hidden  bool
	// Replaceable built-ins step aside for an extension that registers one of
	// their tool, command, or flag names.
	Replaceable    bool
	DefaultEnabled bool
}

type CompiledLoadError struct {
	Name string
	Err  error
}

func (loadError CompiledLoadError) Error() string {
	return fmt.Sprintf("load compiled extension %q: %v", loadError.Name, loadError.Err)
}

// LoadCompiled registers the enabled catalog entries; enablement decisions
// belong to the caller (agent/assembly resolves settings into DefaultEnabled).
func LoadCompiled(cwd string, catalog []CompiledExtension) (*Registry, []CompiledLoadError) {
	var registry *Registry
	var loadErrors []CompiledLoadError
	for _, entry := range catalog {
		if !entry.DefaultEnabled {
			continue
		}
		if registry == nil {
			registry = NewRegistry(cwd)
		}
		if err := registry.Register(BuiltinPathPrefix+entry.Name, entry.Factory, WithHidden(entry.Hidden), WithReplaceable(entry.Replaceable)); err != nil {
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
	return registeredFlags(registry.Extensions())
}

// registeredFlags lists flags in registration order, the first extension to
// register a name winning.
func registeredFlags(extensions []*Extension) []Flag {
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
