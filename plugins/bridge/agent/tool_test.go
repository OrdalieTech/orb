package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
)

func TestToolUsesExplicitAttachmentAndValidatesCalls(t *testing.T) {
	if _, err := NewTool(nil); err == nil {
		t.Fatal("missing attachment accepted")
	}
	calls := 0
	tool, err := NewTool(func(_ context.Context, peer string, call connect.Call) (json.RawMessage, error) {
		calls++
		if peer != "remote" || call.Method != "inspect" {
			t.Fatal("call changed")
		}
		return json.RawMessage(`{"remote":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tool.Execute(t.Context(), "call", map[string]any{"peer_id": "remote", "call": map[string]any{"method": "inspect"}}, nil); err == nil || calls != 0 {
		t.Fatal("invalid call reached remote")
	}
	result, err := tool.Execute(t.Context(), "call", map[string]any{"peer_id": "remote", "call": connect.Call{InstanceID: protocol.NewID(), Service: protocol.Service, Method: "inspect", Args: connect.JSON(struct{}{})}}, nil)
	if err != nil || calls != 1 || len(result.Content) != 1 {
		t.Fatal("valid call failed", err)
	}
}
