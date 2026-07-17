package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

func TestConvertToLLMProjectsCodingAgentMessages(t *testing.T) {
	standard := &ai.UserMessage{Content: ai.NewUserText("standard"), Timestamp: 1}
	messages := agent.AgentMessages{
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
	messages := agent.AgentMessages{
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

func TestConvertToLLMNormalizesNullCustomContent(t *testing.T) {
	got, err := ConvertToLLM(context.Background(), agent.AgentMessages{
		json.RawMessage(`{"role":"custom","customType":"empty","content":null,"display":false,"timestamp":7}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	user := got[0].(*ai.UserMessage)
	if user.Content.Text != nil || user.Content.Blocks == nil || len(user.Content.Blocks) != 0 {
		t.Fatalf("content = %#v", user.Content)
	}
}

func TestConvertToLLMPreservesStandardFallbackAndLoneSurrogate(t *testing.T) {
	got, err := ConvertToLLM(context.Background(), agent.AgentMessages{
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"\ud800"},{"type":"future","value":1}],"timestamp":1}`),
		json.RawMessage(`{"role":"custom","customType":"future","content":"\ud800","display":false,"timestamp":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("converted = %#v", got)
	}
	standard := got[0].(*ai.UserMessage)
	if len(standard.Content.Blocks) != 2 || standard.Content.Blocks[0].(*ai.TextContent).Text != "\xed\xa0\x80" {
		t.Fatalf("standard = %#v", standard.Content)
	}
	if unknown, ok := standard.Content.Blocks[1].(*ai.UnknownContentBlock); !ok || string(unknown.Raw) != `{"type":"future","value":1}` {
		t.Fatalf("unknown block = %T %#v", standard.Content.Blocks[1], standard.Content.Blocks[1])
	}
	custom := got[1].(*ai.UserMessage)
	if len(custom.Content.Blocks) != 1 || custom.Content.Blocks[0].(*ai.TextContent).Text != "\xed\xa0\x80" {
		t.Fatalf("custom = %#v", custom.Content)
	}
}

func TestConvertToLLMPreservesLoneSurrogateFullOutputPath(t *testing.T) {
	got, err := ConvertToLLM(context.Background(), agent.AgentMessages{
		json.RawMessage(`{"role":"bashExecution","command":"cmd","output":"out","exitCode":0,"truncated":true,"fullOutputPath":"\ud800","timestamp":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertUserText(t, got[0], "Ran `cmd`\n```\nout\n```\n\n[Output truncated. Full output: \xed\xa0\x80]", 1)
	encoded, err := ai.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"text":"Ran `)) || !bytes.Contains(encoded, []byte(`\ud800`)) || bytes.Contains(encoded, []byte("\ufffd")) {
		t.Fatalf("encoded = %s", encoded)
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
