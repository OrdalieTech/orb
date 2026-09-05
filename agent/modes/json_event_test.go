package modes

import (
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestJSONToolCallStartRejectsMissingToolCall(t *testing.T) {
	message := &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "text"}}}
	for _, index := range []int{0, 1} {
		event := engine.MessageUpdateEvent{Message: message, AssistantMessageEvent: ai.ToolCallStartEvent{ContentIndex: index, Partial: message}}
		_, err := marshalJSONEvent(event)
		want := "toolcall_start content at index 0 is not a tool call"
		if index == 1 {
			want = "toolcall_start content at index 1 is not a tool call"
		}
		if err == nil || err.Error() != want {
			t.Fatalf("index %d: %v", index, err)
		}
	}
}
