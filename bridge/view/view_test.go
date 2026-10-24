package view

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	attach "github.com/OrdalieTech/orb/agent/bridge"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/internal/document"
)

// client is an app reduced to its model: it applies what the view sends as an app does.
type client struct {
	out     chan json.RawMessage
	state   State
	home    []Entry
	rows    map[string][]Row
	replies map[string]json.RawMessage
	alerts  []string
	patches map[string][][2]int // each patch of a tab's rows: where it started, how many rows after it
	saved   atomic.Pointer[[]byte]
	end     func() // ends the Orb as its process exiting would
}

func newClient() *client {
	return &client{out: make(chan json.RawMessage, 4096), rows: map[string][]Row{}, replies: map[string]json.RawMessage{}, patches: map[string][][2]int{}}
}

func (c *client) apply(raw json.RawMessage) {
	var m struct {
		T, Tab, ID, Text string
		At               int
		Rows             []Row
		Result           json.RawMessage
		Error            string
	}
	_ = json.Unmarshal(raw, &m)
	switch m.T {
	case "state":
		c.state = State{}
		_ = json.Unmarshal(raw, &c.state)
	case "home":
		var h struct{ Entries []Entry }
		_ = json.Unmarshal(raw, &h)
		c.home = h.Entries
	case "rows":
		c.rows[m.Tab] = append(c.rows[m.Tab][:m.At], m.Rows...)
		c.patches[m.Tab] = append(c.patches[m.Tab], [2]int{m.At, len(c.rows[m.Tab])})
	case "reply":
		c.replies[m.ID] = m.Result
		if m.Error != "" {
			c.replies[m.ID] = json.RawMessage(fmt.Sprintf("%q", "error: "+m.Error))
		}
	case "alert":
		c.alerts = append(c.alerts, m.Text)
	}
}

// until applies what arrives until ok holds.
func (c *client) until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for !ok() {
		select {
		case raw := <-c.out:
			c.apply(raw)
		case <-deadline:
			t.Fatalf("never: %s\nstate: %s\nrows: %s", what, bridge.JSON(c.state), bridge.JSON(c.rows))
		}
	}
}

// orb is a machine's Bridge with one Orb on a faux model attached, which an app reaches as its owner.
func orb(t *testing.T, speed float64, steps ...faux.ResponseStep) (*App, *client, string) {
	t.Helper()
	return orbWith(t, nil, speed, steps...)
}

// orbWith is orb whose Orb also runs plugin, with an app's UI as Orbs a Bridge starts have.
func orbWith(t *testing.T, plugin extensions.Factory, speed float64, steps ...faux.ResponseStep) (*App, *client, string) {
	t.Helper()
	ctx := t.Context()
	b, err := bridge.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	peers := bridge.NewPeers(ctx, b)
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{TokensPerSecond: speed})
	provider.SetResponses(steps)
	options := agent.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: provider.GetModel(), StreamFn: provider.StreamSimple}
	if plugin != nil {
		loader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{CWD: cwd, AgentDir: options.AgentDir, NoContextFiles: true, NoThemes: true, ExtensionFactories: []extensions.Factory{plugin}})
		if err != nil {
			t.Fatal(err)
		}
		if err := loader.Reload(ctx, nil); err != nil {
			t.Fatal(err)
		}
		options.ResourceLoader = loader
	}
	host, err := agent.NewAgentSessionRuntime(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if plugin != nil {
		host.Session().BindExtensionUI(extensions.NoopUI{}, extensions.ModeRPC)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	enrolled, token, err := b.Enroll("test", b.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	a, err := attach.Attach(ctx, host, attach.Options{InstanceID: enrolled.ID, Store: &document.Memory{}, Authorize: b.Authorize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	generation, err := b.Attach(enrolled.ID, token, protocol.NewID(), bridge.NewLocal(a.Invoke))
	if err != nil {
		t.Fatal(err)
	}
	_ = a.SetGeneration(generation)
	c := newClient()
	c.end = func() {
		_ = a.Close()
		_, _ = b.Admin(ctx, "retire", bridge.JSON(map[string][]string{"instance_ids": {enrolled.ID}}))
	}
	app := New(ctx, Options{
		Call: func(ctx context.Context, method string, params, result any) error {
			raw, err := peers.Admin(ctx, method, bridge.JSON(params))
			if err != nil || result == nil {
				return err
			}
			return json.Unmarshal(raw, result)
		},
		SaveTabs: func(b []byte) { c.saved.Store(&b) },
		Name:     "this phone",
		Emit:     func(m any) { c.out <- bridge.JSON(m) },
	})
	app.Do([]byte(`{"do":"hello","budget":4}`))
	app.Do([]byte(`{"do":"visible","on":true}`))
	return app, c, enrolled.ID
}

func (c *client) tab(id string) Tab {
	for _, t := range c.state.Tabs {
		if t.ID == id {
			return t
		}
	}
	return Tab{}
}

// An app lists the Orb on Home, opens it, sends a message and draws the answer: what the person
// typed, then Orb's markdown a block per row, as the turn streams and once it ends.
func TestAnAppOpensAnOrbAndDrawsItsAnswer(t *testing.T) {
	answer := "Here is **the plan**:\n\n- read the diff\n- run the tests\n\n> a quote\n> on two lines\n\n```go\nfmt.Println(1)\n```"
	app, c, instance := orb(t, 0, faux.AssistantMessage(answer))
	c.until(t, "the Orb on Home", func() bool {
		return slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "i:"+instance && e.Unstored })
	})
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + instance + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "the conversation ready", func() bool { return c.tab(opened.Tab).Loaded })
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"what now?"}`))
	c.until(t, "the answer drawn", func() bool {
		rows := c.rows[opened.Tab]
		return len(rows) == 6 && !c.tab(opened.Tab).Busy && !c.tab(opened.Tab).Streaming
	})
	rows := c.rows[opened.Tab]
	if rows[0].Kind != "you" || rows[0].Text != "what now?" || rows[0].Via != "" {
		t.Fatalf("what the person said: %+v", rows[0])
	}
	if b := rows[1].Block; b.Type != "p" || !slices.ContainsFunc(b.Spans, func(s Span) bool { return s.Bold && s.Text == "the plan" }) {
		t.Fatalf("paragraph: %+v", b)
	}
	if b := rows[2].Block; b.Type != "li" || b.Mark != "·" || b.Spans[0].Text != "read the diff" || rows[3].Block.Spans[0].Text != "run the tests" {
		t.Fatalf("list: %+v %+v", rows[2].Block, rows[3].Block)
	}
	if b := rows[4].Block; b.Type != "quote" || len(b.Blocks) != 1 || b.Blocks[0].Spans[0].Text != "a quote\non two lines" {
		t.Fatalf("one quote of two lines: %+v", b)
	}
	if b := rows[5].Block; b.Type != "code" || b.Lang != "go" || b.Text != "fmt.Println(1)" {
		t.Fatalf("code: %+v", b)
	}
	if got := c.tab(opened.Tab); got.Title == "" || got.Remote || got.Where != "this phone" {
		t.Fatalf("tab: %+v", got)
	}
}

// A conversation streams as small patches: each one carries the rows from the first that
// changed, so a token resends the paragraph it grew, never the conversation.
func TestAStreamedAnswerArrivesAsSmallPatches(t *testing.T) {
	long := strings.Repeat("A first paragraph that the stream will not touch again. ", 40) + "\n\n" + strings.Repeat("word ", 400)
	app, c, instance := orb(t, 400, faux.AssistantMessage(long))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + instance + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "ready", func() bool { return c.tab(opened.Tab).Loaded })
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"go"}`))
	c.until(t, "the whole answer", func() bool { return len(c.rows[opened.Tab]) == 3 && !c.tab(opened.Tab).Busy })
	if len(c.alerts) != 0 {
		t.Fatalf("alerted about a conversation on screen: %v", c.alerts)
	}
	// Once the second paragraph streams, no patch carries the first again: each starts at the
	// paragraph it grew.
	patches := c.patches[opened.Tab]
	second := slices.IndexFunc(patches, func(p [2]int) bool { return p[1] >= 3 })
	if len(patches) < 5 || second < 0 || slices.ContainsFunc(patches[second+1:], func(p [2]int) bool { return p[0] < 2 }) {
		t.Fatalf("patches (start, rows): %v", patches)
	}
}

// A question from the questions plugin is walked one at a time and answered as one result; an
// approval is a choice.
func TestQuestionsAreWalkedAndAnsweredAsOne(t *testing.T) {
	tb := &tab{Tab: Tab{}, a: &App{}}
	tb.asked = "q1"
	tb.questions = []question{{ID: "colour", Question: "Colour?", Header: "Taste", Options: []struct{ Label string }{{"blue"}, {"red"}}}, {ID: "size", Question: "Size?"}}
	tb.Ask = tb.question()
	if tb.Ask.Message != "Taste  1 / 2" || !slices.Equal(tb.Ask.Choices, []string{"blue", "red"}) {
		t.Fatalf("first: %+v", tb.Ask)
	}
	blue, big := "blue", "big"
	tb.answer(&blue)
	if tb.Ask == nil || tb.Ask.Title != "Size?" {
		t.Fatalf("second: %+v", tb.Ask)
	}
	tb.answer(&big)
	if want := `[{"id":"colour","selected":["blue"]},{"custom":"big","id":"size","selected":[]}]`; string(bridge.JSON(tb.answers)) != want || tb.Ask != nil {
		t.Fatalf("answers: %s", bridge.JSON(tb.answers))
	}
}

// Two providers may offer a model of one name: the conversation's is the one its Orb runs, so a
// change of reasoning level keeps it on that provider.
func TestAModelIsKnownByItsProvider(t *testing.T) {
	describe := `{"model":"GPT-6 Luna","provider":"openai-codex","target":{"session_id":"s"},"models":[
		{"id":"gpt-6-luna","provider":"openai-codex","name":"GPT-6 Luna","thinking":["low","high"]},
		{"id":"gpt-6-luna","provider":"opencode-go","name":"GPT-6 Luna"}]}`
	a := &App{ctx: t.Context(), asked: map[*tab]bool{}, o: Options{Emit: func(any) {}, Call: func(_ context.Context, _ string, _, result any) error {
		return json.Unmarshal([]byte(describe), result)
	}}}
	tb := &tab{a: a, instance: "i"}
	if !tb.describe() || tb.Model != "openai-codex/gpt-6-luna" || len(tb.Levels) != 2 {
		t.Fatalf("model %q, levels %v", tb.Model, tb.Levels)
	}
}

// Earlier messages loaded above keep the keys of the rows already drawn, so an app keeps its place.
func TestEarlierMessagesKeepTheRowsKeys(t *testing.T) {
	var msgs []json.RawMessage
	for i := range 4 {
		msgs = append(msgs, json.RawMessage(fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":"q%d"}]}`, i)),
			json.RawMessage(fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":"a%d\n\nmore"}]}`, i)))
	}
	var window, all transcript
	window.load(msgs[4:], 4)
	all.load(msgs, 0)
	shown, _ := window.rows()
	grown, _ := all.rows()
	for i, r := range shown {
		if g := grown[len(grown)-len(shown)+i]; g.Key != r.Key {
			t.Fatalf("row %d: %q became %q", i, r.Key, g.Key)
		}
	}
}

// An answer that finishes out of sight alerts once and counts as unread until its tab shows.
func TestAnAnswerOutOfSightIsUnreadUntilShown(t *testing.T) {
	app, c, instance := orb(t, 0, faux.AssistantMessage("done"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + instance + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	c.until(t, "followed off screen", func() bool { return c.tab(opened.Tab).Loaded })
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"go"}`))
	c.until(t, "an unread answer", func() bool { return c.tab(opened.Tab).Unread == 1 && len(c.alerts) == 1 })
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "read once shown", func() bool { return c.tab(opened.Tab).Unread == 0 })
}

// A tab brought back after a restart follows the Orb that has its thread open, even on a machine
// that does not let this app start Orb (this one has no launcher at all).
func TestARestoredTabFollowsTheOrbThatHasItsThread(t *testing.T) {
	app, c, instance := orb(t, 0, faux.AssistantMessage("hello again"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + instance + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "ready", func() bool { return c.tab(opened.Tab).Loaded })
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"hi"}`))
	c.until(t, "kept to bring back", func() bool {
		return len(c.rows[opened.Tab]) == 2 && c.saved.Load() != nil && strings.Contains(string(*c.saved.Load()), opened.Tab)
	})
	again := newClient()
	restarted := New(t.Context(), Options{Call: app.o.Call, Tabs: *c.saved.Load(), Name: "this phone", Emit: func(m any) { again.out <- bridge.JSON(m) }})
	restarted.Do([]byte(`{"do":"hello","budget":4}`))
	restarted.Do([]byte(`{"do":"visible","on":true}`))
	restarted.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	again.until(t, "the conversation back", func() bool {
		rows := again.rows[opened.Tab]
		return len(rows) == 2 && rows[1].Block != nil && rows[1].Block.Spans[0].Text == "hello again" && again.tab(opened.Tab).Status == ""
	})
}

// Only web and mail links stay links: what a model writes cannot point an app at a file, a script
// or another app.
func TestOnlyWebAndMailLinksStayLinks(t *testing.T) {
	spans := Blocks("[site](https://orb.dev/a?b=1) [file](file:///etc/passwd) [js](javascript:alert(1)) [app](x-apple.systempreferences:x) [rel](notes.md) <me@orb.dev> https://ordalie.tech", true)[0].Spans
	links := map[string]string{}
	for _, s := range spans {
		links[strings.TrimSpace(s.Text)] = s.Href
	}
	want := map[string]string{"site": "https://orb.dev/a?b=1", "file": "", "js": "", "app": "", "rel": "", "me@orb.dev": "mailto:me@orb.dev", "https://ordalie.tech": "https://ordalie.tech"}
	for text, href := range want {
		if links[text] != href {
			t.Errorf("%q links to %q, want %q (spans %+v)", text, links[text], href, spans)
		}
	}
}

// An Orb that ended over there (its machine retires the ones it started when their plugins
// change) shows as ended, to reopen at the next message, instead of staying a live tab that
// drops what is sent to it.
func TestAnOrbThatEndsOverThereShowsEnded(t *testing.T) {
	app, c, instance := orb(t, 0, faux.AssistantMessage("hi"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + instance + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "ready", func() bool { return c.tab(opened.Tab).Loaded })
	c.end()
	c.until(t, "told it ended", func() bool { return c.tab(opened.Tab).Status == ended })
}

// A turn of plugin tools draws as the TUI shows it: its calls in the order made, the task list as
// the task in progress and how far it got, and a background job's end once.
func TestPluginToolsDrawInOrder(t *testing.T) {
	var tr transcript
	for _, e := range []string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[
			{"type":"toolCall","id":"a","name":"todo","arguments":{"items":[{"text":"read","status":"done"},{"text":"fix","status":"in_progress"},{"text":"ship","status":"pending"}]}},
			{"type":"toolCall","id":"b","name":"subagent","arguments":{"mode":"parallel","tasks":[{"task":"x"},{"task":"y"}]}}]}}`,
		`{"type":"tool_execution_end","toolCallId":"a","result":{"content":[{"type":"text","text":"[x] read\n→ [ ] fix\n[ ] ship"}]}}`,
		`{"type":"tool_execution_end","toolCallId":"b","result":{"content":[{"type":"text","text":"[1] scout\nx"}]}}`,
		`{"type":"message_start","message":{"role":"custom","customType":"job","content":"Job 1 exited with code 0","display":true}}`,
		`{"type":"message_end","message":{"role":"custom","customType":"job","content":"Job 1 exited with code 0","display":true}}`,
	} {
		tr.apply(json.RawMessage(e))
	}
	rows, _ := tr.rows()
	if len(rows) != 2 || rows[0].Kind != "run" || rows[1].Kind != "note" {
		t.Fatalf("rows: %+v", rows)
	}
	a := rows[0].Actions
	if len(a) != 2 || a[0].Verb != "tasks" || a[0].Target != "fix" || a[0].Result != "1 / 3 done" || a[1].Verb != "subagent" || a[1].Target != "2 tasks" {
		t.Fatalf("actions: %+v", a)
	}
}

// What a plugin shows beside a conversation reaches the app as slots: its widget's lines, and its
// last notice until the next run starts.
func TestAPluginsWidgetAndNoticeShowBesideTheConversation(t *testing.T) {
	runs := 0
	plugin := func(api extensions.API) error {
		api.On(extensions.EventAgentStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			runs++
			ctx.UI().SetWidget("tasks", &extensions.Widget{Lines: []string{fmt.Sprintf("✓ %d/2  → fix it", runs)}}, nil)
			return nil, nil
		})
		api.On(extensions.EventAgentEnd, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			if runs == 1 {
				ctx.UI().Notify("a server needs attention", extensions.NotifyError)
			}
			return nil, nil
		})
		return nil
	}
	app, c, instance := orbWith(t, plugin, 0, faux.AssistantMessage("one"), faux.AssistantMessage("two"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + instance + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "ready", func() bool { return c.tab(opened.Tab).Loaded })
	slots := func() string { return fmt.Sprint(c.tab(opened.Tab).Slots) }
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"go"}`))
	c.until(t, "the widget and the notice", func() bool {
		return slots() == "[{notice [a server needs attention] true} {widget:tasks [✓ 1/2  → fix it] false}]"
	})
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"again"}`))
	c.until(t, "the notice gone, the widget current", func() bool { return slots() == "[{widget:tasks [✓ 2/2  → fix it] false}]" })
}
