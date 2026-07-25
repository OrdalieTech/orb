package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

var todoSchema = ai.JSONSchema(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","description":"The complete task list. Every call replaces the previous list, so resend unchanged tasks.","items":{"type":"object","required":["text","status"],"properties":{"text":{"type":"string","description":"Short imperative description of one task."},"status":{"type":"string","enum":["pending","in_progress","done"],"description":"Task state; keep at most one task in_progress."}}}}}}`)

type todoInput struct {
	Items []todoItem `json:"items"`
}

type todoItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

func tasksExtension() extensions.Factory {
	return func(api extensions.API) error {
		show := func(ctx extensions.Context, items []todoItem) {
			if len(items) == 0 {
				ctx.UI().SetWidget("tasks", nil, nil)
				return
			}
			ctx.UI().SetWidget("tasks", &extensions.Widget{Lines: strings.Split(renderTasks(items), "\n")}, nil)
		}
		// The list lives in tool-result details, as upstream todo.ts does, so a
		// resumed or branched session shows the list that branch actually has.
		// Nothing is kept in memory: every call replaces the whole list.
		restore := func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			show(ctx, todosFromBranch(ctx.SessionManager()))
			return nil, nil
		}
		api.On(extensions.EventSessionStart, restore)
		api.On(extensions.EventSessionTree, restore)
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
				show(ctx, input.Items)
				result := textResult(renderTasks(input.Items))
				result.Details = todoInput{Items: input.Items}
				return result, nil
			},
		})
		return nil
	}
}

func todosFromBranch(manager extensions.ReadonlySessionManager) []todoItem {
	if manager == nil {
		return nil
	}
	branch := manager.GetBranch()
	// Walk from the tail and skip entries that cannot name the tool: this runs on
	// session start and on every tree navigation, and unmarshalling a whole branch
	// to find its last todo result cost tens of milliseconds on long sessions. A
	// substring hit is only a candidate; the checks below still reject it.
	for index := len(branch) - 1; index >= 0; index-- {
		entry := branch[index]
		if entry.Type != "message" || !bytes.Contains(entry.Message, []byte(`"todo"`)) {
			continue
		}
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		result, ok := message.(*ai.ToolResultMessage)
		if !ok || result.ToolName != "todo" || result.IsError || len(result.Details) == 0 {
			continue
		}
		var details todoInput
		if json.Unmarshal(result.Details, &details) == nil {
			return details.Items
		}
	}
	return nil
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
