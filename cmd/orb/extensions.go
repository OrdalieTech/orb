package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"encoding/json"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/assembly"
	agentbridge "github.com/OrdalieTech/orb/agent/bridge"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	extensionhost "github.com/OrdalieTech/orb/agent/extensions/host"
	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
	"github.com/OrdalieTech/orb/plugins/codexsessions"
	herdrext "github.com/OrdalieTech/orb/plugins/herdr"
	"github.com/OrdalieTech/orb/plugins/permissions"
)

// otherDiagnostic wraps a plain warning string for the startup diagnostics
// stream; startupDiagnosticText flattens it back unchanged.
func otherDiagnostic(message string) modes.StartupDiagnostic {
	return modes.StartupDiagnostic{Kind: modes.StartupDiagnosticOther, Message: message}
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

var compiledExtensions []extensions.CompiledExtension

func compiledExtensionsForEnvironment(getenv func(string) string) []extensions.CompiledExtension {
	rows := append([]extensions.CompiledExtension(nil), compiledExtensions...)
	if getenv("HERDR_ENV") != "1" || getenv("HERDR_BIN_PATH") == "" || getenv("HERDR_PANE_ID") == "" {
		return rows
	}
	return append(rows, extensions.CompiledExtension{
		Name: "herdr", Hidden: true, DefaultEnabled: true,
		Factory: herdrext.Extension(herdrBinary(getenv("HERDR_BIN_PATH")), getenv("HERDR_PANE_ID")),
	})
}

// herdrBinary is the Herdr client to report through. A Herdr server updated
// in place still names its replaced executable, which Linux reports with a
// " (deleted)" suffix; the new binary sits at the same path, and failing that
// the one on PATH answers the same socket.
func herdrBinary(path string) string {
	path = strings.TrimSuffix(path, " (deleted)")
	if _, err := os.Stat(path); err != nil {
		if found, lookErr := exec.LookPath("herdr"); lookErr == nil {
			return found
		}
	}
	return path
}

func loadCompiledExtensions(cwd, agentDir string, args CLIArgs, settings *config.SettingsManager, packages *agent.ResolvedPaths) (*extensions.Registry, []modes.StartupDiagnostic) {
	var policy *permissions.Policy
	if args.Auto {
		var err error
		policy, err = permissions.FromSettings(settings.GetPluginSettings("permissions"))
		if err != nil {
			policy = &permissions.Policy{Guards: []func(context.Context, permissions.ToolCallInfo) string{
				func(context.Context, permissions.ToolCallInfo) string { return err.Error() },
			}}
		}
		policy.SetMode("auto")
	}
	// metadataOnly runs (e.g. --list-models) build the runtime purely to
	// enumerate models/providers; MCP servers contribute tools, not models, so
	// skip them rather than eagerly spawn and connect every configured server.
	rows := assembly.Rows(assembly.Options{
		UsageCache: args.usageCache,
		Memory:     args.native.Memory(),
		Policy:     policy,
		CWD:        cwd, AgentDir: agentDir, Settings: settings,
		Bridge: bridgeExtension(args, settings), BridgeManagement: true,
		BridgeAgentCalls: agentbridge.Extension(func(ctx context.Context, peer string, call bridge.Call) (json.RawMessage, error) {
			var result json.RawMessage
			err := args.bridgeLink.invoke(ctx, "outbound", map[string]any{"peer_id": peer, "call": call}, &result)
			return result, err
		}),
		ClaudeSessions: claudesessions.Management(settings, agentDir, os.Environ()),
		CodexSessions:  codexsessions.Extension(os.Environ()),
		// Background jobs run through the same bash as the built-in: sandbox, shell and prefix.
		Bash: func(cwd string) (engine.AgentTool, error) {
			mode, err := permissions.SandboxMode(settings)
			if err != nil {
				return nil, err
			}
			built, err := createBuiltInTools(cwd, []string{"bash"}, settings, mode)
			if err != nil {
				return nil, err
			}
			return built[0], nil
		},
		Compiled:   compiledExtensionsForEnvironment(os.Getenv),
		MCP:        !args.NoExtensions && !args.metadataOnly,
		MCPServers: args.mcpServers,
	})
	var diagnostics []modes.StartupDiagnostic
	resolved := assembly.Resolve(rows, settings, args.NoExtensions)
	for i := range resolved {
		if args.Auto && !args.NoExtensions && resolved[i].ID == "permissions" {
			resolved[i].Enabled, resolved[i].DecidedBy = true, "--auto"
		}
		// -e builtin:<name> loads that bundled plugin, even with --no-extensions.
		if slices.Contains(args.Extensions, extensions.BuiltinPathPrefix+resolved[i].ID) {
			resolved[i].Enabled, resolved[i].DecidedBy = true, "-e"
		}
	}
	registry, loadErrors := assembly.Load(cwd, resolved)
	for _, loadError := range loadErrors {
		diagnostics = append(diagnostics, otherDiagnostic(loadError.Error()))
	}
	if len(args.Extensions) > 0 || !args.NoExtensions {
		explicitPaths := make([]string, 0, len(args.Extensions))
		var sourceSpecs []string
		for _, extension := range args.Extensions {
			if strings.HasPrefix(extension, extensions.BuiltinPathPrefix) {
				continue
			}
			if isPackageSourceSpec(extension) {
				sourceSpecs = append(sourceSpecs, extension)
			} else {
				explicitPaths = append(explicitPaths, extension)
			}
		}
		if len(sourceSpecs) > 0 {
			// -e package specs resolve with temporary install semantics.
			manager := agent.NewPackageManager(agent.PackageManagerOptions{
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
		options := extensionDiscoveryOptions(cwd, agentDir, args.NoExtensions, settings, packages, explicitPaths)
		if paths := extensionhost.Discover(options); len(paths) > 0 {
			if registry == nil {
				registry = extensions.NewRegistry(cwd)
			}
			hostOptions := extensionhost.Options{AgentDir: agentDir, CWD: cwd, Version: version, Stderr: os.Stderr}
			// The CLI sets allowNoModel only for interactive runtime construction;
			// headless hosts never claim the Herdr pane.
			if args.allowNoModel && herdrext.InPane() {
				hostOptions.WrapFactory = herdrext.WrapPiIntegration
				hostOptions.ChildEnv = herdrext.PiForegroundHint
			}
			manager := extensionhost.NewManager(hostOptions)
			// Child agent sessions (agent_session_v1 / sdk_v1 resource reload)
			// run on the real NewAgentSession-backed runtime.
			manager.SetAgentSessionService(agent.NewExtensionAgentSessionService(
				agent.ExtensionAgentSessionServiceOptions{CWD: cwd, AgentDir: agentDir, Configure: configureChild(args.native)},
			))
			result := manager.RegisterInto(context.Background(), registry, paths)
			replaceActiveExtensionHost(manager)
			for _, diagnostic := range result.Diagnostics {
				diagnostics = append(diagnostics, modes.StartupDiagnostic{Kind: modes.StartupDiagnosticOther, Path: diagnostic.Path, Message: diagnostic.Message})
			}
			for _, loadError := range result.Errors {
				diagnostics = append(diagnostics, modes.StartupDiagnostic{Kind: modes.StartupDiagnosticExtension, Path: loadError.Path, Message: loadError.Error})
			}
		} else {
			replaceActiveExtensionHost(nil)
		}
	}
	for _, warning := range registry.OmitReplaced() {
		diagnostics = append(diagnostics, otherDiagnostic(warning))
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

// extensionDiscoveryOptions is the one construction of the JS-extension
// discovery inputs; boot and `orb plugins list --all` share it so the dump
// cannot drift from what boots.
func extensionDiscoveryOptions(cwd, agentDir string, noDiscovery bool, settings *config.SettingsManager, packages *agent.ResolvedPaths, explicitPaths []string) extensionhost.DiscoveryOptions {
	options := extensionhost.DiscoveryOptions{
		CWD:                    cwd,
		AgentDir:               agentDir,
		ProjectTrusted:         settings.IsProjectTrusted(),
		NoDiscovery:            noDiscovery,
		ConfiguredPaths:        settings.GetGlobalExtensionPaths(),
		ProjectConfiguredPaths: settings.GetProjectExtensionPaths(),
		ExplicitPaths:          explicitPaths,
	}
	if packages != nil {
		options.ResolvedPackagePaths, options.ProjectResolvedPackagePaths = packageExtensionPaths(packages.Extensions)
	}
	return options
}

// isPackageSourceSpec reports known package/URL prefixes; everything else is a
// local path.
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
func packageExtensionPaths(resources []agent.ResolvedResource) (user, project []string) {
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
	settings, err := args.native.Settings(cwd, agentDir, config.WithProjectTrusted(false))
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
	packageManager := agent.NewPackageManager(agent.PackageManagerOptions{
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

// resolveStartupProjectTrust decides project trust: when trust is genuinely in
// question it loads the pre-trust (global) extension set first so a
// project_trust handler is consulted ahead of the trust store and the
// interactive prompt. It leaves settings carrying the decision.
func resolveStartupProjectTrust(ctx context.Context, cwd, agentDir string, args CLIArgs, settings *config.SettingsManager) (projectTrustResolution, error) {
	resolution := projectTrustResolution{Undecided: args.ProjectTrusted == nil && config.HasTrustRequiringProjectResources(cwd)}
	var preTrustDiagnostics []modes.StartupDiagnostic
	var trustRunner *extensions.Runner
	if resolution.Undecided {
		untrustedPaths, err := agent.NewPackageManager(agent.PackageManagerOptions{
			CWD: cwd, AgentDir: agentDir, Settings: settings,
		}).Resolve(nil)
		if err != nil {
			return projectTrustResolution{}, err
		}
		resolution.PreTrustRegistry, preTrustDiagnostics = loadCompiledExtensions(cwd, agentDir, args, settings, untrustedPaths)
		trustRunner = extensions.NewRunner(resolution.PreTrustRegistry, extensions.RunnerOptions{CWD: cwd})
	}
	trustStore, err := args.native.Trust(agentDir)
	if err != nil {
		return resolution, err
	}
	trusted, err := agent.ResolveProjectTrusted(ctx, agent.ResolveProjectTrustedOptions{
		CWD:                 cwd,
		TrustStore:          trustStore,
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
		fmt.Fprintf(&section, "%-30s%s\n", name, cmp.Or(flag.Description, "Registered by "+flag.ExtensionPath))
	}
	return strings.TrimSuffix(helpText, "\n") + section.String()
}
