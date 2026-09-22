package claudesessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/plugins/permissions"
)

// Model selects this capability only for an explicit or restored Claude session.
func Model(provider, model *string, settings *config.SettingsManager) *ai.Model {
	if provider == nil || *provider != Name {
		return nil
	}
	id, _ := settings.GetPluginSettings(Name)["model"].(string)
	if model != nil {
		id = *model
	}
	if id == "" {
		id = "default"
	}
	return &ai.Model{ID: id, Name: "Claude · " + id, API: Name, Provider: Name, Input: ai.InputModalities{"text", "image"}}
}

// RestoreSelection retains the native executor even when a CLI host carries
// startup defaults for another provider. An executor change requires a new session.
func RestoreSelection(state session.SessionContext, provider, model **string, branch ...session.SessionEntry) {
	if state.Model == nil {
		return
	}
	if state.Model.Provider != Name && (*provider == nil || **provider != Name) {
		if len(state.Messages) > 0 {
			return
		}
		leaving := false
		for _, entry := range branch {
			if entry.CustomType == Name+".exit" {
				leaving = true
				break
			}
		}
		if !leaving {
			return
		}
	}
	if state.Model.Provider == Name && len(state.Messages) > 0 && *provider != nil && **provider == Name && *model != nil {
		return
	}
	if state.Model.Provider == "unknown" && state.Model.ModelID == "unknown" {
		*provider, *model = nil, nil
		return
	}
	*provider = ptr(state.Model.Provider)
	*model = ptr(state.Model.ModelID)
}

// Factory rebuilds the plugin attachment for every SDK session replacement.
// The caller supplies storage in AgentSessionOptions and owns runtime disposal.
func Factory(options Options) agent.CreateAgentSessionRuntimeFactory {
	if options.Env != nil {
		options.Env = append([]string{}, options.Env...)
	}
	return func(ctx context.Context, opts agent.AgentSessionOptions) (*agent.AgentSessionResult, error) {
		owned := options
		if owned.Sandbox == "" {
			mode, err := permissions.SandboxMode(opts.Settings)
			if err != nil {
				return nil, err
			}
			owned.Sandbox = mode
		}
		owned.Manager = opts.SessionManager
		var runtime *agent.SessionRuntime
		if owned.Ask == nil {
			owned.Ask = func(ctx context.Context, title string, choices []string) (string, error) {
				return runtime.RequestInput(ctx, title, choices)
			}
		}
		driver, err := New(owned)
		if err != nil {
			return nil, err
		}
		id := "default"
		if opts.Model != nil && opts.Model.Provider == Name {
			id = opts.Model.ID
		}
		if state := owned.Manager.BuildSessionContext(); state.Model != nil && state.Model.Provider == Name {
			id = state.Model.ModelID
		}
		models, err := discoverModels(ctx, owned, owned.Manager.GetCWD())
		if err != nil {
			return nil, err
		}
		opts.Model = selectedModel(models, id)
		models = includeSelected(models, opts.Model)
		opts.SessionLoop = driver.Loop
		opts.ContextUsage = func() *harness.ContextUsage { return nativeContextUsage(owned.Manager) }
		opts.NoTools = "all"
		opts.Tools = []string{}
		if owned.RenderText != nil {
			for _, name := range nativeToolNames {
				tool := nativeTool(name)
				opts.Tools = append(opts.Tools, name)
				opts.CustomTools = append(opts.CustomTools, extensions.ToolDefinition{
					Name: name, Label: name, Description: "Native Claude tool",
					Execute: func(ctx context.Context, id string, args any, update engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
						return tool.Execute(ctx, id, args, update)
					},
					RenderCall: func(args any, theme extensions.Theme, context extensions.ToolRenderContext) extensions.Component {
						label, detail, split := strings.Cut(toolSummary(name, args, context.CWD), " · ")
						color := "accent"
						switch name {
						case "Bash":
							color = "bashMode"
						case "Edit", "Write":
							color = "success"
						}
						text := theme.FG(color, theme.Bold(label))
						if split {
							text += theme.FG("toolTitle", " "+detail)
						}
						return owned.RenderText(text)
					},
				})
			}
		}
		if opts.Resources == nil && opts.ResourceLoader == nil {
			opts.Resources = &agent.Resources{}
		}
		opts.AvailableModels = func() []ai.Model { return models }
		result, err := agent.NewAgentSession(opts)
		if err == nil {
			runtime = result.Session
		}
		return result, err
	}
}

// Configure composes a driver into the same runtime configuration used by all
// Orb hosts. bind must run before exposing the created runtime to controllers.
func Configure(cfg *agent.SessionRuntimeConfig, agentDir string, env []string) (bind func(*agent.SessionRuntime), err error) {
	state := cfg.Agent.State()
	if state.Model == nil || state.Model.Provider != Name {
		return func(*agent.SessionRuntime) {}, nil
	}
	options, err := configuredOptions(context.Background(), cfg.Settings, agentDir, env)
	if err != nil {
		return nil, err
	}
	options.Manager = cfg.SessionManager
	var runtime *agent.SessionRuntime
	options.Ask = func(ctx context.Context, title string, choices []string) (string, error) {
		return runtime.RequestInput(ctx, title, choices)
	}
	driver, err := New(options)
	if err != nil {
		return nil, err
	}
	models, err := discoverModels(context.Background(), options, options.Manager.GetCWD())
	if err != nil {
		return nil, err
	}
	state.Model = selectedModel(models, state.Model.ID)
	models = includeSelected(models, state.Model)
	state.Tools = nil
	cfg.Agent = engine.NewAgent(nil, engine.WithInitialState(state), engine.WithSessionLoop(driver.Loop))
	cfg.GetAPIKey, cfg.GetRequestAuth, cfg.GetModelHeaders = nil, nil, nil
	cfg.ContextUsage = func() *harness.ContextUsage { return nativeContextUsage(options.Manager) }
	cfg.BaseTools = make([]engine.AgentTool, 0, len(nativeToolNames))
	for _, name := range nativeToolNames {
		cfg.BaseTools = append(cfg.BaseTools, nativeTool(name))
	}
	cfg.InitialActiveToolNames = nil
	names := append([]string{}, nativeToolNames...)
	cfg.AllowedToolNames = &names
	cfg.RebuildBaseTools = nil
	cfg.AvailableModels = func() []ai.Model { return models }
	cfg.ScopedModels = nil
	return func(s *agent.SessionRuntime) { runtime = s }, nil
}

func configuredOptions(ctx context.Context, settingsManager *config.SettingsManager, agentDir string, env []string) (Options, error) {
	mode, err := permissions.SandboxMode(settingsManager)
	if err != nil {
		return Options{}, err
	}
	if err := checkSandbox(mode); err != nil {
		return Options{}, err
	}
	settings := settingsManager.GetPluginSettings(Name)
	option := func(key, fallback string) string {
		if value, ok := settings[key].(string); ok && value != "" {
			return value
		}
		return fallback
	}
	node, err := executable(option("node", "node"), env)
	if err != nil {
		return Options{}, errors.New("claude sessions needs Node 22.6 or newer on this host")
	}
	claude, err := executable(option("claude", "claude"), env)
	if err != nil {
		return Options{}, errors.New("install the official Claude Code CLI on this host, then run claude auth login")
	}
	defaultSDK := filepath.Join(agentDir, "plugins", Name, "node_modules", "@anthropic-ai", "claude-agent-sdk", "sdk.mjs")
	sdk := option("sdk", defaultSDK)
	if sdk != defaultSDK {
		if _, err = os.Stat(sdk); err != nil {
			return Options{}, errors.New("the configured Claude SDK is unavailable; check the SDK path in Claude settings")
		}
	} else if err = installSDK(ctx, agentDir, env); err != nil {
		return Options{}, err
	}
	return Options{Node: node, Claude: claude, SDK: sdk, Env: env, Sandbox: mode}, nil
}

// Serialize setup across Orb processes; existing sessions never run npm again.
func installSDK(ctx context.Context, agentDir string, env []string) error {
	dir := filepath.Join(agentDir, "plugins", Name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("could not prepare Claude; check that Orb's settings directory is writable")
	}
	unlock, err := filelock.Acquire(dir)
	if err != nil {
		return errors.New("claude setup is already running; try again shortly")
	}
	defer func() { _ = unlock() }()
	sdk := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk", "sdk.mjs")
	if _, err = os.Stat(sdk); err == nil {
		return nil
	}
	npm, err := executable("npm", env)
	if err != nil {
		return errors.New("claude needs Node.js with npm installed on this host")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	install := exec.CommandContext(ctx, npm, "install", "--prefix", dir, "--ignore-scripts", "--no-audit", "--no-fund", "@anthropic-ai/claude-agent-sdk@"+SDKVersion)
	install.Env = env
	if err = install.Run(); err != nil {
		// A failed npm run may already have written the entry point.
		_ = os.RemoveAll(filepath.Dir(sdk))
		return errors.New("could not finish Claude setup; check your connection and try again")
	}
	if _, err = os.Stat(sdk); err != nil {
		return errors.New("claude setup did not finish; try again")
	}
	return nil
}

type modelInfo struct {
	Value    string   `json:"value"`
	Resolved string   `json:"resolvedModel"`
	Display  string   `json:"displayName"`
	Effort   bool     `json:"supportsEffort"`
	Levels   []string `json:"supportedEffortLevels"`
	Adaptive bool     `json:"supportsAdaptiveThinking"`
}

func discoverModels(ctx context.Context, options Options, cwd string) ([]ai.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := json.Marshal(map[string]any{"type": "start", "catalog": true, "sdk": options.SDK, "claude": options.Claude, "cwd": cwd})
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, options.Node, "--input-type=module", "-e", hostSource)
	cmd.Env, cmd.Dir, cmd.Stdin = options.Env, cwd, bytes.NewReader(append(data, '\n'))
	kill := isolate(cmd)
	defer func() { _ = kill() }()
	cmd.WaitDelay = time.Second
	raw, err := cmd.Output()
	if err != nil {
		return nil, errors.New("could not load Claude models; check Claude sign-in on this host and retry")
	}
	var response struct {
		Type    string
		Models  []modelInfo
		Message string
	}
	if len(raw) > 8<<20 || json.Unmarshal(raw, &response) != nil || response.Type != "catalog" || len(response.Models) == 0 {
		return nil, errors.New("claude did not return its model catalog; check Claude sign-in on this host and retry")
	}
	models := make([]ai.Model, 0, len(response.Models))
	for _, info := range response.Models {
		if info.Value == "" {
			continue
		}
		name := "Claude · " + info.Display
		if info.Resolved != "" {
			name += " · " + info.Resolved
		}
		levels := map[ai.ModelThinkingLevel]*string{}
		for _, level := range []ai.ModelThinkingLevel{ai.ModelThinkingMinimal, ai.ModelThinkingLow, ai.ModelThinkingMedium, ai.ModelThinkingHigh, ai.ModelThinkingXHigh, ai.ModelThinkingMax} {
			levels[level] = nil
		}
		for _, level := range info.Levels {
			levels[ai.ModelThinkingLevel(level)] = ptr(level)
		}
		metadata, _ := json.Marshal(info)
		models = append(models, ai.Model{ID: info.Value, Name: name, API: Name, Provider: Name, Input: ai.InputModalities{"text", "image"}, Reasoning: info.Effort || info.Adaptive, ThinkingLevelMap: &levels, Compat: metadata})
	}
	if len(models) == 0 {
		return nil, errors.New("claude returned no usable models")
	}
	return models, nil
}

func selectedModel(models []ai.Model, id string) *ai.Model {
	for _, model := range models {
		if model.ID == id {
			return &model
		}
	}
	for _, model := range models {
		var info modelInfo
		if json.Unmarshal(model.Compat, &info) == nil && info.Resolved == id {
			model.ID = id
			return &model
		}
	}
	return &ai.Model{ID: id, Name: id, API: Name, Provider: Name, Input: ai.InputModalities{"text", "image"}}
}
func includeSelected(models []ai.Model, selected *ai.Model) []ai.Model {
	for _, model := range models {
		if model.ID == selected.ID {
			return models
		}
	}
	return append(models, *selected)
}

// Resolve only against the host-supplied environment, never process-global PATH.
func executable(name string, env []string) (string, error) {
	if filepath.IsAbs(name) {
		info, err := os.Stat(name)
		if err != nil {
			return "", err
		}
		if info.IsDir() || info.Mode().Perm()&0111 == 0 {
			return "", fmt.Errorf("%s is not executable", name)
		}
		return name, nil
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return "", errors.New("executable path must be absolute")
	}
	search := ""
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "PATH="); ok {
			search = value
		}
	}
	for _, dir := range filepath.SplitList(search) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}

// These tools provide presentation only; the SDK alone executes native calls.
var nativeToolNames = []string{"AskUserQuestion", "ExitPlanMode", "EnterPlanMode", "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet", "Bash", "Read", "Write", "Edit", "Glob", "Grep"}

type nativeTool string

func (t nativeTool) Spec() engine.AgentToolSpec {
	return engine.AgentToolSpec{Name: string(t), Label: string(t), Description: "Native Claude tool"}
}
func (nativeTool) Execute(context.Context, string, any, engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
	return engine.AgentToolResult{}, errors.New("this tool executes inside the native Claude session")
}
func (t nativeTool) RenderCall(args any) string { return toolSummary(string(t), args, "") }
func (nativeTool) RenderResult(result engine.AgentToolResult) string {
	var parts []string
	for _, block := range result.Content {
		if text, ok := block.(*ai.TextContent); ok && text != nil {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolSummary(name string, args any, cwd string) string {
	raw, err := json.Marshal(args)
	if err != nil {
		return name
	}
	var input struct {
		Questions                                    json.RawMessage `json:"questions"`
		Plan, Command, Description, Pattern, Subject string
		FilePath                                     string `json:"file_path"`
		Todos                                        []struct{ Content, Status string }
	}
	if json.Unmarshal(raw, &input) != nil {
		return name
	}
	if input.FilePath != "" && cwd != "" && filepath.IsAbs(input.FilePath) {
		if relative, err := filepath.Rel(cwd, input.FilePath); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			input.FilePath = relative
		}
	}

	switch name {
	case "AskUserQuestion":
		if request, err := nativeQuestions(input.Questions); err == nil {
			return request.Summary()
		}
	case "ExitPlanMode":
		return "Plan approval\n" + input.Plan
	case "EnterPlanMode":
		return "Planning"
	case "TodoWrite":
		lines := []string{"Tasks"}
		for _, todo := range input.Todos {
			lines = append(lines, "  "+todo.Status+" · "+todo.Content)
		}
		return strings.Join(lines, "\n")
	}
	for _, detail := range []string{input.FilePath, input.Command, input.Pattern, input.Subject, input.Description} {
		if detail != "" {
			return name + " · " + detail
		}
	}
	return name
}

// Management exposes the dormant capability through a native command and palette
// entry. Installing dependencies and starting sessions are explicit user actions.
func Management(settings *config.SettingsManager, agentDir string, env []string) extensions.Factory {
	env = append([]string{}, env...)
	return func(api extensions.API) error {
		limitFooter(api)
		api.RegisterCommand("claude", extensions.Command{SettingsLabel: "Claude Sessions", Description: "Claude Sessions · start or configure", Handler: func(ctx context.Context, args string, command extensions.CommandContext) error {
			if !command.HasUI() {
				return errors.New("/claude needs interactive mode; use --provider claude-sessions for headless sessions")
			}
			if strings.TrimSpace(args) == "usage" {
				return showUsage(ctx, command)
			}
			actions := []string{"New Claude session", "Model"}
			if current := command.Model(); current != nil && current.Provider == Name {
				actions = append(actions, "Usage", "Switch to Orb")
			}
			choice, ok, err := command.UI().Select(ctx, "Claude Sessions", actions, nil)
			if err != nil || !ok {
				return err
			}
			switch choice {
			case "Usage":
				return showUsage(ctx, command)
			case "Switch to Orb":
				values := map[string]string{}
				for _, item := range env {
					if key, value, ok := strings.Cut(item, "="); ok {
						values[key] = value
					}
				}
				models, e := command.ModelRegistry().AvailableWithError(values)
				if e != nil {
					return e
				}
				model := agent.PreferredAvailableModel(models)
				for _, candidate := range models {
					if string(candidate.Provider) == settings.GetDefaultProvider() && candidate.ID == settings.GetDefaultModel() {
						copy := candidate
						model = &copy
						break
					}
				}
				if model == nil {
					model = &ai.Model{Provider: "unknown", ID: "unknown"}
				}
				_, e = command.NewSession(ctx, &extensions.NewSessionOptions{Prepare: func(sm *session.SessionManager) error {
					if _, e := sm.AppendCustomEntry(Name+".exit", nil); e != nil {
						return e
					}
					_, e := sm.AppendModelChange(string(model.Provider), model.ID)
					return e
				}})
				if e != nil {
					return e
				}
			case "New Claude session":
				command.UI().Notify("Preparing Claude…", extensions.NotifyInfo)
				if _, err := configuredOptions(ctx, settings, agentDir, env); err != nil {
					return err
				}
				model := Model(ptr(Name), nil, settings)
				_, err = command.NewSession(ctx, &extensions.NewSessionOptions{Prepare: func(sm *session.SessionManager) error { _, e := sm.AppendModelChange(Name, model.ID); return e }})
			case "Model":
				command.UI().Notify("Loading Claude models…", extensions.NotifyInfo)
				options, e := configuredOptions(ctx, settings, agentDir, env)
				if e != nil {
					return e
				}
				models, e := discoverModels(ctx, options, command.CWD())
				if e != nil {
					return e
				}
				labels := make([]string, len(models))
				for i, model := range models {
					labels[i] = model.Name
				}
				value, ok, e := command.UI().Select(ctx, "Claude model for new sessions", labels, nil)
				if e != nil {
					return e
				}
				if ok {
					for i, label := range labels {
						if label == value {
							enabled := settings.GetPlugins()[Name]
							settings.SetPluginSetting(Name, "model", models[i].ID)
							settings.SetPluginEnabled(Name, enabled)
							break
						}
					}
				}
			}
			if err != nil {
				return err
			}
			for _, failure := range settings.DrainErrors() {
				return failure
			}
			return nil
		}})
		return nil
	}
}
func ptr(s string) *string { return &s }

func limitLabel(kind string) string {
	switch kind {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "7d"
	case "seven_day_opus":
		return "Opus 7d"
	case "seven_day_sonnet":
		return "Sonnet 7d"
	case "seven_day_overage_included":
		return "Extra 7d"
	case "overage":
		return "Extra"
	}
	return ""
}

// LimitsStatus formats the latest native subscription reading for local or remote UI.
func LimitsStatus(manager extensions.ReadonlySessionManager, now time.Time) string {
	text := limitsStatus(manager, now)
	if usage := nativeContextUsage(manager); usage != nil && usage.Tokens != nil {
		capacity := fmt.Sprintf("%.0fk", float64(*usage.Tokens)/1000)
		if *usage.Tokens < 1000 {
			capacity = fmt.Sprintf("%d", *usage.Tokens)
		} else if *usage.Tokens < 10000 {
			capacity = fmt.Sprintf("%.1fk", float64(*usage.Tokens)/1000)
		}
		if *usage.Tokens >= 1000000 {
			capacity = fmt.Sprintf("%.1fM", float64(*usage.Tokens)/1000000)
		}
		return fmt.Sprintf("%s · %s|%.0f%%", text, capacity, *usage.Percent)
	}
	return text
}

func nativeContextUsage(manager extensions.ReadonlySessionManager) *harness.ContextUsage {
	for entry := manager.GetLeafEntry(); entry != nil; {
		if entry.CustomType == Name+".context" {
			var usage struct {
				MaxTokens   int64
				TotalTokens *int64
				Percentage  *float64
			}
			if json.Unmarshal(entry.Data, &usage) == nil && usage.MaxTokens > 0 && usage.Percentage != nil && *usage.Percentage >= 0 && *usage.Percentage <= 100 {
				if usage.TotalTokens != nil && (*usage.TotalTokens < 0 || *usage.TotalTokens > usage.MaxTokens) {
					usage.TotalTokens = nil
				}
				return &harness.ContextUsage{Tokens: usage.TotalTokens, ContextWindow: float64(usage.MaxTokens), Percent: usage.Percentage}
			}
			break
		}
		if entry.ParentID == nil {
			break
		}
		entry = manager.GetEntry(*entry.ParentID)
	}
	return nil
}

func limitsStatus(manager extensions.ReadonlySessionManager, now time.Time) string {
	info := latestLimits(manager)
	if info != nil {
		if info.ObservedAt.IsZero() || now.Sub(info.ObservedAt) > 5*time.Minute {
			return "Claude limits stale"
		}
		windows := info.UnifiedWindows
		remaining := 101.0
		limiting := ""
		for _, key := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_overage_included", "overage"} {
			window, ok := windows[key]
			if !ok || window.ResetsAt <= now.Unix() || window.Utilization == nil || *window.Utilization < 0 || *window.Utilization > 1 {
				continue
			}
			if left := 100 * (1 - *window.Utilization); left < remaining {
				remaining, limiting = left, limitLabel(key)
			}
		}
		if remaining <= 100 {
			text := fmt.Sprintf("Claude %s %.0f%% left", limiting, remaining)
			if info.Status == "rejected" {
				text += " · limit reached"
			}
			return text
		}
		if info.ResetsAt > 0 && info.ResetsAt <= now.Unix() {
			return "Claude limits pending"
		}
		switch info.Status {
		case "allowed":
			return "Claude"
		case "allowed_warning":
			return "Claude nearing limit"
		case "rejected":
			if info.ResetsAt > now.Unix() {
				return "Claude limit reached · resets " + time.Unix(info.ResetsAt, 0).Local().Format("Mon 15:04")
			}
			return "Claude limit reached"
		}
	}
	return "Claude"
}

func limitFooter(api extensions.API) {
	var mu sync.Mutex
	var timer *time.Timer
	var generation uint64
	for _, kind := range []extensions.EventType{extensions.EventSessionStart, extensions.EventModelSelect, extensions.EventMessageEnd, extensions.EventAgentEnd, extensions.EventSessionShutdown} {
		api.On(kind, func(_ context.Context, event extensions.Event, ctx extensions.Context) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			generation++
			if timer != nil {
				timer.Stop()
				timer = nil
			}
			_, shutdown := event.(extensions.SessionShutdownEvent)
			if shutdown || ctx.Mode() != extensions.ModeTUI || !ctx.HasUI() || ctx.Model() == nil || ctx.Model().Provider != Name {
				if ctx.Mode() == extensions.ModeTUI && ctx.HasUI() {
					ctx.UI().SetStatus(Name+".limits", nil)
				}
				return nil, nil
			}
			current := generation
			var refresh func()
			refresh = func() {
				text := limitsStatus(ctx.SessionManager(), time.Now())
				ctx.UI().SetStatus(Name+".limits", &text)
				timer = time.AfterFunc(time.Minute, func() {
					mu.Lock()
					defer mu.Unlock()
					if generation == current {
						refresh()
					}
				})
			}
			refresh()
			return nil, nil
		})
	}
}

func latestLimits(manager extensions.ReadonlySessionManager) *subscriptionLimits {
	for entry := manager.GetLeafEntry(); entry != nil; {
		if entry.CustomType == Name+".limits" {
			var info subscriptionLimits
			if json.Unmarshal(entry.Data, &info) != nil {
				return nil
			}
			if info.UnifiedWindows == nil {
				info.UnifiedWindows = map[string]limitWindow{}
			}
			if _, exists := info.UnifiedWindows[info.RateLimitType]; !exists && limitLabel(info.RateLimitType) != "" {
				info.UnifiedWindows[info.RateLimitType] = info.limitWindow
			}
			return &info
		}
		if entry.ParentID == nil {
			break
		}
		entry = manager.GetEntry(*entry.ParentID)
	}
	return nil
}

func usageRows(manager extensions.ReadonlySessionManager, now time.Time) []string {
	rows := []string{}
	if usage := nativeContextUsage(manager); usage != nil && usage.Tokens != nil {
		rows = append(rows, fmt.Sprintf("Context: %d / %.0f tokens · %.1f%% used", *usage.Tokens, usage.ContextWindow, *usage.Percent))
	}
	info := latestLimits(manager)
	if info == nil {
		return append(rows, "Quota not reported yet · send a message to update")
	}
	for _, key := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_overage_included", "overage"} {
		window, ok := info.UnifiedWindows[key]
		if !ok && key != "five_hour" && key != "seven_day" {
			continue
		}
		value := "not reported"
		if ok && window.Utilization != nil && *window.Utilization >= 0 && *window.Utilization <= 1 {
			value = fmt.Sprintf("%.0f%% used · %.0f%% left", *window.Utilization*100, (1-*window.Utilization)*100)
		}
		if window.ResetsAt > 0 {
			if window.ResetsAt <= now.Unix() {
				value = "reset passed · awaiting update"
			} else {
				value += " · resets " + time.Unix(window.ResetsAt, 0).Local().Format("Mon 15:04")
			}
		}
		rows = append(rows, limitLabel(key)+": "+value)
	}
	updated := "Updated " + info.ObservedAt.Local().Format("Mon 15:04")
	if info.ObservedAt.IsZero() || now.Sub(info.ObservedAt) > 5*time.Minute {
		updated = "Stale reading · send a message to update"
	}
	return append(rows, updated, "Only limits reported by Claude are shown")
}

func showUsage(ctx context.Context, command extensions.CommandContext) error {
	_, _, err := command.UI().Select(ctx, "Claude usage", usageRows(command.SessionManager(), time.Now()), nil)
	return err
}
