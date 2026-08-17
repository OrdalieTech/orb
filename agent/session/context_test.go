package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuildSessionContextProjectsCompactionAwareCodingMessages(t *testing.T) {
	rootID, compactID, customID, branchID := "root", "compact", "custom", "branch"
	entries := []SessionEntry{
		{Type: "message", ID: rootID, Timestamp: "2025-01-01T00:00:00Z", Message: json.RawMessage(`{"role":"user","content":"old","timestamp":1}`)},
		{Type: "compaction", ID: compactID, ParentID: &rootID, Timestamp: "2025-01-01T00:00:01Z", Summary: "summary", FirstKeptEntryID: rootID, TokensBefore: 12},
		{Type: "custom_message", ID: customID, ParentID: &compactID, Timestamp: "2025-01-01T00:00:02Z", CustomType: "state", Content: json.RawMessage("null")},
		{Type: "branch_summary", ID: branchID, ParentID: &customID, Timestamp: "2025-01-01T00:00:03Z", FromID: rootID, Summary: "branch"},
	}

	context := BuildSessionContext(entries, &branchID)
	roles := make([]string, 0, len(context.Messages))
	for _, raw := range context.Messages {
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, message.Role)
		if message.Role == "custom" && string(message.Content) != "[]" {
			t.Fatalf("custom content = %s, want []", message.Content)
		}
	}
	if want := []string{"compactionSummary", "user", "custom", "branchSummary"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %#v, want %#v", roles, want)
	}
}

func TestBuildSessionContextOmitsEmptyBranchSummary(t *testing.T) {
	id := "branch"
	context := BuildSessionContext([]SessionEntry{{Type: "branch_summary", ID: id}}, &id)
	if len(context.Messages) != 0 {
		t.Fatalf("messages = %#v", context.Messages)
	}
}
