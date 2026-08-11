package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/tui"
)

func TestStartupWarningsCompaction(t *testing.T) {
	warnings := newStartupWarnings([]StartupDiagnostic{
		{
			Kind:    StartupDiagnosticExtension,
			Path:    "/home/user/.pi/agent/npm/node_modules/@quintinshaw/pi-dynamic-workflows/extensions/workflow.ts",
			Message: "Failed to load extension: Cannot find package 'typebox' imported from /home/user/.pi/agent/npm/node_modules/@quintinshaw/pi-dynamic-workflows/src/agent.ts",
		},
		{Kind: StartupDiagnosticCollision, Path: "/home/user/skills/but.md", Message: `"but"`},
		{Kind: StartupDiagnosticCollision, Path: "/home/user/skills/ponytail.md", Message: `"ponytail"`},
		{Kind: StartupDiagnosticOther, Message: `some other diagnostic`},
	})
	want := []string{
		`extension workflow.ts: Cannot find package 'typebox'`,
		`some other diagnostic`,
		`name collisions: "but", "ponytail"`,
	}
	if len(warnings.lines) != len(want) {
		t.Fatalf("lines = %q, want %q", warnings.lines, want)
	}
	for index, line := range want {
		if warnings.lines[index] != line {
			t.Errorf("line %d = %q, want %q", index, warnings.lines[index], line)
		}
	}
}

func TestStartupWarningsRenderTruncatesInsteadOfWrapping(t *testing.T) {
	warnings := newStartupWarnings([]StartupDiagnostic{
		{
			Kind:    StartupDiagnosticExtension,
			Path:    "/very/long/path/to/some/extension.ts",
			Message: "Failed to load extension: Cannot find package 'typebox' imported from /another/very/long/path",
		},
		{Kind: StartupDiagnosticCollision, Message: `"but"`},
	})
	const width = 40
	lines := warnings.Render(width)
	if len(lines) != len(warnings.lines) {
		t.Fatalf("rendered %d lines for %d warnings; truncation must never wrap", len(lines), len(warnings.lines))
	}
	for index, line := range lines {
		if visible := tui.VisibleWidth(line); visible > width {
			t.Errorf("line %d visible width %d exceeds %d: %q", index, visible, width, line)
		}
		if !strings.Contains(line, "┃") {
			t.Errorf("line %d missing the warning gutter bar: %q", index, line)
		}
	}
	if !strings.Contains(lines[0], "…") {
		t.Errorf("long line was not truncated with an ellipsis: %q", lines[0])
	}
}
