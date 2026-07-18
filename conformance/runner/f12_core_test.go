package runner_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/conformance/runner"
	"github.com/OrdalieTech/pi-go/tui"
)

type f12CoreTerminal struct {
	mu            sync.Mutex
	columns, rows int
	writes        []string
	cursorEvents  []string
	input         func(string)
}

func (terminal *f12CoreTerminal) Start(input func(string), _ func()) error {
	terminal.mu.Lock()
	terminal.input = input
	terminal.mu.Unlock()
	return nil
}
func (terminal *f12CoreTerminal) Stop() error {
	terminal.mu.Lock()
	terminal.input = nil
	terminal.mu.Unlock()
	return nil
}
func (terminal *f12CoreTerminal) DrainInput(time.Duration, time.Duration) {}
func (terminal *f12CoreTerminal) Write(data string) {
	terminal.mu.Lock()
	terminal.writes = append(terminal.writes, data)
	terminal.mu.Unlock()
}
func (terminal *f12CoreTerminal) Columns() int              { return terminal.columns }
func (terminal *f12CoreTerminal) Rows() int                 { return terminal.rows }
func (terminal *f12CoreTerminal) KittyProtocolActive() bool { return false }
func (terminal *f12CoreTerminal) MoveBy(int)                {}
func (terminal *f12CoreTerminal) HideCursor() {
	terminal.mu.Lock()
	terminal.cursorEvents = append(terminal.cursorEvents, "hide")
	terminal.mu.Unlock()
}
func (terminal *f12CoreTerminal) ShowCursor() {
	terminal.mu.Lock()
	terminal.cursorEvents = append(terminal.cursorEvents, "show")
	terminal.mu.Unlock()
}
func (terminal *f12CoreTerminal) ClearLine()       {}
func (terminal *f12CoreTerminal) ClearFromCursor() {}
func (terminal *f12CoreTerminal) ClearScreen()     {}
func (terminal *f12CoreTerminal) SetTitle(string)  {}
func (terminal *f12CoreTerminal) SetProgress(bool) {}
func (terminal *f12CoreTerminal) resetWrites() {
	terminal.mu.Lock()
	terminal.writes = nil
	terminal.mu.Unlock()
}
func (terminal *f12CoreTerminal) output() string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return strings.Join(terminal.writes, "")
}
func (terminal *f12CoreTerminal) writeList() []string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return append([]string(nil), terminal.writes...)
}
func (terminal *f12CoreTerminal) cursorEventList() []string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return append([]string(nil), terminal.cursorEvents...)
}
func (terminal *f12CoreTerminal) resetCursorEvents() {
	terminal.mu.Lock()
	terminal.cursorEvents = nil
	terminal.mu.Unlock()
}
func (terminal *f12CoreTerminal) send(data string) {
	terminal.mu.Lock()
	input := terminal.input
	terminal.mu.Unlock()
	input(data)
}

type f12CoreComponent struct {
	lines   []string
	focused bool
	inputs  []string
	onInput func(string)
	widths  []int
}

func (component *f12CoreComponent) Render(width int) []string {
	component.widths = append(component.widths, width)
	return component.lines
}
func (component *f12CoreComponent) HandleInput(event tui.KeyEvent) {
	component.inputs = append(component.inputs, event.Raw)
	if component.onInput != nil {
		component.onInput(event.Raw)
	}
}
func (component *f12CoreComponent) SetFocused(focused bool) { component.focused = focused }

type f12CorePlainComponent struct{ lines []string }

func (component *f12CorePlainComponent) Render(int) []string { return component.lines }

type f12OverlayFixture struct {
	SchemaVersion    int                    `json:"schemaVersion"`
	RenderCases      []f12OverlayRenderCase `json:"renderCases"`
	FocusCases       []f12OverlayFocusCase  `json:"focusCases"`
	CursorTrace      []string               `json:"cursorTrace"`
	UpstreamCoverage struct {
		FocusCaseNames     []string          `json:"focusCaseNames"`
		OverlayOptionCases map[string]string `json:"overlayOptionCases"`
	} `json:"upstreamCoverage"`
}

type f12OverlayRenderCase struct {
	Name                    string             `json:"name"`
	Width                   int                `json:"width"`
	Rows                    int                `json:"rows"`
	Base                    []string           `json:"base"`
	Overlays                []f12Overlay       `json:"overlays"`
	Actions                 []f12OverlayAction `json:"actions"`
	Expected                string             `json:"expected"`
	ExpectedRequestedWidths []*int             `json:"expectedRequestedWidths"`
}

type f12Overlay struct {
	Lines   []string                  `json:"lines"`
	Options *f12OverlayFixtureOptions `json:"options"`
}

type f12OverlayFixtureOptions struct {
	Width           json.RawMessage   `json:"width"`
	MinWidth        int               `json:"minWidth"`
	MaxHeight       json.RawMessage   `json:"maxHeight"`
	Anchor          tui.OverlayAnchor `json:"anchor"`
	OffsetX         int               `json:"offsetX"`
	OffsetY         int               `json:"offsetY"`
	Row             json.RawMessage   `json:"row"`
	Col             json.RawMessage   `json:"col"`
	Margin          json.RawMessage   `json:"margin"`
	NonCapturing    bool              `json:"nonCapturing"`
	VisibleMinWidth *int              `json:"visibleMinWidth"`
	VisibleFlag     string            `json:"visibleFlag"`
}

type f12OverlayAction struct {
	Action  string `json:"action"`
	Overlay int    `json:"overlay"`
	Hidden  bool   `json:"hidden"`
}

type f12OverlayFocusCase struct {
	Name         string                       `json:"name"`
	Components   []string                     `json:"components"`
	NonFocusable []string                     `json:"nonFocusable"`
	Mounted      []string                     `json:"mounted"`
	InitialFocus json.RawMessage              `json:"initialFocus"`
	Flags        map[string]bool              `json:"flags"`
	Handlers     []f12OverlayFocusHandler     `json:"handlers"`
	Operations   []f12OverlayFocusOperation   `json:"operations"`
	Width        int                          `json:"width"`
	Rows         int                          `json:"rows"`
	Expected     []f12OverlayFocusObservation `json:"expected"`
}

type f12OverlayFocusHandler struct {
	Component  string                     `json:"component"`
	Data       string                     `json:"data"`
	Operations []f12OverlayFocusOperation `json:"operations"`
}

type f12OverlayFocusOperation struct {
	Op         string                     `json:"op"`
	Component  string                     `json:"component"`
	Handle     string                     `json:"handle"`
	Options    *f12OverlayFixtureOptions  `json:"options"`
	Target     json.RawMessage            `json:"target"`
	Hidden     bool                       `json:"hidden"`
	Flag       string                     `json:"flag"`
	Value      bool                       `json:"value"`
	Components []string                   `json:"components"`
	Data       string                     `json:"data"`
	Operations []f12OverlayFocusOperation `json:"operations"`
	Label      string                     `json:"label"`
	Probe      []string                   `json:"probe"`
}

type f12OverlayFocusObservation struct {
	Label      string                                `json:"label"`
	Focused    []string                              `json:"focused"`
	Inputs     map[string][]string                   `json:"inputs"`
	Handles    map[string]f12OverlayFocusHandleState `json:"handles"`
	HasOverlay bool                                  `json:"hasOverlay"`
	Front      *string                               `json:"front,omitempty"`
}

type f12OverlayFocusHandleState struct {
	Hidden  bool `json:"hidden"`
	Focused bool `json:"focused"`
}

func f12SizeValue(t *testing.T, raw json.RawMessage) tui.SizeValue {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return tui.SizeValue{}
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		percentage, err := strconv.ParseFloat(strings.TrimSuffix(value, "%"), 64)
		if err != nil || !strings.HasSuffix(value, "%") {
			t.Fatalf("invalid percentage size %q", value)
		}
		return tui.PercentSize(percentage)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return tui.AbsoluteSize(value)
}

func f12OverlayOptions(t *testing.T, fixture *f12OverlayFixtureOptions) tui.OverlayOptions {
	t.Helper()
	options := tui.OverlayOptions{
		Width:        f12SizeValue(t, fixture.Width),
		MinWidth:     fixture.MinWidth,
		MaxHeight:    f12SizeValue(t, fixture.MaxHeight),
		Anchor:       fixture.Anchor,
		OffsetX:      fixture.OffsetX,
		OffsetY:      fixture.OffsetY,
		Row:          f12SizeValue(t, fixture.Row),
		Col:          f12SizeValue(t, fixture.Col),
		NonCapturing: fixture.NonCapturing,
	}
	if len(fixture.Margin) > 0 && string(fixture.Margin) != "null" {
		if fixture.Margin[0] == '{' {
			var margin struct {
				Top, Right, Bottom, Left int
			}
			if err := json.Unmarshal(fixture.Margin, &margin); err != nil {
				t.Fatal(err)
			}
			options.Margin = &tui.OverlayMargin{Top: margin.Top, Right: margin.Right, Bottom: margin.Bottom, Left: margin.Left}
		} else {
			var margin int
			if err := json.Unmarshal(fixture.Margin, &margin); err != nil {
				t.Fatal(err)
			}
			options.Margin = tui.UniformOverlayMargin(margin)
		}
	}
	if fixture.VisibleMinWidth != nil {
		minimum := *fixture.VisibleMinWidth
		options.Visible = func(width, _ int) bool { return width >= minimum }
	}
	return options
}

func TestF12OverlayFramesMatchUpstream(t *testing.T) {
	var fixture f12OverlayFixture
	runner.LoadJSON(t, "F12", "overlays.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.RenderCases) != 45 || len(fixture.FocusCases) != 44 {
		t.Fatalf("overlay fixture header = version %d render %d focus %d", fixture.SchemaVersion, len(fixture.RenderCases), len(fixture.FocusCases))
	}
	if len(fixture.UpstreamCoverage.FocusCaseNames) != 44 || len(fixture.UpstreamCoverage.OverlayOptionCases) != 24 {
		t.Fatalf("upstream coverage = focus %d options %d", len(fixture.UpstreamCoverage.FocusCaseNames), len(fixture.UpstreamCoverage.OverlayOptionCases))
	}
	renderNames := make(map[string]bool, len(fixture.RenderCases))
	for _, fixtureCase := range fixture.RenderCases {
		renderNames[fixtureCase.Name] = true
	}
	for index, name := range fixture.UpstreamCoverage.FocusCaseNames {
		if fixture.FocusCases[index].Name != name {
			t.Fatalf("focus case %d = %q, upstream %q", index, fixture.FocusCases[index].Name, name)
		}
	}
	for upstreamName, renderName := range fixture.UpstreamCoverage.OverlayOptionCases {
		if !renderNames[renderName] {
			t.Fatalf("upstream overlay option %q maps to missing render %q", upstreamName, renderName)
		}
	}
	tui.SetCapabilities(tui.TerminalCapabilities{})
	t.Cleanup(tui.ResetCapabilitiesCache)
	for _, fixtureCase := range fixture.RenderCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			terminal := &f12CoreTerminal{columns: fixtureCase.Width, rows: fixtureCase.Rows}
			ui := tui.NewTUI(terminal)
			ui.AddChild(&f12CoreComponent{lines: fixtureCase.Base})
			handles := make([]tui.OverlayHandle, 0, len(fixtureCase.Overlays))
			components := make([]*f12CoreComponent, 0, len(fixtureCase.Overlays))
			for _, overlay := range fixtureCase.Overlays {
				component := &f12CoreComponent{lines: overlay.Lines}
				components = append(components, component)
				if overlay.Options == nil {
					handles = append(handles, ui.ShowOverlay(component))
				} else {
					handles = append(handles, ui.ShowOverlay(component, f12OverlayOptions(t, overlay.Options)))
				}
			}
			for _, action := range fixtureCase.Actions {
				switch action.Action {
				case "focus":
					handles[action.Overlay].Focus()
				case "hide":
					handles[action.Overlay].Hide()
				case "setHidden":
					handles[action.Overlay].SetHidden(action.Hidden)
				case "hideOverlay":
					ui.HideOverlay()
				default:
					t.Fatalf("unknown overlay action %q", action.Action)
				}
			}
			if err := ui.Start(); err != nil {
				t.Fatal(err)
			}
			terminal.resetWrites()
			ui.ForceRender()
			got := terminal.output()
			gotWidths := make([]*int, len(components))
			for index, component := range components {
				if len(component.widths) > 0 {
					width := component.widths[len(component.widths)-1]
					gotWidths[index] = &width
				}
			}
			if err := ui.Stop(); err != nil {
				t.Fatal(err)
			}
			if got != fixtureCase.Expected {
				t.Fatalf("overlay frame differs\n got: %q\nwant: %q", got, fixtureCase.Expected)
			}
			if mustJSON(gotWidths) != mustJSON(fixtureCase.ExpectedRequestedWidths) {
				t.Fatalf("requested widths = %s, want %s", mustJSON(gotWidths), mustJSON(fixtureCase.ExpectedRequestedWidths))
			}
		})
	}
}

func TestF12OverlayCursorTraceMatchesUpstream(t *testing.T) {
	var fixture f12OverlayFixture
	runner.LoadJSON(t, "F12", "overlays.json", &fixture)
	terminal := &f12CoreTerminal{columns: 20, rows: 6}
	ui := tui.NewTUI(terminal)
	ui.AddChild(&f12CorePlainComponent{})
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	ui.ForceRender()
	terminal.resetCursorEvents()
	first := ui.ShowOverlay(&f12CorePlainComponent{lines: []string{"A"}}, tui.OverlayOptions{NonCapturing: true})
	first.Hide()
	ui.ShowOverlay(&f12CorePlainComponent{lines: []string{"A"}}, tui.OverlayOptions{NonCapturing: true})
	ui.ShowOverlay(&f12CorePlainComponent{lines: []string{"B"}}, tui.OverlayOptions{NonCapturing: true})
	ui.HideOverlay()
	ui.HideOverlay()
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := terminal.cursorEventList(); mustJSON(got) != mustJSON(fixture.CursorTrace) {
		t.Fatalf("cursor trace = %s, want %s", mustJSON(got), mustJSON(fixture.CursorTrace))
	}
}

func f12TraceLine(name string) string {
	if len(name) == 1 {
		return strings.ToUpper(name)
	}
	return "@@" + name + "@@"
}

func f12FocusTarget(t *testing.T, raw json.RawMessage, components map[string]tui.Component) tui.Component {
	t.Helper()
	if string(raw) == "null" {
		return nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		t.Fatalf("invalid focus target %s: %v", raw, err)
	}
	component, ok := components[name]
	if !ok {
		t.Fatalf("unknown focus target %q", name)
	}
	return component
}

func TestF12OverlayFocusTracesMatchUpstream(t *testing.T) {
	var fixture f12OverlayFixture
	runner.LoadJSON(t, "F12", "overlays.json", &fixture)
	if len(fixture.FocusCases) != 44 {
		t.Fatalf("focus trace count = %d, want 44", len(fixture.FocusCases))
	}
	tui.SetCapabilities(tui.TerminalCapabilities{})
	t.Cleanup(tui.ResetCapabilitiesCache)
	for _, fixtureCase := range fixture.FocusCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			width, rows := fixtureCase.Width, fixtureCase.Rows
			if width == 0 {
				width = 80
			}
			if rows == 0 {
				rows = 24
			}
			terminal := &f12CoreTerminal{columns: width, rows: rows}
			ui := tui.NewTUI(terminal)
			plain := make(map[string]bool, len(fixtureCase.NonFocusable))
			for _, name := range fixtureCase.NonFocusable {
				plain[name] = true
			}
			components := make(map[string]tui.Component, len(fixtureCase.Components))
			focusable := make(map[string]*f12CoreComponent, len(fixtureCase.Components))
			for _, name := range fixtureCase.Components {
				lines := []string{f12TraceLine(name)}
				if plain[name] {
					components[name] = &f12CorePlainComponent{lines: lines}
					continue
				}
				component := &f12CoreComponent{lines: lines}
				components[name], focusable[name] = component, component
			}
			root := &tui.Container{}
			ui.AddChild(root)
			mount := func(names []string) {
				root.Clear()
				for _, name := range names {
					component, ok := components[name]
					if !ok {
						t.Fatalf("unknown mounted component %q", name)
					}
					root.AddChild(component)
				}
			}
			mount(fixtureCase.Mounted)
			if len(fixtureCase.InitialFocus) == 0 || string(fixtureCase.InitialFocus) == "null" {
				ui.SetFocus(nil)
			} else {
				ui.SetFocus(f12FocusTarget(t, fixtureCase.InitialFocus, components))
			}
			flags := make(map[string]bool, len(fixtureCase.Flags))
			var flagsMu sync.RWMutex
			for name, value := range fixtureCase.Flags {
				flags[name] = value
			}
			handles := make(map[string]tui.OverlayHandle)
			pending := make([][]f12OverlayFocusOperation, 0)
			var applyOperation func(f12OverlayFocusOperation)
			applyOperation = func(operation f12OverlayFocusOperation) {
				switch operation.Op {
				case "show":
					component, ok := components[operation.Component]
					if !ok {
						t.Fatalf("unknown overlay component %q", operation.Component)
					}
					if operation.Options == nil {
						handles[operation.Handle] = ui.ShowOverlay(component)
						return
					}
					options := f12OverlayOptions(t, operation.Options)
					if operation.Options.VisibleFlag != "" {
						flag := operation.Options.VisibleFlag
						options.Visible = func(int, int) bool {
							flagsMu.RLock()
							defer flagsMu.RUnlock()
							return flags[flag]
						}
					}
					handles[operation.Handle] = ui.ShowOverlay(component, options)
				case "focus":
					handles[operation.Handle].Focus()
				case "hide":
					handles[operation.Handle].Hide()
				case "setHidden":
					handles[operation.Handle].SetHidden(operation.Hidden)
				case "unfocus":
					if len(operation.Target) == 0 {
						handles[operation.Handle].Unfocus()
					} else {
						handles[operation.Handle].Unfocus(tui.OverlayUnfocusOptions{Target: f12FocusTarget(t, operation.Target, components)})
					}
				case "hideOverlay":
					ui.HideOverlay()
				case "setFocus":
					ui.SetFocus(f12FocusTarget(t, operation.Target, components))
				case "setFlag":
					flagsMu.Lock()
					flags[operation.Flag] = operation.Value
					flagsMu.Unlock()
				case "mount":
					mount(operation.Components)
				case "input":
					terminal.send(operation.Data)
				case "schedule":
					pending = append(pending, operation.Operations)
				case "flush":
					scheduled := pending
					pending = nil
					for _, operations := range scheduled {
						for _, nested := range operations {
							applyOperation(nested)
						}
					}
				case "observe":
					t.Fatal("observe must be handled by the trace loop")
				default:
					t.Fatalf("unknown focus operation %q", operation.Op)
				}
			}
			for _, entry := range fixtureCase.Handlers {
				component := focusable[entry.Component]
				if component == nil {
					t.Fatalf("handler component %q is not focusable", entry.Component)
				}
				entry := entry
				component.onInput = func(data string) {
					if data == entry.Data {
						for _, operation := range entry.Operations {
							applyOperation(operation)
						}
					}
				}
			}
			if err := ui.Start(); err != nil {
				t.Fatal(err)
			}
			terminal.resetWrites()
			got := make([]f12OverlayFocusObservation, 0, len(fixtureCase.Expected))
			for _, operation := range fixtureCase.Operations {
				if operation.Op != "observe" {
					applyOperation(operation)
					continue
				}
				var front *string
				if len(operation.Probe) > 0 {
					terminal.resetWrites()
					ui.ForceRender()
					output := terminal.output()
					lastMarker := -1
					for _, name := range operation.Probe {
						if marker := strings.LastIndex(output, f12TraceLine(name)); marker > lastMarker {
							value := name
							front = &value
							lastMarker = marker
						}
					}
				}
				observation := f12OverlayFocusObservation{
					Label:      operation.Label,
					Focused:    make([]string, 0),
					Inputs:     make(map[string][]string),
					Handles:    make(map[string]f12OverlayFocusHandleState),
					HasOverlay: ui.HasOverlay(),
					Front:      front,
				}
				for _, name := range fixtureCase.Components {
					component := focusable[name]
					if component == nil {
						continue
					}
					if component.focused {
						observation.Focused = append(observation.Focused, name)
					}
					if len(component.inputs) > 0 {
						observation.Inputs[name] = append([]string(nil), component.inputs...)
					}
				}
				for name, handle := range handles {
					observation.Handles[name] = f12OverlayFocusHandleState{Hidden: handle.IsHidden(), Focused: handle.IsFocused()}
				}
				got = append(got, observation)
			}
			if err := ui.Stop(); err != nil {
				t.Fatal(err)
			}
			if gotJSON, wantJSON := mustJSON(got), mustJSON(fixtureCase.Expected); gotJSON != wantJSON {
				t.Fatalf("focus trace differs\n got: %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}

type f12TerminalColorFixture struct {
	SchemaVersion          int                          `json:"schemaVersion"`
	ParserCases            []f12TerminalColorParserCase `json:"parserCases"`
	SchemeParserCases      []f12SchemeParserCase        `json:"schemeParserCases"`
	BackgroundCases        []f12BackgroundCase          `json:"backgroundCases"`
	BackgroundLateQueue    f12BackgroundLateQueue       `json:"backgroundLateQueue"`
	SchemeProtocols        f12SchemeProtocols           `json:"schemeProtocols"`
	NotificationWrites     []string                     `json:"notificationWrites"`
	NotificationStopWrites []string                     `json:"notificationStopWrites"`
}

type f12TerminalColorParserCase struct {
	Name       string        `json:"name"`
	Input      string        `json:"input"`
	IsResponse bool          `json:"isResponse"`
	Expected   *tui.RgbColor `json:"expected"`
}

type f12SchemeParserCase struct {
	Input    string                   `json:"input"`
	Expected *tui.TerminalColorScheme `json:"expected"`
}

type f12BackgroundCase struct {
	Name                         string        `json:"name"`
	Scenario                     string        `json:"scenario"`
	Writes                       []string      `json:"writes"`
	Result                       *tui.RgbColor `json:"result"`
	SettledAfterNonMatchingInput *bool         `json:"settledAfterNonMatchingInput,omitempty"`
	ListenerInputs               []string      `json:"listenerInputs"`
	FocusedInputs                []string      `json:"focusedInputs"`
}

type f12BackgroundLateQueue struct {
	Writes                     []string      `json:"writes"`
	FirstResult                *tui.RgbColor `json:"firstResult"`
	SettledAfterFirstLateReply bool          `json:"settledAfterFirstLateReply"`
	SecondResult               *tui.RgbColor `json:"secondResult"`
	ListenerInputs             []string      `json:"listenerInputs"`
	FocusedInputs              []string      `json:"focusedInputs"`
}

type f12SchemeProtocols struct {
	Query                 f12SchemeObservation           `json:"query"`
	TimeoutLate           f12SchemeTimeoutObservation    `json:"timeoutLate"`
	Concurrent            f12ConcurrentSchemeObservation `json:"concurrent"`
	ListenerOrder         []string                       `json:"listenerOrder"`
	ListenerMutationOrder []string                       `json:"listenerMutationOrder"`
}

type f12SchemeObservation struct {
	Writes []string                  `json:"writes"`
	Result *tui.TerminalColorScheme  `json:"result"`
	Events []tui.TerminalColorScheme `json:"events"`
}

type f12SchemeTimeoutObservation struct {
	Writes         []string                  `json:"writes"`
	Result         *tui.TerminalColorScheme  `json:"result"`
	Events         []tui.TerminalColorScheme `json:"events"`
	ListenerInputs []string                  `json:"listenerInputs"`
	FocusedInputs  []string                  `json:"focusedInputs"`
}

type f12ConcurrentSchemeObservation struct {
	Writes  []string                  `json:"writes"`
	Results []tui.TerminalColorScheme `json:"results"`
	Events  []tui.TerminalColorScheme `json:"events"`
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func receiveColor(t *testing.T, result <-chan *tui.RgbColor) *tui.RgbColor {
	t.Helper()
	select {
	case color := <-result:
		return color
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal color")
		return nil
	}
}

func receiveScheme(t *testing.T, result <-chan tui.TerminalColorScheme) tui.TerminalColorScheme {
	t.Helper()
	select {
	case scheme := <-result:
		return scheme
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal color scheme")
		return ""
	}
}

func schemePointer(scheme tui.TerminalColorScheme) *tui.TerminalColorScheme {
	if scheme == "" {
		return nil
	}
	return &scheme
}

func exactWrites(writes []string, sequence string) []string {
	result := make([]string, 0)
	for _, write := range writes {
		if write == sequence {
			result = append(result, write)
		}
	}
	return result
}

func TestF12TerminalColorsMatchUpstream(t *testing.T) {
	var fixture f12TerminalColorFixture
	runner.LoadJSON(t, "F12", "terminal-colors.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.ParserCases) != 10 || len(fixture.SchemeParserCases) != 5 || len(fixture.BackgroundCases) != 5 {
		t.Fatalf("terminal-color fixture header = %#v", fixture)
	}
	for _, fixtureCase := range fixture.ParserCases {
		t.Run("parse-"+fixtureCase.Name, func(t *testing.T) {
			if got := tui.IsOsc11BackgroundColorResponse(fixtureCase.Input); got != fixtureCase.IsResponse {
				t.Fatalf("IsOsc11BackgroundColorResponse = %v, want %v", got, fixtureCase.IsResponse)
			}
			color, ok := tui.ParseOsc11BackgroundColor(fixtureCase.Input)
			if ok != (fixtureCase.Expected != nil) || ok && color != *fixtureCase.Expected {
				t.Fatalf("ParseOsc11BackgroundColor = %#v, %v; want %#v", color, ok, fixtureCase.Expected)
			}
		})
	}
	for _, fixtureCase := range fixture.SchemeParserCases {
		scheme, ok := tui.ParseTerminalColorSchemeReport(fixtureCase.Input)
		if ok != (fixtureCase.Expected != nil) || ok && scheme != *fixtureCase.Expected {
			t.Fatalf("ParseTerminalColorSchemeReport(%q) = %q, %v; want %#v", fixtureCase.Input, scheme, ok, fixtureCase.Expected)
		}
	}

	for _, fixtureCase := range fixture.BackgroundCases {
		t.Run("background-"+fixtureCase.Scenario, func(t *testing.T) {
			terminal := &f12CoreTerminal{columns: 80, rows: 24}
			ui := tui.NewTUI(terminal)
			focused := &f12CoreComponent{lines: []string{"INPUT"}}
			listenerInputs := make([]string, 0)
			ui.AddChild(focused)
			ui.SetFocus(focused)
			ui.AddInputListener(func(data string) tui.InputListenerResult {
				listenerInputs = append(listenerInputs, data)
				return tui.InputListenerResult{}
			})
			if err := ui.Start(); err != nil {
				t.Fatal(err)
			}
			terminal.resetWrites()
			timeout := time.Second
			if fixtureCase.Scenario == "late" {
				timeout = time.Millisecond
			}
			query := ui.QueryTerminalBackgroundColor(timeout)
			var result *tui.RgbColor
			var settledAfterNonMatchingInput *bool
			switch fixtureCase.Scenario {
			case "valid":
				terminal.send("\x1b]11;#ffffff\x07")
				result = receiveColor(t, query)
			case "consumed":
				terminal.send("\x1b]11;#000000\x07")
				result = receiveColor(t, query)
			case "invalid":
				terminal.send("\x1b]11;not-a-color\x07")
				result = receiveColor(t, query)
			case "nonMatching":
				terminal.send("x")
				settled := false
				select {
				case result = <-query:
					settled = true
				default:
				}
				settledAfterNonMatchingInput = &settled
				if !settled {
					terminal.send("\x1b]11;#ffffff\x07")
					result = receiveColor(t, query)
				}
			case "late":
				result = receiveColor(t, query)
				terminal.send("\x1b]11;#ffffff\x07")
			default:
				t.Fatalf("unknown background scenario %q", fixtureCase.Scenario)
			}
			got := f12BackgroundCase{
				Name:                         fixtureCase.Name,
				Scenario:                     fixtureCase.Scenario,
				Writes:                       exactWrites(terminal.writeList(), "\x1b]11;?\x07"),
				Result:                       result,
				SettledAfterNonMatchingInput: settledAfterNonMatchingInput,
				ListenerInputs:               listenerInputs,
				FocusedInputs:                append([]string{}, focused.inputs...),
			}
			if err := ui.Stop(); err != nil {
				t.Fatal(err)
			}
			if mustJSON(got) != mustJSON(fixtureCase) {
				t.Fatalf("background protocol differs\n got: %s\nwant: %s", mustJSON(got), mustJSON(fixtureCase))
			}
		})
	}

	queueTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	queueUI := tui.NewTUI(queueTerminal)
	focused := &f12CoreComponent{lines: []string{"INPUT"}}
	listenerInputs := make([]string, 0)
	queueUI.SetFocus(focused)
	queueUI.AddInputListener(func(data string) tui.InputListenerResult {
		listenerInputs = append(listenerInputs, data)
		return tui.InputListenerResult{}
	})
	if err := queueUI.Start(); err != nil {
		t.Fatal(err)
	}
	queueTerminal.resetWrites()
	first := queueUI.QueryTerminalBackgroundColor(time.Millisecond)
	if got := receiveColor(t, first); got != nil {
		t.Fatalf("first timeout = %#v", got)
	}
	second := queueUI.QueryTerminalBackgroundColor(time.Second)
	queueTerminal.send("\x1b]11;#111111\x07")
	settledAfterFirstLateReply := false
	select {
	case <-second:
		settledAfterFirstLateReply = true
	case <-time.After(5 * time.Millisecond):
	}
	queueTerminal.send("\x1b]11;rgb:ffff/0000/8000\x1b\\")
	secondResult := receiveColor(t, second)
	queueWrites := queueTerminal.writeList()
	queueTerminal.send("x")
	late := fixture.BackgroundLateQueue
	if settledAfterFirstLateReply != late.SettledAfterFirstLateReply || mustJSON(secondResult) != mustJSON(late.SecondResult) ||
		mustJSON(listenerInputs) != mustJSON(late.ListenerInputs) || mustJSON(focused.inputs) != mustJSON(late.FocusedInputs) ||
		mustJSON(queueWrites) != mustJSON(late.Writes) {
		t.Fatalf("late queue differs: settled=%v result=%s listeners=%s focus=%s writes=%s", settledAfterFirstLateReply,
			mustJSON(secondResult), mustJSON(listenerInputs), mustJSON(focused.inputs), mustJSON(queueWrites))
	}
	_ = queueUI.Stop()

	schemeTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	schemeUI := tui.NewTUI(schemeTerminal)
	events := make([]tui.TerminalColorScheme, 0)
	schemeUI.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) { events = append(events, scheme) })
	if err := schemeUI.Start(); err != nil {
		t.Fatal(err)
	}
	schemeTerminal.resetWrites()
	schemeResult := schemeUI.QueryTerminalColorScheme(time.Second)
	schemeTerminal.send("\x1b[?997;2n")
	gotScheme := receiveScheme(t, schemeResult)
	gotQuery := f12SchemeObservation{
		Writes: exactWrites(schemeTerminal.writeList(), "\x1b[?996n"),
		Result: schemePointer(gotScheme),
		Events: events,
	}
	if mustJSON(gotQuery) != mustJSON(fixture.SchemeProtocols.Query) {
		t.Fatalf("scheme query = %s, want %s", mustJSON(gotQuery), mustJSON(fixture.SchemeProtocols.Query))
	}
	_ = schemeUI.Stop()

	timeoutTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	timeoutUI := tui.NewTUI(timeoutTerminal)
	timeoutFocused := &f12CoreComponent{lines: []string{"INPUT"}}
	timeoutListenerInputs := make([]string, 0)
	timeoutEvents := make([]tui.TerminalColorScheme, 0)
	timeoutUI.SetFocus(timeoutFocused)
	timeoutUI.AddInputListener(func(data string) tui.InputListenerResult {
		timeoutListenerInputs = append(timeoutListenerInputs, data)
		return tui.InputListenerResult{}
	})
	timeoutUI.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) { timeoutEvents = append(timeoutEvents, scheme) })
	if err := timeoutUI.Start(); err != nil {
		t.Fatal(err)
	}
	timeoutTerminal.resetWrites()
	timedOut := receiveScheme(t, timeoutUI.QueryTerminalColorScheme(time.Millisecond))
	timeoutTerminal.send("\x1b[?997;1n")
	gotTimeout := f12SchemeTimeoutObservation{
		Writes:         exactWrites(timeoutTerminal.writeList(), "\x1b[?996n"),
		Result:         schemePointer(timedOut),
		Events:         timeoutEvents,
		ListenerInputs: timeoutListenerInputs,
		FocusedInputs:  append([]string{}, timeoutFocused.inputs...),
	}
	if mustJSON(gotTimeout) != mustJSON(fixture.SchemeProtocols.TimeoutLate) {
		t.Fatalf("scheme timeout/late = %s, want %s", mustJSON(gotTimeout), mustJSON(fixture.SchemeProtocols.TimeoutLate))
	}
	_ = timeoutUI.Stop()

	concurrentTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	concurrentUI := tui.NewTUI(concurrentTerminal)
	concurrentEvents := make([]tui.TerminalColorScheme, 0)
	concurrentUI.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) { concurrentEvents = append(concurrentEvents, scheme) })
	if err := concurrentUI.Start(); err != nil {
		t.Fatal(err)
	}
	concurrentTerminal.resetWrites()
	concurrentFirst := concurrentUI.QueryTerminalColorScheme(time.Second)
	concurrentSecond := concurrentUI.QueryTerminalColorScheme(time.Second)
	concurrentTerminal.send("\x1b[?997;2n")
	gotConcurrent := f12ConcurrentSchemeObservation{
		Writes: exactWrites(concurrentTerminal.writeList(), "\x1b[?996n"),
		Results: []tui.TerminalColorScheme{
			receiveScheme(t, concurrentFirst),
			receiveScheme(t, concurrentSecond),
		},
		Events: concurrentEvents,
	}
	if mustJSON(gotConcurrent) != mustJSON(fixture.SchemeProtocols.Concurrent) {
		t.Fatalf("concurrent scheme queries = %s, want %s", mustJSON(gotConcurrent), mustJSON(fixture.SchemeProtocols.Concurrent))
	}
	_ = concurrentUI.Stop()

	listenerTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	listenerUI := tui.NewTUI(listenerTerminal)
	listenerOrder := make([]string, 0)
	removeFirst := listenerUI.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) {
		listenerOrder = append(listenerOrder, "first:"+string(scheme))
	})
	listenerUI.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) {
		listenerOrder = append(listenerOrder, "second:"+string(scheme))
	})
	if err := listenerUI.Start(); err != nil {
		t.Fatal(err)
	}
	listenerTerminal.send("\x1b[?997;1n")
	removeFirst()
	listenerTerminal.send("\x1b[?997;2n")
	if mustJSON(listenerOrder) != mustJSON(fixture.SchemeProtocols.ListenerOrder) {
		t.Fatalf("scheme listener order = %s, want %s", mustJSON(listenerOrder), mustJSON(fixture.SchemeProtocols.ListenerOrder))
	}
	_ = listenerUI.Stop()

	mutationTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	mutationUI := tui.NewTUI(mutationTerminal)
	listenerMutationOrder := make([]string, 0)
	removeSecond := func() {}
	mutationUI.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) {
		listenerMutationOrder = append(listenerMutationOrder, "first:"+string(scheme))
		removeSecond()
		mutationUI.OnTerminalColorSchemeChange(func(addedScheme tui.TerminalColorScheme) {
			listenerMutationOrder = append(listenerMutationOrder, "added:"+string(addedScheme))
		})
	})
	removeSecond = mutationUI.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) {
		listenerMutationOrder = append(listenerMutationOrder, "second:"+string(scheme))
	})
	if err := mutationUI.Start(); err != nil {
		t.Fatal(err)
	}
	mutationTerminal.send("\x1b[?997;1n")
	if mustJSON(listenerMutationOrder) != mustJSON(fixture.SchemeProtocols.ListenerMutationOrder) {
		t.Fatalf("scheme listener mutation order = %s, want %s", mustJSON(listenerMutationOrder), mustJSON(fixture.SchemeProtocols.ListenerMutationOrder))
	}
	_ = mutationUI.Stop()

	notificationsTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	notificationsUI := tui.NewTUI(notificationsTerminal)
	notificationsUI.SetTerminalColorSchemeNotifications(true)
	notificationsUI.SetTerminalColorSchemeNotifications(true)
	if err := notificationsUI.Start(); err != nil {
		t.Fatal(err)
	}
	notificationsUI.SetTerminalColorSchemeNotifications(false)
	notificationsUI.SetTerminalColorSchemeNotifications(false)
	_ = notificationsUI.Stop()
	notificationWrites := make([]string, 0)
	for _, write := range notificationsTerminal.writeList() {
		if strings.Contains(write, "2031") {
			notificationWrites = append(notificationWrites, write)
		}
	}
	if mustJSON(notificationWrites) != mustJSON(fixture.NotificationWrites) {
		t.Fatalf("notification writes = %s, want %s", mustJSON(notificationWrites), mustJSON(fixture.NotificationWrites))
	}

	stopNotificationsTerminal := &f12CoreTerminal{columns: 80, rows: 24}
	stopNotificationsUI := tui.NewTUI(stopNotificationsTerminal)
	stopNotificationsUI.SetTerminalColorSchemeNotifications(true)
	if err := stopNotificationsUI.Start(); err != nil {
		t.Fatal(err)
	}
	if err := stopNotificationsUI.Stop(); err != nil {
		t.Fatal(err)
	}
	stopNotificationWrites := make([]string, 0)
	for _, write := range stopNotificationsTerminal.writeList() {
		if strings.Contains(write, "2031") {
			stopNotificationWrites = append(stopNotificationWrites, write)
		}
	}
	if mustJSON(stopNotificationWrites) != mustJSON(fixture.NotificationStopWrites) {
		t.Fatalf("notification stop writes = %s, want %s", mustJSON(stopNotificationWrites), mustJSON(fixture.NotificationStopWrites))
	}
}
