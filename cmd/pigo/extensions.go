package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/extensions/examples/permissiongate"
	"github.com/OrdalieTech/pigo/codingagent/extensions/examples/pirate"
	"github.com/OrdalieTech/pigo/codingagent/extensions/examples/statusline"
	"github.com/OrdalieTech/pigo/codingagent/extensions/jsbridge"
	"github.com/OrdalieTech/pigo/codingagent/mcp"
)

// Each runtime (re)load builds a fresh jsbridge loader; the previous one's
// goroutine-backed VMs must be closed or /reload leaks them (~16MB each).
// If cmd/pigo ever hosts two concurrent live runtimes, move Close ownership to
// runtime disposal instead of this process-scoped slot.
var (
	jsLoaderMu     sync.Mutex
	activeJSLoader *jsbridge.Loader
)

var compiledExtensions = []extensions.CompiledExtension{
	{Name: "permission-gate", Factory: permissiongate.Extension},
	{Name: "pirate", Factory: pirate.Extension},
	{Name: "status-line", Factory: statusline.Extension},
}

func loadCompiledExtensions(cwd, agentDir string, args CLIArgs, settings *config.SettingsManager, packages *codingagent.ResolvedPaths) (*extensions.Registry, []string) {
	catalog := append([]extensions.CompiledExtension(nil), compiledExtensions...)
	var diagnostics []string
	// metadataOnly runs (e.g. --list-models) build the runtime purely to
	// enumerate models/providers; MCP servers contribute tools, not models, so
	// skip them rather than eagerly spawn and connect every configured server.
	if !args.NoExtensions && !args.metadataOnly {
		servers, warnings, err := mcp.ParseSettingsWithWarnings(map[string]any(settings.GetSettings()))
		diagnostics = append(diagnostics, warnings...)
		if err != nil {
			diagnostics = append(diagnostics, err.Error())
		}
		if len(servers) > 0 {
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
	if len(args.Extensions) > 0 || !args.NoExtensions {
		explicitPaths := make([]string, 0, len(args.Extensions))
		var sourceSpecs []string
		for _, extension := range args.Extensions {
			if isPackageSourceSpec(extension) {
				sourceSpecs = append(sourceSpecs, extension)
			} else {
				explicitPaths = append(explicitPaths, extension)
			}
		}
		if len(sourceSpecs) > 0 {
			// Upstream resource-loader.ts:355 resolves -e package specs through
			// packageManager.resolveExtensionSources with temporary install semantics.
			manager := codingagent.NewPackageManager(codingagent.PackageManagerOptions{
				CWD: cwd, AgentDir: agentDir, Settings: settings,
			})
			resolved, err := manager.ResolveExtensionSources(sourceSpecs, false, true)
			if err != nil {
				diagnostics = append(diagnostics, err.Error())
			} else {
				for _, resource := range resolved.Extensions {
					if resource.Enabled {
						explicitPaths = append(explicitPaths, resource.Path)
					}
				}
			}
		}
		options := jsbridge.DiscoveryOptions{
			CWD:                    cwd,
			AgentDir:               agentDir,
			ProjectTrusted:         settings.IsProjectTrusted(),
			NoDiscovery:            args.NoExtensions,
			ConfiguredPaths:        settings.GetGlobalExtensionPaths(),
			ProjectConfiguredPaths: settings.GetProjectExtensionPaths(),
			ExplicitPaths:          explicitPaths,
		}
		if packages != nil {
			options.ResolvedPackagePaths, options.ProjectResolvedPackagePaths = packageExtensionPaths(packages.Extensions)
		}
		if paths := jsbridge.Discover(options); len(paths) > 0 {
			if registry == nil {
				registry = extensions.NewRegistry(cwd)
			}
			loader := jsbridge.NewLoader(options)
			jsLoaderMu.Lock()
			previous := activeJSLoader
			activeJSLoader = loader
			jsLoaderMu.Unlock()
			if previous != nil {
				previous.Close()
			}
			result := loader.RegisterInto(context.Background(), registry)
			for _, loadError := range result.Errors {
				diagnostics = append(diagnostics, fmt.Sprintf("Extension error (%s): %s", loadError.Path, loadError.Error))
			}
		}
	}
	return registry, diagnostics
}

// isPackageSourceSpec mirrors upstream isLocalPath: known package/URL prefixes
// are package sources, everything else is a local path.
func isPackageSourceSpec(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range [...]string{"npm:", "git:", "github:", "http:", "https:", "ssh:"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// packageExtensionPaths splits enabled package-provided extension entry points
// by scope; project-scope entries stay invisible until the project is trusted
// (jsbridge.Discover gates ProjectResolvedPackagePaths on ProjectTrusted).
func packageExtensionPaths(resources []codingagent.ResolvedResource) (user, project []string) {
	for _, resource := range resources {
		if !resource.Enabled || resource.Metadata.Origin != "package" {
			continue
		}
		if resource.Metadata.Scope == "project" {
			project = append(project, resource.Path)
		} else {
			user = append(user, resource.Path)
		}
	}
	return user, project
}

// loadStartupExtensions loads the discovered extension set for runtime-metadata
// paths (--help, unknown-flag validation) with the same project-trust gating as
// createRuntimeInputs: untrusted project settings contribute nothing, so no
// project-configured MCP server or extension can run before trust is granted.
func loadStartupExtensions(cwd string, args CLIArgs) (*extensions.Registry, []string, error) {
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return nil, nil, err
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(false))
	if err != nil {
		return nil, nil, err
	}
	projectTrusted, err := codingagent.ResolveProjectTrusted(context.Background(), codingagent.ResolveProjectTrustedOptions{
		CWD:                 cwd,
		TrustStore:          config.NewProjectTrustStore(agentDir),
		TrustOverride:       args.ProjectTrusted,
		DefaultProjectTrust: settings.GetDefaultProjectTrust(),
	})
	if err != nil {
		return nil, nil, err
	}
	settings.SetProjectTrusted(projectTrusted)
	packageManager := codingagent.NewPackageManager(codingagent.PackageManagerOptions{
		CWD: cwd, AgentDir: agentDir, Settings: settings,
	})
	resolvedPaths, err := packageManager.Resolve(nil)
	if err != nil {
		return nil, nil, err
	}
	registry, diagnostics := loadCompiledExtensions(cwd, agentDir, args, settings, resolvedPaths)
	return registry, diagnostics, nil
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
