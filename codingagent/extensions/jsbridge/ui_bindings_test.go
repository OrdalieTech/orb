package jsbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
)

// --- fakes backing the extensions.UI seam ---

type dialogCall struct {
	title       string
	message     string
	options     []string
	placeholder *string
	prefill     *string
	opts        *extensions.DialogOptions
}

type scriptedUI struct {
	extensions.NoopUI
	mu sync.Mutex

	notifications     [][2]string
	titles            []string
	statuses          map[string][]*string
	statusOrder       []string
	widgets           map[string][]*extensions.Widget
	widgetOptions     map[string][]*extensions.WidgetOptions
	workingMessages   []*string
	workingVisible    []bool
	workingIndicators []*extensions.WorkingIndicatorOptions
	thinkingLabels    []*string
	footers           []extensions.FooterFactory
	headers           []extensions.HeaderFactory
	editorTexts       []string
	pastes            []string
	editorText        string
	toolsExpanded     bool
	acFactories       []extensions.AutocompleteProviderFactory
	terminalHandlers  []extensions.TerminalInputHandler
	terminalUnsubs    int
	setThemeInputs    []any
	customOptions     []*extensions.CustomOptions
	customDrive       func(extensions.Component)
	customCancel      bool
	editorFactories   []extensions.EditorFactory

	selectCalls  []dialogCall
	confirmCalls []dialogCall
	inputCalls   []dialogCall
	editorCalls  []dialogCall

	selectFn  func(title string, options []string, opts *extensions.DialogOptions) (string, bool)
	confirmFn func(title, message string, opts *extensions.DialogOptions) bool
	inputFn   func(title string, placeholder *string, opts *extensions.DialogOptions) (string, bool)
	editorFn  func(title string, prefill *string) (string, bool)
	dialogErr error

	theme          extensions.Theme
	allThemes      []extensions.ThemeInfo
	themesByName   map[string]extensions.Theme
	setThemeResult extensions.ThemeSetResult
}

func newScriptedUI() *scriptedUI {
	return &scriptedUI{
		statuses:       make(map[string][]*string),
		widgets:        make(map[string][]*extensions.Widget),
		widgetOptions:  make(map[string][]*extensions.WidgetOptions),
		themesByName:   make(map[string]extensions.Theme),
		setThemeResult: extensions.ThemeSetResult{Success: true},
	}
}

func (ui *scriptedUI) Select(_ context.Context, title string, options []string, opts *extensions.DialogOptions) (string, bool, error) {
	ui.mu.Lock()
	ui.selectCalls = append(ui.selectCalls, dialogCall{title: title, options: append([]string(nil), options...), opts: opts})
	handler := ui.selectFn
	dialogErr := ui.dialogErr
	ui.mu.Unlock()
	if dialogErr != nil {
		return "", false, dialogErr
	}
	if handler == nil {
		return "", false, nil
	}
	value, ok := handler(title, options, opts)
	return value, ok, nil
}

func (ui *scriptedUI) Confirm(_ context.Context, title, message string, opts *extensions.DialogOptions) (bool, error) {
	ui.mu.Lock()
	ui.confirmCalls = append(ui.confirmCalls, dialogCall{title: title, message: message, opts: opts})
	handler := ui.confirmFn
	dialogErr := ui.dialogErr
	ui.mu.Unlock()
	if dialogErr != nil {
		return false, dialogErr
	}
	if handler == nil {
		return false, nil
	}
	return handler(title, message, opts), nil
}

func (ui *scriptedUI) Input(_ context.Context, title string, placeholder *string, opts *extensions.DialogOptions) (string, bool, error) {
	ui.mu.Lock()
	ui.inputCalls = append(ui.inputCalls, dialogCall{title: title, placeholder: placeholder, opts: opts})
	handler := ui.inputFn
	dialogErr := ui.dialogErr
	ui.mu.Unlock()
	if dialogErr != nil {
		return "", false, dialogErr
	}
	if handler == nil {
		return "", false, nil
	}
	value, ok := handler(title, placeholder, opts)
	return value, ok, nil
}

func (ui *scriptedUI) Editor(_ context.Context, title string, prefill *string) (string, bool, error) {
	ui.mu.Lock()
	ui.editorCalls = append(ui.editorCalls, dialogCall{title: title, prefill: prefill})
	handler := ui.editorFn
	dialogErr := ui.dialogErr
	ui.mu.Unlock()
	if dialogErr != nil {
		return "", false, dialogErr
	}
	if handler == nil {
		return "", false, nil
	}
	value, ok := handler(title, prefill)
	return value, ok, nil
}

func (ui *scriptedUI) Notify(message string, notificationType extensions.NotificationType) {
	ui.mu.Lock()
	ui.notifications = append(ui.notifications, [2]string{message, string(notificationType)})
	ui.mu.Unlock()
}

func (ui *scriptedUI) OnTerminalInput(handler extensions.TerminalInputHandler) func() {
	ui.mu.Lock()
	ui.terminalHandlers = append(ui.terminalHandlers, handler)
	ui.mu.Unlock()
	return func() {
		ui.mu.Lock()
		ui.terminalUnsubs++
		ui.mu.Unlock()
	}
}

func (ui *scriptedUI) SetStatus(key string, text *string) {
	ui.mu.Lock()
	ui.statuses[key] = append(ui.statuses[key], text)
	ui.statusOrder = append(ui.statusOrder, key)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetWorkingMessage(message *string) {
	ui.mu.Lock()
	ui.workingMessages = append(ui.workingMessages, message)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetWorkingVisible(visible bool) {
	ui.mu.Lock()
	ui.workingVisible = append(ui.workingVisible, visible)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetWorkingIndicator(options *extensions.WorkingIndicatorOptions) {
	ui.mu.Lock()
	ui.workingIndicators = append(ui.workingIndicators, options)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetHiddenThinkingLabel(label *string) {
	ui.mu.Lock()
	ui.thinkingLabels = append(ui.thinkingLabels, label)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetWidget(key string, widget *extensions.Widget, options *extensions.WidgetOptions) {
	ui.mu.Lock()
	ui.widgets[key] = append(ui.widgets[key], widget)
	ui.widgetOptions[key] = append(ui.widgetOptions[key], options)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetFooter(factory extensions.FooterFactory) {
	ui.mu.Lock()
	ui.footers = append(ui.footers, factory)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetHeader(factory extensions.HeaderFactory) {
	ui.mu.Lock()
	ui.headers = append(ui.headers, factory)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetTitle(title string) {
	ui.mu.Lock()
	ui.titles = append(ui.titles, title)
	ui.mu.Unlock()
}

func (ui *scriptedUI) PasteToEditor(text string) {
	ui.mu.Lock()
	ui.pastes = append(ui.pastes, text)
	ui.mu.Unlock()
}

func (ui *scriptedUI) SetEditorText(text string) {
	ui.mu.Lock()
	ui.editorTexts = append(ui.editorTexts, text)
	ui.editorText = text
	ui.mu.Unlock()
}

func (ui *scriptedUI) GetEditorText() string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.editorText
}

func (ui *scriptedUI) AddAutocompleteProvider(factory extensions.AutocompleteProviderFactory) {
	ui.mu.Lock()
	ui.acFactories = append(ui.acFactories, factory)
	ui.mu.Unlock()
}

func (ui *scriptedUI) Theme() extensions.Theme {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if ui.theme == nil {
		return tagTheme{}
	}
	return ui.theme
}

func (ui *scriptedUI) GetAllThemes() []extensions.ThemeInfo {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return append([]extensions.ThemeInfo(nil), ui.allThemes...)
}

func (ui *scriptedUI) GetTheme(name string) extensions.Theme {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.themesByName[name]
}

func (ui *scriptedUI) SetTheme(value any) extensions.ThemeSetResult {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.setThemeInputs = append(ui.setThemeInputs, value)
	return ui.setThemeResult
}

func (ui *scriptedUI) GetToolsExpanded() bool {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.toolsExpanded
}

func (ui *scriptedUI) SetToolsExpanded(expanded bool) {
	ui.mu.Lock()
	ui.toolsExpanded = expanded
	ui.mu.Unlock()
}

func (ui *scriptedUI) notifyList() [][2]string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return append([][2]string(nil), ui.notifications...)
}

func (ui *scriptedUI) lastStatus(t *testing.T, key string) *string {
	t.Helper()
	ui.mu.Lock()
	defer ui.mu.Unlock()
	history := ui.statuses[key]
	if len(history) == 0 {
		t.Fatalf("status %q was never set", key)
	}
	return history[len(history)-1]
}

func (ui *scriptedUI) titleList() []string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return append([]string(nil), ui.titles...)
}

func requireNotified(t *testing.T, ui *scriptedUI, message, notificationType string) {
	t.Helper()
	for _, notification := range ui.notifyList() {
		if notification[0] == message && notification[1] == notificationType {
			return
		}
	}
	t.Fatalf("notification %q (%s) missing from %#v", message, notificationType, ui.notifyList())
}

type tagTheme struct{}

func (tagTheme) FG(color, text string) string { return "<" + color + ">" + text + "</fg>" }

func (tagTheme) BG(color, text string) string { return "[" + color + "]" + text + "[/bg]" }

func (tagTheme) Bold(text string) string { return "**" + text + "**" }

func (tagTheme) Italic(text string) string { return "_" + text + "_" }

func (tagTheme) Underline(text string) string { return "__" + text + "__" }

func (tagTheme) Inverse(text string) string { return "!" + text + "!" }

func (tagTheme) Strikethrough(text string) string { return "~~" + text + "~~" }

func (tagTheme) FGANSI(color string) string { return "fg-ansi:" + color }

func (tagTheme) BGANSI(color string) string { return "bg-ansi:" + color }

func (tagTheme) ColorMode() string { return "truecolor" }

func (tagTheme) ThinkingBorderColor(level agent.ThinkingLevel) func(string) string {
	return func(text string) string { return "think(" + string(level) + "):" + text }
}

func (tagTheme) BashModeBorderColor() func(string) string {
	return func(text string) string { return "bash:" + text }
}

type stubHost struct {
	width, height int
	invalidations atomic.Int32
}

func (host *stubHost) Width() int { return host.width }

func (host *stubHost) Height() int { return host.height }

func (host *stubHost) Invalidate() { host.invalidations.Add(1) }

type stubFooterData struct{ branch string }

func (data stubFooterData) GitBranch() string { return data.branch }

func (data stubFooterData) Statuses() map[string]string {
	return map[string]string{"model": "ready", "demo": "on"}
}

type stubAutocompleteProvider struct {
	mu          sync.Mutex
	requests    []extensions.AutocompleteRequest
	suggestions *extensions.AutocompleteResult
}

func (provider *stubAutocompleteProvider) TriggerCharacters() []string { return []string{"@", "/"} }

func (provider *stubAutocompleteProvider) GetSuggestions(_ context.Context, request extensions.AutocompleteRequest) (*extensions.AutocompleteResult, error) {
	provider.mu.Lock()
	provider.requests = append(provider.requests, request)
	provider.mu.Unlock()
	return provider.suggestions, nil
}

func (provider *stubAutocompleteProvider) ApplyCompletion(request extensions.AutocompleteRequest, item extensions.AutocompleteItem, prefix string) ([]string, int, int) {
	return append([]string(nil), "applied:"+item.Value+":"+prefix), request.CursorLine + 1, request.CursorCol + 2
}

func (provider *stubAutocompleteProvider) ShouldTriggerFileCompletion(extensions.AutocompleteRequest) bool {
	return true
}

func (provider *stubAutocompleteProvider) requestCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.requests)
}

func loadUIExample(t *testing.T, cwd, name string, ui extensions.UI, options extensions.RunnerOptions) *extensions.Runner {
	t.Helper()
	options.UI = ui
	options.Mode = extensions.ModeTUI
	return loadBridgeRunner(t, cwd, []bridgeSource{{name, fixtureSource(t, name)}}, options)
}

func stringPointerValue(t *testing.T, value *string) string {
	t.Helper()
	if value == nil {
		t.Fatal("expected string value, got nil")
	}
	return *value
}

// --- upstream examples run unmodified against the ui surface ---

func TestUIExampleConfirmDestructive(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	ui.confirmFn = func(string, string, *extensions.DialogOptions) bool { return false }
	ui.selectFn = func(_ string, options []string, _ *extensions.DialogOptions) (string, bool) {
		return options[1], true
	}
	runner := loadUIExample(t, project, "confirm-destructive.ts", ui, extensions.RunnerOptions{})
	result := runner.Emit(context.Background(), extensions.SessionBeforeSwitchEvent{Reason: extensions.SessionSwitchReason("new")})
	switchResult, ok := result.(extensions.SessionBeforeSwitchResult)
	if !ok || !switchResult.Cancel {
		t.Fatalf("declined clear result = %#v", result)
	}
	requireNotified(t, ui, "Clear cancelled", "info")
	ui.mu.Lock()
	confirmed := ui.confirmCalls[len(ui.confirmCalls)-1]
	ui.mu.Unlock()
	if confirmed.title != "Clear session?" || confirmed.message != "This will delete all messages in the current session." {
		t.Fatalf("confirm call = %#v", confirmed)
	}

	ui.mu.Lock()
	ui.confirmFn = func(string, string, *extensions.DialogOptions) bool { return true }
	ui.mu.Unlock()
	if result := runner.Emit(context.Background(), extensions.SessionBeforeSwitchEvent{Reason: extensions.SessionSwitchReason("new")}); result != nil {
		t.Fatalf("accepted clear result = %#v", result)
	}

	forkResult := runner.Emit(context.Background(), extensions.SessionBeforeForkEvent{EntryID: "entry-12345678"})
	forked, ok := forkResult.(extensions.SessionBeforeForkResult)
	if !ok || !forked.Cancel {
		t.Fatalf("declined fork result = %#v", forkResult)
	}
	requireNotified(t, ui, "Fork cancelled", "info")
	ui.mu.Lock()
	selected := ui.selectCalls[len(ui.selectCalls)-1]
	ui.mu.Unlock()
	if selected.title != "Fork from entry entry-12?" || len(selected.options) != 2 || selected.options[0] != "Yes, create fork" {
		t.Fatalf("fork select call = %#v", selected)
	}
}

func TestUIExampleCustomFooter(t *testing.T) {
	project := t.TempDir()
	manager, err := session.Create(project, filepath.Join(project, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "custom-footer.ts", ui, extensions.RunnerOptions{SessionManager: manager})
	if err := runner.Command("footer").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Custom footer enabled", "info")
	ui.mu.Lock()
	factory := ui.footers[len(ui.footers)-1]
	ui.mu.Unlock()
	if factory == nil {
		t.Fatal("custom footer factory was not registered")
	}
	host := &stubHost{width: 100, height: 30}
	component := factory(host, tagTheme{}, stubFooterData{branch: "main"})
	if component == nil {
		t.Fatal("footer factory returned no component")
	}
	lines := component.Render(80)
	if len(lines) != 1 || !strings.Contains(lines[0], "no-model") || !strings.Contains(lines[0], "(main)") {
		t.Fatalf("footer render = %#v", lines)
	}
	disposable, ok := component.(extensions.DisposableComponent)
	if !ok {
		t.Fatal("footer component is not disposable")
	}
	disposable.Dispose()

	if err := runner.Command("footer").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Default footer restored", "info")
	ui.mu.Lock()
	restored := ui.footers[len(ui.footers)-1]
	ui.mu.Unlock()
	if restored != nil {
		t.Fatal("setFooter(undefined) did not clear the factory")
	}
}

func TestUIExampleCustomHeader(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "custom-header.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	ui.mu.Lock()
	factoryCount := len(ui.headers)
	var factory extensions.HeaderFactory
	if factoryCount > 0 {
		factory = ui.headers[factoryCount-1]
	}
	ui.mu.Unlock()
	if factory == nil {
		t.Fatal("custom header factory was not registered")
	}
	component := factory(&stubHost{width: 80, height: 24}, tagTheme{})
	if component == nil {
		t.Fatal("header factory returned no component")
	}
	joined := strings.Join(component.Render(80), "\n")
	if !strings.Contains(joined, "shitty coding agent") {
		t.Fatalf("header render = %q", joined)
	}
	if err := runner.Command("builtin-header").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Built-in header restored", "info")
	ui.mu.Lock()
	restored := ui.headers[len(ui.headers)-1]
	ui.mu.Unlock()
	if restored != nil {
		t.Fatal("setHeader(undefined) did not clear the factory")
	}
}

func TestUIExampleGithubIssueAutocomplete(t *testing.T) {
	project := t.TempDir()
	binDir := filepath.Join(project, "fake-bin")
	writeFakeExecutable(t, filepath.Join(binDir, "git"), "#!/bin/sh\necho \"origin\tgit@github.com:acme/demo.git (fetch)\"\nexit 0\n")
	writeFakeExecutable(t, filepath.Join(binDir, "gh"), "#!/bin/sh\necho \"gh unavailable\" >&2\nexit 1\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ui := newScriptedUI()
	runner := loadUIExample(t, project, "github-issue-autocomplete.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	ui.mu.Lock()
	factoryCount := len(ui.acFactories)
	var factory extensions.AutocompleteProviderFactory
	if factoryCount > 0 {
		factory = ui.acFactories[0]
	}
	ui.mu.Unlock()
	if factoryCount != 1 || factory == nil {
		t.Fatalf("autocomplete factories registered = %d", factoryCount)
	}
	current := &stubAutocompleteProvider{suggestions: &extensions.AutocompleteResult{
		Prefix: "cur",
		Items:  []extensions.AutocompleteItem{{Value: "one", Label: "One"}},
	}}
	provider := factory(current)
	if provider == nil {
		t.Fatal("autocomplete factory returned no provider")
	}

	// #-token path: gh fails, so the example falls back to the current provider.
	request := extensions.AutocompleteRequest{Lines: []string{"see #1"}, CursorLine: 0, CursorCol: 6, Signal: context.Background()}
	suggestions, err := provider.GetSuggestions(context.Background(), request)
	if err != nil || suggestions == nil || suggestions.Prefix != "cur" {
		t.Fatalf("fallback suggestions = %#v, err = %v", suggestions, err)
	}
	// No token: delegates immediately.
	if _, err := provider.GetSuggestions(context.Background(), extensions.AutocompleteRequest{Lines: []string{"hello"}, CursorLine: 0, CursorCol: 5, Signal: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if current.requestCount() != 2 {
		t.Fatalf("current provider calls = %d", current.requestCount())
	}
	lines, cursorLine, cursorCol := provider.ApplyCompletion(request, extensions.AutocompleteItem{Value: "#12", Label: "#12"}, "#1")
	if len(lines) != 1 || lines[0] != "applied:#12:#1" || cursorLine != 1 || cursorCol != 8 {
		t.Fatalf("applyCompletion = %#v, %d, %d", lines, cursorLine, cursorCol)
	}
	if !provider.ShouldTriggerFileCompletion(request) {
		t.Fatal("shouldTriggerFileCompletion did not delegate to the current provider")
	}
}

func TestUIExampleHiddenThinkingLabel(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "hidden-thinking-label.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	if got := stringPointerValue(t, lastValue(t, ui, func() []*string { return ui.thinkingLabels })); got != "Pondering..." {
		t.Fatalf("session-start label = %q", got)
	}
	if err := runner.Command("thinking-label").Handler(context.Background(), "Deep in thought", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if got := stringPointerValue(t, lastValue(t, ui, func() []*string { return ui.thinkingLabels })); got != "Deep in thought" {
		t.Fatalf("custom label = %q", got)
	}
	requireNotified(t, ui, "Hidden thinking label set to: Deep in thought", "info")
	if err := runner.Command("thinking-label").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if got := lastValue(t, ui, func() []*string { return ui.thinkingLabels }); got != nil {
		t.Fatalf("reset label = %#v", got)
	}
	requireNotified(t, ui, "Hidden thinking label reset to: Pondering...", "info")
}

func lastValue[T any](t *testing.T, ui *scriptedUI, read func() []T) T {
	t.Helper()
	ui.mu.Lock()
	defer ui.mu.Unlock()
	values := read()
	if len(values) == 0 {
		t.Fatal("no recorded values")
	}
	return values[len(values)-1]
}

func TestUIExampleMacSystemTheme(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "mac-system-theme.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	ui.mu.Lock()
	inputs := append([]any(nil), ui.setThemeInputs...)
	ui.mu.Unlock()
	if len(inputs) != 1 || inputs[0] != "light" {
		t.Fatalf("setTheme inputs = %#v", inputs)
	}
	runner.Emit(context.Background(), extensions.SessionShutdownEvent{})
}

func TestUIExampleModelStatus(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "model-status.ts", ui, extensions.RunnerOptions{})
	model := &ai.Model{ID: "model-1", Provider: "provider-1"}
	runner.Emit(context.Background(), extensions.ModelSelectEvent{Model: model, Source: extensions.ModelSelectSet})
	requireNotified(t, ui, "Model: provider-1/model-1", "info")
	if got := stringPointerValue(t, ui.lastStatus(t, "model")); got != "🤖 model-1" {
		t.Fatalf("model status = %q", got)
	}
}

func TestUIExamplePermissionGate(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	ui.selectFn = func(string, []string, *extensions.DialogOptions) (string, bool) { return "No", true }
	runner := loadUIExample(t, project, "permission-gate.ts", ui, extensions.RunnerOptions{})
	if result := runner.EmitToolCall(context.Background(), extensions.ToolCallEvent{
		ToolCallID: "call-1", ToolName: "bash", Input: map[string]any{"command": "ls -la"},
	}); result != nil && result.Block {
		t.Fatalf("safe command result = %#v", result)
	}
	blocked := runner.EmitToolCall(context.Background(), extensions.ToolCallEvent{
		ToolCallID: "call-2", ToolName: "bash", Input: map[string]any{"command": "sudo rm -rf /tmp/x"},
	})
	if blocked == nil || !blocked.Block || blocked.Reason != "Blocked by user" {
		t.Fatalf("blocked result = %#v", blocked)
	}
	ui.mu.Lock()
	ui.selectFn = func(string, []string, *extensions.DialogOptions) (string, bool) { return "Yes", true }
	ui.mu.Unlock()
	if result := runner.EmitToolCall(context.Background(), extensions.ToolCallEvent{
		ToolCallID: "call-3", ToolName: "bash", Input: map[string]any{"command": "sudo id"},
	}); result != nil && result.Block {
		t.Fatalf("allowed result = %#v", result)
	}
}

func TestUIExampleProjectTrust(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	ui.selectFn = func(string, []string, *extensions.DialogOptions) (string, bool) {
		return "Trust with note and remember", true
	}
	ui.inputFn = func(_ string, placeholder *string, _ *extensions.DialogOptions) (string, bool) {
		if placeholder == nil || *placeholder != "Optional note for this demo" {
			return "bad placeholder", true
		}
		return "demo note", true
	}
	runner := loadUIExample(t, project, "project-trust.ts", ui, extensions.RunnerOptions{})
	result, errors := runner.EmitProjectTrust(context.Background(), extensions.ProjectTrustEvent{CWD: project}, nil)
	if len(errors) != 0 {
		t.Fatalf("project trust errors = %#v", errors)
	}
	if result == nil || result.Trusted != extensions.ProjectTrustYes || !result.Remember {
		t.Fatalf("trust result = %#v", result)
	}
	requireNotified(t, ui, "Recorded demo note: demo note", "info")

	ui.mu.Lock()
	ui.selectFn = func(string, []string, *extensions.DialogOptions) (string, bool) {
		return "Do not trust this session", true
	}
	ui.mu.Unlock()
	result, errors = runner.EmitProjectTrust(context.Background(), extensions.ProjectTrustEvent{CWD: project}, nil)
	if len(errors) != 0 || result == nil || result.Trusted != extensions.ProjectTrustNo || result.Remember {
		t.Fatalf("distrust result = %#v, errors = %#v", result, errors)
	}
}

func TestUIExampleRPCDemo(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "rpc-demo.ts", ui, extensions.RunnerOptions{})

	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	if titles := ui.titleList(); len(titles) != 1 || titles[0] != "pi RPC Demo" {
		t.Fatalf("titles = %#v", titles)
	}
	widget := lastValue(t, ui, func() []*extensions.Widget { return ui.widgets["rpc-demo"] })
	if widget == nil || len(widget.Lines) != 2 || widget.Lines[0] != "--- RPC Extension UI Demo ---" {
		t.Fatalf("widget = %#v", widget)
	}
	if got := stringPointerValue(t, ui.lastStatus(t, "rpc-demo")); got != "Turns: 0" {
		t.Fatalf("session-start status = %q", got)
	}
	runner.Emit(context.Background(), extensions.TurnStartEvent{})
	if got := stringPointerValue(t, ui.lastStatus(t, "rpc-demo")); got != "Turn 1 running..." {
		t.Fatalf("turn-start status = %q", got)
	}
	runner.Emit(context.Background(), extensions.TurnEndEvent{})
	if got := stringPointerValue(t, ui.lastStatus(t, "rpc-demo")); got != "Turn 1 done" {
		t.Fatalf("turn-end status = %q", got)
	}

	ui.mu.Lock()
	ui.selectFn = func(string, []string, *extensions.DialogOptions) (string, bool) { return "Block", true }
	ui.mu.Unlock()
	blocked := runner.EmitToolCall(context.Background(), extensions.ToolCallEvent{
		ToolCallID: "call-1", ToolName: "bash", Input: map[string]any{"command": "sudo make install"},
	})
	if blocked == nil || !blocked.Block || blocked.Reason != "Blocked by user" {
		t.Fatalf("blocked tool call = %#v", blocked)
	}
	requireNotified(t, ui, "Command blocked by user", "warning")

	ui.mu.Lock()
	ui.confirmFn = func(string, string, *extensions.DialogOptions) bool { return false }
	ui.mu.Unlock()
	result := runner.Emit(context.Background(), extensions.SessionBeforeSwitchEvent{Reason: extensions.SessionSwitchReason("new")})
	switchResult, ok := result.(extensions.SessionBeforeSwitchResult)
	if !ok || !switchResult.Cancel {
		t.Fatalf("clear result = %#v", result)
	}
	requireNotified(t, ui, "Clear cancelled", "info")

	ui.mu.Lock()
	ui.inputFn = func(_ string, placeholder *string, _ *extensions.DialogOptions) (string, bool) {
		if placeholder == nil || *placeholder != "type something..." {
			return "bad placeholder", true
		}
		return "hello", true
	}
	ui.mu.Unlock()
	if err := runner.Command("rpc-input").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "You entered: hello", "info")
	ui.mu.Lock()
	ui.inputFn = func(string, *string, *extensions.DialogOptions) (string, bool) { return "", false }
	ui.mu.Unlock()
	if err := runner.Command("rpc-input").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Input cancelled", "info")

	ui.mu.Lock()
	ui.editorFn = func(_ string, prefill *string) (string, bool) {
		if prefill == nil || *prefill != "Line 1\nLine 2\nLine 3" {
			return "bad prefill", true
		}
		return "Line 1\nLine 2", true
	}
	ui.mu.Unlock()
	if err := runner.Command("rpc-editor").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Editor submitted (2 lines)", "info")
	ui.mu.Lock()
	ui.editorFn = func(string, *string) (string, bool) { return "", false }
	ui.mu.Unlock()
	if err := runner.Command("rpc-editor").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Editor cancelled", "info")

	if err := runner.Command("rpc-prefill").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if got := lastValue(t, ui, func() []string { return ui.editorTexts }); got != "This text was set by the rpc-demo extension." {
		t.Fatalf("editor text = %q", got)
	}
	requireNotified(t, ui, "Editor prefilled", "info")
}

func TestUIExampleStatusLine(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "status-line.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	if got := stringPointerValue(t, ui.lastStatus(t, "status-demo")); got != "<dim>Ready</fg>" {
		t.Fatalf("ready status = %q", got)
	}
	runner.Emit(context.Background(), extensions.TurnStartEvent{})
	if got := stringPointerValue(t, ui.lastStatus(t, "status-demo")); got != "<accent>●</fg><dim> Turn 1...</fg>" {
		t.Fatalf("turn status = %q", got)
	}
	runner.Emit(context.Background(), extensions.TurnEndEvent{})
	if got := stringPointerValue(t, ui.lastStatus(t, "status-demo")); got != "<success>✓</fg><dim> Turn 1 complete</fg>" {
		t.Fatalf("complete status = %q", got)
	}
}

func TestUIExampleSystemPromptHeader(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "system-prompt-header.ts", ui, extensions.RunnerOptions{
		ContextActions: extensions.ContextActions{GetSystemPrompt: func() string { return "prompt!" }},
	})
	runner.Emit(context.Background(), extensions.AgentStartEvent{})
	if got := stringPointerValue(t, ui.lastStatus(t, "system-prompt")); got != "System: 7 chars" {
		t.Fatalf("prompt status = %q", got)
	}
	runner.Emit(context.Background(), extensions.SessionShutdownEvent{})
	if got := ui.lastStatus(t, "system-prompt"); got != nil {
		t.Fatalf("shutdown status = %#v", got)
	}
}

func TestUIExampleTimedConfirm(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	ui.confirmFn = func(string, string, *extensions.DialogOptions) bool { return true }
	ui.selectFn = func(string, []string, *extensions.DialogOptions) (string, bool) { return "Option B", true }
	runner := loadUIExample(t, project, "timed-confirm.ts", ui, extensions.RunnerOptions{})

	if err := runner.Command("timed").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Confirmed by user!", "info")
	ui.mu.Lock()
	timed := ui.confirmCalls[len(ui.confirmCalls)-1]
	ui.mu.Unlock()
	if timed.opts == nil || timed.opts.Timeout == nil || *timed.opts.Timeout != 5000 {
		t.Fatalf("timed confirm options = %#v", timed.opts)
	}

	if err := runner.Command("timed-select").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Selected: Option B", "info")
	ui.mu.Lock()
	timedSelect := ui.selectCalls[len(ui.selectCalls)-1]
	ui.mu.Unlock()
	if timedSelect.opts == nil || timedSelect.opts.Timeout == nil || *timedSelect.opts.Timeout != 10000 {
		t.Fatalf("timed select options = %#v", timedSelect.opts)
	}

	if err := runner.Command("timed-signal").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	ui.mu.Lock()
	signalConfirm := ui.confirmCalls[len(ui.confirmCalls)-1]
	ui.mu.Unlock()
	if signalConfirm.opts == nil || signalConfirm.opts.Signal == nil {
		t.Fatalf("signal confirm options = %#v", signalConfirm.opts)
	}
}

func TestUIExampleTitlebarSpinner(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "titlebar-spinner.ts", ui, extensions.RunnerOptions{
		Actions: extensions.Actions{GetSessionName: func(context.Context) (*string, error) { return nil, nil }},
	})
	base := "π - " + filepath.Base(project)
	runner.Emit(context.Background(), extensions.AgentStartEvent{})
	if titles := ui.titleList(); len(titles) == 0 || titles[0] != base {
		t.Fatalf("agent-start titles = %#v", titles)
	}
	deadline := time.Now().Add(2 * time.Second)
	sawFrame := false
	for time.Now().Before(deadline) {
		for _, title := range ui.titleList() {
			if strings.HasSuffix(title, " "+base) && title != base {
				sawFrame = true
			}
		}
		if sawFrame {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawFrame {
		t.Fatalf("no animated title observed: %#v", ui.titleList())
	}
	runner.Emit(context.Background(), extensions.AgentEndEvent{})
	titles := ui.titleList()
	if titles[len(titles)-1] != base {
		t.Fatalf("agent-end titles = %#v", titles)
	}
}

func TestUIExampleWidgetPlacement(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "widget-placement.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	above := lastValue(t, ui, func() []*extensions.Widget { return ui.widgets["widget-above"] })
	if above == nil || len(above.Lines) != 1 || above.Lines[0] != "Above editor widget" || above.Factory != nil {
		t.Fatalf("above widget = %#v", above)
	}
	if options := lastValue(t, ui, func() []*extensions.WidgetOptions { return ui.widgetOptions["widget-above"] }); options != nil {
		t.Fatalf("above widget options = %#v", options)
	}
	below := lastValue(t, ui, func() []*extensions.Widget { return ui.widgets["widget-below"] })
	if below == nil || len(below.Lines) != 1 || below.Lines[0] != "Below editor widget" {
		t.Fatalf("below widget = %#v", below)
	}
	options := lastValue(t, ui, func() []*extensions.WidgetOptions { return ui.widgetOptions["widget-below"] })
	if options == nil || options.Placement != extensions.WidgetBelowEditor {
		t.Fatalf("below widget options = %#v", options)
	}
}

func TestUIExampleWorkingIndicator(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "working-indicator.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	spinner := lastValue(t, ui, func() []*extensions.WorkingIndicatorOptions { return ui.workingIndicators })
	if spinner == nil || len(spinner.Frames) != 10 || spinner.IntervalMS != 80 || !strings.Contains(spinner.Frames[0], "⠋") {
		t.Fatalf("session-start indicator = %#v", spinner)
	}
	if got := stringPointerValue(t, ui.lastStatus(t, "working-indicator")); got != "<dim>Indicator: custom spinner</fg>" {
		t.Fatalf("indicator status = %q", got)
	}

	if err := runner.Command("working-indicator").Handler(context.Background(), "none", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	hidden := lastValue(t, ui, func() []*extensions.WorkingIndicatorOptions { return ui.workingIndicators })
	if hidden == nil || hidden.Frames == nil || len(hidden.Frames) != 0 || hidden.IntervalMS != 0 {
		t.Fatalf("hidden indicator = %#v", hidden)
	}

	if err := runner.Command("working-indicator").Handler(context.Background(), "dot", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	dot := lastValue(t, ui, func() []*extensions.WorkingIndicatorOptions { return ui.workingIndicators })
	if dot == nil || len(dot.Frames) != 1 || !strings.Contains(dot.Frames[0], "●") || dot.IntervalMS != 0 {
		t.Fatalf("dot indicator = %#v", dot)
	}

	if err := runner.Command("working-indicator").Handler(context.Background(), "reset", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if restored := lastValue(t, ui, func() []*extensions.WorkingIndicatorOptions { return ui.workingIndicators }); restored != nil {
		t.Fatalf("reset indicator = %#v", restored)
	}
	requireNotified(t, ui, "Working indicator set to: pi default spinner", "info")

	if err := runner.Command("working-indicator").Handler(context.Background(), "bogus", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "Usage: /working-indicator [dot|pulse|none|spinner|reset]", "error")
}

func TestUIExampleWorkingMessage(t *testing.T) {
	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "working-message-test.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	message := lastValue(t, ui, func() []*string { return ui.workingMessages })
	if message == nil || !strings.Contains(*message, "Working... (custom)") {
		t.Fatalf("working message = %#v", message)
	}
	indicator := lastValue(t, ui, func() []*extensions.WorkingIndicatorOptions { return ui.workingIndicators })
	if indicator == nil || len(indicator.Frames) != 1 || !strings.Contains(indicator.Frames[0], "●") {
		t.Fatalf("working indicator = %#v", indicator)
	}
}

func writeFakeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// --- unit coverage for individual bindings ---

func TestUIDialogResolveCancelAndError(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("dialogs", { handler: async (_args, ctx) => {
    const selected = await ctx.ui.select("Pick", ["a", "b"], { timeout: 1500 });
    const cancelledSelect = await ctx.ui.select("Pick again", ["a"]);
    const confirmed = await ctx.ui.confirm("Sure?", "Really?");
    const value = await ctx.ui.input("Name", "placeholder");
    const cancelledInput = await ctx.ui.input("Name again");
    const edited = await ctx.ui.editor("Edit", "seed");
    const cancelledEditor = await ctx.ui.editor("Edit again");
    pi.appendEntry("dialog-results", {
      selected,
      cancelledSelect: cancelledSelect === undefined,
      confirmed,
      value,
      cancelledInput: cancelledInput === undefined,
      edited,
      cancelledEditor: cancelledEditor === undefined,
    });
  }});
  pi.registerCommand("dialog-error", { handler: async (_args, ctx) => {
    try {
      await ctx.ui.select("Broken", ["a"]);
      pi.appendEntry("dialog-error", { threw: false });
    } catch (error) {
      pi.appendEntry("dialog-error", { threw: true, message: String(error) });
    }
  }});
}
`
	ui := newScriptedUI()
	selectResults := []struct {
		value string
		ok    bool
	}{{"b", true}, {"", false}}
	selectIndex := 0
	ui.selectFn = func(string, []string, *extensions.DialogOptions) (string, bool) {
		result := selectResults[selectIndex%len(selectResults)]
		selectIndex++
		return result.value, result.ok
	}
	ui.confirmFn = func(string, string, *extensions.DialogOptions) bool { return true }
	inputOK := true
	ui.inputFn = func(string, *string, *extensions.DialogOptions) (string, bool) {
		ok := inputOK
		inputOK = false
		return "typed", ok
	}
	editorOK := true
	ui.editorFn = func(string, *string) (string, bool) {
		ok := editorOK
		editorOK = false
		return "edited text", ok
	}
	var recorded map[string]any
	var errorRecorded map[string]any
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, data any) error {
		if customType == "dialog-results" {
			recorded, _ = data.(map[string]any)
		}
		if customType == "dialog-error" {
			errorRecorded, _ = data.(map[string]any)
		}
		return nil
	}}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"dialogs.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI, Actions: actions,
	})
	if err := runner.Command("dialogs").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if recorded == nil || recorded["selected"] != "b" || recorded["cancelledSelect"] != true ||
		recorded["confirmed"] != true || recorded["value"] != "typed" || recorded["cancelledInput"] != true ||
		recorded["edited"] != "edited text" || recorded["cancelledEditor"] != true {
		t.Fatalf("dialog results = %#v", recorded)
	}
	ui.mu.Lock()
	firstSelect := ui.selectCalls[0]
	secondSelect := ui.selectCalls[1]
	firstInput := ui.inputCalls[0]
	secondInput := ui.inputCalls[1]
	firstEditor := ui.editorCalls[0]
	secondEditor := ui.editorCalls[1]
	ui.mu.Unlock()
	if firstSelect.title != "Pick" || len(firstSelect.options) != 2 || firstSelect.opts == nil ||
		firstSelect.opts.Timeout == nil || *firstSelect.opts.Timeout != 1500 {
		t.Fatalf("first select call = %#v", firstSelect)
	}
	if secondSelect.opts != nil {
		t.Fatalf("second select opts = %#v", secondSelect.opts)
	}
	if firstInput.placeholder == nil || *firstInput.placeholder != "placeholder" || secondInput.placeholder != nil {
		t.Fatalf("input placeholders = %#v, %#v", firstInput.placeholder, secondInput.placeholder)
	}
	if firstEditor.prefill == nil || *firstEditor.prefill != "seed" || secondEditor.prefill != nil {
		t.Fatalf("editor prefills = %#v, %#v", firstEditor.prefill, secondEditor.prefill)
	}

	ui.mu.Lock()
	ui.dialogErr = fmt.Errorf("selector exploded")
	ui.mu.Unlock()
	if err := runner.Command("dialog-error").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if errorRecorded == nil || errorRecorded["threw"] != true || !strings.Contains(fmt.Sprint(errorRecorded["message"]), "selector exploded") {
		t.Fatalf("dialog error = %#v", errorRecorded)
	}
}

func TestUINotifyTypesAndSetters(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("surface", { handler: async (_args, ctx) => {
    ctx.ui.notify("plain");
    ctx.ui.notify("info message", "info");
    ctx.ui.notify("warning message", "warning");
    ctx.ui.notify("error message", "error");
    ctx.ui.setStatus("key", "value");
    ctx.ui.setStatus("key", undefined);
    ctx.ui.setTitle("new title");
    ctx.ui.setWorkingMessage("busy");
    ctx.ui.setWorkingMessage();
    ctx.ui.setWorkingVisible(false);
    ctx.ui.setWorkingVisible(true);
    ctx.ui.setWorkingIndicator({ frames: ["x", "y"], intervalMs: 120 });
    ctx.ui.setWorkingIndicator({ frames: [] });
    ctx.ui.setWorkingIndicator();
    ctx.ui.setHiddenThinkingLabel("Pondering");
    ctx.ui.setHiddenThinkingLabel();
    ctx.ui.setWidget("lines", ["one", "two"]);
    ctx.ui.setWidget("lines", undefined);
    ctx.ui.pasteToEditor("pasted");
    ctx.ui.setEditorText("typed text");
    const editorText = ctx.ui.getEditorText();
    ctx.ui.setToolsExpanded(true);
    const expanded = ctx.ui.getToolsExpanded();
    pi.appendEntry("setter-results", { editorText, expanded });
  }});
}
`
	ui := newScriptedUI()
	var recorded map[string]any
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, data any) error {
		if customType == "setter-results" {
			recorded, _ = data.(map[string]any)
		}
		return nil
	}}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"setters.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI, Actions: actions,
	})
	if err := runner.Command("surface").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	wantNotifications := [][2]string{
		{"plain", "info"}, {"info message", "info"}, {"warning message", "warning"}, {"error message", "error"},
	}
	notifications := ui.notifyList()
	if len(notifications) != len(wantNotifications) {
		t.Fatalf("notifications = %#v", notifications)
	}
	for index, want := range wantNotifications {
		if notifications[index] != want {
			t.Fatalf("notification[%d] = %#v, want %#v", index, notifications[index], want)
		}
	}
	ui.mu.Lock()
	statusHistory := append([]*string(nil), ui.statuses["key"]...)
	titles := append([]string(nil), ui.titles...)
	workingMessages := append([]*string(nil), ui.workingMessages...)
	workingVisible := append([]bool(nil), ui.workingVisible...)
	indicators := append([]*extensions.WorkingIndicatorOptions(nil), ui.workingIndicators...)
	labels := append([]*string(nil), ui.thinkingLabels...)
	widgetHistory := append([]*extensions.Widget(nil), ui.widgets["lines"]...)
	pastes := append([]string(nil), ui.pastes...)
	ui.mu.Unlock()
	if len(statusHistory) != 2 || statusHistory[0] == nil || *statusHistory[0] != "value" || statusHistory[1] != nil {
		t.Fatalf("status history = %#v", statusHistory)
	}
	if len(titles) != 1 || titles[0] != "new title" {
		t.Fatalf("titles = %#v", titles)
	}
	if len(workingMessages) != 2 || workingMessages[0] == nil || *workingMessages[0] != "busy" || workingMessages[1] != nil {
		t.Fatalf("working messages = %#v", workingMessages)
	}
	if len(workingVisible) != 2 || workingVisible[0] || !workingVisible[1] {
		t.Fatalf("working visible = %#v", workingVisible)
	}
	if len(indicators) != 3 ||
		indicators[0] == nil || len(indicators[0].Frames) != 2 || indicators[0].IntervalMS != 120 ||
		indicators[1] == nil || indicators[1].Frames == nil || len(indicators[1].Frames) != 0 ||
		indicators[2] != nil {
		t.Fatalf("indicators = %#v", indicators)
	}
	if len(labels) != 2 || labels[0] == nil || *labels[0] != "Pondering" || labels[1] != nil {
		t.Fatalf("labels = %#v", labels)
	}
	if len(widgetHistory) != 2 || widgetHistory[0] == nil || len(widgetHistory[0].Lines) != 2 || widgetHistory[1] != nil {
		t.Fatalf("widget history = %#v", widgetHistory)
	}
	if len(pastes) != 1 || pastes[0] != "pasted" {
		t.Fatalf("pastes = %#v", pastes)
	}
	if recorded == nil || recorded["editorText"] != "typed text" || recorded["expanded"] != true {
		t.Fatalf("setter results = %#v", recorded)
	}
	if !ui.GetToolsExpanded() {
		t.Fatal("setToolsExpanded did not reach the UI seam")
	}
}

func TestUIWidgetFactoryBridge(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("widget", { handler: async (_args, ctx) => {
    ctx.ui.setWidget("factory-widget", (tui, theme) => ({
      render(width) {
        return [theme.fg("accent", "w=" + width + " cols=" + tui.terminal.columns + " rows=" + tui.terminal.rows)];
      },
      dispose() {
        tui.requestRender();
        pi.appendEntry("widget-disposed", {});
      },
    }), { placement: "belowEditor" });
  }});
}
`
	ui := newScriptedUI()
	disposed := make(chan struct{}, 1)
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, _ any) error {
		if customType == "widget-disposed" {
			disposed <- struct{}{}
		}
		return nil
	}}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"widget.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI, Actions: actions,
	})
	if err := runner.Command("widget").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	widget := lastValue(t, ui, func() []*extensions.Widget { return ui.widgets["factory-widget"] })
	options := lastValue(t, ui, func() []*extensions.WidgetOptions { return ui.widgetOptions["factory-widget"] })
	if widget == nil || widget.Factory == nil || widget.Lines != nil || options == nil || options.Placement != extensions.WidgetBelowEditor {
		t.Fatalf("factory widget = %#v, options = %#v", widget, options)
	}
	host := &stubHost{width: 90, height: 25}
	component := widget.Factory(host, tagTheme{})
	if component == nil {
		t.Fatal("widget factory returned no component")
	}
	lines := component.Render(42)
	if len(lines) != 1 || lines[0] != "<accent>w=42 cols=90 rows=25</fg>" {
		t.Fatalf("widget render = %#v", lines)
	}
	disposable, ok := component.(extensions.DisposableComponent)
	if !ok {
		t.Fatal("widget component is not disposable")
	}
	disposable.Dispose()
	select {
	case <-disposed:
	case <-time.After(2 * time.Second):
		t.Fatal("dispose did not reach the JS component")
	}
	if host.invalidations.Load() != 1 {
		t.Fatalf("host invalidations = %d", host.invalidations.Load())
	}
}

func TestUIFooterDataBridge(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("footer", { handler: async (_args, ctx) => {
    ctx.ui.setFooter((tui, theme, footerData) => {
      const unsubscribe = footerData.onBranchChange(() => {});
      const statuses = footerData.getExtensionStatuses();
      return {
        render() {
          return [
            String(footerData.getGitBranch()),
            String(footerData.getAvailableProviderCount()),
            String(typeof unsubscribe),
            statuses.get("model") + "/" + statuses.size,
          ];
        },
      };
    });
  }});
}
`
	ui := newScriptedUI()
	runner := loadBridgeRunner(t, project, []bridgeSource{{"footer.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI,
	})
	if err := runner.Command("footer").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	factory := lastValue(t, ui, func() []extensions.FooterFactory { return ui.footers })
	if factory == nil {
		t.Fatal("footer factory missing")
	}
	component := factory(&stubHost{width: 80, height: 24}, tagTheme{}, stubFooterData{branch: "feature"})
	lines := component.Render(80)
	want := []string{"feature", "0", "function", "ready/2"}
	if len(lines) != len(want) {
		t.Fatalf("footer data render = %#v", lines)
	}
	for index, line := range want {
		if lines[index] != line {
			t.Fatalf("footer data render[%d] = %q, want %q", index, lines[index], line)
		}
	}
	// Empty branch surfaces as null, matching upstream getGitBranch(): string | null.
	nullBranch := factory(&stubHost{width: 80, height: 24}, tagTheme{}, stubFooterData{branch: ""})
	if lines := nullBranch.Render(80); lines[0] != "null" {
		t.Fatalf("null branch render = %#v", lines)
	}
}

func TestUIAutocompleteRoundTrip(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("register", { handler: async (_args, ctx) => {
    ctx.ui.addAutocompleteProvider((current) => ({
      triggerCharacters: [...(current.triggerCharacters ?? []), "#"],
      async getSuggestions(lines, cursorLine, cursorCol, options) {
        if (options.signal.aborted) throw new Error("signal should not start aborted");
        if (lines[cursorLine] === "delegate") {
          return current.getSuggestions(lines, cursorLine, cursorCol, options);
        }
        if (lines[cursorLine] === "empty") return null;
        const applied = current.applyCompletion(["x"], 0, 1, { value: "v", label: "l" }, "p");
        const fileTrigger = current.shouldTriggerFileCompletion(lines, cursorLine, cursorCol);
        await Promise.resolve();
        return {
          items: [{ value: "js", label: "JS", description: applied.lines[0] + ":" + applied.cursorLine + ":" + applied.cursorCol + ":" + options.force + ":" + fileTrigger }],
          prefix: lines[cursorLine].slice(0, cursorCol),
        };
      },
      applyCompletion(lines, cursorLine, cursorCol, item, prefix) {
        return { lines: [prefix + item.value, ...lines], cursorLine: cursorLine + 1, cursorCol: cursorCol + item.value.length };
      },
      shouldTriggerFileCompletion() { return true; },
    }));
  }});
}
`
	ui := newScriptedUI()
	runner := loadBridgeRunner(t, project, []bridgeSource{{"autocomplete.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI,
	})
	if err := runner.Command("register").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	factory := lastValue(t, ui, func() []extensions.AutocompleteProviderFactory { return ui.acFactories })
	current := &stubAutocompleteProvider{suggestions: &extensions.AutocompleteResult{
		Prefix: "go", Items: []extensions.AutocompleteItem{{Value: "from-go", Label: "From Go"}},
	}}
	provider := factory(current)
	if provider == nil {
		t.Fatal("autocomplete factory returned no provider")
	}
	if triggers := provider.TriggerCharacters(); len(triggers) != 3 || triggers[0] != "@" || triggers[2] != "#" {
		t.Fatalf("trigger characters = %#v", triggers)
	}
	request := extensions.AutocompleteRequest{Lines: []string{"hello"}, CursorLine: 0, CursorCol: 3, Signal: context.Background(), Force: true}
	result, err := provider.GetSuggestions(context.Background(), request)
	if err != nil || result == nil || result.Prefix != "hel" || len(result.Items) != 1 || result.Items[0].Value != "js" {
		t.Fatalf("suggestions = %#v, err = %v", result, err)
	}
	if result.Items[0].Description != "applied:v:p:1:3:true:true" {
		t.Fatalf("delegated description = %q", result.Items[0].Description)
	}
	delegated, err := provider.GetSuggestions(context.Background(), extensions.AutocompleteRequest{Lines: []string{"delegate"}, CursorLine: 0, CursorCol: 0, Signal: context.Background()})
	if err != nil || delegated == nil || delegated.Prefix != "go" || delegated.Items[0].Value != "from-go" {
		t.Fatalf("delegated suggestions = %#v, err = %v", delegated, err)
	}
	empty, err := provider.GetSuggestions(context.Background(), extensions.AutocompleteRequest{Lines: []string{"empty"}, CursorLine: 0, CursorCol: 0, Signal: context.Background()})
	if err != nil || empty != nil {
		t.Fatalf("empty suggestions = %#v, err = %v", empty, err)
	}
	lines, cursorLine, cursorCol := provider.ApplyCompletion(request, extensions.AutocompleteItem{Value: "item", Label: "Item"}, "pre")
	if len(lines) != 2 || lines[0] != "preitem" || lines[1] != "hello" || cursorLine != 1 || cursorCol != 7 {
		t.Fatalf("apply completion = %#v, %d, %d", lines, cursorLine, cursorCol)
	}
	if !provider.ShouldTriggerFileCompletion(request) {
		t.Fatal("shouldTriggerFileCompletion = false")
	}
}

func TestUIOnTerminalInputBridge(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  let unsubscribe;
  pi.registerCommand("listen", { handler: async (_args, ctx) => {
    unsubscribe = ctx.ui.onTerminalInput((data) => {
      if (data === "swallow") return { consume: true };
      if (data === "replace") return { consume: false, data: "replaced" };
      return undefined;
    });
  }});
  pi.registerCommand("stop", { handler: async () => { unsubscribe(); } });
}
`
	ui := newScriptedUI()
	runner := loadBridgeRunner(t, project, []bridgeSource{{"terminal.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI,
	})
	if err := runner.Command("listen").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	ui.mu.Lock()
	handler := ui.terminalHandlers[len(ui.terminalHandlers)-1]
	ui.mu.Unlock()
	if handler == nil {
		t.Fatal("terminal input handler was not registered")
	}
	if result := handler("swallow"); result == nil || !result.Consume || result.Data != nil {
		t.Fatalf("swallow result = %#v", result)
	}
	result := handler("replace")
	if result == nil || result.Consume || result.Data == nil || *result.Data != "replaced" {
		t.Fatalf("replace result = %#v", result)
	}
	if result := handler("other"); result != nil {
		t.Fatalf("pass-through result = %#v", result)
	}
	if err := runner.Command("stop").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	ui.mu.Lock()
	unsubs := ui.terminalUnsubs
	ui.mu.Unlock()
	if unsubs != 1 {
		t.Fatalf("terminal unsubscribes = %d", unsubs)
	}
}

func TestUIThemeBindings(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("themes", { handler: async (_args, ctx) => {
    const theme = ctx.ui.theme;
    const known = ctx.ui.getTheme("known");
    const setKnown = ctx.ui.setTheme(known);
    const setByName = ctx.ui.setTheme("dark");
    const setUnknown = ctx.ui.setTheme("missing");
    pi.appendEntry("theme-results", {
      fg: theme.fg("accent", "text"),
      bg: theme.bg("panel", "text"),
      bold: theme.bold("b"),
      italic: theme.italic("i"),
      underline: theme.underline("u"),
      inverse: theme.inverse("v"),
      strikethrough: theme.strikethrough("s"),
      fgAnsi: theme.getFgAnsi("accent"),
      bgAnsi: theme.getBgAnsi("panel"),
      colorMode: theme.getColorMode(),
      thinking: theme.getThinkingBorderColor("high")("edge"),
      bashBorder: theme.getBashModeBorderColor()("edge"),
      all: ctx.ui.getAllThemes(),
      knownFg: known.fg("dim", "k"),
      unknownTheme: ctx.ui.getTheme("missing") === undefined,
      setKnown,
      setByName,
      setUnknown,
    });
  }});
}
`
	ui := newScriptedUI()
	darkPath := "/themes/dark.json"
	ui.allThemes = []extensions.ThemeInfo{{Name: "dark", Path: &darkPath}, {Name: "builtin"}}
	ui.themesByName["known"] = tagTheme{}
	var recorded map[string]any
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, data any) error {
		if customType == "theme-results" {
			recorded, _ = data.(map[string]any)
		}
		return nil
	}}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"themes.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI, Actions: actions,
	})
	if err := runner.Command("themes").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if recorded == nil {
		t.Fatal("theme results were not recorded")
	}
	expectations := map[string]any{
		"fg": "<accent>text</fg>", "bg": "[panel]text[/bg]", "bold": "**b**", "italic": "_i_",
		"underline": "__u__", "inverse": "!v!", "strikethrough": "~~s~~",
		"fgAnsi": "fg-ansi:accent", "bgAnsi": "bg-ansi:panel", "colorMode": "truecolor",
		"thinking": "think(high):edge", "bashBorder": "bash:edge",
		"knownFg": "<dim>k</fg>", "unknownTheme": true,
	}
	for key, want := range expectations {
		if recorded[key] != want {
			t.Fatalf("theme result %q = %#v, want %#v", key, recorded[key], want)
		}
	}
	all, _ := recorded["all"].([]any)
	if len(all) != 2 {
		t.Fatalf("getAllThemes = %#v", recorded["all"])
	}
	first, _ := all[0].(map[string]any)
	second, _ := all[1].(map[string]any)
	if first["name"] != "dark" || first["path"] != darkPath || second["name"] != "builtin" {
		t.Fatalf("theme infos = %#v", all)
	}
	if _, hasPath := second["path"]; hasPath {
		t.Fatalf("builtin theme should have no path: %#v", second)
	}
	for _, key := range []string{"setKnown", "setByName", "setUnknown"} {
		result, _ := recorded[key].(map[string]any)
		if result == nil || result["success"] != true {
			t.Fatalf("%s = %#v", key, recorded[key])
		}
	}
	ui.mu.Lock()
	inputs := append([]any(nil), ui.setThemeInputs...)
	ui.mu.Unlock()
	if len(inputs) != 3 {
		t.Fatalf("setTheme inputs = %#v", inputs)
	}
	if theme, ok := inputs[0].(extensions.Theme); !ok || theme != extensions.Theme(tagTheme{}) {
		t.Fatalf("setTheme(theme) input = %#v", inputs[0])
	}
	if inputs[1] != "dark" || inputs[2] != "missing" {
		t.Fatalf("setTheme name inputs = %#v", inputs[1:])
	}
}

func TestUISetThemeErrorResult(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("bad-theme", { handler: async (_args, ctx) => {
    const result = ctx.ui.setTheme("nope");
    pi.appendEntry("theme-error", result);
  }});
}
`
	ui := newScriptedUI()
	ui.setThemeResult = extensions.ThemeSetResult{Success: false, Error: "theme not found: nope"}
	var recorded map[string]any
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, data any) error {
		if customType == "theme-error" {
			recorded, _ = data.(map[string]any)
		}
		return nil
	}}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"bad-theme.ts", source}}, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI, Actions: actions,
	})
	if err := runner.Command("bad-theme").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if recorded == nil || recorded["success"] != false || recorded["error"] != "theme not found: nope" {
		t.Fatalf("theme error result = %#v", recorded)
	}
}
