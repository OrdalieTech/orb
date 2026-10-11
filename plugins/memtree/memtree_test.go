package memtree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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

// fakeSessions is a linear branch of entries.
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

func (s *fakeSessions) append(entry session.SessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.ID = fmt.Sprintf("e%d", len(s.entries))
	if len(s.entries) > 0 {
		entry.ParentID = &s.entries[len(s.entries)-1].ID
	}
	s.entries = append(s.entries, entry)
}

// save appends a memtree entry, as the extension API does.
func (s *fakeSessions) save(customType string, data any) error {
	raw, _ := json.Marshal(data)
	s.append(session.SessionEntry{Type: "custom", CustomType: customType, Data: raw})
	return nil
}

func (s *fakeSessions) add(role string, content any) {
	raw, _ := json.Marshal(map[string]any{"role": role, "content": content})
	s.append(session.SessionEntry{Type: "message", Timestamp: "2026-10-04T10:00:00.000Z", Message: raw})
}

// compactor stands in for the model: a line of size bytes per step, recording the messages it
// compressed and the view each call read. While hold is open, the merge of 0+1 and 1+1 waits.
type compactor struct {
	mu         sync.Mutex
	size       int
	hold       chan struct{}
	compressed []int
	chats      []string
}

var compressed = regexp.MustCompile(`compress message (\d+) into`)

func (c *compactor) ask(_ context.Context, messages ai.MessageList, _ int) (*ai.AssistantMessage, error) {
	blocks := messages[0].(*ai.UserMessage).Content.Blocks
	task := blocks[len(blocks)-1].(*ai.TextContent).Text
	if c.hold != nil && strings.Contains(task, "merge lines 0+1 and 1+1,") {
		<-c.hold
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chats = append(c.chats, ai.ContentText(blocks[:len(blocks)-1], ""))
	line := "merged"
	if match := compressed.FindStringSubmatch(task); match != nil {
		var number int
		_, _ = fmt.Sscan(match[1], &number)
		c.compressed = append(c.compressed, number)
		line = "summary of " + match[1]
	}
	return &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: line + " " + strings.Repeat("s", c.size-len(line)-1)}}}, nil
}

func settled(tr *tree, n int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return tr.settle(ctx, n)
}

// idle waits until no compactor call but a held merge of 0+1 and 1+1 runs, and a pump starts
// none: the last call may start merges of its own.
func idle(t *testing.T, tr *tree, held bool) {
	t.Helper()
	running := func() int {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		busy := len(tr.busy)
		if held && len(tr.log) > 1 && tr.busy[tr.keyLocked(1, 0)] {
			busy--
		}
		return busy
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if running() == 0 {
			if tr.pump(); running() == 0 {
				return
			}
		}
	}
	t.Fatal("compactor never went idle")
}

// The view follows OptChat: each message appends its line, and once the view passes its budget one
// batch merges the most due pairs down to half of it, so between batches it only grows at its end.
// Compactions read a view merged further. A restart keeps the live view, not one rebuilt from
// the log, even where a live merge had to wait for its summary.
func TestTheViewGrowsAtItsEndBetweenBatchesAndSurvivesARestart(t *testing.T) {
	sessions := &fakeSessions{}
	tr := newTree(sessions, sessions.save)
	defer tr.stop()
	model := &compactor{size: nodeBytes, hold: make(chan struct{})}
	tr.ask = model.ask
	var previous []part
	batches := 0
	for index := range 400 {
		sessions.add("user", fmt.Sprintf("message %d %s", index, strings.Repeat("x", 600)))
		tr.pump()
		if !settled(tr, index+1) {
			t.Fatalf("message %d was not summarized", index)
		}
		idle(t, tr, true)
		tr.mu.Lock()
		view, size := slices.Clone(tr.view), tr.size
		tr.mu.Unlock()
		if len(view) != len(previous)+1 || !slices.Equal(view[:len(previous)], previous) {
			if size > viewBytes/2 {
				t.Fatalf("message %d changed the view without a batch: %d bytes", index, size)
			}
			batches++
		}
		if size > viewBytes {
			t.Fatalf("message %d left the view at %d bytes", index, size)
		}
		previous = view
	}
	close(model.hold)
	idle(t, tr, false)
	if batches != 2 || previous[0] != (part{0, 0}) {
		t.Fatalf("%d batches, view starts %v", batches, previous[:2])
	}
	for _, chat := range model.chats {
		if strings.Contains(chat, unbuilt) || len(chat) > viewBytes/4+5_000 {
			t.Fatalf("a compaction read %d bytes or a placeholder", len(chat))
		}
	}
	if got := ai.ContentText(tr.zoom(5, 1)); got != "5+0|user: message 5 "+strings.Repeat("x", 600) {
		t.Fatalf("zoom(5, 1) = %.40q", got)
	}

	again := newTree(sessions, sessions.save)
	defer again.stop()
	again.pump()
	_, want := tr.current()
	if _, got := again.current(); !slices.Equal(got, want) {
		t.Fatalf("the view changed across a restart:\n%v\nwant\n%v", got, want)
	}
}

// Compactor errors with calls in flight never leave a turn a view over its budget, nor waiting
// for good: every merge fails slowly, and a message the compactor always fails on (a refusal, an
// empty reply) gets giveUp calls, then keeps its text cut to size, saved so that nothing asks about
// it again at the next start.
func TestFailingCompactionsStillLeaveAViewWithinBudget(t *testing.T) {
	sessions := &fakeSessions{}
	for index := range 120 {
		sessions.add("user", fmt.Sprintf("message %d %s", index, strings.Repeat("x", 900)))
	}
	tr := newTree(sessions, sessions.save)
	defer tr.stop()
	model := &compactor{size: 500}
	var refusals atomic.Int32
	tr.ask = func(ctx context.Context, messages ai.MessageList, mark int) (*ai.AssistantMessage, error) {
		blocks := messages[0].(*ai.UserMessage).Content.Blocks
		task := blocks[len(blocks)-1].(*ai.TextContent).Text
		if strings.Contains(task, "merge lines") {
			time.Sleep(20 * time.Millisecond)
			return nil, errors.New("overloaded")
		}
		if match := compressed.FindStringSubmatch(task); match != nil && strings.HasSuffix(match[1], "0") {
			refusals.Add(1)
			return &ai.AssistantMessage{}, nil
		}
		return model.ask(ctx, messages, mark)
	}
	tr.retry, tr.high = time.Millisecond, 8_000
	tr.pump()
	if !settled(tr, 120) {
		t.Fatal("the turn never got its view")
	}
	n, view := tr.current()
	if size := rendered(tr.render(n, view)); size > 8_000 {
		t.Fatalf("the view is %d bytes over a budget of 8,000", size)
	}
	idle(t, tr, false)
	again := newTree(sessions, sessions.save)
	again.mu.Lock()
	again.syncLocked()
	saved := again.builtLocked(0, 10)
	again.mu.Unlock()
	if got := refusals.Load(); got != 12*giveUp || !saved {
		t.Fatalf("%d calls for 12 refused messages; message 10 saved cut: %v", got, saved)
	}
}

// A process whose session refuses memtree's entries, as Orb's store does once another process wrote
// to it, stops calling the compactor: its turns go on with lines cut to size, and nothing it built
// lands in the session.
func TestARefusedSessionStopsTheCompactor(t *testing.T) {
	sessions := &fakeSessions{}
	for index := range 60 {
		sessions.add("user", fmt.Sprintf("message %d %s", index, strings.Repeat("x", 900)))
	}
	tr := newTree(sessions, func(string, any) error { return fmt.Errorf("%w: s1", session.ErrNotOwner) })
	defer tr.stop()
	model := &compactor{size: 300}
	var calls atomic.Int32
	tr.ask = func(ctx context.Context, messages ai.MessageList, mark int) (*ai.AssistantMessage, error) {
		calls.Add(1)
		return model.ask(ctx, messages, mark)
	}
	tr.pump()
	if !settled(tr, 60) {
		t.Fatal("the turn never got its view")
	}
	idle(t, tr, false)
	if n := calls.Load(); n > 2*jobs {
		t.Fatalf("%d compactor calls after the session refused the first entry", n)
	}
	if got := ai.ContentText(tr.zoom(59, 1)); !strings.HasPrefix(got, "59+0|user: message 59") || memtreeEntries(sessions.GetEntries()) != 0 {
		t.Fatalf("message 59 reads %.40q", got)
	}
	n, view := tr.current()
	if lines := tr.render(n, view); !strings.Contains(strings.Join(lines, "\n"), "|user: message 59 xxx") {
		t.Fatal("the view does not hold the cut line")
	}

	// Any other error, such as a busy database, loses that one entry and summaries go on.
	busy := newTree(sessions, func(string, any) error { return errors.New("database is locked") })
	defer busy.stop()
	busy.ask = model.ask
	busy.pump()
	if !settled(busy, 60) {
		t.Fatal("the turn never got its view")
	}
	if lines := strings.Join(busy.render(busy.current()), "\n"); !strings.Contains(lines, "|summary of 59 ") {
		t.Fatalf("a busy database stopped the summaries: %.200q", lines[max(0, len(lines)-200):])
	}
}

// Long text is logged as several messages, a tool's output keeps its head and tail, and zoom
// gives a message whole with its images.
func TestLongTextIsPagedAndZoomGivesImages(t *testing.T) {
	sessions := &fakeSessions{}
	sessions.add("user", []map[string]any{{"type": "text", "text": "see"}, {"type": "image", "data": "aGk=", "mimeType": "image/png"}})
	sessions.add("user", strings.Repeat("u", 2*pageChars+1))
	sessions.add("toolResult", strings.Repeat("e", 3*pageChars))
	tr := newTree(sessions, sessions.save)
	defer tr.stop()
	if n, _ := tr.current(); n != 5 {
		t.Fatalf("%d messages", n)
	}
	if got := tr.zoom(0, 1); len(got) != 2 || got[1].(*ai.ImageContent).Data != "aGk=" {
		t.Fatalf("zoom(0, 1) = %v", got)
	}
	if got := ai.ContentText(tr.zoom(3, 1)); got != "3+0|user: u" {
		t.Fatalf("zoom(3, 1) = %q", got)
	}
	if got := ai.ContentText(tr.zoom(4, 1)); len(got) != len("4+0|echo: \n...\n")+pageChars {
		t.Fatalf("zoom(4, 1) is %d bytes", len(got))
	}
}

func newRuntime(t *testing.T, options Options, stream engine.StreamFn, model *ai.Model) (*agent.SessionRuntime, *session.SessionManager) {
	t.Helper()
	cwd, agentDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"compaction":{"reserveTokens":120000,"keepRecentTokens":1},"retry":{"baseDelayMs":1}}`), 0o600); err != nil {
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
	extensionRegistry := extensions.NewRegistry(cwd)
	if err := extensionRegistry.Register("<inline:memtree>", Extension(options)); err != nil {
		t.Fatal(err)
	}
	created := engine.NewAgent(stream, engine.WithInitialState(engine.AgentState{SystemPrompt: "test", Model: model}), engine.WithConvertToLLM(agent.ConvertToLLM))
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: configured, ExtensionRegistry: extensionRegistry, ExtensionMode: extensions.ModePrint})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	return runtime, manager
}

func sessionRuntime(t *testing.T, options Options, replies ...faux.ResponseStep) (*agent.SessionRuntime, *session.SessionManager, *[]ai.Context) {
	t.Helper()
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
	runtime, manager := newRuntime(t, options, provider.StreamSimple, provider.GetModel())
	return runtime, manager, &requests
}

func userTexts(request ai.Context) []string {
	var texts []string
	for _, message := range request.Messages {
		if user, ok := message.(*ai.UserMessage); ok {
			if user.Content.Text != nil {
				texts = append(texts, *user.Content.Text)
			} else {
				texts = append(texts, ai.ContentText(user.Content.Blocks, ""))
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
	if len(second) != 2 || second[0] != "<chat>\n0+1|user: first\n1+1|orb: hello\n</chat>" || second[1] != "second" {
		t.Fatalf("second request user messages: %q", second)
	}
	if system := (*requests)[1].SystemPrompt; system == nil || !strings.HasPrefix(*system, prompt+"\n\n") {
		t.Fatalf("OptChat's prompt does not lead the system prompt: %v", system)
	}
	third := (*requests)[2].Messages
	result, ok := third[len(third)-1].(*ai.ToolResultMessage)
	if !ok || ai.ContentText(result.Content) != "0+0|user: first" {
		t.Fatalf("zoom result: %#v", third[len(third)-1])
	}
}

const sse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const toolUse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"date\",\"input\":{}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"id\\\":0}\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// On Anthropic, a turn marks its view's last whole block for the cache, and the block the last
// turn marked, within four marks. A compaction sends the turns' system prompt and tools, never
// answers with a tool call, marks its own view the same way, and is shown where 512 bytes cut a
// line too long.
func TestAnthropicRequestsMarkTheViewAndCompactionsShareTheTurnsPrefix(t *testing.T) {
	replies := []string{"ok", "ok", strings.Repeat("a long reply ", 60), "ok", "ok"}
	var turn, compaction atomic.Int32
	runtime, requests := host(t, func(_ context.Context, compacting bool) string {
		if !compacting {
			return replies[turn.Add(1)-1]
		}
		if compaction.Add(1) == 1 {
			return strings.Repeat("too long ", 70)
		}
		return "a summary"
	})
	for _, prompt := range []string{"one", "two", "three", "four", "five"} {
		if err := runtime.Session().Prompt(t.Context(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	marks := func(body map[string]any) int { return strings.Count(encoded(body), `"cache_control"`) }
	viewMarked := func(body map[string]any, block int) bool {
		first := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
		return strings.HasPrefix(first[0].(map[string]any)["text"].(string), "<chat>\n") && first[block].(map[string]any)["cache_control"] != nil
	}
	turns, compactions := requests(false), requests(true)
	if len(turns) != 5 || len(compactions) != 2 {
		t.Fatalf("%d turns, %d compactions", len(turns), len(compactions))
	}
	for _, turn := range turns[2:] {
		if !viewMarked(turn, 0) || marks(turn) > 4 {
			t.Fatalf("turn view marked: %v, %d marks", viewMarked(turn, 0), marks(turn))
		}
	}
	// The fifth turn's view has a second whole block; the fourth marked the first.
	if fifth := turns[4]; !viewMarked(fifth, 1) || marks(fifth) != 4 || strings.Contains(encoded(fifth["tools"]), "cache_control") {
		t.Fatalf("fifth turn: second block marked %v, %d marks", viewMarked(fifth, 1), marks(fifth))
	}
	first, retry := compactions[0], encoded(compactions[1])
	if !reflect.DeepEqual(first["system"], turns[3]["system"]) || !reflect.DeepEqual(first["tools"], turns[3]["tools"]) || turns[3]["tools"] == nil {
		t.Fatal("the compaction does not send the turns' system prompt and tools")
	}
	if !viewMarked(first, 0) || marks(first) > 4 {
		t.Fatalf("compaction view marked: %v, %d marks", viewMarked(first, 0), marks(first))
	}
	if !strings.Contains(retry, "Too long: your line is 629 bytes, over the 512-byte limit.") || !strings.Contains(retry, "| ← LIMIT") {
		t.Fatalf("the retry does not show the cut: %.300s", retry[len(retry)-300:])
	}
	if !strings.Contains(encoded(turns[3]), "5+1|a summary") {
		t.Fatal("the long reply's line is not the compactor's second answer")
	}
}

// A run Orb starts without a prompt, such as a retry after a provider error, continues the last
// prompt's turn from its view, not from the raw history.
func TestARetryContinuesTheTurnFromItsView(t *testing.T) {
	failed := "stream error: stream ID 745; INTERNAL_ERROR; received from peer"
	runtime, _, requests := sessionRuntime(t, Options{},
		faux.AssistantMessage("hello"),
		faux.AssistantMessage("", faux.AssistantMessageOptions{StopReason: ai.StopReasonError, ErrorMessage: &failed}),
		faux.AssistantMessage("done"),
	)
	ctx := context.Background()
	for _, prompt := range []string{"first", "second"} {
		if err := runtime.Prompt(ctx, prompt); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.WaitForIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 3 {
		t.Fatalf("%d requests", len(*requests))
	}
	if retry := userTexts((*requests)[2]); len(retry) != 2 || retry[0] != "<chat>\n0+1|user: first\n1+1|orb: hello\n</chat>" || retry[1] != "second" {
		t.Fatalf("retry request user messages: %q", retry)
	}
}

// On a model with a small context window, the view a run sends stays within half of it in bytes;
// without a compactor (none configured, no credentials) it holds the messages cut to size.
func TestTheViewFitsASmallModelsWindow(t *testing.T) {
	provider := faux.New()
	var requests []ai.Context
	provider.SetResponses([]faux.ResponseStep{faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		requests = append(requests, request)
		return faux.AssistantMessage("ok"), nil
	})})
	model := *provider.GetModel()
	model.ContextWindow = 32_000
	runtime, manager := newRuntime(t, Options{}, provider.StreamSimple, &model)
	for index := range 60 {
		if _, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText(fmt.Sprintf("note %d %s", index, strings.Repeat("words ", 100)))}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.RefreshContext()
	if err := runtime.Prompt(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if view := userTexts(requests[0])[0]; len(view) > 16_000 || strings.Contains(view, unbuilt) || !strings.Contains(view, "|user: note 59 words words") {
		t.Fatalf("the view is %d bytes for a 32,000-token window: %.200q", len(view), view[max(0, len(view)-200):])
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
	if compaction == nil || !strings.Contains(compaction.Summary, "<chat>\n0+1|user: alpha\n1+1|orb: one\n") {
		t.Fatalf("compaction %+v", compaction)
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

// host runs memtree in an agent host whose model, for turns and compactions alike, is a fake
// Anthropic server. answer gives a request's reply and may hold it until ctx ends; a compaction
// offered tools but not told to leave them gets a tool call, as a model may answer. Every request
// is recorded, turns and compactions apart.
func host(t *testing.T, answer func(ctx context.Context, compaction bool) string) (*agent.AgentSessionRuntime, func(compaction bool) []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	requests := map[bool][]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		compaction := strings.Contains(encoded(body["messages"]), "Compaction:")
		mu.Lock()
		requests[compaction] = append(requests[compaction], body)
		mu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		if choice, _ := body["tool_choice"].(map[string]any); compaction && body["tools"] != nil && choice["type"] != "none" {
			_, _ = io.WriteString(writer, toolUse)
			return
		}
		text := answer(request.Context(), compaction)
		if request.Context().Err() != nil {
			return
		}
		quoted, _ := json.Marshal(text)
		_, _ = fmt.Fprintf(writer, sse, quoted)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), fmt.Appendf(nil, `{"providers":{"lab":{"baseUrl":%q,"api":"anthropic-messages","apiKey":"k","models":[{"id":"m"}]}}}`, server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	model, _ := registry.Find("lab", "m")
	extensionRegistry := extensions.NewRegistry(dir)
	if err := extensionRegistry.Register("<inline:memtree>", Extension(Options{})); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewAgentSessionRuntime(t.Context(), agent.AgentSessionOptions{CWD: dir, AgentDir: dir, Model: &model, ModelRegistry: registry, ExtensionRegistry: extensionRegistry, Resources: &agent.Resources{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Dispose(context.Background()) })
	return runtime, func(compaction bool) []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(requests[compaction])
	}
}

func lastTurn(requests func(bool) []map[string]any) string {
	turns := requests(false)
	return encoded(turns[len(turns)-1])
}

// encoded is value as JSON, without escaping the view's angle brackets.
func encoded(value any) string {
	var text strings.Builder
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return text.String()
}

// holder answers turns at once and holds compactions until release closes or their request ends,
// counting those the server got and those that ended first.
type holder struct {
	release            chan struct{}
	started, cancelled atomic.Int32
}

func hold() *holder { return &holder{release: make(chan struct{})} }

func (h *holder) answer(ctx context.Context, compaction bool) string {
	if compaction {
		h.started.Add(1)
		select {
		case <-h.release:
		case <-ctx.Done():
			h.cancelled.Add(1)
		}
		return "a summary"
	}
	return "ok"
}

// until waits until done holds, or fails the test.
func until(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

func memtreeEntries(entries []session.SessionEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Type == "custom" && strings.HasPrefix(entry.CustomType, "memtree") {
			count++
		}
	}
	return count
}

var long = strings.Repeat("a long message ", 50)

// Escape while a turn waits for summaries ends the turn without sending anything; the next turn
// gets its view once the summary comes.
func TestEscapeDuringTheWaitSendsNothing(t *testing.T) {
	held := hold()
	runtime, requests := host(t, held.answer)
	session := runtime.Session()
	if err := session.Prompt(t.Context(), long); err != nil {
		t.Fatal(err)
	}
	until(t, "the compaction never reached the server", func() bool { return held.started.Load() == 1 })
	done := make(chan error, 1)
	go func() { done <- session.Prompt(t.Context(), "second") }()
	until(t, "the second prompt was never logged", func() bool {
		return strings.Contains(encoded(session.Manager().GetEntries()), `"text":"second"`)
	})
	session.Abort()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Escape did not end the wait")
	}
	if got := len(requests(false)); got != 1 {
		t.Fatalf("%d turn requests after Escape, want 1", got)
	}
	close(held.release)
	if err := session.Prompt(t.Context(), "third"); err != nil {
		t.Fatal(err)
	}
	if last := lastTurn(requests); !strings.Contains(last, "0+1|a summary") {
		t.Fatalf("the next turn has no view: %.300s", last)
	}
}

// A switch to another session while a compaction runs cancels it, and nothing of the old tree
// lands in either session.
func TestASessionSwitchCancelsCompactions(t *testing.T) {
	held := hold()
	defer close(held.release)
	runtime, _ := host(t, held.answer)
	old := runtime.Session().Manager()
	if err := runtime.Session().Prompt(t.Context(), long); err != nil {
		t.Fatal(err)
	}
	until(t, "the compaction never reached the server", func() bool { return held.started.Load() == 1 })
	if _, err := runtime.NewSession(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	until(t, "the old session's compaction still runs", func() bool { return held.cancelled.Load() == 1 })
	if memtreeEntries(old.GetEntries()) != 0 || memtreeEntries(runtime.Session().Manager().GetEntries()) != 0 {
		t.Fatal("a compaction of the old session saved an entry")
	}
	if err := runtime.Session().Prompt(t.Context(), "fresh"); err != nil {
		t.Fatal(err)
	}
}

// A fork while a compaction runs starts from the summaries saved before its point.
func TestAForkKeepsTheSummariesBeforeIt(t *testing.T) {
	held := hold()
	runtime, requests := host(t, held.answer)
	session := runtime.Session()
	if err := session.Prompt(t.Context(), long); err != nil {
		t.Fatal(err)
	}
	close(held.release)
	if err := session.Prompt(t.Context(), "second"); err != nil {
		t.Fatal(err)
	}
	var second string
	for _, entry := range session.Manager().GetEntries() {
		if entry.Type == "message" && strings.Contains(string(entry.Message), `"text":"second"`) {
			second = entry.ID
		}
	}
	if err := session.Prompt(t.Context(), long+"zebra"); err != nil {
		t.Fatal(err)
	}
	until(t, "the second compaction never reached the server", func() bool { return held.started.Load() == 2 })
	if _, err := runtime.Fork(t.Context(), second, nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Session().Prompt(t.Context(), "forked"); err != nil {
		t.Fatal(err)
	}
	last := lastTurn(requests)
	if !strings.Contains(last, "0+1|a summary") || strings.Contains(last, "zebra") {
		t.Fatalf("the fork's view: %.400s", last)
	}
}

// A /tree move while a compaction of the left branch runs: the summary lands, but the next turn's
// view covers only the branch it is on.
func TestATreeMoveDuringACompaction(t *testing.T) {
	held := hold()
	runtime, requests := host(t, held.answer)
	session := runtime.Session()
	if err := session.Prompt(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	answered := *session.Manager().GetLeafID()
	if err := session.Prompt(t.Context(), long); err != nil {
		t.Fatal(err)
	}
	until(t, "the compaction never reached the server", func() bool { return held.started.Load() == 1 })
	if _, err := session.NavigateTree(t.Context(), answered, agent.NavigateTreeOptions{}); err != nil {
		t.Fatal(err)
	}
	close(held.release)
	if err := session.Prompt(t.Context(), "gamma"); err != nil {
		t.Fatal(err)
	}
	last := lastTurn(requests)
	if !strings.Contains(last, "0+1|user: alpha") || strings.Contains(last, "a long message") || strings.Contains(last, "2+1|") {
		t.Fatalf("the view after the move: %.400s", last)
	}
}

// A run that overflows the window in fresh mode is compacted onto the view and goes on.
func TestAnOverflowInAFreshRunCompactsOntoTheView(t *testing.T) {
	overflow := "prompt is too long: 300000 tokens > 200000 maximum"
	runtime, _, requests := sessionRuntime(t, Options{},
		faux.AssistantMessage("one"),
		faux.AssistantMessage("", faux.AssistantMessageOptions{StopReason: ai.StopReasonError, ErrorMessage: &overflow}),
		faux.AssistantMessage("two"),
	)
	for _, prompt := range []string{"first", "second"} {
		if err := runtime.Prompt(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 3 {
		t.Fatalf("%d requests", len(*requests))
	}
	if retry := strings.Join(userTexts((*requests)[2]), "|"); !strings.Contains(retry, "<chat>\n0+1|user: first\n1+1|orb: one") || !strings.Contains(retry, "second") {
		t.Fatalf("the request after the overflow: %q", retry)
	}
}
