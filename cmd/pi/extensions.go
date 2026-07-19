package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/extensions/examples/permissiongate"
	"github.com/OrdalieTech/pi-go/codingagent/extensions/examples/pirate"
	"github.com/OrdalieTech/pi-go/codingagent/extensions/examples/statusline"
	"github.com/OrdalieTech/pi-go/codingagent/extensions/jsbridge"
	"github.com/OrdalieTech/pi-go/codingagent/mcp"
)

var compiledExtensions = []extensions.CompiledExtension{
	{Name: "permission-gate", Factory: permissiongate.Extension},
	{Name: "pirate", Factory: pirate.Extension},
	{Name: "status-line", Factory: statusline.Extension},
}

func loadCompiledExtensions(cwd string, args CLIArgs, settings *config.SettingsManager) (*extensions.Registry, []string) {
	catalog := append([]extensions.CompiledExtension(nil), compiledExtensions...)
	var diagnostics []string
	if !args.NoExtensions {
		servers, err := mcp.ParseSettings(map[string]any(settings.GetSettings()))
		if err != nil {
			diagnostics = append(diagnostics, err.Error())
		} else if len(servers) > 0 {
			manager := mcp.NewManager(cwd, servers)
			catalog = append(catalog, extensions.CompiledExtension{
				Name: "mcp", Factory: manager.Extension(), Hidden: true, DefaultEnabled: true,
			})
		}
	}
	registry, loadErrors := extensions.LoadCompiled(cwd, catalog, settings.GetGoExtensions(), args.NoExtensions)
	for _, loadError := range loadErrors {
		diagnostics = append(diagnostics, loadError.Error())
	}
	if !args.NoExtensions {
		options := jsbridge.DiscoveryOptions{
			CWD:                    cwd,
			ProjectTrusted:         settings.IsProjectTrusted(),
			ConfiguredPaths:        settings.GetGlobalExtensionPaths(),
			ProjectConfiguredPaths: settings.GetProjectExtensionPaths(),
			ExplicitPaths:          args.Extensions,
		}
		if paths := jsbridge.Discover(options); len(paths) > 0 {
			if registry == nil {
				registry = extensions.NewRegistry(cwd)
			}
			result := jsbridge.NewLoader(options).RegisterInto(context.Background(), registry)
			for _, loadError := range result.Errors {
				diagnostics = append(diagnostics, fmt.Sprintf("Extension error (%s): %s", loadError.Path, loadError.Error))
			}
		}
	}
	return registry, diagnostics
}

func applyExtensionFlags(registry *extensions.Registry, flags []CLIUnknownFlag) []string {
	registered := make(map[string]extensions.Flag)
	if registry != nil {
		for _, flag := range registry.RegisteredFlags() {
			if _, exists := registered[flag.Name]; !exists {
				registered[flag.Name] = flag
			}
		}
	}
	var unknown []string
	var diagnostics []string
	for _, supplied := range flags {
		flag, exists := registered[supplied.Name]
		if !exists {
			unknown = append(unknown, supplied.Name)
			continue
		}
		if flag.Type == extensions.FlagBoolean {
			registry.SetFlagValue(supplied.Name, true)
			continue
		}
		if supplied.Value != nil {
			registry.SetFlagValue(supplied.Name, *supplied.Value)
			continue
		}
		diagnostics = append(diagnostics, fmt.Sprintf("Extension flag \"--%s\" requires a value", supplied.Name))
	}
	if len(unknown) > 0 {
		option := "option"
		if len(unknown) > 1 {
			option = "options"
		}
		diagnostics = append(diagnostics, "Unknown "+option+": --"+strings.Join(unknown, ", --"))
	}
	return diagnostics
}

func extensionHelpText(registry *extensions.Registry) string {
	if registry == nil {
		return helpText
	}
	flags := registry.RegisteredFlags()
	if len(flags) == 0 {
		return helpText
	}
	var section strings.Builder
	section.WriteString("\nExtension CLI Flags:\n")
	for _, flag := range flags {
		name := "  --" + flag.Name
		if flag.Type == extensions.FlagString {
			name += " <value>"
		}
		description := flag.Description
		if description == "" {
			description = "Registered by " + flag.ExtensionPath
		}
		fmt.Fprintf(&section, "%-30s%s\n", name, description)
	}
	return strings.TrimSuffix(helpText, "\n") + section.String()
}
