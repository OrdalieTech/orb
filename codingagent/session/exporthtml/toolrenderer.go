package exporthtml

import (
	"encoding/json"
	"strings"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// Tool HTML renderer for custom tools in HTML export, ported from upstream
// core/export-html/tool-renderer.ts: invokes the tools' TUI renderers and
// converts the ANSI output to HTML.

// ToolHTMLRendererDeps configures NewToolHTMLRenderer.
type ToolHTMLRendererDeps struct {
	// GetToolDefinition looks up a tool definition by name.
	GetToolDefinition func(name string) *extensions.ToolDefinition
	// Theme styles the renders.
	Theme extensions.Theme
	// CWD is the working directory for the render context.
	CWD string
	// Width is the terminal width for rendering (default 100).
	Width int
}

type toolHTMLRenderer struct {
	deps                     ToolHTMLRendererDeps
	width                    int
	renderedCallComponents   map[string]extensions.Component
	renderedResultComponents map[string]extensions.Component
	renderedStates           map[string]map[string]any
	renderedArgs             map[string]any
}

// NewToolHTMLRenderer creates the live-export ToolHTMLRenderer over the
// registered tool definitions (upstream createToolHtmlRenderer).
func NewToolHTMLRenderer(deps ToolHTMLRendererDeps) ToolHTMLRenderer {
	width := deps.Width
	if width == 0 {
		width = 100
	}
	return &toolHTMLRenderer{
		deps:                     deps,
		width:                    width,
		renderedCallComponents:   make(map[string]extensions.Component),
		renderedResultComponents: make(map[string]extensions.Component),
		renderedStates:           make(map[string]map[string]any),
		renderedArgs:             make(map[string]any),
	}
}

func (renderer *toolHTMLRenderer) state(toolCallID string) map[string]any {
	state, exists := renderer.renderedStates[toolCallID]
	if !exists {
		state = make(map[string]any)
		renderer.renderedStates[toolCallID] = state
	}
	return state
}

func (renderer *toolHTMLRenderer) renderContext(toolCallID string, lastComponent extensions.Component, expanded, isPartial, isError bool) extensions.ToolRenderContext {
	return extensions.ToolRenderContext{
		Args:             renderer.renderedArgs[toolCallID],
		ToolCallID:       toolCallID,
		Invalidate:       func() {},
		LastComponent:    lastComponent,
		State:            renderer.state(toolCallID),
		CWD:              renderer.deps.CWD,
		ExecutionStarted: true,
		ArgsComplete:     true,
		IsPartial:        isPartial,
		Expanded:         expanded,
		ShowImages:       false,
		IsError:          isError,
	}
}

func (renderer *toolHTMLRenderer) RenderCall(toolCallID, toolName string, arguments any) (rendered *string) {
	// On renderer failure fall back to structured result rendering.
	defer func() {
		if recover() != nil {
			rendered = nil
		}
	}()
	renderer.renderedArgs[toolCallID] = arguments
	if renderer.deps.GetToolDefinition == nil {
		return nil
	}
	definition := renderer.deps.GetToolDefinition(toolName)
	if definition == nil || definition.RenderCall == nil {
		return nil
	}
	component := definition.RenderCall(
		arguments,
		renderer.deps.Theme,
		renderer.renderContext(toolCallID, renderer.renderedCallComponents[toolCallID], false, true, false),
	)
	renderer.renderedCallComponents[toolCallID] = component
	if component == nil {
		return nil
	}
	html := AnsiLinesToHTML(component.Render(renderer.width))
	return &html
}

func (renderer *toolHTMLRenderer) RenderResult(toolCallID, toolName string, content, details any, isError bool) (rendered *ToolHTMLRenderResult) {
	// On renderer failure fall back to structured result rendering.
	defer func() {
		if recover() != nil {
			rendered = nil
		}
	}()
	if renderer.deps.GetToolDefinition == nil {
		return nil
	}
	definition := renderer.deps.GetToolDefinition(toolName)
	if definition == nil || definition.RenderResult == nil {
		return nil
	}
	result := agent.AgentToolResult{Content: decodeToolResultContent(content), Details: details}

	collapsedComponent := definition.RenderResult(
		result,
		extensions.ToolRenderResultOptions{Expanded: false, IsPartial: false},
		renderer.deps.Theme,
		renderer.renderContext(toolCallID, renderer.renderedResultComponents[toolCallID], false, false, isError),
	)
	renderer.renderedResultComponents[toolCallID] = collapsedComponent
	if collapsedComponent == nil {
		// Upstream's null component throws inside try/catch -> undefined.
		return nil
	}
	collapsed := AnsiLinesToHTML(trimRenderedResultLines(collapsedComponent.Render(renderer.width)))

	expandedComponent := definition.RenderResult(
		result,
		extensions.ToolRenderResultOptions{Expanded: true, IsPartial: false},
		renderer.deps.Theme,
		renderer.renderContext(toolCallID, renderer.renderedResultComponents[toolCallID], true, false, isError),
	)
	renderer.renderedResultComponents[toolCallID] = expandedComponent
	if expandedComponent == nil {
		return nil
	}
	expanded := AnsiLinesToHTML(trimRenderedResultLines(expandedComponent.Render(renderer.width)))

	rendered = &ToolHTMLRenderResult{Expanded: &expanded}
	if collapsed != "" && collapsed != expanded {
		rendered.Collapsed = &collapsed
	}
	return rendered
}

// decodeToolResultContent converts the session-storage content value (a
// decoded JSON array) into typed tool-result blocks for the TUI renderers.
func decodeToolResultContent(content any) ai.ToolResultContent {
	if content == nil {
		return ai.ToolResultContent{}
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return ai.ToolResultContent{}
	}
	var decoded ai.ToolResultContent
	if decoded.UnmarshalJSON(encoded) != nil {
		return ai.ToolResultContent{}
	}
	return decoded
}

func isBlankRenderedLine(line string) bool {
	return strings.TrimSpace(stripSGRSequences(line)) == ""
}

func stripSGRSequences(line string) string {
	var stripped strings.Builder
	cursor := 0
	for {
		matchStart, matchEnd, _, found := nextSGRSequence(line, cursor)
		if !found {
			stripped.WriteString(line[cursor:])
			return stripped.String()
		}
		stripped.WriteString(line[cursor:matchStart])
		cursor = matchEnd
	}
}

func trimRenderedResultLines(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && isBlankRenderedLine(lines[start]) {
		start++
	}
	for end > start && isBlankRenderedLine(lines[end-1]) {
		end--
	}
	return lines[start:end]
}
