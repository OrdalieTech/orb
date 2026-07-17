package tools

import (
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

// PlainTextRenderer is the tool-rendering seam used until the Phase 4 TUI
// replaces these strings with components.
type PlainTextRenderer interface {
	RenderCall(args any) string
	RenderResult(result agent.AgentToolResult) string
}

func (tool *readTool) RenderCall(args any) string {
	object := renderArgs(args)
	text := "read " + renderPath(object)
	offset, offsetErr := optionalNumber(object, "offset")
	limit, limitErr := optionalNumber(object, "limit")
	if offsetErr != nil || limitErr != nil || offset == nil && limit == nil {
		return text
	}
	start := 1.0
	if offset != nil {
		start = *offset
	}
	text += ":" + formatJSNumber(start)
	if limit != nil {
		end := start + *limit - 1
		if end != 0 {
			text += "-" + formatJSNumber(end)
		}
	}
	return text
}

func (*readTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

func (*writeTool) RenderCall(args any) string {
	return "write " + renderPath(renderArgs(args))
}

func (*writeTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

func (*editTool) RenderCall(args any) string {
	return "edit " + renderPath(renderArgs(args))
}

func (*editTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

func (*lsTool) RenderCall(args any) string {
	object := renderArgs(args)
	path := renderPath(object)
	if path == "" {
		path = "."
	}
	text := "ls " + path
	if limit, err := optionalNumber(object, "limit"); err == nil && limit != nil {
		text += " (limit " + formatJSNumber(*limit) + ")"
	}
	return text
}

func (*lsTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

func renderArgs(args any) map[string]any {
	object, err := toolParams(args)
	if err != nil {
		return map[string]any{}
	}
	return object
}

func renderPath(args map[string]any) string {
	for _, name := range []string{"path", "file_path"} {
		if value, ok := args[name].(string); ok {
			return value
		}
	}
	return ""
}

func renderTextResult(result agent.AgentToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if text, ok := block.(*ai.TextContent); ok && text != nil {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

var (
	_ PlainTextRenderer = (*readTool)(nil)
	_ PlainTextRenderer = (*writeTool)(nil)
	_ PlainTextRenderer = (*editTool)(nil)
	_ PlainTextRenderer = (*lsTool)(nil)
)
