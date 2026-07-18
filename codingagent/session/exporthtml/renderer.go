package exporthtml

import (
	"bytes"
	"encoding/json"

	"github.com/OrdalieTech/pi-go/codingagent/session"
)

// RenderedToolHTML is the custom-tool HTML envelope consumed by the embedded
// upstream renderer. Pointer fields preserve optional property semantics.
type RenderedToolHTML struct {
	CallHTML            *string `json:"callHtml,omitempty"`
	ResultHTMLCollapsed *string `json:"resultHtmlCollapsed,omitempty"`
	ResultHTMLExpanded  *string `json:"resultHtmlExpanded,omitempty"`
}

// ToolHTMLRenderResult is the optional collapsed/expanded HTML returned for a
// custom tool result.
type ToolHTMLRenderResult struct {
	Collapsed *string
	Expanded  *string
}

// ToolHTMLRenderer pre-renders custom tool calls and results for live exports.
// TODO(WP-450): adapt registered TUI tool renderers to this seam, matching
// upstream createToolHtmlRenderer and its ANSI-to-HTML conversion.
type ToolHTMLRenderer interface {
	RenderCall(toolCallID, toolName string, arguments any) *string
	RenderResult(toolCallID, toolName string, content, details any, isError bool) *ToolHTMLRenderResult
}

var templateRenderedTools = map[string]struct{}{
	"bash":  {},
	"read":  {},
	"write": {},
	"edit":  {},
	"ls":    {},
}

type renderedToolRecord struct {
	id   string
	html RenderedToolHTML
}

// renderedToolCollection retains plain-object insertion order until the final
// JSON.stringify normalization, which then applies JavaScript's integer-key
// ordering without lexically sorting ordinary tool-call IDs.
type renderedToolCollection struct {
	records []renderedToolRecord
	indexes map[string]int
}

func newRenderedToolCollection() *renderedToolCollection {
	return &renderedToolCollection{indexes: make(map[string]int)}
}

func (collection *renderedToolCollection) get(id string) (RenderedToolHTML, bool) {
	index, ok := collection.indexes[id]
	if !ok {
		return RenderedToolHTML{}, false
	}
	return collection.records[index].html, true
}

func (collection *renderedToolCollection) set(id string, html RenderedToolHTML) {
	if index, ok := collection.indexes[id]; ok {
		collection.records[index].html = html
		return
	}
	collection.indexes[id] = len(collection.records)
	collection.records = append(collection.records, renderedToolRecord{id: id, html: html})
}

func (collection *renderedToolCollection) len() int {
	if collection == nil {
		return 0
	}
	return len(collection.records)
}

func (collection *renderedToolCollection) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, record := range collection.records {
		if index > 0 {
			output.WriteByte(',')
		}
		encodedID, err := json.Marshal(record.id)
		if err != nil {
			return nil, err
		}
		encodedHTML, err := json.Marshal(record.html)
		if err != nil {
			return nil, err
		}
		output.Write(encodedID)
		output.WriteByte(':')
		output.Write(encodedHTML)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func preRenderCustomTools(entries []session.SessionEntry, renderer ToolHTMLRenderer) *renderedToolCollection {
	if renderer == nil {
		return nil
	}
	renderedTools := newRenderedToolCollection()
	for _, entry := range entries {
		if entry.Type != "message" {
			continue
		}
		message := decodeJSONObject(entry.Message)
		role := decodeJSONString(message["role"])
		if role == "assistant" {
			var blocks []json.RawMessage
			if json.Unmarshal(message["content"], &blocks) == nil {
				for _, rawBlock := range blocks {
					block := decodeJSONObject(rawBlock)
					if decodeJSONString(block["type"]) != "toolCall" {
						continue
					}
					toolName := decodeJSONString(block["name"])
					if isTemplateRenderedTool(toolName) {
						continue
					}
					toolCallID := decodeJSONString(block["id"])
					callHTML := renderer.RenderCall(toolCallID, toolName, decodeJSONValue(block["arguments"]))
					if callHTML != nil && *callHTML != "" {
						renderedTools.set(toolCallID, RenderedToolHTML{CallHTML: cloneString(callHTML)})
					}
				}
			}
		}

		if role != "toolResult" {
			continue
		}
		toolCallID := decodeJSONString(message["toolCallId"])
		if toolCallID == "" {
			continue
		}
		toolName := decodeJSONString(message["toolName"])
		existing, hasExisting := renderedTools.get(toolCallID)
		if !hasExisting && isTemplateRenderedTool(toolName) {
			continue
		}
		result := renderer.RenderResult(
			toolCallID,
			toolName,
			decodeJSONValue(message["content"]),
			decodeJSONValue(message["details"]),
			decodeJSONBool(message["isError"]),
		)
		if result == nil {
			continue
		}
		existing.ResultHTMLCollapsed = cloneString(result.Collapsed)
		existing.ResultHTMLExpanded = cloneString(result.Expanded)
		renderedTools.set(toolCallID, existing)
	}
	if renderedTools.len() == 0 {
		return nil
	}
	return renderedTools
}

func isTemplateRenderedTool(name string) bool {
	_, ok := templateRenderedTools[name]
	return ok
}

func decodeJSONObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func decodeJSONString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func decodeJSONBool(raw json.RawMessage) bool {
	var value bool
	_ = json.Unmarshal(raw, &value)
	return value
}

func decodeJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
