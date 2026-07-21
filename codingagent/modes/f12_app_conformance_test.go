package modes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/modes/theme"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
)

type f12ApplicationFixture struct {
	SchemaVersion      int                   `json:"schemaVersion"`
	Frames             []f12ApplicationFrame `json:"frames"`
	NotificationFrames []f12ApplicationFrame `json:"notificationFrames"`
	DialogFrames       []f12ApplicationFrame `json:"dialogFrames"`
	DialogResults      []f12DialogResult     `json:"dialogResults"`
	ExternalEditor     f12ExternalEditor     `json:"externalEditor"`
	Autocomplete       struct {
		Enabled               []f12AutocompleteReplay `json:"enabled"`
		SkillCommandsDisabled []f12AutocompleteReplay `json:"skillCommandsDisabled"`
		ProviderTransfer      struct {
			DefaultAssigned     bool `json:"defaultAssigned"`
			ReplacementAssigned bool `json:"replacementAssigned"`
			SameProvider        bool `json:"sameProvider"`
			StoredProvider      bool `json:"storedProvider"`
		} `json:"providerTransfer"`
	} `json:"autocomplete"`
	Lifecycle f12ApplicationLifecycle `json:"lifecycle"`
}

type f12ApplicationLifecycle struct {
	Editor struct {
		FactoryStored           bool     `json:"factoryStored"`
		CustomInstalled         bool     `json:"customInstalled"`
		TextCopied              string   `json:"textCopied"`
		CallbacksCopied         bool     `json:"callbacksCopied"`
		BorderColor             string   `json:"borderColor"`
		PaddingX                int      `json:"paddingX"`
		AutocompleteTransferred bool     `json:"autocompleteTransferred"`
		ExpandedText            string   `json:"expandedText"`
		PasteInput              string   `json:"pasteInput"`
		PasteTargetWasCustom    bool     `json:"pasteTargetWasCustom"`
		ActionEvents            []string `json:"actionEvents"`
		PreservedActionEvents   []string `json:"preservedActionEvents"`
		RestoredText            string   `json:"restoredText"`
		RestoredDefault         bool     `json:"restoredDefault"`
		Focus                   []string `json:"focus"`
		Renders                 int      `json:"renders"`
		FinalChild              string   `json:"finalChild"`
	} `json:"editor"`
	TerminalInput struct {
		PresentEmpty struct {
			Consume bool   `json:"consume"`
			HasData bool   `json:"hasData"`
			Data    string `json:"data"`
		} `json:"presentEmpty"`
		Absent struct {
			Consume bool `json:"consume"`
			HasData bool `json:"hasData"`
		} `json:"absent"`
		ActiveAfterExplicit int   `json:"activeAfterExplicit"`
		ActiveAfterReset    int   `json:"activeAfterReset"`
		TrackedAfterReset   int   `json:"trackedAfterReset"`
		UnsubscribeEvents   []int `json:"unsubscribeEvents"`
	} `json:"terminalInput"`
	Working struct {
		StoredBeforeStreaming struct {
			Message string `json:"message"`
			Options struct {
				Frames []string `json:"frames"`
			} `json:"options"`
		} `json:"storedBeforeStreaming"`
		VisibleAfterHide         bool     `json:"visibleAfterHide"`
		ShownKind                string   `json:"shownKind"`
		ClearKinds               []string `json:"clearKinds"`
		DefaultIntervalMS        int      `json:"defaultIntervalMs"`
		InitialLines             []string `json:"initialLines"`
		NextLines                []string `json:"nextLines"`
		DefaultMessageLines      []string `json:"defaultMessageLines"`
		HiddenIndicatorLines     []string `json:"hiddenIndicatorLines"`
		EmptyMessageLines        []string `json:"emptyMessageLines"`
		RequestRenderCount       int      `json:"requestRenderCount"`
		ActiveIntervalsAfterHide int      `json:"activeIntervalsAfterHide"`
	} `json:"working"`
	CustomUI struct {
		Installed f12CustomUILifecycle `json:"installed"`
		EarlyDone f12CustomUILifecycle `json:"earlyDone"`
	} `json:"customUI"`
	DialogsAndPrimitives struct {
		EmptySelector struct {
			Installed  bool     `json:"installed"`
			Lines      []string `json:"lines"`
			Result     any      `json:"result"`
			FinalChild string   `json:"finalChild"`
			Focus      []string `json:"focus"`
			Renders    int      `json:"renders"`
		} `json:"emptySelector"`
		TimedInput struct {
			InitialLines   []string `json:"initialLines"`
			Result         any      `json:"result"`
			FinalChild     string   `json:"finalChild"`
			Focus          []string `json:"focus"`
			Renders        int      `json:"renders"`
			IntervalActive bool     `json:"intervalActive"`
		} `json:"timedInput"`
		Countdown struct {
			Ticks      []int `json:"ticks"`
			Expires    int   `json:"expires"`
			Renders    int   `json:"renders"`
			IntervalMS int   `json:"intervalMs"`
			Active     bool  `json:"active"`
		} `json:"countdown"`
		DynamicBorderWidthZero []string `json:"dynamicBorderWidthZero"`
	} `json:"dialogsAndPrimitives"`
	HeaderFooter struct {
		Events          []string `json:"events"`
		FooterDisposals []string `json:"footerDisposals"`
		HeaderDisposals []string `json:"headerDisposals"`
		FinalFooter     string   `json:"finalFooter"`
		FinalHeader     string   `json:"finalHeader"`
	} `json:"headerFooter"`
	ThemeObject struct {
		Result         extensions.ThemeSetResult `json:"result"`
		InstanceCalls  []string                  `json:"instanceCalls"`
		NameCalls      []string                  `json:"nameCalls"`
		SettingsWrites []string                  `json:"settingsWrites"`
	} `json:"themeObject"`
	OrdinaryError f12ApplicationFrame `json:"ordinaryError"`
}

type f12CustomUILifecycle struct {
	Result       string   `json:"result"`
	EditorText   string   `json:"editorText"`
	FinalChild   string   `json:"finalChild"`
	Events       []string `json:"events"`
	DisposeCount int      `json:"disposeCount"`
}

type f12AutocompleteReplay struct {
	Input  string                      `json:"input"`
	Result *f12AutocompleteSuggestions `json:"result"`
}

type f12AutocompleteSuggestions struct {
	Prefix string                 `json:"prefix"`
	Items  []tui.AutocompleteItem `json:"items"`
}

type f12ExternalEditor struct {
	KeyData     string   `json:"keyData"`
	HintVisible bool     `json:"hintVisible"`
	InitialText string   `json:"initialText"`
	FinalText   string   `json:"finalText"`
	Lifecycle   []string `json:"lifecycle"`
}

type f12DialogResult struct {
	Width              int    `json:"width"`
	Selected           string `json:"selected"`
	SelectorCancelled  bool   `json:"selectorCancelled"`
	InputValue         string `json:"inputValue"`
	InputCancelled     bool   `json:"inputCancelled"`
	PlaceholderVisible bool   `json:"placeholderVisible"`
}

type f12ApplicationFrame struct {
	ID    string   `json:"id"`
	Width int      `json:"width"`
	Lines []string `json:"lines"`
}

type f12LifecycleReplacementEditor struct {
	text                string
	inputs              []string
	provider            extensions.AutocompleteProvider
	onSubmit            func(string)
	onChange            func(string)
	onEscape            func()
	onCtrlD             func()
	onPasteImage        func()
	onExtensionShortcut func(string) bool
	actionHandlers      map[string]func()
	paddingX            int
	borderColor         tui.StyleFunc
}

type f12HandleOnlyEditor struct{}

func (*f12HandleOnlyEditor) Render(int) []string { return nil }
func (*f12HandleOnlyEditor) HandleInput(string)  {}

func (*f12LifecycleReplacementEditor) Render(int) []string { return []string{"CUSTOM"} }
func (editor *f12LifecycleReplacementEditor) HandleInput(data string) {
	editor.inputs = append(editor.inputs, data)
}
func (editor *f12LifecycleReplacementEditor) GetText() string { return editor.text }
func (editor *f12LifecycleReplacementEditor) GetExpandedText() string {
	return "expanded:" + editor.text
}
func (editor *f12LifecycleReplacementEditor) SetText(text string) { editor.text = text }
func (editor *f12LifecycleReplacementEditor) SetAutocompleteProvider(provider extensions.AutocompleteProvider) {
	editor.provider = provider
}
func (editor *f12LifecycleReplacementEditor) SetOnSubmit(callback func(string)) {
	editor.onSubmit = callback
}
func (editor *f12LifecycleReplacementEditor) SetOnChange(callback func(string)) {
	editor.onChange = callback
}
func (editor *f12LifecycleReplacementEditor) SetOnEscape(callback func()) {
	editor.onEscape = callback
}
func (editor *f12LifecycleReplacementEditor) GetOnEscape() func() { return editor.onEscape }
func (editor *f12LifecycleReplacementEditor) SetOnCtrlD(callback func()) {
	editor.onCtrlD = callback
}
func (editor *f12LifecycleReplacementEditor) GetOnCtrlD() func() { return editor.onCtrlD }
func (editor *f12LifecycleReplacementEditor) SetOnPasteImage(callback func()) {
	editor.onPasteImage = callback
}
func (editor *f12LifecycleReplacementEditor) GetOnPasteImage() func() { return editor.onPasteImage }
func (editor *f12LifecycleReplacementEditor) SetOnExtensionShortcut(callback func(string) bool) {
	editor.onExtensionShortcut = callback
}
func (editor *f12LifecycleReplacementEditor) GetOnExtensionShortcut() func(string) bool {
	return editor.onExtensionShortcut
}
func (editor *f12LifecycleReplacementEditor) SetActionHandlers(handlers map[string]func()) {
	editor.actionHandlers = make(map[string]func(), len(handlers))
	for action, handler := range handlers {
		editor.actionHandlers[action] = handler
	}
}
func (editor *f12LifecycleReplacementEditor) GetActionHandlers() map[string]func() {
	return editor.actionHandlers
}
func (editor *f12LifecycleReplacementEditor) SetPaddingX(padding int) {
	editor.paddingX = padding
}
func (editor *f12LifecycleReplacementEditor) SetBorderColor(color tui.StyleFunc) {
	editor.borderColor = color
}

type f12LifecycleInputTerminal struct {
	*fakeTerminalImpl
	mu      sync.Mutex
	onInput func(string)
}

type f12TitleTerminal struct {
	*fakeTerminalImpl
	mu     sync.Mutex
	titles []string
}

func (terminal *f12TitleTerminal) SetTitle(title string) {
	terminal.mu.Lock()
	terminal.titles = append(terminal.titles, title)
	terminal.mu.Unlock()
}

func (terminal *f12TitleTerminal) lastTitle() string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if len(terminal.titles) == 0 {
		return ""
	}
	return terminal.titles[len(terminal.titles)-1]
}

func (terminal *f12LifecycleInputTerminal) Start(input func(string), _ func()) error {
	terminal.mu.Lock()
	terminal.onInput = input
	terminal.mu.Unlock()
	return nil
}

func (terminal *f12LifecycleInputTerminal) send(data string) {
	terminal.mu.Lock()
	input := terminal.onInput
	terminal.mu.Unlock()
	if input != nil {
		input(data)
	}
}

type f12LifecycleInputCapture struct {
	mu     sync.Mutex
	inputs []string
}

type f12LifecycleDisposable struct {
	label    string
	disposed int
}

func (component *f12LifecycleDisposable) Render(int) []string { return []string{component.label} }
func (component *f12LifecycleDisposable) Dispose()            { component.disposed++ }

type f12LifecycleRenderCounter struct {
	mu      sync.Mutex
	renders int
}

func (counter *f12LifecycleRenderCounter) RequestRender() {
	counter.mu.Lock()
	counter.renders++
	counter.mu.Unlock()
}

func (counter *f12LifecycleRenderCounter) count() int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.renders
}

func (*f12LifecycleInputCapture) Render(int) []string { return []string{"FOCUS"} }
func (capture *f12LifecycleInputCapture) HandleInput(event tui.KeyEvent) {
	capture.mu.Lock()
	capture.inputs = append(capture.inputs, event.Raw)
	capture.mu.Unlock()
}
func (capture *f12LifecycleInputCapture) inputCount() int {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return len(capture.inputs)
}

func TestF12ApplicationEditorLifecycleMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	want := loadF12ApplicationFixture(t).Lifecycle.Editor
	modeUI := tui.NewTUI(newFakeTerminal(48, 24))
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	defaultEditor := NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	defaultEditor.SetText("draft")
	defaultEditor.SetPaddingX(3)
	defaultBorder := func(value string) string { return "DEFAULT:" + value }
	defaultEditor.SetBorderColor(defaultBorder)
	actionEvents := []string{}
	defaultEditor.OnSubmit = func(text string) { actionEvents = append(actionEvents, "submit:"+text) }
	defaultEditor.OnChange = func(text string) { actionEvents = append(actionEvents, "change:"+text) }
	defaultEditor.OnEscape = func() { actionEvents = append(actionEvents, "escape") }
	defaultEditor.OnCtrlD = func() { actionEvents = append(actionEvents, "ctrl-d") }
	defaultEditor.OnPasteImage = func() { actionEvents = append(actionEvents, "paste-image") }
	defaultEditor.OnExtensionShortcut = func(data string) bool {
		actionEvents = append(actionEvents, "shortcut:"+data)
		return true
	}
	defaultEditor.OnAction("app.clear", func() { actionEvents = append(actionEvents, "action:clear") })
	mode := &InteractiveMode{
		ui: modeUI, keybindings: bindings, editor: defaultEditor,
		editorContainer:      &tui.Container{},
		autocompleteProvider: tui.NewCombinedAutocompleteProvider(nil, t.TempDir(), ""),
	}
	mode.editorContainer.AddChild(defaultEditor)
	interactiveUI := NewInteractiveUI(mode)
	mode.interactiveUI = interactiveUI
	custom := &f12LifecycleReplacementEditor{}
	interactiveUI.SetEditorComponent(func(extensions.UIHost, extensions.Theme, extensions.Keybindings) extensions.EditorComponent {
		return custom
	})

	if got := interactiveUI.GetEditorComponent() != nil; got != want.FactoryStored {
		t.Errorf("custom editor factory stored = %t, want %t", got, want.FactoryStored)
	}
	if got := mode.extensionEditor == custom; got != want.CustomInstalled {
		t.Errorf("custom editor installed = %t, want %t", got, want.CustomInstalled)
	}
	if custom.text != want.TextCopied {
		t.Errorf("custom editor copied text = %q, want %q", custom.text, want.TextCopied)
	}
	if got := custom.onSubmit != nil && custom.onChange != nil; got != want.CallbacksCopied {
		t.Errorf("custom editor callbacks copied = %t, want %t", got, want.CallbacksCopied)
	}
	if custom.onSubmit != nil {
		custom.onSubmit("value")
	}
	if custom.onChange != nil {
		custom.onChange("changed")
	}
	if custom.onEscape != nil {
		custom.onEscape()
	}
	if custom.onCtrlD != nil {
		custom.onCtrlD()
	}
	if custom.onPasteImage != nil {
		custom.onPasteImage()
	}
	if custom.onExtensionShortcut != nil {
		custom.onExtensionShortcut("ctrl+x")
	}
	if custom.actionHandlers != nil && custom.actionHandlers["app.clear"] != nil {
		custom.actionHandlers["app.clear"]()
	}
	if !reflect.DeepEqual(actionEvents, want.ActionEvents) {
		t.Errorf("custom editor action events = %#v, want %#v", actionEvents, want.ActionEvents)
	}
	if custom.paddingX != want.PaddingX {
		t.Errorf("custom editor padding = %d, want %d", custom.paddingX, want.PaddingX)
	}
	if custom.borderColor == nil {
		t.Errorf("custom editor border color was not copied; upstream=%q", want.BorderColor)
	}
	if got := custom.provider != nil; got != want.AutocompleteTransferred {
		t.Errorf("custom editor autocomplete transferred = %t, want %t", got, want.AutocompleteTransferred)
	}
	if got := interactiveUI.GetEditorText(); got != want.ExpandedText {
		t.Errorf("active editor expanded text = %q, want %q", got, want.ExpandedText)
	}
	interactiveUI.PasteToEditor("paste\nblock")
	if got := strings.Join(custom.inputs, ""); got != want.PasteInput {
		t.Errorf("active editor paste input = %q, want %q", got, want.PasteInput)
	}
	custom.SetText("custom-change")
	interactiveUI.SetEditorComponent(nil)
	if got := defaultEditor.GetText(); got != want.RestoredText {
		t.Errorf("restored default editor text = %q, want %q", got, want.RestoredText)
	}
	preserved := &f12LifecycleReplacementEditor{
		onEscape:     func() { actionEvents = append(actionEvents, "custom-escape") },
		onCtrlD:      func() { actionEvents = append(actionEvents, "custom-ctrl-d") },
		onPasteImage: func() { actionEvents = append(actionEvents, "custom-paste-image") },
		onExtensionShortcut: func(data string) bool {
			actionEvents = append(actionEvents, "custom-shortcut:"+data)
			return false
		},
		actionHandlers: map[string]func(){
			"app.clear":     func() { actionEvents = append(actionEvents, "custom-action:clear") },
			"custom.action": func() { actionEvents = append(actionEvents, "custom-action:kept") },
		},
	}
	preservedStart := len(actionEvents)
	interactiveUI.SetEditorComponent(func(extensions.UIHost, extensions.Theme, extensions.Keybindings) extensions.EditorComponent {
		return preserved
	})
	preserved.onSubmit("preserved")
	preserved.onChange("preserved-change")
	preserved.onEscape()
	preserved.onCtrlD()
	preserved.onPasteImage()
	preserved.onExtensionShortcut("ctrl+y")
	preserved.actionHandlers["app.clear"]()
	if action := preserved.actionHandlers["custom.action"]; action != nil {
		action()
	}
	if got := actionEvents[preservedStart:]; !reflect.DeepEqual(got, want.PreservedActionEvents) {
		t.Errorf("custom editor preserved actions = %#v, want %#v", got, want.PreservedActionEvents)
	}
	interactiveUI.SetEditorComponent(nil)
}

func TestF12ApplicationEditorContractRequiresTextAccess(t *testing.T) {
	if _, ok := any(&f12HandleOnlyEditor{}).(extensions.EditorComponent); ok {
		t.Fatal("editor component without GetText/SetText satisfied the upstream editor contract")
	}
}

func TestInteractiveAgentLifecycleOwnsWorkingState(t *testing.T) {
	initF12ApplicationTheme(t)
	mode := newF12AutocompleteMode(t, true)
	mode.status = &tui.Container{}
	mode.interactiveUI = NewInteractiveUI(mode)

	mode.handleEvent(agent.AgentStartEvent{})
	working, ok := mode.statusIndicator.(*StatusIndicator)
	if !ok || working.Kind != StatusWorking {
		t.Fatalf("agent_start status = %#v, want working while run-loop flag is false", mode.statusIndicator)
	}
	if !mode.streaming {
		t.Fatal("agent_start did not mark the interactive session streaming")
	}

	mode.handleEvent(codingagent.AgentSettledEvent{})
	if mode.streaming {
		t.Fatal("agent_settled left the interactive session streaming")
	}
	if mode.statusIndicator != nil {
		t.Fatalf("agent_settled status = %#v, want cleared", mode.statusIndicator)
	}
}

func TestInteractiveAgentStartClearsStatusWhenWorkingHidden(t *testing.T) {
	initF12ApplicationTheme(t)
	mode := newF12AutocompleteMode(t, true)
	mode.status = &tui.Container{}
	mode.interactiveUI = NewInteractiveUI(mode)
	mode.interactiveUI.SetWorkingVisible(false)
	mode.setStatus(NewRetryStatusIndicator(mode.ui, 1, 2, 1000))

	mode.handleEvent(agent.AgentStartEvent{})
	if mode.statusIndicator != nil {
		t.Fatalf("hidden agent_start status = %#v, want cleared", mode.statusIndicator)
	}
}

func TestInteractiveSessionTitleSurvivesEventsAndExtensionReset(t *testing.T) {
	initF12ApplicationTheme(t)
	mode := newF12AutocompleteMode(t, true)
	if err := mode.session.SetSessionName("named session"); err != nil {
		t.Fatal(err)
	}
	terminal := &f12TitleTerminal{fakeTerminalImpl: newFakeTerminal(88, 40)}
	mode.ui = tui.NewTUI(terminal)
	mode.editor = NewCustomEditor(mode.ui, theme.EditorTheme(), mode.keybindings)
	mode.header = &tui.Container{}
	mode.status = &tui.Container{}
	mode.widgetAbove = &tui.Container{}
	mode.editorContainer = &tui.Container{}
	mode.widgetBelow = &tui.Container{}
	mode.footer = &tui.Container{}
	mode.editorContainer.AddChild(mode.editor)
	mode.interactiveUI = NewInteractiveUI(mode)
	want := "pigo - named session - " + filepath.Base(mode.cwd)

	mode.handleEvent(codingagent.SessionInfoChangedEvent{})
	if got := terminal.lastTitle(); got != want {
		t.Fatalf("session-info title = %q, want %q", got, want)
	}
	mode.interactiveUI.SetTitle("extension title")
	mode.interactiveUI.resetExtensionUI()
	if got := terminal.lastTitle(); got != want {
		t.Fatalf("reset title = %q, want %q", got, want)
	}
}

func TestWorkingIndicatorMutationCannotResurrectDetachedTicker(t *testing.T) {
	initF12ApplicationTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(80, 24))
	mode := &InteractiveMode{ui: modeUI, status: &tui.Container{}}
	ui := NewInteractiveUI(mode)
	mode.interactiveUI = ui
	options := &extensions.WorkingIndicatorOptions{Frames: []string{"a", "b"}, IntervalMS: 1}
	indicators := make([]*StatusIndicator, 0, 4000)
	for range 4000 {
		indicator := NewWorkingStatusIndicator(modeUI, "working", options)
		indicators = append(indicators, indicator)
		mode.setStatus(indicator)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			ui.SetWorkingIndicator(options)
		}()
		go func() {
			defer wait.Done()
			<-start
			mode.clearStatusIndicatorKind(StatusWorking)
		}()
		close(start)
		wait.Wait()
	}
	snapshot := func() []string {
		lines := make([]string, len(indicators))
		for index, indicator := range indicators {
			lines[index] = strings.Join(indicator.Render(80), "\n")
		}
		return lines
	}
	before := snapshot()
	time.Sleep(10 * time.Millisecond)
	if after := snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatal("working-indicator mutation restarted a detached animation ticker")
	}
}

func TestF12ApplicationTerminalInputLifecycleMatchesUpstream(t *testing.T) {
	want := loadF12ApplicationFixture(t).Lifecycle.TerminalInput
	terminal := &f12LifecycleInputTerminal{fakeTerminalImpl: newFakeTerminal(48, 24)}
	modeUI := tui.NewTUI(terminal)
	if err := modeUI.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = modeUI.Stop() })
	capture := &f12LifecycleInputCapture{}
	modeUI.AddChild(capture)
	modeUI.SetFocus(capture)
	mode := &InteractiveMode{ui: modeUI}
	interactiveUI := NewInteractiveUI(mode)
	mode.interactiveUI = interactiveUI
	emptyTransform := want.PresentEmpty.Data
	unsubscribeEmpty := interactiveUI.OnTerminalInput(func(string) *extensions.TerminalInputResult {
		return &extensions.TerminalInputResult{Consume: want.PresentEmpty.Consume, Data: &emptyTransform}
	})
	terminal.send("source")
	if want.PresentEmpty.HasData && capture.inputCount() != 0 {
		t.Errorf("present empty terminal transform reached focused component %d times", capture.inputCount())
	}
	unsubscribeEmpty()

	listenerCalls := 0
	interactiveUI.OnTerminalInput(func(string) *extensions.TerminalInputResult {
		listenerCalls++
		return nil
	})
	terminal.send("before-reset")
	mode.detachSession()
	terminal.send("after-reset")
	wantCalls := 1 + want.ActiveAfterReset
	if listenerCalls != wantCalls {
		t.Errorf("terminal listener calls across session invalidation = %d, want %d", listenerCalls, wantCalls)
	}
}

func TestF12ApplicationWorkingLifecycleMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	want := loadF12ApplicationFixture(t).Lifecycle.Working
	modeUI := tui.NewTUI(newFakeTerminal(48, 24))
	mode := &InteractiveMode{ui: modeUI, status: &tui.Container{}}
	interactiveUI := NewInteractiveUI(mode)
	message := want.StoredBeforeStreaming.Message
	interactiveUI.SetWorkingMessage(&message)
	interactiveUI.SetWorkingIndicator(&extensions.WorkingIndicatorOptions{
		Frames: want.StoredBeforeStreaming.Options.Frames,
	})
	interactiveUI.SetWorkingVisible(false)
	mode.mu.Lock()
	mode.streaming = true
	mode.mu.Unlock()
	interactiveUI.SetWorkingVisible(true)
	t.Cleanup(func() {
		if indicator, ok := mode.statusIndicator.(*StatusIndicator); ok {
			indicator.Dispose()
		}
	})
	if got := mode.status.Render(48); !reflect.DeepEqual(got, want.InitialLines) {
		wantJSON, _ := json.Marshal(want.InitialLines)
		gotJSON, _ := json.Marshal(got)
		t.Errorf("working state was not retained before streaming\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
	deadline := time.Now().Add(time.Second)
	for !reflect.DeepEqual(mode.status.Render(48), want.NextLines) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := mode.status.Render(48); !reflect.DeepEqual(got, want.NextLines) {
		t.Errorf("working indicator animation = %#v, want %#v", got, want.NextLines)
	}
	interactiveUI.SetWorkingMessage(nil)
	if got := mode.status.Render(48); !reflect.DeepEqual(got, want.DefaultMessageLines) {
		t.Errorf("working default message = %#v, want %#v", got, want.DefaultMessageLines)
	}
	interactiveUI.SetWorkingIndicator(&extensions.WorkingIndicatorOptions{Frames: []string{}})
	if got := mode.status.Render(48); !reflect.DeepEqual(got, want.HiddenIndicatorLines) {
		t.Errorf("working hidden indicator = %#v, want %#v", got, want.HiddenIndicatorLines)
	}
	interactiveUI.SetWorkingVisible(false)
	empty := ""
	interactiveUI.SetWorkingMessage(&empty)
	interactiveUI.SetWorkingVisible(true)
	if got := mode.status.Render(48); !reflect.DeepEqual(got, want.EmptyMessageLines) {
		t.Errorf("working explicit empty message = %#v, want %#v", got, want.EmptyMessageLines)
	}
	interactiveUI.SetWorkingVisible(false)
}

func TestF12ApplicationCustomUILifecycleMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	want := loadF12ApplicationFixture(t).Lifecycle.CustomUI
	modeUI := tui.NewTUI(newFakeTerminal(48, 24))
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	editor := NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	editor.SetText("draft")
	mode := &InteractiveMode{
		ui: modeUI, keybindings: bindings, editor: editor,
		editorContainer: &tui.Container{},
	}
	mode.editorContainer.AddChild(editor)
	interactiveUI := NewInteractiveUI(mode)

	type outcome struct {
		value any
		ok    bool
		err   error
	}
	component := &f12LifecycleDisposable{label: "CUSTOM"}
	doneReady := make(chan extensions.CustomDone, 1)
	result := make(chan outcome, 1)
	go func() {
		value, ok, err := interactiveUI.Custom(t.Context(), func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			doneReady <- done
			return component, nil
		}, nil)
		result <- outcome{value: value, ok: ok, err: err}
	}()
	done := <-doneReady
	deadline := time.Now().Add(time.Second)
	for !reflect.DeepEqual(mode.editorContainer.Render(48), []string{"CUSTOM"}) {
		if time.Now().After(deadline) {
			t.Fatal("custom component was not installed")
		}
		time.Sleep(time.Millisecond)
	}
	editor.SetText("mutated-during-custom")
	done("first")
	done("second")
	installed := <-result
	if installed.err != nil || !installed.ok || installed.value != want.Installed.Result {
		t.Errorf("installed custom result = (%v, %t, %v), want (%q, true, nil)", installed.value, installed.ok, installed.err, want.Installed.Result)
	}
	if got := editor.GetText(); got != want.Installed.EditorText {
		t.Errorf("editor text after custom UI = %q, want %q", got, want.Installed.EditorText)
	}
	if component.disposed != want.Installed.DisposeCount {
		t.Errorf("installed custom dispose count = %d, want %d", component.disposed, want.Installed.DisposeCount)
	}
	if got := mode.editorContainer.EndsWith(editor); got != (want.Installed.FinalChild == "editor") {
		t.Errorf("installed custom restored editor = %t, want %t", got, want.Installed.FinalChild == "editor")
	}

	early := &f12LifecycleDisposable{label: "EARLY"}
	value, ok, err := interactiveUI.Custom(t.Context(), func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		done("first")
		done("second")
		return early, nil
	}, nil)
	if err != nil || !ok || value != want.EarlyDone.Result {
		t.Errorf("early-done custom result = (%v, %t, %v), want (%q, true, nil)", value, ok, err, want.EarlyDone.Result)
	}
	if early.disposed != want.EarlyDone.DisposeCount {
		t.Errorf("early-done dispose count = %d, want %d", early.disposed, want.EarlyDone.DisposeCount)
	}
}

func TestF12ApplicationHeaderFooterDisposalMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	want := loadF12ApplicationFixture(t).Lifecycle.HeaderFooter
	mode := newF12AutocompleteMode(t, true)
	mode.header = &tui.Container{}
	mode.footer = &tui.Container{}
	interactiveUI := NewInteractiveUI(mode)
	footerA := &f12LifecycleDisposable{label: "footer-a"}
	footerB := &f12LifecycleDisposable{label: "footer-b"}
	headerA := &f12LifecycleDisposable{label: "header-a"}
	headerB := &f12LifecycleDisposable{label: "header-b"}
	interactiveUI.SetFooter(func(extensions.UIHost, extensions.Theme, extensions.FooterDataProvider) extensions.Component {
		return footerA
	})
	interactiveUI.SetFooter(func(extensions.UIHost, extensions.Theme, extensions.FooterDataProvider) extensions.Component {
		return footerB
	})
	interactiveUI.SetFooter(nil)
	interactiveUI.SetHeader(func(extensions.UIHost, extensions.Theme) extensions.Component { return headerA })
	interactiveUI.SetHeader(func(extensions.UIHost, extensions.Theme) extensions.Component { return headerB })
	interactiveUI.SetHeader(nil)
	gotFooter := []string{}
	if footerA.disposed > 0 {
		gotFooter = append(gotFooter, "dispose:footer-a")
	}
	if footerB.disposed > 0 {
		gotFooter = append(gotFooter, "dispose:footer-b")
	}
	gotHeader := []string{}
	if headerA.disposed > 0 {
		gotHeader = append(gotHeader, "dispose:header-a")
	}
	if headerB.disposed > 0 {
		gotHeader = append(gotHeader, "dispose:header-b")
	}
	if !reflect.DeepEqual(gotFooter, want.FooterDisposals) {
		t.Errorf("footer disposals = %#v, want %#v", gotFooter, want.FooterDisposals)
	}
	if !reflect.DeepEqual(gotHeader, want.HeaderDisposals) {
		t.Errorf("header disposals = %#v, want %#v", gotHeader, want.HeaderDisposals)
	}
}

func TestF12ApplicationThemeObjectMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	want := loadF12ApplicationFixture(t).Lifecycle.ThemeObject
	mode := newF12AutocompleteMode(t, true)
	if err := mode.initializeTheme(); err != nil {
		t.Fatal(err)
	}
	interactiveUI := NewInteractiveUI(mode)
	light := interactiveUI.GetTheme("light")
	if light == nil {
		t.Fatal("light theme unavailable")
	}
	mode.chat = &tui.Container{}
	streaming := NewAssistantMessageComponent(
		f12UILifecycleAssistantMessage(), true, theme.MarkdownTheme(), "Thinking...", 1,
	)
	mode.currentStreaming = streaming
	mode.chat.AddChild(streaming)
	got := interactiveUI.SetTheme(light)
	if got != want.Result {
		t.Errorf("Theme-object result = %+v, want %+v", got, want.Result)
	}
	if !mode.chat.EndsWith(streaming) || len(streaming.Render(80)) == 0 {
		t.Fatal("theme-object switch detached the live streaming response")
	}
}

func TestF12ApplicationOrdinaryErrorSpacingMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	want := loadF12ApplicationFixture(t).Lifecycle.OrdinaryError
	mode := &InteractiveMode{ui: tui.NewTUI(newFakeTerminal(want.Width, 24)), chat: &tui.Container{}}
	mode.chat.AddChild(tui.NewText("BEFORE", 1, 0, nil))
	mode.showError(errors.New("ORDINARY"))
	got := f12ApplicationFrame{Width: want.Width, Lines: mode.chat.Render(want.Width)}
	if !reflect.DeepEqual(got, want) {
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		t.Errorf("ordinary error frame differs\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
}

func TestF12ApplicationDialogAndPrimitiveLifecycleMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	want := loadF12ApplicationFixture(t).Lifecycle.DialogsAndPrimitives
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	modeUI := tui.NewTUI(newFakeTerminal(32, 24))
	editor := NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode := &InteractiveMode{
		ui: modeUI, keybindings: bindings, editor: editor,
		editorContainer: &tui.Container{}, footerStatuses: make(map[string]string),
	}
	mode.editorContainer.AddChild(editor)
	interactiveUI := NewInteractiveUI(mode)
	ctx, cancel := context.WithCancel(t.Context())
	selectDone := make(chan struct{})
	go func() {
		defer close(selectDone)
		_, _, _ = interactiveUI.Select(ctx, "Empty selector", []string{}, nil)
	}()
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		lines := mode.editorContainer.Render(32)
		if reflect.DeepEqual(lines, want.EmptySelector.Lines) {
			break
		}
		select {
		case <-selectDone:
			if want.EmptySelector.Installed {
				t.Errorf("empty selector returned before installing dialog; lines=%q", lines)
			}
			goto emptyDone
		default:
		}
		if time.Now().After(deadline) {
			t.Errorf("empty selector frame = %q, want %q", lines, want.EmptySelector.Lines)
			break
		}
		time.Sleep(time.Millisecond)
	}
emptyDone:
	cancel()
	<-selectDone

	timeout := int64(1001)
	renderCounter := &f12LifecycleRenderCounter{}
	timedInput := NewExtensionInputComponent("Timed input", "ignored", func(string) {}, func() {}, &extensionDialogOptions{
		ui: renderCounter, timeout: &timeout,
	})
	if got := timedInput.Render(32); !reflect.DeepEqual(got, want.TimedInput.InitialLines) {
		wantJSON, _ := json.Marshal(want.TimedInput.InitialLines)
		gotJSON, _ := json.Marshal(got)
		t.Errorf("timed input initial frame differs\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
	timedInput.Dispose()

	if got := NewDynamicBorderWithColor(func(value string) string { return "<" + value + ">" }).Render(0); !reflect.DeepEqual(got, want.DynamicBorderWidthZero) {
		t.Errorf("DynamicBorder width zero = %#v, want %#v", got, want.DynamicBorderWidthZero)
	}
}

func TestF12ApplicationCountdownTicksMatchUpstream(t *testing.T) {
	want := loadF12ApplicationFixture(t).Lifecycle.DialogsAndPrimitives.Countdown
	renderCounter := &f12LifecycleRenderCounter{}
	var mu sync.Mutex
	ticks := []int{}
	expires := 0
	expired := make(chan struct{}, 1)
	timer := NewCountdownTimer(1001, renderCounter, func(seconds int) {
		mu.Lock()
		ticks = append(ticks, seconds)
		mu.Unlock()
	}, func() {
		mu.Lock()
		expires++
		mu.Unlock()
		expired <- struct{}{}
	})
	t.Cleanup(timer.Dispose)
	select {
	case <-expired:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("countdown did not expire")
	}
	mu.Lock()
	gotTicks := append([]int(nil), ticks...)
	gotExpires := expires
	mu.Unlock()
	if !reflect.DeepEqual(gotTicks, want.Ticks) {
		t.Errorf("countdown ticks = %#v, want %#v", gotTicks, want.Ticks)
	}
	if gotExpires != want.Expires {
		t.Errorf("countdown expires = %d, want %d", gotExpires, want.Expires)
	}
	if got := renderCounter.count(); got != want.Renders {
		t.Errorf("countdown renders = %d, want %d", got, want.Renders)
	}
}

func TestF12ApplicationStatusFramesMatchUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	if fixture.SchemaVersion != 7 || len(fixture.Frames) != 8 || len(fixture.NotificationFrames) != 6 || len(fixture.DialogFrames) != 10 {
		t.Fatalf("F12 application fixture = version %d, frames %d", fixture.SchemaVersion, len(fixture.Frames))
	}

	for _, width := range []int{48, 88} {
		width := width
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			terminal := newFakeTerminal(width, 40)
			ui := tui.NewTUI(terminal)
			chat := &tui.Container{}
			mode := &InteractiveMode{ui: ui, chat: chat}
			capture := func(id string) f12ApplicationFrame {
				return f12ApplicationFrame{ID: id, Width: width, Lines: chat.Render(width)}
			}

			got := make([]f12ApplicationFrame, 0, 4)
			mode.showStatusMessage("STATUS_ONE")
			got = append(got, capture("status-first"))
			mode.showStatusMessage("STATUS_TWO")
			got = append(got, capture("status-replaced"))
			chat.AddChild(tui.NewText(theme.FG("accent", "OTHER"), 1, 0, nil))
			mode.showStatusMessage("STATUS_THREE")
			got = append(got, capture("status-after-content"))
			mode.showStatusMessage("STATUS_FOUR")
			got = append(got, capture("status-after-content-replaced"))

			want := make([]f12ApplicationFrame, 0, 4)
			for _, frame := range fixture.Frames {
				if frame.Width == width {
					want = append(want, frame)
				}
			}
			if !reflect.DeepEqual(got, want) {
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("application status frames differ\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
		})
	}
}

type f12AutocompleteEditor struct {
	provider extensions.AutocompleteProvider
	text     string
}

func (*f12AutocompleteEditor) Render(int) []string        { return nil }
func (*f12AutocompleteEditor) HandleInput(string)         {}
func (editor *f12AutocompleteEditor) GetText() string     { return editor.text }
func (editor *f12AutocompleteEditor) SetText(text string) { editor.text = text }
func (editor *f12AutocompleteEditor) SetAutocompleteProvider(provider extensions.AutocompleteProvider) {
	editor.provider = provider
}

func TestF12ApplicationAutocompleteProviderTransferMatchesUpstream(t *testing.T) {
	fixture := loadF12ApplicationFixture(t).Autocomplete.ProviderTransfer
	if !fixture.DefaultAssigned || !fixture.ReplacementAssigned || !fixture.SameProvider || !fixture.StoredProvider {
		t.Fatalf("upstream autocomplete provider transfer = %+v", fixture)
	}

	mode := newF12AutocompleteMode(t, true)
	replacement := &f12AutocompleteEditor{}
	mode.extensionEditor = replacement
	mode.setupAutocomplete()
	if replacement.provider == nil {
		t.Fatal("active replacement editor did not receive autocomplete provider")
	}
	adapter, ok := replacement.provider.(tuiAutocompleteAdapter)
	if !ok || !reflect.DeepEqual(adapter.provider, mode.autocompleteProvider) {
		t.Fatal("active replacement editor did not receive the stored autocomplete provider")
	}
}

func TestF12ApplicationAutocompleteMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	assertMatches := func(t *testing.T, mode *InteractiveMode, want []f12AutocompleteReplay) {
		t.Helper()
		got := make([]f12AutocompleteReplay, 0, len(want))
		for _, replay := range want {
			result := mode.autocompleteProvider.GetSuggestions(
				t.Context(), []string{replay.Input}, 0, len([]rune(replay.Input)), false,
			)
			var normalized *f12AutocompleteSuggestions
			if result != nil {
				normalized = &f12AutocompleteSuggestions{Prefix: result.Prefix, Items: result.Items}
			}
			got = append(got, f12AutocompleteReplay{Input: replay.Input, Result: normalized})
		}
		if !reflect.DeepEqual(got, want) {
			wantJSON, _ := json.MarshalIndent(want, "", "  ")
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			t.Fatalf("application autocomplete differs\nwant: %s\n got: %s", wantJSON, gotJSON)
		}
	}
	tests := []struct {
		name    string
		enabled bool
		want    []f12AutocompleteReplay
	}{
		{name: "enabled", enabled: true, want: fixture.Autocomplete.Enabled},
		{name: "skill-commands-disabled", want: fixture.Autocomplete.SkillCommandsDisabled},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			mode := newF12AutocompleteMode(t, test.enabled)
			assertMatches(t, mode, test.want)
			if !test.enabled {
				return
			}
			t.Run("post-session-start-refresh", func(t *testing.T) {
				mode.autocompleteProvider = tui.NewCombinedAutocompleteProvider(nil, mode.cwd, "")
				if err := mode.refreshResourcesAfterSessionStart(mode.session); err != nil {
					t.Fatal(err)
				}
				assertMatches(t, mode, test.want)
			})
		})
	}
}

func newF12AutocompleteMode(t *testing.T, enableSkillCommands bool) *InteractiveMode {
	t.Helper()
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetEnableSkillCommands(enableSkillCommands)
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	userSource := extensions.SourceInfo{
		Path: "/fixture/extensions/commands.ts", Source: "local",
		Scope: extensions.SourceScopeUser, Origin: extensions.SourceOriginTopLevel,
	}
	registerCommand := func(path, name, description string) {
		t.Helper()
		if registerErr := registry.Register(path, func(api extensions.API) error {
			api.RegisterCommand(name, extensions.Command{Description: description})
			return nil
		}, extensions.WithSourceInfo(userSource)); registerErr != nil {
			t.Fatal(registerErr)
		}
	}
	registerCommand("extension-command", "extension-command", "Run extension command")
	registerCommand("model-one", "model", "Conflicts with a built-in")
	registerCommand("model-two", "model", "Conflicts with a built-in")

	prompts := []codingagent.PromptTemplate{{
		Name: "review-prompt", Description: "Review a path", ArgumentHint: "<path>",
		FilePath:   "/fixture/prompts/review-prompt.md",
		SourceInfo: codingagent.SourceInfo{Path: "/fixture/prompts/review-prompt.md", Source: "local", Scope: "project", Origin: "top-level"},
	}}
	skills := []codingagent.Skill{{
		Name: "inspect-skill", Description: "Inspect the workspace",
		FilePath: "/fixture/skills/inspect-skill/SKILL.md", BaseDir: "/fixture/skills/inspect-skill",
		SourceInfo: codingagent.SourceInfo{Path: "/fixture/skills/inspect-skill/SKILL.md", Source: "cli", Scope: "temporary", Origin: "top-level"},
	}}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings,
		NoSkills: true, NoPromptTemplates: true, NoThemes: true, NoContextFiles: true,
		SkillsOverride: func(codingagent.ResourceSkillsResult) codingagent.ResourceSkillsResult {
			return codingagent.ResourceSkillsResult{Skills: skills, Diagnostics: []codingagent.ResourceDiagnostic{}}
		},
		PromptsOverride: func(codingagent.ResourcePromptsResult) codingagent.ResourcePromptsResult {
			return codingagent.ResourcePromptsResult{Prompts: prompts, Diagnostics: []codingagent.ResourceDiagnostic{}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = loader.Reload(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ResourceLoader: loader,
		SlashResolver: &codingagent.SlashResolver{PromptTemplates: prompts, Skills: skills},
		AvailableModels: func() []ai.Model {
			return []ai.Model{
				{ID: "claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5"},
				{ID: "gpt-5.1", Provider: "openai", Name: "GPT 5.1"},
				{ID: "openai/gpt-5", Provider: "openrouter", Name: "GPT 5 via OpenRouter"},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	modeUI := tui.NewTUI(newFakeTerminal(88, 40))
	bindings := NewAppKeybindings(nil)
	host := &f12AutocompleteHost{authOptions: InteractiveAuthOptions{Login: []InteractiveAuthProvider{
		{ID: "openai", Name: "OpenAI", AuthType: aiauth.AuthTypeOAuth},
		{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth},
		{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeAPIKey},
		{ID: "google", Name: "Google", AuthType: aiauth.AuthTypeAPIKey},
	}}}
	mode := &InteractiveMode{session: runtime, cwd: cwd, ui: modeUI, keybindings: bindings, options: InteractiveModeOptions{Host: host}}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.setupAutocomplete()
	return mode
}

type f12AutocompleteHost struct {
	InteractiveSessionHost
	authOptions InteractiveAuthOptions
}

func (host *f12AutocompleteHost) AuthOptions(context.Context) (InteractiveAuthOptions, error) {
	return host.authOptions, nil
}

func TestF12ExtensionEditorExternalLifecycleMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t).ExternalEditor
	lifecyclePath := filepath.Join(t.TempDir(), "lifecycle.txt")
	t.Setenv("PIGO_F12_EXTERNAL_EDITOR_LOG", lifecyclePath)
	externalEditorCommand := strings.Join([]string{os.Args[0], "-test.run=TestF12ExternalEditorHelper", "--"}, " ")
	t.Setenv("VISUAL", "pigo-f12-visual-must-not-run")
	t.Setenv("EDITOR", "")

	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	terminal := &f12ExternalEditorTerminal{fakeTerminalImpl: newFakeTerminal(88, 40), lifecyclePath: lifecyclePath}
	modeUI := tui.NewTUI(terminal)
	editor := NewExtensionEditorComponent(modeUI, bindings, "Edit value", fixture.InitialText, func(string) {}, func() {}, externalEditorCommand)
	terminal.component = editor
	modeUI.AddChild(editor)
	if err := modeUI.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = modeUI.Stop() })
	terminal.record = true
	baselineRedraws := modeUI.FullRedraws()

	if got := editor.editor.GetText(); got != fixture.InitialText {
		t.Fatalf("initial editor text = %q, want %q", got, fixture.InitialText)
	}
	if got := strings.Contains(strings.Join(editor.Render(88), "\n"), "external editor"); got != fixture.HintVisible {
		t.Fatalf("external editor hint visible = %t, want %t", got, fixture.HintVisible)
	}
	editor.HandleInput(tui.KeyEvent{Raw: fixture.KeyData})

	deadline := time.Now().Add(5 * time.Second)
	for {
		lifecycle, _ := os.ReadFile(lifecyclePath)
		if strings.Contains(string(lifecycle), "start:") && modeUI.FullRedraws() > baselineRedraws {
			if got := editor.editor.GetText(); got != fixture.FinalText {
				t.Fatalf("external editor text = %q, want %q", got, fixture.FinalText)
			}
			got := append(strings.Split(strings.TrimSpace(string(lifecycle)), "\n"), "render:true")
			if !reflect.DeepEqual(got, fixture.Lifecycle) {
				t.Fatalf("external editor lifecycle = %#v, want %#v", got, fixture.Lifecycle)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("external editor lifecycle did not complete: lifecycle=%q redraws=%d", lifecycle, modeUI.FullRedraws()-baselineRedraws)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestF12ExternalEditorHelper(t *testing.T) {
	logPath := os.Getenv("PIGO_F12_EXTERNAL_EDITOR_LOG")
	if logPath == "" {
		return
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString("edit\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if len(os.Args) == 0 {
		t.Fatal("missing external editor path")
	}
	if err = os.WriteFile(os.Args[len(os.Args)-1], []byte("edited externally\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

type f12ExternalEditorTerminal struct {
	*fakeTerminalImpl
	component     *ExtensionEditorComponent
	lifecyclePath string
	record        bool
}

func (terminal *f12ExternalEditorTerminal) Start(func(string), func()) error {
	if terminal.record {
		terminal.appendLifecycle("start:" + terminal.component.editor.GetText())
	}
	return nil
}

func (terminal *f12ExternalEditorTerminal) Stop() error {
	if terminal.record {
		terminal.appendLifecycle("stop")
	}
	return nil
}

func (terminal *f12ExternalEditorTerminal) appendLifecycle(event string) {
	file, err := os.OpenFile(terminal.lifecyclePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(event + "\n")
	_ = file.Close()
}

func TestF12ExtensionDialogFramesMatchUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)

	for _, expectedResult := range fixture.DialogResults {
		expectedResult := expectedResult
		t.Run(strconv.Itoa(expectedResult.Width), func(t *testing.T) {
			width := expectedResult.Width
			var selected string
			selectorCancelled := false
			selector := newExtensionSelectorComponent(
				"Pick one",
				[]string{"alpha", "beta", "gamma"},
				func(value string) { selected = value },
				func() { selectorCancelled = true },
				nil,
			)
			got := []f12ApplicationFrame{{ID: "selector-initial", Width: width, Lines: selector.Render(width)}}
			selector.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
			got = append(got, f12ApplicationFrame{ID: "selector-down", Width: width, Lines: selector.Render(width)})
			selector.HandleInput(tui.KeyEvent{Raw: "\r"})

			var inputValue string
			inputCancelled := false
			input := NewExtensionInputComponent(
				"Enter value",
				"PLACEHOLDER_IS_IGNORED",
				func(value string) { inputValue = value },
				func() { inputCancelled = true },
				nil,
			)
			got = append(got, f12ApplicationFrame{ID: "input-initial", Width: width, Lines: input.Render(width)})
			input.HandleInput(tui.KeyEvent{Raw: "abc"})
			got = append(got, f12ApplicationFrame{ID: "input-typed", Width: width, Lines: input.Render(width)})
			input.HandleInput(tui.KeyEvent{Raw: "\r"})

			terminal := newFakeTerminal(width, 40)
			modeUI := tui.NewTUI(terminal)
			editor := NewExtensionEditorComponent(modeUI, bindings, "Edit value", "alpha\nbeta", func(string) {}, func() {}, "false")
			got = append(got, f12ApplicationFrame{ID: "editor-prefill", Width: width, Lines: editor.Render(width)})

			var want []f12ApplicationFrame
			for _, frame := range fixture.DialogFrames {
				if frame.Width == width {
					want = append(want, frame)
				}
			}
			if !reflect.DeepEqual(got, want) {
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("extension dialog frames differ\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
			actualResult := f12DialogResult{
				Width: width, Selected: selected, SelectorCancelled: selectorCancelled,
				InputValue: inputValue, InputCancelled: inputCancelled, PlaceholderVisible: false,
			}
			if actualResult != expectedResult {
				t.Fatalf("extension dialog result = %+v, want %+v", actualResult, expectedResult)
			}
		})
	}
}

func TestF12InteractiveUISelectUsesEditorSlot(t *testing.T) {
	initF12ApplicationTheme(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	modeUI := tui.NewTUI(newFakeTerminal(48, 40))
	mode := &InteractiveMode{
		ui:              modeUI,
		keybindings:     bindings,
		editorContainer: &tui.Container{},
		widgetAbove:     &tui.Container{},
		footerStatuses:  make(map[string]string),
	}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	interactiveUI := NewInteractiveUI(mode)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = interactiveUI.Select(ctx, "Pick one", []string{"alpha", "beta", "gamma"}, nil)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		lines := mode.editorContainer.Render(48)
		if len(lines) > 0 && strings.Contains(lines[0], "─") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("extension selector was not installed in the editor slot; editor=%#v widget=%#v", lines, mode.widgetAbove.Render(48))
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestInteractiveUISelectItemsPreservesDisplayLabels(t *testing.T) {
	initF12ApplicationTheme(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	modeUI := tui.NewTUI(newFakeTerminal(48, 40))
	mode := &InteractiveMode{
		ui:              modeUI,
		keybindings:     bindings,
		editorContainer: &tui.Container{},
		widgetAbove:     &tui.Container{},
		footerStatuses:  make(map[string]string),
	}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	interactiveUI := NewInteractiveUI(mode)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = interactiveUI.selectItems(ctx, "Choose provider", []tui.SelectItem{{
			Value: "0", Label: "Provider One ✓ configured",
		}}, nil)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		lines := strings.Join(mode.editorContainer.Render(48), "\n")
		if strings.Contains(lines, "✓ configured") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("selector rendered item identity instead of display label: %q", lines)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestF12ApplicationNotificationsMatchUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	for _, width := range []int{48, 88} {
		width := width
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			mode := &InteractiveMode{
				ui:             tui.NewTUI(newFakeTerminal(width, 40)),
				chat:           &tui.Container{},
				footerStatuses: make(map[string]string),
			}
			interactiveUI := NewInteractiveUI(mode)
			got := make([]f12ApplicationFrame, 0, 3)
			capture := func(id string) {
				got = append(got, f12ApplicationFrame{ID: id, Width: width, Lines: mode.chat.Render(width)})
			}
			interactiveUI.Notify("NOTICE", extensions.NotifyInfo)
			capture("notify-info")
			interactiveUI.Notify("CAUTION", extensions.NotifyWarning)
			capture("notify-warning")
			interactiveUI.Notify("BROKEN", extensions.NotifyError)
			capture("notify-error")

			want := make([]f12ApplicationFrame, 0, 3)
			for _, frame := range fixture.NotificationFrames {
				if frame.Width == width {
					want = append(want, frame)
				}
			}
			if !reflect.DeepEqual(got, want) {
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("application notification frames differ\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
		})
	}
}

func initF12ApplicationTheme(t testing.TB) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 application theme path")
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

func loadF12ApplicationFixture(t testing.TB) f12ApplicationFixture {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 application fixture path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "conformance", "fixtures", "F12-app", "status-frames.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture f12ApplicationFixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newExtensionSelectorComponent(
	title string,
	options []string,
	onSelect func(string),
	onCancel func(),
	config *extensionDialogOptions,
) *ExtensionSelectorComponent {
	items := make([]tui.SelectItem, len(options))
	for index, option := range options {
		items[index] = tui.SelectItem{Value: option, Label: option}
	}
	return NewExtensionSelectorItemsComponent(title, items, onSelect, onCancel, config)
}
