package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	theme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/tui"
)

type f12UILifecycleFixture struct {
	SchemaVersion int `json:"schemaVersion"`
	Reset         struct {
		Events        []string `json:"events"`
		DisposalOrder []string `json:"disposalOrder"`
		Final         struct {
			SelectorPresent          bool   `json:"selectorPresent"`
			InputPresent             bool   `json:"inputPresent"`
			EditorDialogPresent      bool   `json:"editorDialogPresent"`
			AutocompleteWrappers     int    `json:"autocompleteWrappers"`
			ExtensionShortcutPresent bool   `json:"extensionShortcutPresent"`
			WorkingMessage           any    `json:"workingMessage"`
			WorkingVisible           bool   `json:"workingVisible"`
			HiddenThinkingLabel      string `json:"hiddenThinkingLabel"`
		} `json:"final"`
	} `json:"reset"`
	Widgets struct {
		Snapshots []struct {
			Step  string   `json:"step"`
			Above []string `json:"above"`
			Below []string `json:"below"`
		} `json:"snapshots"`
		Events []string `json:"events"`
	} `json:"widgets"`
	HiddenThinking struct {
		Custom             f12ThinkingSnapshot `json:"custom"`
		Reset              f12ThinkingSnapshot `json:"reset"`
		RequestRenderCount int                 `json:"requestRenderCount"`
	} `json:"hiddenThinking"`
	ToolsExpanded struct {
		Events []string `json:"events"`
		Final  struct {
			ToolsExpanded    bool   `json:"toolsExpanded"`
			ActiveHeader     string `json:"activeHeader"`
			BuiltInExpanded  bool   `json:"builtInExpanded"`
			ResourceExpanded bool   `json:"resourceExpanded"`
			ChatExpanded     bool   `json:"chatExpanded"`
		} `json:"final"`
	} `json:"toolsExpanded"`
	CustomOverlay struct {
		Options struct {
			DynamicCalls   int `json:"dynamicCalls"`
			DynamicOptions struct {
				Anchor string `json:"anchor"`
				Width  string `json:"width"`
				Margin struct {
					Top    int `json:"top"`
					Right  int `json:"right"`
					Bottom int `json:"bottom"`
					Left   int `json:"left"`
				} `json:"margin"`
				OffsetX int `json:"offsetX"`
				OffsetY int `json:"offsetY"`
			} `json:"dynamicOptions"`
			FallbackOptions struct {
				Width int `json:"width"`
			} `json:"fallbackOptions"`
		} `json:"options"`
		EarlyDone struct {
			Result       string `json:"result"`
			OverlayShows int    `json:"overlayShows"`
			DisposeCount int    `json:"disposeCount"`
		} `json:"earlyDone"`
		DisposeFailure struct {
			Result       string `json:"result"`
			DisposeCount int    `json:"disposeCount"`
			Rejected     bool   `json:"rejected"`
		} `json:"disposeFailure"`
		FactoryFailure string `json:"factoryFailure"`
		Handle         struct {
			Initial                   f12OverlayObservation `json:"initial"`
			UnfocusedToPrevious       f12OverlayObservation `json:"unfocusedToPrevious"`
			UnfocusedToTarget         f12OverlayObservation `json:"unfocusedToTarget"`
			UnfocusedToNull           f12OverlayObservation `json:"unfocusedToNull"`
			TemporaryHidden           f12OverlayObservation `json:"temporaryHidden"`
			TemporaryRestored         f12OverlayObservation `json:"temporaryRestored"`
			PermanentlyHidden         f12OverlayObservation `json:"permanentlyHidden"`
			AfterPermanentShowAttempt f12OverlayObservation `json:"afterPermanentShowAttempt"`
			Result                    string                `json:"result"`
			DisposeCount              int                   `json:"disposeCount"`
		} `json:"handle"`
	} `json:"customOverlay"`
}

type f12ThinkingSnapshot struct {
	Historical  []string `json:"historical"`
	Streaming   []string `json:"streaming"`
	StoredLabel string   `json:"storedLabel"`
}

type f12OverlayObservation struct {
	HasOverlay     bool `json:"hasOverlay"`
	Hidden         bool `json:"hidden"`
	HandleFocused  bool `json:"handleFocused"`
	EditorFocused  bool `json:"editorFocused"`
	OverlayFocused bool `json:"overlayFocused"`
}

type f12UILifecycleManifest struct {
	Family         string   `json:"family"`
	UpstreamCommit string   `json:"upstreamCommit"`
	Generator      string   `json:"generator"`
	Sources        []string `json:"sources"`
	Files          []string `json:"files"`
}

type f12UILifecycleTraceComponent struct {
	mu       sync.Mutex
	label    string
	events   *[]string
	expanded bool
}

func (component *f12UILifecycleTraceComponent) Render(int) []string {
	return []string{component.label}
}

func (component *f12UILifecycleTraceComponent) Dispose() {
	component.record("dispose:" + component.label)
}

func (component *f12UILifecycleTraceComponent) SetExpanded(expanded bool) {
	component.mu.Lock()
	component.expanded = expanded
	component.mu.Unlock()
	component.record("expand:" + component.label + ":" + map[bool]string{true: "true", false: "false"}[expanded])
}

func (component *f12UILifecycleTraceComponent) record(event string) {
	component.mu.Lock()
	defer component.mu.Unlock()
	*component.events = append(*component.events, event)
}

func (component *f12UILifecycleTraceComponent) isExpanded() bool {
	component.mu.Lock()
	defer component.mu.Unlock()
	return component.expanded
}

type f12UILifecycleEditor struct {
	mu      sync.Mutex
	text    string
	focused bool
	inputs  []string
}

func (*f12UILifecycleEditor) Render(int) []string { return []string{"EDITOR"} }
func (editor *f12UILifecycleEditor) HandleInput(data string) {
	editor.mu.Lock()
	editor.inputs = append(editor.inputs, data)
	editor.mu.Unlock()
}
func (editor *f12UILifecycleEditor) GetText() string {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.text
}
func (editor *f12UILifecycleEditor) SetText(text string) {
	editor.mu.Lock()
	editor.text = text
	editor.mu.Unlock()
}
func (editor *f12UILifecycleEditor) SetFocused(focused bool) {
	editor.mu.Lock()
	editor.focused = focused
	editor.mu.Unlock()
}
func (editor *f12UILifecycleEditor) state() (bool, []string) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.focused, append([]string(nil), editor.inputs...)
}

type f12UILifecycleOverlayComponent struct {
	mu             sync.Mutex
	label          string
	width          int
	focused        bool
	renderWidths   []int
	disposeCount   int
	panicOnDispose bool
}

func (component *f12UILifecycleOverlayComponent) Render(width int) []string {
	component.mu.Lock()
	component.renderWidths = append(component.renderWidths, width)
	component.mu.Unlock()
	return []string{component.label}
}

func (*f12UILifecycleOverlayComponent) HandleInput(tui.KeyEvent) {}

func (component *f12UILifecycleOverlayComponent) SetFocused(focused bool) {
	component.mu.Lock()
	component.focused = focused
	component.mu.Unlock()
}

func (component *f12UILifecycleOverlayComponent) Dispose() {
	component.mu.Lock()
	component.disposeCount++
	panicOnDispose := component.panicOnDispose
	component.mu.Unlock()
	if panicOnDispose {
		panic("dispose failed")
	}
}

func (component *f12UILifecycleOverlayComponent) Width() int { return component.width }

func (component *f12UILifecycleOverlayComponent) state() (bool, int, []int) {
	component.mu.Lock()
	defer component.mu.Unlock()
	return component.focused, component.disposeCount, append([]int(nil), component.renderWidths...)
}

func TestF12UILifecycleManifestIsPinned(t *testing.T) {
	var manifest f12UILifecycleManifest
	f12UILifecycleLoadJSON(t, "manifest.json", &manifest)
	if manifest.Family != "F12-ui-lifecycle" || manifest.Generator != "conformance/extract/f12-ui-lifecycle.ts" {
		t.Fatalf("unexpected UI lifecycle manifest: %+v", manifest)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve UPSTREAM.lock path")
	}
	var lock struct {
		Commit string `json:"commit"`
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "UPSTREAM.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &lock); err != nil {
		t.Fatal(err)
	}
	if manifest.UpstreamCommit != lock.Commit {
		t.Fatalf("UI lifecycle upstream commit = %q, lock = %q", manifest.UpstreamCommit, lock.Commit)
	}
	if !reflect.DeepEqual(manifest.Files, []string{"lifecycle.json"}) || len(manifest.Sources) != 8 {
		t.Fatalf("UI lifecycle manifest coverage = files %v sources %v", manifest.Files, manifest.Sources)
	}
}

func TestF12ResetExtensionUILifecycleMatchesUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(40, 24))
	bindings := NewAppKeybindings(nil)
	editor := NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode := &InteractiveMode{
		ui: modeUI, keybindings: bindings, editor: editor,
		header: &tui.Container{}, chat: &tui.Container{}, status: &tui.Container{},
		widgetAbove: &tui.Container{}, editorContainer: &tui.Container{}, widgetBelow: &tui.Container{},
		footer: &tui.Container{}, footerStatuses: map[string]string{"extension": "active"},
	}
	mode.editorContainer.AddChild(editor)
	ui := NewInteractiveUI(mode)
	mode.interactiveUI = ui

	disposals := make([]string, 0, len(fixture.Reset.DisposalOrder))
	component := func(label string) *f12UILifecycleTraceComponent {
		return &f12UILifecycleTraceComponent{label: label, events: &disposals}
	}
	ui.SetFooter(func(extensions.UIHost, extensions.Theme, extensions.FooterDataProvider) extensions.Component {
		return component("footer")
	})
	ui.SetHeader(func(extensions.UIHost, extensions.Theme) extensions.Component {
		return component("header")
	})
	ui.SetWidget("above", &extensions.Widget{Factory: func(extensions.UIHost, extensions.Theme) extensions.Component {
		return component("widget-above")
	}}, nil)
	ui.SetWidget("below", &extensions.Widget{Factory: func(extensions.UIHost, extensions.Theme) extensions.Component {
		return component("widget-below")
	}}, &extensions.WidgetOptions{Placement: extensions.WidgetBelowEditor})
	ui.mu.Lock()
	ui.acProviders = []extensions.AutocompleteProviderFactory{func(provider extensions.AutocompleteProvider) extensions.AutocompleteProvider { return provider }}
	ui.editorFactory = func(extensions.UIHost, extensions.Theme, extensions.Keybindings) extensions.EditorComponent {
		return &f12UILifecycleEditor{}
	}
	ui.mu.Unlock()
	disposals = disposals[:0]
	mode.thinkingLabel = "Extension thinking"
	mode.editor.OnExtensionShortcut = func(string) bool { return true }

	type modeResetter interface{ resetExtensionUI() }
	type uiResetter interface{ resetExtensionUI() }
	if resetter, ok := any(mode).(modeResetter); ok {
		resetter.resetExtensionUI()
	} else if resetter, ok := any(ui).(uiResetter); ok {
		resetter.resetExtensionUI()
	} else {
		mode.detachSession()
	}

	ui.mu.Lock()
	autocompleteWrappers := len(ui.acProviders)
	editorFactoryPresent := ui.editorFactory != nil
	widgetsRemaining := len(ui.widgets)
	ui.mu.Unlock()
	mode.mu.Lock()
	actualFinal := struct {
		AutocompleteWrappers     int
		ExtensionShortcutPresent bool
		HiddenThinkingLabel      string
		EditorFactoryPresent     bool
		WidgetsRemaining         int
		StatusesRemaining        int
	}{
		AutocompleteWrappers:     autocompleteWrappers,
		ExtensionShortcutPresent: mode.editor.OnExtensionShortcut != nil,
		HiddenThinkingLabel:      mode.thinkingLabel,
		EditorFactoryPresent:     editorFactoryPresent,
		WidgetsRemaining:         widgetsRemaining,
		StatusesRemaining:        len(mode.footerStatuses),
	}
	mode.mu.Unlock()
	wantFinal := struct {
		AutocompleteWrappers     int
		ExtensionShortcutPresent bool
		HiddenThinkingLabel      string
		EditorFactoryPresent     bool
		WidgetsRemaining         int
		StatusesRemaining        int
	}{
		AutocompleteWrappers:     fixture.Reset.Final.AutocompleteWrappers,
		ExtensionShortcutPresent: fixture.Reset.Final.ExtensionShortcutPresent,
		HiddenThinkingLabel:      fixture.Reset.Final.HiddenThinkingLabel,
	}
	if !reflect.DeepEqual(actualFinal, wantFinal) {
		t.Errorf("resetExtensionUI final state differs\nwant: %+v\n got: %+v", wantFinal, actualFinal)
	}
	actualDisposalOrder := make([]string, len(disposals))
	for index, event := range disposals {
		actualDisposalOrder[index] = strings.TrimPrefix(event, "dispose:")
	}
	if !reflect.DeepEqual(actualDisposalOrder, fixture.Reset.DisposalOrder) {
		t.Errorf("resetExtensionUI disposal order = %v, want %v", actualDisposalOrder, fixture.Reset.DisposalOrder)
	}
}

func TestF12ResetExtensionUICancelsLiveDialogsInUpstreamOrder(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	wantCloseOrder := []string{"selector:hide", "input:hide", "editor-dialog:hide"}
	if len(fixture.Reset.Events) < len(wantCloseOrder) ||
		!reflect.DeepEqual(fixture.Reset.Events[:len(wantCloseOrder)], wantCloseOrder) {
		t.Fatalf("pinned reset dialog order = %v, want %v", fixture.Reset.Events, wantCloseOrder)
	}
	if fixture.Reset.Final.SelectorPresent || fixture.Reset.Final.InputPresent || fixture.Reset.Final.EditorDialogPresent {
		t.Fatalf("pinned reset leaves a dialog present: %+v", fixture.Reset.Final)
	}

	f12UILifecycleInitTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(40, 24))
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	mode := &InteractiveMode{
		ui: modeUI, keybindings: bindings,
		editorContainer: &tui.Container{}, widgetAbove: &tui.Container{}, widgetBelow: &tui.Container{},
		footerStatuses: make(map[string]string),
	}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	ui := NewInteractiveUI(mode)
	mode.interactiveUI = ui

	type dialogResult struct {
		value string
		ok    bool
		err   error
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	selectorResult := make(chan dialogResult, 1)
	inputResult := make(chan dialogResult, 1)
	editorResult := make(chan dialogResult, 1)

	waitForDialog := func(kind string) tui.Component {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			children := mode.editorContainer.Children()
			if len(children) == 1 {
				matched := false
				switch kind {
				case "selector":
					_, matched = children[0].(*ExtensionSelectorComponent)
				case "input":
					_, matched = children[0].(*ExtensionInputComponent)
				case "editor":
					_, matched = children[0].(*ExtensionEditorComponent)
				}
				if matched {
					return children[0]
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s dialog was not installed in the editor slot", kind)
			}
			time.Sleep(time.Millisecond)
		}
	}

	go func() {
		value, ok, err := ui.Select(ctx, "Pick one", []string{"alpha", "beta"}, nil)
		selectorResult <- dialogResult{value: value, ok: ok, err: err}
	}()
	selector := waitForDialog("selector").(*ExtensionSelectorComponent)
	go func() {
		value, ok, err := ui.Input(ctx, "Enter value", nil, nil)
		inputResult <- dialogResult{value: value, ok: ok, err: err}
	}()
	input := waitForDialog("input").(*ExtensionInputComponent)
	go func() {
		value, ok, err := ui.Editor(ctx, "Edit value", nil)
		editorResult <- dialogResult{value: value, ok: ok, err: err}
	}()
	editorDialog := waitForDialog("editor").(*ExtensionEditorComponent)

	var closeOrderMu sync.Mutex
	closeOrder := make([]string, 0, len(wantCloseOrder))
	recordClose := func(event string, next func()) func() {
		return func() {
			closeOrderMu.Lock()
			closeOrder = append(closeOrder, event)
			closeOrderMu.Unlock()
			next()
		}
	}
	selector.onCancel = recordClose(wantCloseOrder[0], selector.onCancel)
	input.onCancel = recordClose(wantCloseOrder[1], input.onCancel)
	editorDialog.onCancel = recordClose(wantCloseOrder[2], editorDialog.onCancel)

	ui.resetExtensionUI()
	if ctx.Err() != nil {
		t.Fatalf("resetExtensionUI cancelled the caller context: %v", ctx.Err())
	}

	channels := []chan dialogResult{selectorResult, inputResult, editorResult}
	results := make([]dialogResult, len(channels))
	deadline := time.NewTimer(250 * time.Millisecond)
	defer deadline.Stop()
	for index, resultChannel := range channels {
		select {
		case results[index] = <-resultChannel:
		case <-deadline.C:
			cancel()
			for cleanupIndex := index; cleanupIndex < len(channels); cleanupIndex++ {
				select {
				case <-channels[cleanupIndex]:
				case <-time.After(time.Second):
				}
			}
			t.Fatalf("resetExtensionUI left %s blocked until caller-context cancellation", wantCloseOrder[index])
		}
	}
	for index, result := range results {
		if result.value != "" || result.ok || result.err != nil {
			t.Errorf("%s result = %+v, want cancelled without error", wantCloseOrder[index], result)
		}
	}
	closeOrderMu.Lock()
	actualCloseOrder := append([]string(nil), closeOrder...)
	closeOrderMu.Unlock()
	if !reflect.DeepEqual(actualCloseOrder, wantCloseOrder) {
		t.Errorf("resetExtensionUI dialog close order = %v, want %v", actualCloseOrder, wantCloseOrder)
	}
}

func TestF12WidgetLifecycleMatchesUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	type outcome struct {
		snapshots []struct {
			Step  string
			Above []string
			Below []string
		}
		events []string
	}
	completed := make(chan outcome, 1)
	go func() {
		modeUI := tui.NewTUI(newFakeTerminal(40, 24))
		mode := &InteractiveMode{ui: modeUI, widgetAbove: &tui.Container{}, widgetBelow: &tui.Container{}}
		ui := NewInteractiveUI(mode)
		events := []string{}
		result := outcome{events: events}
		capture := func(step string) {
			result.snapshots = append(result.snapshots, struct {
				Step  string
				Above []string
				Below []string
			}{step, mode.widgetAbove.Render(40), mode.widgetBelow.Render(40)})
		}
		component := func(label string) *f12UILifecycleTraceComponent {
			return &f12UILifecycleTraceComponent{label: label, events: &events}
		}
		capture("empty")
		lines := make([]string, 11)
		for index := range lines {
			lines[index] = "line-" + string(rune('1'+index))
		}
		lines[9], lines[10] = "line-10", "line-11"
		ui.SetWidget("capped", &extensions.Widget{Lines: lines}, nil)
		capture("eleven-lines-capped")
		ui.SetWidget("capped", &extensions.Widget{Factory: func(extensions.UIHost, extensions.Theme) extensions.Component {
			events = append(events, "factory:replacement")
			return component("replacement")
		}}, &extensions.WidgetOptions{Placement: extensions.WidgetBelowEditor})
		capture("replacement-moved-below")
		ui.SetWidget("outer", &extensions.Widget{Factory: func(extensions.UIHost, extensions.Theme) extensions.Component {
			events = append(events, "factory:outer:start")
			ui.SetWidget("nested", &extensions.Widget{Factory: func(extensions.UIHost, extensions.Theme) extensions.Component {
				events = append(events, "factory:nested")
				return component("nested")
			}}, &extensions.WidgetOptions{Placement: extensions.WidgetBelowEditor})
			events = append(events, "factory:outer:end")
			return component("outer")
		}}, nil)
		capture("reentrant-factory")
		ui.SetWidget("capped", nil, nil)
		capture("removed-by-key")
		ui.SetWidget("outer", nil, nil)
		ui.SetWidget("nested", nil, nil)
		capture("cleared")
		result.events = events
		completed <- result
	}()

	select {
	case actual := <-completed:
		if !reflect.DeepEqual(actual.snapshots, f12WidgetSnapshots(fixture)) {
			wantJSON, _ := json.MarshalIndent(f12WidgetSnapshots(fixture), "", "  ")
			gotJSON, _ := json.MarshalIndent(actual.snapshots, "", "  ")
			t.Errorf("widget lifecycle frames differ\nwant: %s\n got: %s", wantJSON, gotJSON)
		}
		wantEvents := make([]string, 0, len(fixture.Widgets.Events))
		for _, event := range fixture.Widgets.Events {
			if event != "render" {
				wantEvents = append(wantEvents, event)
			}
		}
		if !reflect.DeepEqual(actual.events, wantEvents) {
			t.Errorf("widget lifecycle events = %v, want %v", actual.events, wantEvents)
		}
	case <-time.After(time.Second):
		t.Fatal("widget factory re-entry deadlocked; upstream permits setWidget from inside a factory")
	}
}

func TestF12HiddenThinkingLifecycleMatchesUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(48, 24))
	message := f12UILifecycleAssistantMessage()
	historical := NewAssistantMessageComponent(message, true, theme.MarkdownTheme(), "Thinking...", 1)
	streaming := NewAssistantMessageComponent(message, true, theme.MarkdownTheme(), "Thinking...", 1)
	mode := &InteractiveMode{ui: modeUI, chat: &tui.Container{}, currentStreaming: streaming, thinkingHidden: true, thinkingLabel: "Thinking..."}
	mode.chat.AddChild(historical)
	ui := NewInteractiveUI(mode)
	custom := "Extension thought"
	ui.SetHiddenThinkingLabel(&custom)
	actualCustom := f12ThinkingSnapshot{Historical: historical.Render(48), Streaming: streaming.Render(48), StoredLabel: mode.thinkingLabel}
	ui.SetHiddenThinkingLabel(nil)
	actualReset := f12ThinkingSnapshot{Historical: historical.Render(48), Streaming: streaming.Render(48), StoredLabel: mode.thinkingLabel}
	if !reflect.DeepEqual(actualCustom, fixture.HiddenThinking.Custom) {
		t.Errorf("custom hidden-thinking label differs\nwant: %#v\n got: %#v", fixture.HiddenThinking.Custom, actualCustom)
	}
	if !reflect.DeepEqual(actualReset, fixture.HiddenThinking.Reset) {
		t.Errorf("reset hidden-thinking label differs\nwant: %#v\n got: %#v", fixture.HiddenThinking.Reset, actualReset)
	}
}

func TestF12ToolsExpandedPropagationMatchesUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(40, 24))
	events := []string{}
	component := func(label string) *f12UILifecycleTraceComponent {
		return &f12UILifecycleTraceComponent{label: label, events: &events}
	}
	builtIn, resource, chat := component("built-in-header"), component("resource"), component("chat")
	mode := &InteractiveMode{ui: modeUI, header: &tui.Container{}, chat: &tui.Container{}, toolComponents: map[string]*ToolExecutionComponent{}}
	mode.header.AddChild(builtIn)
	mode.chat.AddChild(resource)
	mode.chat.AddChild(chat)
	ui := NewInteractiveUI(mode)
	ui.SetToolsExpanded(true)
	var custom *f12UILifecycleTraceComponent
	ui.SetHeader(func(extensions.UIHost, extensions.Theme) extensions.Component {
		events = append(events, "factory:custom-header")
		custom = component("custom-header")
		return custom
	})
	ui.SetToolsExpanded(false)

	wantEvents := make([]string, 0, 8)
	for _, event := range fixture.ToolsExpanded.Events {
		if event == "render" || strings.HasPrefix(event, "dispose:") || event == "expand:built-in-header:false" {
			continue
		}
		wantEvents = append(wantEvents, event)
		if len(wantEvents) == 8 {
			break
		}
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Errorf("tools-expanded propagation = %v, want %v", events, wantEvents)
	}
	if custom == nil || custom.isExpanded() || resource.isExpanded() || chat.isExpanded() {
		t.Errorf("tools-expanded final states = custom:%v resource:%t chat:%t, want all false", custom, resource.isExpanded(), chat.isExpanded())
	}
}

func TestF12CustomOverlayOptionsMatchUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	mode, ui := f12UILifecycleOverlayHarness()
	var dynamicCalls atomic.Int32
	doneReady := make(chan extensions.CustomDone, 1)
	handleReady := make(chan extensions.OverlayHandle, 1)
	opts := &extensions.CustomOptions{
		Overlay: true,
		DynamicOverlayOptions: func() extensions.OverlayOptions {
			dynamicCalls.Add(1)
			return extensions.OverlayOptions{
				Anchor: extensions.OverlayTopRight, Width: "50%",
				Margin:  map[string]int{"top": 2, "right": 3, "bottom": 4, "left": 5},
				OffsetX: -1, OffsetY: 1,
			}
		},
		OnHandle: func(handle extensions.OverlayHandle) { handleReady <- handle },
	}
	outcome := f12UILifecycleRunCustom(t.Context(), ui, func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		doneReady <- done
		return &f12UILifecycleOverlayComponent{label: "dynamic"}, nil
	}, opts)
	f12UILifecycleReceive(t, handleReady, "dynamic overlay handle")
	done := f12UILifecycleReceive(t, doneReady, "dynamic overlay done callback")
	time.Sleep(10 * time.Millisecond)
	if got := int(dynamicCalls.Load()); got != fixture.CustomOverlay.Options.DynamicCalls {
		t.Errorf("dynamic overlay options calls = %d, want %d", got, fixture.CustomOverlay.Options.DynamicCalls)
	}
	done("dynamic-done")
	if result := f12UILifecycleReceive(t, outcome, "dynamic overlay result"); result.err != nil || !result.ok || result.value != "dynamic-done" {
		t.Errorf("dynamic overlay result = (%v, %t, %v)", result.value, result.ok, result.err)
	}

	want := fixture.CustomOverlay.Options.DynamicOptions
	static := &extensions.CustomOptions{StaticOverlayOptions: &extensions.OverlayOptions{
		Anchor: extensions.OverlayAnchor(want.Anchor), Width: want.Width,
		Margin:  map[string]int{"top": want.Margin.Top, "right": want.Margin.Right, "bottom": want.Margin.Bottom, "left": want.Margin.Left},
		OffsetX: want.OffsetX, OffsetY: want.OffsetY,
	}}
	layout := resolveOverlayLayout(static, mode.Width(), mode.Height())
	if layout.Width != mode.Width()/2 || layout.Anchor != want.Anchor || layout.OffsetX != want.OffsetX || layout.OffsetY != want.OffsetY {
		t.Errorf("custom overlay layout = %+v, want width %d anchor %q offsets (%d,%d)", layout, mode.Width()/2, want.Anchor, want.OffsetX, want.OffsetY)
	}
	top, right, bottom, left, hasMargin := f12UILifecycleOverlayMargin(layout)
	if !hasMargin || top != want.Margin.Top || right != want.Margin.Right || bottom != want.Margin.Bottom || left != want.Margin.Left {
		t.Errorf("custom overlay margin = (%d,%d,%d,%d present=%t), want %+v", top, right, bottom, left, hasMargin, want.Margin)
	}
}

func TestF12CustomOverlayComponentWidthFallbackMatchesUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	mode, ui := f12UILifecycleOverlayHarness()
	if err := mode.ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mode.ui.Stop() })
	wantWidth := fixture.CustomOverlay.Options.FallbackOptions.Width
	component := &f12UILifecycleOverlayComponent{label: "fallback", width: wantWidth}
	doneReady := make(chan extensions.CustomDone, 1)
	handleReady := make(chan extensions.OverlayHandle, 1)
	outcome := f12UILifecycleRunCustom(t.Context(), ui, func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		doneReady <- done
		return component, nil
	}, &extensions.CustomOptions{Overlay: true, OnHandle: func(handle extensions.OverlayHandle) { handleReady <- handle }})
	f12UILifecycleReceive(t, handleReady, "fallback overlay handle")
	done := f12UILifecycleReceive(t, doneReady, "fallback overlay done callback")
	deadline := time.Now().Add(time.Second)
	var widths []int
	for len(widths) == 0 && time.Now().Before(deadline) {
		mode.ui.RequestRender()
		time.Sleep(time.Millisecond)
		_, _, widths = component.state()
	}
	if !slicesContains(widths, wantWidth) {
		t.Errorf("component width fallback render widths = %v, want a %d-cell render", widths, wantWidth)
	}
	done("fallback-done")
	f12UILifecycleReceive(t, outcome, "fallback overlay result")
}

func TestF12CustomOverlayEarlyDoneAndFailuresMatchUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	t.Run("early-done", func(t *testing.T) {
		mode, ui := f12UILifecycleOverlayHarness()
		component := &f12UILifecycleOverlayComponent{label: "early"}
		value, ok, err := ui.Custom(t.Context(), func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			done(fixture.CustomOverlay.EarlyDone.Result)
			return component, nil
		}, &extensions.CustomOptions{Overlay: true})
		_, disposed, _ := component.state()
		actual := struct {
			Result       any
			OK           bool
			Err          error
			OverlayShows int
			DisposeCount int
		}{value, ok, err, map[bool]int{true: 1}[mode.ui.HasOverlay()], disposed}
		want := fixture.CustomOverlay.EarlyDone
		if actual.Result != want.Result || !actual.OK || actual.Err != nil || actual.OverlayShows != want.OverlayShows || actual.DisposeCount != want.DisposeCount {
			t.Errorf("early-done overlay = %+v, want %+v", actual, want)
		}
	})

	t.Run("dispose-failure", func(t *testing.T) {
		_, ui := f12UILifecycleOverlayHarness()
		component := &f12UILifecycleOverlayComponent{label: "throwing", panicOnDispose: true}
		doneReady := make(chan extensions.CustomDone, 1)
		handleReady := make(chan extensions.OverlayHandle, 1)
		outcome := f12UILifecycleRunCustom(t.Context(), ui, func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			doneReady <- done
			return component, nil
		}, &extensions.CustomOptions{Overlay: true, OnHandle: func(handle extensions.OverlayHandle) { handleReady <- handle }})
		f12UILifecycleReceive(t, handleReady, "throwing overlay handle")
		done := f12UILifecycleReceive(t, doneReady, "throwing overlay done callback")
		done(fixture.CustomOverlay.DisposeFailure.Result)
		result := f12UILifecycleReceive(t, outcome, "throwing overlay result")
		_, disposed, _ := component.state()
		if result.panicked != fixture.CustomOverlay.DisposeFailure.Rejected || result.err != nil || !result.ok || result.value != fixture.CustomOverlay.DisposeFailure.Result || disposed != fixture.CustomOverlay.DisposeFailure.DisposeCount {
			t.Errorf("dispose-failure overlay = result(%v,%t,%v) panicked=%t disposed=%d, want %+v", result.value, result.ok, result.err, result.panicked, disposed, fixture.CustomOverlay.DisposeFailure)
		}
	})

	t.Run("factory-failure", func(t *testing.T) {
		_, ui := f12UILifecycleOverlayHarness()
		_, ok, err := ui.Custom(t.Context(), func(extensions.UIHost, extensions.Theme, extensions.Keybindings, extensions.CustomDone) (extensions.Component, error) {
			return nil, errors.New("factory failed")
		}, &extensions.CustomOptions{Overlay: true})
		if ok || err == nil || err.Error() != fixture.CustomOverlay.FactoryFailure {
			t.Errorf("factory failure = (ok %t, err %v), want %q", ok, err, fixture.CustomOverlay.FactoryFailure)
		}
	})
}

func TestF12CustomOverlayHandleLifecycleMatchesUpstream(t *testing.T) {
	fixture := f12UILifecycleLoadFixture(t)
	f12UILifecycleInitTheme(t)
	mode, ui := f12UILifecycleOverlayHarness()
	editor := &f12UILifecycleEditor{text: "draft"}
	mode.extensionEditor = editor
	mode.restoreEditorComponent()
	mode.ui.SetFocus(extensionEditorAdapter{EditorComponent: editor})
	component := &f12UILifecycleOverlayComponent{label: "overlay"}
	doneReady := make(chan extensions.CustomDone, 1)
	handleReady := make(chan extensions.OverlayHandle, 1)
	outcome := f12UILifecycleRunCustom(t.Context(), ui, func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		doneReady <- done
		return component, nil
	}, &extensions.CustomOptions{Overlay: true, OnHandle: func(handle extensions.OverlayHandle) { handleReady <- handle }})
	handle := f12UILifecycleReceive(t, handleReady, "overlay handle")
	done := f12UILifecycleReceive(t, doneReady, "overlay done callback")
	observe := func() f12OverlayObservation {
		focused, _, _ := component.state()
		editorFocused, _ := editor.state()
		return f12OverlayObservation{
			HasOverlay: mode.ui.HasOverlay(), Hidden: handle.IsHidden(), HandleFocused: handle.IsFocused(),
			EditorFocused: editorFocused, OverlayFocused: focused,
		}
	}
	actual := struct {
		Initial                   f12OverlayObservation
		UnfocusedToPrevious       f12OverlayObservation
		UnfocusedToTarget         f12OverlayObservation
		UnfocusedToNull           f12OverlayObservation
		TemporaryHidden           f12OverlayObservation
		TemporaryRestored         f12OverlayObservation
		PermanentlyHidden         f12OverlayObservation
		AfterPermanentShowAttempt f12OverlayObservation
	}{Initial: observe()}
	handle.Unfocus()
	actual.UnfocusedToPrevious = observe()
	handle.Focus()
	handle.Unfocus(extensions.OverlayUnfocusOptions{Target: editor})
	actual.UnfocusedToTarget = observe()
	if err := mode.ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mode.ui.Stop() })
	mode.ui.Terminal().(*f12LifecycleInputTerminal).send("focused-editor-input")
	if _, inputs := editor.state(); !reflect.DeepEqual(inputs, []string{"focused-editor-input"}) {
		t.Fatalf("explicit extension-editor focus routed inputs = %v", inputs)
	}
	handle.Focus()
	handle.Unfocus(extensions.OverlayUnfocusOptions{Target: nil})
	actual.UnfocusedToNull = observe()
	handle.Focus()
	handle.SetHidden(true)
	actual.TemporaryHidden = observe()
	handle.SetHidden(false)
	actual.TemporaryRestored = observe()
	handle.Hide()
	actual.PermanentlyHidden = observe()
	handle.SetHidden(false)
	handle.Focus()
	actual.AfterPermanentShowAttempt = observe()
	done(fixture.CustomOverlay.Handle.Result)
	result := f12UILifecycleReceive(t, outcome, "overlay result")
	_, disposed, _ := component.state()
	want := fixture.CustomOverlay.Handle
	if actual.Initial != want.Initial || actual.UnfocusedToPrevious != want.UnfocusedToPrevious || actual.UnfocusedToTarget != want.UnfocusedToTarget || actual.UnfocusedToNull != want.UnfocusedToNull || actual.TemporaryHidden != want.TemporaryHidden || actual.TemporaryRestored != want.TemporaryRestored || actual.PermanentlyHidden != want.PermanentlyHidden || actual.AfterPermanentShowAttempt != want.AfterPermanentShowAttempt {
		t.Errorf("overlay handle lifecycle differs\nwant: %+v\n got: %+v", want, actual)
	}
	if result.err != nil || !result.ok || result.value != want.Result || disposed != want.DisposeCount {
		t.Errorf("overlay close = result(%v,%t,%v) disposed=%d, want result %q disposed %d", result.value, result.ok, result.err, disposed, want.Result, want.DisposeCount)
	}
}

type f12UILifecycleCustomOutcome struct {
	value    any
	ok       bool
	err      error
	panicked bool
}

func f12UILifecycleRunCustom(ctx context.Context, ui *InteractiveUI, factory extensions.CustomFactory, options *extensions.CustomOptions) <-chan f12UILifecycleCustomOutcome {
	result := make(chan f12UILifecycleCustomOutcome, 1)
	go func() {
		outcome := f12UILifecycleCustomOutcome{}
		defer func() {
			if recover() != nil {
				outcome.panicked = true
			}
			result <- outcome
		}()
		outcome.value, outcome.ok, outcome.err = ui.Custom(ctx, factory, options)
	}()
	return result
}

func f12UILifecycleOverlayHarness() (*InteractiveMode, *InteractiveUI) {
	terminal := &f12LifecycleInputTerminal{fakeTerminalImpl: newFakeTerminal(80, 24)}
	modeUI := tui.NewTUI(terminal)
	bindings := NewAppKeybindings(nil)
	editor := NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	editor.SetText("draft")
	mode := &InteractiveMode{ui: modeUI, keybindings: bindings, editor: editor, editorContainer: &tui.Container{}}
	mode.editorContainer.AddChild(editor)
	modeUI.AddChild(mode.editorContainer)
	modeUI.SetFocus(editor)
	ui := NewInteractiveUI(mode)
	mode.interactiveUI = ui
	return mode, ui
}

func f12UILifecycleReceive[T any](t testing.TB, values <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", label)
		return zero
	}
}

func slicesContains(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func f12UILifecycleOverlayMargin(layout any) (top, right, bottom, left int, ok bool) {
	value := reflect.ValueOf(layout)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, 0, 0, 0, false
		}
		value = value.Elem()
	}
	margin := value.FieldByName("Margin")
	if !margin.IsValid() {
		return 0, 0, 0, 0, false
	}
	if margin.Kind() == reflect.Pointer {
		if margin.IsNil() {
			return 0, 0, 0, 0, false
		}
		margin = margin.Elem()
	}
	fields := []*int{&top, &right, &bottom, &left}
	for index, name := range []string{"Top", "Right", "Bottom", "Left"} {
		field := margin.FieldByName(name)
		if !field.IsValid() || !field.CanInt() {
			return 0, 0, 0, 0, false
		}
		*fields[index] = int(field.Int())
	}
	return top, right, bottom, left, true
}

func f12UILifecycleAssistantMessage() *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content: ai.AssistantContent{
			&ai.ThinkingContent{Thinking: "first secret"},
			&ai.ThinkingContent{Thinking: "second secret"},
			&ai.TextContent{Text: "visible answer"},
		},
		API: "fixture", Provider: "fixture", Model: "fixture", StopReason: ai.StopReasonStop,
	}
}

func f12WidgetSnapshots(fixture f12UILifecycleFixture) []struct {
	Step  string
	Above []string
	Below []string
} {
	result := make([]struct {
		Step  string
		Above []string
		Below []string
	}, len(fixture.Widgets.Snapshots))
	for index, snapshot := range fixture.Widgets.Snapshots {
		result[index] = struct {
			Step  string
			Above []string
			Below []string
		}{snapshot.Step, snapshot.Above, snapshot.Below}
	}
	return result
}

func f12UILifecycleLoadFixture(t testing.TB) f12UILifecycleFixture {
	t.Helper()
	var fixture f12UILifecycleFixture
	f12UILifecycleLoadJSON(t, "lifecycle.json", &fixture)
	if fixture.SchemaVersion != 2 {
		t.Fatalf("F12 UI lifecycle schema version = %d, want 2", fixture.SchemaVersion)
	}
	return fixture
}

func f12UILifecycleLoadJSON(t testing.TB, name string, target any) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 UI lifecycle fixture path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "conformance", "fixtures", "F12-ui-lifecycle", name))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, target); err != nil {
		t.Fatal(err)
	}
}

func f12UILifecycleInitTheme(t testing.TB) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 UI lifecycle theme path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := theme.Parse("dark", encoded, theme.Color256)
	if err != nil {
		t.Fatal(err)
	}
	theme.SetCurrent(parsed)
	t.Cleanup(func() { theme.SetCurrent(nil) })
}

// resolveOverlayLayout and its coercion helpers moved here from production:
// only this conformance surface exercises them.
func resolveOverlayLayout(opts *extensions.CustomOptions, width, height int) tui.OverlayLayout {
	resolved := extensions.OverlayOptions{Anchor: extensions.OverlayCenter}
	if opts != nil {
		if opts.StaticOverlayOptions != nil {
			resolved = *opts.StaticOverlayOptions
		}
		if opts.DynamicOverlayOptions != nil {
			resolved = opts.DynamicOverlayOptions()
		}
	}
	result := tui.OverlayLayout{Width: overlayDimension(resolved.Width, width), MinWidth: resolved.MinWidth, MaxHeight: overlayDimension(resolved.MaxHeight, height), Anchor: string(resolved.Anchor), OffsetX: resolved.OffsetX, OffsetY: resolved.OffsetY, Visible: resolved.Visible, NonCapturing: resolved.NonCapturing}
	if value, ok := overlayCoordinate(resolved.Row); ok {
		result.Row = &value
	}
	if value, ok := overlayCoordinate(resolved.Column); ok {
		result.Column = &value
	}
	result.Margin = overlayMargin(resolved.Margin)
	return result
}

func overlayDimension(value any, total int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if strings.HasSuffix(typed, "%") {
			var percent float64
			if _, err := fmt.Sscanf(strings.TrimSuffix(typed, "%"), "%f", &percent); err == nil {
				return int(float64(total) * percent / 100)
			}
		}
	}
	return 0
}

func overlayCoordinate(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	}
	return 0, false
}
