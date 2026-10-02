// Package assembly enumerates the compiled composition of the orb product —
// compiled extensions, first-party plugins, and MCP — as ordered rows with
// stable ids, resolves their enablement from settings, and loads the result.
// Enumeration and boot share this one implementation, so what a dump prints
// is exactly what boots (P3). The package holds no state: N assemblies with
// N settings managers coexist in one process.
package assembly

import (
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/jobs"
	"github.com/OrdalieTech/orb/plugins/mcp"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/permissions"
	"github.com/OrdalieTech/orb/plugins/usage"
)

// Source records which mechanism contributes a row.
type Source string

const (
	SourceCompiled Source = "compiled"
	SourcePlugin   Source = "plugin"
	SourceMCP      Source = "mcp"
)

// Row is one composable unit of the product, addressable by its stable ID.
// Row ids are a public contract (P3): renaming one is a breaking change.
type Row struct {
	ID          string
	Description string
	Source      Source
	Hidden      bool
	// Replaceable rows step aside for an extension registering one of their
	// tool, command, or flag names.
	Replaceable    bool
	DefaultEnabled bool
	Factory        extensions.Factory
}

// Options are the explicit inputs of one assembly; nothing is read from the
// environment.
type Options struct {
	UsageCache *usage.Cache
	Memory     memory.Store
	// BridgeManagement exposes the host-supplied settings page before service activation.
	BridgeManagement bool
	Bridge           extensions.Factory
	BridgeAgentCalls extensions.Factory
	ClaudeSessions   extensions.Factory
	Bash             jobs.Bash
	CWD              string
	AgentDir         string
	Settings         *config.SettingsManager
	Policy           *permissions.Policy
	// Compiled rows supplied by the assembly owner (cmd/orb's compiled
	// extensions, or an embedder's own), first in boot order.
	Compiled []extensions.CompiledExtension
	// MCP includes the mcp.json servers as a hidden row. Leave it off for
	// metadata-only runs so no configured server is spawned.
	MCP bool
}

// Rows enumerates the composition in boot order: caller compiled rows,
// plugin-control, the first-party plugin catalog, tool_search, then MCP.
func Rows(options Options) []Row {
	names := Names()
	rows := make([]Row, 0, len(options.Compiled)+len(names)+2)
	for _, entry := range options.Compiled {
		rows = append(rows, Row{
			ID: entry.Name, Source: SourceCompiled,
			Hidden: entry.Hidden, DefaultEnabled: entry.DefaultEnabled, Factory: entry.Factory,
		})
	}
	rows = append(rows, Row{
		ID: "plugin-control", Description: "Enable or disable bundled plugins (/plugins)",
		Source: SourcePlugin, Hidden: true, DefaultEnabled: true,
		Factory: Control(options.CWD, options.AgentDir, options.Settings),
	})
	catalog := Catalog(CatalogOptions{UsageCache: options.UsageCache, Memory: options.Memory, Settings: options.Settings, Policy: options.Policy, AgentDir: options.AgentDir, Bridge: options.Bridge, BridgeAgentCalls: options.BridgeAgentCalls, ClaudeSessions: options.ClaudeSessions, Bash: options.Bash})
	for _, name := range names {
		rows = append(rows, Row{
			ID: name, Description: Description(name),
			Source: SourcePlugin, Factory: catalog[name],
			Hidden:         name == "bridge" || name == "bridge-agent-calls" || name == "provider-usage",
			DefaultEnabled: name == "bridge" && options.BridgeManagement && options.Bridge != nil,
		})
	}
	rows = append(rows, Row{
		ID: "tool-search", Description: "tool_search: load deferred tools such as MCP servers'",
		Source: SourceCompiled, Hidden: true, Replaceable: true, DefaultEnabled: true,
		Factory: mcp.ToolSearchExtension(),
	})
	if options.MCP {
		rows = append(rows, Row{
			ID: "mcp", Description: "MCP servers from mcp.json",
			Source: SourceMCP, Hidden: true, Replaceable: true, DefaultEnabled: true,
			Factory: mcp.Extension(options.AgentDir),
		})
	}
	return rows
}

// Resolved is a Row plus its effective enablement and the layer that decided it.
type Resolved struct {
	Row
	Enabled   bool
	DecidedBy string // "default", "goExtensions", "plugins", "always", "--no-extensions", "--auto", "-e"
}

// Resolve applies the settings gates: DefaultEnabled, then the goExtensions
// override, then — for plugin rows — the plugins gates (plugin-control is
// always on, as is host-supplied Bridge management; capability execution stays opt-in).
// disableAll (--no-extensions) turns everything off.
func Resolve(rows []Row, settings *config.SettingsManager, disableAll bool) []Resolved {
	goOverrides := settings.GetGoExtensions()
	pluginGates := settings.GetPlugins()
	resolved := make([]Resolved, 0, len(rows))
	for _, row := range rows {
		entry := Resolved{Row: row, Enabled: row.DefaultEnabled, DecidedBy: "default"}
		if override, exists := goOverrides[row.ID]; exists {
			entry.Enabled, entry.DecidedBy = override, "goExtensions"
		}
		if row.Source == SourcePlugin {
			if row.ID == "plugin-control" || (row.ID == "bridge" && row.DefaultEnabled) {
				entry.Enabled, entry.DecidedBy = true, "always"
			} else if gate, exists := pluginGates[row.ID]; exists {
				entry.Enabled, entry.DecidedBy = gate, "plugins"
			} else {
				// Off because plugins default off, not because settings said so
				// (a stray goExtensions override is clobbered here, as at boot).
				entry.Enabled, entry.DecidedBy = false, "default"
			}
		}
		if disableAll {
			entry.Enabled, entry.DecidedBy = false, "--no-extensions"
		}
		resolved = append(resolved, entry)
	}
	return resolved
}

// Load registers the enabled rows into a Registry, preserving
// extensions.LoadCompiled semantics: "builtin:id" registration paths, a nil
// registry when nothing is enabled, and identical error text.
func Load(cwd string, resolved []Resolved) (*extensions.Registry, []extensions.CompiledLoadError) {
	catalog := make([]extensions.CompiledExtension, len(resolved))
	for index, row := range resolved {
		catalog[index] = extensions.CompiledExtension{
			Name: row.ID, Factory: row.Factory, Hidden: row.Hidden, Replaceable: row.Replaceable, DefaultEnabled: row.Enabled,
		}
	}
	return extensions.LoadCompiled(cwd, catalog)
}
