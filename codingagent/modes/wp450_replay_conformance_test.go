package modes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	modetheme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/OrdalieTech/pigo/tui"
)

const (
	wp450ReplayRows     = 120
	wp450FixedTimestamp = int64(1_700_000_000_000)
)

var wp450ReplayWidths = []int{52, 88}

// ConformanceReplayFrame is the normalized visible frame emitted by the
// isolated WP-450 headless replay seam.
type ConformanceReplayFrame struct {
	ID    string   `json:"id"`
	Width int      `json:"width"`
	Lines []string `json:"lines"`
}

type ConformanceUIDemoArtifact struct {
	SchemaVersion int `json:"schemaVersion"`
	StatusLine    struct {
		Events []ConformanceUIStatusEvent `json:"events"`
	} `json:"statusLine"`
	WidgetPlacement struct {
		Above []ConformanceUIWidget `json:"above"`
		Below []ConformanceUIWidget `json:"below"`
	} `json:"widgetPlacement"`
	HeaderFooterInitialization struct {
		Width    int      `json:"width"`
		Header   []string `json:"header"`
		Footer   []string `json:"footer"`
		Retained struct {
			StatusKeys []string `json:"statusKeys"`
			WidgetKeys []string `json:"widgetKeys"`
			Header     bool     `json:"header"`
			Footer     bool     `json:"footer"`
		} `json:"retained"`
	} `json:"headerFooterInitialization"`
}

type ConformanceUIStatusEvent struct {
	Event string `json:"event"`
	Value string `json:"value"`
}

type ConformanceUIWidget struct {
	Key       string   `json:"key"`
	Placement string   `json:"placement"`
	Lines     []string `json:"lines"`
}

// RenderWP450ConformanceReplay is test support for the independent WP-450
// conformance lane. It deliberately uses the narrow production components
// available at the WP-450 integration seam.
func RenderWP450ConformanceReplay() []ConformanceReplayFrame {
	restore := setWP450ConformanceTheme()
	defer restore()

	frames := make([]ConformanceReplayFrame, 0, len(wp450ReplayWidths)*10)
	for _, width := range wp450ReplayWidths {
		frames = append(frames, renderWP450ReplayWidth(width)...)
	}
	return frames
}

func renderWP450ReplayWidth(width int) []ConformanceReplayFrame {
	scenarioRoot, err := os.MkdirTemp("", "pi-wp450-replay-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(scenarioRoot) }()
	if err := os.WriteFile(scenarioRoot+"/fixture.txt", []byte("alpha\nold value\nomega\n"), 0o644); err != nil {
		panic(err)
	}
	terminal := &wp450ConformanceTerminal{columns: width, rows: wp450ReplayRows}
	ui := tui.NewTUI(terminal)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)

	mode := &InteractiveMode{
		ui:              ui,
		keybindings:     bindings,
		header:          &tui.Container{},
		chat:            &tui.Container{},
		pendingMessages: &tui.Container{},
		status:          &tui.Container{},
		widgetAbove:     &tui.Container{},
		editorContainer: &tui.Container{},
		widgetBelow:     &tui.Container{},
		toolComponents:  make(map[string]*ToolExecutionComponent),
		footerStatuses:  make(map[string]string),
		cwd:             scenarioRoot,
		outputPad:       1,
	}
	mode.editor = NewCustomEditor(ui, modetheme.EditorTheme(), bindings)
	mode.editor.SetPaddingX(1)
	mode.editorContainer.AddChild(mode.editor)
	mode.status.AddChild(&IdleStatus{})
	mode.header.AddChild(tui.NewText("pigo built-in header", 0, 0, nil))
	interactiveUI := NewInteractiveUI(mode)
	mode.interactiveUI = interactiveUI

	ready := modetheme.FG("dim", "Ready")
	interactiveUI.SetStatus("status-demo", &ready)
	mode.widgetAbove.AddChild(tui.NewSpacer(1))
	interactiveUI.SetWidget("widget-above", &extensions.Widget{Lines: []string{"Above editor widget"}}, nil)
	interactiveUI.SetWidget(
		"widget-below",
		&extensions.Widget{Lines: []string{"Below editor widget"}},
		&extensions.WidgetOptions{Placement: extensions.WidgetBelowEditor},
	)
	interactiveUI.SetHeader(wp450HeaderFactory)

	footerSession := &wp450FooterSession{state: agent.AgentState{
		Model:         &ai.Model{ID: "fixture-model", Provider: "fixture", ContextWindow: 8192, Reasoning: true},
		ThinkingLevel: agent.ThinkingLevel("medium"),
	}}
	footerProvider := &wp450FooterProvider{mode: mode}
	footer := NewFooterComponent(footerSession, footerProvider)

	root := &tui.Container{}
	for _, component := range []tui.Component{
		mode.header,
		mode.chat,
		mode.pendingMessages,
		mode.status,
		mode.widgetAbove,
		mode.editorContainer,
		mode.widgetBelow,
		footer,
	} {
		root.AddChild(component)
	}
	ui.SetFocus(mode.editor)

	frames := make([]ConformanceReplayFrame, 0, 10)
	capture := func(id string) {
		frames = append(frames, ConformanceReplayFrame{ID: id, Width: width, Lines: normalizeWP450Lines(root.Render(width))})
	}

	capture("session-initialized")
	running := modetheme.FG("accent", "●") + modetheme.FG("dim", " Turn 1...")
	interactiveUI.SetStatus("status-demo", &running)
	mode.chat.AddChild(NewUserMessageComponent(
		"Please update `fixture.txt` and explain the change.",
		modetheme.MarkdownTheme(),
		1,
	))
	capture("user-message")

	assistant := &ai.AssistantMessage{
		Content: ai.AssistantContent{
			&ai.ThinkingContent{Thinking: "I should inspect the target and make one precise replacement."},
			&ai.TextContent{Text: "I found the requested line and will update it now."},
		},
		API:        ai.APIAnthropicMessages,
		Provider:   ai.ProviderID("anthropic"),
		Model:      "fixture-model",
		StopReason: ai.StopReasonStop,
		Timestamp:  wp450FixedTimestamp,
	}
	mode.chat.AddChild(NewAssistantMessageComponent(assistant, false, modetheme.MarkdownTheme(), "Thinking...", 1))
	capture("assistant-thinking-text")

	edit := NewToolExecutionComponent(
		"edit",
		"call-edit",
		map[string]any{"path": "fixture.txt"},
		false,
		nativeToolDefinition("edit", tools.NewEditTool(scenarioRoot, nil)),
		ui,
		scenarioRoot,
	)
	mode.chat.AddChild(edit)
	capture("tool-call")
	edit.UpdateArgs(map[string]any{
		"path":  "fixture.txt",
		"edits": []map[string]any{{"oldText": "old value", "newText": "new value"}},
	})
	edit.SetArgsComplete()
	edit.MarkExecutionStarted()
	capture("tool-update-diff")
	diff := tools.GenerateDiffString("alpha\nold value\nomega\n", "alpha\nnew value\nomega\n", 4)
	edit.UpdateResult(
		ai.ToolResultContent{&ai.TextContent{Text: "Successfully replaced text in fixture.txt"}},
		false,
		map[string]any{"diff": diff.Diff, "patch": "", "firstChangedLine": diff.FirstChangedLine},
		false,
	)
	capture("tool-result-diff")

	bash := NewBashExecutionComponent("printf 'alpha\\nbeta\\n'", ui, false)
	bash.AppendOutput("alpha\nbeta\n")
	exitCode := 0
	bash.SetComplete(&exitCode, false)
	mode.chat.AddChild(bash)
	capture("bash-complete")

	compaction := NewCompactionSummaryMessage(
		"Earlier work inspected the file and preserved the surrounding lines.",
		12_345,
		modetheme.MarkdownTheme(),
	)
	compaction.SetExpanded(true)
	mode.chat.AddChild(compaction)
	mode.chat.AddChild(NewCustomMessageComponent(
		"fixture-note",
		"Custom boundary retained after compaction.",
		modetheme.MarkdownTheme(),
	))
	branch := NewBranchSummaryMessage(
		"The alternate branch changed the same fixture line.",
		modetheme.MarkdownTheme(),
	)
	branch.SetExpanded(true)
	mode.chat.AddChild(branch)
	capture("session-boundaries")

	mode.pendingMessages.AddChild(tui.NewSpacer(1))
	mode.pendingMessages.AddChild(tui.NewTruncatedText(modetheme.FG("dim", "Steering: verify the diff"), 1, 0))
	mode.pendingMessages.AddChild(tui.NewTruncatedText(modetheme.FG("dim", "Follow-up: summarize the result"), 1, 0))
	mode.pendingMessages.AddChild(tui.NewTruncatedText(modetheme.FG("dim", "↳ alt+up to edit all queued messages"), 1, 0))
	capture("queue-pending")

	complete := modetheme.FG("success", "✓") + modetheme.FG("dim", " Turn 1 complete")
	interactiveUI.SetStatus("status-demo", &complete)
	mode.editor.SetText("/name replay-界")
	capture("editor-ready")

	return frames
}

func RenderWP450UIDemoArtifact() ConformanceUIDemoArtifact {
	restore := setWP450ConformanceTheme()
	defer restore()

	const width = 72
	terminal := &wp450ConformanceTerminal{columns: width, rows: 30}
	ui := tui.NewTUI(terminal)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	mode := &InteractiveMode{
		ui:              ui,
		keybindings:     bindings,
		header:          &tui.Container{},
		chat:            &tui.Container{},
		status:          &tui.Container{},
		widgetAbove:     &tui.Container{},
		editorContainer: &tui.Container{},
		widgetBelow:     &tui.Container{},
		footer:          &tui.Container{},
		toolComponents:  make(map[string]*ToolExecutionComponent),
		footerStatuses:  make(map[string]string),
		cwd:             "/workspace",
	}
	mode.header.AddChild(tui.NewText("pigo built-in header", 0, 0, nil))
	mode.editor = NewCustomEditor(ui, modetheme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	interactiveUI := NewInteractiveUI(mode)
	mode.interactiveUI = interactiveUI

	artifact := ConformanceUIDemoArtifact{SchemaVersion: 1}
	artifact.HeaderFooterInitialization.Width = width
	recordStatus := func(event, value string) {
		interactiveUI.SetStatus("status-demo", &value)
		artifact.StatusLine.Events = append(artifact.StatusLine.Events, ConformanceUIStatusEvent{
			Event: event,
			Value: stripWP450TerminalControls(value),
		})
	}
	recordStatus("session_start", modetheme.FG("dim", "Ready"))
	interactiveUI.SetWidget("widget-above", &extensions.Widget{Lines: []string{"Above editor widget"}}, nil)
	interactiveUI.SetWidget(
		"widget-below",
		&extensions.Widget{Lines: []string{"Below editor widget"}},
		&extensions.WidgetOptions{Placement: extensions.WidgetBelowEditor},
	)
	interactiveUI.SetHeader(wp450HeaderFactory)
	interactiveUI.SetFooter(wp450FooterFactory)
	recordStatus("turn_start", modetheme.FG("accent", "●")+modetheme.FG("dim", " Turn 1..."))
	recordStatus("turn_end", modetheme.FG("success", "✓")+modetheme.FG("dim", " Turn 1 complete"))

	artifact.WidgetPlacement.Above, artifact.WidgetPlacement.Below = wp450ObservedWidgets(interactiveUI, width)
	artifact.HeaderFooterInitialization.Header = normalizeWP450Lines(mode.header.Render(width))
	artifact.HeaderFooterInitialization.Footer = normalizeWP450Lines(mode.footer.Render(width))
	for key := range mode.footerStatuses {
		artifact.HeaderFooterInitialization.Retained.StatusKeys = append(artifact.HeaderFooterInitialization.Retained.StatusKeys, key)
	}
	for key := range interactiveUI.widgets {
		artifact.HeaderFooterInitialization.Retained.WidgetKeys = append(artifact.HeaderFooterInitialization.Retained.WidgetKeys, key)
	}
	sort.Strings(artifact.HeaderFooterInitialization.Retained.StatusKeys)
	sort.Strings(artifact.HeaderFooterInitialization.Retained.WidgetKeys)
	artifact.HeaderFooterInitialization.Retained.Header = containsWP450Line(
		artifact.HeaderFooterInitialization.Header,
		"shitty coding agent v0.81.0",
	)
	artifact.HeaderFooterInitialization.Retained.Footer = containsWP450Line(
		artifact.HeaderFooterInitialization.Footer,
		"↑1.2k ↓34 $0.003",
	)
	return artifact
}

func wp450ObservedWidgets(ui *InteractiveUI, width int) (above, below []ConformanceUIWidget) {
	keys := make([]string, 0, len(ui.widgets))
	for key := range ui.widgets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := ui.widgets[key]
		component := ui.widgetComps[key]
		lines := normalizeWP450Lines(component.Render(width))
		for index := range lines {
			lines[index] = strings.TrimSpace(lines[index])
		}
		widget := ConformanceUIWidget{Key: key, Placement: string(entry.placement), Lines: lines}
		if entry.placement == extensions.WidgetBelowEditor {
			below = append(below, widget)
		} else {
			above = append(above, widget)
		}
	}
	return above, below
}

func wp450HeaderFactory(_ extensions.UIHost, theme extensions.Theme) extensions.Component {
	blue := func(value string) string { return theme.FG("accent", value) }
	dim := func(value string) string { return theme.FG("dim", value) }
	eye := "█" + dim("▌")
	leg := "     " + blue("██") + "    " + blue("██")
	return wp450LinesComponent{
		"",
		"     " + eye + "  " + eye,
		"  " + blue(strings.Repeat("█", 14)),
		leg,
		leg,
		leg,
		leg,
		"",
		theme.FG("muted", "   shitty coding agent") + dim(" v0.81.0"),
	}
}

func wp450FooterFactory(_ extensions.UIHost, theme extensions.Theme, _ extensions.FooterDataProvider) extensions.Component {
	return wp450DynamicComponent(func(width int) []string {
		left := theme.FG("dim", "↑1.2k ↓34 $0.003")
		right := theme.FG("dim", "fixture-model (main)")
		padding := strings.Repeat(" ", max(1, width-tui.VisibleWidth(left)-tui.VisibleWidth(right)))
		return []string{tui.TruncateToWidth(left+padding+right, width, "", false)}
	})
}

type wp450DynamicComponent func(int) []string

func (component wp450DynamicComponent) Render(width int) []string { return component(width) }

type wp450LinesComponent []string

func (component wp450LinesComponent) Render(int) []string { return append([]string(nil), component...) }

type wp450FooterSession struct{ state agent.AgentState }

func (session *wp450FooterSession) State() agent.AgentState { return session.state }
func (*wp450FooterSession) GetSessionStats() codingagent.SessionStats {
	tokens := int64(1024)
	percent := 12.5
	return codingagent.SessionStats{ContextUsage: &harness.ContextUsage{
		Tokens: &tokens, ContextWindow: 8192, Percent: &percent,
	}}
}

type wp450FooterProvider struct{ mode *InteractiveMode }

func (*wp450FooterProvider) GitBranch() string           { return "main" }
func (*wp450FooterProvider) CurrentCWD() string          { return "/workspace" }
func (*wp450FooterProvider) SessionName() string         { return "fixture-session" }
func (*wp450FooterProvider) AvailableProviderCount() int { return 1 }
func (provider *wp450FooterProvider) Statuses() map[string]string {
	return provider.mode.Statuses()
}

type wp450ConformanceTerminal struct{ columns, rows int }

func (*wp450ConformanceTerminal) Start(func(string), func()) error { return nil }
func (*wp450ConformanceTerminal) Stop() error                      { return nil }
func (*wp450ConformanceTerminal) DrainInput(time.Duration, time.Duration) {
}
func (*wp450ConformanceTerminal) Write(string)              {}
func (terminal *wp450ConformanceTerminal) Columns() int     { return terminal.columns }
func (terminal *wp450ConformanceTerminal) Rows() int        { return terminal.rows }
func (*wp450ConformanceTerminal) KittyProtocolActive() bool { return false }
func (*wp450ConformanceTerminal) MoveBy(int)                {}
func (*wp450ConformanceTerminal) HideCursor()               {}
func (*wp450ConformanceTerminal) ShowCursor()               {}
func (*wp450ConformanceTerminal) ClearLine()                {}
func (*wp450ConformanceTerminal) ClearFromCursor()          {}
func (*wp450ConformanceTerminal) ClearScreen()              {}
func (*wp450ConformanceTerminal) SetTitle(string)           {}
func (*wp450ConformanceTerminal) SetProgress(bool)          {}

func setWP450ConformanceTheme() func() {
	previous := modetheme.Current()
	registry := modetheme.Load(modetheme.LoadOptions{Mode: modetheme.TrueColor, NoThemes: true})
	dark, ok := registry.Get("dark")
	if !ok {
		panic("WP-450 conformance theme is unavailable")
	}
	modetheme.SetCurrent(dark)
	return func() { modetheme.SetCurrent(previous) }
}

func normalizeWP450Lines(lines []string) []string {
	normalized := make([]string, len(lines))
	for index, line := range lines {
		normalized[index] = strings.TrimRightFunc(stripWP450TerminalControls(line), unicode.IsSpace)
	}
	for len(normalized) > 0 && normalized[len(normalized)-1] == "" {
		normalized = normalized[:len(normalized)-1]
	}
	return normalized
}

func stripWP450TerminalControls(value string) string {
	var result strings.Builder
	for position := 0; position < len(value); {
		if value[position] != '\x1b' || position+1 >= len(value) {
			_, size := utf8.DecodeRuneInString(value[position:])
			result.WriteString(value[position : position+size])
			position += size
			continue
		}
		switch value[position+1] {
		case '[':
			end := position + 2
			for end < len(value) && (value[end] < 0x40 || value[end] > 0x7e) {
				end++
			}
			if end < len(value) {
				position = end + 1
				continue
			}
		case ']', '_':
			end := position + 2
			consumed := false
			for end < len(value) {
				if value[end] == '\a' {
					position = end + 1
					consumed = true
					break
				}
				if value[end] == '\x1b' && end+1 < len(value) && value[end+1] == '\\' {
					position = end + 2
					consumed = true
					break
				}
				end++
			}
			if consumed {
				continue
			}
		}
		result.WriteByte(value[position])
		position++
	}
	return result.String()
}

func containsWP450Line(lines []string, fragment string) bool {
	for _, line := range lines {
		if strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

// Tests migrated from conformance/runner when the replay machinery moved out
// of production; fixture bytes and assertions are unchanged.

type wp450ReplayFixture struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Frames        []ConformanceReplayFrame `json:"frames"`
}

func wp450LoadJSON(t *testing.T, name string, target any) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "conformance", "fixtures", "WP450", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func TestWP450SideBySideReplayMatchesUpstream(t *testing.T) {
	var manifest struct {
		Family    string `json:"family"`
		Generator string `json:"generator"`
	}
	wp450LoadJSON(t, "manifest.json", &manifest)
	if manifest.Family != "WP450" || manifest.Generator != "conformance/extract/wp450-replay.ts" {
		t.Fatalf("unexpected WP450 manifest: %+v", manifest)
	}
	var fixture wp450ReplayFixture
	wp450LoadJSON(t, "replay.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Frames) != 20 {
		t.Fatalf("WP450 replay header = version %d, frames %d", fixture.SchemaVersion, len(fixture.Frames))
	}
	want := make(map[string]ConformanceReplayFrame, len(fixture.Frames))
	for _, frame := range fixture.Frames {
		key := wp450FrameKey(frame)
		if _, exists := want[key]; exists {
			t.Fatalf("duplicate upstream frame %s", key)
		}
		want[key] = frame
	}
	gotFrames := RenderWP450ConformanceReplay()
	got := make(map[string]ConformanceReplayFrame, len(gotFrames))
	for _, frame := range gotFrames {
		key := wp450FrameKey(frame)
		if _, exists := got[key]; exists {
			t.Fatalf("duplicate Go frame %s", key)
		}
		got[key] = frame
	}
	for _, expected := range fixture.Frames {
		key := wp450FrameKey(expected)
		t.Run(key, func(t *testing.T) {
			actual, ok := got[key]
			if !ok {
				t.Fatalf("Go replay omitted frame %s", key)
			}
			if diff := wp450LinesDiff(expected.Lines, actual.Lines); diff != "" {
				t.Fatalf("normalized visible frame differs:\n%s", diff)
			}
		})
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("Go replay emitted unexpected frame %s", key)
		}
	}
}

func TestWP450CtxUIDemosRetainInitializationState(t *testing.T) {
	var expected ConformanceUIDemoArtifact
	wp450LoadJSON(t, "ui-demos.json", &expected)
	if expected.SchemaVersion != 1 || len(expected.StatusLine.Events) != 3 {
		t.Fatalf("unexpected WP450 ctx.ui fixture header: %+v", expected)
	}
	actual := RenderWP450UIDemoArtifact()
	if reflect.DeepEqual(actual, expected) {
		return
	}
	wantJSON, _ := json.MarshalIndent(expected, "", "  ")
	gotJSON, _ := json.MarshalIndent(actual, "", "  ")
	t.Fatalf("ctx.ui demo state differs\nwant: %s\n got: %s", wantJSON, gotJSON)
}

func wp450FrameKey(frame ConformanceReplayFrame) string {
	return fmt.Sprintf("%d/%s", frame.Width, frame.ID)
}

func wp450LinesDiff(want, got []string) string {
	if len(want) != len(got) {
		return fmt.Sprintf("line count differs: want %d, got %d\nwant: %q\ngot:  %q", len(want), len(got), want, got)
	}
	for index := range want {
		if want[index] != got[index] {
			return fmt.Sprintf("line differs at index %d: want %q, got %q", index, want[index], got[index])
		}
	}
	return ""
}
