package modes

import (
	"strings"
	"sync"
	"testing"
	"time"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
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

func TestToolActivityMarkersTrackExecution(t *testing.T) {
	initTestTheme(t)
	tool := NewToolExecutionComponent("read", "call", nil, false, nil, &toolOutputRenderRequester{}, "/")
	check := func(marker string) {
		t.Helper()
		for _, width := range []int{12, 52, 88} {
			lines := tool.Render(width)
			if lines[0] != "" || !strings.HasPrefix(tui.StripANSI(lines[1]), marker+"  read") {
				t.Fatalf("width %d: unexpected activity header: %q", width, lines)
			}
			for _, line := range lines[2:] {
				if !strings.HasPrefix(tui.StripANSI(line), "   ") || tui.VisibleWidth(line) > width {
					t.Fatalf("width %d: output lost its plain indentation: %q", width, line)
				}
			}
			if strings.Contains(strings.Join(lines, "\n"), "\x1b[48;") {
				t.Fatal("tool activity has a background fill")
			}
			if strings.Join(tool.Render(width), "\n") != strings.Join(lines, "\n") {
				t.Fatal("rendering mutated cached lines")
			}
		}
	}
	check("○")
	tool.MarkExecutionStarted()
	check("●")
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "first\nsecond\nthird\nfourth"}}, false, nil, true)
	check("●")
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "first\nsecond\nthird\nfourth"}}, false, nil, false)
	check("✓")
	tool.SetExpanded(true)
	check("✓")
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "permission denied"}}, true, nil, false)
	check("×")
}

func TestCommandDetailsStayAccessibleWithoutCompletedPreviews(t *testing.T) {
	initTestTheme(t)
	command := "printf start; " + strings.Repeat("printf middle; ", 8) + "printf finish"
	tool := NewToolExecutionComponent("bash", "command", map[string]any{"command": command}, false,
		nativeToolDefinition("bash", tools.NewBashTool("/", nil)), &toolOutputRenderRequester{}, "/")
	tool.SetArgsComplete()
	if lines := tool.Render(40); len(lines) != 2 || !strings.Contains(tui.StripANSI(lines[1]), "…") {
		t.Fatalf("long command should start as a single action row: %q", lines)
	}
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "live output"}}, false, nil, true)
	if got := strings.Join(tool.Render(40), "\n"); !strings.Contains(got, "live output") {
		t.Fatal("running command lost its live preview")
	}
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "final output"}}, false, nil, false)
	if lines := tool.Render(40); len(lines) != 2 || strings.Contains(strings.Join(lines, "\n"), "final output") {
		t.Fatalf("completed command should collapse to its action: %q", lines)
	}
	tool.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0, Row: 1})
	if got := strings.Join(tool.Render(40), "\n"); !strings.Contains(got, "finish") || !strings.Contains(got, "final output") {
		t.Fatalf("expansion lost the full command or output: %q", got)
	}
	tool.SetExpanded(false)
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "command failed"}}, true, nil, false)
	if got := strings.Join(tool.Render(40), "\n"); !strings.Contains(got, "command failed") {
		t.Fatal("collapsed command hid its failure")
	}
}

type summaryTool struct {
	engine.AgentTool
	call string
}

func (tool summaryTool) RenderCall(any) string                 { return tool.call }
func (summaryTool) RenderResult(engine.AgentToolResult) string { return "" }

func TestCollapsedToolTitlesKeepUsefulDetails(t *testing.T) {
	initTestTheme(t)
	path := "/workspace/" + strings.Repeat("long-directory/", 10) + "session.go"
	for _, test := range []struct {
		name, call, want string
	}{
		{"Read", "Read · " + path, "session.go"},
		{"read", "read " + tui.Hyperlink(path, "file://"+path) + ":20-40", "session.go:20-40"},
		{"Read", "Read · " + path + ".日本語", "session.go.日本語"},
		{"Bash", "Bash · grep -rn 'session' " + path, "grep -rn 'session'"},
		{"Grep", "Grep · session_runtime_" + strings.Repeat("x", 100), "session_runtime_"},
	} {
		t.Run(test.name+"/"+test.want, func(t *testing.T) {
			tool := NewToolExecutionComponent(test.name, "call", nil, false,
				nativeToolDefinition(test.name, summaryTool{call: test.call}), &toolOutputRenderRequester{}, "/workspace")
			for _, width := range []int{40, 52, 80} {
				lines := tool.Render(width)
				if len(lines) != 2 || !strings.Contains(tui.StripANSI(lines[1]), test.want) {
					t.Fatalf("width %d lost useful detail: %q", width, lines)
				}
			}
			for _, width := range []int{8, 12, 40, 80} {
				for _, line := range tool.Render(width) {
					if tui.VisibleWidth(line) > width {
						t.Fatalf("title overflows width %d: %q", width, line)
					}
				}
			}
			tool.SetExpanded(true)
			got := tui.StripANSI(strings.Join(tool.Render(240), "\n"))
			if !strings.Contains(got, tui.StripANSI(test.call)) {
				t.Fatalf("expanded title lost full details: %q", got)
			}
		})
	}
}

func TestToolActivityBatchesLiveAndReplay(t *testing.T) {
	initTestTheme(t)
	for _, live := range []bool{false, true} {
		name := "replay"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			manager, err := sessionstore.InMemory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			runtime := newCacheStatsRuntime(t, manager)
			t.Cleanup(runtime.Dispose)
			mode := &InteractiveMode{
				session: runtime, ui: tui.NewTUI(newFakeTerminal(80, 24)),
				chat: tui.NewWindowedContainer(), toolComponents: make(map[string]*ToolExecutionComponent),
			}
			render := func() string {
				return tui.StripANSI(strings.Join(mode.chat.RenderLines(80, 0, mode.chat.LineCount(80)), "\n"))
			}
			start := func(name, id string) {
				call := &ai.ToolCall{Name: name, ID: id}
				message := &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "\n"}, &ai.ThinkingContent{}, call}, StopReason: "toolUse"}
				if live {
					mode.handleEvent(engine.MessageStartEvent{Message: message})
					mode.handleEvent(engine.MessageEndEvent{Message: message})
					mode.handleEvent(engine.ToolExecutionStartEvent{ToolName: name, ToolCallID: id})
				} else {
					mode.renderAgentMessage(message)
				}
			}
			finish := func(name, id, output string, failed bool) {
				content := ai.ToolResultContent{&ai.TextContent{Text: output}}
				if live {
					mode.handleEvent(engine.ToolExecutionEndEvent{ToolName: name, ToolCallID: id, IsError: failed, Result: engine.AgentToolResult{Content: content}})
				} else {
					mode.renderToolResult(&ai.ToolResultMessage{ToolName: name, ToolCallID: id, Content: content, IsError: failed})
				}
			}
			start("read", "a")
			finish("read", "a", "beginning\none\ntwo\nthree\nfirst file", false)
			start("Grep", "b")
			if got := render(); !strings.Contains(got, "1 read · 1 search") || !strings.Contains(got, "●  Grep") || strings.Contains(got, "first file") {
				t.Fatalf("active search should remain visible beside the batch: %q", got)
			}
			finish("Grep", "b", "search result", false)
			if got := render(); mode.chat.LineCount(80) != 2 || strings.Contains(got, "search result") {
				t.Fatalf("finished activity did not collapse or invalidate its cached rows: %q", got)
			}
			group := mode.toolActivity
			if !group.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0, Row: 1}) {
				t.Fatal("batch header did not expand")
			}
			if got := render(); !strings.Contains(got, "✓  read") || !strings.Contains(got, "✓  Grep") || strings.Contains(got, "first file") || strings.Contains(got, "search result") {
				t.Fatalf("batch expansion should list actions without dumping their output: %q", got)
			}
			group.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0, Row: 3})
			if got := render(); !strings.Contains(got, "beginning") {
				t.Fatalf("clicking a grouped tool did not expand its output: %q", got)
			}
			for _, width := range []int{12, 40, 88} {
				for _, line := range group.Render(width) {
					if tui.VisibleWidth(line) > width {
						t.Fatalf("batch overflows width %d: %q", width, line)
					}
				}
			}
			group.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0, Row: 1})
			mode.setToolsExpanded(true)
			if got := render(); !strings.Contains(got, "beginning") || !strings.Contains(got, "search result") {
				t.Fatalf("global expansion omitted grouped tools: %q", got)
			}
			mode.setToolsExpanded(false)
			start("Read", "c")
			finish("Read", "c", "permission denied", true)
			if got := render(); !strings.Contains(got, "2 reads · 1 search") || !strings.Contains(got, "×  Read") || !strings.Contains(got, "permission denied") {
				t.Fatalf("collapsed group hid a failure: %q", got)
			}
			for _, boundary := range []string{"bash", "edit", "write", "custom"} {
				previous := mode.toolActivity
				start(boundary, boundary)
				finish(boundary, boundary, "done", false)
				start("read", boundary+"-read")
				finish("read", boundary+"-read", "next file", false)
				if mode.toolActivity == previous {
					t.Fatalf("batch crossed %s", boundary)
				}
			}
			previous := mode.toolActivity
			mode.renderAgentMessage(&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "A new step."}}})
			start("read", "after-text")
			if mode.toolActivity == previous {
				t.Fatal("batch crossed assistant prose")
			}
			previous = mode.toolActivity
			mode.renderAgentMessage(&ai.UserMessage{Content: ai.NewUserText("Another request.")})
			start("read", "after-user")
			if mode.toolActivity == previous {
				t.Fatal("batch crossed a user message")
			}
		})
	}
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
	snap := runner.OpenSnapshot(t, "WP450", "tool-output-previews.json")
	for caseIndex, expected := range fixture.Cases {
		key := wp450FrameKey(ConformanceReplayFrame{ID: expected.ID, Width: expected.Width})
		actual, ok := got[key]
		if !ok {
			t.Errorf("Go preview omitted case %s", key)
			continue
		}
		if snap.Set(actual.Lines, "cases", caseIndex, "lines") {
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
