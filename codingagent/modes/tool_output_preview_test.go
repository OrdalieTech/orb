package modes

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
	"github.com/OrdalieTech/pi-go/tui"
)

type toolOutputPreviewCase struct {
	ID    string   `json:"id"`
	Width int      `json:"width"`
	Lines []string `json:"lines"`
}

type toolOutputPreviewFixture struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Cases         []toolOutputPreviewCase `json:"cases"`
}

func TestWP450ToolOutputPreviewsMatchUpstream(t *testing.T) {
	initTestTheme(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)

	var fixture toolOutputPreviewFixture
	wp450LoadJSON(t, "tool-output-previews.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != len(wp450ReplayWidths)*8 {
		t.Fatalf("unexpected tool-output preview fixture: version %d, cases %d", fixture.SchemaVersion, len(fixture.Cases))
	}

	got := make(map[string]toolOutputPreviewCase, len(fixture.Cases))
	for _, width := range wp450ReplayWidths {
		for _, preview := range renderToolOutputPreviewCases(width) {
			got[wp450FrameKey(ConformanceReplayFrame{ID: preview.ID, Width: preview.Width})] = preview
		}
	}
	for _, expected := range fixture.Cases {
		key := wp450FrameKey(ConformanceReplayFrame{ID: expected.ID, Width: expected.Width})
		actual, ok := got[key]
		if !ok {
			t.Errorf("Go preview omitted case %s", key)
			continue
		}
		if diff := wp450LinesDiff(expected.Lines, actual.Lines); diff != "" {
			t.Errorf("%s differs:\n%s", key, diff)
		}
	}
}

func renderToolOutputPreviewCases(width int) []toolOutputPreviewCase {
	requester := &toolOutputRenderRequester{}
	output := wp450LongToolOutput()
	cases := make([]toolOutputPreviewCase, 0, 8)
	capture := func(id string, component tui.Component) {
		lines := normalizeWP450Lines(component.Render(width))
		filtered := lines[:0]
		for _, line := range lines {
			if !strings.Contains(line, "Running... (") {
				filtered = append(filtered, line)
			}
		}
		cases = append(cases, toolOutputPreviewCase{ID: id, Width: width, Lines: filtered})
	}

	tool := NewToolExecutionComponent(
		"bash",
		"call-streaming-bash",
		map[string]any{"command": "printf streaming-output"},
		false,
		nativeToolDefinition("bash", tools.NewBashTool("/workspace", nil)),
		requester,
		"/workspace",
	)
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: output}}, false, nil, true)
	capture("tool-partial-collapsed", tool)
	tool.SetExpanded(true)
	capture("tool-partial-expanded", tool)
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: output}}, false, nil, false)
	capture("tool-final-expanded", tool)
	tool.SetExpanded(false)
	capture("tool-final-collapsed", tool)

	bash := NewBashExecutionComponent("printf streaming-output", requester, false)
	bash.AppendOutput(output)
	capture("bang-bash-partial-collapsed", bash)
	bash.SetExpanded(true)
	capture("bang-bash-partial-expanded", bash)
	exitCode := 0
	bash.SetComplete(&exitCode, false)
	capture("bang-bash-final-expanded", bash)
	bash.SetExpanded(false)
	capture("bang-bash-final-collapsed", bash)

	return cases
}

func TestStreamingToolPreviewDoesNotClearScreen(t *testing.T) {
	initTestTheme(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	terminal := &toolOutputTerminal{columns: 52, rows: 24}
	uiRoot := tui.NewTUI(terminal)
	component := NewToolExecutionComponent(
		"bash",
		"call-streaming-bash",
		map[string]any{"command": "printf streaming-output"},
		false,
		nativeToolDefinition("bash", tools.NewBashTool("/workspace", nil)),
		uiRoot,
		"/workspace",
	)
	uiRoot.AddChild(component)
	if err := uiRoot.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = uiRoot.Stop() }()

	fullClears := 0
	for frame := 0; frame < 12; frame++ {
		lines := make([]string, 36)
		for index := range lines {
			lines[index] = "stream window " + strings.Repeat("x", 36) + string(rune('A'+(frame+index)%26))
		}
		component.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: strings.Join(lines, "\n")}}, false, nil, true)
		terminal.resetOutput()
		uiRoot.RenderNow()
		fullClears += strings.Count(terminal.output(), "\x1b[2J")
	}
	if fullClears != 0 {
		t.Fatalf("streaming preview emitted %d full-screen clears", fullClears)
	}
	t.Logf("streaming full-screen clears: %d", fullClears)
}

func wp450LongToolOutput() string {
	lines := make([]string, 24)
	for index := range lines {
		lines[index] = "stream line " + twoDigits(index+1) + ": abcdefghijklmnopqrstuvwxyz 0123456789"
	}
	return strings.Join(lines, "\n")
}

func twoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}

type toolOutputRenderRequester struct{}

func (*toolOutputRenderRequester) RequestRender() {}

type toolOutputTerminal struct {
	mu            sync.Mutex
	columns, rows int
	writes        []string
}

func (*toolOutputTerminal) Start(func(string), func()) error        { return nil }
func (*toolOutputTerminal) Stop() error                             { return nil }
func (*toolOutputTerminal) DrainInput(time.Duration, time.Duration) {}
func (terminal *toolOutputTerminal) Columns() int                   { return terminal.columns }
func (terminal *toolOutputTerminal) Rows() int                      { return terminal.rows }
func (*toolOutputTerminal) KittyProtocolActive() bool               { return false }
func (*toolOutputTerminal) HideCursor()                             {}
func (*toolOutputTerminal) ShowCursor()                             {}
func (*toolOutputTerminal) ClearLine()                              {}
func (*toolOutputTerminal) ClearFromCursor()                        {}
func (*toolOutputTerminal) ClearScreen()                            {}
func (*toolOutputTerminal) SetTitle(string)                         {}
func (*toolOutputTerminal) SetProgress(bool)                        {}
func (terminal *toolOutputTerminal) MoveBy(lines int)               {}
func (terminal *toolOutputTerminal) Write(data string) {
	terminal.mu.Lock()
	terminal.writes = append(terminal.writes, data)
	terminal.mu.Unlock()
}
func (terminal *toolOutputTerminal) output() string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return strings.Join(terminal.writes, "")
}
func (terminal *toolOutputTerminal) resetOutput() {
	terminal.mu.Lock()
	terminal.writes = nil
	terminal.mu.Unlock()
}
