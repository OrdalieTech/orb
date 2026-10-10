package memtree

import (
	"context"
	"encoding/json"
	"fmt"
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
func (s *fakeSessions) save(customType string, data any) {
	raw, _ := json.Marshal(data)
	s.append(session.SessionEntry{Type: "custom", CustomType: customType, Data: raw})
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

// The view follows OptChat: each message appends its line, and once the view passes viewHigh one
// batch merges the most due pairs down to viewLow, so between batches it only grows at its end.
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
			if size > viewLow {
				t.Fatalf("message %d changed the view without a batch: %d bytes", index, size)
			}
			batches++
		}
		if size > viewHigh {
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
		if strings.Contains(chat, unbuilt) || len(chat) > compactHigh+5_000 {
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

func TestCompressShowsTheCutAndKeepsTheShortestTry(t *testing.T) {
	var seen []ai.MessageList
	replies := []int{700, 560, 530, 600, 540}
	ask := func(_ context.Context, messages ai.MessageList, _ int) (*ai.AssistantMessage, error) {
		seen = append(seen, messages)
		return &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: strings.Repeat("é", replies[len(seen)-1]/2)}}}, nil
	}
	line, err := compress(context.Background(), ask, ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("step")}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != tries || len(line) != 530 {
		t.Fatalf("%d tries, kept %d bytes", len(seen), len(line))
	}
	feedback := *seen[1][2].(*ai.UserMessage).Content.Text
	if !strings.HasPrefix(feedback, "Too long: your line is 700 bytes, over the 512-byte limit.") || !strings.HasSuffix(feedback, strings.Repeat("é", 256)+"| ← LIMIT") {
		t.Fatalf("feedback: %q", feedback)
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

func newRuntime(t *testing.T, options Options, stream engine.StreamFn, model *ai.Model, registry extensions.ModelRegistry) (*agent.SessionRuntime, *session.SessionManager) {
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
	runtimeConfig := agent.SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: configured, ExtensionRegistry: extensionRegistry, ExtensionMode: extensions.ModePrint}
	if registry != nil {
		runtimeConfig.ModelRegistry = registry
		runtimeConfig.GetAPIKey = func(ctx context.Context, provider ai.ProviderID) (*string, error) {
			resolved, err := registry.ResolveProviderAuth(ctx, string(provider), nil)
			if err != nil || resolved == nil {
				return nil, err
			}
			return resolved.Auth.APIKey, nil
		}
	}
	runtime, err := agent.NewSessionRuntime(runtimeConfig)
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
	runtime, manager := newRuntime(t, options, provider.StreamSimple, provider.GetModel(), nil)
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

// On Anthropic, a turn marks its view's last whole block for the cache, and the block the last
// turn marked, within four marks; a compaction sends the turns' system prompt and tools, then
// marks its own view the same way.
func TestAnthropicRequestsMarkTheViewAndCompactionsShareTheTurnsPrefix(t *testing.T) {
	var mu sync.Mutex
	var turns, compactions []map[string]any
	replies := []string{"ok", "ok", strings.Repeat("a long reply ", 60), "ok", "ok"}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		messages, _ := json.Marshal(body["messages"])
		mu.Lock()
		text := "a summary"
		if strings.Contains(string(messages), "Compaction:") {
			compactions = append(compactions, body)
		} else {
			text = replies[len(turns)]
			turns = append(turns, body)
		}
		mu.Unlock()
		quoted, _ := json.Marshal(text)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(writer, sse, quoted)
	}))
	defer server.Close()
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), fmt.Appendf(nil, `{"providers":{"lab":{"baseUrl":%q,"api":"anthropic-messages","apiKey":"k","models":[{"id":"m"}]}}}`, server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	model, ok := registry.Find("lab", "m")
	if !ok {
		t.Fatal("no lab/m model")
	}
	runtime, _ := newRuntime(t, Options{}, registry.StreamSimple, &model, registry)
	for _, prompt := range []string{"one", "two", "three", "four", "five"} {
		if err := runtime.Prompt(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	marks := func(body map[string]any) int {
		encoded, _ := json.Marshal(body)
		return strings.Count(string(encoded), `"cache_control"`)
	}
	viewMarked := func(body map[string]any, block int) bool {
		first := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
		return strings.HasPrefix(first[0].(map[string]any)["text"].(string), "<chat>\n") && first[block].(map[string]any)["cache_control"] != nil
	}
	mu.Lock()
	defer mu.Unlock()
	if len(turns) != 5 || len(compactions) != 1 {
		t.Fatalf("%d turns, %d compactions", len(turns), len(compactions))
	}
	for _, turn := range turns[2:] {
		if !viewMarked(turn, 0) || marks(turn) > 4 {
			t.Fatalf("turn view marked: %v, %d marks", viewMarked(turn, 0), marks(turn))
		}
	}
	// The fifth turn's view has a second whole block; the fourth marked the first.
	if fifth := turns[4]; !viewMarked(fifth, 1) || marks(fifth) != 4 || strings.Contains(fmt.Sprint(fifth["tools"]), "cache_control") {
		t.Fatalf("fifth turn: second block marked %v, %d marks, tools %v", viewMarked(fifth, 1), marks(fifth), fifth["tools"])
	}
	compaction, turn := compactions[0], turns[3]
	if !reflect.DeepEqual(compaction["system"], turn["system"]) || !reflect.DeepEqual(compaction["tools"], turn["tools"]) || turn["tools"] == nil {
		t.Fatal("the compaction does not send the turns' system prompt and tools")
	}
	if !viewMarked(compaction, 0) || marks(compaction) > 4 {
		t.Fatalf("compaction view marked: %v, %d marks", viewMarked(compaction, 0), marks(compaction))
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
	sessions := &fakeSessions{}
	for index := range 4 {
		sessions.add("user", fmt.Sprintf("message %d %s", index, strings.Repeat("x", 2000)))
	}
	tr := newTree(sessions, sessions.save)
	defer tr.stop()
	model := &compactor{size: 300}
	var poisoned atomic.Int32
	tr.ask = func(ctx context.Context, messages ai.MessageList, mark int) (*ai.AssistantMessage, error) {
		if blocks := messages[0].(*ai.UserMessage).Content.Blocks; strings.Contains(blocks[len(blocks)-1].(*ai.TextContent).Text, "compress message 1 into") {
			poisoned.Add(1)
			return &ai.AssistantMessage{}, nil
		}
		return model.ask(ctx, messages, mark)
	}
	tr.retry = time.Millisecond
	tr.pump()
	if !settled(tr, 4) {
		t.Fatal("messages after the failing one were never summarized")
	}
	idle(t, tr, false)
	time.Sleep(20 * time.Millisecond)
	if got := ai.ContentText(tr.zoom(1, 1)); !strings.HasPrefix(got, "1+0|user: message 1 x") || poisoned.Load() != giveUp {
		t.Fatalf("after %d calls, message 1 reads %.40q", poisoned.Load(), got)
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
	if !strings.Contains(third, "0+1|user: alpha\n1+1|orb: one\n</chat>|gamma") || strings.Contains(third, "beta") {
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
