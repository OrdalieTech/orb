package config

import (
	"encoding/json"
	"fmt"
)

// Package settings surface of upstream settings-manager.ts.

// PackageSource mirrors upstream's string-or-object package entry. Nil slices
// mean the key is absent (load all of that type); empty slices mean an
// explicit [] (load none).
type PackageSource struct {
	Source     string
	Autoload   *bool
	Extensions []string
	Skills     []string
	Prompts    []string
	Themes     []string
	IsObject   bool
}

func parsePackageFilterList(value any, exists bool) []string {
	if !exists {
		return nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func parsePackageSource(value any) (PackageSource, bool) {
	if settings, ok := value.(Settings); ok {
		value = map[string]any(settings)
	}
	switch typed := value.(type) {
	case string:
		return PackageSource{Source: typed}, true
	case map[string]any:
		source, _ := typed["source"].(string)
		if source == "" {
			return PackageSource{}, false
		}
		parsed := PackageSource{Source: source, IsObject: true}
		if autoload, ok := typed["autoload"].(bool); ok {
			parsed.Autoload = boolPtr(autoload)
		}
		for _, key := range [...]string{"extensions", "skills", "prompts", "themes"} {
			raw, exists := typed[key]
			list := parsePackageFilterList(raw, exists)
			switch key {
			case "extensions":
				parsed.Extensions = list
			case "skills":
				parsed.Skills = list
			case "prompts":
				parsed.Prompts = list
			case "themes":
				parsed.Themes = list
			}
		}
		return parsed, true
	default:
		return PackageSource{}, false
	}
}

// PackageSourcesFrom reads the "packages" array from a settings document.
func PackageSourcesFrom(settings Settings) []PackageSource {
	raw, exists := settings["packages"]
	if !exists {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil
	}
	sources := make([]PackageSource, 0, len(values))
	for _, value := range values {
		if parsed, ok := parsePackageSource(value); ok {
			sources = append(sources, parsed)
		}
	}
	return sources
}

// WithSource returns a copy pointing at newSource, preserving filters.
func (source PackageSource) WithSource(newSource string) PackageSource {
	source.Source = newSource
	return source
}

// settingsValue converts to the map/string shape stored in Settings, with
// object keys in upstream declaration order when re-encoded for disk.
func (source PackageSource) settingsValue() any {
	if !source.IsObject {
		return source.Source
	}
	object := map[string]any{"source": source.Source}
	if source.Autoload != nil {
		object["autoload"] = *source.Autoload
	}
	assign := func(key string, list []string) {
		if list == nil {
			return
		}
		values := make([]any, len(list))
		for index, item := range list {
			values[index] = item
		}
		object[key] = values
	}
	assign("extensions", source.Extensions)
	assign("skills", source.Skills)
	assign("prompts", source.Prompts)
	assign("themes", source.Themes)
	return object
}

// encodePackageSources produces the raw JSON for the packages array with
// object keys in upstream declaration order (source, autoload, filters).
func encodePackageSources(sources []PackageSource) (json.RawMessage, error) {
	entries := make([]json.RawMessage, 0, len(sources))
	for _, source := range sources {
		if !source.IsObject {
			encoded, err := encodeSetting(source.Source)
			if err != nil {
				return nil, err
			}
			entries = append(entries, encoded)
			continue
		}
		object := settingsObject{}
		appendMember := func(name string, value any) error {
			encoded, err := encodeSetting(value)
			if err != nil {
				return err
			}
			object = object.set(name, encoded)
			return nil
		}
		if err := appendMember("source", source.Source); err != nil {
			return nil, err
		}
		if source.Autoload != nil {
			if err := appendMember("autoload", *source.Autoload); err != nil {
				return nil, err
			}
		}
		for _, filter := range [...]struct {
			name string
			list []string
		}{{"extensions", source.Extensions}, {"skills", source.Skills}, {"prompts", source.Prompts}, {"themes", source.Themes}} {
			if filter.list == nil {
				continue
			}
			if err := appendMember(filter.name, filter.list); err != nil {
				return nil, err
			}
		}
		encoded, err := object.marshalIndented()
		if err != nil {
			return nil, err
		}
		entries = append(entries, encoded)
	}
	var buffer []byte
	buffer = append(buffer, '[')
	for index, entry := range entries {
		if index > 0 {
			buffer = append(buffer, ',')
		}
		buffer = append(buffer, entry...)
	}
	buffer = append(buffer, ']')
	return buffer, nil
}

func (manager *SettingsManager) GetGlobalPackages() []PackageSource {
	return PackageSourcesFrom(manager.GetGlobalSettings())
}

func (manager *SettingsManager) GetProjectPackages() []PackageSource {
	return PackageSourcesFrom(manager.GetProjectSettings())
}

func packageSourcesSettingsValue(sources []PackageSource) []any {
	values := make([]any, len(sources))
	for index, source := range sources {
		values[index] = source.settingsValue()
	}
	return values
}

// SetPackages writes the global packages array.
func (manager *SettingsManager) SetPackages(sources []PackageSource) error {
	raw, err := encodePackageSources(sources)
	if err != nil {
		return err
	}
	manager.setGlobalValues(settingsMember{name: "packages", value: raw})
	return nil
}

// SetProjectPackages writes the project packages array; it refuses when the
// project is untrusted (upstream assertProjectTrustedForWrite).
func (manager *SettingsManager) SetProjectPackages(sources []PackageSource) error {
	raw, err := encodePackageSources(sources)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.projectTrusted {
		return fmt.Errorf("Project is not trusted; refusing to write project settings") //nolint:staticcheck // Upstream error text is observable.
	}
	manager.project["packages"] = cloneValue(any(packageSourcesSettingsValue(sources)))
	manager.effective = mergeSettings(manager.global, manager.project)
	if manager.projectLoadError {
		return nil
	}
	if err := writeGlobalSettings(manager.projectPath, settingsObject{{name: "packages", value: raw}}, "", "", nil); err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: ProjectSettings, Err: err})
	}
	return nil
}

func (manager *SettingsManager) GetGlobalExtensionPaths() []string {
	return settingsStringSlice(manager.GetGlobalSettings(), "extensions")
}

func (manager *SettingsManager) GetProjectExtensionPaths() []string {
	return settingsStringSlice(manager.GetProjectSettings(), "extensions")
}

func (manager *SettingsManager) GetGlobalThemePaths() []string {
	return settingsStringSlice(manager.GetGlobalSettings(), "themes")
}

func (manager *SettingsManager) GetProjectThemePaths() []string {
	return settingsStringSlice(manager.GetProjectSettings(), "themes")
}

func (manager *SettingsManager) setGlobalResourcePaths(key string, paths []string) error {
	paths = append([]string{}, paths...)
	manager.setGlobalValues(settingMember(key, paths))
	return nil
}

func (manager *SettingsManager) setProjectResourcePaths(key string, paths []string) error {
	paths = append([]string{}, paths...)
	raw, err := encodeSetting(paths)
	if err != nil {
		return err
	}
	values := make([]any, len(paths))
	for index, path := range paths {
		values[index] = path
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.projectTrusted {
		return fmt.Errorf("Project is not trusted; refusing to write project settings") //nolint:staticcheck // Upstream error text is observable.
	}
	manager.project[key] = values
	manager.effective = mergeSettings(manager.global, manager.project)
	if manager.projectLoadError {
		return nil
	}
	if err := writeGlobalSettings(manager.projectPath, settingsObject{{name: key, value: raw}}, "", "", nil); err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: ProjectSettings, Err: err})
	}
	return nil
}

func (manager *SettingsManager) SetExtensionPaths(paths []string) error {
	return manager.setGlobalResourcePaths("extensions", paths)
}

func (manager *SettingsManager) SetProjectExtensionPaths(paths []string) error {
	return manager.setProjectResourcePaths("extensions", paths)
}

func (manager *SettingsManager) SetSkillPaths(paths []string) error {
	return manager.setGlobalResourcePaths("skills", paths)
}

func (manager *SettingsManager) SetProjectSkillPaths(paths []string) error {
	return manager.setProjectResourcePaths("skills", paths)
}

func (manager *SettingsManager) SetPromptTemplatePaths(paths []string) error {
	return manager.setGlobalResourcePaths("prompts", paths)
}

func (manager *SettingsManager) SetProjectPromptTemplatePaths(paths []string) error {
	return manager.setProjectResourcePaths("prompts", paths)
}

func (manager *SettingsManager) SetThemePaths(paths []string) error {
	return manager.setGlobalResourcePaths("themes", paths)
}

func (manager *SettingsManager) SetProjectThemePaths(paths []string) error {
	return manager.setProjectResourcePaths("themes", paths)
}

// GetDefaultProjectTrust returns "always", "never", or "ask" (global-only).
func (manager *SettingsManager) GetDefaultProjectTrust() string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if value, ok := manager.global["defaultProjectTrust"].(string); ok && (value == "always" || value == "never") {
		return value
	}
	return "ask"
}
