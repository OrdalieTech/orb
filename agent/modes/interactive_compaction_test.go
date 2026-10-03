package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

func TestAutoCompactionRestoresWorkingIndicator(t *testing.T) {
	for _, reason := range []string{"threshold", "overflow"} {
		t.Run(reason, func(t *testing.T) {
			mode := compactionIndicatorMode(t)
			message := "Still working..."
			mode.interactiveUI.SetWorkingMessage(&message)
			mode.interactiveUI.SetWorkingIndicator(&extensions.WorkingIndicatorOptions{Frames: []string{"*"}})
			mode.handleEvent(engine.AgentStartEvent{})
			mode.handleEvent(agent.CompactionStartEvent{Reason: reason})
			if status, ok := mode.statusIndicator.(*StatusIndicator); !ok || status.Kind != StatusCompaction {
				t.Fatalf("compaction status = %#v", mode.statusIndicator)
			}
			// Continuation after compaction starts another turn, not another agent run.
			mode.handleEvent(agent.CompactionEndEvent{Reason: reason})
			mode.handleEvent(engine.ToolExecutionStartEvent{ToolName: "bash", ToolCallID: "continued", Args: map[string]any{"command": "printf done"}})
			if rendered := selectorANSI.ReplaceAllString(strings.Join(mode.status.Render(36), "\n"), ""); !strings.Contains(rendered, "* Still working...") {
				t.Fatalf("active compacted run lost indicator: %q", rendered)
			}
			lane := compactStatus{Component: mode.status, Inline: mode.statusInEditor}
			visible := append(lane.Render(36), mode.editor.Render(36)...)
			if rendered := strings.Join(visible, "\n"); !strings.Contains(rendered, "Still working...") {
				t.Fatalf("status lane and editor lost working indicator: %q", rendered)
			}
			mode.interactiveUI.SetWorkingMessage(nil)
			lane.Render(36)
			if rendered := strings.Join(mode.editor.Render(36), "\n"); !strings.Contains(rendered, "Working...") {
				t.Fatalf("stock working indicator missing from editor border: %q", rendered)
			}
			mode.handleEvent(agent.AgentSettledEvent{})
			if rendered := strings.Join(mode.status.Render(36), "\n"); strings.Contains(strings.ToLower(rendered), "working") {
				t.Fatalf("settled run retained indicator: %q", rendered)
			}
		})
	}
}

func TestCompactionIndicatorRespectsHiddenAndIdle(t *testing.T) {
	for _, test := range []struct {
		name, reason    string
		active, visible bool
	}{
		{"hidden", "threshold", true, false},
		{"idle", "threshold", false, true},
		{"manual", "manual", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode := compactionIndicatorMode(t)
			mode.streaming = test.active
			mode.interactiveUI.SetWorkingVisible(test.visible)
			mode.handleEvent(agent.CompactionStartEvent{Reason: test.reason})
			mode.handleEvent(agent.CompactionEndEvent{Reason: test.reason, Aborted: true})
			if rendered := strings.ToLower(strings.Join(mode.status.Render(36), "\n")); strings.Contains(rendered, "working") || strings.Contains(rendered, "compacting") {
				t.Fatalf("unexpected status: %q", rendered)
			}
		})
	}
}

func compactionIndicatorMode(t *testing.T) *InteractiveMode {
	t.Helper()
	mode := newPendingToolMode(t, nil)
	if _, err := mode.session.Manager().AppendSessionInfo("Named session"); err != nil {
		t.Fatal(err)
	}
	mode.status = &tui.Container{}
	mode.editorContainer = &tui.Container{}
	mode.interactiveUI = NewInteractiveUI(mode)
	mode.editor = NewCustomEditor(mode.ui, theme.EditorTheme(), NewAppKeybindings(nil))
	mode.editor.setTopBorderDecorator(func(width int, base string, border tui.StyleFunc) string {
		return mode.editorTopBorder(width, base, border).Line
	})
	mode.editorContainer.AddChild(mode.editor)
	t.Cleanup(func() { mode.clearStatusIndicator(); mode.session.Dispose() })
	return mode
}
