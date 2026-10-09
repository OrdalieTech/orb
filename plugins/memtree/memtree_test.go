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
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
)

// fakeSessions is a linear branch of message entries.
type fakeSessions struct {
	extensions.ReadonlySessionManager
	mu      sync.Mutex
	entries []session.SessionEntry
}

func (*fakeSessions) GetSessionID() string { return "s1" }
func (*fakeSessions) IsPersisted() bool    { return true }
func (s *fakeSessions) GetLeafID() *string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		return nil
	}
	return &s.entries[len(s.entries)-1].ID
}

func (s *fakeSessions) GetEntry(id string) *session.SessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var index int
	if _, err := fmt.Sscanf(id, "e%d", &index); err != nil || index >= len(s.entries) {
		return nil
	}
	entry := s.entries[index]
	return &entry
}

func (s *fakeSessions) GetEntries() []session.SessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.SessionEntry(nil), s.entries...)
}

// save appends a memtree entry, as the extension API does.
func (s *fakeSessions) save(key nodeKey, text string) {
	data, _ := json.Marshal(savedNode{key, text})
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := &s.entries[len(s.entries)-1].ID
	s.entries = append(s.entries, session.SessionEntry{Type: "custom", ID: fmt.Sprintf("e%d", len(s.entries)), ParentID: parent, CustomType: nodeType, Data: data})
}

func (s *fakeSessions) add(role string, content any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("e%d", len(s.entries))
	var parent *string
	if len(s.entries) > 0 {
		parent = &s.entries[len(s.entries)-1].ID
	}
	raw, _ := json.Marshal(map[string]any{"role": role, "content": content})
	s.entries = append(s.entries, session.SessionEntry{Type: "message", ID: id, ParentID: parent, Timestamp: "2026-10-04T10:00:00.000Z", Message: raw})
	return id
}

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

func settled(tr *tree, n int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return tr.settle(ctx, n)
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

func TestMessagesAreCompressedInOrderAndTheViewFitsItsBudget(t *testing.T) {
	sessions := longSession(40)
	tr := newTree(sessions, sessions.save)
	defer tr.stop()
	model := &compactor{}
	tr.ask, tr.budget = model.ask, 4000
	tr.pump()
	if !settled(tr, 40) {
		t.Fatal("messages were not all summarized")
	}
	idle(t, tr)

	for index, number := range model.compressed {
		if number != index {
			t.Fatalf("compressed out of order: %v", model.compressed)
		}
	}
	if !strings.Contains(model.requests[1], "summary of 0 ") || strings.Contains(model.requests[1], "summary of 1 ") {
		t.Fatalf("the context of message 1 is the view before it: %q", model.requests[1])
	}
	if got := tr.zoom(0, 32); !strings.HasPrefix(got, "0+16|merged") || !strings.Contains(got, "\n16+16|merged") {
		t.Fatalf("zoom(0, 32) = %q", got)
	}
	if got := tr.zoom(5, 1); got != "5+0|user: message 5 "+strings.Repeat("x", 2000) {
		t.Fatalf("zoom(5, 1) = %.40q", got)
	}
	if got := tr.zoom(3, 2); got != "No line 3+2." {
		t.Fatalf("zoom(3, 2) = %q", got)
	}
	for _, chat := range model.requests {
		if strings.Contains(chat, "|") || strings.Contains(chat, "+1") || strings.Contains(chat, unbuilt) {
			t.Fatalf("compactor saw ids or placeholders: %q", chat)
		}
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
	tr := newTree(sessions, sessions.save)
	defer tr.stop()
	model := &compactor{}
	tr.ask, tr.budget = model.ask, 3000
	var previous []part
	for index := 1; index < 64; index++ {
		sessions.add("user", fmt.Sprintf("message %d %s", index, strings.Repeat("x", 600)))
		tr.pump()
		if !settled(tr, index+1) {
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

func TestNodesSurviveARestartInTheSession(t *testing.T) {
	sessions := longSession(6)
	first := newTree(sessions, sessions.save)
	model := &compactor{}
	first.ask = model.ask
	first.pump()
	settled(first, 6)
	idle(t, first)
	first.stop()

	second := newTree(sessions, sessions.save)
	defer second.stop()
	second.pump() // no compactor: every node comes from the session's memtree entries
	if !settled(second, 6) || second.render(6) != first.render(6) {
		t.Fatalf("reloaded view %q, want %q", second.render(6), first.render(6))
	}
}

func sessionRuntime(t *testing.T, options Options, replies ...faux.ResponseStep) (*agent.SessionRuntime, *session.SessionManager, *[]ai.Context) {
	t.Helper()
	cwd, agentDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"compaction":{"reserveTokens":120000,"keepRecentTokens":1}}`), 0o600); err != nil {
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
	if err := registry.Register("<inline:memtree>", Extension(options)); err != nil {
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
	runtime, _, requests := sessionRuntime(t, Options{},
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
	if encoded, _ := json.Marshal((*requests)[1]); !strings.Contains(string(encoded), "You keep no memory between turns") {
		t.Fatalf("MASTER is not in the second request: %s", encoded)
	}
	third := (*requests)[2].Messages
	result, ok := third[len(third)-1].(*ai.ToolResultMessage)
	if !ok || ai.ContentText(result.Content) != "0+0|user: first" {
		t.Fatalf("zoom result: %#v", third[len(third)-1])
	}
}

func TestCompactionStoresTheViewOfWhatItCuts(t *testing.T) {
	runtime, manager, _ := sessionRuntime(t, Options{Mode: "compaction"}, faux.AssistantMessage("one"), faux.AssistantMessage("two"))
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

// Without a compactor (none configured, no credentials), a run starts at once from the messages
// cut to size instead of waiting for summaries that cannot come.
func TestAFreshRunGoesOnWithoutACompactor(t *testing.T) {
	runtime, _, requests := sessionRuntime(t, Options{}, faux.AssistantMessage("ok"), faux.AssistantMessage("later"))
	first := strings.Repeat("a long first message ", 40)
	if err := runtime.Prompt(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Prompt(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if view := userTexts((*requests)[len(*requests)-1])[0]; len(*requests) != 2 || !strings.HasPrefix(view, "<chat>\n0+1|user: a long first message a long") || strings.Contains(view, unbuilt) {
		t.Fatalf("the second run did not start from the cut message: %q", userTexts((*requests)[len(*requests)-1]))
	}
}

// A message the compactor keeps failing on (a refusal, an empty reply) gets giveUp calls, then
// keeps its text cut to size, saved with the session: later messages summarize past it, and
// nothing calls again, in this process or the next.
func TestAMessageTheCompactorCannotSummarizeIsCutToSize(t *testing.T) {
	sessions := longSession(4)
	tr := newTree(sessions, sessions.save)
	defer tr.stop()
	model := &compactor{}
	var poisoned atomic.Int32
	tr.ask = func(ctx context.Context, messages ai.MessageList) (*ai.AssistantMessage, error) {
		if step := messages[0].(*ai.UserMessage).Content.Blocks[1].(*ai.TextContent).Text; strings.HasPrefix(step, "Compress") && strings.Contains(step, "message 1 x") {
			poisoned.Add(1)
			return &ai.AssistantMessage{}, nil
		}
		return model.ask(ctx, messages)
	}
	tr.retry, tr.budget = time.Millisecond, 4000
	tr.pump()
	if !settled(tr, 4) {
		t.Fatal("messages after the failing one were never summarized")
	}
	idle(t, tr)
	time.Sleep(20 * time.Millisecond)
	if got := tr.zoom(1, 1); !strings.HasPrefix(got, "1+0|user: message 1 x") || poisoned.Load() != giveUp {
		t.Fatalf("after %d calls, message 1 reads %.40q", poisoned.Load(), got)
	}
	if model.compressed[len(model.compressed)-1] != 3 {
		t.Fatalf("compressed %v", model.compressed)
	}
	again := newTree(sessions, sessions.save)
	again.mu.Lock()
	again.syncLocked()
	saved := again.builtLocked(0, 1)
	again.mu.Unlock()
	if !saved {
		t.Fatal("the next start would ask about message 1 again")
	}
}

func TestAFreshRunAfterATreeMoveSeesOnlyItsBranch(t *testing.T) {
	runtime, manager, requests := sessionRuntime(t, Options{}, faux.AssistantMessage("one"), faux.AssistantMessage("two"), faux.AssistantMessage("three"))
	ctx := context.Background()
	if err := runtime.Prompt(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	answered := *manager.GetLeafID()
	if err := runtime.Prompt(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.NavigateTree(ctx, answered, agent.NavigateTreeOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prompt(ctx, "gamma"); err != nil {
		t.Fatal(err)
	}
	third := strings.Join(userTexts((*requests)[2]), "|")
	if !strings.Contains(third, "0+1|user: alpha\n1+1|talk: one\n</chat>|gamma") || strings.Contains(third, "beta") {
		t.Fatalf("third request: %q", third)
	}
}

func TestFreshModeNeverCompactsBeforeARun(t *testing.T) {
	compactsFirst := func(options Options) bool {
		var manager *session.SessionManager
		compacted := false
		runtime, manager, _ := sessionRuntime(t, options, faux.Factory(func(context.Context, ai.Context, *ai.StreamOptions, faux.State, *ai.Model) (*ai.AssistantMessage, error) {
			compacted = session.GetLatestCompactionEntry(manager.GetBranch()) != nil
			return faux.AssistantMessage("ok"), nil
		}))
		// A long history whose last request was over the 8k threshold, as before memtree was on.
		for index := range 120 {
			if _, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText(fmt.Sprintf("note %d %s", index, strings.Repeat("words ", 70)))}); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.AppendMessage(&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "noted"}}, StopReason: ai.StopReasonStop, Usage: ai.Usage{TotalTokens: 13000}}); err != nil {
				t.Fatal(err)
			}
		}
		runtime.RefreshContext()
		if err := runtime.Prompt(context.Background(), "next"); err != nil {
			t.Fatal(err)
		}
		return compacted
	}
	if compaction, fresh := compactsFirst(Options{Mode: "compaction"}), compactsFirst(Options{}); !compaction || fresh {
		t.Fatalf("history alone compacts before the run in compaction mode: %v, in fresh mode: %v", compaction, fresh)
	}
}
