package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/rpc"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/session/exporthtml"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/auth/oauth"
	aimodels "github.com/OrdalieTech/orb/ai/models"
	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/gateway"
	"github.com/OrdalieTech/orb/chat/platforms"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/jstrim"
	"github.com/OrdalieTech/orb/internal/mermaid"
	"github.com/OrdalieTech/orb/internal/semver"
	"github.com/OrdalieTech/orb/internal/toolenv"
	"github.com/OrdalieTech/orb/platforms/native"
	"github.com/OrdalieTech/orb/platforms/native/sandbox"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
	"github.com/OrdalieTech/orb/platforms/native/teamenv"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
	"github.com/OrdalieTech/orb/plugins/usage"
	"golang.org/x/term"
)

// version is injected by goreleaser ldflags at release time. The unstamped
// default deliberately carries no number so a dev build can never masquerade
// as an older (or newer) release.
var version = "dev"

const (
	upstreamVersion     = "1.0.0"
	upstreamCommit      = "a13d35a742c6ef8462812a28fbe1d8c8b7431c32"
	latestReleaseURL    = selfupdate.LatestReleaseURL
	versionCheckTimeout = 10 * time.Second
)

type cliStreams struct {
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	StdinTTY  bool
	StdoutTTY bool
	StderrTTY bool
}

type cliDependencies struct {
	createRuntime           func(string, CLIArgs, engine.AgentMessages) (runtimeInputs, error)
	runAuth                 func(context.Context, CLIArgs, cliStreams) int
	runConfig               func(context.Context, modes.ConfigSelectorOptions) error
	loadModels              func(string) (*config.ModelRegistry, error)
	refreshModels           func(context.Context, string) error
	runInteractive          func(context.Context, *agent.SessionRuntime, modes.InteractiveModeOptions) int
	selectSession           SessionSelector
	selectSessionContext    ContextSessionSelector
	selectMissingSessionCWD func(context.Context, *MissingSessionCWDError) (string, bool, error)
	runRPCFixture           func(context.Context, CLIArgs, cliStreams, string) (handled bool, code int)
	selfUpdate              func(context.Context, io.Writer, bool, bool) int
}

func init() {
	if runtime.GOOS == "darwin" {
		scrubDisabledMallocStackLogging()
	}
}

func scrubDisabledMallocStackLogging() {
	// ponytail: one early scrub covers every child without changing deliberate logging.
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "MallocStackLogging") && (value == "" || value == "0") {
			_ = os.Unsetenv(name)
		}
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__sandbox" {
		os.Exit(runSandboxChild())
	}
	if len(os.Args) == 2 && os.Args[1] == "mermaid" {
		os.Exit(runMermaid(os.Stdin, os.Stdout))
	}
	// Orb started under a platform's name, through a link in an agent's shell.
	if platform, ok := platforms.Lookup(filepath.Base(os.Args[0])); ok && platform.Alias != nil {
		os.Exit(platform.Alias(os.Environ(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) == 4 && os.Args[1] == "chat" && os.Args[2] == "connect" {
		os.Exit(runChatConnect(os.Args[3], os.Stdin, os.Stdout))
	}
	if err := teamenv.LoadSecrets(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		os.Exit(1)
	}
	if os.Getenv(toolenv.Allow) != "" {
		teamenv.Hide()
	}
	// Process markers, entry points only — not set when embedded through the SDK.
	_ = os.Setenv("AI_AGENT", "orb")
	_ = os.Setenv("PI_CODING_AGENT", "true")
	os.Exit(runNativeCLI(context.Background(), os.Args[1:], cliStreams{
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		StdinTTY:  isTerminalFile(os.Stdin),
		StdoutTTY: isTerminalFile(os.Stdout),
		StderrTTY: isTerminalFile(os.Stderr),
	}))
}

func hideProcess() { teamenv.Hide() }

// runMermaid draws the Mermaid diagram on stdin as the TUI shows it, Unicode text, for clients
// with no renderer of their own (the Android app); nothing drawable exits 1. It opens no state.
func runMermaid(in io.Reader, out io.Writer) int {
	src, err := io.ReadAll(io.LimitReader(in, 1<<20))
	art := mermaid.Render(string(src))
	if err != nil || art == nil {
		return 1
	}
	_, _ = io.WriteString(out, strings.Join(art.Plain, "\n")+"\n")
	return 0
}

func runSandboxChild() int {
	// Landlock and no_new_privs bind to the calling OS thread; without this
	// pin the goroutine could migrate and exec from an unrestricted thread.
	runtime.LockOSThread()
	if err := sandbox.SelfRestrict(sandbox.Mode(os.Getenv(sandbox.EnvMode)), os.Getenv(sandbox.EnvRoot)); err != nil { //nolint:staticcheck // SelfRestrict can succeed on Linux; Darwin always refuses this entry point.
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 126
	}
	shell := os.Getenv(sandbox.EnvShell)
	if shell == "" {
		shell = "/bin/sh"
	}
	if err := native.Exec(shell, []string{shell, "-c", os.Getenv(sandbox.EnvCommand)}, os.Environ()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "sandbox: exec:", err)
	}
	return 126
}

func runCLI(ctx context.Context, argv []string, streams cliStreams) int {
	defer replaceActiveExtensionHost(nil)
	return runCLIWithDependencies(ctx, argv, streams, platformCLIDependencies())
}

func runCLIWithDependencies(ctx context.Context, argv []string, streams cliStreams, dependencies cliDependencies) int {
	if streams.Stdin == nil {
		streams.Stdin = strings.NewReader("")
	}
	if streams.Stdout == nil {
		streams.Stdout = io.Discard
	}
	if streams.Stderr == nil {
		streams.Stderr = io.Discard
	}
	if dependencies.createRuntime == nil {
		dependencies.createRuntime = createRuntimeInputs
	}
	if dependencies.runAuth == nil {
		dependencies.runAuth = runAuthCommand
	}
	if dependencies.runConfig == nil {
		dependencies.runConfig = modes.RunConfigSelector
	}
	if dependencies.loadModels == nil {
		dependencies.loadModels = config.NewModelRegistry
	}
	if dependencies.refreshModels == nil {
		dependencies.refreshModels = refreshModelCatalogs
	}
	if dependencies.runInteractive == nil {
		dependencies.runInteractive = modes.RunInteractiveMode
	}
	if dependencies.selfUpdate == nil {
		dependencies.selfUpdate = runSelfUpdate
	}
	if state := stateFromContext(ctx); state != nil {
		refresh := dependencies.refreshModels
		dependencies.refreshModels = func(refreshCtx context.Context, agentDir string) error {
			return refresh(context.WithValue(refreshCtx, nativeStateKey{}, state), agentDir)
		}
	}
	if dependencies.selectSession == nil && dependencies.selectSessionContext == nil {
		if state := stateFromContext(ctx); state != nil {
			dependencies.selectSessionContext = func(current, all ContextSessionListLoader) (string, bool, error) {
				bindings, err := state.keybindings()
				if err != nil {
					return "", false, err
				}
				return modes.RunSessionSelectorWithOptions(ctx, modes.SessionSelectorOptions{CurrentSessionsContext: modes.SessionSelectorContextLoader(current), AllSessionsContext: modes.SessionSelectorContextLoader(all), Keybindings: modes.NewAppKeybindings(bindings), DeleteSession: state.deleteSession})
			}
		} else {
			dependencies.selectSessionContext = startupContextTUISessionSelector(ctx)
		}
	}
	if dependencies.selectMissingSessionCWD == nil {
		dependencies.selectMissingSessionCWD = func(ctx context.Context, issue *MissingSessionCWDError) (string, bool, error) {
			return modes.RunStartupSelector(ctx, modes.StartupSelectorOptions{
				Title: formatMissingSessionCWDPrompt(issue),
				Choices: []modes.StartupChoice{
					{Label: "Continue", Value: issue.CurrentCWD},
					{Label: "Cancel", Cancel: true},
				},
			})
		}
	}
	if len(argv) > 0 && argv[0] == "bridge" {
		return runBridgeCommand(ctx, argv[1:], streams)
	}
	if len(argv) > 0 && argv[0] == "chat" {
		return runChatCommand(ctx, argv[1:], streams, dependencies)
	}
	if handled, code := handleCredentialPrintCommand(ctx, argv, streams); handled {
		return code
	}
	if handled, code := handlePluginsCommand(ctx, argv, streams); handled {
		return code
	}
	if handled, code := handleMCPCommand(ctx, argv, streams); handled {
		return code
	}
	if handled, code := handlePackageCommand(ctx, argv, streams, dependencies); handled {
		return code
	}
	if handled, code := handleConfigCommand(ctx, argv, streams, dependencies); handled {
		return code
	}

	args := normalizeRuntimeCLIArgs(ParseArgs(argv))
	args.native = stateFromContext(ctx)
	args.bridgeLink = &cliBridgeLink{}
	args.usageCache = &usage.Cache{}
	offlineValue, networkDisabled := os.LookupEnv("PI_OFFLINE")
	offlineValue = strings.ToLower(offlineValue)
	offlineMode := args.Offline || offlineValue == "1" || offlineValue == "true" || offlineValue == "yes"
	if offlineMode {
		_ = os.Setenv("PI_OFFLINE", "1")
		_ = os.Setenv("PI_SKIP_VERSION_CHECK", "1")
		networkDisabled = true
	}
	hasErrors := false
	for _, diagnostic := range args.Diagnostics {
		prefix := "Warning: "
		color := colorWarning
		if diagnostic.Type == "error" {
			prefix = "Error: "
			color = colorError
			hasErrors = true
		}
		_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, color, prefix+diagnostic.Message))
	}
	if hasErrors {
		return 1
	}
	if args.Version {
		_, _ = fmt.Fprintln(streams.Stdout, versionOutput())
		return 0
	}
	if args.Command != "" {
		return dependencies.runAuth(ctx, args, streams)
	}
	if args.Export != nil && *args.Export != "" {
		outputPath := ""
		if len(args.Messages) > 0 {
			outputPath = args.Messages[0]
		}
		var path string
		var err error
		if args.native != nil && !strings.ContainsAny(*args.Export, `/\`) && !strings.HasSuffix(*args.Export, ".jsonl") {
			stored, openErr := args.native.sessions().OpenPath(ctx, *args.Export)
			if openErr != nil {
				return reportCLIError(streams.Stderr, openErr)
			}
			manager, openErr := session.FromHarnessStorage(stored.Storage(), session.WithHarnessRepo(args.native.sessions()))
			if openErr != nil {
				return reportCLIError(streams.Stderr, openErr)
			}
			if strings.HasSuffix(outputPath, ".md") {
				path, err = exporthtml.ExportSessionMarkdown(manager, outputPath)
			} else {
				path, err = exporthtml.ExportSession(manager, exporthtml.Options{OutputPath: outputPath})
			}
		} else if strings.HasSuffix(outputPath, ".md") {
			path, err = exporthtml.ExportMarkdownFromFile(*args.Export, outputPath)
		} else {
			path, err = exporthtml.ExportFromFile(*args.Export, exporthtml.Options{OutputPath: outputPath})
		}
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintln(streams.Stdout, "Exported to: "+path)
		return 0
	}
	if args.Mode == "rpc" && len(args.FileArgs) > 0 {
		// Checked before session-flag validation, as upstream does.
		_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorError, "Error: @file arguments are not supported in RPC mode"))
		return 1
	}
	if sessionErrors := validateSessionFlags(args); len(sessionErrors) > 0 {
		_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorError, "Error: "+sessionErrors[0]))
		return 1
	}
	if args.native == nil {
		if _, err := migrateStartupAuth(); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	if args.Help {
		text := helpText
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			// Help needs extension flags, but no MCP or resource startup.
			helpArgs := args
			helpArgs.metadataOnly = true
			if registry, _, _, loadErr := loadStartupExtensions(cwd, helpArgs); loadErr == nil {
				text = extensionHelpText(registry)
			}
		}
		_, _ = io.WriteString(metadataOutput(args, streams), text)
		return 0
	}

	validationErrors := make([]string, 0, 2)
	if args.APIKey != nil && *args.APIKey != "" && args.Model == nil && len(args.Models) == 0 {
		validationErrors = append(validationErrors, "--api-key requires a model to be specified via --model, --provider/--model, or --models")
	}
	// claude-sessions picks its own default model.
	if args.Provider != nil && *args.Provider != "" && *args.Provider != claudesessions.Name && args.Model == nil {
		validationErrors = append(validationErrors, fmt.Sprintf("--provider requires --model (for example: --provider %s --model <pattern>)", *args.Provider))
	}
	if len(args.UnknownFlags) > 0 && len(validationErrors) > 0 {
		var registry *extensions.Registry
		if validationCWD, cwdErr := os.Getwd(); cwdErr == nil {
			registry, _, _, _ = loadStartupExtensions(validationCWD, args)
		}
		flagErrors := applyExtensionFlags(registry, args.UnknownFlags)
		validationErrors = append(flagErrors, validationErrors...)
	}
	for _, message := range validationErrors {
		_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorError, "Error: "+message))
	}
	if len(validationErrors) > 0 {
		return 1
	}
	if len(args.UnknownFlags) > 0 {
		validationCWD, cwdErr := os.Getwd()
		if cwdErr != nil {
			return reportCLIError(streams.Stderr, cwdErr)
		}
		registry, warnings, trusted, loadErr := loadStartupExtensions(validationCWD, args)
		if loadErr != nil {
			return reportCLIError(streams.Stderr, loadErr)
		}
		args.extensionRegistry, args.extensionWarnings = registry, warnings
		args.extensionsLoaded = true
		args.resolvedProjectTrust = trusted
		flagErrors := applyExtensionFlags(args.extensionRegistry, args.UnknownFlags)
		if len(flagErrors) > 0 {
			for _, warning := range args.extensionWarnings {
				_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorWarning, "Warning: "+startupDiagnosticText(warning)))
			}
			for _, message := range flagErrors {
				_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorError, "Error: "+message))
			}
			return 1
		}
	}
	if args.ListModels != nil {
		// Models are listed after full runtime creation so providers registered
		// by extensions participate in the listing.
		listCWD, err := os.Getwd()
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		listArgs := args
		listArgs.useUnknownModel = true
		listArgs.metadataOnly = true
		inputs, err := dependencies.createRuntime(listCWD, listArgs, nil)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		// createRuntime drains settings errors into Diagnostics; without this the
		// listing silently ran on defaults when settings.json failed to parse.
		for _, diagnostic := range inputs.Diagnostics {
			_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorWarning, "Warning: "+startupDiagnosticText(diagnostic)))
		}
		if inputs.ModelRegistry != nil {
			if loadError := inputs.ModelRegistry.Error(); loadError != "" {
				_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorWarning, "Warning: errors loading models.json:\n"+loadError))
			}
		}
		var models []ai.Model
		if inputs.AvailableModels != nil {
			models = inputs.AvailableModels()
		}
		_, _ = io.WriteString(metadataOutput(args, streams), formatModelList(models, *args.ListModels))
		return 0
	}
	if args.Mode == "acp" {
		return runACP(ctx, args, dependencies, streams)
	}
	isInteractive := !args.Print && args.Mode != "json" && args.Mode != "rpc" && streams.StdinTTY && streams.StdoutTTY
	cwd, err := os.Getwd()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if args.Mode == "rpc" && dependencies.runRPCFixture != nil {
		if handled, code := dependencies.runRPCFixture(ctx, args, streams, cwd); handled {
			return code
		}
	}
	if isInteractive {
		args.allowNoModel = true
	} else {
		args.useUnknownModel = true
	}
	baseArgs := args
	manager, sessionContext, err := createCLISession(cwd, args, streams, dependencies.selectSession, dependencies.selectSessionContext)
	if err != nil {
		if errors.Is(err, errNoSessionSelected) {
			return 0
		}
		return reportCLIError(streams.Stderr, err)
	}
	if issue := getMissingSessionCWDIssue(manager, cwd); issue != nil {
		if !isInteractive {
			return reportCLIError(streams.Stderr, issue)
		}
		selectedCWD, selected, selectErr := dependencies.selectMissingSessionCWD(ctx, issue)
		if selectErr != nil {
			return reportCLIError(streams.Stderr, selectErr)
		}
		if !selected {
			return 0
		}
		agentDir, dirErr := config.GetAgentDir()
		if dirErr != nil {
			return reportCLIError(streams.Stderr, dirErr)
		}
		if args.native != nil {
			var opened *harness.Session
			opened, err = args.native.sessions().OpenPath(ctx, manager.GetSessionID())
			if err == nil {
				manager, err = session.FromHarnessStorage(opened.Storage(), session.WithHarnessRepo(args.native.sessions()), session.WithCwdOverride(selectedCWD))
			}
		} else {
			manager, err = session.Open(issue.SessionFile, manager.GetSessionDir(), session.WithAgentDir(agentDir), session.WithCwdOverride(selectedCWD))
		}
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		sessionContext = manager.BuildSessionContext()
	}
	if args.Name != nil {
		name := strings.TrimFunc(*args.Name, jstrim.IsSpace)
		if name == "" {
			return reportCLIError(streams.Stderr, errors.New("--name requires a non-empty value"))
		}
		if _, err := manager.AppendSessionInfo(name); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		sessionContext = manager.BuildSessionContext()
	}
	if len(sessionContext.Messages) > 0 {
		applySessionDefaults(&args, sessionContext, manager.GetBranch())
	}
	if isInteractive {
		inputs, runtimeErr := dependencies.createRuntime(manager.GetCWD(), args, decodeSessionMessages(sessionContext.Messages))
		if runtimeErr != nil {
			return reportCLIError(streams.Stderr, runtimeErr)
		}
		if runtimeErr = appendInitialRuntimeState(manager, inputs.Agent.State(), sessionContext); runtimeErr != nil {
			return reportCLIError(streams.Stderr, runtimeErr)
		}
		sessionRuntime, runtimeErr := buildSessionRuntime(inputs, manager, sessionRuntimeOptions{
			mode: extensions.ModeTUI, errorWriter: streams.Stderr, deferSessionStart: true,
		})
		if runtimeErr != nil {
			return reportCLIError(streams.Stderr, runtimeErr)
		}
		initial, initialImages, inputErr := PrepareInitialInput(&args, manager.GetCWD(), nil)
		if inputErr != nil {
			sessionRuntime.Dispose()
			return reportCLIError(streams.Stderr, inputErr)
		}
		agentDir, dirErr := config.GetAgentDir()
		if dirErr != nil {
			sessionRuntime.Dispose()
			return reportCLIError(streams.Stderr, dirErr)
		}
		var startupModelRefresh func(context.Context) error
		if startupModelRefreshEnabled("interactive", offlineMode, !networkDisabled) {
			startupModelRefresh = func(refreshContext context.Context) error {
				return refreshStartupModels(refreshContext, !networkDisabled, agentDir, inputs.ModelRegistry, dependencies.refreshModels)
			}
		}
		host := newInteractiveSessionHost(baseArgs, dependencies, sessionRuntime, inputs, agentDir, streams.Stderr)
		defer attachCLIBridge(ctx, bridgeInteractiveHost{host}, args, inputs.Settings, streams.Stderr)()

		bindings, err := args.native.keybindings()
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		return dependencies.runInteractive(ctx, host.Session(), modes.InteractiveModeOptions{
			Keybindings:    bindings,
			InitialMessage: initial,
			InitialImages:  initialImages,
			Messages:       append([]string(nil), args.Messages...),
			SessionHeader:  manager.GetHeader(),
			Verbose:        args.Verbose,
			StartupVersionCheck: newStartupVersionCheck(
				version, http.DefaultClient, latestReleaseURL, versionCheckTimeout,
			),
			StartupModelRefresh: startupModelRefresh,
			// Skill and prompt warnings (a name other tools accept, a YAML slip in a skill from another
			// tool's folder) are not shown: the owner cannot act on them from here, and the skill
			// loads or is skipped either way. Upstream prints them at startup.
			// ponytail: dropped, not listed elsewhere; add an `orb doctor` when a skipped skill needs explaining.
			Diagnostics:         inputs.Diagnostics,
			Host:                host,
			Changelog:           "",
			Output:              streams.Stdout,
			OutputTTY:           streams.StdoutTTY,
			InitialThemeSetting: args.UseTheme,
		})
	}
	extensionMode := extensions.ModePrint
	switch args.Mode {
	case "json":
		extensionMode = extensions.ModeJSON
	case "rpc":
		extensionMode = extensions.ModeRPC
	}
	sessionHost, err := newCLISessionRuntimeHost(ctx, cliSessionRuntimeHostOptions{
		BaseArgs: baseArgs, Manager: manager,
		Dependencies: dependencies, Streams: streams, ExtensionMode: extensionMode,
		// RPC binds its extension UI in bindReplacement; hold session_start
		// until then so extensions see a live ctx.ui (not the headless noop).
		DeferSessionStart: args.Mode == "rpc",
	})
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer attachCLIBridge(ctx, sessionHost, args, sessionHost.Services().SettingsManager, streams.Stderr)()
	if services := sessionHost.Services(); services != nil {
		startStartupModelRefresh(ctx, args.Mode, offlineMode, !networkDisabled, services.AgentDir, services.ModelRegistry, dependencies.refreshModels)
	}
	sessionRuntime := sessionHost.Session()
	if args.Mode == "rpc" {
		// Defer the initial extension bind: rpc.Serve binds the RPC extension UI
		// and then the extensions, so session_start fires once with a live ctx.ui.
		host, hostErr := rpc.NewRuntimeHost(ctx, sessionHost, true)
		if hostErr != nil {
			sessionHost.Dispose(ctx)
			return reportCLIError(streams.Stderr, hostErr)
		}
		return serveRPC(ctx, host, streams, func() []rpc.SlashCommand { return rpc.SlashCommands(host.Session()) })
	}
	printSession := newCLIPrintSession(ctx, sessionHost)
	sessionHost.SetRebindSession(printSession.Bind)
	if err := printSession.Bind(sessionRuntime); err != nil {
		sessionHost.Dispose(ctx)
		return reportCLIError(streams.Stderr, err)
	}
	defer sessionHost.Dispose(ctx)

	var stdinContent *string
	if !streams.StdinTTY {
		stdinContent, err = ReadPipedStdin(streams.Stdin)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	initial, initialImages, err := PrepareInitialInput(&args, manager.GetCWD(), stdinContent)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	outputMode := modes.PrintOutputText
	if args.Mode == "json" {
		outputMode = modes.PrintOutputJSON
	}
	return modes.RunPrintMode(ctx, printSession, modes.PrintModeOptions{
		Mode:           outputMode,
		Messages:       args.Messages,
		InitialMessage: initial,
		InitialImages:  initialImages,
		SessionHeader:  sessionRuntime.Manager().GetHeader(),
		Stdout:         streams.Stdout,
		Stderr:         streams.Stderr,
	})
}

const (
	colorError   = "\x1b[31m"
	colorWarning = "\x1b[33m"
	colorClose   = "\x1b[39m"
)

// colorizeDiagnostic keys color support on STDOUT even though the lines go to
// stderr, as upstream's chalk does, with NO_COLOR and TERM=dumb opt-outs.
func colorizeDiagnostic(streams cliStreams, color, line string) string {
	if !streams.StdoutTTY || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return line
	}
	return color + line + colorClose
}

func metadataOutput(args CLIArgs, streams cliStreams) io.Writer {
	if !args.Print && args.Mode == "" {
		return streams.Stdout
	}
	if args.Mode == "json" || args.Mode == "rpc" || args.Print || !streams.StdinTTY || !streams.StdoutTTY {
		return streams.Stderr
	}
	return streams.Stdout
}

func migrateStartupAuth() (string, error) {
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return "", err
	}
	_, err = config.MigrateAuthToAuthJSON(agentDir)
	return agentDir, err
}

func catalogRefreshOptions(ctx context.Context, agentDir string) aimodels.RefreshOptions {
	options := aimodels.RefreshOptions{StorePath: filepath.Join(agentDir, "models-store.json"), UserAgent: aimodels.OrbUserAgent(version)}
	if state := stateFromContext(ctx); state != nil {
		options.StoreDocument = state.Document(options.StorePath)
		options.StorePath = ""
	}
	return options
}

func refreshModelCatalogs(ctx context.Context, agentDir string) error {
	timeoutContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := aimodels.Refresh(timeoutContext, catalogRefreshOptions(ctx, agentDir))
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(timeoutContext.Err(), context.DeadlineExceeded) {
		return errors.New("model catalog refresh timed out")
	}
	return err
}

func versionOutput() string {
	return fmt.Sprintf("orb %s (upstream pi %s @ %.8s)", version, upstreamVersion, upstreamCommit)
}

func newStartupVersionCheck(currentVersion string, client *http.Client, endpoint string, timeout time.Duration) func(context.Context, extensions.UI) {
	return func(ctx context.Context, ui extensions.UI) {
		if os.Getenv("PI_SKIP_VERSION_CHECK") != "" || os.Getenv("PI_OFFLINE") != "" {
			return
		}
		tag, err := selfupdate.LatestTag(ctx, currentVersion, client, endpoint, timeout)
		if err != nil || !isNewerPackageVersion(tag, currentVersion) {
			return
		}
		ui.Notify(fmt.Sprintf("orb %s is available. Run: orb update", tag), extensions.NotifyInfo)
	}
}

func isNewerPackageVersion(candidate, current string) bool {
	candidate, current = strings.TrimSpace(candidate), strings.TrimSpace(current)
	candidateVersion, candidateOK := semver.Parse(candidate)
	currentVersion, currentOK := semver.Parse(current)
	if candidateOK && currentOK {
		return semver.Compare(candidateVersion, currentVersion) > 0
	}
	return candidate != current
}

func startupModelRefreshEnabled(mode string, offline, allowNetwork bool) bool {
	if mode == "interactive" {
		return !offline && allowNetwork
	}
	return mode == "rpc" && !offline
}

func startStartupModelRefresh(ctx context.Context, mode string, offline, allowNetwork bool, agentDir string, registry *config.ModelRegistry, refresh func(context.Context, string) error) {
	if !startupModelRefreshEnabled(mode, offline, allowNetwork) || registry == nil {
		return
	}
	go func() {
		_ = refreshStartupModels(ctx, allowNetwork, agentDir, registry, refresh)
	}()
}

func refreshStartupModels(ctx context.Context, allowNetwork bool, agentDir string, registry *config.ModelRegistry, refresh func(context.Context, string) error) error {
	if registry == nil {
		return nil
	}
	if allowNetwork && refresh != nil {
		_ = refresh(ctx, agentDir)
		_ = refreshCodexModels(ctx, agentDir, registry)
	}
	return registry.Reload()
}

// refreshCodexModels lists the models the signed-in ChatGPT account offers.
func refreshCodexModels(ctx context.Context, agentDir string, registry *config.ModelRegistry) error {
	auth, err := registry.ResolveProviderAuth(ctx, "openai-codex", nil)
	if err != nil || auth == nil || auth.Auth.APIKey == nil {
		return err
	}
	token := *auth.Auth.APIKey
	timeoutContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	options := catalogRefreshOptions(ctx, agentDir)
	options.Client = http.DefaultClient
	return aimodels.RefreshCodex(timeoutContext, options, token, oauth.OpenAICodexAccountID(token))
}

func applySessionDefaults(args *CLIArgs, context session.SessionContext, branch []session.SessionEntry) {
	if len(context.Messages) > 0 && context.Model != nil && (args.Model == nil || *args.Model == "") {
		// Upstream treats provider/model as one selection. A provider-only CLI
		// argument does not override the model restored from a session.
		args.Provider = stringValue(context.Model.Provider)
		args.Model = stringValue(context.Model.ModelID)
		args.RestoredModel = true
	}
	if args.Thinking == nil && len(context.Messages) > 0 && hasThinkingLevelChange(branch) {
		args.Thinking = stringValue(context.ThinkingLevel)
	}
}

func decodeSessionMessages(rawMessages []json.RawMessage) engine.AgentMessages {
	messages := make(engine.AgentMessages, 0, len(rawMessages))
	for _, raw := range rawMessages {
		message, err := ai.UnmarshalMessage(raw)
		if err == nil {
			messages = append(messages, message)
		} else {
			messages = append(messages, append(json.RawMessage(nil), raw...))
		}
	}
	return messages
}

func appendInitialRuntimeState(manager *session.SessionManager, state engine.AgentState, prior session.SessionContext) error {
	if len(prior.Messages) > 0 {
		if hasThinkingLevelChange(manager.GetBranch()) {
			return nil
		}
	} else if state.Model != nil && !agent.IsUnknownModel(state.Model) {
		if _, err := manager.AppendModelChange(string(state.Model.Provider), state.Model.ID); err != nil {
			return err
		}
	}
	_, err := manager.AppendThinkingLevelChange(string(state.ThinkingLevel))
	return err
}

func hasThinkingLevelChange(branch []session.SessionEntry) bool {
	return slices.ContainsFunc(branch, func(entry session.SessionEntry) bool { return entry.Type == "thinking_level_change" })
}

func isTerminalFile(file *os.File) bool {
	return file != nil && term.IsTerminal(int(file.Fd()))
}

func reportCLIError(writer io.Writer, err error) int {
	_, _ = fmt.Fprintln(writer, "Error: "+err.Error())
	return 1
}

func runChatCommand(ctx context.Context, args []string, streams cliStreams, dependencies cliDependencies) int {
	if len(args) == 0 || slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		_, _ = io.WriteString(streams.Stdout, chatHelpText())
		return 0
	}
	tools := false
	var selected []string
	for _, arg := range args {
		switch arg = strings.ToLower(arg); {
		case arg == "--tools":
			tools = true
		case slices.Contains(platforms.Names(), arg):
			if !slices.Contains(selected, arg) {
				selected = append(selected, arg)
			}
		default:
			return reportCLIError(streams.Stderr, fmt.Errorf("unsupported chat platform %q", arg))
		}
	}
	if len(selected) == 0 {
		return reportCLIError(streams.Stderr, errors.New("usage: orb chat <platform>... [--tools]"))
	}
	agents := teamAgent(ctx, dependencies, streams)
	var fronts []func(context.Context) error
	var chats []string
	for _, name := range selected {
		if platform, _ := platforms.Lookup(name); platform.Front != nil {
			fronts = append(fronts, func(ctx context.Context) error {
				agent := chat.Agent{Serve: agents.serve, Connect: chatConnectCommand, Environ: toolenv.Environ, Export: toolenv.Export, Log: streams.Stderr}
				return platform.Front(ctx, agent, os.Environ())
			})
		} else {
			chats = append(chats, name)
		}
	}
	if len(chats) == 0 {
		return runFronts(ctx, fronts, streams)
	}
	authorize, err := gateway.Authorizer(os.Getenv(platforms.AllowedSenders))
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	agentDir, err := migrateStartupAuth()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	dataDir := strings.TrimSpace(os.Getenv("ORB_CHAT_DATA_DIR"))
	if dataDir == "" {
		dataDir = filepath.Join(agentDir, "chat", strings.Join(chats, "+"))
	}
	var adapters []chat.Adapter
	var ingresses []func(context.Context, func(chat.Message) error) error
	for _, platform := range chats {
		open, _ := platforms.Lookup(platform)
		adapter, inbound, err := open.Open(os.Environ())
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		ingress := inbound.Poll
		if inbound.Webhook != nil {
			ingress = gateway.Webhook(platform, strings.TrimSpace(os.Getenv("ORB_CHAT_LISTEN")), strings.TrimSpace(os.Getenv("ORB_CHAT_PATH")), inbound.Webhook)
		}
		adapters, ingresses = append(adapters, adapter), append(ingresses, ingress)
	}
	var workspace []chat.LocalProviderOption
	if tools {
		cwd, err := os.Getwd()
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		workspace = append(workspace, agentWorkspace(agents, cwd))
	}
	return runLocalChat(ctx, dataDir, adapters, ingresses, fronts, authorize, workspace, streams)
}

// agentWorkspace makes chat conversations full sessions of the agent, built as
// its ACP sessions are, with its tools working in cwd.
func agentWorkspace(agents acpHost, cwd string) chat.LocalProviderOption {
	return chat.WithWorkspace(cwd, func(ctx context.Context, manager *session.SessionManager) (*agent.AgentSession, func(), error) {
		args := agents.args
		args.native = args.native.conversation()
		runtime, close, err := openHeadless(ctx, args, agents.dependencies, agents.streams, manager)
		if err != nil {
			return nil, nil, err
		}
		return runtime.Session(), close, nil
	})
}

// runLocalChat runs the gateway on dataDir, its sessions and spool in the
// native state when there is one.
func runLocalChat(
	ctx context.Context,
	dataDir string,
	adapters []chat.Adapter,
	ingresses []func(context.Context, func(chat.Message) error) error,
	fronts []func(context.Context) error,
	authorize func(chat.Message) error,
	providerOptions []chat.LocalProviderOption,
	streams cliStreams,
) int {
	options := gateway.Options{DataDir: dataDir, Adapters: adapters, Ingresses: ingresses, Fronts: fronts, Authorize: authorize, Provider: providerOptions, Log: streams.Stderr}
	if state := stateFromContext(ctx); state != nil {
		settings, err := state.settings(dataDir, state.agentDir)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		auth, err := state.auth(state.agentDir)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		registry, err := state.models(state.agentDir, state.accounts(state.agentDir, auth), false)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		options.Provider = append(options.Provider, chat.WithPersistence(func(key chat.ConversationKey) harness.SessionRepo {
			return state.DB.Sessions(state.ChatNamespace(filepath.Join(dataDir, "sessions", key.String())))
		}, settings, registry), chat.WithAgentDir(state.agentDir))
		options.Spool = state.DB.Chat(state.ChatNamespace(dataDir))
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := gateway.Run(ctx, options); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}

// runFronts runs an agent's fronts until one ends or a signal arrives.
func runFronts(ctx context.Context, fronts []func(context.Context) error, streams cliStreams) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := gateway.RunFronts(ctx, fronts, streams.Stderr, nil); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}

// chatHelpText documents `orb chat` and the platforms linked in.
func chatHelpText() string {
	var credentials strings.Builder
	for _, platform := range platforms.All {
		line := fmt.Sprintf("  %-25s %s", strings.Join(platform.Env, ", "), platform.About)
		if len(strings.Join(platform.Env, ", ")) > 25 && platform.About != "" {
			line = "  " + strings.Join(platform.Env, ", ") + "\n" + strings.Repeat(" ", 28) + platform.About
		}
		credentials.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return `Usage: orb chat <platform>... [--tools]

Runs this agent on every platform named, as one process with one memory.

Platforms: ` + strings.Join(platforms.Names(), ", ") + `

  --tools  Gives chat conversations the agent's tools in the working directory.
           Only for an agent running isolated, such as one container per agent.

Common environment:
  ORB_CHAT_ALLOWED_SENDERS Comma-separated platform user IDs (required for chat platforms)
  ORB_CHAT_DATA_DIR        Session and spool directory (default ~/.pi/agent/chat/<platform>)
  ORB_CHAT_LISTEN          Webhook listen address (default 127.0.0.1:8080)
  ORB_CHAT_PATH            Webhook path (default /<platform>)

Platform credentials:
` + credentials.String()
}

const helpText = `orb - AI coding assistant

Usage: orb [options] [@files...] [messages...]

       orb login <provider>
       orb logout [provider]
       orb auth <command>
       orb chat <platform>

OAuth providers: anthropic, openai-codex, github-copilot, kimi-coding, openrouter, xai

Commands:
  orb chat <platform>         Run a chat gateway
  orb install <source> [-l]   Install a package source and save it to settings
  orb remove <source> [-l]    Remove a package source from settings
  orb uninstall <source> [-l] Alias for remove
  orb update [target]         Update orb itself, installed packages, or model catalogs
  orb upgrade [target]        Alias for update
  orb list                    List installed packages from settings
  orb config [-l]             Open TUI to enable/disable package resources (Tab switches scope)
  orb plugins <command>       List, enable, or disable bundled plugins (list --all shows the full composition)
  orb mermaid < diagram.mmd   Draw a Mermaid diagram as text, as the TUI shows it
  orb mcp <command>           Check MCP servers, sign in to or out of OAuth servers
  orb auth <command>           Print credentials for external clients
  orb storage <command>        Migrate, import/export, back up, or recover conversations
  orb <command> --help        Show help for chat/install/remove/uninstall/update/upgrade/list/config/auth/mcp

  --provider <name>              Provider to search for --model (requires --model)
  --model <id>                   Model ID
  --models <patterns>            Comma-separated model cycling patterns
  --list-models [search]         List available models
  --api-key <key>                Provider API key
  --system-prompt <text|file>    Replace the system prompt
  --append-system-prompt <text>  Append text or file contents
  --thinking <level>             off|minimal|low|medium|high|xhigh|max
  --mode <mode>                  Output mode: text (default), json, rpc, or acp (Agent Client Protocol on stdio)
  --print, -p                    Process prompts and exit
  --continue, -c                 Continue previous session
  --resume, -r                   Select a session to resume
  --session <path|id>            Use specific session file or partial UUID
  --session-id <id>              Use exact project session ID, creating it if missing
  --fork <path|id>               Fork specific session file or partial UUID into a new session
  --name, -n <name>              Set the session display name
  --pi-files                     Use Pi-compatible files (must be the first argument)
  --session-dir <dir>            Session directory in --pi-files compatibility mode
  --no-session                   Don't save session (ephemeral)
  --export <path|id> [output]    Export a session to HTML or Markdown and exit
  --tools, -t <names>            Comma-separated tool allowlist
  --exclude-tools, -xt <names>   Comma-separated tool denylist
  --skill <path>                 Load a skill file or directory; repeatable
  --no-skills, -ns               Disable discovered skills; --skill remains additive
  --prompt-template <path>       Load a prompt template file or directory; repeatable
  --no-prompt-templates, -np     Disable prompt template discovery
  --extension, -e <path>         Load an extension file or builtin:<name> (can be used multiple times)
  --no-extensions, -ne           Disable extension discovery and built-in extensions (explicit -e paths still work)
  --theme <path>                 Load a theme file or directory; repeatable
  --use-theme <name[/name]>      Set the initial interactive theme for this run
  --no-themes                    Disable theme discovery
  --no-context-files, -nc        Disable AGENTS.md/CLAUDE.md discovery
  --verbose                      Force verbose startup (overrides quietStartup setting)
  --approve, -a                  Trust project-local resources for this run
  --auto                         Auto-approve tool requests; enforce permission denials
  --no-approve, -na              Ignore project-local resources for this run
  --offline                      Disable startup network operations (same as PI_OFFLINE=1)
  --help, -h                     Show help
  --version, -v                  Show version
`
