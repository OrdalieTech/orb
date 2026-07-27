package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/tui"
)

var todoSchema = ai.JSONSchema(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","description":"The complete task list. Every call replaces the previous list, so resend unchanged tasks.","items":{"type":"object","required":["text","status"],"properties":{"text":{"type":"string","description":"Short imperative description of one task."},"status":{"type":"string","enum":["pending","in_progress","done"],"description":"Task state; keep at most one task in_progress."}}}}}}`)

type todoInput struct {
	Items []todoItem `json:"items"`
}

type todoItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

type taskWidgetTheme interface {
	FG(color, text string) string
}

type taskWidget struct {
	mu             sync.Mutex
	items          []todoItem
	expanded       bool
	host           extensions.UIHost
	theme          taskWidgetTheme
	cachedWidth    int
	cachedExpanded bool
	cachedLines    []string
	cacheRevision  uint64
}

func newTaskWidget(items []todoItem, host extensions.UIHost, theme taskWidgetTheme) *taskWidget {
	return &taskWidget{items: append([]todoItem(nil), items...), host: host, theme: theme}
}

func (widget *taskWidget) Invalidate() {
	widget.mu.Lock()
	widget.cachedLines = nil
	widget.cacheRevision++
	widget.mu.Unlock()
}

func (widget *taskWidget) Render(width int) []string {
	if width <= 0 {
		return nil
	}
	widget.mu.Lock()
	expanded, revision := widget.expanded, widget.cacheRevision
	if widget.cachedLines != nil && widget.cachedWidth == width && widget.cachedExpanded == expanded {
		lines := append([]string(nil), widget.cachedLines...)
		widget.mu.Unlock()
		return lines
	}
	widget.mu.Unlock()
	marker := "▸ "
	if expanded {
		marker = "▾ "
	}
	headerText := marker + renderTaskSummary(widget.items)
	if expanded && widget.theme != nil {
		headerText = widget.theme.FG("dim", headerText)
	}
	headerPadding := min(1, max(0, (width-1)/2))
	lines := tui.NewTruncatedText(headerText, headerPadding, 0).Render(width)
	if expanded {
		taskLines := strings.Split(renderTasks(widget.items), "\n")
		if widget.theme != nil {
			for index := range taskLines {
				taskLines[index] = widget.theme.FG("dim", taskLines[index])
			}
		}
		listPadding := min(4, max(0, (width-1)/2))
		lines = append(lines, tui.NewText(strings.Join(taskLines, "\n"), listPadding, 0, nil).Render(width)...)
	}
	widget.mu.Lock()
	if widget.expanded == expanded && widget.cacheRevision == revision {
		widget.cachedWidth, widget.cachedExpanded = width, expanded
		widget.cachedLines = append(widget.cachedLines[:0], lines...)
	}
	widget.mu.Unlock()
	return lines
}

func (widget *taskWidget) HandleMouse(event tui.MouseEvent) bool {
	if event.Type != tui.MousePress || event.Button != 0 {
		return false
	}
	if event.Clicks > 1 {
		return true
	}
	widget.mu.Lock()
	widget.expanded = !widget.expanded
	widget.cachedLines = nil
	widget.cacheRevision++
	widget.mu.Unlock()
	if widget.host != nil {
		widget.host.Invalidate()
	}
	return true
}

func tasksExtension() extensions.Factory {
	return func(api extensions.API) error {
		show := func(ctx extensions.Context, items []todoItem) {
			if len(items) == 0 {
				ctx.UI().SetWidget("tasks", nil, nil)
				return
			}
			summary := renderTaskSummary(items)
			widget := &extensions.Widget{Lines: []string{summary}}
			if ctx.Mode() == extensions.ModeTUI {
				widget.Factory = func(host extensions.UIHost, theme extensions.Theme) extensions.Component {
					return newTaskWidget(items, host, theme)
				}
			}
			ctx.UI().SetWidget("tasks", widget, nil)
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
		api.RegisterCommand("tasks", extensions.Command{
			Description: "Show all tasks on the current branch",
			Handler: func(ctx context.Context, _ string, command extensions.CommandContext) error {
				if command.Mode() != extensions.ModeTUI {
					command.UI().Notify("/tasks requires interactive mode", extensions.NotifyError)
					return nil
				}
				_, _, err := command.UI().Select(ctx, renderTaskPanel(todosFromBranch(command.SessionManager())), []string{"Close"}, nil)
				return err
			},
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "todo", Label: "Todo", Description: "Replace the current session task list", Parameters: todoSchema,
			RenderResult: func(result agent.AgentToolResult, options extensions.ToolRenderResultOptions, _ extensions.Theme, _ extensions.ToolRenderContext) extensions.Component {
				items, ok := todoItemsFromDetails(result.Details)
				if !ok {
					return tui.NewText(ai.ContentText(result.Content), 0, 0, nil)
				}
				if options.Expanded {
					return tui.NewText(renderTasks(items), 0, 0, nil)
				}
				return tui.NewTruncatedText(renderTaskSummary(items), 0, 0)
			},
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

func renderTaskSummary(items []todoItem) string {
	if len(items) == 0 {
		return "No tasks."
	}
	done, queued := 0, 0
	current := ""
	for _, item := range items {
		switch item.Status {
		case "done":
			done++
		case "pending":
			queued++
		case "in_progress":
			if current == "" {
				current = item.Text
			}
		}
	}
	summary := fmt.Sprintf("✓ %d/%d", done, len(items))
	if current != "" {
		summary += "  → " + current
	}
	if queued > 0 {
		summary += fmt.Sprintf("  ·  +%d queued", queued)
	} else if current == "" {
		summary += "  All tasks complete"
	}
	return summary
}

func renderTaskPanel(items []todoItem) string {
	if len(items) == 0 {
		return "No tasks."
	}
	done := 0
	for _, item := range items {
		if item.Status == "done" {
			done++
		}
	}
	return fmt.Sprintf("Tasks — %d/%d complete\n\n%s", done, len(items), renderTasks(items))
}

func todoItemsFromDetails(details any) ([]todoItem, bool) {
	if details == nil {
		return nil, false
	}
	if input, ok := details.(todoInput); ok {
		return input.Items, true
	}
	if input, ok := details.(*todoInput); ok && input != nil {
		return input.Items, true
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return nil, false
	}
	var input struct {
		Items *[]todoItem `json:"items"`
	}
	if json.Unmarshal(encoded, &input) != nil || input.Items == nil {
		return nil, false
	}
	return *input.Items, true
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
