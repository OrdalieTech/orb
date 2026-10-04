package memtree

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
)

type fakeSessions struct {
	extensions.ReadonlySessionManager
	mu      sync.Mutex
	entries []session.SessionEntry
}

func (*fakeSessions) GetSessionID() string { return "s1" }
func (*fakeSessions) IsPersisted() bool    { return true }
func (s *fakeSessions) GetBranch(...string) []session.SessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.SessionEntry(nil), s.entries...)
}

func (s *fakeSessions) BuildSessionContext() session.SessionContext {
	var context session.SessionContext
	for _, entry := range s.GetBranch() {
		context.Messages = append(context.Messages, entry.Message)
	}
	return context
}

func (s *fakeSessions) add(role string, content any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("e%d", len(s.entries))
	raw, _ := json.Marshal(map[string]any{"role": role, "content": content})
	s.entries = append(s.entries, session.SessionEntry{Type: "message", ID: id, Timestamp: "2026-10-04T10:00:00.000Z", Message: raw})
	return id
}

type fakeContext struct {
	extensions.Context
	sessions *fakeSessions
}

func (c *fakeContext) SessionManager() extensions.ReadonlySessionManager { return c.sessions }
func (*fakeContext) Model() *ai.Model                                    { return nil }
func (*fakeContext) ModelRegistry() extensions.ModelRegistry             { return nil }
func (*fakeContext) HasUI() bool                                         { return false }

// compactor stands in for the model: a 300-byte line per message or merge, recording the
// order messages are compressed in and every request it saw.
type compactor struct {
	mu         sync.Mutex
	compressed []int
	requests   []string
}

var messageNumber = regexp.MustCompile(`message (\d+) x`)

func (c *compactor) ask(_ context.Context, messages ai.MessageList) (*ai.AssistantMessage, error) {
	first := messages[0].(*ai.UserMessage).Content.Blocks
	chat, step := first[0].(*ai.TextContent).Text, first[1].(*ai.TextContent).Text
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, chat)
	line := "merged " + strings.Repeat("s", 293)
	if strings.Contains(step, "Compress this message") {
		match := messageNumber.FindStringSubmatch(step)
		number, _ := strconv.Atoi(match[1])
		c.compressed = append(c.compressed, number)
		line = fmt.Sprintf("summary of %d %s", number, strings.Repeat("s", 280))
	}
	return &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: line}}}, nil
}

func longSession(n int) *fakeSessions {
	sessions := &fakeSessions{}
	for index := range n {
		sessions.add("user", fmt.Sprintf("message %d %s", index, strings.Repeat("x", 2000)))
	}
	return sessions
}

// idle waits until no compactor call is running; the last one may start merges of its own.
func idle(t *testing.T, tr *tree) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		busy := len(tr.busy)
		tr.mu.Unlock()
		if busy == 0 {
			tr.pump()
			tr.mu.Lock()
			busy = len(tr.busy)
			tr.mu.Unlock()
			if busy == 0 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("compactor never went idle")
}

func TestEntryMessagesKeepWhatWasSaidAndDoneButNotThinking(t *testing.T) {
	raw := func(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }
	entries := []session.SessionEntry{
		{Type: "message", ID: "a", Message: raw(map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "fix it"}, map[string]any{"type": "image"}}})},
		{Type: "message", ID: "b", Message: raw(map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "secret"},
			map[string]any{"type": "text", "text": "Reading."},
			map[string]any{"type": "toolCall", "name": "read", "arguments": map[string]any{"path": "a.go"}},
			map[string]any{"type": "toolCall", "name": "bash", "arguments": map[string]any{"command": "ls"}},
		}})},
		{Type: "message", ID: "c", Message: raw(map[string]any{"role": "toolResult", "isError": true, "content": []any{map[string]any{"type": "text", "text": "no such file"}}})},
		{Type: "message", ID: "d", Message: raw(map[string]any{"role": "bashExecution", "command": "make", "output": "ok"})},
		{Type: "message", ID: "e", Message: raw(map[string]any{"role": "bashExecution", "command": "env", "output": "secret", "excludeFromContext": true})},
		{Type: "custom_message", ID: "f", Content: raw("job finished")},
		{Type: "branch_summary", ID: "g", Summary: "tried another fix"},
		{Type: "compaction", ID: "h", Summary: "old summary"},
		{Type: "message", ID: "i", Message: raw(map[string]any{"role": "system", "content": "prompt"})},
	}
	var got []string
	for _, entry := range entries {
		for _, message := range entryMessages(entry) {
			got = append(got, message.key+" "+message.line())
		}
	}
	want := []string{
		"a/0 user: fix it\n[image]",
		"b/0 talk: Reading.",
		`b/1 tool: read {"path":"a.go"}`,
		`b/2 tool: bash {"command":"ls"}`,
		"c/0 echo: error: no such file",
		"d/0 echo: $ make\nok",
		"f/0 note: job finished",
		"g/0 note: tried another fix",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages:\n%q\nwant\n%q", got, want)
	}
}

func TestMessagesAreCompressedInOrderAndTheViewFitsItsBudget(t *testing.T) {
	sessions := longSession(40)
	tr, err := newTree(sessions, "")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.stop()
	model := &compactor{}
	tr.ask, tr.budget = model.ask, 4000
	tr.pump()
	if !tr.settle(context.Background(), 40, 5*time.Second) {
		t.Fatal("messages were not all summarized")
	}
	idle(t, tr)

	for index, number := range model.compressed {
		if number != index {
			t.Fatalf("compressed out of order: %v", model.compressed)
		}
	}
	for _, chat := range model.requests {
		if strings.Contains(chat, "|") || strings.Contains(chat, "+1") {
			t.Fatalf("compactor saw ids: %q", chat)
		}
	}
	if !strings.Contains(model.requests[1], "summary of 0 ") || strings.Contains(model.requests[1], "summary of 1 ") {
		t.Fatalf("the context of message 1 is the view before it: %q", model.requests[1])
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	next := 0
	for _, p := range tr.view {
		if p.start() != next || !tr.builtLocked(p.l, p.i) {
			t.Fatalf("view does not tile the log with summaries: %+v", tr.view)
		}
		next = p.start() + 1<<p.l
	}
	if next != 40 || tr.size > tr.budget {
		t.Fatalf("view covers %d messages in %d bytes, budget %d", next, tr.size, tr.budget)
	}
}

func TestTheViewOnlyCoarsensAsMessagesArrive(t *testing.T) {
	sessions := longSession(1)
	tr, err := newTree(sessions, "")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.stop()
	model := &compactor{}
	tr.ask, tr.budget = model.ask, 3000
	var previous []part
	for index := 1; index < 64; index++ {
		sessions.add("user", fmt.Sprintf("message %d %s", index, strings.Repeat("x", 600)))
		tr.pump()
		if !tr.settle(context.Background(), index+1, 5*time.Second) {
			t.Fatal("not summarized")
		}
		idle(t, tr)
		tr.mu.Lock()
		view := append([]part(nil), tr.view...)
		tr.mu.Unlock()
		for _, old := range previous {
			covered := false
			for _, p := range view {
				if p.l >= old.l && p.start() <= old.start() && old.start() < p.start()+1<<p.l {
					covered = true
				}
			}
			if !covered {
				t.Fatalf("line %+v was split: %+v", old, view)
			}
		}
		previous = view
	}
}

func TestCompressShowsTheCutAndKeepsTheShortestTry(t *testing.T) {
	var seen []ai.MessageList
	replies := []int{700, 560, 530, 600, 540}
	ask := func(_ context.Context, messages ai.MessageList) (*ai.AssistantMessage, error) {
		seen = append(seen, messages)
		return &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: strings.Repeat("é", replies[len(seen)-1]/2)}}}, nil
	}
	line, err := compress(context.Background(), ask, ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("step")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != tries || len(line) != 530 {
		t.Fatalf("%d tries, kept %d bytes", len(seen), len(line))
	}
	feedback := *seen[1][2].(*ai.UserMessage).Content.Text
	if !strings.Contains(feedback, "That line is 700 bytes; the limit is 512") || !strings.Contains(feedback, strings.Repeat("é", 256)+"| ← LIMIT") {
		t.Fatalf("feedback: %q", feedback)
	}
}

func TestZoomOpensLinesDownToTheMessage(t *testing.T) {
	sessions := longSession(8)
	tr, err := newTree(sessions, "")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.stop()
	model := &compactor{}
	tr.ask = model.ask
	tr.pump()
	tr.settle(context.Background(), 8, 5*time.Second)
	idle(t, tr)
	if got := tr.zoom(0, 8); !strings.HasPrefix(got, "0+4|merged") || !strings.Contains(got, "\n4+4|merged") {
		t.Fatalf("zoom(0, 8) = %q", got)
	}
	if got := tr.zoom(4, 2); !strings.HasPrefix(got, "4+1|summary of 4 ") || !strings.Contains(got, "\n5+1|summary of 5 ") {
		t.Fatalf("zoom(4, 2) = %q", got)
	}
	if got := tr.zoom(5, 1); got != "5+0|user: message 5 "+strings.Repeat("x", 2000) {
		t.Fatalf("zoom(5, 1) = %.40q", got)
	}
	for _, bad := range [][2]int{{3, 2}, {0, 3}, {8, 1}, {0, 16}} {
		if got := tr.zoom(bad[0], bad[1]); !strings.HasPrefix(got, "No line") {
			t.Fatalf("zoom%v = %q", bad, got)
		}
	}
}

func TestNodesSurviveARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s1.jsonl")
	sessions := longSession(6)
	first, err := newTree(sessions, file)
	if err != nil {
		t.Fatal(err)
	}
	model := &compactor{}
	first.ask = model.ask
	first.pump()
	first.settle(context.Background(), 6, 5*time.Second)
	idle(t, first)
	first.stop()

	second, err := newTree(sessions, file)
	if err != nil {
		t.Fatal(err)
	}
	defer second.stop()
	second.pump() // no compactor: everything comes from the file
	if !second.settle(context.Background(), 6, time.Second) || second.render(6) != first.render(6) {
		t.Fatalf("reloaded view %q, want %q", second.render(6), first.render(6))
	}
}

func TestCompactionKeepsItsCutAndSummarizesWhatCameBefore(t *testing.T) {
	sessions := &fakeSessions{}
	var ids []string
	for index := range 10 {
		ids = append(ids, sessions.add("user", fmt.Sprintf("short %d", index)))
	}
	p := &plugin{}
	event := extensions.SessionBeforeCompactEvent{Preparation: harness.CompactionPreparation{FirstKeptEntryID: ids[7], TokensBefore: 99}}
	result, err := p.compact(context.Background(), event, &fakeContext{sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	compaction := result.(extensions.SessionBeforeCompactResult).Compaction
	if compaction.FirstKeptEntryID != ids[7] || compaction.TokensBefore != 99 {
		t.Fatalf("compaction %+v", compaction)
	}
	if !strings.Contains(compaction.Summary, "<chat>\n0+1|user: short 0\n") || !strings.Contains(compaction.Summary, "6+1|user: short 6\n</chat>") || strings.Contains(compaction.Summary, "short 7") {
		t.Fatalf("summary %q", compaction.Summary)
	}
}

// sessionRuntime runs memtree in a real session over the faux provider, keeping every request.
func sessionRuntime(t *testing.T, settings map[string]any, replies ...faux.ResponseStep) (*agent.SessionRuntime, *session.SessionManager, *[]ai.Context) {
	t.Helper()
	cwd, agentDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"compaction":{"keepRecentTokens":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:memtree>", Extension(agentDir, settings)); err != nil {
		t.Fatal(err)
	}
	provider := faux.New()
	var requests []ai.Context
	steps := make([]faux.ResponseStep, len(replies))
	for index, reply := range replies {
		steps[index] = faux.Factory(func(ctx context.Context, request ai.Context, options *ai.StreamOptions, state faux.State, model *ai.Model) (*ai.AssistantMessage, error) {
			requests = append(requests, request)
			if factory, ok := reply.(faux.ResponseFactory); ok {
				return factory(ctx, request, options, state, model)
			}
			return reply.(*ai.AssistantMessage), nil
		})
	}
	provider.SetResponses(steps)
	created := engine.NewAgent(provider.StreamSimple, engine.WithInitialState(engine.AgentState{SystemPrompt: "test", Model: provider.GetModel()}), engine.WithConvertToLLM(agent.ConvertToLLM))
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: configured, ExtensionRegistry: registry, ExtensionMode: extensions.ModePrint})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	return runtime, manager, &requests
}

func userTexts(request ai.Context) []string {
	var texts []string
	for _, message := range request.Messages {
		if user, ok := message.(*ai.UserMessage); ok {
			if user.Content.Text != nil {
				texts = append(texts, *user.Content.Text)
			} else {
				texts = append(texts, ai.ContentText(user.Content.Blocks))
			}
		}
	}
	return texts
}

func TestAFreshRunStartsFromTheViewAndZoomsIntoIt(t *testing.T) {
	runtime, _, requests := sessionRuntime(t, map[string]any{"mode": "fresh"},
		faux.AssistantMessage("hello"),
		faux.AssistantMessage(faux.ToolCall("zoom", map[string]any{"id": 0, "n": 1}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("done"),
	)
	ctx := context.Background()
	if err := runtime.Prompt(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prompt(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 3 {
		t.Fatalf("%d requests", len(*requests))
	}
	second := userTexts((*requests)[1])
	if len(second) != 2 || !strings.Contains(second[0], "<chat>\n0+1|user: first\n1+1|talk: hello\n</chat>") || second[1] != "second" {
		t.Fatalf("second request user messages: %q", second)
	}
	third := (*requests)[2].Messages
	result, ok := third[len(third)-1].(*ai.ToolResultMessage)
	if !ok || ai.ContentText(result.Content) != "0+0|user: first" {
		t.Fatalf("zoom result: %#v", third[len(third)-1])
	}
}

func TestCompactionStoresTheViewOfWhatItCuts(t *testing.T) {
	runtime, manager, _ := sessionRuntime(t, nil, faux.AssistantMessage("one"), faux.AssistantMessage("two"))
	ctx := context.Background()
	for _, prompt := range []string{"alpha", "beta"} {
		if err := runtime.Prompt(ctx, prompt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.Compact(ctx, ""); err != nil {
		t.Fatal(err)
	}
	compaction := session.GetLatestCompactionEntry(manager.GetBranch())
	if compaction == nil || !strings.Contains(compaction.Summary, "<chat>\n0+1|user: alpha\n1+1|talk: one\n") {
		t.Fatalf("compaction %+v", compaction)
	}
}
