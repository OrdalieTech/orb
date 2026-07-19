package exporthtml

import (
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

func TestAnsiToHTMLConversions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "hello", want: "hello"},
		{name: "escapes html", input: `<b> & "quote" 'tick'`, want: "&lt;b&gt; &amp; &quot;quote&quot; &#039;tick&#039;"},
		{name: "standard fg", input: "\x1b[31mred\x1b[0m", want: `<span style="color:#800000">red</span>`},
		{name: "bright fg", input: "\x1b[92mgreen\x1b[0m", want: `<span style="color:#00ff00">green</span>`},
		{name: "bg", input: "\x1b[44mblue\x1b[0m", want: `<span style="background-color:#000080">blue</span>`},
		{name: "256 cube", input: "\x1b[38;5;196mx\x1b[0m", want: `<span style="color:#ff0000">x</span>`},
		{name: "256 gray", input: "\x1b[38;5;244mx\x1b[0m", want: `<span style="color:#808080">x</span>`},
		{name: "rgb", input: "\x1b[38;2;1;2;3mx\x1b[0m", want: `<span style="color:rgb(1,2,3)">x</span>`},
		{name: "styles", input: "\x1b[1;2;3;4mx\x1b[0m", want: `<span style="font-weight:bold;opacity:0.6;font-style:italic;text-decoration:underline">x</span>`},
		{name: "style resets", input: "\x1b[1mx\x1b[22my", want: `<span style="font-weight:bold">x</span>y`},
		{name: "default fg reset", input: "\x1b[31;4mx\x1b[39my", want: `<span style="color:#800000;text-decoration:underline">x</span><span style="text-decoration:underline">y</span>`},
		{name: "empty params reset", input: "\x1b[31mred\x1b[mplain", want: `<span style="color:#800000">red</span>plain`},
		{name: "unknown code ignored", input: "\x1b[95;999mx\x1b[0m", want: `<span style="color:#ff00ff">x</span>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := AnsiToHTML(test.input); got != test.want {
				t.Fatalf("AnsiToHTML(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestAnsiLinesToHTMLDoesNotInsertWhitespaceBetweenLines(t *testing.T) {
	t.Parallel()
	if got := AnsiLinesToHTML([]string{"one", "two"}); got != `<div class="ansi-line">one</div><div class="ansi-line">two</div>` {
		t.Fatalf("AnsiLinesToHTML = %q", got)
	}
	if got := AnsiLinesToHTML([]string{""}); got != `<div class="ansi-line">&nbsp;</div>` {
		t.Fatalf("empty line = %q", got)
	}
}

type staticComponent []string

func (component staticComponent) Render(int) []string { return component }

func TestToolHTMLRendererTrimsTUISpacingLines(t *testing.T) {
	t.Parallel()
	tool := &extensions.ToolDefinition{
		Name: "custom", Label: "custom", Description: "custom",
		RenderResult: func(agent.AgentToolResult, extensions.ToolRenderResultOptions, extensions.Theme, extensions.ToolRenderContext) extensions.Component {
			return staticComponent{"", "\x1b[31mone\x1b[0m", "two", ""}
		},
	}
	renderer := NewToolHTMLRenderer(ToolHTMLRendererDeps{
		GetToolDefinition: func(string) *extensions.ToolDefinition { return tool },
		CWD:               "/tmp",
	})
	result := renderer.RenderResult("id", "custom", []any{}, nil, false)
	if result == nil || result.Expanded == nil {
		t.Fatalf("result = %#v", result)
	}
	want := `<div class="ansi-line"><span style="color:#800000">one</span></div><div class="ansi-line">two</div>`
	if *result.Expanded != want {
		t.Fatalf("expanded = %q, want %q", *result.Expanded, want)
	}
	// Collapsed render equals expanded, so it is omitted (upstream shape).
	if result.Collapsed != nil {
		t.Fatalf("collapsed = %q, want omitted", *result.Collapsed)
	}
}

func TestToolHTMLRendererRendersCallsAndRecoversFromPanics(t *testing.T) {
	t.Parallel()
	rendered := 0
	tool := &extensions.ToolDefinition{
		Name: "custom",
		RenderCall: func(args any, _ extensions.Theme, context extensions.ToolRenderContext) extensions.Component {
			rendered++
			if context.ToolCallID != "call-1" || !context.ArgsComplete || !context.ExecutionStarted {
				t.Fatalf("render context = %+v", context)
			}
			if arguments, ok := args.(map[string]any); !ok || arguments["value"] != "x" {
				t.Fatalf("arguments = %#v", args)
			}
			return staticComponent{"header"}
		},
		RenderResult: func(result agent.AgentToolResult, _ extensions.ToolRenderResultOptions, _ extensions.Theme, context extensions.ToolRenderContext) extensions.Component {
			if !context.IsError {
				t.Fatal("isError not propagated")
			}
			if len(result.Content) != 1 {
				t.Fatalf("content = %#v", result.Content)
			}
			if text, ok := result.Content[0].(*ai.TextContent); !ok || text.Text != "output" {
				t.Fatalf("content block = %#v", result.Content[0])
			}
			panic("renderer exploded")
		},
	}
	renderer := NewToolHTMLRenderer(ToolHTMLRendererDeps{
		GetToolDefinition: func(name string) *extensions.ToolDefinition {
			if name == "custom" {
				return tool
			}
			return nil
		},
		CWD: "/tmp",
	})
	call := renderer.RenderCall("call-1", "custom", map[string]any{"value": "x"})
	if call == nil || *call != `<div class="ansi-line">header</div>` {
		t.Fatalf("call html = %#v", call)
	}
	if rendered != 1 {
		t.Fatalf("render call invocations = %d", rendered)
	}
	// Panicking renderResult falls back to structured rendering (nil).
	if result := renderer.RenderResult("call-1", "custom", []any{map[string]any{"type": "text", "text": "output"}}, nil, true); result != nil {
		t.Fatalf("panicking renderer result = %#v", result)
	}
	// Unknown tools have no custom renderer.
	if call := renderer.RenderCall("call-2", "unknown", nil); call != nil {
		t.Fatalf("unknown tool call = %#v", call)
	}
}
