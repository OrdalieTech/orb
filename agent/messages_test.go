package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestConvertToLLMProjectsCodingAgentMessages(t *testing.T) {
	standard := &ai.UserMessage{Content: ai.NewUserText("standard"), Timestamp: 1}
	messages := engine.AgentMessages{
		standard,
		json.RawMessage(`{"role":"custom","customType":"note","content":"custom","display":false,"timestamp":2}`),
		json.RawMessage(`{"role":"custom","customType":"image","content":[{"type":"text","text":"blocks"},{"type":"image","data":"AA==","mimeType":"image/png"}],"display":true,"timestamp":3}`),
		json.RawMessage(`{"role":"branchSummary","summary":"branch","fromId":"entry","timestamp":4}`),
		json.RawMessage(`{"role":"compactionSummary","summary":"compact","tokensBefore":10,"timestamp":5}`),
		json.RawMessage(`{"role":"unknown","content":"ignored"}`),
	}

	got, err := ConvertToLLM(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0] != standard {
		t.Fatalf("converted = %#v", got)
	}
	assertUserText(t, got[1], "custom", 2)
	blocks := got[2].(*ai.UserMessage)
	if len(blocks.Content.Blocks) != 2 {
		t.Fatalf("custom blocks = %#v", blocks.Content)
	}
	assertUserText(t, got[3], BranchSummaryPrefix+"branch"+BranchSummarySuffix, 4)
	assertUserText(t, got[4], CompactionSummaryPrefix+"compact"+CompactionSummarySuffix, 5)
}

func TestConvertToLLMProjectsBashExecutionAndSkipsExcluded(t *testing.T) {
	messages := engine.AgentMessages{
		json.RawMessage(`{"role":"bashExecution","command":"false","output":"nope","exitCode":1,"cancelled":false,"truncated":true,"fullOutputPath":"/tmp/full","timestamp":9}`),
		json.RawMessage(`{"role":"bashExecution","command":"secret","output":"","cancelled":false,"truncated":false,"excludeFromContext":true,"timestamp":10}`),
	}
	got, err := ConvertToLLM(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("converted = %#v", got)
	}
	assertUserText(t, got[0], "Ran `false`\n```\nnope\n```\n\nCommand exited with code 1\n\n[Output truncated. Full output: /tmp/full]", 9)
}

func TestConvertToLLMWithBlockImagesAppliesSettingDynamically(t *testing.T) {
	blocked := false
	convert := ConvertToLLMWithBlockImages(func() bool { return blocked })
	image := &ai.ImageContent{Data: "AA==", MimeType: "image/png"}
	user := &ai.UserMessage{Content: ai.NewUserContent(
		&ai.TextContent{Text: "before"}, image, image, &ai.TextContent{Text: imageBlockedText}, &ai.TextContent{Text: "after"},
	), Timestamp: 1}
	toolResult := &ai.ToolResultMessage{Content: ai.ToolResultContent{image, image}, Timestamp: 2}

	unblocked, err := convert(context.Background(), engine.AgentMessages{user, toolResult})
	if err != nil {
		t.Fatal(err)
	}
	if unblockUser := unblocked[0].(*ai.UserMessage); len(unblockUser.Content.Blocks) != 5 {
		t.Fatalf("unblocked user content = %#v", unblockUser.Content)
	}
	blocked = true
	converted, err := convert(context.Background(), engine.AgentMessages{user, toolResult})
	if err != nil {
		t.Fatal(err)
	}
	blockedUser := converted[0].(*ai.UserMessage)
	if len(blockedUser.Content.Blocks) != 3 {
		t.Fatalf("blocked user content = %#v", blockedUser.Content)
	}
	if text := blockedUser.Content.Blocks[1].(*ai.TextContent).Text; text != imageBlockedText {
		t.Fatalf("blocked user placeholder = %q", text)
	}
	blockedTool := converted[1].(*ai.ToolResultMessage)
	if len(blockedTool.Content) != 1 || blockedTool.Content[0].(*ai.TextContent).Text != imageBlockedText {
		t.Fatalf("blocked tool content = %#v", blockedTool.Content)
	}
	if len(user.Content.Blocks) != 5 || len(toolResult.Content) != 2 {
		t.Fatal("conversion mutated source messages")
	}
}

func assertUserText(t testing.TB, message ai.Message, want string, timestamp int64) {
	t.Helper()
	user, ok := message.(*ai.UserMessage)
	if !ok || user.Timestamp != timestamp || len(user.Content.Blocks) != 1 {
		t.Fatalf("message = %#v", message)
	}
	text, ok := user.Content.Blocks[0].(*ai.TextContent)
	if !ok || text.Text != want {
		t.Fatalf("content = %#v, want %q", user.Content, want)
	}
}
