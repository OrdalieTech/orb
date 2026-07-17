package tools

import (
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

func TestWaveOneToolsExposePlainTextRenderHooks(t *testing.T) {
	dir := t.TempDir()
	for _, testCase := range []struct {
		name string
		tool agent.AgentTool
		args any
		want string
	}{
		{name: "read", tool: NewReadTool(dir, nil), args: map[string]any{"path": "a.go", "offset": 3, "limit": 4}, want: "read a.go:3-6"},
		{name: "write", tool: NewWriteTool(dir, nil), args: map[string]any{"path": "a.go"}, want: "write a.go"},
		{name: "edit", tool: NewEditTool(dir, nil), args: map[string]any{"path": "a.go"}, want: "edit a.go"},
		{name: "ls default", tool: NewLsTool(dir, nil), args: map[string]any{}, want: "ls ."},
		{name: "ls limit", tool: NewLsTool(dir, nil), args: map[string]any{"path": "src", "limit": 12.5}, want: "ls src (limit 12.5)"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, ok := testCase.tool.(PlainTextRenderer)
			if !ok {
				t.Fatalf("%T does not implement PlainTextRenderer", testCase.tool)
			}
			if got := renderer.RenderCall(testCase.args); got != testCase.want {
				t.Fatalf("RenderCall() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestPlainTextRenderResultJoinsTextBlocks(t *testing.T) {
	renderer := NewReadTool(t.TempDir(), nil).(PlainTextRenderer)
	result := agent.AgentToolResult{Content: ai.ToolResultContent{
		&ai.TextContent{Text: "first"},
		&ai.ImageContent{Data: "ignored", MimeType: "image/png"},
		&ai.TextContent{Text: "second"},
	}}
	if got, want := renderer.RenderResult(result), "first\nsecond"; got != want {
		t.Fatalf("RenderResult() = %q, want %q", got, want)
	}
}
