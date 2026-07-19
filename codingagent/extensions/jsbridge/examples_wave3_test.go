package jsbridge

// Wave 3 of the F11 example matrix (WP-550): each upstream single-file
// example fixture runs unmodified against the Go bridge, with behavior
// driven through the same seams the TUI uses.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

func gitRun(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=pi", "GIT_AUTHOR_EMAIL=pi@example.com",
		"GIT_COMMITTER_NAME=pi", "GIT_COMMITTER_EMAIL=pi@example.com",
	)
	output, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		code = exitError.ExitCode()
	}
	return string(output), code
}

func gitMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, code := gitRun(t, dir, args...)
	if code != 0 {
		t.Fatalf("git %v exited %d:\n%s", args, code, output)
	}
	return output
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitMust(t, dir, "init")
	gitMust(t, dir, "checkout", "-b", "main")
	gitMust(t, dir, "config", "user.name", "pi")
	gitMust(t, dir, "config", "user.email", "pi@example.com")
}

func waitForNotification(t *testing.T, ui *scriptedUI, message, notificationType string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, notification := range ui.notifyList() {
			if notification[0] == message && notification[1] == notificationType {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("notification %q (%s) missing from %#v", message, notificationType, ui.notifyList())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assistantTextMessage(text string) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content:    ai.AssistantContent{&ai.TextContent{Text: text}},
		StopReason: ai.StopReasonStop,
		Timestamp:  1,
	}
}

func TestWave3ExampleAutoCommitOnExit(t *testing.T) {
	project := t.TempDir()
	initGitRepo(t, project)
	mustWrite(t, filepath.Join(project, "base.txt"), "base\n")
	gitMust(t, project, "add", "-A")
	gitMust(t, project, "commit", "-m", "base")
	mustWrite(t, filepath.Join(project, "work.txt"), "pending change\n")

	manager, err := session.Create(project, filepath.Join(project, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(assistantTextMessage("Add feature X\nDetails follow")); err != nil {
		t.Fatal(err)
	}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "auto-commit-on-exit.ts", ui, extensions.RunnerOptions{SessionManager: manager})
	runner.Emit(context.Background(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})

	subject := strings.TrimSpace(gitMust(t, project, "log", "-1", "--format=%s"))
	if subject != "[pi] Add feature X" {
		t.Fatalf("auto-commit subject = %q", subject)
	}
	if output, code := gitRun(t, project, "status", "--porcelain"); code != 0 || strings.Contains(output, "work.txt") {
		t.Fatalf("worktree not committed: %q (exit %d)", output, code)
	}
	requireNotified(t, ui, "Auto-committed: [pi] Add feature X", "info")
}

func TestWave3ExampleBookmark(t *testing.T) {
	project := t.TempDir()
	manager, err := session.Create(project, filepath.Join(project, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(assistantTextMessage("bookmark me")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	labels := map[string]*string{}
	actions := extensions.Actions{SetLabel: func(_ context.Context, id string, label *string) error {
		mu.Lock()
		labels[id] = label
		mu.Unlock()
		return nil
	}}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "bookmark.ts", ui, extensions.RunnerOptions{SessionManager: manager, Actions: actions})
	if err := runner.Command("bookmark").Handler(context.Background(), "important", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Bookmarked as: important", "info")
	mu.Lock()
	labelled := len(labels)
	var value *string
	for _, label := range labels {
		value = label
	}
	mu.Unlock()
	if labelled != 1 || value == nil || *value != "important" {
		t.Fatalf("setLabel calls = %#v", labels)
	}

	// The read-only session manager has no stored labels, so unbookmark
	// reports there is nothing to remove.
	if err := runner.Command("unbookmark").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "No bookmarked entry found", "warning")
}

func TestWave3ExampleInputTransform(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "input-transform.ts", ui, extensions.RunnerOptions{})

	result := runner.EmitInput(context.Background(), "ping", nil, extensions.InputSource("user"), nil)
	if result.Action != extensions.InputHandled {
		t.Fatalf("ping result = %#v", result)
	}
	requireNotified(t, ui, "pong", "info")

	result = runner.EmitInput(context.Background(), "?quick What is TypeScript?", nil, extensions.InputSource("user"), nil)
	if result.Action != extensions.InputTransform || result.Text != "Respond briefly in 1-2 sentences: What is TypeScript?" {
		t.Fatalf("quick result = %#v", result)
	}

	result = runner.EmitInput(context.Background(), "ping", nil, extensions.InputSource("extension"), nil)
	if result.Action != extensions.InputContinue {
		t.Fatalf("extension-source result = %#v", result)
	}
}

func TestWave3ExampleInputTransformStreaming(t *testing.T) {
	project := t.TempDir()
	initGitRepo(t, project)
	mustWrite(t, filepath.Join(project, "tracked.txt"), "one\n")
	gitMust(t, project, "add", "-A")
	gitMust(t, project, "commit", "-m", "base")
	mustWrite(t, filepath.Join(project, "tracked.txt"), "one\ntwo\n")

	ui := newScriptedUI()
	runner := loadUIExample(t, project, "input-transform-streaming.ts", ui, extensions.RunnerOptions{})

	steer := extensions.DeliverSteer
	result := runner.EmitInput(context.Background(), "look at the changes", nil, extensions.InputSource("user"), &steer)
	if result.Action != extensions.InputContinue {
		t.Fatalf("steer result = %#v", result)
	}

	result = runner.EmitInput(context.Background(), "look at the changes", nil, extensions.InputSource("user"), nil)
	if result.Action != extensions.InputTransform ||
		!strings.Contains(result.Text, "Current uncommitted changes:") ||
		!strings.Contains(result.Text, "tracked.txt") {
		t.Fatalf("transform result = %#v", result)
	}
}

func TestWave3ExampleInlineBash(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "inline-bash.ts", ui, extensions.RunnerOptions{})

	result := runner.EmitInput(context.Background(), "What's in !{echo hello}?", nil, extensions.InputSource("user"), nil)
	if result.Action != extensions.InputTransform || result.Text != "What's in hello?" {
		t.Fatalf("inline result = %#v", result)
	}
	notified := false
	for _, notification := range ui.notifyList() {
		if strings.Contains(notification[0], "Expanded 1 inline command(s)") && notification[1] == "info" {
			notified = true
		}
	}
	if !notified {
		t.Fatalf("expansion notification missing: %#v", ui.notifyList())
	}

	// Whole-line bash input is preserved untouched.
	if result := runner.EmitInput(context.Background(), "!ls", nil, extensions.InputSource("user"), nil); result.Action != extensions.InputContinue {
		t.Fatalf("whole-line bash result = %#v", result)
	}
}

func TestWave3ExampleKimiDeferredTools(t *testing.T) {
	project := t.TempDir()
	var mu sync.Mutex
	active := []string{}
	actions := extensions.Actions{
		GetActiveTools: func() ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), active...), nil
		},
		SetActiveTools: func(names []string) error {
			mu.Lock()
			active = append([]string(nil), names...)
			mu.Unlock()
			return nil
		},
	}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "kimi-deferred-tools.ts", ui, extensions.RunnerOptions{Actions: actions})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	mu.Lock()
	initial := append([]string(nil), active...)
	mu.Unlock()
	if len(initial) != 1 || initial[0] != "tool_search" {
		t.Fatalf("session_start active tools = %#v", initial)
	}

	search := runner.ToolDefinition("tool_search")
	if search == nil || search.PromptSnippet == "" {
		t.Fatalf("tool_search definition = %#v", search)
	}
	missed, err := search.Execute(context.Background(), "search-1", map[string]any{"query": "weather"}, nil, runner.CreateContext())
	if err != nil || missed.Content[0].(*ai.TextContent).Text != "The relevant tools do not exist." {
		t.Fatalf("miss result = %#v, err = %v", missed, err)
	}
	found, err := search.Execute(context.Background(), "search-2", map[string]any{"query": "calculator"}, nil, runner.CreateContext())
	if err != nil || found.Content[0].(*ai.TextContent).Text != "Success. Found 1 matching tool(s)" {
		t.Fatalf("found result = %#v, err = %v", found, err)
	}
	mu.Lock()
	expanded := append([]string(nil), active...)
	mu.Unlock()
	if len(expanded) != 2 || expanded[0] != "tool_search" || expanded[1] != "Calculator" {
		t.Fatalf("expanded active tools = %#v", expanded)
	}
	calculator := runner.ToolDefinition("Calculator")
	result, err := calculator.Execute(context.Background(), "calc-1", map[string]any{"expr": "100 + 500"}, nil, runner.CreateContext())
	if err != nil || result.Content[0].(*ai.TextContent).Text != "42" {
		t.Fatalf("calculator result = %#v, err = %v", result, err)
	}
}

func TestWave3ExampleReloadRuntime(t *testing.T) {
	project := t.TempDir()
	var mu sync.Mutex
	reloads := 0
	var sent []string
	actions := extensions.Actions{SendUserMessage: func(_ context.Context, content ai.UserContent, options *extensions.SendUserMessageOptions) error {
		mu.Lock()
		defer mu.Unlock()
		if content.Text == nil || options == nil || options.DeliverAs != extensions.DeliverFollowUp {
			t.Errorf("sendUserMessage content = %#v options = %#v", content, options)
			return nil
		}
		sent = append(sent, *content.Text)
		return nil
	}}
	commandActions := &extensions.CommandActions{Reload: func(context.Context) error {
		mu.Lock()
		reloads++
		mu.Unlock()
		return nil
	}}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "reload-runtime.ts", ui, extensions.RunnerOptions{Actions: actions, CommandActions: commandActions})
	if err := runner.Command("reload-runtime").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	reloaded := reloads
	mu.Unlock()
	if reloaded != 1 {
		t.Fatalf("reload calls = %d", reloaded)
	}

	tool := runner.ToolDefinition("reload_runtime")
	result, err := tool.Execute(context.Background(), "reload-1", map[string]any{}, nil, runner.CreateContext())
	if err != nil || result.Content[0].(*ai.TextContent).Text != "Queued /reload-runtime as a follow-up command." {
		t.Fatalf("tool result = %#v, err = %v", result, err)
	}
	mu.Lock()
	queued := append([]string(nil), sent...)
	mu.Unlock()
	if len(queued) != 1 || queued[0] != "/reload-runtime" {
		t.Fatalf("queued messages = %#v", queued)
	}
}

func TestWave3ExampleSessionName(t *testing.T) {
	project := t.TempDir()
	var mu sync.Mutex
	var name *string
	actions := extensions.Actions{
		SetSessionName: func(_ context.Context, value string) error {
			mu.Lock()
			name = &value
			mu.Unlock()
			return nil
		},
		GetSessionName: func(context.Context) (*string, error) {
			mu.Lock()
			defer mu.Unlock()
			return name, nil
		},
	}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "session-name.ts", ui, extensions.RunnerOptions{Actions: actions})
	if err := runner.Command("session-name").Handler(context.Background(), "Sprint planning", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Session named: Sprint planning", "info")
	if err := runner.Command("session-name").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Session: Sprint planning", "info")
}

func TestWave3ExampleShutdownCommand(t *testing.T) {
	project := t.TempDir()
	var mu sync.Mutex
	shutdowns := 0
	contextActions := extensions.ContextActions{Shutdown: func() {
		mu.Lock()
		shutdowns++
		mu.Unlock()
	}}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "shutdown-command.ts", ui, extensions.RunnerOptions{ContextActions: contextActions})
	if err := runner.Command("quit").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	finish := runner.ToolDefinition("finish_and_exit")
	result, err := finish.Execute(context.Background(), "finish-1", map[string]any{}, nil, runner.CreateContext())
	if err != nil || result.Content[0].(*ai.TextContent).Text != "Shutdown requested. Exiting after this response." {
		t.Fatalf("finish result = %#v, err = %v", result, err)
	}

	var updates []string
	deploy := runner.ToolDefinition("deploy_and_exit")
	deployed, err := deploy.Execute(context.Background(), "deploy-1", map[string]any{"environment": "staging"}, func(update agent.AgentToolResult) {
		updates = append(updates, update.Content[0].(*ai.TextContent).Text)
	}, runner.CreateContext())
	if err != nil || deployed.Details.(map[string]any)["environment"] != "staging" {
		t.Fatalf("deploy result = %#v, err = %v", deployed, err)
	}
	if len(updates) != 2 || updates[0] != "Deploying to staging..." || updates[1] != "Deployment complete, exiting..." {
		t.Fatalf("deploy updates = %#v", updates)
	}
	mu.Lock()
	total := shutdowns
	mu.Unlock()
	if total != 3 {
		t.Fatalf("shutdown calls = %d", total)
	}
}

func TestWave3ExampleTriggerCompact(t *testing.T) {
	project := t.TempDir()
	var mu sync.Mutex
	usage := &extensions.ContextUsage{}
	var compacts []*extensions.CompactOptions
	contextActions := extensions.ContextActions{
		GetContextUsage: func() *extensions.ContextUsage {
			mu.Lock()
			defer mu.Unlock()
			return usage
		},
		Compact: func(options *extensions.CompactOptions) {
			mu.Lock()
			compacts = append(compacts, options)
			mu.Unlock()
		},
	}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "trigger-compact.ts", ui, extensions.RunnerOptions{ContextActions: contextActions})

	setTokens := func(tokens int64) {
		mu.Lock()
		usage = &extensions.ContextUsage{Tokens: &tokens, ContextWindow: 200_000}
		mu.Unlock()
	}
	setTokens(90_000)
	runner.Emit(context.Background(), extensions.TurnEndEvent{TurnIndex: 0})
	setTokens(150_000)
	runner.Emit(context.Background(), extensions.TurnEndEvent{TurnIndex: 1})
	mu.Lock()
	automatic := len(compacts)
	mu.Unlock()
	if automatic != 1 {
		t.Fatalf("threshold crossing triggered %d compactions", automatic)
	}
	requireNotified(t, ui, "Compaction started", "info")

	if err := runner.Command("trigger-compact").Handler(context.Background(), "keep the decisions", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	manual := compacts[len(compacts)-1]
	mu.Unlock()
	if manual.CustomInstructions != "keep the decisions" || manual.OnComplete == nil || manual.OnError == nil {
		t.Fatalf("manual compact options = %#v", manual)
	}
	manual.OnComplete(harness.CompactionResult{Summary: "done", TokensBefore: 150_000})
	waitForNotification(t, ui, "Compaction completed", "info")
	manual.OnError(context.DeadlineExceeded)
	waitForNotification(t, ui, "Compaction failed: context deadline exceeded", "error")
}

func TestWave3ExamplePromptCustomizer(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "prompt-customizer.ts", ui, extensions.RunnerOptions{})
	appendPrompt := "User appended instructions"
	result := runner.EmitBeforeAgentStart(context.Background(), "prompt", nil, "base prompt", extensions.SystemPromptOptions{
		CWD:                project,
		SelectedTools:      []string{"read", "bash"},
		AppendSystemPrompt: &appendPrompt,
		Skills:             []extensions.Skill{{Name: "release-notes", Description: "writes release notes"}},
	})
	if result == nil || result.SystemPrompt == nil {
		t.Fatalf("prompt-customizer result = %#v", result)
	}
	prompt := *result.SystemPrompt
	for _, expected := range []string{
		"base prompt",
		"## Tool Guidance",
		"Use the `read` tool for file contents",
		"Execute commands with the `bash` tool",
		"Available skills: release-notes",
		"User appended instructions",
		"## Extension-Added Context",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("system prompt missing %q:\n%s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "`edit` tool") {
		t.Fatalf("guidance for inactive tools leaked:\n%s", prompt)
	}
}

func TestWave3ExampleProviderPayload(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "provider-payload.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.BeforeProviderRequestEvent{Payload: map[string]any{"model": "model-1", "temperature": 1}})
	runner.Emit(context.Background(), extensions.AfterProviderResponseEvent{Status: 200, Headers: map[string]string{"x-request-id": "req-1"}})
	content, err := os.ReadFile(filepath.Join(project, ".pi", "provider-payload.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(content)
	if !strings.Contains(log, `"model": "model-1"`) || !strings.Contains(log, "[200]") || !strings.Contains(log, "x-request-id") {
		t.Fatalf("provider payload log = %q", log)
	}
}

func TestWave3ExampleNotify(t *testing.T) {
	t.Setenv("WT_SESSION", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "notify.ts", ui, extensions.RunnerOptions{})

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	runner.Emit(context.Background(), extensions.AgentEndEvent{})
	os.Stdout = previous
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 256)
	length, _ := read.Read(buffer)
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	if written := string(buffer[:length]); written != "\x1b]777;notify;Pi;Ready for input\x07" {
		t.Fatalf("terminal notification = %q", written)
	}
}

func TestWave3ExampleEntryRenderer(t *testing.T) {
	project := t.TempDir()
	var mu sync.Mutex
	var appendedType string
	var appendedData any
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, data any) error {
		mu.Lock()
		appendedType = customType
		appendedData = data
		mu.Unlock()
		return nil
	}}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "entry-renderer.ts", ui, extensions.RunnerOptions{Actions: actions})
	if err := runner.Command("status-card").Handler(context.Background(), "deploy is green", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	data, _ := appendedData.(map[string]any)
	entryType := appendedType
	mu.Unlock()
	if entryType != "status-card" || data == nil || data["message"] != "deploy is green" {
		t.Fatalf("appended entry = %q %#v", entryType, appendedData)
	}

	renderer := runner.EntryRenderer("status-card")
	if renderer == nil {
		t.Fatal("entry renderer was not registered")
	}
	component := renderer(map[string]any{
		"type": "custom", "customType": "status-card",
		"data": map[string]any{"message": "deploy is green", "timestamp": 1700000000000},
	}, extensions.EntryRenderOptions{Expanded: true}, tagTheme{})
	if component == nil {
		t.Fatal("entry renderer returned no component")
	}
	rendered := strings.Join(component.Render(60), "\n")
	if !strings.Contains(rendered, "<accent>[status]</fg> deploy is green") || !strings.Contains(rendered, "[customMessageBg]") {
		t.Fatalf("entry render = %q", rendered)
	}
	// Expanded rendering appends the timestamp line.
	if !strings.Contains(rendered, "<dim>") {
		t.Fatalf("expanded timestamp missing: %q", rendered)
	}
}

func TestWave3ExampleMessageRenderer(t *testing.T) {
	project := t.TempDir()
	var mu sync.Mutex
	var sent []extensions.CustomMessage
	actions := extensions.Actions{SendMessage: func(_ context.Context, message extensions.CustomMessage, _ *extensions.SendMessageOptions) error {
		mu.Lock()
		sent = append(sent, message)
		mu.Unlock()
		return nil
	}}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "message-renderer.ts", ui, extensions.RunnerOptions{Actions: actions})
	if err := runner.Command("status").Handler(context.Background(), "warn deploy pending", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	message := sent[len(sent)-1]
	mu.Unlock()
	if message.CustomType != "status-update" || message.Content != "deploy pending" || !message.Display {
		t.Fatalf("sent message = %#v", message)
	}
	details, _ := message.Details.(map[string]any)
	if details == nil || details["level"] != "warn" {
		t.Fatalf("message details = %#v", message.Details)
	}

	renderer := runner.MessageRenderer("status-update")
	if renderer == nil {
		t.Fatal("message renderer was not registered")
	}
	rendered := strings.Join(renderer(message, extensions.MessageRenderOptions{Expanded: true}, tagTheme{}).Render(60), "\n")
	if !strings.Contains(rendered, "<warning>[WARN]</fg> deploy pending") ||
		!strings.Contains(rendered, "at ") ||
		!strings.Contains(rendered, "[customMessageBg]") {
		t.Fatalf("message render = %q", rendered)
	}
}

func TestWave3ExampleCustomCompaction(t *testing.T) {
	project := t.TempDir()
	preparation := harness.CompactionPreparation{
		FirstKeptEntryID: "entry-7",
		MessagesToSummarize: agent.AgentMessages{
			&ai.UserMessage{Content: ai.NewUserText("please fix the bug"), Timestamp: 1},
		},
		TokensBefore: 4200,
	}

	// Without the summarization model the extension defers to default compaction.
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "custom-compaction.ts", ui, extensions.RunnerOptions{ModelRegistry: &bridgeModelRegistry{}})
	if result := runner.Emit(context.Background(), extensions.SessionBeforeCompactEvent{
		Preparation: preparation,
		Reason:      extensions.CompactionReason("manual"),
		Signal:      context.Background(),
	}); result != nil {
		t.Fatalf("missing-model compaction result = %#v", result)
	}
	requireNotified(t, ui, "Custom compaction extension triggered", "info")
	requireNotified(t, ui, "Could not find Gemini Flash model, using default compaction", "warning")

	// With the model but no API key it reports the auth gap and defers.
	registry := &bridgeModelRegistry{models: []ai.Model{{Provider: "google", ID: "gemini-2.5-flash"}}}
	keylessUI := newScriptedUI()
	keylessRunner := loadUIExample(t, project, "custom-compaction.ts", keylessUI, extensions.RunnerOptions{ModelRegistry: registry})
	if result := keylessRunner.Emit(context.Background(), extensions.SessionBeforeCompactEvent{
		Preparation: preparation,
		Reason:      extensions.CompactionReason("manual"),
		Signal:      context.Background(),
	}); result != nil {
		t.Fatalf("keyless compaction result = %#v", result)
	}
	requireNotified(t, keylessUI, "No API key for google, using default compaction", "warning")
}

func TestWave3ExampleQna(t *testing.T) {
	project := t.TempDir()
	manager, err := session.Create(project, filepath.Join(project, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(assistantTextMessage("Should we use Postgres or SQLite?")); err != nil {
		t.Fatal(err)
	}

	// Without a model the command refuses to run.
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "qna.ts", ui, extensions.RunnerOptions{SessionManager: manager, ModelRegistry: &bridgeModelRegistry{}})
	if err := runner.Command("qna").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "No model selected", "error")

	// With a model but no API key the loader flow cancels cleanly.
	model := ai.Model{Provider: "provider-1", ID: "model-1"}
	modelUI := newScriptedUI()
	modelRunner := loadUIExample(t, project, "qna.ts", modelUI, extensions.RunnerOptions{
		SessionManager: manager,
		ModelRegistry:  &bridgeModelRegistry{models: []ai.Model{model}},
		ContextActions: extensions.ContextActions{GetModel: func() *ai.Model { return &model }},
	})
	var loaderRender string
	modelUI.customDrive = func(component extensions.Component) {
		loaderRender = strings.Join(component.Render(60), "\n")
	}
	if err := modelRunner.Command("qna").Handler(context.Background(), "", modelRunner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	waitForNotification(t, modelUI, "Cancelled", "info")
	if !strings.Contains(loaderRender, "Extracting questions using") || !strings.Contains(loaderRender, "<border>") {
		t.Fatalf("bordered loader render = %q", loaderRender)
	}
	if modelUI.GetEditorText() != "" {
		t.Fatalf("editor text set on cancelled run: %q", modelUI.GetEditorText())
	}
}

func TestWave3ExampleHandoff(t *testing.T) {
	project := t.TempDir()
	manager, err := session.Create(project, filepath.Join(project, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText("build the exporter"), Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(assistantTextMessage("Exporter scaffolding is done")); err != nil {
		t.Fatal(err)
	}
	model := ai.Model{Provider: "provider-1", ID: "model-1"}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "handoff.ts", ui, extensions.RunnerOptions{
		SessionManager: manager,
		ModelRegistry:  &bridgeModelRegistry{models: []ai.Model{model}},
		ContextActions: extensions.ContextActions{GetModel: func() *ai.Model { return &model }},
	})
	var loaderRender string
	ui.customDrive = func(component extensions.Component) {
		loaderRender = strings.Join(component.Render(60), "\n")
	}
	// convertToLlm + serializeConversation run before the loader; the keyless
	// registry makes generation fail, which cancels the handoff.
	if err := runner.Command("handoff").Handler(context.Background(), "ship the exporter", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	waitForNotification(t, ui, "Cancelled", "info")
	if !strings.Contains(loaderRender, "Generating handoff prompt...") {
		t.Fatalf("bordered loader render = %q", loaderRender)
	}
}

func TestWave3ExampleOverlayTest(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "overlay-test.ts", ui, extensions.RunnerOptions{})
	var rendered string
	ui.customDrive = func(component extensions.Component) {
		rendered = strings.Join(component.Render(80), "\n")
		input, ok := component.(interface{ HandleInput(string) })
		if !ok {
			t.Error("overlay component does not accept input")
			return
		}
		input.HandleInput("h")
		input.HandleInput("i")
		input.HandleInput("\r")
	}
	if err := runner.Command("overlay-test").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, `Search: "hi"`, "info")
	if !strings.Contains(rendered, "Overlay Test") || !strings.Contains(rendered, "╭") {
		t.Fatalf("overlay render = %q", rendered)
	}
	ui.mu.Lock()
	options := ui.customOptions[len(ui.customOptions)-1]
	ui.mu.Unlock()
	if options == nil || !options.Overlay {
		t.Fatalf("custom options = %#v", options)
	}
}

func TestWave3ExampleTicTacToe(t *testing.T) {
	project := t.TempDir()
	manager, err := session.Create(project, filepath.Join(project, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sessionName string
	actions := extensions.Actions{SetSessionName: func(_ context.Context, value string) error {
		mu.Lock()
		sessionName = value
		mu.Unlock()
		return nil
	}}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "tic-tac-toe.ts", ui, extensions.RunnerOptions{SessionManager: manager, Actions: actions})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})

	var boardRender string
	ui.customDrive = func(component extensions.Component) {
		boardRender = strings.Join(component.Render(60), "\n")
		if input, ok := component.(interface{ HandleInput(string) }); ok {
			input.HandleInput("q")
		}
	}
	if err := runner.Command("tic-tac-toe").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	named := sessionName
	mu.Unlock()
	if named != "Tic-Tac-Toe" {
		t.Fatalf("session name = %q", named)
	}
	if boardRender == "" {
		t.Fatal("game board never rendered")
	}

	renderer := runner.MessageRenderer("tic-tac-toe-move")
	if renderer == nil {
		t.Fatal("move message renderer was not registered")
	}
	board := []any{[]any{"X", nil, nil}, []any{nil, nil, nil}, []any{nil, nil, nil}}
	component := renderer(extensions.CustomMessage{
		CustomType: "tic-tac-toe-move",
		Content:    "Player X marked (0,0)",
		Display:    true,
		Details: map[string]any{
			"board": board, "agentCursorRow": 1, "agentCursorCol": 1,
			"status": "playing", "currentTurn": "O",
		},
	}, extensions.MessageRenderOptions{Expanded: false}, tagTheme{})
	if component == nil {
		t.Fatal("move renderer returned no component")
	}
	rendered := strings.Join(component.Render(60), "\n")
	if !strings.Contains(rendered, "Player X played") {
		t.Fatalf("move banner = %q", rendered)
	}
}

func TestWave3ExampleTruncatedTool(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	project := t.TempDir()
	small := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		small = append(small, "alpha match line")
	}
	mustWrite(t, filepath.Join(project, "small.txt"), strings.Join(small, "\n")+"\n")

	ui := newScriptedUI()
	runner := loadUIExample(t, project, "truncated-tool.ts", ui, extensions.RunnerOptions{})
	tool := runner.ToolDefinition("rg")
	if tool == nil || !strings.Contains(tool.Description, "2000 lines or 50.0KB") {
		t.Fatalf("rg tool definition = %#v", tool)
	}

	result, err := tool.Execute(context.Background(), "rg-1", map[string]any{"pattern": "alpha", "glob": "small.txt"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(*ai.TextContent).Text
	if !strings.Contains(text, "alpha match line") || strings.Contains(text, "[Output truncated") {
		t.Fatalf("small search result = %q", text)
	}
	details := result.Details.(map[string]any)
	if details["matchCount"] != int64(30) && details["matchCount"] != float64(30) {
		t.Fatalf("match count = %#v", details["matchCount"])
	}

	big := make([]string, 0, 2500)
	for i := 0; i < 2500; i++ {
		big = append(big, "beta overflow line")
	}
	mustWrite(t, filepath.Join(project, "big.txt"), strings.Join(big, "\n")+"\n")
	truncated, err := tool.Execute(context.Background(), "rg-2", map[string]any{"pattern": "beta", "glob": "big.txt"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	truncatedText := truncated.Content[0].(*ai.TextContent).Text
	if !strings.Contains(truncatedText, "[Output truncated: showing ") ||
		!strings.Contains(truncatedText, " of 2500 lines") ||
		!strings.Contains(truncatedText, "Full output saved to: ") {
		t.Fatalf("truncation notice missing: %q", truncatedText[len(truncatedText)-300:])
	}
	truncatedDetails := truncated.Details.(map[string]any)
	fullPath, _ := truncatedDetails["fullOutputPath"].(string)
	if fullPath == "" {
		t.Fatalf("full output path missing: %#v", truncatedDetails)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(fullPath)) })
	full, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(full), "\n"); lines != 2500 {
		t.Fatalf("full output line count = %d", lines)
	}

	missing, err := tool.Execute(context.Background(), "rg-3", map[string]any{"pattern": "nomatchhere", "glob": "small.txt"}, nil, runner.CreateContext())
	if err != nil || missing.Content[0].(*ai.TextContent).Text != "No matches found" {
		t.Fatalf("no-match result = %#v, err = %v", missing, err)
	}
}

func TestWave3ExampleGitMergeAndResolve(t *testing.T) {
	project := t.TempDir()
	initGitRepo(t, project)
	mustWrite(t, filepath.Join(project, "file.txt"), "line1\nline2\nline3\n")
	gitMust(t, project, "add", "-A")
	gitMust(t, project, "commit", "-m", "base")
	gitMust(t, project, "checkout", "-b", "feature")
	mustWrite(t, filepath.Join(project, "file.txt"), "line1\nfeature\nline3\n")
	gitMust(t, project, "commit", "-am", "feature change")
	gitMust(t, project, "checkout", "main")
	mustWrite(t, filepath.Join(project, "file.txt"), "line1\nmain\nline3\n")
	gitMust(t, project, "commit", "-am", "main change")
	if _, code := gitRun(t, project, "merge", "feature"); code == 0 {
		t.Fatal("expected a merge conflict")
	}

	var mu sync.Mutex
	var sent []string
	actions := extensions.Actions{SendUserMessage: func(_ context.Context, content ai.UserContent, options *extensions.SendUserMessageOptions) error {
		mu.Lock()
		defer mu.Unlock()
		if content.Text != nil && options != nil && options.DeliverAs == extensions.DeliverFollowUp {
			sent = append(sent, *content.Text)
		}
		return nil
	}}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "git-merge-and-resolve.ts", ui, extensions.RunnerOptions{Actions: actions})
	runner.Emit(context.Background(), extensions.AgentEndEvent{})
	mu.Lock()
	messages := append([]string(nil), sent...)
	mu.Unlock()
	if len(messages) != 1 {
		t.Fatalf("follow-up messages = %#v", messages)
	}
	if !strings.Contains(messages[0], "Merged MERGE_HEAD with conflicts:") ||
		!strings.Contains(messages[0], "file.txt:2-6 (ours 3, theirs 5)") ||
		!strings.Contains(messages[0], "Resolve these conflicts.") {
		t.Fatalf("conflict message = %q", messages[0])
	}
}
