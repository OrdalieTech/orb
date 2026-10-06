package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

type safetyComponent struct {
	renders, invalidates int
	fail                 string
}

func (s *safetyComponent) Render(int) []string {
	s.renders++
	if s.fail == "render" {
		panic("secret panic")
	}
	return []string{"healthy component"}
}
func (s *safetyComponent) Invalidate() {
	s.invalidates++
	if s.fail == "invalidate" {
		panic("secret panic")
	}
}

func TestToolRendererSafety(t *testing.T) {
	initTestTheme(t)
	for _, phase := range []string{"call", "result"} {
		for _, failure := range []string{"factory", "render", "invalidate"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				calls := 0
				component := &safetyComponent{}
				factory := func() extensions.Component {
					calls++
					if failure == "factory" {
						panic("secret panic")
					}
					return component
				}
				def := &extensions.ToolDefinition{}
				if phase == "call" {
					def.RenderCall = func(any, extensions.Theme, extensions.ToolRenderContext) extensions.Component { return factory() }
				} else {
					def.RenderResult = func(engine.AgentToolResult, extensions.ToolRenderResultOptions, extensions.Theme, extensions.ToolRenderContext) extensions.Component {
						return factory()
					}
				}
				tool := NewToolExecutionComponent("native_tool", "id", nil, false, def, nil, "/")
				content := ai.ToolResultContent{&ai.TextContent{Text: "raw result"}}
				tool.UpdateResult(content, false, "details", false)
				tool.SetExpanded(true)
				tool.Render(100)
				component.fail = failure
				if failure == "invalidate" {
					tool.Invalidate()
				} else {
					tool.Render(100)
				}
				before, renders, invalidates := calls, component.renders, component.invalidates
				for range 3 {
					tool.UpdateArgs(map[string]any{"stream": "next"})
					tool.UpdateResult(content, false, "details", false)
					tool.Invalidate()
					text := tui.StripANSI(strings.Join(tool.Render(100), "\n"))
					if !strings.Contains(text, "native_tool") || !strings.Contains(text, "renderer failed") || !strings.Contains(text, "raw result") || strings.Contains(text, "secret panic") {
						t.Fatalf("bad fallback: %q", text)
					}
				}
				if calls != before || component.renders != renders || component.invalidates != invalidates {
					t.Fatal("poisoned renderer retried")
				}
				if tool.result.Content[0] != content[0] || tool.result.Details != "details" {
					t.Fatal("raw result changed")
				}
				sibling := NewToolExecutionComponent("sibling", "other", nil, false, &extensions.ToolDefinition{RenderCall: func(any, extensions.Theme, extensions.ToolRenderContext) extensions.Component {
					return &safetyComponent{}
				}}, nil, "/")
				if !strings.Contains(strings.Join(sibling.Render(100), "\n"), "healthy component") {
					t.Fatal("sibling affected")
				}
			})
		}
	}
}

func TestToolRendererLastComponentAndStreamingPanic(t *testing.T) {
	initTestTheme(t)
	for _, result := range []bool{false, true} {
		component := &safetyComponent{}
		calls := 0
		factory := func(ctx extensions.ToolRenderContext) extensions.Component {
			calls++
			if calls == 1 {
				if ctx.LastComponent != nil {
					t.Fatal("unexpected initial component")
				}
				return component
			}
			if ctx.LastComponent != component {
				t.Fatalf("LastComponent wrapped or lost: %T", ctx.LastComponent)
			}
			if calls == 2 {
				return nil
			}
			panic("secret streaming arguments")
		}
		def := &extensions.ToolDefinition{}
		if result {
			def.RenderResult = func(_ engine.AgentToolResult, _ extensions.ToolRenderResultOptions, _ extensions.Theme, ctx extensions.ToolRenderContext) extensions.Component {
				return factory(ctx)
			}
		} else {
			def.RenderCall = func(_ any, _ extensions.Theme, ctx extensions.ToolRenderContext) extensions.Component {
				return factory(ctx)
			}
		}
		tool := NewToolExecutionComponent("native", "id", nil, false, def, nil, "/")
		if result {
			tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "raw"}}, false, nil, true)
		}
		if !strings.Contains(strings.Join(tool.Render(80), "\n"), "healthy component") {
			t.Fatal("normal render changed")
		}
		tool.UpdateArgs(nil)
		if strings.Contains(strings.Join(tool.Render(80), "\n"), "healthy component") {
			t.Fatal("nil return must omit component")
		}
		tool.UpdateArgs(map[string]any{"stream": true})
		tool.UpdateArgs(nil)
		if calls != 3 {
			t.Fatalf("factory retried: %d", calls)
		}
		if !strings.Contains(strings.Join(tool.Render(80), "\n"), "renderer failed") {
			t.Fatal("missing diagnostic")
		}
	}
}
