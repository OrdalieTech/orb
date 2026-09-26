package claudesessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
)

// Another model's turn reaches Claude as plain alternating messages, its tool
// calls and results as text, with UUIDs that rebuild identically.
func TestRebuildRewritesOtherModelsTurns(t *testing.T) {
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []ai.Message{
		&ai.UserMessage{Content: ai.NewUserText("list files")},
		&ai.AssistantMessage{Provider: "openai", Content: ai.AssistantContent{&ai.ToolCall{ID: "c", Name: "bash", Arguments: map[string]any{"command": "ls"}}}},
		&ai.ToolResultMessage{ToolCallID: "c", ToolName: "bash", Content: ai.ToolResultContent{&ai.TextContent{Text: "a.go"}}},
		&ai.AssistantMessage{Provider: "openai", Content: ai.AssistantContent{&ai.TextContent{Text: "one file"}}},
		&ai.UserMessage{Content: ai.NewUserText("this turn's prompt")},
	} {
		if _, err := manager.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	records := rebuild(manager, 1)
	var got []string
	for _, record := range records {
		raw, _ := json.Marshal(record["message"].(map[string]any)["content"])
		got = append(got, record["type"].(string)+" "+string(raw))
	}
	want := []string{
		`user [{"text":"list files","type":"text"}]`,
		`assistant [{"text":"[bash {\"command\":\"ls\"}]","type":"text"}]`,
		`user [{"text":"[bash result]\na.go","type":"text"}]`,
		`assistant [{"text":"one file","type":"text"}]`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("records:\n%s", strings.Join(got, "\n"))
	}
	if again := rebuild(manager, 1); again[3]["uuid"] != records[3]["uuid"] || records[3]["parentUuid"] != records[2]["uuid"] {
		t.Fatal("rebuilt records are not chained identically")
	}
}

// A Claude Code session opens in Orb with its messages to read and its own
// records for Claude to resume from.
func TestImportClaudeCodeSession(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "projects", "-work")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","uuid":"u1","cwd":"/work","message":{"role":"user","content":"read a.go"}}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"m1","model":"claude","content":[{"type":"text","text":"Reading."}]}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"m1","model":"claude","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]}}`,
		`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package a"}]}}`,
		`{"type":"assistant","uuid":"a3","message":{"id":"m2","model":"claude","content":[{"type":"text","text":"It is package a."}]}}`,
		`{"type":"file-history-snapshot","messageId":"x"}`,
	}
	if err := os.WriteFile(filepath.Join(project, "0b7a4a1e-1111-4222-8333-444455556666.jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	var cwd string
	manager, err := ImportClaudeCode("0b7a4a1e-1111-4222-8333-444455556666", []string{"CLAUDE_CONFIG_DIR=" + base}, func(dir string) (*session.SessionManager, error) {
		cwd = dir
		return session.InMemory(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	var messages []ai.Message
	for _, raw := range manager.BuildSessionContext().Messages {
		message, err := ai.UnmarshalMessage(raw)
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if cwd != "/work" || len(messages) != 4 {
		t.Fatalf("imported %d messages in %q", len(messages), cwd)
	}
	first, _ := messages[1].(*ai.AssistantMessage)
	if first == nil || len(first.Content) != 2 || first.StopReason != ai.StopReasonToolUse {
		t.Fatalf("one API message split across records was not joined: %#v", messages[1])
	}
	if result, _ := messages[2].(*ai.ToolResultMessage); result == nil || result.ToolName != "Read" {
		t.Fatalf("tool result = %#v", messages[2])
	}
	// Claude resumes from its own five records, not from Orb's rewrite.
	if records := rebuild(manager, 0); len(records) != 5 || records[4]["uuid"] != "a3" {
		t.Fatalf("resume records = %d", len(records))
	}
}

// Orb names a session's transcript directory as the CLI does; the expected
// names come from the SDK's own function.
func TestProjectDirMatchesClaudeCode(t *testing.T) {
	long := "/Users/me/" + strings.Repeat("très-long-dossier-", 12) + "/projet"
	for cwd, want := range map[string]string{
		"/private/tmp/work dir/x": "-private-tmp-work-dir-x",
		long:                      "-Users-me-" + strings.Repeat("tr-s-long-dossier-", 10) + "tr-s-long--gg0ona",
	} {
		if got := filepath.Base(projectDir("/c", cwd)); got != want {
			t.Errorf("%q: got %q, want %q", cwd, got, want)
		}
	}
}
