package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/codingagent"
	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	"github.com/OrdalieTech/orb/codingagent/extensions/examples/permissiongate"
	"github.com/OrdalieTech/orb/codingagent/extensions/examples/pirate"
	"github.com/OrdalieTech/orb/codingagent/extensions/examples/statusline"
	extensionhost "github.com/OrdalieTech/orb/codingagent/extensions/host"
	"github.com/OrdalieTech/orb/codingagent/mcp"
	"github.com/OrdalieTech/orb/codingagent/modes"
	firstpartyplugins "github.com/OrdalieTech/orb/codingagent/plugins"
)

// otherDiagnostic wraps a plain warning string for the startup diagnostics
// stream; startupDiagnosticText flattens it back unchanged.
func otherDiagnostic(message string) modes.StartupDiagnostic {
	return modes.StartupDiagnostic{Kind: modes.StartupDiagnosticOther, Message: message}
}

func otherDiagnostics(messages []string) []modes.StartupDiagnostic {
	diagnostics := make([]modes.StartupDiagnostic, 0, len(messages))
	for _, message := range messages {
		diagnostics = append(diagnostics, otherDiagnostic(message))
	}
	return diagnostics
}

func hostLoadErrorDiagnostic(loadError extensionhost.LoadError) modes.StartupDiagnostic {
	return modes.StartupDiagnostic{Kind: modes.StartupDiagnosticExtension, Path: loadError.Path, Message: loadError.Error}
}

// hostDiagnostic keeps the host-reported message and path; the message alone is
// what print modes historically emitted for these.
func hostDiagnostic(diagnostic extensions.Diagnostic) modes.StartupDiagnostic {
	return modes.StartupDiagnostic{Kind: modes.StartupDiagnosticOther, Path: diagnostic.Path, Message: diagnostic.Message}
}

// startupDiagnosticText flattens a startup diagnostic to the string stderr and
// print modes have always emitted.
func startupDiagnosticText(diagnostic modes.StartupDiagnostic) string {
	switch diagnostic.Kind {
	case modes.StartupDiagnosticExtension:
		return "Extension error (" + diagnostic.Path + "): " + diagnostic.Message
	case modes.StartupDiagnosticCollision:
		return "name " + diagnostic.Message + " collision"
	default:
		return diagnostic.Message
	}
}

// Each runtime (re)load builds a fresh extension host; the previous child must
// be closed before it becomes unreachable.
// If cmd/orb ever hosts two concurrent live runtimes, move Close ownership to
// runtime disposal instead of this process-scoped slot.
var (
	extensionHostMu     sync.Mutex
	activeExtensionHost *extensionhost.Manager
)

var compiledExtensions = []extensions.CompiledExtension{
	{Name: "permission-gate", Factory: permissiongate.Extension},
	{Name: "pirate", Factory: pirate.Extension},
	{Name: "status-line", Factory: statusline.Extension},
}

func loadCompiledExtensions(cwd, agentDir string, args CLIArgs, settings *config.SettingsManager, packages *codingagent.ResolvedPaths) (*extensions.Registry, []modes.StartupDiagnostic) {
	catalog := append([]extensions.CompiledExtension(nil), compiledExtensions...)
	catalog = append(catalog, extensions.CompiledExtension{
		Name: "plugin-control", Factory: firstpartyplugins.Control(settings), Hidden: true, DefaultEnabled: true,
	})
	pluginCatalog := firstpartyplugins.Catalog(firstpartyplugins.Options{Settings: settings, AgentDir: agentDir})
	for _, name := range firstpartyplugins.Names() {
		catalog = append(catalog, extensions.CompiledExtension{Name: name, Factory: pluginCatalog[name]})
	}
	var diagnostics []modes.StartupDiagnostic
	// metadataOnly runs (e.g. --list-models) build the runtime purely to
	// enumerate models/providers; MCP servers contribute tools, not models, so
	// skip them rather than eagerly spawn and connect every configured server.
	if !args.NoExtensions && !args.metadataOnly {
		servers, warnings, err := mcp.ParseSettingsWithWarnings(map[string]any(settings.GetSettings()))
		diagnostics = append(diagnostics, otherDiagnostics(warnings)...)
		if err != nil {
			diagnostics = append(diagnostics, otherDiagnostic(err.Error()))
		}
		if len(servers) > 0 {
			manager := mcp.NewManager(cwd, servers)
			catalog = append(catalog, extensions.CompiledExtension{
				Name: "mcp", Factory: manager.Extension(), Hidden: true, DefaultEnabled: true,
			})
		}
	}
	overrides := settings.GetGoExtensions()
	if overrides == nil {
		overrides = make(map[string]bool)
	}
	// Built-in plugin gates are separate from goExtensions: control is always
	// available, while every actual plugin is off unless settings.plugins says on.
	overrides["plugin-control"] = true
	pluginSettings := settings.GetPlugins()
	for _, name := range firstpartyplugins.Names() {
		overrides[name] = pluginSettings[name]
	}
	registry, loadErrors := extensions.LoadCompiled(cwd, catalog, overrides, args.NoExtensions)
	for _, loadError := range loadErrors {
		diagnostics = append(diagnostics, otherDiagnostic(loadError.Error()))
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
				diagnostics = append(diagnostics, otherDiagnostic(err.Error()))
			} else {
				for _, resource := range resolved.Extensions {
					if resource.Enabled {
						explicitPaths = append(explicitPaths, resource.Path)
					}
				}
			}
		}
		options := extensionhost.DiscoveryOptions{
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
		if paths := extensionhost.Discover(options); len(paths) > 0 {
			if registry == nil {
				registry = extensions.NewRegistry(cwd)
			}
			// Metadata commands consume the write-through snapshot of the last
			// full load when its fingerprint matches; any miss falls through to
			// spawning the host exactly as before.
			if args.metadataOnly && !args.skipMetadataCache {
				if cached := extensionhost.LoadMetadataCache(extensionhost.MetadataCacheParams{
					AgentDir: agentDir, CWD: cwd, ProjectTrusted: settings.IsProjectTrusted(), Paths: paths,
				}); cached != nil {
					replaceActiveExtensionHost(nil)
					for _, diagnostic := range cached.Diagnostics {
						diagnostics = append(diagnostics, hostDiagnostic(diagnostic))
					}
					loadErrors := append(append([]extensionhost.LoadError(nil), cached.Errors...), cached.Register(registry)...)
					for _, loadError := range loadErrors {
						diagnostics = append(diagnostics, hostLoadErrorDiagnostic(loadError))
					}
					return registry, diagnostics
				}
			}
			manager := extensionhost.NewManager(extensionhost.Options{
				AgentDir:       agentDir,
				CWD:            cwd,
				ProjectTrusted: settings.IsProjectTrusted(),
				// The pre-trust probe load registers a provisional set the real
				// load overwrites moments later: it neither reads nor writes the
				// metadata snapshot.
				SkipMetadataCacheWrite: args.skipMetadataCache,
				Version:                version,
				Stderr:                 os.Stderr,
			})
			// Child agent sessions (agent_session_v1 / sdk_v1 resource reload)
			// run on the real NewAgentSession-backed runtime.
			manager.SetAgentSessionService(codingagent.NewExtensionAgentSessionService(
				codingagent.ExtensionAgentSessionServiceOptions{CWD: cwd, AgentDir: agentDir},
			))
			result := manager.RegisterInto(context.Background(), registry, paths)
			replaceActiveExtensionHost(manager)
			for _, diagnostic := range result.Diagnostics {
				diagnostics = append(diagnostics, hostDiagnostic(diagnostic))
			}
			for _, loadError := range result.Errors {
				diagnostics = append(diagnostics, hostLoadErrorDiagnostic(loadError))
			}
		} else {
			replaceActiveExtensionHost(nil)
		}
	}
	return registry, diagnostics
}

func replaceActiveExtensionHost(manager *extensionhost.Manager) {
	extensionHostMu.Lock()
	previousManager := activeExtensionHost
	activeExtensionHost = manager
	extensionHostMu.Unlock()
	if previousManager != nil && previousManager != manager {
		_ = previousManager.Close()
	}
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
// (host.Discover gates ProjectResolvedPackagePaths on ProjectTrusted).
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
func loadStartupExtensions(cwd string, args CLIArgs) (*extensions.Registry, []modes.StartupDiagnostic, *bool, error) {
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return nil, nil, nil, err
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(false))
	if err != nil {
		return nil, nil, nil, err
	}
	trust, err := resolveStartupProjectTrust(context.Background(), cwd, agentDir, args, settings)
	if err != nil {
		return nil, nil, nil, err
	}
	if trust.Undecided && !trust.Trusted {
		return trust.PreTrustRegistry, trust.Diagnostics, &trust.Trusted, nil
	}
	trustDiagnostics := trust.Diagnostics
	packageManager := codingagent.NewPackageManager(codingagent.PackageManagerOptions{
		CWD: cwd, AgentDir: agentDir, Settings: settings,
	})
	resolvedPaths, err := packageManager.Resolve(nil)
	if err != nil {
		return nil, nil, nil, err
	}
	registry, diagnostics := loadCompiledExtensions(cwd, agentDir, args, settings, resolvedPaths)
	return registry, append(trustDiagnostics, diagnostics...), &trust.Trusted, nil
}

// projectTrustResolution is the outcome of the trust decision plus the pre-trust
// extension set that made it: when trust is refused, that set is final because
// project-scoped resources stay out.
type projectTrustResolution struct {
	Trusted          bool
	Undecided        bool
	PreTrustRegistry *extensions.Registry
	Diagnostics      []modes.StartupDiagnostic
}

// resolveStartupProjectTrust decides project trust the way upstream does: when
// trust is genuinely in question it loads the pre-trust (global) extension set
// first so a project_trust handler is consulted ahead of the trust store and the
// interactive prompt (main.ts resolveProjectTrust wiring → project-trust.ts
// emitProjectTrustEvent). It leaves settings carrying the decision.
func resolveStartupProjectTrust(ctx context.Context, cwd, agentDir string, args CLIArgs, settings *config.SettingsManager) (projectTrustResolution, error) {
	resolution := projectTrustResolution{Undecided: args.ProjectTrusted == nil && config.HasTrustRequiringProjectResources(cwd)}
	var preTrustDiagnostics []modes.StartupDiagnostic
	var trustRunner *extensions.Runner
	if resolution.Undecided {
		untrustedPaths, err := codingagent.NewPackageManager(codingagent.PackageManagerOptions{
			CWD: cwd, AgentDir: agentDir, Settings: settings,
		}).Resolve(nil)
		if err != nil {
			return projectTrustResolution{}, err
		}
		preTrustArgs := args
		preTrustArgs.skipMetadataCache = true
		resolution.PreTrustRegistry, preTrustDiagnostics = loadCompiledExtensions(cwd, agentDir, preTrustArgs, settings, untrustedPaths)
		trustRunner = extensions.NewRunner(resolution.PreTrustRegistry, extensions.RunnerOptions{CWD: cwd})
	}
	trusted, err := codingagent.ResolveProjectTrusted(ctx, codingagent.ResolveProjectTrustedOptions{
		CWD:                 cwd,
		TrustStore:          config.NewProjectTrustStore(agentDir),
		TrustOverride:       args.ProjectTrusted,
		DefaultProjectTrust: settings.GetDefaultProjectTrust(),
		Runner:              trustRunner,
		OnExtensionError: func(message string) {
			resolution.Diagnostics = append(resolution.Diagnostics, otherDiagnostic(message))
		},
	})
	if err != nil {
		return projectTrustResolution{}, err
	}
	resolution.Trusted = trusted
	// On the trusted path the post-trust reload re-reports the same global
	// extension diagnostics; keep the pre-trust copies only when the refused
	// pre-trust registry is the final one.
	if !trusted {
		resolution.Diagnostics = append(resolution.Diagnostics, preTrustDiagnostics...)
	}
	settings.SetProjectTrusted(trusted)
	return resolution, nil
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
