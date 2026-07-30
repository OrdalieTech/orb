package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/codingagent/extensions"
	"github.com/OrdalieTech/orb/tui"
)

type uiNotification struct {
	message string
	kind    extensions.NotificationType
}

type uiStatus struct {
	key  string
	text *string
}

type uiWidget struct {
	key     string
	widget  *extensions.Widget
	options *extensions.WidgetOptions
}

type hostUIStub struct {
	extensions.NoopUI

	mu                     sync.Mutex
	notifications          []uiNotification
	statuses               []uiStatus
	workingMessages        []*string
	workingVisible         []bool
	workingIndicators      []*extensions.WorkingIndicatorOptions
	hiddenThinkingLabels   []*string
	widgets                []uiWidget
	titles                 []string
	pastes                 []string
	editorText             string
	toolsExpanded          bool
	customRenders          [][]string
	pendingDialogStarted   chan struct{}
	pendingDialogCancelled chan error
	editorMounted          chan extensions.EditorComponent
}

func newHostUIStub() *hostUIStub {
	return &hostUIStub{
		pendingDialogStarted:   make(chan struct{}, 1),
		pendingDialogCancelled: make(chan error, 1),
		editorMounted:          make(chan extensions.EditorComponent, 1),
	}
}

func (ui *hostUIStub) Notify(message string, kind extensions.NotificationType) {
	ui.mu.Lock()
	ui.notifications = append(ui.notifications, uiNotification{message: message, kind: kind})
	ui.mu.Unlock()
}

func (ui *hostUIStub) Select(ctx context.Context, title string, _ []string, _ *extensions.DialogOptions) (string, bool, error) {
	if title == "Abort dialog" {
		<-ctx.Done()
		return "", false, context.Cause(ctx)
	}
	if title != "Pending dialog" {
		return "second", true, nil
	}
	ui.pendingDialogStarted <- struct{}{}
	<-ctx.Done()
	err := context.Cause(ctx)
	ui.pendingDialogCancelled <- err
	return "", false, err
}

func (*hostUIStub) Confirm(context.Context, string, string, *extensions.DialogOptions) (bool, error) {
	return true, nil
}

func (*hostUIStub) Input(context.Context, string, *string, *extensions.DialogOptions) (string, bool, error) {
	return "typed", true, nil
}

func (*hostUIStub) Editor(context.Context, string, *string) (string, bool, error) {
	return "edited", true, nil
}

func (ui *hostUIStub) SetStatus(key string, text *string) {
	ui.mu.Lock()
	ui.statuses = append(ui.statuses, uiStatus{key: key, text: cloneString(text)})
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetWorkingMessage(message *string) {
	ui.mu.Lock()
	ui.workingMessages = append(ui.workingMessages, cloneString(message))
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetWorkingVisible(visible bool) {
	ui.mu.Lock()
	ui.workingVisible = append(ui.workingVisible, visible)
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetWorkingIndicator(options *extensions.WorkingIndicatorOptions) {
	ui.mu.Lock()
	if options == nil {
		ui.workingIndicators = append(ui.workingIndicators, nil)
	} else {
		copied := *options
		copied.Frames = append([]string(nil), options.Frames...)
		ui.workingIndicators = append(ui.workingIndicators, &copied)
	}
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetHiddenThinkingLabel(label *string) {
	ui.mu.Lock()
	ui.hiddenThinkingLabels = append(ui.hiddenThinkingLabels, cloneString(label))
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetWidget(key string, widget *extensions.Widget, options *extensions.WidgetOptions) {
	ui.mu.Lock()
	var copiedWidget *extensions.Widget
	if widget != nil {
		value := *widget
		value.Lines = append([]string(nil), widget.Lines...)
		copiedWidget = &value
	}
	var copiedOptions *extensions.WidgetOptions
	if options != nil {
		value := *options
		copiedOptions = &value
	}
	ui.widgets = append(ui.widgets, uiWidget{key: key, widget: copiedWidget, options: copiedOptions})
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetTitle(title string) {
	ui.mu.Lock()
	ui.titles = append(ui.titles, title)
	ui.mu.Unlock()
}

func (ui *hostUIStub) PasteToEditor(text string) {
	ui.mu.Lock()
	ui.pastes = append(ui.pastes, text)
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetEditorText(text string) {
	ui.mu.Lock()
	ui.editorText = text
	ui.mu.Unlock()
}

func (ui *hostUIStub) SetEditorComponent(factory extensions.EditorFactory) {
	if factory == nil {
		return
	}
	host := &stubUIHost{invalidated: make(chan struct{}, 8)}
	ui.editorMounted <- factory(host, ui.Theme(), stubKeybindings{})
}

func (ui *hostUIStub) GetEditorText() string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.editorText
}

func (ui *hostUIStub) GetToolsExpanded() bool {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.toolsExpanded
}

func (ui *hostUIStub) SetToolsExpanded(expanded bool) {
	ui.mu.Lock()
	ui.toolsExpanded = expanded
	ui.mu.Unlock()
}

func (ui *hostUIStub) Custom(ctx context.Context, factory extensions.CustomFactory, _ *extensions.CustomOptions) (any, bool, error) {
	done := make(chan any, 1)
	host := &stubUIHost{invalidated: make(chan struct{}, 8)}
	component, err := factory(host, ui.Theme(), stubKeybindings{}, func(value any) { done <- value })
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if disposable, ok := component.(extensions.DisposableComponent); ok {
			disposable.Dispose()
		}
	}()

	focusable, ok := component.(tui.Focusable)
	if !ok {
		return nil, false, errors.New("host component is not focusable")
	}
	focusable.SetFocused(true)
	first := waitForRender(tContext{ctx}, component, 40, func(lines []string) bool {
		return len(lines) == 4 && lines[0] == "count:0" && lines[3] == "focused:true"
	})
	ui.recordCustomRender(first)
	input, ok := component.(tui.InputHandler)
	if !ok {
		return nil, false, errors.New("host component does not handle TUI input")
	}
	input.HandleInput(tui.KeyEvent{Raw: "+", Key: "+"})
	second := waitForRender(tContext{ctx}, component, 40, func(lines []string) bool {
		return len(lines) == 4 && lines[0] == "count:1" && lines[3] == "focused:true"
	})
	ui.recordCustomRender(second)
	input.HandleInput(tui.KeyEvent{Raw: "q", Key: "q"})
	select {
	case value := <-done:
		return value, true, nil
	case <-ctx.Done():
		return nil, false, context.Cause(ctx)
	case <-time.After(30 * time.Second):
		return nil, false, errors.New("timed out waiting for custom done")
	}
}

type sdkThemeUIStub struct {
	extensions.NoopUI
	rendered []string
}

func (ui *sdkThemeUIStub) Custom(ctx context.Context, factory extensions.CustomFactory, _ *extensions.CustomOptions) (any, bool, error) {
	done := make(chan any, 1)
	host := &stubUIHost{invalidated: make(chan struct{}, 8)}
	component, err := factory(host, ui.Theme(), stubKeybindings{}, func(value any) { done <- value })
	if err != nil {
		return nil, false, err
	}
	if disposable, ok := component.(extensions.DisposableComponent); ok {
		defer disposable.Dispose()
	}
	ui.rendered = waitForRender(tContext{ctx}, component, 40, func(lines []string) bool { return len(lines) > 0 })
	if len(ui.rendered) == 0 {
		return nil, false, errors.New("SDK-themed component did not render")
	}
	select {
	case value := <-done:
		return value, true, nil
	case <-ctx.Done():
		return nil, false, context.Cause(ctx)
	}
}

type tContext struct{ context.Context }

func waitForRender(ctx tContext, component extensions.Component, width int, accept func([]string) bool) []string {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		lines := component.Render(width)
		if accept(lines) {
			return append([]string(nil), lines...)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-ticker.C:
		}
	}
}

func (ui *hostUIStub) recordCustomRender(lines []string) {
	ui.mu.Lock()
	ui.customRenders = append(ui.customRenders, append([]string(nil), lines...))
	ui.mu.Unlock()
}

type stubUIHost struct{ invalidated chan struct{} }

func (*stubUIHost) Width() int  { return 40 }
func (*stubUIHost) Height() int { return 20 }
func (host *stubUIHost) Invalidate() {
	select {
	case host.invalidated <- struct{}{}:
	default:
	}
}

type stubKeybindings struct{}

func (stubKeybindings) Matches(input, binding string) bool { return input == binding }
func (stubKeybindings) Keys(string) []string               { return []string{"ctrl+q"} }
func (stubKeybindings) Definition(string) extensions.KeybindingDefinition {
	return extensions.KeybindingDefinition{}
}
func (stubKeybindings) Conflicts() []extensions.KeybindingConflict { return nil }
func (stubKeybindings) UserBindings() map[string][]string          { return nil }
func (stubKeybindings) ResolvedBindings() map[string][]string      { return nil }

func TestHostInitializesSDKGlobalThemeBeforeCustomFactory(t *testing.T) {
	agentDir := isolatedTempDir(t)
	sdkRoot := filepath.Join(agentDir, "npm", "node_modules", "@earendil-works", "pi-coding-agent")
	writeFile(t, filepath.Join(sdkRoot, "package.json"), `{"name":"@earendil-works/pi-coding-agent","type":"module","exports":"./dist/index.js"}`, 0o600)
	writeFile(t, filepath.Join(sdkRoot, "dist", "index.js"), `
const theme = new Proxy({}, {
  get(_target, property) {
    const value = globalThis[Symbol.for("@earendil-works/pi-coding-agent:theme")];
    if (!value) throw new Error("Theme not initialized. Call initTheme() first.");
    return value[property];
  }
});
export class BorderedLoader {
  constructor(_tui, _theme, label) { this.line = theme.fg("muted", label + " esc"); }
  render() { return [this.line]; }
  dispose() {}
}
`, 0o600)
	entry := filepath.Join(agentDir, "extensions", "sdk-theme.mjs")
	writeFile(t, entry, `
import { BorderedLoader } from "@earendil-works/pi-coding-agent";
export default function (pi) {
  pi.registerCommand("sdk-theme-loader", {
    async handler(_args, ctx) {
      const value = await ctx.ui.custom((tui, theme, _keybindings, done) => {
        const loader = new BorderedLoader(tui, theme, "Loading");
        queueMicrotask(() => done("mounted"));
        return loader;
      });
      if (value !== "mounted") throw new Error("custom loader did not complete");
    }
  });
}
`, 0o600)
	_, registry, _, result, cwd := startFixtureManagerIn(t, agentDir, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	ui := &sdkThemeUIStub{}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModeTUI, UI: ui})
	command := runner.Command("sdk-theme-loader")
	if command == nil {
		t.Fatal("sdk-theme-loader command was not registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := command.Handler(ctx, "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ui.rendered, []string{"Loading esc"}) {
		t.Fatalf("SDK-themed render = %#v", ui.rendered)
	}
}

func TestRealHostUISurfaceAndCustomComponent(t *testing.T) {
	_, registry, _, result, cwd := startFixtureManager(t, fixturePath(t, "ui.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	ui := newHostUIStub()
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModeTUI, UI: ui})
	tool := runner.ToolDefinition("host_ui_surface")
	if tool == nil {
		t.Fatal("host_ui_surface was not registered")
	}
	resultValue, err := tool.Execute(context.Background(), "ui-1", map[string]any{}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	details, ok := resultValue.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %#v", resultValue.Details)
	}
	wantDetails := map[string]any{
		"selected": "second", "confirmed": true, "input": "typed", "edited": "edited",
		"aborted": true, "custom": map[string]any{"count": float64(1)}, "editorText": "editor text", "toolsExpanded": true,
	}
	if !reflect.DeepEqual(details, wantDetails) {
		t.Fatalf("details = %#v, want %#v", details, wantDetails)
	}

	ui.mu.Lock()
	defer ui.mu.Unlock()
	if !reflect.DeepEqual(ui.notifications, []uiNotification{{message: "host notification", kind: extensions.NotifyWarning}}) {
		t.Fatalf("notifications = %#v", ui.notifications)
	}
	if len(ui.statuses) != 2 || ui.statuses[0].text == nil || *ui.statuses[0].text != "working" || ui.statuses[1].text != nil {
		t.Fatalf("statuses = %#v", ui.statuses)
	}
	if len(ui.workingMessages) != 1 || ui.workingMessages[0] == nil || *ui.workingMessages[0] != "host working" || !reflect.DeepEqual(ui.workingVisible, []bool{false}) {
		t.Fatalf("working messages/visibility = %#v / %#v", ui.workingMessages, ui.workingVisible)
	}
	if len(ui.workingIndicators) != 1 || !reflect.DeepEqual(ui.workingIndicators[0].Frames, []string{"a", "b"}) || ui.workingIndicators[0].IntervalMS != 25 {
		t.Fatalf("working indicators = %#v", ui.workingIndicators)
	}
	if len(ui.hiddenThinkingLabels) != 1 || ui.hiddenThinkingLabels[0] == nil || *ui.hiddenThinkingLabels[0] != "host thinking" {
		t.Fatalf("thinking labels = %#v", ui.hiddenThinkingLabels)
	}
	if len(ui.widgets) != 2 || !reflect.DeepEqual(ui.widgets[0].widget.Lines, []string{"widget one", "widget two"}) || ui.widgets[0].options.Placement != extensions.WidgetBelowEditor || ui.widgets[1].widget != nil {
		t.Fatalf("widgets = %#v", ui.widgets)
	}
	if !reflect.DeepEqual(ui.titles, []string{"host title"}) || !reflect.DeepEqual(ui.pastes, []string{"pasted text"}) {
		t.Fatalf("titles/pastes = %#v / %#v", ui.titles, ui.pastes)
	}
	if !reflect.DeepEqual(ui.customRenders, [][]string{{"count:0", "width:40", "keys:ctrl+q", "focused:true"}, {"count:1", "width:40", "keys:ctrl+q", "focused:true"}}) {
		t.Fatalf("custom renders = %#v", ui.customRenders)
	}
}

func TestRealHostUIEditorMountDoesNotBlockReader(t *testing.T) {
	_, registry, _, result, cwd := startFixtureManager(t, fixturePath(t, "ui.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	ui := newHostUIStub()
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModeTUI, UI: ui})
	tool := runner.ToolDefinition("host_ui_editor")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := tool.Execute(ctx, "ui-editor", map[string]any{}, nil, runner.CreateContext()); err != nil {
		t.Fatal(err)
	}
	select {
	case editor := <-ui.editorMounted:
		if editor == nil {
			t.Fatal("host-backed editor was not mounted")
		}
	case <-ctx.Done():
		t.Fatal("host-backed editor mount blocked the protocol reader")
	}
}

func TestDialogCancellationBeforeHandlerRegistration(t *testing.T) {
	ui := newUIGeneration(nil)
	ui.cancelDialog("host-1")
	ctx, cancel := ui.requestContext(nil)
	defer cancel(nil)
	ui.registerDialog("host-1", cancel)
	defer ui.unregisterDialog("host-1")
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("dialog context cause = %v, want context canceled", context.Cause(ctx))
	}
}

func TestStaleUIContextDoesNotPanicHostReader(t *testing.T) {
	runner := extensions.NewRunner(extensions.NewRegistry(t.TempDir()), extensions.RunnerOptions{})
	contextValue := runner.CreateContext()
	runner.Invalidate("stale")
	generation := &generation{}
	generation.ui = newUIGeneration(generation)
	generation.ui.contexts["stale"] = contextValue
	request, err := json.Marshal(wireUIRequest{ContextID: "stale", Method: "notify"})
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{}
	manager.handleUIEvent(generation, request)
	if _, protocolErr := manager.handleUIRequest(generation, frame{Params: request}); protocolErr == nil || protocolErr.Code != "stale_ui_context" {
		t.Fatalf("stale UI request error = %#v", protocolErr)
	}
}

type panickingHostUI struct{ extensions.NoopUI }

func (*panickingHostUI) Notify(string, extensions.NotificationType) { panic("active UI panic") }

func TestActiveUIEventPanicIsNotSwallowed(t *testing.T) {
	runner := extensions.NewRunner(extensions.NewRegistry(t.TempDir()), extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &panickingHostUI{}})
	generation := &generation{}
	generation.ui = newUIGeneration(generation)
	generation.ui.contexts["active"] = runner.CreateContext()
	request, err := json.Marshal(wireUIRequest{ContextID: "active", Method: "notify"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recovered := recover(); recovered != "active UI panic" {
			t.Fatalf("active UI panic = %v, want propagation", recovered)
		}
	}()
	(&Manager{}).handleUIEvent(generation, request)
	t.Fatal("active UI panic was swallowed")
}

func TestWireComponentDoesNotReuseFrameAtDifferentWidth(t *testing.T) {
	component := &wireComponent{
		lines:          []string{"eighty-column frame"},
		renderedWidth:  80,
		requestPending: true,
	}
	if lines := component.Render(40); len(lines) != 0 {
		t.Fatalf("stale-width render = %#v, want an empty frame", lines)
	}
	if lines := component.Render(80); !reflect.DeepEqual(lines, []string{"eighty-column frame"}) {
		t.Fatalf("matching-width render = %#v", lines)
	}
}

func TestUIGenerationCloseDropsBoundContexts(t *testing.T) {
	runner := extensions.NewRunner(extensions.NewRegistry(t.TempDir()), extensions.RunnerOptions{})
	generation := &generation{}
	generation.ui = newUIGeneration(generation)
	generation.ui.contexts["active"] = runner.CreateContext()
	generation.ui.dialogs["dialog"] = nil
	generation.ui.close()
	generation.ui.contextMu.RLock()
	defer generation.ui.contextMu.RUnlock()
	if len(generation.ui.contexts) != 0 || len(generation.ui.dialogs) != 0 {
		t.Fatalf("closed UI generation retained %d contexts and %d dialogs", len(generation.ui.contexts), len(generation.ui.dialogs))
	}
}

func TestPendingHostDialogGetsTypedRestartCancellation(t *testing.T) {
	manager, registry, _, result, cwd := startFixtureManager(t, fixturePath(t, "ui.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	ui := newHostUIStub()
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModeTUI, UI: ui})
	pending := runner.ToolDefinition("host_ui_pending_dialog")
	crash := runner.ToolDefinition("host_ui_crash")
	resultCh := make(chan error, 1)
	go func() {
		_, err := pending.Execute(context.Background(), "pending", map[string]any{}, nil, runner.CreateContext())
		resultCh <- err
	}()
	select {
	case <-ui.pendingDialogStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("pending dialog did not start")
	}
	_, _ = crash.Execute(context.Background(), "crash", map[string]any{}, nil, runner.CreateContext())
	select {
	case cause := <-ui.pendingDialogCancelled:
		var cancellation *UIDialogCancellationError
		if !errors.Is(cause, ErrUIDialogHostRestarted) || !errors.As(cause, &cancellation) || cancellation.Reason != UIDialogCancellationHostRestarted {
			t.Fatalf("dialog cancellation = %v", cause)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("pending dialog was not cancelled")
	}
	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("pending tool unexpectedly succeeded")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("pending tool did not finish")
	}
	deadline := time.Now().Add(30 * time.Second)
	for manager.RestartCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.RestartCount() == 0 {
		t.Fatal("manager did not restart after fixture crash")
	}
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
