package jsbridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// ─── scriptedUI custom/editor overrides ─────────────────────

func (ui *scriptedUI) Custom(ctx context.Context, factory extensions.CustomFactory, options *extensions.CustomOptions) (any, bool, error) {
	ui.mu.Lock()
	ui.customOptions = append(ui.customOptions, options)
	drive := ui.customDrive
	cancel := ui.customCancel
	ui.mu.Unlock()
	done := make(chan any, 1)
	component, err := factory(&stubHost{width: 80, height: 24}, tagTheme{}, stubKeybindings{}, func(result any) {
		select {
		case done <- result:
		default:
		}
	})
	if err != nil {
		return nil, false, err
	}
	if cancel {
		if disposable, ok := component.(extensions.DisposableComponent); ok {
			disposable.Dispose()
		}
		return nil, false, nil
	}
	if drive != nil {
		drive(component)
	}
	select {
	case result := <-done:
		if disposable, ok := component.(extensions.DisposableComponent); ok {
			disposable.Dispose()
		}
		return result, true, nil
	case <-time.After(5 * time.Second):
		return nil, false, nil
	case <-ctx.Done():
		return nil, false, nil
	}
}

func (ui *scriptedUI) SetEditorComponent(factory extensions.EditorFactory) {
	ui.mu.Lock()
	ui.editorFactories = append(ui.editorFactories, factory)
	ui.mu.Unlock()
}

func (ui *scriptedUI) GetEditorComponent() extensions.EditorFactory {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if len(ui.editorFactories) == 0 {
		return nil
	}
	return ui.editorFactories[len(ui.editorFactories)-1]
}

type stubKeybindings struct{}

func (stubKeybindings) Matches(input, binding string) bool { return input == "<"+binding+">" }
func (stubKeybindings) Keys(binding string) []string       { return []string{binding} }
func (stubKeybindings) Definition(string) extensions.KeybindingDefinition {
	return extensions.KeybindingDefinition{}
}
func (stubKeybindings) Conflicts() []extensions.KeybindingConflict { return nil }
func (stubKeybindings) UserBindings() map[string][]string          { return nil }
func (stubKeybindings) ResolvedBindings() map[string][]string      { return nil }

type recordedOverlayHandle struct {
	mu      sync.Mutex
	calls   []string
	hidden  bool
	focused bool
}

func (handle *recordedOverlayHandle) record(call string) {
	handle.mu.Lock()
	handle.calls = append(handle.calls, call)
	handle.mu.Unlock()
}

func (handle *recordedOverlayHandle) callList() []string {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return append([]string(nil), handle.calls...)
}

func (handle *recordedOverlayHandle) Hide()            { handle.record("hide") }
func (handle *recordedOverlayHandle) SetHidden(h bool) { handle.record("setHidden"); handle.hidden = h }
func (handle *recordedOverlayHandle) IsHidden() bool   { handle.record("isHidden"); return handle.hidden }
func (handle *recordedOverlayHandle) Focus()           { handle.record("focus") }
func (handle *recordedOverlayHandle) Unfocus(...extensions.OverlayUnfocusOptions) {
	handle.record("unfocus")
}
func (handle *recordedOverlayHandle) IsFocused() bool {
	handle.record("isFocused")
	return handle.focused
}

// recordingEditor is the registered CustomEditor base double.
type recordingEditor struct {
	mu     sync.Mutex
	text   string
	inputs []string
}

func (editor *recordingEditor) Render(width int) []string {
	return []string{"top", "text:" + editor.getTextLocked(), strings.Repeat("-", min(width, 20))}
}

func (editor *recordingEditor) getTextLocked() string {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.text
}

func (editor *recordingEditor) GetText() string { return editor.getTextLocked() }

func (editor *recordingEditor) SetText(text string) {
	editor.mu.Lock()
	editor.text = text
	editor.mu.Unlock()
}

func (editor *recordingEditor) HandleInput(data string) {
	editor.mu.Lock()
	editor.inputs = append(editor.inputs, data)
	editor.mu.Unlock()
}

func (editor *recordingEditor) inputList() []string {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return append([]string(nil), editor.inputs...)
}

// ─── ctx.ui.custom tests ────────────────────────────────────

func TestUICustomResolvesThroughDoneWithOverlayOptions(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("open", { handler: async (_args, ctx) => {
    const result = await ctx.ui.custom(
      (tui, theme, keybindings, done) => ({
        render: (width) => [theme.fg("accent", "w:" + width + " kb:" + keybindings.matches("<app.interrupt>", "app.interrupt"))],
        handleInput: (data) => done("done:" + data),
        dispose: () => ctx.ui.notify("disposed", "info"),
      }),
      {
        overlay: true,
        overlayOptions: { anchor: "top-right", width: "50%", col: 3, minWidth: 12, nonCapturing: true },
        onHandle: (handle) => { if (!handle.isFocused()) handle.focus(); },
      },
    );
    ctx.ui.notify("custom:" + result, "info");
  }});
}
`
	ui := newScriptedUI()
	var rendered []string
	ui.customDrive = func(component extensions.Component) {
		rendered = component.Render(44)
		if focusable, ok := component.(interface{ HandleInput(string) }); ok {
			focusable.HandleInput("x")
		}
	}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"custom.ts", source}}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	if err := runner.Command("open").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "custom:done:x", "info")
	requireNotified(t, ui, "disposed", "info")
	if len(rendered) != 1 || !strings.Contains(rendered[0], "w:44") || !strings.Contains(rendered[0], "kb:true") {
		t.Fatalf("custom render = %#v", rendered)
	}
	ui.mu.Lock()
	options := ui.customOptions[len(ui.customOptions)-1]
	ui.mu.Unlock()
	if options == nil || !options.Overlay || options.StaticOverlayOptions == nil {
		t.Fatalf("custom options = %#v", options)
	}
	static := options.StaticOverlayOptions
	if static.Anchor != extensions.OverlayTopRight || static.Width != "50%" || static.MinWidth != 12 || !static.NonCapturing {
		t.Fatalf("overlay options = %#v", static)
	}
	if column, ok := static.Column.(int64); !ok || column != 3 {
		t.Fatalf("overlay column = %#v", static.Column)
	}
	if options.OnHandle == nil {
		t.Fatal("onHandle was not decoded")
	}
	handle := &recordedOverlayHandle{}
	options.OnHandle(handle)
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls := handle.callList()
		if len(calls) >= 2 {
			if calls[0] != "isFocused" || calls[1] != "focus" {
				t.Fatalf("handle calls = %#v", calls)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("onHandle callback never ran; calls = %#v", handle.callList())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUICustomCancelledResolvesUndefined(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("open", { handler: async (_args, ctx) => {
    const result = await ctx.ui.custom((tui, theme, keybindings, done) => ({ render: () => ["c"] }));
    ctx.ui.notify("cancelled:" + result, "info");
  }});
}
`
	ui := newScriptedUI()
	ui.customCancel = true
	runner := loadBridgeRunner(t, project, []bridgeSource{{"custom.ts", source}}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	if err := runner.Command("open").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "cancelled:undefined", "info")
}

func TestUICustomDynamicOverlayOptionsAndAsyncFactory(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("open", { handler: async (_args, ctx) => {
    let width = 20;
    const result = await ctx.ui.custom(
      async (tui, theme, keybindings, done) => {
        await Promise.resolve();
        return { render: () => ["async"], handleInput: (d) => done(d) };
      },
      { overlay: true, overlayOptions: () => ({ anchor: "bottom", width: width += 1 }) },
    );
    ctx.ui.notify("async:" + result, "info");
  }});
}
`
	ui := newScriptedUI()
	ui.customDrive = func(component extensions.Component) {
		if focusable, ok := component.(interface{ HandleInput(string) }); ok {
			focusable.HandleInput("ok")
		}
	}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"custom.ts", source}}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	if err := runner.Command("open").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "async:ok", "info")
	ui.mu.Lock()
	options := ui.customOptions[len(ui.customOptions)-1]
	ui.mu.Unlock()
	if options == nil || options.DynamicOverlayOptions == nil || options.StaticOverlayOptions != nil {
		t.Fatalf("dynamic custom options = %#v", options)
	}
	first := options.DynamicOverlayOptions()
	second := options.DynamicOverlayOptions()
	if first.Anchor != extensions.OverlayBottom || first.Width == second.Width {
		t.Fatalf("dynamic overlay options = %#v then %#v", first, second)
	}
}

// ─── editor replacement tests ───────────────────────────────

func TestUISetEditorComponentRoundTripAndIdentity(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  const factory = (tui, theme, keybindings) => {
    let text = "initial";
    return {
      render: (width) => ["editor:" + width],
      getText: () => text,
      setText: (value) => { text = value; },
      handleInput: (data) => { text += "|" + data; },
    };
  };
  pi.registerCommand("set", { handler: async (_args, ctx) => {
    ctx.ui.setEditorComponent(factory);
    ctx.ui.notify("same:" + (ctx.ui.getEditorComponent() === factory), "info");
  }});
  pi.registerCommand("clear", { handler: async (_args, ctx) => {
    ctx.ui.setEditorComponent(undefined);
    ctx.ui.notify("cleared:" + (ctx.ui.getEditorComponent() === undefined), "info");
  }});
}
`
	ui := newScriptedUI()
	runner := loadBridgeRunner(t, project, []bridgeSource{{"editor.ts", source}}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	if err := runner.Command("set").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "same:true", "info")
	factory := ui.GetEditorComponent()
	if factory == nil {
		t.Fatal("editor factory was not forwarded to the UI seam")
	}
	editor := factory(&stubHost{width: 80, height: 24}, tagTheme{}, stubKeybindings{})
	if editor == nil {
		t.Fatal("bridged editor factory returned nil")
	}
	if lines := editor.Render(33); len(lines) != 1 || lines[0] != "editor:33" {
		t.Fatalf("editor render = %#v", lines)
	}
	if text := editor.GetText(); text != "initial" {
		t.Fatalf("editor text = %q", text)
	}
	editor.SetText("swapped")
	editor.HandleInput("k")
	if text := editor.GetText(); text != "swapped|k" {
		t.Fatalf("editor text after input = %q", text)
	}
	if err := runner.Command("clear").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	requireNotified(t, ui, "cleared:true", "info")
	if cleared := ui.GetEditorComponent(); cleared != nil {
		t.Fatal("clearing the editor factory did not reach the UI seam")
	}
}

func TestUIExampleModalEditorRunsOnCustomEditorBase(t *testing.T) {
	base := &recordingEditor{text: "seed"}
	extensions.RegisterCustomEditorBase(func(extensions.UIHost, extensions.Theme, extensions.Keybindings) extensions.EditorComponent {
		return base
	})
	t.Cleanup(func() { extensions.RegisterCustomEditorBase(nil) })

	project := t.TempDir()
	ui := newScriptedUI()
	runner := loadUIExample(t, project, "modal-editor.ts", ui, extensions.RunnerOptions{})
	runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})

	factory := ui.GetEditorComponent()
	if factory == nil {
		t.Fatal("modal-editor did not register an editor factory")
	}
	editor := factory(&stubHost{width: 80, height: 24}, tagTheme{}, stubKeybindings{})
	if editor == nil {
		t.Fatal("modal editor factory returned nil")
	}

	// Insert mode: printable input reaches the base editor.
	editor.HandleInput("x")
	// Escape switches to normal mode without reaching the base.
	editor.HandleInput("\x1b")
	// Normal mode: h maps to the left-arrow escape sequence.
	editor.HandleInput("h")
	// Normal mode: printable characters without a mapping are swallowed.
	editor.HandleInput("q")
	// i returns to insert mode; typing reaches the base again.
	editor.HandleInput("i")
	editor.HandleInput("y")
	if inputs := base.inputList(); len(inputs) != 3 || inputs[0] != "x" || inputs[1] != "\x1b[D" || inputs[2] != "y" {
		t.Fatalf("base editor inputs = %#v", inputs)
	}

	if text := editor.GetText(); text != "seed" {
		t.Fatalf("modal editor text = %q", text)
	}
	editor.SetText("hello")
	if text := base.GetText(); text != "hello" {
		t.Fatalf("base text after setText = %q", text)
	}

	// Normal-mode indicator lands on the bottom border line.
	editor.HandleInput("\x1b")
	lines := editor.Render(40)
	if len(lines) != 3 || !strings.Contains(lines[len(lines)-1], "NORMAL") {
		t.Fatalf("modal render = %#v", lines)
	}
}

func TestUICustomExampleFixturesLoad(t *testing.T) {
	base := &recordingEditor{}
	extensions.RegisterCustomEditorBase(func(extensions.UIHost, extensions.Theme, extensions.Keybindings) extensions.EditorComponent {
		return base
	})
	t.Cleanup(func() { extensions.RegisterCustomEditorBase(nil) })
	for _, name := range []string{
		"border-status-editor.ts",
		"interactive-shell.ts",
		"rainbow-editor.ts",
		"snake.ts",
		"space-invaders.ts",
		"tools.ts",
	} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			ui := newScriptedUI()
			runner := loadUIExample(t, project, name, ui, extensions.RunnerOptions{})
			runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
		})
	}
}
