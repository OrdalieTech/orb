package claudesessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
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
		raw, _ := json.Marshal(record.orb["message"].(map[string]any)["content"])
		got = append(got, record.orb["type"].(string)+" "+string(raw))
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
	if again := rebuild(manager, 1); again[3].UUID != records[3].UUID || records[3].Parent != records[2].UUID {
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
		`{"type":"user","uuid":"u1","parentUuid":null,"cwd":"/work","message":{"role":"user","content":"read a.go"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"id":"m1","model":"claude","content":[{"type":"text","text":"Reading."}]}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"a1","message":{"id":"m1","model":"claude","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package a"}]},"toolUseResult":{"file":{"content":"display copy"}}}`,
		`{"type":"assistant","uuid":"a3","parentUuid":"u2","message":{"id":"m2","model":"claude","content":[{"type":"text","text":"It is package a."}]}}`,
		// A rewind left this branch behind; the conversation goes on from a3 below.
		`{"type":"user","uuid":"u9","parentUuid":"a3","message":{"role":"user","content":"abandoned question"}}`,
		`{"type":"assistant","uuid":"a9","parentUuid":"u9","message":{"id":"m9","model":"claude","content":[{"type":"text","text":"abandoned answer"}]}}`,
		`{"type":"user","uuid":"u4","parentUuid":"a3","message":{"role":"user","content":"and b.go?"}}`,
		`{"type":"assistant","uuid":"a4","parentUuid":"u4","message":{"id":"m4","model":"claude","content":[{"type":"text","text":"No b.go."}]}}`,
		`{"type":"file-history-snapshot","messageId":"x"}`,
	}
	file := []byte(strings.Join(lines, "\n"))
	if err := os.WriteFile(filepath.Join(project, "0b7a4a1e-1111-4222-8333-444455556666.jsonl"), file, 0o600); err != nil {
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
	if cwd != "/work" || len(messages) != 6 {
		t.Fatalf("imported %d messages in %q", len(messages), cwd)
	}
	first, _ := messages[1].(*ai.AssistantMessage)
	if first == nil || len(first.Content) != 2 || first.StopReason != ai.StopReasonToolUse {
		t.Fatalf("one API message split across records was not joined: %#v", messages[1])
	}
	if result, _ := messages[2].(*ai.ToolResultMessage); result == nil || result.ToolName != "Read" || result.Content[0].(*ai.TextContent).Text != "package a" {
		t.Fatalf("tool result = %#v", messages[2])
	}
	// Orb keeps Claude's records without Claude Code's display copy of tool
	// output, and they still stand for the records the session file holds.
	for _, entry := range manager.GetEntries() {
		if strings.Contains(string(entry.Data), "display copy") {
			t.Fatalf("kept Claude Code's display copy: %s", entry.Data)
		}
	}
	if missing, at, err := planTranscript(rebuild(manager, 0), parentsIn(file), "0b7a4a1e-1111-4222-8333-444455556666"); err != nil || len(missing) > 0 || at != "a4" {
		t.Fatalf("resuming the imported session rewrites it: %q at %q, %v", missing, at, err)
	}
	if raw, _ := json.Marshal(messages); strings.Contains(string(raw), "abandoned") {
		t.Fatalf("imported a branch the conversation left: %s", raw)
	}
	// Claude resumes from its own records, not from Orb's rewrite.
	if records := rebuild(manager, 0); len(records) != 7 || records[6].UUID != "a4" {
		t.Fatalf("resume records = %d", len(records))
	}
}

// An imported session reads as a native Claude turn in Orb: signed reasoning
// (an empty block stays invisible but keeps its signature), images, usage,
// timestamps, and as notices what Orb shows as activity; what Claude Code
// hides stays hidden.
func TestImportShowsClaudeTurnsAsOrbDoes(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "projects", "-work")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	png := strings.Repeat("iVBO", 512)
	lines := []string{
		`{"type":"user","uuid":"u1","parentUuid":null,"cwd":"/work","timestamp":"2026-10-01T10:00:00.000Z","origin":{"kind":"human"},"message":{"role":"user","content":[{"type":"text","text":"look [Image #1]"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}]}}`,
		`{"type":"user","uuid":"m0","parentUuid":"u1","isMeta":true,"message":{"role":"user","content":"<system-reminder>hidden</system-reminder>"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"m0","timestamp":"2026-10-01T10:00:01.000Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"","signature":"sig-empty"}],"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":5}}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"a1","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"Read it first.","signature":"sig-text"}],"usage":{"input_tokens":10,"output_tokens":20}}}`,
		`{"type":"assistant","uuid":"a3","parentUuid":"a2","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"redacted_thinking","data":"opaque"}]}}`,
		`{"type":"assistant","uuid":"a4","parentUuid":"a3","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}],"usage":{"input_tokens":10,"output_tokens":40}}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a4","timestamp":"2026-10-01T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"package a"}]}]}}`,
		`{"type":"user","uuid":"u3","parentUuid":"u2","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"c1","parentUuid":null,"logicalParentUuid":"u3","compactMetadata":{"trigger":"auto","preTokens":9000,"postTokens":1200}}`,
		`{"type":"user","uuid":"s1","parentUuid":"c1","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"This session is being continued from a previous conversation."}}`,
		`{"type":"user","uuid":"u4","parentUuid":"s1","message":{"role":"user","content":"<command-message>model</command-message>\n<command-name>/model</command-name>\n<command-args>opus</command-args>"}}`,
		`{"type":"user","uuid":"u5","parentUuid":"u4","message":{"role":"user","content":"<local-command-stdout>Set model to opus</local-command-stdout>"}}`,
		`{"type":"user","uuid":"u6","parentUuid":"u5","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<summary>Background command \"make\" completed (exit code 0)</summary>\n</task-notification>"}}`,
		`{"type":"assistant","uuid":"a5","parentUuid":"u6","message":{"id":"m2","model":"claude-opus-5-5","content":[{"type":"text","text":"Build passed."}],"stop_reason":"max_tokens"}}`,
		`{"type":"assistant","uuid":"a6","parentUuid":"a5","isApiErrorMessage":true,"error":"unknown","message":{"id":"m3","model":"<synthetic>","content":[{"type":"text","text":"API Error: overloaded"}]}}`,
	}
	if err := os.WriteFile(filepath.Join(project, "0b7a4a1e-1111-4222-8333-444455556666.jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := ImportClaudeCode("0b7a4a1e-1111-4222-8333-444455556666", []string{"CLAUDE_CONFIG_DIR=" + base}, func(dir string) (*session.SessionManager, error) {
		return session.InMemory(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range manager.GetBranch() {
		if entry.Type == "custom_message" {
			var text string
			_ = json.Unmarshal(entry.Content, &text)
			got = append(got, "notice "+text)
		}
		if entry.Type != "message" {
			continue
		}
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(message)
		got = append(got, string(raw))
	}
	want := []string{
		`{"role":"user","content":[{"type":"text","text":"look [Image #1]"},{"type":"image","data":"` + png + `","mimeType":"image/png"}],"timestamp":1790848800000}`,
		`{"role":"assistant","content":[{"type":"thinking","thinking":"","thinkingSignature":"sig-empty"},{"type":"thinking","thinking":"Read it first.","thinkingSignature":"sig-text"},{"type":"thinking","thinking":"[Reasoning redacted]","thinkingSignature":"opaque","redacted":true},{"type":"toolCall","id":"t1","name":"Read","arguments":{"file_path":"a.go"}}],"api":"claude-sessions","provider":"claude-sessions","model":"default","usage":{"input":10,"output":40,"cacheRead":100,"cacheWrite":5,"totalTokens":155,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":1790848801000,"responseId":"m1","responseModel":"claude-opus-5-5"}`,
		`{"role":"toolResult","toolCallId":"t1","toolName":"Read","content":[{"type":"text","text":"package a"}],"isError":false,"timestamp":1790848802000}`,
		`notice Claude compacted context from 9000 tokens to 1200`,
		`{"role":"user","content":"/model opus","timestamp":0}`,
		`notice Set model to opus`,
		`notice Claude task completed: Background command "make" completed (exit code 0)`,
		`{"role":"assistant","content":[{"type":"text","text":"Build passed."}],"api":"claude-sessions","provider":"claude-sessions","model":"default","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"length","timestamp":0,"responseId":"m2","responseModel":"claude-opus-5-5"}`,
		`{"role":"assistant","content":[],"api":"claude-sessions","provider":"claude-sessions","model":"default","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"error","timestamp":0,"responseId":"m3","responseModel":"\u003csynthetic\u003e","errorMessage":"API Error: overloaded"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("imported:\n%s", strings.Join(got, "\n"))
	}
	// Claude resumes from its own records, signatures and all.
	if records := rebuild(manager, 0); len(records) != len(lines) || !strings.Contains(string(records[2].raw), `"sig-empty"`) {
		t.Fatalf("resume records = %d", len(records))
	}
	// Orb keeps the screenshot once, in its message: Claude's record refers to it,
	// and a session file written for another account gets it back whole.
	for _, entry := range manager.GetEntries() {
		if entry.CustomType == transcriptEntry && strings.Contains(string(entry.Data), png) {
			t.Fatal("kept the image twice")
		}
	}
	elsewhere := t.TempDir()
	if _, _, err := syncTranscript(rebuild(manager, 0), "0b7a4a1e-1111-4222-8333-444455556666", elsewhere, nil); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(elsewhere, "0b7a4a1e-1111-4222-8333-444455556666.jsonl"))
	if err != nil || !strings.Contains(string(written), `"data":"`+png+`"`) || strings.Contains(string(written), imageRef) {
		t.Fatalf("rewritten session lost the image: %v", err)
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

// A native failure with no reply (a context too long for the model) ends the
// turn with an error reply the user sees, as Orb's own loop does.
func TestFailedResultShowsAsErrorReply(t *testing.T) {
	_, driver := fixture(t)
	var replies []*ai.AssistantMessage
	tr := translation{driver: driver, ctx: t.Context(), tools: map[string]string{}, emit: func(_ context.Context, event engine.AgentEvent) error {
		if end, ok := event.(engine.MessageEndEvent); ok {
			replies = append(replies, end.Message.(*ai.AssistantMessage))
		}
		return nil
	}}
	if err := tr.event([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["Prompt is too long"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || replies[0].StopReason != ai.StopReasonError || *replies[0].ErrorMessage != "Prompt is too long" {
		t.Fatalf("replies = %#v", replies)
	}
}

// A record whose parent is missing from the rebuild links to the record
// before it, so Claude reads the whole chain back.
func TestRebuildRepairsMissingParents(t *testing.T) {
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	records := []map[string]any{
		{"type": "user", "uuid": "u1", "parentUuid": nil, "message": map[string]any{"role": "user", "content": "one"}},
		{"type": "assistant", "uuid": "a1", "parentUuid": "attachment-not-mirrored", "message": map[string]any{"role": "assistant", "content": []any{}}},
	}
	if _, err := manager.AppendCustomEntry(transcriptEntry, records); err != nil {
		t.Fatal(err)
	}
	if got := rebuild(manager, 0); len(got) != 2 || got[1].Parent != "u1" {
		t.Fatalf("records = %#v", got)
	}
}

// Each turn mirrors only what Claude appended since the last one; a line still
// being written waits, and a rewritten transcript is read again without repeats.
func TestMirrorReadsOnlyTheNewTail(t *testing.T) {
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	write := func(flag int, text string) {
		file, err := os.OpenFile(path, flag|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = file.WriteString(text)
			_ = file.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	mirrored, read := map[string]bool{}, map[string]int64{}
	uuids := func() (got []string) {
		for _, entry := range manager.GetEntries() {
			var records []struct{ UUID string }
			if entry.CustomType == transcriptEntry && json.Unmarshal(entry.Data, &records) == nil {
				for _, record := range records {
					got = append(got, record.UUID)
				}
			}
		}
		return got
	}
	step := func(want string) {
		t.Helper()
		if err := mirror(manager, dir, "s", mirrored, read); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(uuids(), " "); got != want {
			t.Fatalf("mirrored %q, want %q", got, want)
		}
	}
	write(os.O_TRUNC, `{"type":"user","uuid":"u1"}`+"\n"+`{"type":"assistant","uu`)
	step("u1")
	write(os.O_APPEND, `id":"a1"}`+"\n")
	step("u1 a1")
	write(os.O_TRUNC, `{"type":"user","uuid":"u1"}`+"\n")
	write(os.O_APPEND, `{"type":"user","uuid":"u2"}`+"\n")
	step("u1 a1 u2")
}

// Syncing appends only what the session lacks; a record whose parent moved
// (Orb compacted the branch) comes back as a copy, and records after it follow.
func TestSyncTranscriptAppendsWhatTheSessionLacks(t *testing.T) {
	dir := t.TempDir()
	line := func(id, parent string) *record {
		return &record{UUID: id, Parent: parent, orb: map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": id}}}
	}
	lines := func() []string {
		data, _ := os.ReadFile(filepath.Join(dir, "s.jsonl"))
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	// Each sync reuses what the last one read and wrote of the file.
	var known fileLinks
	at, size, err := syncTranscript([]*record{line("r1", ""), line("r2", "r1")}, "s", dir, &known)
	if err != nil || at != "r2" || len(lines()) != 3 || lines()[2] != `{"leafUuid":"r2","sessionId":"s","type":"last-prompt"}` {
		t.Fatalf("first sync: %q %v %v", at, err, lines())
	}
	if again, same, err := syncTranscript([]*record{line("r1", ""), line("r2", "r1")}, "s", dir, &known); err != nil || again != "r2" || same != size || len(lines()) != 3 {
		t.Fatalf("second sync appended: %q %d %v", again, same, lines())
	}
	at, _, err = syncTranscript([]*record{line("sum", ""), line("r2", "sum"), line("r3", "r2")}, "s", dir, &known)
	copied := uuidFor("r2 sum")
	if err != nil || len(lines()) != 7 || !strings.Contains(lines()[4], `"uuid":"`+copied+`"`) || !strings.Contains(lines()[5], `"parentUuid":"`+copied+`"`) || at != "r3" {
		t.Fatalf("compacted sync: %q %v %v", at, err, lines())
	}
	// A record Claude Code wrote meanwhile is read before the next sync.
	file, err := os.OpenFile(filepath.Join(dir, "s.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"type":"user","uuid":"x","parentUuid":"r3"}` + "\n")
	_ = file.Close()
	if at, _, err = syncTranscript([]*record{line("sum", ""), line("r2", "sum"), line("r3", "r2"), line("x", "r3")}, "s", dir, &known); err != nil || len(lines()) != 8 || at != "x" {
		t.Fatalf("sync after Claude Code wrote: %q %v %v", at, err, lines())
	}
}

// A conversation continued in Claude Code gains those turns when Orb opens it
// again, once; turns on a branch Orb does not hold are left alone.
func TestCatchUpTakesTurnsAddedInClaudeCode(t *testing.T) {
	base := t.TempDir()
	id := "0b7a4a1e-1111-4222-8333-444455556666"
	project := filepath.Join(base, "projects", "-work")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, id+".jsonl")
	write := func(lines ...string) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = file.WriteString(strings.Join(lines, "\n") + "\n")
			_ = file.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"user","uuid":"u1","parentUuid":null,"cwd":"/work","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"id":"m1","model":"claude","content":[{"type":"text","text":"hi"}]}}`)
	env := []string{"CLAUDE_CONFIG_DIR=" + base}
	manager, err := ImportClaudeCode(id, env, func(dir string) (*session.SessionManager, error) {
		return session.InMemory(dir, session.WithSessionID(id))
	})
	if err != nil {
		t.Fatal(err)
	}
	texts := func() (got []string) {
		for _, raw := range manager.BuildSessionContext().Messages {
			message, _ := ai.UnmarshalMessage(raw)
			switch m := message.(type) {
			case *ai.UserMessage:
				got = append(got, *m.Content.Text)
			case *ai.AssistantMessage:
				got = append(got, m.Content[0].(*ai.TextContent).Text)
			}
		}
		return got
	}
	// The Claude model chosen in Orb stays chosen.
	if _, err := manager.AppendModelChange(Name, "opus"); err != nil {
		t.Fatal(err)
	}
	write(`{"type":"user","uuid":"u2","parentUuid":"a1","cwd":"/work","message":{"role":"user","content":"from claude code"}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"u2","message":{"id":"m2","model":"claude","content":[{"type":"text","text":"noted"}]}}`)
	for range 2 {
		if _, err := catchUp(manager, env); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(texts(), "|"); got != "hello|hi|from claude code|noted" {
		t.Fatalf("messages = %s", got)
	}
	if model := manager.BuildSessionContext().Model; model == nil || model.ModelID != "opus" {
		t.Fatalf("model after catch-up = %+v", model)
	}
	if records := rebuild(manager, 0); records[len(records)-1].UUID != "a2" {
		t.Fatalf("Claude would resume at %v", records[len(records)-1].UUID)
	}
}
