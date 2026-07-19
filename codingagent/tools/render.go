package tools

import (
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/tui"
)

// PlainTextRenderer is the tool-rendering seam used until the Phase 4 TUI
// replaces these strings with components.
type PlainTextRenderer interface {
	RenderCall(args any) string
	RenderResult(result agent.AgentToolResult) string
}

// ShortenPath abbreviates a home-relative path with "~" for tool headers,
// matching upstream render-utils shortenPath.
func ShortenPath(path string) string {
	home, err := toolUserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// linkPath wraps the display text in an OSC 8 file:// hyperlink when the
// terminal supports hyperlinks, matching upstream render-utils linkPath.
func linkPath(displayText, rawPath, cwd string) string {
	if !tui.GetCapabilities().Hyperlinks {
		return displayText
	}
	absolutePath := rawPath
	if expanded, err := expandPath(rawPath, false, false); err == nil {
		absolutePath = expanded
	}
	if !filepath.IsAbs(absolutePath) {
		absolutePath = filepath.Join(cwd, absolutePath)
	}
	return tui.Hyperlink(displayText, pathToFileURL(filepath.Clean(absolutePath)))
}

// pathToFileURL percent-encodes an absolute path as a file:// URL the way
// Node's url.pathToFileURL does (WHATWG path percent-encode set plus "%").
func pathToFileURL(path string) string {
	var encoded strings.Builder
	encoded.WriteString("file://")
	for _, unit := range []byte(filepath.ToSlash(path)) {
		switch {
		case unit < 0x20 || unit == 0x7f || unit >= 0x80,
			unit == ' ', unit == '"', unit == '#', unit == '<', unit == '>',
			unit == '?', unit == '`', unit == '{', unit == '}', unit == '^',
			unit == '|', unit == '\\', unit == '%':
			const hex = "0123456789ABCDEF"
			encoded.WriteByte('%')
			encoded.WriteByte(hex[unit>>4])
			encoded.WriteByte(hex[unit&0x0f])
		default:
			encoded.WriteByte(unit)
		}
	}
	return encoded.String()
}

// renderLinkedPath renders a tool-header path (~-shortened, hyperlinked),
// matching the display/link behavior of upstream renderToolPath.
func renderLinkedPath(rawPath, cwd string) string {
	if rawPath == "" {
		return ""
	}
	return linkPath(ShortenPath(rawPath), rawPath, cwd)
}

func (tool *readTool) RenderCall(args any) string {
	object := renderArgs(args)
	text := "read " + renderLinkedPath(renderPath(object), tool.cwd)
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

func (tool *writeTool) RenderCall(args any) string {
	return "write " + renderLinkedPath(renderPath(renderArgs(args)), tool.cwd)
}

func (*writeTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

func (tool *editTool) RenderCall(args any) string {
	return "edit " + renderLinkedPath(renderPath(renderArgs(args)), tool.cwd)
}

func (*editTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

func (tool *lsTool) RenderCall(args any) string {
	object := renderArgs(args)
	path := renderPath(object)
	if path == "" {
		path = "."
	}
	text := "ls " + renderLinkedPath(path, tool.cwd)
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
