package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/tui"
)

func withCapabilities(t *testing.T, capabilities tui.TerminalCapabilities) {
	t.Helper()
	tui.SetCapabilities(capabilities)
	t.Cleanup(tui.ResetCapabilitiesCache)
}

func TestWaveOneToolsExposePlainTextRenderHooks(t *testing.T) {
	withCapabilities(t, tui.TerminalCapabilities{})
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

func TestToolHeadersShortenHomePaths(t *testing.T) {
	withCapabilities(t, tui.TerminalCapabilities{})
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	dir := t.TempDir()
	inHome := filepath.Join(home, "project", "a.go")
	for _, testCase := range []struct {
		name string
		tool agent.AgentTool
		args any
		want string
	}{
		{name: "read", tool: NewReadTool(dir, nil), args: map[string]any{"path": inHome}, want: "read ~/project/a.go"},
		{name: "write", tool: NewWriteTool(dir, nil), args: map[string]any{"path": inHome}, want: "write ~/project/a.go"},
		{name: "edit", tool: NewEditTool(dir, nil), args: map[string]any{"path": inHome}, want: "edit ~/project/a.go"},
		{name: "ls", tool: NewLsTool(dir, nil), args: map[string]any{"path": filepath.Join(home, "project")}, want: "ls ~/project"},
		{name: "find", tool: NewFindTool(dir, nil), args: map[string]any{"pattern": "*.go", "path": filepath.Join(home, "project")}, want: "find *.go in ~/project"},
		{name: "grep", tool: NewGrepTool(dir, nil), args: map[string]any{"pattern": "x", "path": filepath.Join(home, "project")}, want: "grep /x/ in ~/project"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			renderer := testCase.tool.(PlainTextRenderer)
			if got := renderer.RenderCall(testCase.args); got != testCase.want {
				t.Fatalf("RenderCall() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestToolHeadersEmitOSC8HyperlinksWhenSupported(t *testing.T) {
	withCapabilities(t, tui.TerminalCapabilities{Hyperlinks: true})
	dir := t.TempDir()
	renderer := NewReadTool(dir, nil).(PlainTextRenderer)
	spaced := filepath.Join(dir, "a b.go")
	want := "read " + tui.Hyperlink(ShortenPath(spaced), pathToFileURL(spaced))
	if got := renderer.RenderCall(map[string]any{"path": spaced}); got != want {
		t.Fatalf("RenderCall() = %q, want %q", got, want)
	}
	// pathToFileURL percent-encodes the WHATWG path set plus "%", like Node.
	if encoded := pathToFileURL("/tmp/a b#c%d.go"); encoded != "file:///tmp/a%20b%23c%25d.go" {
		t.Fatalf("pathToFileURL = %q", encoded)
	}
	if encoded := pathToFileURL("/tmp/café.go"); encoded != "file:///tmp/caf%C3%A9.go" {
		t.Fatalf("pathToFileURL unicode = %q", encoded)
	}
	// Relative paths resolve against the tool cwd for the link target only.
	relative := renderer.RenderCall(map[string]any{"path": "a.go"})
	if want := "read " + tui.Hyperlink("a.go", pathToFileURL(filepath.Join(dir, "a.go"))); relative != want {
		t.Fatalf("relative RenderCall() = %q, want %q", relative, want)
	}
	// find/grep headers shorten but never hyperlink (upstream render-utils usage).
	find := NewFindTool(dir, nil).(PlainTextRenderer)
	if got := find.RenderCall(map[string]any{"pattern": "*.go", "path": dir}); got != "find *.go in "+ShortenPath(dir) {
		t.Fatalf("find RenderCall() = %q", got)
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
