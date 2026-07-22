package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/modes/theme"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
)

// Port of upstream regression 4167-thinking-toggle-pending-tool-render.test.ts:
// rebuilding the chat from session entries (hide-thinking toggle, /settings
// changes, session rebind) must keep unresolved rendered tool calls registered
// so live completion events still update them, while completed historical
// tool calls render their persisted results.

const pendingToolCallID = "tool-4167"
const pendingToolName = "slow_tool"

func newPendingToolMode(t *testing.T, entries []any) *InteractiveMode {
	t.Helper()
	initTestTheme(t)
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range entries {
		if _, err := manager.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(nil), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	ui := tui.NewTUI(newFakeTerminal(120, 40))
	return &InteractiveMode{
		session:        runtime,
		ui:             ui,
		chat:           tui.NewWindowedContainer(),
		toolComponents: make(map[string]*ToolExecutionComponent),
		cwd:            cwd,
		mdTheme:        theme.MarkdownTheme(),
	}
}

func pendingToolCallMessage() *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content: ai.AssistantContent{&ai.ToolCall{
			ID: pendingToolCallID, Name: pendingToolName, Arguments: map[string]any{"delayMs": 10_000},
		}},
		API: "test-api", Provider: "test-provider", Model: "test-model",
		StopReason: ai.StopReasonToolUse,
	}
}

func pendingToolResultMessage(text string) *ai.ToolResultMessage {
	return &ai.ToolResultMessage{
		ToolCallID: pendingToolCallID,
		ToolName:   pendingToolName,
		Content:    ai.ToolResultContent{&ai.TextContent{Text: text}},
	}
}

func renderChatText(t *testing.T, mode *InteractiveMode) string {
	t.Helper()
	return strings.Join(normalizeWP450Lines(mode.chat.Render(120)), "\n")
}

func renderWindowedChatText(mode *InteractiveMode) string {
	height := mode.chat.LineCount(120)
	return strings.Join(normalizeWP450Lines(mode.chat.RenderLines(120, 0, height)), "\n")
}

func assistantTextMessage(text string) *ai.AssistantMessage {
	return &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: text}}}
}

func TestWindowedChatProductionMutationsRefreshCachedChildren(t *testing.T) {
	t.Run("assistant update", func(t *testing.T) {
		mode := newPendingToolMode(t, nil)
		mode.handleEvent(agent.MessageStartEvent{Message: assistantTextMessage("BEFORE_STREAM")})
		_ = mode.chat.LineCount(120)
		mode.handleEvent(agent.MessageUpdateEvent{Message: assistantTextMessage("AFTER_STREAM")})
		if rendered := renderWindowedChatText(mode); !strings.Contains(rendered, "AFTER_STREAM") || strings.Contains(rendered, "BEFORE_STREAM") {
			t.Fatalf("assistant cache did not refresh:\n%s", rendered)
		}
	})

	t.Run("tool result", func(t *testing.T) {
		mode := newPendingToolMode(t, nil)
		mode.handleEvent(agent.ToolExecutionStartEvent{ToolCallID: "tool-window", ToolName: "window_tool"})
		_ = mode.chat.LineCount(120)
		mode.handleEvent(agent.ToolExecutionEndEvent{
			ToolCallID: "tool-window",
			ToolName:   "window_tool",
			Result: agent.AgentToolResult{
				Content: ai.ToolResultContent{&ai.TextContent{Text: "WINDOW_TOOL_RESULT"}},
			},
		})
		if rendered := renderWindowedChatText(mode); !strings.Contains(rendered, "WINDOW_TOOL_RESULT") {
			t.Fatalf("tool cache did not refresh:\n%s", rendered)
		}
	})

	t.Run("bound bash requester", func(t *testing.T) {
		mode := newPendingToolMode(t, nil)
		component := mode.newBashExecutionComponent("printf test", false)
		defer component.SetComplete(nil, true)
		mode.chat.AddChild(component)
		_ = mode.chat.LineCount(120)
		component.AppendOutput("WINDOW_BASH_OUTPUT")
		component.ui.RequestRender()
		if rendered := renderWindowedChatText(mode); !strings.Contains(rendered, "WINDOW_BASH_OUTPUT") {
			t.Fatalf("bash requester did not refresh cache:\n%s", rendered)
		}
	})
}

func TestRestoredPendingToolCallsReceiveLiveCompletionEvents(t *testing.T) {
	mode := newPendingToolMode(t, []any{pendingToolCallMessage()})

	mode.renderInitialMessages()
	if mode.toolComponents[pendingToolCallID] == nil {
		t.Fatal("unresolved rendered tool call was not registered for live completion events")
	}

	mode.handleEvent(agent.ToolExecutionEndEvent{
		ToolCallID: pendingToolCallID,
		ToolName:   pendingToolName,
		Result:     agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "FINAL_RESULT"}}},
	})

	if rendered := renderChatText(t, mode); !strings.Contains(rendered, "FINAL_RESULT") {
		t.Fatalf("live completion did not reach the restored tool render:\n%s", rendered)
	}
}

func TestRestoredCompletedToolCallsRenderHistoricalResults(t *testing.T) {
	mode := newPendingToolMode(t, []any{pendingToolCallMessage(), pendingToolResultMessage("HISTORICAL_RESULT")})

	mode.renderInitialMessages()

	component := mode.toolComponents[pendingToolCallID]
	if component == nil {
		t.Fatal("historical tool call was not rendered")
	}
	// The historical pair is fully resolved: its render is the persisted
	// result, not a pending spinner state (upstream keeps no pending entry).
	if component.result == nil || component.isPartial {
		t.Fatalf("historical tool component still pending: result=%v isPartial=%t", component.result, component.isPartial)
	}
	if rendered := renderChatText(t, mode); !strings.Contains(rendered, "HISTORICAL_RESULT") {
		t.Fatalf("historical result missing from rebuilt chat:\n%s", rendered)
	}
}
