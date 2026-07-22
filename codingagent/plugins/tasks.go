package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

var todoSchema = ai.JSONSchema(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","items":{"type":"object","required":["text","status"],"properties":{"text":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","done"]}}}}}}`)

type todoInput struct {
	Items []todoItem `json:"items"`
}

type todoItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

func tasksExtension() extensions.Factory {
	return func(api extensions.API) error {
		// ponytail: Tasks live only for this extension/session instance; append a
		// custom session entry when restored task state becomes a real need.
		var mu sync.Mutex
		var items []todoItem
		api.RegisterTool(extensions.ToolDefinition{
			Name: "todo", Label: "Todo", Description: "Replace the current session task list", Parameters: todoSchema,
			Execute: func(_ context.Context, _ string, raw any, _ agent.AgentToolUpdateCallback, ctx extensions.Context) (agent.AgentToolResult, error) {
				var input todoInput
				if err := decode(raw, &input); err != nil {
					return agent.AgentToolResult{}, err
				}
				for index := range input.Items {
					input.Items[index].Text = strings.TrimSpace(input.Items[index].Text)
					if input.Items[index].Text == "" {
						return agent.AgentToolResult{}, fmt.Errorf("todo: items[%d].text is required", index)
					}
					switch input.Items[index].Status {
					case "pending", "in_progress", "done":
					default:
						return agent.AgentToolResult{}, fmt.Errorf("todo: items[%d].status must be pending, in_progress, or done", index)
					}
				}
				mu.Lock()
				items = append(items[:0], input.Items...)
				text := renderTasks(items)
				mu.Unlock()
				if len(input.Items) == 0 {
					ctx.UI().SetWidget("tasks", nil, nil)
				} else {
					ctx.UI().SetWidget("tasks", &extensions.Widget{Lines: strings.Split(text, "\n")}, nil)
				}
				return textResult(text), nil
			},
		})
		return nil
	}
}

func renderTasks(items []todoItem) string {
	if len(items) == 0 {
		return "No tasks."
	}
	lines := make([]string, len(items))
	for index, item := range items {
		switch item.Status {
		case "done":
			lines[index] = "[x] " + item.Text
		case "in_progress":
			lines[index] = "→ [ ] " + item.Text
		default:
			lines[index] = "[ ] " + item.Text
		}
	}
	return strings.Join(lines, "\n")
}

func decode(raw any, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func textResult(text string) agent.AgentToolResult {
	return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}
}
