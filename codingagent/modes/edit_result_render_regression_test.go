package modes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/codingagent/tools"
	"github.com/OrdalieTech/orb/tui"
)

// Regression for the v0.4.14 report where a SUCCESSFUL edit rendered the
// tool's "Could not find the exact text" mismatch error: every updateDisplay
// re-computed the live preview against the CURRENT file, and once the edit had
// been applied the oldText no longer matched, so the stale recompute replaced
// (live path) or stacked above (history replay path) the recorded result diff.

const editRegressionOld = "Hello from Orb.\nThis is a test file.\nLine three.\n"
const editRegressionNew = "Hello from Orb.\nThis file was modified successfully.\nLine three.\n"

func newEditRegressionComponent(t *testing.T, cwd string) *ToolExecutionComponent {
	t.Helper()
	initTestTheme(t)
	return NewToolExecutionComponent(
		"edit",
		"call-edit-regression",
		map[string]any{"path": "test-edit.txt"},
		false,
		nativeToolDefinition("edit", tools.NewEditTool(cwd, nil)),
		tui.NewTUI(newFakeTerminal(100, 40)),
		cwd,
	)
}

func editRegressionArgs() map[string]any {
	return map[string]any{
		"path": "test-edit.txt",
		"edits": []map[string]any{{
			"oldText": "This is a test file.",
			"newText": "This file was modified successfully.",
		}},
	}
}

func editRegressionRender(component *ToolExecutionComponent) string {
	return f12TerminalCSI.ReplaceAllString(strings.Join(component.Render(88), "\n"), "")
}

func editRegressionResultDetails(t *testing.T) tools.EditToolDetails {
	t.Helper()
	diff := tools.GenerateDiffString(editRegressionOld, editRegressionNew, 4)
	if diff.Diff == "" {
		t.Fatal("empty fixture diff")
	}
	return tools.EditToolDetails{Diff: diff.Diff, FirstChangedLine: diff.FirstChangedLine}
}

func assertEditSuccessRender(t *testing.T, rendered, stage string) {
	t.Helper()
	if strings.Contains(rendered, "Could not find the exact text") {
		t.Fatalf("%s: stale preview mismatch error in successful edit render:\n%s", stage, rendered)
	}
	if !strings.Contains(rendered, "2 - This is a test file.") ||
		!strings.Contains(rendered, "2 + This file was modified successfully.") {
		t.Fatalf("%s: success diff missing from render:\n%s", stage, rendered)
	}
	if strings.Count(rendered, "2 - This is a test file.") != 1 {
		t.Fatalf("%s: diff rendered more than once:\n%s", stage, rendered)
	}
}

// Live path: streaming args, execution start, then the edit mutates the file
// before the success end event. The recorded result diff must survive every
// later re-composition; the preview must never be recomputed from disk.
func TestEditToolSuccessRenderSurvivesFileMutation(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "test-edit.txt")
	if err := os.WriteFile(path, []byte(editRegressionOld), 0o644); err != nil {
		t.Fatal(err)
	}
	component := newEditRegressionComponent(t, cwd)
	component.UpdateArgs(editRegressionArgs())
	component.SetArgsComplete()
	component.MarkExecutionStarted()
	assertEditSuccessRender(t, editRegressionRender(component), "pre-execution preview")

	// Execution applies the edit: oldText no longer exists on disk.
	if err := os.WriteFile(path, []byte(editRegressionNew), 0o644); err != nil {
		t.Fatal(err)
	}
	component.UpdateResult(
		ai.ToolResultContent{&ai.TextContent{Text: "Successfully replaced 1 block(s) in test-edit.txt."}},
		false,
		editRegressionResultDetails(t),
		false,
	)
	assertEditSuccessRender(t, editRegressionRender(component), "after success end event")

	// Any later re-composition (expand toggle, invalidate) must not
	// resurrect the preview.
	component.SetExpanded(true)
	component.Invalidate()
	assertEditSuccessRender(t, editRegressionRender(component), "after re-composition")
}

// History replay path (session resume, compaction rebuild): the component is
// rebuilt AFTER the edit was applied, so the first preview compute already
// fails against the post-edit file. The final render must show only the
// recorded diff, not the transient mismatch error stacked above it.
func TestEditToolReplayRenderDropsStalePreviewError(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "test-edit.txt")
	if err := os.WriteFile(path, []byte(editRegressionNew), 0o644); err != nil {
		t.Fatal(err)
	}
	component := newEditRegressionComponent(t, cwd)
	component.UpdateArgs(editRegressionArgs())
	component.SetArgsComplete()
	component.UpdateResult(
		ai.ToolResultContent{&ai.TextContent{Text: "Successfully replaced 1 block(s) in test-edit.txt."}},
		false,
		editRegressionResultDetails(t),
		false,
	)
	assertEditSuccessRender(t, editRegressionRender(component), "replayed result")
}

// A genuinely failed edit still renders its error, exactly once, with no diff.
func TestEditToolFailureRenderShowsErrorOnce(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "test-edit.txt")
	if err := os.WriteFile(path, []byte("unrelated content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	component := newEditRegressionComponent(t, cwd)
	component.UpdateArgs(editRegressionArgs())
	component.SetArgsComplete()
	failure := "Could not find the exact text in test-edit.txt. The old text must match exactly including all whitespace and newlines."
	component.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: failure}}, true, nil, false)

	rendered := editRegressionRender(component)
	if strings.Count(rendered, "Could not find the exact text") != 1 {
		t.Fatalf("failed edit must render its error exactly once:\n%s", rendered)
	}
	if strings.Contains(rendered, "2 + This file was modified successfully.") {
		t.Fatalf("failed edit must not render a diff:\n%s", rendered)
	}
}
