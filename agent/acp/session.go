package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

// translate maps a session event to an ACP session/update; tools tracks the
// calls already announced.
func translate(event any, tools map[string]bool) map[string]any {
	switch event := event.(type) {
	case engine.MessageUpdateEvent:
		switch delta := event.AssistantMessageEvent.(type) {
		case ai.TextDeltaEvent:
			return chunk("agent_message_chunk", delta.Delta)
		case ai.ThinkingDeltaEvent:
			return chunk("agent_thought_chunk", delta.Delta)
		}
	case engine.ToolExecutionStartEvent:
		if event.ParentToolCallID != "" {
			return nil
		}
		tools[event.ToolCallID] = true
		return map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": event.ToolCallID, "title": event.ToolName,
			"kind": toolKind(event.ToolName), "status": "in_progress", "rawInput": event.Args,
		}
	case engine.ToolExecutionEndEvent:
		if !tools[event.ToolCallID] {
			return nil
		}
		delete(tools, event.ToolCallID)
		status := "completed"
		if event.IsError {
			status = "failed"
		}
		update := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": event.ToolCallID, "status": status}
		if text := ai.ContentText(event.Result.Content); text != "" {
			update["content"] = []any{map[string]any{"type": "content", "content": map[string]string{"type": "text", "text": text}}}
		}
		return update
	}
	return nil
}

func chunk(kind, text string) map[string]any {
	if text == "" {
		return nil
	}
	return map[string]any{"sessionUpdate": kind, "content": map[string]string{"type": "text", "text": text}}
}

func toolKind(name string) string {
	switch name {
	case "read":
		return "read"
	case "edit", "write":
		return "edit"
	case "bash":
		return "execute"
	case "grep", "find", "ls":
		return "search"
	}
	return "other"
}

// promptInput flattens ACP content blocks into Orb's prompt text and images.
func promptInput(blocks []json.RawMessage) (string, []*ai.ImageContent) {
	var text strings.Builder
	var images []*ai.ImageContent
	for _, raw := range blocks {
		var block struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri"`
			Resource struct {
				URI      string  `json:"uri"`
				Text     *string `json:"text"`
				MimeType string  `json:"mimeType"`
			} `json:"resource"`
		}
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "image":
			images = append(images, &ai.ImageContent{Data: block.Data, MimeType: block.MimeType})
		case "resource_link":
			fmt.Fprintf(&text, "\n[Context] %s", block.URI)
		case "resource":
			if block.Resource.Text != nil {
				fmt.Fprintf(&text, "\n[Embedded Context] %s\n%s", block.Resource.URI, *block.Resource.Text)
			} else {
				fmt.Fprintf(&text, "\n[Embedded Context] %s", block.Resource.URI)
			}
		}
	}
	return text.String(), images
}

// command runs the slash commands Orb's interactive mode owns and a headless
// client still needs; prompt templates and skills expand in the prompt itself.
func (s *server) command(session *live, runtime *agent.AgentSession, text string) (bool, error) {
	name, argument, _ := strings.Cut(strings.TrimSpace(text), " ")
	argument = strings.TrimSpace(argument)
	var reply string
	switch name {
	case "/compact":
		result, err := runtime.Compact(s.ctx, argument)
		if err != nil {
			return true, err
		}
		reply = "Compacted the conversation."
		if result != nil && result.Summary != "" {
			reply += "\n\n" + result.Summary
		}
	case "/name":
		if argument == "" {
			return true, invalid("/name needs a name")
		}
		if err := runtime.SetSessionName(argument); err != nil {
			return true, err
		}
		reply = "Named the session " + argument + "."
	case "/session":
		stats := runtime.GetSessionStats()
		reply = fmt.Sprintf("Session %s\nMessages: %d\nTokens: in %d, out %d, cache read %d, cache write %d\nCost: $%.4f",
			stats.SessionID, stats.TotalMessages, stats.Tokens.Input, stats.Tokens.Output, stats.Tokens.CacheRead, stats.Tokens.CacheWrite, stats.Cost)
	default:
		return false, nil
	}
	s.update(session.id, chunk("agent_message_chunk", reply))
	return true, nil
}

func commands(runtime *agent.AgentSession) []map[string]any {
	list := []map[string]any{
		{"name": "compact", "description": "Summarize the conversation to free context", "input": map[string]string{"hint": "optional focus"}},
		{"name": "name", "description": "Name this session", "input": map[string]string{"hint": "<name>"}},
		{"name": "session", "description": "Show this session's messages, tokens and cost"},
	}
	for _, command := range runtime.Commands() {
		list = append(list, map[string]any{"name": command.Name, "description": command.Description})
	}
	return list
}

// configOptions lists the model and reasoning selectors, as pi-acp names them.
func configOptions(runtime *agent.AgentSession) []map[string]any {
	state := runtime.State()
	var options []map[string]any
	if models := runtime.AvailableModels(); len(models) > 0 {
		current := ""
		if state.Model != nil {
			current = modelValue(*state.Model)
		}
		values := make([]map[string]string, 0, len(models))
		for _, model := range models {
			values = append(values, map[string]string{"value": modelValue(model), "name": model.Name})
		}
		options = append(options, map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": current, "options": values})
	}
	if levels := runtime.AvailableThinkingLevels(); len(levels) > 1 {
		values := make([]map[string]string, 0, len(levels))
		for _, level := range levels {
			values = append(values, map[string]string{"value": string(level), "name": string(level)})
		}
		options = append(options, map[string]any{"id": "thought_level", "name": "Reasoning", "category": "thought_level", "type": "select", "currentValue": string(state.ThinkingLevel), "options": values})
	}
	return options
}

func modelValue(model ai.Model) string { return string(model.Provider) + "/" + model.ID }

func configure(ctx context.Context, runtime *agent.AgentSession, id, value string) error {
	switch id {
	case "model":
		for _, model := range runtime.AvailableModels() {
			if modelValue(model) == value || model.ID == value {
				return runtime.SetModel(ctx, model)
			}
		}
		return invalid("unknown model %s", value)
	case "thought_level":
		for _, level := range runtime.AvailableThinkingLevels() {
			if string(level) == value {
				return runtime.SetThinkingLevel(level)
			}
		}
		return invalid("unknown reasoning level %s", value)
	}
	return invalid("unknown config option %s", id)
}

// replay streams a loaded session's conversation, as session/load requires.
func (s *server) replay(session *live) {
	for _, message := range session.runtime.Session().State().Messages {
		switch message := message.(type) {
		case *ai.UserMessage:
			text := ai.ContentText(message.Content.Blocks)
			if message.Content.Text != nil {
				text = *message.Content.Text
			}
			if update := chunk("user_message_chunk", text); update != nil {
				s.update(session.id, update)
			}
		case *ai.AssistantMessage:
			for _, block := range message.Content {
				var update map[string]any
				switch block := block.(type) {
				case *ai.TextContent:
					update = chunk("agent_message_chunk", block.Text)
				case *ai.ThinkingContent:
					update = chunk("agent_thought_chunk", block.Thinking)
				}
				if update != nil {
					s.update(session.id, update)
				}
			}
		}
	}
}
