package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/OrdalieTech/pi-go/internal/jsonwire"
	"golang.org/x/term"
)

const version = "0.1.0-dev"

type cliStreams struct {
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	StdinTTY  bool
	StdoutTTY bool
}

type cliDependencies struct {
	createRuntime func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error)
	runAuth       func(context.Context, CLIArgs, cliStreams) int
	loadModels    func(string) (*config.ModelRegistry, error)
	refreshModels func(context.Context, string) error
	selectSession SessionSelector
	runRPCFixture func(context.Context, CLIArgs, cliStreams, string) (handled bool, code int)
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
	if dependencies.loadModels == nil {
		dependencies.loadModels = config.NewModelRegistry
	}
	if dependencies.refreshModels == nil {
		dependencies.refreshModels = refreshModelCatalogs
	}
	if dependencies.selectSession == nil {
		dependencies.selectSession = terminalSessionSelector(streams)
	}
	if handled, code := handleModelUpdate(ctx, argv, streams, dependencies); handled {
		return code
	}

	args := normalizeRuntimeCLIArgs(ParseArgs(argv))
	originalArgs := args
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
		_, _ = fmt.Fprintln(streams.Stdout, version)
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
			if agentDir, dirErr := config.GetAgentDir(); dirErr == nil {
				if settings, settingsErr := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir)); settingsErr == nil {
					registry, _ := loadCompiledExtensions(cwd, args, settings)
					text = extensionHelpText(registry)
				}
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
			if agentDir, dirErr := config.GetAgentDir(); dirErr == nil {
				if validationSettings, settingsErr := config.NewSettingsManager(validationCWD, config.WithAgentDir(agentDir)); settingsErr == nil {
					registry, _ = loadCompiledExtensions(validationCWD, args, validationSettings)
				}
			}
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
		agentDir, dirErr := config.GetAgentDir()
		if dirErr != nil {
			return reportCLIError(streams.Stderr, dirErr)
		}
		validationSettings, settingsErr := config.NewSettingsManager(validationCWD, config.WithAgentDir(agentDir))
		if settingsErr != nil {
			return reportCLIError(streams.Stderr, settingsErr)
		}
		args.extensionRegistry, args.extensionWarnings = loadCompiledExtensions(validationCWD, args, validationSettings)
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
		agentDir, err := config.GetAgentDir()
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		registry, err := dependencies.loadModels(agentDir)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		if loadError := registry.Error(); loadError != "" {
			_, _ = fmt.Fprintln(streams.Stderr, "Warning: errors loading models.json:\n"+loadError)
		}
		_, _ = io.WriteString(metadataOutput(args, streams), formatModelList(registry.Available(nil), *args.ListModels))
		return 0
	}
	cwd, err := os.Getwd()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if args.Mode == "rpc" && dependencies.runRPCFixture != nil {
		if handled, code := dependencies.runRPCFixture(ctx, args, streams, cwd); handled {
			return code
		}
	}
	manager, sessionContext, err := createCLISession(cwd, args, streams, dependencies.selectSession)
	if err != nil {
		if errors.Is(err, errNoSessionSelected) {
			return 0
		}
		return reportCLIError(streams.Stderr, err)
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
	if !args.Print && args.Mode != "json" && args.Mode != "rpc" && streams.StdinTTY && streams.StdoutTTY {
		_, _ = fmt.Fprintln(streams.Stderr, "Error: interactive mode is not available until the TUI work packages; use -p")
		return 1
	}
	if len(manager.GetEntries()) > 0 {
		applySessionDefaults(&args, sessionContext, manager.GetBranch())
	}
	priorMessages := decodeSessionMessages(sessionContext.Messages)
	runtime, err := dependencies.createRuntime(manager.GetCWD(), args, priorMessages)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	for _, diagnostic := range runtime.Diagnostics {
		_, _ = fmt.Fprintln(streams.Stderr, "Warning: "+diagnostic)
	}
	if err := appendInitialRuntimeState(manager, runtime.Agent.State(), sessionContext); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	settings := runtime.Settings
	if settings == nil {
		agentDir, settingsErr := config.GetAgentDir()
		if settingsErr != nil {
			return reportCLIError(streams.Stderr, settingsErr)
		}
		settings, settingsErr = config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(agentDir))
		if settingsErr != nil {
			return reportCLIError(streams.Stderr, settingsErr)
		}
	}
	extensionMode := extensions.ModePrint
	switch args.Mode {
	case "json":
		extensionMode = extensions.ModeJSON
	case "rpc":
		extensionMode = extensions.ModeRPC
	}
	sessionRuntime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: runtime.Agent, SessionManager: manager, Settings: settings,
		GetAPIKey: runtime.GetAPIKey, GetRequestAuth: runtime.GetRequestAuth, GetModelHeaders: runtime.GetModelHeaders,
		AvailableModels:   runtime.AvailableModels,
		ScopedModels:      runtime.ScopedModels,
		SlashResolver:     runtime.SlashResolver,
		ExtensionRegistry: runtime.Extensions, ExtensionMode: extensionMode, ModelRegistry: runtime.ModelRegistry,
		ExtensionErrorHandler: func(extensionError extensions.ExtensionError) {
			_, _ = fmt.Fprintf(streams.Stderr, "Extension error (%s, %s): %s\n", extensionError.ExtensionPath, extensionError.Event, extensionError.Error)
		},
		BaseTools: runtime.BaseTools, InitialActiveToolNames: runtime.ActiveToolNames,
		AllowedToolNames: runtime.AllowedTools, ExcludedToolNames: runtime.ExcludedTools,
		SystemPromptOptions: &runtime.PromptOptions,
	})
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if args.Mode == "rpc" {
		host := newRPCSessionHost(originalArgs, dependencies, sessionRuntime)
		return modes.RunRPCMode(ctx, host, modes.RPCModeOptions{
			Stdin: streams.Stdin, Stdout: streams.Stdout, Stderr: streams.Stderr,
			Commands: func() []modes.RPCSlashCommand { return rpcSlashCommands(host.Session()) },
		})
	}
	defer sessionRuntime.Dispose()

	var stdinContent *string
	if !streams.StdinTTY {
		stdinContent, err = ReadPipedStdin(streams.Stdin)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	initialMessage, err := PrepareInitialMessage(&args, manager.GetCWD(), stdinContent)
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
	return modes.RunPrintMode(ctx, sessionRuntime, modes.PrintModeOptions{
		Mode:           outputMode,
		Messages:       args.Messages,
		InitialMessage: initial,
		SessionHeader:  manager.GetHeader(),
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

func handleModelUpdate(ctx context.Context, argv []string, streams cliStreams, dependencies cliDependencies) (bool, int) {
	if len(argv) == 0 || argv[0] != "update" || !slices.Contains(argv[1:], "--models") {
		return false, 0
	}
	if len(argv) != 2 || argv[1] != "--models" {
		_, _ = fmt.Fprintln(streams.Stderr, "Error: --models cannot be combined with another update target")
		return true, 1
	}
	agentDir, err := migrateStartupAuth()
	if err == nil {
		err = dependencies.refreshModels(ctx, agentDir)
	}
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintln(streams.Stdout, "Model catalogs refreshed")
	return true, 0
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
	if state.Model != nil {
		if _, err := manager.AppendModelChange(string(state.Model.Provider), state.Model.ID); err != nil {
			return err
		}
	}
	if _, err := manager.AppendThinkingLevelChange(string(state.ThinkingLevel)); err != nil {
		return err
	}
	return nil
}

func persistAgentMessages(manager *session.SessionManager) agent.EventSink {
	return func(_ context.Context, event agent.AgentEvent) error {
		messageEnd, ok := event.(agent.MessageEndEvent)
		if !ok {
			return nil
		}
		encoded, err := ai.Marshal(messageEnd.Message)
		if err != nil {
			return err
		}
		var envelope struct {
			Role       json.RawMessage `json:"role"`
			CustomType json.RawMessage `json:"customType"`
			Content    json.RawMessage `json:"content"`
			Display    bool            `json:"display"`
			Details    json.RawMessage `json:"details"`
		}
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			return err
		}
		role, err := jsonwire.UnmarshalString(bytes.TrimSpace(envelope.Role))
		if err != nil {
			return err
		}
		switch role {
		case "user", "assistant", "toolResult":
			_, err = manager.AppendMessage(messageEnd.Message)
			return err
		case "custom":
			customType, decodeErr := jsonwire.UnmarshalString(bytes.TrimSpace(envelope.CustomType))
			if decodeErr != nil {
				return decodeErr
			}
			if len(envelope.Content) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Content), []byte("null")) {
				envelope.Content = json.RawMessage("[]")
			}
			if len(envelope.Details) > 0 {
				_, err = manager.AppendCustomMessageEntry(customType, envelope.Content, envelope.Display, envelope.Details)
			} else {
				_, err = manager.AppendCustomMessageEntry(customType, envelope.Content, envelope.Display)
			}
			return err
		default:
			return nil
		}
	}
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
  --no-extensions, -ne           Disable compiled-in extension discovery
  --no-context-files, -nc        Disable AGENTS.md/CLAUDE.md discovery
  --approve, -a                  Trust project-local resources for this run
  --no-approve, -na              Ignore project-local resources for this run
  --help, -h                     Show help
  --version, -v                  Show version
`
