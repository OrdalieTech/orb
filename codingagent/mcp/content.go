package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func mapToolResult(server, tool string, result *mcpsdk.CallToolResult) agent.AgentToolResult {
	if result == nil {
		return agent.AgentToolResult{Content: textToolContent(""), Details: resultDetails(server, tool, nil)}
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
	if len(content) == 0 {
		content = textToolContent("")
	}
	return agent.AgentToolResult{Content: content, Details: resultDetails(server, tool, result)}
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

func resultDetails(server, tool string, result *mcpsdk.CallToolResult) map[string]any {
	details := map[string]any{"server": server, "tool": tool}
	if result == nil {
		return details
	}
	details["isError"] = result.IsError
	if result.StructuredContent != nil {
		details["structuredContent"] = result.StructuredContent
	}
	if len(result.Meta) != 0 {
		details["meta"] = result.Meta
	}
	return details
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
		if text, ok := block.(*ai.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	if len(texts) == 0 {
		return "MCP tool returned an error"
	}
	return strings.Join(texts, "\n")
}
