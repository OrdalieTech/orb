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
	"strings"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	aimodels "github.com/OrdalieTech/orb/ai/models"
	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/discord"
	"github.com/OrdalieTech/orb/chat/googlechat"
	"github.com/OrdalieTech/orb/chat/messenger"
	"github.com/OrdalieTech/orb/chat/slack"
	"github.com/OrdalieTech/orb/chat/teams"
	"github.com/OrdalieTech/orb/chat/telegram"
	"github.com/OrdalieTech/orb/chat/whatsapp"
	"github.com/OrdalieTech/orb/codingagent"
	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	"github.com/OrdalieTech/orb/codingagent/modes"
	"github.com/OrdalieTech/orb/codingagent/session"
	"github.com/OrdalieTech/orb/codingagent/session/exporthtml"
	"github.com/OrdalieTech/orb/internal/jstrim"
	"github.com/OrdalieTech/orb/internal/semver"
	"golang.org/x/term"
)

// version is injected by goreleaser ldflags at release time. The unstamped
// default deliberately carries no number so a dev build can never masquerade
// as an older (or newer) release.
var version = "dev"

const (
	upstreamVersion        = "0.84.1"
	upstreamCommit         = "53fa77ccd8a279eb87e92294ef3687b03ff80112"
	latestReleaseURL       = "https://api.github.com/repos/OrdalieTech/orb/releases/latest"
	versionCheckTimeout    = 10 * time.Second
	selfUpdateCheckTimeout = 3 * time.Second
	versionResponseMaxSize = 64 << 10
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
	createRuntime           func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error)
	runAuth                 func(context.Context, CLIArgs, cliStreams) int
	runConfig               func(context.Context, modes.ConfigSelectorOptions) error
	loadModels              func(string) (*config.ModelRegistry, error)
	refreshModels           func(context.Context, string) error
	runInteractive          func(context.Context, *codingagent.SessionRuntime, modes.InteractiveModeOptions) int
	selectSession           SessionSelector
	selectMissingSessionCWD func(context.Context, *MissingSessionCWDError) (string, bool, error)
	runRPCFixture           func(context.Context, CLIArgs, cliStreams, string) (handled bool, code int)
}

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], cliStreams{
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		StdinTTY:  isTerminalFile(os.Stdin),
		StdoutTTY: isTerminalFile(os.Stdout),
		StderrTTY: isTerminalFile(os.Stderr),
	}))
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
	if dependencies.selectSession == nil {
		dependencies.selectSession = startupTUISessionSelector(ctx)
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
	if len(argv) > 0 && argv[0] == "chat" {
		return runChatCommand(ctx, argv[1:], streams)
	}
	if handled, code := handleCredentialPrintCommand(ctx, argv, streams); handled {
		return code
	}
	if handled, code := handlePluginsCommand(ctx, argv, streams); handled {
		return code
	}
	if handled, code := handlePackageCommand(ctx, argv, streams, dependencies); handled {
		return code
	}
	if handled, code := handleConfigCommand(ctx, argv, streams, dependencies); handled {
		return code
	}

	args := normalizeRuntimeCLIArgs(ParseArgs(argv))
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
		if strings.HasSuffix(outputPath, ".md") {
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
		// Upstream guards before session-flag validation (main.ts:546-549).
		_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorError, "Error: @file arguments are not supported in RPC mode"))
		return 1
	}
	if sessionErrors := validateSessionFlags(args); len(sessionErrors) > 0 {
		_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorError, "Error: "+sessionErrors[0]))
		return 1
	}
	if _, err := migrateStartupAuth(); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if args.Help {
		text := helpText
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			// Help needs only registration-static flag metadata: metadataOnly
			// keeps configured MCP servers from spawning and lets the host
			// metadata snapshot cache satisfy the load without a JS runtime.
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
				_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorWarning, "Warning: "+warning))
			}
			for _, message := range flagErrors {
				_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorError, "Error: "+message))
			}
			return 1
		}
	}
	if args.ListModels != nil {
		// Upstream lists models after full runtime creation (main.ts:747-764), so
		// providers registered by extensions participate in the listing.
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
			_, _ = fmt.Fprintln(streams.Stderr, colorizeDiagnostic(streams, colorWarning, "Warning: "+diagnostic))
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
	manager, sessionContext, err := createCLISession(cwd, args, streams, dependencies.selectSession)
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
		manager, err = session.Open(issue.SessionFile, manager.GetSessionDir(), session.WithAgentDir(agentDir), session.WithCwdOverride(selectedCWD))
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
	if len(manager.GetEntries()) > 0 {
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
		initialMessage, initialImages, inputErr := PrepareInitialInput(&args, manager.GetCWD(), nil)
		if inputErr != nil {
			sessionRuntime.Dispose()
			return reportCLIError(streams.Stderr, inputErr)
		}
		initial := ""
		if initialMessage != nil {
			initial = *initialMessage
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
		return dependencies.runInteractive(ctx, host.Session(), modes.InteractiveModeOptions{
			InitialMessage: initial,
			InitialImages:  initialImages,
			Messages:       append([]string(nil), args.Messages...),
			SessionHeader:  manager.GetHeader(),
			Verbose:        args.Verbose,
			StartupVersionCheck: newStartupVersionCheck(
				version, http.DefaultClient, latestReleaseURL, versionCheckTimeout,
			),
			StartupModelRefresh: startupModelRefresh,
			// Skill/prompt resource diagnostics stay interactive-only; upstream
			// print/RPC modes emit no resource diagnostics (main.ts:87-91).
			Diagnostics: append(append([]string(nil), inputs.Diagnostics...), inputs.ResourceDiagnostics...),
			Host:        host,
			Changelog:   "",
			Output:      streams.Stdout,
			OutputTTY:   streams.StdoutTTY,
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
	if services := sessionHost.Services(); services != nil {
		startStartupModelRefresh(ctx, args.Mode, offlineMode, !networkDisabled, services.AgentDir, services.ModelRegistry, dependencies.refreshModels)
	}
	sessionRuntime := sessionHost.Session()
	if args.Mode == "rpc" {
		// Defer the initial extension bind: RunRPCMode binds the RPC extension UI
		// and then the extensions, so session_start fires once with a live ctx.ui.
		host, hostErr := newRPCSessionHost(ctx, sessionHost, true)
		if hostErr != nil {
			sessionHost.Dispose(ctx)
			return reportCLIError(streams.Stderr, hostErr)
		}
		return modes.RunRPCMode(ctx, host, modes.RPCModeOptions{
			Stdin: streams.Stdin, Stdout: streams.Stdout, Stderr: streams.Stderr,
			Commands: func() []modes.RPCSlashCommand { return rpcSlashCommands(host.Session()) },
		})
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
	initialMessage, initialImages, err := PrepareInitialInput(&args, manager.GetCWD(), stdinContent)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	initial := ""
	if initialMessage != nil {
		initial = *initialMessage
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

// colorizeDiagnostic mirrors upstream's chalk.red/chalk.yellow startup
// diagnostics (main.ts:87-93, 511-514). Upstream's default chalk keys color
// support on STDOUT even though the lines go to stderr, with NO_COLOR and
// TERM=dumb opt-outs.
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

func refreshModelCatalogs(ctx context.Context, agentDir string) error {
	timeoutContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := aimodels.Refresh(timeoutContext, aimodels.RefreshOptions{
		StorePath: filepath.Join(agentDir, "models-store.json"),
		UserAgent: aimodels.OrbUserAgent(version),
	})
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
		tag, err := fetchLatestReleaseVersion(ctx, currentVersion, client, endpoint, timeout)
		if err != nil || !isNewerPackageVersion(tag, currentVersion) {
			return
		}
		ui.Notify(fmt.Sprintf("orb %s is available. Run: orb update", tag), extensions.NotifyInfo)
	}
}

func fetchLatestReleaseVersion(ctx context.Context, currentVersion string, client *http.Client, endpoint string, timeout time.Duration) (string, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.New("invalid release endpoint")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "orb/"+currentVersion)
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return "", errors.New("timed out")
		}
		if errors.Is(err, context.Canceled) {
			return "", errors.New("canceled")
		}
		return "", errors.New("network error")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("GitHub returned %s", response.Status)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, versionResponseMaxSize)).Decode(&release) != nil {
		return "", errors.New("invalid GitHub response")
	}
	tag := strings.TrimSpace(release.TagName)
	if tag == "" {
		return "", errors.New("GitHub response had no version")
	}
	return tag, nil
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
	}
	return registry.Reload()
}

func applySessionDefaults(args *CLIArgs, context session.SessionContext, branch []session.SessionEntry) {
	if len(context.Messages) > 0 && context.Model != nil && (args.Model == nil || *args.Model == "") {
		// Upstream treats provider/model as one selection. A provider-only CLI
		// argument does not override the model restored from a session.
		args.Provider = stringValue(context.Model.Provider)
		args.Model = stringValue(context.Model.ModelID)
		args.RestoredModel = true
	}
	hasThinkingEntry := false
	for _, entry := range branch {
		if entry.Type == "thinking_level_change" {
			hasThinkingEntry = true
			break
		}
	}
	if args.Thinking == nil && len(context.Messages) > 0 && hasThinkingEntry {
		args.Thinking = stringValue(context.ThinkingLevel)
	}
}

func decodeSessionMessages(rawMessages []json.RawMessage) agent.AgentMessages {
	messages := make(agent.AgentMessages, 0, len(rawMessages))
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

func appendInitialRuntimeState(manager *session.SessionManager, state agent.AgentState, prior session.SessionContext) error {
	hasExistingSession := len(prior.Messages) > 0
	hasThinkingEntry := false
	for _, entry := range manager.GetBranch() {
		if entry.Type == "thinking_level_change" {
			hasThinkingEntry = true
			break
		}
	}
	if hasExistingSession {
		if hasThinkingEntry {
			return nil
		}
		_, err := manager.AppendThinkingLevelChange(string(state.ThinkingLevel))
		return err
	}
	if state.Model != nil && !codingagent.IsUnknownModel(state.Model) {
		if _, err := manager.AppendModelChange(string(state.Model.Provider), state.Model.ID); err != nil {
			return err
		}
	}
	if _, err := manager.AppendThinkingLevelChange(string(state.ThinkingLevel)); err != nil {
		return err
	}
	return nil
}

func isTerminalFile(file *os.File) bool {
	if file == nil {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func reportCLIError(writer io.Writer, err error) int {
	_, _ = fmt.Fprintln(writer, "Error: "+err.Error())
	return 1
}

func runChatCommand(ctx context.Context, args []string, streams cliStreams) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) ||
		(len(args) == 2 && (args[1] == "--help" || args[1] == "-h")) {
		_, _ = io.WriteString(streams.Stdout, chatHelpText)
		return 0
	}
	if len(args) != 1 {
		return reportCLIError(streams.Stderr, errors.New("usage: orb chat <platform>"))
	}
	platform := strings.ToLower(args[0])
	switch platform {
	case "telegram", "discord", "slack", "teams", "whatsapp", "messenger", "googlechat":
	default:
		return reportCLIError(streams.Stderr, fmt.Errorf("unsupported chat platform %q", platform))
	}
	authorize, err := chatAuthorizer(os.Getenv("ORB_CHAT_ALLOWED_SENDERS"))
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	agentDir, err := migrateStartupAuth()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	dataDir := strings.TrimSpace(os.Getenv("ORB_CHAT_DATA_DIR"))
	if dataDir == "" {
		dataDir = filepath.Join(agentDir, "chat", platform)
	}

	var adapter chat.Adapter
	var ingress func(context.Context, func(chat.Message) error) error
	// ponytail: credentials stay in the process environment; add named account
	// persistence only when one process needs to switch between accounts.
	switch platform {
	case "telegram":
		token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
		if token == "" {
			return reportCLIError(streams.Stderr, errors.New("TELEGRAM_BOT_TOKEN is required"))
		}
		telegramAdapter, createErr := telegram.New(telegram.Options{Token: token})
		if createErr != nil {
			return reportCLIError(streams.Stderr, createErr)
		}
		adapter, ingress = telegramAdapter, telegramAdapter.Poll
	case "discord":
		token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
		if token == "" {
			return reportCLIError(streams.Stderr, errors.New("DISCORD_BOT_TOKEN is required"))
		}
		discordAdapter, createErr := discord.New(discord.Options{Token: token})
		if createErr != nil {
			return reportCLIError(streams.Stderr, createErr)
		}
		adapter, ingress = discordAdapter, discordAdapter.Run
	case "slack":
		slackAdapter, createErr := slack.New(slack.Options{
			Token: os.Getenv("SLACK_BOT_TOKEN"), SigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
			BotUserID: os.Getenv("SLACK_BOT_USER_ID"),
		})
		if createErr != nil {
			return reportCLIError(streams.Stderr, createErr)
		}
		adapter, ingress = slackAdapter, webhookIngress(platform, slackAdapter.Webhook)
	case "teams":
		teamsAdapter, createErr := teams.New(teams.Options{
			AppID: os.Getenv("TEAMS_APP_ID"), AppPassword: os.Getenv("TEAMS_APP_PASSWORD"),
			TenantID: os.Getenv("TEAMS_TENANT_ID"),
		})
		if createErr != nil {
			return reportCLIError(streams.Stderr, createErr)
		}
		adapter, ingress = teamsAdapter, webhookIngress(platform, teamsAdapter.Webhook)
	case "whatsapp":
		whatsappAdapter, createErr := whatsapp.New(whatsapp.Options{
			Token: os.Getenv("WHATSAPP_TOKEN"), PhoneNumberID: os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
			AppSecret: os.Getenv("WHATSAPP_APP_SECRET"), VerifyToken: os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		})
		if createErr != nil {
			return reportCLIError(streams.Stderr, createErr)
		}
		adapter, ingress = whatsappAdapter, webhookIngress(platform, whatsappAdapter.Webhook)
	case "messenger":
		messengerAdapter, createErr := messenger.New(messenger.Options{
			Token: os.Getenv("MESSENGER_TOKEN"), PageID: os.Getenv("MESSENGER_PAGE_ID"),
			AppSecret: os.Getenv("MESSENGER_APP_SECRET"), VerifyToken: os.Getenv("MESSENGER_VERIFY_TOKEN"),
		})
		if createErr != nil {
			return reportCLIError(streams.Stderr, createErr)
		}
		adapter, ingress = messengerAdapter, webhookIngress(platform, messengerAdapter.Webhook)
	case "googlechat":
		credentials, readErr := os.ReadFile(os.Getenv("GOOGLE_CHAT_CREDENTIALS_FILE"))
		if readErr != nil {
			return reportCLIError(streams.Stderr, fmt.Errorf("read GOOGLE_CHAT_CREDENTIALS_FILE: %w", readErr))
		}
		googleAdapter, createErr := googlechat.New(googlechat.Options{
			ProjectNumber: os.Getenv("GOOGLE_CHAT_PROJECT_NUMBER"), CredentialsJSON: credentials,
		})
		if createErr != nil {
			return reportCLIError(streams.Stderr, createErr)
		}
		adapter, ingress = googleAdapter, webhookIngress(platform, googleAdapter.Webhook)
	}
	return runLocalChat(ctx, platform, dataDir, adapter, ingress, authorize, streams)
}

func webhookIngress(
	platform string,
	webhook func(func(chat.Message) error) http.Handler,
) func(context.Context, func(chat.Message) error) error {
	return func(ctx context.Context, publish func(chat.Message) error) error {
		listen := strings.TrimSpace(os.Getenv("ORB_CHAT_LISTEN"))
		if listen == "" {
			listen = "127.0.0.1:8080"
		}
		webhookPath := strings.TrimSpace(os.Getenv("ORB_CHAT_PATH"))
		if webhookPath == "" {
			webhookPath = "/" + platform
		}
		if !strings.HasPrefix(webhookPath, "/") || strings.ContainsAny(webhookPath, "{} \t\r\n") {
			return errors.New("ORB_CHAT_PATH must be a literal path starting with /")
		}
		mux := http.NewServeMux()
		mux.Handle(webhookPath, webhook(publish))
		// ponytail: one stdlib webhook server per process; terminate TLS and
		// multiplex public routes in the deployment's reverse proxy.
		server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		result := make(chan error, 1)
		go func() { result <- server.ListenAndServe() }()
		select {
		case err := <-result:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownContext); err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}

func runLocalChat(
	ctx context.Context,
	platform, dataDir string,
	adapter chat.Adapter,
	ingress func(context.Context, func(chat.Message) error) error,
	authorize func(chat.Message) error,
	streams cliStreams,
) int {
	provider, err := chat.NewLocalProvider(filepath.Join(dataDir, "sessions"))
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	processor, err := chat.New(chat.Options{
		Sessions: provider, Adapters: []chat.Adapter{adapter}, Authorize: authorize,
	})
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	local, err := chat.NewLocal(processor, filepath.Join(dataDir, "spool.jsonl"))
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}

	pollContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, _ = fmt.Fprintf(streams.Stderr, "%s gateway running; press Ctrl-C to stop\n", platform)
	ingressErr := ingress(pollContext, local.Publish)
	shutdownContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	closeErr := errors.Join(local.Close(shutdownContext), processor.Close(shutdownContext))
	if ingressErr != nil && !errors.Is(ingressErr, context.Canceled) {
		return reportCLIError(streams.Stderr, ingressErr)
	}
	if closeErr != nil {
		return reportCLIError(streams.Stderr, closeErr)
	}
	return 0
}

func chatAuthorizer(allowed string) (func(chat.Message) error, error) {
	ids := map[string]struct{}{}
	for _, id := range strings.Split(allowed, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("ORB_CHAT_ALLOWED_SENDERS is required")
	}
	return func(message chat.Message) error {
		if _, ok := ids[message.SenderID]; ok {
			return nil
		}
		return fmt.Errorf("sender %s is not in ORB_CHAT_ALLOWED_SENDERS", message.SenderID)
	}, nil
}

const chatHelpText = `Usage: orb chat <platform>

Platforms: telegram, discord, slack, teams, whatsapp, messenger, googlechat

Common environment:
  ORB_CHAT_ALLOWED_SENDERS Comma-separated platform user IDs (required)
  ORB_CHAT_DATA_DIR        Session and spool directory (default ~/.pi/agent/chat/<platform>)
  ORB_CHAT_LISTEN          Webhook listen address (default 127.0.0.1:8080)
  ORB_CHAT_PATH            Webhook path (default /<platform>)

Platform credentials:
  TELEGRAM_BOT_TOKEN        Telegram bot token
  DISCORD_BOT_TOKEN         Discord bot token
  SLACK_BOT_TOKEN, SLACK_SIGNING_SECRET, SLACK_BOT_USER_ID
  TEAMS_APP_ID, TEAMS_APP_PASSWORD, TEAMS_TENANT_ID
  WHATSAPP_TOKEN, WHATSAPP_PHONE_NUMBER_ID, WHATSAPP_APP_SECRET, WHATSAPP_VERIFY_TOKEN
  MESSENGER_TOKEN, MESSENGER_PAGE_ID, MESSENGER_APP_SECRET, MESSENGER_VERIFY_TOKEN
  GOOGLE_CHAT_PROJECT_NUMBER, GOOGLE_CHAT_CREDENTIALS_FILE
`

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
  orb update [target]         Show orb update instructions or update packages/models
  orb list                    List installed packages from settings
  orb config [-l]             Open TUI to enable/disable package resources (Tab switches scope)
  orb auth <command>           Print credentials for external clients
  orb <command> --help        Show help for chat/install/remove/uninstall/update/list/config/auth

  --provider <name>              Provider name
  --model <id>                   Model ID
  --models <patterns>            Comma-separated model cycling patterns
  --list-models [search]         List available models
  --api-key <key>                Provider API key
  --system-prompt <text|file>    Replace the system prompt
  --append-system-prompt <text>  Append text or file contents
  --thinking <level>             off|minimal|low|medium|high|xhigh|max
  --mode <mode>                  Output mode: text (default), json, or rpc
  --print, -p                    Process prompts and exit
  --continue, -c                 Continue previous session
  --resume, -r                   Select a session to resume
  --session <path|id>            Use specific session file or partial UUID
  --session-id <id>              Use exact project session ID, creating it if missing
  --fork <path|id>               Fork specific session file or partial UUID into a new session
  --name, -n <name>              Set the session display name
  --session-dir <dir>            Directory for session storage and lookup
  --no-session                   Don't save session (ephemeral)
  --export <file> [output]       Export session file to HTML and exit
  --tools, -t <names>            Comma-separated tool allowlist
  --exclude-tools, -xt <names>   Comma-separated tool denylist
  --skill <path>                 Load a skill file or directory; repeatable
  --no-skills, -ns               Disable discovered skills; --skill remains additive
  --prompt-template <path>       Load a prompt template file or directory; repeatable
  --no-prompt-templates, -np     Disable prompt template discovery
  --extension, -e <path>         Load an extension file (can be used multiple times)
  --no-extensions, -ne           Disable extension discovery (explicit -e paths still work)
  --theme <path>                 Load a theme file or directory; repeatable
  --no-themes                    Disable theme discovery
  --no-context-files, -nc        Disable AGENTS.md/CLAUDE.md discovery
  --verbose                      Force verbose startup (overrides quietStartup setting)
  --approve, -a                  Trust project-local resources for this run
  --no-approve, -na              Ignore project-local resources for this run
  --offline                      Disable startup network operations (same as PI_OFFLINE=1)
  --help, -h                     Show help
  --version, -v                  Show version
`
