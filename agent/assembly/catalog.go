package assembly

import (
	"context"
	"fmt"
	"net/http"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/permissions"
	"github.com/OrdalieTech/orb/plugins/questions"
	"github.com/OrdalieTech/orb/plugins/subagents"
	"github.com/OrdalieTech/orb/plugins/tasks"
	"github.com/OrdalieTech/orb/plugins/usage"
	"github.com/OrdalieTech/orb/plugins/usage/footer"
	"github.com/OrdalieTech/orb/plugins/websearch"
)

// CatalogOptions supplies explicit runtime seams so bundled plugins remain instance-scoped.
type CatalogOptions struct {
	UsageCache       *usage.Cache
	Memory           memorysdk.Store
	Bridge           extensions.Factory
	BridgeAgentCalls extensions.Factory
	StreamFn         engine.StreamFn
	HTTPClient       *http.Client
	Settings         *config.SettingsManager
	Policy           *permissions.Policy
	AgentDir         string
	// ClaudeSessions is host-supplied: it runs processes the SDK layer never owns.
	ClaudeSessions extensions.Factory
}

var names = []string{"tasks", "questions", "websearch", "subagents", "permissions", "memory", "claude-sessions", "provider-usage", "bridge", "bridge-agent-calls"}

var descriptions = map[string]string{
	"questions":          "Ask the user questions with choices and custom answers",
	"bridge":             "Pair devices and control explicitly shared Orb instances",
	"bridge-agent-calls": "Allow granted agent-initiated calls through a Bridge attachment",
	"tasks":              "Live session task list and todo tool",
	"websearch":          "Web search and readable page fetching",
	"subagents":          "Single or parallel child agents, including configured external CLIs",
	"permissions":        "Tool-call permissions, explicit approvals and optional audit mode",
	"memory":             "Bounded persistent remember, recall, replace, and forget tools",
	"claude-sessions":    "Claude models and accounts through Claude Code and the official Agent SDK",
	"provider-usage":     "Remaining Codex and OpenCode Go quota in the footer",
}

// Names returns the stable first-party plugin order.
func Names() []string { return append([]string(nil), names...) }

// Description returns the one-line description used by the CLI and TUI.
func Description(name string) string { return descriptions[name] }

// Catalog returns fresh extension factories for embedders to select per instance.
func Catalog(option ...CatalogOptions) map[string]extensions.Factory {
	if len(option) > 1 {
		panic("plugins: Catalog accepts at most one Options value")
	}
	var options CatalogOptions
	if len(option) == 1 {
		options = option[0]
	}
	policy, inheritPolicy := options.Policy, options.Policy
	if policy == nil && options.Settings != nil && options.Settings.GetPlugins()["permissions"] {
		var err error
		policy, err = permissions.FromSettings(options.Settings.GetPluginSettings("permissions"))
		if err != nil {
			policy = &permissions.Policy{Mode: "enforce", Guards: []func(context.Context, permissions.ToolCallInfo) string{func(context.Context, permissions.ToolCallInfo) string { return err.Error() }}}
		}
		inheritPolicy = policy
	}
	if policy == nil {
		policy = &permissions.Policy{}
	}
	if options.Bridge == nil {
		options.Bridge = func(extensions.API) error { return fmt.Errorf("bridge requires host assembly") }
	}
	if options.ClaudeSessions == nil {
		options.ClaudeSessions = func(extensions.API) error { return fmt.Errorf("claude sessions require host assembly") }
	}
	if options.BridgeAgentCalls == nil {
		options.BridgeAgentCalls = func(extensions.API) error { return fmt.Errorf("bridge agent calls require host assembly") }
	}
	return map[string]extensions.Factory{
		"bridge": options.Bridge, "bridge-agent-calls": options.BridgeAgentCalls, "claude-sessions": options.ClaudeSessions,
		"questions":      questions.Extension(),
		"tasks":          tasks.Extension(),
		"websearch":      websearch.Extension(options.HTTPClient),
		"subagents":      subagents.Extension(options.StreamFn, inheritPolicy, options.Settings),
		"permissions":    permissions.Extension(policy, options.Settings, nil),
		"memory":         memoryExtension(options.Memory, options.AgentDir),
		"provider-usage": footer.Extension(usage.Client{HTTPClient: options.HTTPClient, Cache: options.UsageCache}),
	}
}

// Control registers /plugins independently of the default-off plugin catalog.
// cwd and agentDir feed the in-window package installer; empty values disable
// it (SDK embedders composing their own assemblies).
func Control(cwd, agentDir string, settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		api.RegisterCommand("plugins", extensions.Command{
			SettingsLabel: "Plugins",
			Description:   "Enable or disable bundled plugins",
			Handler: func(ctx context.Context, _ string, command extensions.CommandContext) error {
				if !command.HasUI() {
					return fmt.Errorf("/plugins requires interactive mode")
				}
				if command.Mode() == extensions.ModeTUI {
					return pluginsWindow(ctx, command, cwd, agentDir, settings)
				}
				return legacyPluginsSelect(ctx, command, settings)
			},
		})
		return nil
	}
}
