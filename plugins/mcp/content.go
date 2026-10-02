package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mapToolResult converts a CallToolResult. The model sees its content;
// StructuredContent is the whole result without _meta, and isError results are
// error results that keep it.
func mapToolResult(server, tool string, result *mcpsdk.CallToolResult) engine.AgentToolResult {
	details := map[string]any{"server": server, "tool": tool}
	if result == nil {
		return engine.AgentToolResult{Content: textToolContent(""), Details: details}
	}
	content := make(ai.ToolResultContent, 0, len(result.Content))
	for _, block := range result.Content {
		switch value := block.(type) {
		case *mcpsdk.TextContent:
			content = append(content, &ai.TextContent{Text: value.Text})
		case *mcpsdk.ImageContent:
			content = append(content, &ai.ImageContent{
				Data:     base64.StdEncoding.EncodeToString(value.Data),
				MimeType: value.MIMEType,
			})
		case *mcpsdk.EmbeddedResource:
			content = appendEmbeddedResource(content, value)
		default:
			content = append(content, &ai.TextContent{Text: marshalContent(block)})
		}
	}
	if len(content) == 0 && result.StructuredContent != nil {
		content = append(content, &ai.TextContent{Text: marshalContent(result.StructuredContent)})
	}
	if result.IsError && toolResultText(content) == "" {
		content = append(content, &ai.TextContent{Text: "MCP tool " + server + "/" + tool + " returned an error"})
	}
	if len(content) == 0 {
		content = textToolContent("")
	}
	var structured map[string]any
	if data, err := json.Marshal(result); err == nil && json.Unmarshal(data, &structured) == nil {
		delete(structured, "_meta")
	}
	return engine.AgentToolResult{Content: content, Details: details, StructuredContent: structured, IsError: result.IsError}
}

func appendEmbeddedResource(content ai.ToolResultContent, embedded *mcpsdk.EmbeddedResource) ai.ToolResultContent {
	if embedded == nil || embedded.Resource == nil {
		return append(content, &ai.TextContent{Text: marshalContent(embedded)})
	}
	resource := embedded.Resource
	if strings.HasPrefix(strings.ToLower(resource.MIMEType), "image/") && len(resource.Blob) > 0 {
		return append(content, &ai.ImageContent{
			Data:     base64.StdEncoding.EncodeToString(resource.Blob),
			MimeType: resource.MIMEType,
		})
	}
	if resource.Text != "" || len(resource.Blob) == 0 {
		return append(content, &ai.TextContent{Text: resource.Text})
	}
	return append(content, &ai.TextContent{Text: marshalContent(embedded)})
}

func marshalContent(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "MCP returned unsupported content"
	}
	return string(data)
}

func textToolContent(text string) ai.ToolResultContent {
	return ai.ToolResultContent{&ai.TextContent{Text: text}}
}

func toolResultText(content ai.ToolResultContent) string {
	texts := make([]string, 0, len(content))
	for _, block := range content {
		if text, ok := block.(*ai.TextContent); ok && text.Text != "" {
			texts = append(texts, text.Text)
		}
	}
	return strings.Join(texts, "\n")
}
