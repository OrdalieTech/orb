package questions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/tui"
)

func TestQuestionPanelSelectionCustomBackAndDismiss(t *testing.T) {
	request := Request{Questions: []Question{
		{ID: "design", Question: "Choose the design", Options: []Option{{Label: "custom", Description: "A valid option named custom"}, {Label: "Second"}}},
		{ID: "details", Question: "Which details?", MultiSelect: true, Options: []Option{{Label: "Sessions", Description: "Persistent conversations"}, {Label: "Bridge", Preview: "A → B"}}},
	}}
	var result Result
	panel := NewPanel(request, extensions.NewNoopUI().Theme(), func() int { return 32 }, func() {}, func(r Result) { result = r })
	key := func(raw string) {
		t.Helper()
		panel.HandleInput(tui.KeyEvent{Raw: raw})
		for _, width := range []int{32, 80, 120} {
			for _, line := range panel.Render(width) {
				if tui.VisibleWidth(line) > width {
					t.Fatalf("overflow at %d", width)
				}
			}
		}
	}
	if text := strings.Join(panel.Render(80), "\n"); !strings.Contains(text, "A valid option named custom") {
		t.Fatal(text)
	}
	key("\r") // choose option literally named custom
	key(" ")  // Sessions
	key("\x1b[B")
	key(" ") // Bridge
	key("\x1b[A")
	key(" ") // deselect Sessions
	key("3") // custom answer
	key("Extra detail")
	key("\r")
	key("\t") // review
	key("\r")
	raw, _ := json.Marshal(result)
	if err := request.ValidateReply(string(raw)); err != nil {
		t.Fatal(err, string(raw))
	}
	if result.Answers[0].Selected[0] != "custom" || strings.Join(result.Answers[1].Selected, ",") != "Bridge" || result.Answers[1].Custom != "Extra detail" {
		t.Fatalf("%+v", result)
	}
	panel = NewPanel(request, extensions.NewNoopUI().Theme(), func() int { return 32 }, func() {}, func(r Result) { result = r })
	key("\r")
	key("\x1b[1;3D")
	key("\x1b[B")
	key("\r")
	if panel.answers[0].Selected[0] != "Second" || len(panel.answers[0].Selected) != 1 {
		t.Fatal("back-navigation retained an old choice")
	}
	key("\x1b")
	if !result.Cancelled {
		t.Fatal("dismissal invented an answer")
	}
}

func TestNativeQuestionToolUsesRuntimeReplies(t *testing.T) {
	dir := t.TempDir()
	registry := extensions.NewRegistry(dir)
	if err := registry.Register("questions", Extension()); err != nil {
		t.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall(ToolName, map[string]any{"questions": []any{map[string]any{"id": "choice", "question": "Choose", "options": []any{map[string]any{"label": "One"}, map[string]any{"label": "Two"}}}}}, faux.ToolCallOptions{ID: "q-1"})),
		faux.AssistantMessage("Continued after your answer"),
	})
	host, err := agent.NewAgentSessionRuntime(t.Context(), agent.AgentSessionOptions{CWD: dir, AgentDir: dir, Model: provider.GetModel(), StreamFn: provider.StreamSimple, ExtensionRegistry: registry, Resources: &agent.Resources{}, Tools: []string{ToolName}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(context.Background())
	if _, err = host.EnableControl(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Session().Prompt(t.Context(), "Ask me") }()
	deadline := time.Now().Add(3 * time.Second)
	var pending *agent.InputRequest
	for time.Now().Before(deadline) {
		pending = host.Session().PendingInput()
		if pending != nil {
			break
		}
		time.Sleep(time.Millisecond * 5)
	}
	if pending == nil || pending.Presentation == nil || pending.Presentation.Kind != Kind {
		t.Fatal("native tool did not publish a shared question")
	}
	pending.Presentation.Data[0] = '!'
	if !json.Valid(host.Session().PendingInput().Presentation.Data) {
		t.Fatal("snapshot aliases pending input")
	}
	if err = host.Session().ReplyInput(pending.ID, `{"answers":[{"id":"choice","selected":["Invented"]}]}`); err == nil {
		t.Fatal("invalid reply accepted")
	}
	if err = host.Session().ReplyInput(pending.ID, `{"answers":[{"id":"choice","selected":["Two"]}]}`); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer did not resume the native loop")
	}
	if err = host.Session().ReplyInput(pending.ID, `{}`); err == nil {
		t.Fatal("stale reply accepted")
	}
	raw, _ := json.Marshal(host.Session().State().Messages)
	if !strings.Contains(string(raw), `\"selected\":[\"Two\"]`) {
		t.Fatalf("answer missing from transcript: %s", raw)
	}
}

func TestQuestionPanelKeepsOversizedAnswerEditable(t *testing.T) {
	request := Request{Questions: []Question{{ID: "text", Question: "Explain"}}}
	called := false
	panel := NewPanel(request, extensions.NewNoopUI().Theme(), func() int { return 32 }, func() {}, func(Result) { called = true })
	panel.input.SetValue(strings.Repeat("x", 16385))
	panel.HandleInput(tui.KeyEvent{Raw: "\r"})
	if called || panel.completed || panel.message == "" {
		t.Fatal("invalid answer closed the panel")
	}
	panel.input.SetValue("Short answer")
	panel.HandleInput(tui.KeyEvent{Raw: "\r"})
	if !called {
		t.Fatal("corrected answer did not submit")
	}
}

func TestQuestionPanelNumberKeysAndReview(t *testing.T) {
	request := Request{Questions: []Question{
		{ID: "a", Header: "Diagram", Question: "Which diagram?", Options: []Option{{Label: "Architecture", Description: "System structure"}, {Label: "Sequence", Description: "Request flow"}}},
		{ID: "b", Header: "Detail", Question: "How detailed?", Options: []Option{{Label: "Brief"}}},
	}}
	var result Result
	panel := NewPanel(request, extensions.NewNoopUI().Theme(), func() int { return 32 }, func() {}, func(r Result) { result = r })
	text := strings.Join(panel.Render(80), "\n")
	for _, want := range []string{"System structure", "Request flow", "Type your own answer", "Confirm"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	panel.HandleInput(tui.KeyEvent{Raw: "2"})
	panel.HandleInput(tui.KeyEvent{Raw: "1"})
	if len(result.Answers) != 0 {
		t.Fatal("submitted without review")
	}
	if !strings.Contains(strings.Join(panel.Render(80), "\n"), "Sequence") {
		t.Fatal("review lost answer")
	}
	panel.HandleInput(tui.KeyEvent{Raw: "\r"})
	if len(result.Answers) != 2 || result.Answers[0].Selected[0] != "Sequence" {
		t.Fatal(result)
	}
}

func TestQuestionPanelMouseTabsChoicesAndDrag(t *testing.T) {
	request := Request{Questions: []Question{{ID: "one", Header: "Pick", Question: "Choose", Options: []Option{{Label: "First"}, {Label: "Second"}}}, {ID: "two", Header: "More", Question: "More?", Options: []Option{{Label: "Yes"}}}}}
	var result Result
	panel := NewPanel(request, extensions.NewNoopUI().Theme(), func() int { return 40 }, func() {}, func(r Result) { result = r })
	click := func(target int, drag bool) {
		t.Helper()
		panel.Render(80)
		row, column := -1, 2
		if target >= 100 && target < 200 {
			for _, tab := range panel.tabs {
				if tab.index == target-100 {
					row, column = tab.row+1, tab.column+2
				}
			}
		} else {
			for r, value := range panel.hits {
				if value == target {
					row = r + 1
					break
				}
			}
		}
		if row < 0 {
			t.Fatalf("target %d not visible", target)
		}
		panel.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Column: column})
		if drag {
			panel.HandleMouse(tui.MouseEvent{Type: tui.MouseDrag, Row: row, Column: column + 1})
		}
		panel.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Row: row, Column: column})
	}
	click(1, true)
	if panel.index != 0 {
		t.Fatal("drag submitted an answer")
	}
	click(1, false)
	if panel.index != 1 {
		t.Fatal("single click did not select")
	}
	click(100, false)
	if panel.index != 0 {
		t.Fatal("tab did not navigate")
	}
	click(101, false)
	click(0, false)
	click(200, false)
	if len(result.Answers) != 2 || result.Answers[0].Selected[0] != "Second" {
		t.Fatal(result)
	}
}

type composerQuestionUI struct {
	extensions.NoopUI
	t *testing.T
}

func (ui composerQuestionUI) Custom(_ context.Context, _ extensions.CustomFactory, opts *extensions.CustomOptions) (any, bool, error) {
	if opts != nil && opts.Overlay {
		ui.t.Fatal("questions must replace the composer, not open an overlay")
	}
	return Result{Answers: []Answer{{ID: "q", Selected: []string{"Yes"}}}}, true, nil
}
func TestQuestionUsesNonModalComposer(t *testing.T) {
	_, err := Ask(t.Context(), Request{Questions: []Question{{ID: "q", Question: "Continue?", Options: []Option{{Label: "Yes"}}}}}, func(ctx context.Context, _ string, _ []string) (string, error) {
		return extensions.InputOptionsFromContext(ctx).Render(ctx, composerQuestionUI{t: t})
	})
	if err != nil {
		t.Fatal(err)
	}
}

type hoverTheme struct{ extensions.Theme }

func (hoverTheme) BG(_ string, text string) string { return "\x1b[7m" + text + "\x1b[27m" }

func TestQuestionHoverRepaintsWithoutAnsweringOrMovingLayout(t *testing.T) {
	request := Request{Questions: []Question{{ID: "q", Header: "Choice", Question: "Choose", MultiSelect: true, Options: []Option{{Label: "One"}, {Label: "Two"}}}}}
	renders := 0
	panel := NewPanel(request, hoverTheme{extensions.NewNoopUI().Theme()}, func() int { return 40 }, func() { renders++ }, func(Result) { t.Fatal("hover submitted") })
	for _, target := range []int{1, 101, 200} {
		before := strings.Join(panel.Render(80), "\n")
		row, column := -1, 2
		if target == 101 {
			tab := panel.tabs[1]
			row, column = tab.row+1, tab.column+2
		} else {
			for r, value := range panel.hits {
				if value == target {
					row = r + 1
					break
				}
			}
		}
		if row < 0 {
			t.Fatal("target missing")
		}
		event := tui.MouseEvent{Type: tui.MouseMove, Row: row, Column: column}
		if !panel.HandleMouse(event) || panel.hover != target {
			t.Fatal("hover not handled")
		}
		after := strings.Join(panel.Render(80), "\n")
		if before == after || tui.StripANSI(before) != tui.StripANSI(after) {
			t.Fatal("hover must change styling without moving content")
		}
		count := renders
		if panel.HandleMouse(event) || renders != count {
			t.Fatal("stationary hover repaints")
		}
		panel.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: -1})
		if panel.hover != -1 || panel.index != 0 || len(panel.selected) != 0 {
			t.Fatal("hover changed answers or remained after leaving")
		}
	}
}
