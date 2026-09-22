package subagents

import (
	"fmt"

	"github.com/OrdalieTech/orb/agent/config"
)

// ToggleExternalCLI flips one external CLI, preserving its command (the
// object form of plugins.subagents.external), or configures a detected CLI
// with its known invocation.
func ToggleExternalCLI(settings *config.SettingsManager, name string) error {
	// The write below lands in the global scope; when project settings define
	// the plugin they shadow that whole object, so the toggle would silently
	// misfire — refuse with the remedy instead.
	if settings.ProjectDefinesPlugin("subagents") {
		return fmt.Errorf("plugins: subagents is configured in project settings — edit %s/settings.json", config.ConfigDirName)
	}
	entries, err := ParseExternalEntries(settings.GlobalPluginSettings("subagents"))
	if err != nil {
		return err
	}
	if _, exists := entries[name]; !exists {
		command := ""
		for _, cli := range KnownCLIs() {
			if cli.Name == name {
				command = cli.Command
			}
		}
		if command == "" {
			return fmt.Errorf("plugins: unknown external CLI %q", name)
		}
		if entries == nil {
			entries = map[string]ExternalEntry{}
		}
		entries[name] = ExternalEntry{Command: command, Enabled: true}
	} else {
		entry := entries[name]
		entry.Enabled = !entry.Enabled
		entries[name] = entry
	}
	external := make(map[string]any, len(entries))
	for entryName, entry := range entries {
		if entry.Enabled {
			external[entryName] = entry.Command
		} else {
			external[entryName] = map[string]any{"command": entry.Command, "enabled": false}
		}
	}
	settings.SetPluginSetting("subagents", "external", external)
	return nil
}
