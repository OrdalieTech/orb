package ai

import (
	"bytes"
	"context"
	"reflect"
)

// TranscriptContext is the normalized provider request context. Context is
// retained as the public compatibility input; NormalizeContext folds its
// legacy prompt and tool fields into the transcript.
type TranscriptContext struct {
	Messages MessageList `json:"messages"`
}

type transcriptContextKey struct{}

func WithTranscriptContext(ctx context.Context, transcript TranscriptContext) context.Context {
	return context.WithValue(ctx, transcriptContextKey{}, transcript)
}

func TranscriptContextFrom(ctx context.Context) (TranscriptContext, bool) {
	if ctx == nil {
		return TranscriptContext{}, false
	}
	transcript, ok := ctx.Value(transcriptContextKey{}).(TranscriptContext)
	return transcript, ok
}

func CreateInitialSystemMessage(systemPrompt *string, tools *[]Tool) *SystemMessage {
	hasPrompt := systemPrompt != nil && *systemPrompt != ""
	hasTools := tools != nil && len(*tools) > 0
	if !hasPrompt && !hasTools {
		return nil
	}
	content := ""
	if systemPrompt != nil {
		content = *systemPrompt
	}
	message := &SystemMessage{Content: content}
	if hasTools {
		message.ToolsAdded = cloneTools(*tools)
	}
	return message
}

func NormalizeContext(context Context) TranscriptContext {
	messages := append(MessageList(nil), context.Messages...)
	if initial := CreateInitialSystemMessage(context.SystemPrompt, context.Tools); initial != nil {
		messages = append(MessageList{initial}, messages...)
	}
	return TranscriptContext{Messages: messages}
}

func InitialSystemMessage(messages MessageList) *SystemMessage {
	if len(messages) == 0 {
		return nil
	}
	message, _ := messages[0].(*SystemMessage)
	return message
}

func WithoutInitialSystemMessage(messages MessageList) MessageList {
	if InitialSystemMessage(messages) == nil {
		return append(MessageList(nil), messages...)
	}
	return append(MessageList(nil), messages[1:]...)
}

func CurrentTools(messages MessageList) []Tool {
	order := []string{}
	tools := map[string]Tool{}
	for _, raw := range messages {
		message, ok := raw.(*SystemMessage)
		if !ok {
			continue
		}
		for _, removed := range message.ToolsRemoved {
			if _, exists := tools[removed.Name]; exists {
				delete(tools, removed.Name)
				for index, name := range order {
					if name == removed.Name {
						order = append(order[:index], order[index+1:]...)
						break
					}
				}
			}
		}
		for _, tool := range message.ToolsAdded {
			if _, exists := tools[tool.Name]; !exists {
				order = append(order, tool.Name)
			}
			tools[tool.Name] = tool
		}
	}
	result := make([]Tool, 0, len(tools))
	for _, name := range order {
		if tool, exists := tools[name]; exists {
			result = append(result, tool)
		}
	}
	return result
}

func ResolveTranscript(context TranscriptContext, supportsMidConversationSystemMessages bool) TranscriptContext {
	if supportsMidConversationSystemMessages {
		return TranscriptContext{Messages: append(MessageList(nil), context.Messages...)}
	}
	return CollapseSystemMessages(context)
}

func DeclaredTools(messages MessageList) []Tool {
	order := []string{}
	definitions := map[string]Tool{}
	for _, raw := range messages {
		message, ok := raw.(*SystemMessage)
		if !ok {
			continue
		}
		for _, tool := range message.ToolsAdded {
			if _, exists := definitions[tool.Name]; !exists {
				order = append(order, tool.Name)
			}
			definitions[tool.Name] = tool
		}
	}
	result := make([]Tool, 0, len(order))
	for _, name := range order {
		result = append(result, definitions[name])
	}
	return result
}

func HasToolRedefinitions(messages MessageList) bool {
	declared := map[string]Tool{}
	for _, raw := range messages {
		message, ok := raw.(*SystemMessage)
		if !ok {
			continue
		}
		for _, tool := range message.ToolsAdded {
			if previous, exists := declared[tool.Name]; exists && !toolDeclarationsEqual(previous, tool) {
				return true
			}
			declared[tool.Name] = tool
		}
	}
	return false
}

func HasNonAdditiveToolChanges(messages MessageList) bool {
	declared := map[string]struct{}{}
	for _, raw := range messages {
		message, ok := raw.(*SystemMessage)
		if !ok {
			continue
		}
		if len(message.ToolsRemoved) > 0 {
			return true
		}
		for _, tool := range message.ToolsAdded {
			if _, exists := declared[tool.Name]; exists {
				return true
			}
			declared[tool.Name] = struct{}{}
		}
	}
	return false
}

type TranscriptTools struct {
	RequestTools     []Tool
	AnchorsAdditions bool
}

func ResolveTranscriptTools(messages MessageList, supportsToolAdditions bool) TranscriptTools {
	anchors := supportsToolAdditions && !HasNonAdditiveToolChanges(messages)
	if anchors {
		initial := InitialSystemMessage(messages)
		if initial == nil {
			return TranscriptTools{RequestTools: []Tool{}, AnchorsAdditions: true}
		}
		return TranscriptTools{RequestTools: cloneTools(initial.ToolsAdded), AnchorsAdditions: true}
	}
	return TranscriptTools{RequestTools: CurrentTools(messages)}
}

func CurrentSystemMessage(messages MessageList) *SystemMessage {
	var content []string
	sections := SystemPromptSections{}
	sectionIndex := map[string]int{}
	var timestamp *int64
	for _, raw := range messages {
		message, ok := raw.(*SystemMessage)
		if !ok {
			continue
		}
		if timestamp == nil {
			value := message.Timestamp
			timestamp = &value
		}
		if text := systemContentText(message.Content); text != "" {
			content = append(content, text)
		}
		for _, section := range message.Sections {
			if section.Text == nil {
				if index, exists := sectionIndex[section.Name]; exists {
					sections = append(sections[:index], sections[index+1:]...)
					sectionIndex = indexSections(sections)
				}
				continue
			}
			if index, exists := sectionIndex[section.Name]; exists {
				sections[index].Text = cloneString(section.Text)
			} else {
				sectionIndex[section.Name] = len(sections)
				sections = append(sections, SystemPromptSection{Name: section.Name, Text: cloneString(section.Text)})
			}
		}
	}
	tools := CurrentTools(messages)
	if timestamp == nil && len(tools) == 0 {
		return nil
	}
	value := int64(0)
	if timestamp != nil {
		value = *timestamp
	}
	return &SystemMessage{Content: joinNonEmpty(content), Sections: sections, ToolsAdded: tools, Timestamp: value}
}

func CurrentSystemPrompt(messages MessageList) string {
	return SystemMessageText(CurrentSystemMessage(messages))
}

func SystemMessageText(message *SystemMessage) string {
	if message == nil {
		return ""
	}
	parts := []string{}
	if text := systemContentText(message.Content); text != "" {
		parts = append(parts, text)
	}
	for _, section := range message.Sections {
		if section.Text != nil && *section.Text != "" {
			parts = append(parts, *section.Text)
		}
	}
	return joinNonEmpty(parts)
}

func CollapseSystemMessages(context TranscriptContext) TranscriptContext {
	head := CurrentSystemMessage(context.Messages)
	messages := make(MessageList, 0, len(context.Messages))
	if head != nil {
		messages = append(messages, head)
	}
	for _, message := range context.Messages {
		if _, system := message.(*SystemMessage); !system {
			messages = append(messages, message)
		}
	}
	return TranscriptContext{Messages: messages}
}

func ToolStateChanges(previous, current []Tool) (added []Tool, removed []ToolReference) {
	previousByName := make(map[string]Tool, len(previous))
	currentByName := make(map[string]Tool, len(current))
	for _, tool := range previous {
		previousByName[tool.Name] = tool
	}
	for _, tool := range current {
		currentByName[tool.Name] = tool
		if old, exists := previousByName[tool.Name]; !exists || !toolDeclarationsEqual(old, tool) {
			added = append(added, toolDeclaration(tool))
		}
	}
	for _, tool := range previous {
		if next, exists := currentByName[tool.Name]; !exists || !toolDeclarationsEqual(tool, next) {
			removed = append(removed, ToolReference{Name: tool.Name})
		}
	}
	return added, removed
}

// NewToolStateSystemMessage creates the loop-generated wire shape where the
// timestamp precedes tool deltas, matching upstream object spread order.
func NewToolStateSystemMessage(timestamp int64, added []Tool, removed []ToolReference) *SystemMessage {
	return &SystemMessage{Content: "", ToolsAdded: added, ToolsRemoved: removed, Timestamp: timestamp, toolFieldsAfterTimestamp: true}
}

func WithToolStateChanges(message *SystemMessage, added []Tool, removed []ToolReference) *SystemMessage {
	if message == nil {
		return nil
	}
	copy := *message
	copy.ToolsAdded = added
	copy.ToolsRemoved = removed
	copy.toolFieldsAfterTimestamp = true
	return &copy
}

func systemContentText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []TextContent:
		parts := make([]string, 0, len(value))
		for _, block := range value {
			parts = append(parts, block.Text)
		}
		return joinWith(parts, "\n")
	case []*TextContent:
		parts := make([]string, 0, len(value))
		for _, block := range value {
			if block != nil {
				parts = append(parts, block.Text)
			}
		}
		return joinWith(parts, "\n")
	default:
		return ""
	}
}

func joinNonEmpty(parts []string) string { return joinWith(parts, "\n\n") }

func joinWith(parts []string, separator string) string {
	var output bytes.Buffer
	for index, part := range parts {
		if index > 0 {
			output.WriteString(separator)
		}
		output.WriteString(part)
	}
	return output.String()
}

func cloneTools(tools []Tool) []Tool { return append([]Tool(nil), tools...) }

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func indexSections(sections SystemPromptSections) map[string]int {
	result := make(map[string]int, len(sections))
	for index, section := range sections {
		result[section.Name] = index
	}
	return result
}

func toolDeclaration(tool Tool) Tool {
	return Tool{Name: tool.Name, Description: tool.Description, Parameters: append(JSONSchema(nil), tool.Parameters...), ConstrainedSampling: tool.ConstrainedSampling}
}

func toolDeclarationsEqual(left, right Tool) bool {
	return reflect.DeepEqual(toolDeclaration(left), toolDeclaration(right))
}
