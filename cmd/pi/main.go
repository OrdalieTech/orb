package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aimodels "github.com/OrdalieTech/pi-go/ai/models"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/modes"
	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/session/exporthtml"
	"github.com/OrdalieTech/pi-go/internal/semver"
	"golang.org/x/term"
)

// version is injected by goreleaser ldflags at release time.
var version = "0.1.0-dev"

const (
	upstreamVersion        = "0.80.10"
	upstreamCommit         = "3a40794ea14c6202586cc203d5b928eca9f6b673"
	latestReleaseURL       = "https://api.github.com/repos/OrdalieTech/pi-go/releases/latest"
	releasesURL            = "https://github.com/OrdalieTech/pi-go/releases"
	versionCheckTimeout    = 10 * time.Second
	versionResponseMaxSize = 64 << 10
)

type cliStreams struct {
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	StdinTTY  bool
	StdoutTTY bool
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
	}))
}

func runCLI(ctx context.Context, argv []string, streams cliStreams) int {
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
		if diagnostic.Type == "error" {
			prefix = "Error: "
			hasErrors = true
		}
		_, _ = fmt.Fprintln(streams.Stderr, prefix+diagnostic.Message)
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
		path, err := exporthtml.ExportFromFile(*args.Export, exporthtml.Options{OutputPath: outputPath})
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintln(streams.Stdout, "Exported to: "+path)
		return 0
	}
	if sessionErrors := validateSessionFlags(args); len(sessionErrors) > 0 {
		_, _ = fmt.Fprintln(streams.Stderr, "Error: "+sessionErrors[0])
		return 1
	}
	if _, err := migrateStartupAuth(); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if args.Help {
		text := helpText
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			if registry, _, loadErr := loadStartupExtensions(cwd, args); loadErr == nil {
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
			registry, _, _ = loadStartupExtensions(validationCWD, args)
		}
		flagErrors := applyExtensionFlags(registry, args.UnknownFlags)
		validationErrors = append(flagErrors, validationErrors...)
	}
	for _, message := range validationErrors {
		_, _ = fmt.Fprintln(streams.Stderr, "Error: "+message)
	}
	if len(validationErrors) > 0 {
		return 1
	}
	if len(args.UnknownFlags) > 0 {
		validationCWD, cwdErr := os.Getwd()
		if cwdErr != nil {
			return reportCLIError(streams.Stderr, cwdErr)
		}
		registry, warnings, loadErr := loadStartupExtensions(validationCWD, args)
		if loadErr != nil {
			return reportCLIError(streams.Stderr, loadErr)
		}
		args.extensionRegistry, args.extensionWarnings = registry, warnings
		args.extensionsLoaded = true
		flagErrors := applyExtensionFlags(args.extensionRegistry, args.UnknownFlags)
		if len(flagErrors) > 0 {
			for _, warning := range args.extensionWarnings {
				_, _ = fmt.Fprintln(streams.Stderr, "Warning: "+warning)
			}
			for _, message := range flagErrors {
				_, _ = fmt.Fprintln(streams.Stderr, "Error: "+message)
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
		if inputs.ModelRegistry != nil {
			if loadError := inputs.ModelRegistry.Error(); loadError != "" {
				_, _ = fmt.Fprintln(streams.Stderr, "Warning: errors loading models.json:\n"+loadError)
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
		name := strings.TrimFunc(*args.Name, isJSTrimSpace)
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
		startStartupModelRefresh(ctx, "interactive", offlineMode, !networkDisabled, agentDir, inputs.ModelRegistry, dependencies.refreshModels)
		host := newInteractiveSessionHost(baseArgs, dependencies, sessionRuntime, inputs, agentDir, streams.Stderr)
		return dependencies.runInteractive(ctx, host.Session(), modes.InteractiveModeOptions{
			InitialMessage: initial,
			InitialImages:  initialImages,
			Messages:       append([]string(nil), args.Messages...),
			SessionHeader:  manager.GetHeader(),
			StartupVersionCheck: newStartupVersionCheck(
				version, http.DefaultClient, latestReleaseURL, versionCheckTimeout,
			),
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
	_, err := aimodels.Refresh(timeoutContext, aimodels.RefreshOptions{StorePath: filepath.Join(agentDir, "models-store.json")})
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(timeoutContext.Err(), context.DeadlineExceeded) {
		return errors.New("model catalog refresh timed out")
	}
	return err
}

func versionOutput() string {
	return fmt.Sprintf("pi-go %s (upstream pi %s @ %.8s)", version, upstreamVersion, upstreamCommit)
}

func newStartupVersionCheck(currentVersion string, client *http.Client, endpoint string, timeout time.Duration) func(context.Context, extensions.UI) {
	return func(ctx context.Context, ui extensions.UI) {
		if os.Getenv("PI_SKIP_VERSION_CHECK") != "" || os.Getenv("PI_OFFLINE") != "" {
			return
		}
		requestContext, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
		if err != nil {
			return
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "pi-go/"+currentVersion)
		response, err := client.Do(request)
		if err != nil {
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return
		}
		var release struct {
			TagName string `json:"tag_name"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, versionResponseMaxSize)).Decode(&release) != nil {
			return
		}
		tag := strings.TrimSpace(release.TagName)
		if tag == "" || !isNewerPackageVersion(tag, currentVersion) {
			return
		}
		ui.Notify(fmt.Sprintf("pi-go %s is available: %s", tag, releasesURL), extensions.NotifyInfo)
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

func startupModelRefreshEnabled(mode string, offline bool) bool {
	return !offline && (mode == "interactive" || mode == "rpc")
}

func startStartupModelRefresh(ctx context.Context, mode string, offline, allowNetwork bool, agentDir string, registry *config.ModelRegistry, refresh func(context.Context, string) error) {
	if !startupModelRefreshEnabled(mode, offline) || registry == nil {
		return
	}
	go func() {
		if allowNetwork && refresh != nil {
			_ = refresh(ctx, agentDir)
		}
		_ = registry.Reload()
	}()
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

const helpText = `pi - AI coding assistant

Usage: pi [options] [@files...] [messages...]

       pi login <provider>
       pi logout [provider]

OAuth providers: anthropic, openai-codex, github-copilot, xai

Commands:
  pi install <source> [-l]     Install extension source and add to settings
  pi remove <source> [-l]      Remove extension source from settings
  pi uninstall <source> [-l]   Alias for remove
  pi update [source|self|pi]   Update pi, extensions, or model catalogs
  pi list                      List installed extensions from settings
  pi config [-l]               Open TUI to enable/disable package resources (Tab switches scope)
  pi <command> --help          Show help for install/remove/uninstall/update/list/config

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
  --no-extensions, -ne           Disable compiled-in extension discovery
  --no-context-files, -nc        Disable AGENTS.md/CLAUDE.md discovery
  --approve, -a                  Trust project-local resources for this run
  --no-approve, -na              Ignore project-local resources for this run
  --offline                      Disable startup network operations (same as PI_OFFLINE=1)
  --help, -h                     Show help
  --version, -v                  Show version
`
