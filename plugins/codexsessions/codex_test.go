package codexsessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
)

const threadID = "01a102e2-af35-7692-82f4-a4094cb943e7"

// Anonymized from Codex CLI 0.159 rollouts: the record shapes are real, the values are not.
var rollout = []string{
	`{"timestamp":"2026-10-03T17:49:32.000Z","ordinal":0,"type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/work","cli_version":"0.159.0","source":"cli","model_provider":"openai","base_instructions":{"text":"You are Codex."},"history_mode":"paginated"}}`,
	`{"timestamp":"2026-10-03T17:49:32.100Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
	`{"timestamp":"2026-10-03T17:49:32.200Z","ordinal":2,"type":"response_item","payload":{"type":"message","id":"m0","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>sandboxed</permissions instructions>"}]}}`,
	`{"timestamp":"2026-10-03T17:49:32.300Z","ordinal":3,"type":"response_item","payload":{"type":"message","id":"m1","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /work\n\nBe brief."},{"type":"input_text","text":"<environment_context>\n  <cwd>/work</cwd>\n</environment_context>"}]}}`,
	`{"timestamp":"2026-10-03T17:49:32.400Z","ordinal":4,"type":"world_state","payload":{"full":true,"state":{}}}`,
	`{"timestamp":"2026-10-03T17:49:32.500Z","ordinal":5,"type":"turn_context","payload":{"turn_id":"t1","cwd":"/work","model":"gpt-6.1-sol","effort":"high"}}`,
	`{"timestamp":"2026-10-03T17:49:33.000Z","ordinal":6,"type":"response_item","payload":{"type":"message","id":"m2","role":"user","content":[{"type":"input_text","text":"<image name=[Image #1]>"},{"type":"input_image","image_url":"data:image/png;base64,iVBO"},{"type":"input_text","text":"</image>"},{"type":"input_text","text":"why does make fail? [Image #1]"}],"internal_chat_message_metadata_passthrough":{"turn_id":"t1"}}}`,
	`{"timestamp":"2026-10-03T17:49:34.000Z","ordinal":7,"type":"response_item","payload":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Run make first."}],"content":null,"encrypted_content":"gAAAAenc1","internal_chat_message_metadata_passthrough":{"turn_id":"t1"}}}`,
	`{"timestamp":"2026-10-03T17:49:34.100Z","ordinal":8,"type":"response_item","payload":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Running make."}],"phase":"commentary"}}`,
	`{"timestamp":"2026-10-03T17:49:34.200Z","ordinal":9,"type":"response_item","payload":{"type":"function_call","id":"fc_1","name":"exec_command","arguments":"{\"cmd\":\"make\",\"workdir\":\"/work/sub\",\"max_output_tokens\":4000}","call_id":"call_1"}}`,
	`{"timestamp":"2026-10-03T17:49:34.300Z","ordinal":10,"type":"response_item","payload":{"type":"function_call","id":"fc_2","name":"get_issue","namespace":"mcp__tracker__","arguments":"{\"id\":7}","call_id":"call_2"}}`,
	`{"timestamp":"2026-10-03T17:49:35.000Z","ordinal":11,"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"Process exited with code 2\nOutput:\nmake: *** No rule"}}`,
	`{"timestamp":"2026-10-03T17:49:35.100Z","ordinal":12,"type":"response_item","payload":{"type":"function_call_output","call_id":"call_2","output":[{"type":"input_text","text":"issue 7"},{"type":"input_image","image_url":"data:image/jpeg;base64,/9j/"},{"type":"encrypted_content","encrypted_content":"gAAAAenc2"}]}}`,
	`{"timestamp":"2026-10-03T17:49:35.200Z","ordinal":13,"type":"event_msg","payload":{"type":"token_count","info":null}}`,
	`{"timestamp":"2026-10-03T17:49:36.000Z","ordinal":14,"type":"response_item","payload":{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"gAAAAenc3"}}`,
	`{"timestamp":"2026-10-03T17:49:36.100Z","ordinal":15,"type":"response_item","payload":{"type":"custom_tool_call","id":"ctc_1","status":"completed","call_id":"call_3","name":"apply_patch","input":"*** Begin Patch\n*** Add File: Makefile\n+all:\n*** End Patch"}}`,
	`{"timestamp":"2026-10-03T17:49:36.200Z","ordinal":16,"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_3","output":[{"type":"input_text","text":"Success. Updated the following files:\nA Makefile"}]}}`,
	`{"timestamp":"2026-10-03T17:49:36.300Z","ordinal":17,"type":"response_item","payload":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"make no rule","queries":["make no rule"]}}}`,
	`{"timestamp":"2026-10-03T17:49:37.000Z","ordinal":18,"type":"response_item","payload":{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"Added a Makefile."}],"phase":"final_answer"}}`,
	`{"timestamp":"2026-10-03T17:49:37.100Z","ordinal":19,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
	`{"timestamp":"2026-10-03T17:49:37.150Z","ordinal":20,"type":"response_item","payload":{"type":"tool_search_call","call_id":"call_9","status":"completed","execution":"client","arguments":{"query":"calendar"}}}`,
	`{"timestamp":"2026-10-03T17:49:37.200Z","ordinal":21,"type":"response_item","payload":{"type":"future_item","id":"x1"}}`,
	`{"timestamp":"2026-10-03T17:49:37.300Z","ordinal":22,"type":"future_record","payload":{}}`,
	`not json`,
}

// codexHomeWith lays out a Codex home: the thread index in state_5.sqlite and
// the rollout it names.
func codexHomeWith(t *testing.T, name string, lines ...string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	path = filepath.Join(home, "sessions", "2026", "10", "03", "rollout-2026-10-03T19-49-32-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index(t, home, threadID, path, name)
	return home, path
}

func index(t *testing.T, home, id, path, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, cwd TEXT NOT NULL, title TEXT NOT NULL, name TEXT, model TEXT)`,
		`INSERT INTO threads VALUES ('` + id + `', '` + path + `', '/work', 'why does make fail?', ` + map[bool]string{true: "NULL", false: "'" + name + "'"}[name == ""] + `, 'gpt-6.1-sol')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func importThread(t *testing.T, home string) (*session.SessionManager, string) {
	t.Helper()
	var cwd string
	manager, err := ImportCodex(threadID, []string{"CODEX_HOME=" + home}, func(dir string) (*session.SessionManager, error) {
		cwd = dir
		return session.InMemory(dir, session.WithSessionID(threadID))
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, cwd
}

// contextMessages decodes the conversation's context; Orb's own roles
// (custom, compactionSummary) stay nil and are read from the raw JSON.
func contextMessages(t *testing.T, manager *session.SessionManager) ([]ai.Message, []string) {
	t.Helper()
	var messages []ai.Message
	var raws []string
	for _, raw := range manager.BuildSessionContext().Messages {
		message, _ := ai.UnmarshalMessage(raw)
		messages, raws = append(messages, message), append(raws, string(raw))
	}
	return messages, raws
}

// A Codex thread reads as an Orb conversation: what the user typed, Codex's
// answers, reasoning summaries as thinking that keeps its encrypted item,
// tools under Orb's names where Orb has one, images; Codex's injected context
// stays out.
func TestImportCodexThread(t *testing.T) {
	home, _ := codexHomeWith(t, "Fix the build", rollout...)
	manager, cwd := importThread(t, home)
	if cwd != "/work" {
		t.Fatalf("cwd = %q", cwd)
	}
	if name := manager.GetSessionName(); name == nil || *name != "Fix the build" {
		t.Fatalf("name = %v", name)
	}
	messages, raws := contextMessages(t, manager)
	if len(messages) != 10 {
		raw, _ := json.MarshalIndent(messages, "", " ")
		t.Fatalf("imported %d messages:\n%s", len(messages), raw)
	}
	user, _ := messages[0].(*ai.UserMessage)
	if user == nil || len(user.Content.Blocks) != 2 {
		t.Fatalf("user = %#v", messages[0])
	}
	if image, _ := user.Content.Blocks[0].(*ai.ImageContent); image == nil || image.Data != "iVBO" || image.MimeType != "image/png" {
		t.Fatalf("image = %#v", user.Content.Blocks[0])
	}
	if text, _ := user.Content.Blocks[1].(*ai.TextContent); text == nil || text.Text != "why does make fail? [Image #1]" {
		t.Fatalf("text = %#v", user.Content.Blocks[1])
	}

	reply, _ := messages[1].(*ai.AssistantMessage)
	if reply == nil || len(reply.Content) != 4 || reply.Provider != "openai-codex" || reply.API != ai.APIOpenAICodexResponses ||
		reply.Model != "gpt-6.1-sol" || reply.StopReason != ai.StopReasonToolUse || reply.Timestamp == 0 {
		t.Fatalf("reply = %#v", messages[1])
	}
	thinking, _ := reply.Content[0].(*ai.ThinkingContent)
	if thinking == nil || thinking.Thinking != "Run make first." || thinking.ThinkingSignature == nil {
		t.Fatalf("thinking = %#v", reply.Content[0])
	}
	var signature map[string]any
	if err := json.Unmarshal([]byte(*thinking.ThinkingSignature), &signature); err != nil || signature["type"] != "reasoning" ||
		signature["id"] != "rs_1" || signature["encrypted_content"] != "gAAAAenc1" || signature["internal_chat_message_metadata_passthrough"] != nil {
		t.Fatalf("signature = %s", *thinking.ThinkingSignature)
	}
	if text, _ := reply.Content[1].(*ai.TextContent); text == nil || text.Text != "Running make." || text.TextSignature == nil ||
		*text.TextSignature != `{"v":1,"id":"msg_1","phase":"commentary"}` {
		t.Fatalf("text = %#v", reply.Content[1])
	}
	bash, _ := reply.Content[2].(*ai.ToolCall)
	if bash == nil || bash.ID != "call_1|fc_1" || bash.Name != "bash" || bash.Arguments["command"] != "cd /work/sub && make" {
		t.Fatalf("bash = %#v", reply.Content[2])
	}
	mcp, _ := reply.Content[3].(*ai.ToolCall)
	if mcp == nil || mcp.Name != "get_issue" || mcp.Namespace == nil || *mcp.Namespace != "mcp__tracker__" || mcp.Arguments["id"] != float64(7) {
		t.Fatalf("mcp = %#v", reply.Content[3])
	}
	if result, _ := messages[2].(*ai.ToolResultMessage); result == nil || result.ToolCallID != "call_1|fc_1" || result.ToolName != "bash" ||
		len(result.Content) != 1 || !strings.Contains(result.Content[0].(*ai.TextContent).Text, "No rule") {
		t.Fatalf("bash result = %#v", messages[2])
	}
	if result, _ := messages[3].(*ai.ToolResultMessage); result == nil || result.ToolName != "get_issue" || len(result.Content) != 2 {
		t.Fatalf("mcp result = %#v", messages[3])
	}

	// Reasoning without a summary stays invisible but keeps its encrypted item.
	patch, _ := messages[4].(*ai.AssistantMessage)
	if patch == nil || len(patch.Content) != 2 {
		t.Fatalf("patch turn = %#v", messages[4])
	}
	if thinking, _ := patch.Content[0].(*ai.ThinkingContent); thinking == nil || thinking.Thinking != "" || !strings.Contains(*thinking.ThinkingSignature, "gAAAAenc3") {
		t.Fatalf("hidden reasoning = %#v", patch.Content[0])
	}
	if call, _ := patch.Content[1].(*ai.ToolCall); call == nil || call.Name != "apply_patch" || !strings.HasPrefix(call.Arguments["input"].(string), "*** Begin Patch") {
		t.Fatalf("apply_patch = %#v", patch.Content[1])
	}
	if result, _ := messages[5].(*ai.ToolResultMessage); result == nil || result.ToolName != "apply_patch" {
		t.Fatalf("apply_patch result = %#v", messages[5])
	}
	if search, _ := messages[6].(*ai.AssistantMessage); search == nil || search.Content[0].(*ai.ToolCall).Name != "web_search" ||
		search.Content[0].(*ai.ToolCall).Arguments["query"] != "make no rule" {
		t.Fatalf("web search = %#v", messages[6])
	}
	if result, _ := messages[7].(*ai.ToolResultMessage); result == nil || result.ToolName != "web_search" {
		t.Fatalf("web search result = %#v", messages[7])
	}
	if final, _ := messages[8].(*ai.AssistantMessage); final == nil || final.Content[0].(*ai.TextContent).Text != "Added a Makefile." || final.StopReason != ai.StopReasonStop {
		t.Fatalf("final = %#v", messages[8])
	}
	if raw, _ := json.Marshal(messages); strings.Contains(string(raw), "AGENTS.md") || strings.Contains(string(raw), "environment_context") ||
		strings.Contains(string(raw), "permissions instructions") || strings.Contains(string(raw), "<image") {
		t.Fatalf("imported Codex's injected context: %s", raw)
	}
	// Records Orb cannot read are skipped and named once.
	if !strings.Contains(raws[9], `"role":"custom"`) || !strings.Contains(raws[9], "3 Codex records") ||
		!strings.Contains(raws[9], "future_item") || !strings.Contains(raws[9], "future_record") {
		t.Fatalf("diagnostic = %s", raws[9])
	}
}

// A compaction keeps the messages Codex kept, as Orb's own compaction would.
func TestImportCodexCompaction(t *testing.T) {
	home, _ := codexHomeWith(t, "",
		rollout[0], rollout[5],
		`{"timestamp":"2026-10-03T17:50:00.000Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"first question"}]}}`,
		`{"timestamp":"2026-10-03T17:50:01.000Z","type":"response_item","payload":{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,
		`{"timestamp":"2026-10-03T17:50:02.000Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /work"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]},{"type":"compaction","encrypted_content":"gAAAAenc4"}]}}`,
		`{"timestamp":"2026-10-03T17:50:03.000Z","type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"second question"}]}}`,
	)
	manager, _ := importThread(t, home)
	if manager.GetSessionName() != nil {
		t.Fatalf("an unnamed thread got a name: %v", *manager.GetSessionName())
	}
	messages, raws := contextMessages(t, manager)
	if len(messages) != 2 || !strings.Contains(raws[0], `"role":"compactionSummary"`) || !strings.Contains(raws[0], "first question") ||
		strings.Contains(raws[0], "first answer") || strings.Contains(raws[0], "AGENTS.md") || !strings.Contains(raws[1], "second question") {
		t.Fatalf("context = %v", raws)
	}
}

// Opening the conversation again takes in the turns Codex added since, until
// the conversation goes on in Orb.
func TestCatchUpAppendsCodexTurns(t *testing.T) {
	home, path := codexHomeWith(t, "", rollout[0], rollout[5], rollout[6], rollout[18])
	manager, _ := importThread(t, home)
	env := []string{"CODEX_HOME=" + home}
	start := func() {
		t.Helper()
		if err := catchUp(context.Background(), manager, env); err != nil {
			t.Fatal(err)
		}
	}
	start()
	if messages, _ := contextMessages(t, manager); len(messages) != 2 {
		t.Fatalf("an unchanged thread changed the conversation: %d messages", len(messages))
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(rollout[9] + "\n" + rollout[11] + "\n" + `{"type":"response_item","payload":{"type":"message","role":"user"`)
	_ = file.Close()
	start()
	messages, _ := contextMessages(t, manager)
	if len(messages) != 4 {
		t.Fatalf("catch-up = %d messages", len(messages))
	}
	if result, _ := messages[3].(*ai.ToolResultMessage); result == nil || result.ToolName != "bash" {
		t.Fatalf("caught-up result = %#v", messages[3])
	}
	// The half-written line is read once Codex finishes it.
	file, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = file.WriteString(`,"content":[{"type":"input_text","text":"thanks"}]}}` + "\n")
	_ = file.Close()
	start()
	if messages, _ = contextMessages(t, manager); len(messages) != 5 {
		t.Fatalf("finished line = %d messages", len(messages))
	}
	if _, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText("asked in Orb")}); err != nil {
		t.Fatal(err)
	}
	file, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = file.WriteString(rollout[18] + "\n")
	_ = file.Close()
	start()
	if messages, _ = contextMessages(t, manager); len(messages) != 6 {
		t.Fatalf("Codex turns joined a conversation Orb went on with: %d messages", len(messages))
	}
}

// A forked thread starts from the history it shares with its parent.
func TestImportForkedThread(t *testing.T) {
	parentID := "01a0f000-0000-7000-8000-000000000001"
	parentLines := []string{rollout[0], rollout[5], rollout[6], rollout[18], `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"after the fork"}]}}`}
	home, _ := codexHomeWith(t, "",
		`{"type":"session_meta","payload":{"id":"`+threadID+`","cwd":"/work","model_provider":"openai","forked_from_id":"`+parentID+`","history_base":{"thread_id":"`+parentID+`","end_ordinal_exclusive":4,"end_byte_offset":`+itoa(len(strings.Join(parentLines[:4], "\n"))+1)+`}}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"in the fork"}]}}`,
	)
	parent := filepath.Join(home, "parent.jsonl")
	if err := os.WriteFile(parent, []byte(strings.Join(parentLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index(t, home, parentID, parent, "")
	manager, _ := importThread(t, home)
	messages, _ := contextMessages(t, manager)
	raw, _ := json.Marshal(messages)
	if len(messages) != 3 || strings.Contains(string(raw), "after the fork") || !strings.Contains(string(raw), "in the fork") {
		t.Fatalf("fork = %s", raw)
	}
}

func TestImportUnknownThread(t *testing.T) {
	home, _ := codexHomeWith(t, "", rollout...)
	for _, env := range [][]string{{"CODEX_HOME=" + home}, {"CODEX_HOME=" + t.TempDir()}} {
		_, err := ImportCodex("01a0ffff-0000-7000-8000-000000000000", env, func(string) (*session.SessionManager, error) {
			t.Fatal("created a conversation")
			return nil, nil
		})
		if err != ErrNoCodexSession {
			t.Fatalf("err = %v", err)
		}
	}
}

func itoa(n int) string { raw, _ := json.Marshal(n); return string(raw) }
