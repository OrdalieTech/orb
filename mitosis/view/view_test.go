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
	"github.com/OrdalieTech/orb/agent/extensions"
	attach "github.com/OrdalieTech/orb/agent/mitosis"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
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
			t.Fatalf("never: %s\nstate: %s\nrows: %s", what, mitosis.JSON(c.state), mitosis.JSON(c.rows))
		}
	}
}

// orb is a machine's Mitosis with one Orb on a faux model attached, which an app reaches as its
// owner, and the session it holds.
func orb(t *testing.T, speed float64, steps ...faux.ResponseStep) (*App, *client, string) {
	t.Helper()
	return orbWith(t, nil, speed, steps...)
}

// orbWith is orb whose Orb also runs plugin, with an app's UI as Orbs a Mitosis starts have.
func orbWith(t *testing.T, plugin extensions.Factory, speed float64, steps ...faux.ResponseStep) (*App, *client, string) {
	t.Helper()
	ctx := t.Context()
	b, err := mitosis.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	peers := mitosis.NewPeers(ctx, b)
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
	enrolled, token, err := b.Enroll("test")
	if err != nil {
		t.Fatal(err)
	}
	a, err := attach.Attach(ctx, host, attach.Options{InstanceID: enrolled.ID, Store: &document.Memory{}, Authorize: b.Authorize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	generation, err := b.Attach(enrolled.ID, token, protocol.NewID(), mitosis.NewLocal(a.Invoke))
	if err != nil {
		t.Fatal(err)
	}
	_ = a.SetGeneration(generation)
	c := newClient()
	c.end = func() {
		_ = a.Close()
		_, _ = b.Admin(ctx, "retire", mitosis.JSON(map[string][]string{"instance_ids": {enrolled.ID}}))
	}
	app := New(ctx, Options{
		Call: func(ctx context.Context, method string, params, result any) error {
			raw, err := peers.Admin(ctx, method, mitosis.JSON(params))
			if err != nil || result == nil {
				return err
			}
			return json.Unmarshal(raw, result)
		},
		// The view saves the tabs just after it sends their rows, so a save wakes until as well.
		SaveTabs: func(b []byte) { c.saved.Store(&b); c.out <- json.RawMessage(`{"t":"saved"}`) },
		Name:     "this phone",
		Emit:     func(m any) { c.out <- mitosis.JSON(m) },
	})
	app.Do([]byte(`{"do":"hello","budget":4}`))
	app.Do([]byte(`{"do":"visible","on":true}`))
	return app, c, host.Session().Manager().GetSessionID()
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
	app, c, session := orb(t, 0, faux.AssistantMessage(answer))
	c.until(t, "the Orb on Home", func() bool {
		return slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "i:"+session && e.Unstored })
	})
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + session + `"}`))
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
	app, c, session := orb(t, 400, faux.AssistantMessage(long))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + session + `"}`))
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

// A question from the questions plugin is walked one at a time and answered as one result, with
// multi_select the choices ticked and a line; an approval is a choice.
func TestQuestionsAreWalkedAndAnsweredAsOne(t *testing.T) {
	tb := &tab{Tab: Tab{}, a: &App{}}
	tb.asked = "q1"
	tb.questions = []question{{ID: "colour", Question: "Colour?", Header: "Taste", Options: []struct{ Label string }{{"blue"}, {"red"}}}, {ID: "size", Question: "Size?"},
		{ID: "extras", Question: "Extras?", MultiSelect: true, Options: []struct{ Label string }{{"nuts"}, {"cream"}}}}
	tb.Ask = tb.question()
	if tb.Ask.Message != "Taste  1 / 3" || !slices.Equal(tb.Ask.Choices, []string{"blue", "red"}) {
		t.Fatalf("first: %+v", tb.Ask)
	}
	blue, big := "blue", "big"
	tb.answer(&blue)
	if tb.Ask == nil || tb.Ask.Title != "Size?" {
		t.Fatalf("second: %+v", tb.Ask)
	}
	tb.answer(&big)
	if tb.Ask == nil || !tb.Ask.Multi {
		t.Fatalf("third: %+v", tb.Ask)
	}
	more := "more"
	tb.answer(&more, "nuts", "cream")
	if want := `[{"id":"colour","selected":["blue"]},{"custom":"big","id":"size","selected":[]},{"custom":"more","id":"extras","selected":["nuts","cream"]}]`; string(mitosis.JSON(tb.answers)) != want || tb.Ask != nil {
		t.Fatalf("answers: %s", mitosis.JSON(tb.answers))
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
	tb := &tab{a: a, running: true, session: "s"}
	if !tb.describe() || tb.Model != "openai-codex/gpt-6-luna" || len(tb.Levels) != 2 {
		t.Fatalf("model %q, levels %v", tb.Model, tb.Levels)
	}
}

// Out of sight, a describe that lands mid-turn, its answer already written, does not hide the
// turn's end from the one that finds it over.
func TestAnAnswerDescribedMidTurnStillEndsUnread(t *testing.T) {
	describe := ""
	a := &App{ctx: t.Context(), asked: map[*tab]bool{}, o: Options{Emit: func(any) {}, Call: func(_ context.Context, _ string, _, result any) error {
		return json.Unmarshal([]byte(describe), result)
	}}}
	tb := &tab{a: a, running: true, session: "s"}
	a.tabs = []*tab{tb}
	for _, step := range []struct {
		describe string
		unread   int
	}{
		{`{"target":{"session_id":"s"},"stats":{"assistantMessages":1}}`, 0},
		{`{"target":{"session_id":"s","execution_id":"x"},"stats":{"assistantMessages":2}}`, 0},
		{`{"target":{"session_id":"s"},"stats":{"assistantMessages":2}}`, 1},
		{`{"target":{"session_id":"s"},"stats":{"assistantMessages":2}}`, 1},
	} {
		describe = step.describe
		if !tb.describe() || tb.Unread != step.unread {
			t.Fatalf("after %s: unread %d, want %d", step.describe, tb.Unread, step.unread)
		}
	}
}

// An app starting has Home read before it says it shows, so Home is there as it does.
func TestHomeIsReadBeforeTheAppShows(t *testing.T) {
	c := newClient()
	New(t.Context(), Options{Name: "this phone", Emit: func(m any) { c.out <- mitosis.JSON(m) }, Call: func(_ context.Context, method string, params, result any) error {
		reply := `{"peer_id":"me"}`
		if method == "remote" {
			reply = `{}`
			if params.(map[string]any)["method"] == "host.sessions" {
				reply = `{"items":[{"session_id":"s","name":"tides","cwd":"/w","modified":1}]}`
			}
		}
		return json.Unmarshal([]byte(reply), result)
	}})
	c.until(t, "Home", func() bool { return len(c.home) == 1 && c.home[0].Title == "tides" })
}

// A device that connects once the app shows (a tab dialing its devices) has its threads on Home
// at once, not at the next periodic read of every device's threads.
func TestADeviceThatConnectsHasItsThreadsReadAtOnce(t *testing.T) {
	c := newClient()
	var up atomic.Bool
	app := New(t.Context(), Options{Name: "this tab", Emit: func(m any) { c.out <- mitosis.JSON(m) }, Call: func(_ context.Context, method string, params, result any) error {
		reply := fmt.Sprintf(`{"peer_id":"me","peers":["pc"],"peer_states":{"pc":%q}}`, map[bool]string{true: "connected", false: "disconnected"}[up.Load()])
		if method == "remote" {
			p := params.(map[string]any)
			if p["peer_id"] == "pc" && !up.Load() {
				return mitosis.Fail("unavailable")
			}
			reply = `{}`
			if p["peer_id"] == "pc" && p["method"] == "host.sessions" {
				reply = `{"items":[{"session_id":"s","name":"tides","cwd":"/w","modified":1}]}`
			}
		}
		return json.Unmarshal([]byte(reply), result)
	}})
	app.Do([]byte(`{"do":"visible","on":true}`))
	time.Sleep(500 * time.Millisecond)
	up.Store(true)
	c.until(t, "the device's threads", func() bool { return len(c.home) == 1 && c.home[0].Title == "tides" })
}

// A conversation shown asleep reads as it was while its Orb starts, is not said asleep, and keeps
// its rows until the Orb has sent its own.
func TestAnAsleepTabShowsItsThreadWhileItsOrbStarts(t *testing.T) {
	c := newClient()
	started := make(chan struct{})
	message := `{"role":"user","content":"tides?","timestamp":1}`
	a := New(t.Context(), Options{Name: "this phone", Tabs: []byte(`[{"ID":"x","Peer":"me","Session":"s"}]`), Emit: func(m any) { c.out <- mitosis.JSON(m) }, Call: func(ctx context.Context, method string, params, result any) error {
		reply := `{"peer_id":"me"}`
		if method == "remote" {
			reply = `{}`
			switch params.(map[string]any)["method"] {
			case "agent.call":
				<-started
				return nil
			case "conversation.describe":
				reply = `{"target":{"session_id":"s"}}`
			case "host.read":
				reply = `{"messages":[` + message + `],"from":0}`
			case "events.subscribe":
				reply = `{"messages":[` + message + `,` + message + `],"from":0,"cursor":"c"}`
			}
		}
		return json.Unmarshal([]byte(reply), result)
	}})
	a.Do([]byte(`{"do":"hello","budget":4}`))
	a.Do([]byte(`{"do":"visible","on":true}`))
	a.Do([]byte(`{"do":"show","tabs":["x"]}`))
	c.until(t, "the thread while its Orb starts", func() bool {
		return len(c.rows["x"]) == 1 && c.tab("x").Status == "opening the thread…" && c.tab("x").Asleep
	})
	close(started)
	c.until(t, "the Orb's own rows", func() bool { return len(c.rows["x"]) == 2 && !c.tab("x").Asleep })
	for _, p := range c.patches["x"] {
		if p[1] == 0 {
			t.Fatalf("the rows went blank as its Orb started: %v", c.patches["x"])
		}
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

// An answer that finishes out of sight counts as unread until its tab shows, and alerts once
// while the app itself is out of sight: on screen, its unread count says it.
func TestAnAnswerOutOfSightIsUnreadUntilShown(t *testing.T) {
	app, c, session := orb(t, 0, faux.AssistantMessage("done"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + session + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	c.until(t, "followed off screen", func() bool { return c.tab(opened.Tab).Loaded })
	app.Do([]byte(`{"do":"visible","on":false}`))
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"go"}`))
	c.until(t, "an unread answer", func() bool { return c.tab(opened.Tab).Unread == 1 && len(c.alerts) == 1 })
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "read once shown", func() bool { return c.tab(opened.Tab).Unread == 0 })
}

// A tab brought back after a restart follows the Orb that has its thread open, even on a machine
// that does not let this app start Orb (this one has no launcher at all).
func TestARestoredTabFollowsTheOrbThatHasItsThread(t *testing.T) {
	app, c, session := orb(t, 0, faux.AssistantMessage("hello again"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + session + `"}`))
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
	restarted := New(t.Context(), Options{Call: app.o.Call, Tabs: *c.saved.Load(), Name: "this phone", Emit: func(m any) { again.out <- mitosis.JSON(m) }})
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
	app, c, session := orb(t, 0, faux.AssistantMessage("hi"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + session + `"}`))
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
	if len(rows) != 3 || rows[0].Kind != "run" || rows[1].Kind != "peer" || rows[2].Kind != "note" {
		t.Fatalf("rows: %+v", rows)
	}
	a := rows[0].Actions
	if len(a) != 1 || a[0].Verb != "tasks" || a[0].Target != "fix" || a[0].Result != "1 / 3 done" {
		t.Fatalf("actions: %+v", a)
	}
	if ex := rows[1].Exchange; ex == nil || ex.Scope != "subagent" || ex.Who != "worker, worker" || ex.Status != "done" {
		t.Fatalf("subagent exchange: %+v", ex)
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
	app, c, session := orbWith(t, plugin, 0, faux.AssistantMessage("one"), faux.AssistantMessage("two"))
	c.until(t, "the Orb on Home", func() bool { return len(c.home) > 0 })
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + session + `"}`))
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

// Only the conversation a peer's prompt runs in says a peer acts here: the others on this
// machine, busy with their own prompts or at rest, do not.
func TestOnlyTheConversationAPeerDrivesSaysSo(t *testing.T) {
	a := &App{self: "me"}
	driven := &tab{a: a, Tab: Tab{ID: "driven", Peer: "me", Busy: true}}
	driven.tr.add(&item{kind: "y", text: "go", via: "mitosis"})
	mine := &tab{a: a, Tab: Tab{ID: "mine", Peer: "me", Busy: true}}
	mine.tr.add(&item{kind: "y", text: "go"})
	a.tabs = []*tab{driven, mine, {a: a, Tab: Tab{ID: "idle", Peer: "me"}}}
	s := a.state()
	if !s.Acting || !s.Tabs[0].Acting || s.Tabs[1].Acting || s.Tabs[2].Acting {
		t.Fatalf("acting: %v, tabs: %+v", s.Acting, s.Tabs)
	}
}

// An edit opens on what it changed, live and replayed alike.
func TestAnEditOpensOnItsDiff(t *testing.T) {
	diff := `  4 func add() {\n-5 \treturn a\n+5 \t// sum\n+6 \treturn a\n`
	var tr transcript
	for _, e := range []string{
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"a","name":"edit","arguments":{"path":"main.go"}}]}}`,
		`{"type":"tool_execution_end","toolCallId":"a","result":{"content":[{"type":"text","text":"ok"}],"details":{"diff":"` + diff + `"}}}`,
	} {
		tr.apply(json.RawMessage(e))
	}
	var again transcript
	again.load([]json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"toolCall","id":"a","name":"edit","arguments":{"path":"main.go"}}]}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"a","content":[{"type":"text","text":"ok"}],"details":{"diff":"` + diff + `"}}`),
	}, 0)
	for _, x := range []*transcript{&tr, &again} {
		rows, _ := x.rows()
		if d := x.detail(rows[0].Actions[0].Key); !strings.HasPrefix(d["diff"], "  4 func add()") {
			t.Fatalf("detail: %q", d)
		}
	}
}

// A /new sent the moment a conversation opens, before its Orb described it, still replaces the
// session and runs the message in the new one.
func TestNewRightAfterOpeningRunsInTheNewSession(t *testing.T) {
	app, c, session := orb(t, 0, faux.AssistantMessage("fresh"))
	c.until(t, "the Orb on Home", func() bool {
		return slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "i:"+session })
	})
	app.Do([]byte(`{"id":"1","do":"open","key":"i:` + session + `"}`))
	c.until(t, "the tab", func() bool { return c.replies["1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"/new hello"}`))
	c.until(t, "the answer drawn", func() bool {
		rows := c.rows[opened.Tab]
		return len(rows) == 2 && rows[0].Text == "hello" && !c.tab(opened.Tab).Busy
	})
	app.mu.Lock()
	now := app.tabs[0].session
	app.mu.Unlock()
	if now == session {
		t.Fatalf("the message ran in the session it should replace")
	}
	if via := c.rows[opened.Tab][0].Via; via != "" {
		t.Fatalf("message sent via /new was marked via=%q, want empty", via)
	}
}

// Approving a pairing from an app admits the paired device to this owner's sync group, as `orb
// mitosis` does, and a non-owner member (unauthorized) still succeeds at pairing.
func TestApproveAdmitsToSyncGroup(t *testing.T) {
	c := newClient()
	var added atomic.Value
	app := New(t.Context(), Options{Name: "this Mac", Emit: func(m any) { c.out <- mitosis.JSON(m) }, Call: func(_ context.Context, method string, params, result any) error {
		switch method {
		case "status":
			return json.Unmarshal([]byte(`{"peer_id":"me","pending":[{"invitation_id":"inv1","claimant":"phone","status":"pending"}]}`), result)
		case "approve":
			return nil
		case "sync":
			if p, ok := params.(map[string]string); ok {
				added.Store(p["add"])
			}
			return nil
		case "remote":
			return json.Unmarshal([]byte(`{}`), result)
		}
		return nil
	}})
	c.until(t, "pending claim", func() bool { return c.state.Claim != nil && c.state.Claim.Claimant == "phone" })
	app.Do([]byte(`{"id":"a1","do":"approve"}`))
	c.until(t, "approve reply", func() bool { _, ok := c.replies["a1"]; return ok })
	if got := c.replies["a1"]; len(got) > 0 {
		t.Fatalf("unexpected approve error: %s", got)
	}
	if got, _ := added.Load().(string); got != "phone" {
		t.Fatalf("sync add = %q, want phone", got)
	}
}

// A connected peer that refuses host.sessions (no host.launch grant, or a Worker/tab) stops
// reporting Reading once that refusal arrives, so Home does not spin "reading ..." forever.
func TestConnectedPeerWithoutLaunchStopsReading(t *testing.T) {
	c := newClient()
	var machines atomic.Pointer[[]Machine]
	app := New(t.Context(), Options{Name: "this tab", Emit: func(m any) {
		raw := mitosis.JSON(m)
		var h struct {
			T        string
			Machines []Machine
		}
		if json.Unmarshal(raw, &h) == nil && h.T == "home" {
			machines.Store(&h.Machines)
		}
		c.out <- raw
	}, Call: func(_ context.Context, method string, params, result any) error {
		if method == "status" {
			return json.Unmarshal([]byte(`{"peer_id":"me","peers":["old","worker"],"peer_states":{"old":"connected","worker":"connected"},
				"group_id":"g","scopes":{"g":["me","old"]}}`), result)
		}
		if method == "remote" {
			p := params.(map[string]any)
			if p["method"] == "host.sessions" {
				return mitosis.Fail("unauthorized")
			}
			return json.Unmarshal([]byte(`{}`), result)
		}
		return nil
	}})
	app.Do([]byte(`{"do":"visible","on":true}`))
	// A paired machine refusing this app only follows; a group member refusing it runs an Orb
	// from before group access, and says so.
	c.until(t, "worker and old no longer reading", func() bool {
		list := machines.Load()
		if list == nil {
			return false
		}
		done := 0
		for _, m := range *list {
			if m.Connected && !m.Reading && !m.Launch && (m.ID == "worker" && !m.Outdated || m.ID == "old" && m.Outdated) {
				done++
			}
		}
		return done == 2
	})
}

// Non-paired sync-group members appear in Home with names seeded from peer_names, dial on demand
// to populate threads and peer count, and back off when offline instead of hammering remote.
func TestGroupMembersSeedNamesAndOfflinePeersBackOff(t *testing.T) {
	c := newClient()
	var machines atomic.Pointer[[]Machine]
	var offlineCalls atomic.Int32
	app := New(t.Context(), Options{Name: "this tab", Emit: func(m any) {
		raw := mitosis.JSON(m)
		var h struct {
			T        string
			Machines []Machine
		}
		if json.Unmarshal(raw, &h) == nil && h.T == "home" {
			machines.Store(&h.Machines)
		}
		c.out <- raw
	}, Call: func(_ context.Context, method string, params, result any) error {
		if method == "status" {
			return json.Unmarshal([]byte(`{
				"peer_id":"me",
				"peers":["fedora","offline"],
				"peer_states":{"fedora":"disconnected","offline":"disconnected"},
				"peer_names":{"fedora":"Fedora","offline":"lab-3"}
			}`), result)
		}
		if method == "remote" {
			p := params.(map[string]any)
			if p["peer_id"] == "offline" {
				offlineCalls.Add(1)
				return mitosis.Fail("unavailable")
			}
			if p["peer_id"] == "fedora" {
				if p["method"] == "host.sessions" {
					return json.Unmarshal([]byte(`{"host":"Fedora","items":[{"session_id":"s1","name":"build","cwd":"/src","modified":10}]}`), result)
				}
				return json.Unmarshal([]byte(`{}`), result)
			}
		}
		return nil
	}})
	app.Do([]byte(`{"do":"visible","on":true}`))
	c.until(t, "group members loaded", func() bool {
		list := machines.Load()
		if list == nil || len(c.home) != 1 || c.home[0].Title != "build" || c.state.Summary != "ready · 1 peer" {
			return false
		}
		var sawFedora, sawOffline bool
		for _, m := range *list {
			if m.ID == "fedora" && m.Name == "Fedora" && m.Connected && m.Launch {
				sawFedora = true
			}
			if m.ID == "offline" && m.Name == "lab-3" && !m.Connected {
				sawOffline = true
			}
		}
		return sawFedora && sawOffline && offlineCalls.Load() > 0
	})
	// Let the initial offline probe finish setting retryAt, then verify extra refreshes back off.
	time.Sleep(50 * time.Millisecond)
	before := offlineCalls.Load()
	app.wakeMachines()
	time.Sleep(200 * time.Millisecond)
	app.wakeMachines()
	time.Sleep(200 * time.Millisecond)
	if after := offlineCalls.Load(); after != before {
		t.Fatalf("offline peer hammered during backoff: before=%d after=%d", before, after)
	}
}

func TestInlineAttach(t *testing.T) {
	c := newClient()
	var wroteName string
	var wroteData []byte
	var remoteChunks []byte
	var remoteName string
	app := New(t.Context(), Options{
		Emit: func(m any) { c.out <- mitosis.JSON(m) },
		WriteFile: func(name string, data []byte) (string, error) {
			wroteName, wroteData = name, append([]byte(nil), data...)
			return "attachments/" + name, nil
		},
		Call: func(_ context.Context, method string, params, result any) error {
			if method == "status" {
				return json.Unmarshal([]byte(`{"peer_id":"me","peers":["box"],"peer_states":{"box":"connected"}}`), result)
			}
			if method == "remote" {
				p := params.(map[string]any)
				if p["method"] == "host.attach" {
					args := p["params"].(map[string]any)
					remoteName = args["name"].(string)
					remoteChunks = append(remoteChunks, args["data"].([]byte)...)
					return json.Unmarshal([]byte(`{"path":"/tmp/orb-attach/shot.png"}`), result)
				}
				return json.Unmarshal([]byte(`{}`), result)
			}
			return nil
		},
	})
	c.until(t, "self loaded", func() bool { return c.state.Self == "me" })

	app.Do([]byte(`{"id":"a1","do":"attach","machine":"me","name":"notes.txt","data":"aGVsbG8="}`))
	c.until(t, "local attach reply", func() bool { return len(c.replies["a1"]) > 0 })
	if strings.Contains(string(c.replies["a1"]), "error:") {
		t.Fatalf("local inline attach failed: %s", c.replies["a1"])
	}
	if wroteName != "notes.txt" || string(wroteData) != "hello" {
		t.Fatalf("unexpected WriteFile(%q, %q)", wroteName, string(wroteData))
	}

	app.Do([]byte(`{"id":"a2","do":"attach","machine":"box","name":"shot.png","data":"d29ybGQ="}`))
	c.until(t, "remote attach reply", func() bool { return len(c.replies["a2"]) > 0 })
	if strings.Contains(string(c.replies["a2"]), "error:") {
		t.Fatalf("remote inline attach failed: %s", c.replies["a2"])
	}
	if remoteName != "shot.png" || string(remoteChunks) != "world" {
		t.Fatalf("unexpected remote attach (%q, %q)", remoteName, string(remoteChunks))
	}
}

// A prompt that fails asynchronously during preflight (e.g. no model selected or missing API key)
// surfaces the error in tab.Status instead of leaving the conversation silently stuck on "ready".
func TestFailedPromptSurfacesErrorInsteadOfReady(t *testing.T) {
	ctx := t.Context()
	b, err := mitosis.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	peers := mitosis.NewPeers(ctx, b)
	cwd := t.TempDir()
	manager, _ := session.InMemory(cwd)
	// No model and no credentials: PromptControlled fails preflight with "No model selected." / "No API key found".
	host, err := agent.NewAgentSessionRuntime(ctx, agent.AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Resources: &agent.Resources{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	enrolled, token, err := b.Enroll("empty-model")
	if err != nil {
		t.Fatal(err)
	}
	a, err := attach.Attach(ctx, host, attach.Options{InstanceID: enrolled.ID, Store: &document.Memory{}, Authorize: b.Authorize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	gen, err := b.Attach(enrolled.ID, token, protocol.NewID(), mitosis.NewLocal(a.Invoke))
	if err != nil {
		t.Fatal(err)
	}
	_ = a.SetGeneration(gen)
	c := newClient()
	app := New(ctx, Options{
		Call: func(ctx context.Context, method string, params, result any) error {
			raw, err := peers.Admin(ctx, method, mitosis.JSON(params))
			if err != nil || result == nil {
				return err
			}
			return json.Unmarshal(raw, result)
		},
		Name: "this tab",
		Emit: func(m any) { c.out <- mitosis.JSON(m) },
	})
	app.Do([]byte(`{"do":"hello","budget":4}`))
	app.Do([]byte(`{"do":"visible","on":true}`))
	sess := host.Session().Manager().GetSessionID()
	c.until(t, "instance visible", func() bool {
		return slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "i:"+sess })
	})
	app.Do([]byte(`{"id":"o1","do":"open","key":"i:` + sess + `"}`))
	c.until(t, "tab opened", func() bool { return c.replies["o1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["o1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "tab loaded", func() bool { return c.tab(opened.Tab).Loaded })

	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"hello?"}`))
	c.until(t, "prompt failure surfaced in Status", func() bool {
		st := c.tab(opened.Tab).Status
		return st != "" && st != "sending…"
	})
	if st := c.tab(opened.Tab).Status; !strings.Contains(st, "No model selected") && !strings.Contains(st, "No API key found") {
		t.Fatalf("status = %q, want model/auth preflight error", st)
	}
}

// A prompt whose revision went stale (e.g. a concurrent model change bumped session_revision)
// automatically re-describes and retries once instead of vanishing silently.
func TestStaleTargetPromptRetriesOnce(t *testing.T) {
	c := newClient()
	var promptCalls atomic.Int32
	var revision atomic.Int32
	revision.Store(1)
	app := New(t.Context(), Options{
		Emit: func(m any) { c.out <- mitosis.JSON(m) },
		Name: "this tab",
		Call: func(_ context.Context, method string, params, result any) error {
			if method == "status" {
				return json.Unmarshal([]byte(`{"peer_id":"me","peers":[],"peer_states":{}}`), result)
			}
			if method != "remote" {
				return nil
			}
			p := params.(map[string]any)
			switch p["method"] {
			case "agent.describe":
				return json.Unmarshal([]byte(`{"items":[{"session_id":"s1","cwd":"/work","state":"idle"}]}`), result)
			case "conversation.describe":
				rev := fmt.Sprintf("%d", revision.Load())
				return json.Unmarshal([]byte(`{"registration_generation":"1","waits":false,"target":{"session_id":"s1","session_revision":"`+rev+`"}}`), result)
			case "events.subscribe":
				return json.Unmarshal([]byte(`{"snapshot_id":"snap","messages":[],"cursor":"c1","events":[],"state":"p1"}`), result)
			case "agent.call":
				call := p["params"].(mitosis.AgentCall)
				if call.Method == "prompt" {
					n := promptCalls.Add(1)
					if n == 1 {
						revision.Store(2)
						return json.Unmarshal([]byte(`{"operation_id":"op-stale","status":"accepted","session_id":"s1"}`), result)
					}
					if call.Expected.Revision != "2" {
						t.Errorf("retry expected revision 2, got %q", call.Expected.Revision)
					}
					return json.Unmarshal([]byte(`{"operation_id":"op-ok","status":"accepted","session_id":"s1"}`), result)
				}
			case "operations.get":
				opID := p["params"].(map[string]string)["operation_id"]
				if opID == "op-stale" {
					return json.Unmarshal([]byte(`{"status":"failed","error":"stale_target"}`), result)
				}
				return json.Unmarshal([]byte(`{"status":"succeeded"}`), result)
			}
			return json.Unmarshal([]byte(`{}`), result)
		},
	})
	app.Do([]byte(`{"do":"hello","budget":4}`))
	app.Do([]byte(`{"do":"visible","on":true}`))
	c.until(t, "s1 visible", func() bool {
		return slices.ContainsFunc(c.home, func(e Entry) bool { return e.Key == "i:s1" })
	})
	app.Do([]byte(`{"id":"o1","do":"open","key":"i:s1"}`))
	c.until(t, "opened", func() bool { return c.replies["o1"] != nil })
	var opened struct{ Tab string }
	_ = json.Unmarshal(c.replies["o1"], &opened)
	app.Do([]byte(`{"do":"show","tabs":["` + opened.Tab + `"]}`))
	c.until(t, "loaded", func() bool { return c.tab(opened.Tab).Loaded })

	app.Do([]byte(`{"do":"send","tab":"` + opened.Tab + `","text":"retry me"}`))
	c.until(t, "prompt retried and succeeded", func() bool {
		return promptCalls.Load() == 2 && c.tab(opened.Tab).Status == ""
	})
}

func TestToolFormattingAndStandaloneEdits(t *testing.T) {
	var tr transcript
	diff := `  1 a\n-2 old\n+2 new1\n+3 new2\n`
	tr.load([]json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[
			{"type":"toolCall","id":"r1","name":"read","arguments":{"path":"a.go","offset":10,"limit":20}},
			{"type":"toolCall","id":"b1","name":"bash","arguments":{"command":"go test ./..."}},
			{"type":"toolCall","id":"m1","name":"mcp__docs__fetch","arguments":{"url":"https://example.com"}},
			{"type":"toolCall","id":"e1","name":"edit","arguments":{"path":"a.go"}},
			{"type":"toolCall","id":"w1","name":"write","arguments":{"path":"b.go","content":"line1\nline2\nline3\n"}}
		]}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"r1","content":[{"type":"text","text":"l1\nl2\nl3"}]}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"b1","isError":true,"content":[{"type":"text","text":"FAIL\nCommand exited with code 1"}]}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"m1","content":[{"type":"text","text":"ok"}]}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"e1","content":[{"type":"text","text":"ok"}],"details":{"diff":"` + diff + `"}}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"w1","content":[{"type":"text","text":"ok"}]}`),
	}, 0)

	rows, _ := tr.rows()
	// Quiet calls fold into one run; edit and write stay surfaced as standalone rows.
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (run + edit + write), got %d: %+v", len(rows), rows)
	}
	if len(rows[0].Actions) != 3 {
		t.Fatalf("expected 3 quiet actions in first run, got %+v", rows[0].Actions)
	}
	if got := rows[0].Actions[0]; got.Target != "a.go:10-29" || got.Result != "3 ln" {
		t.Fatalf("read action = %+v", got)
	}
	if d := tr.detail(rows[0].Actions[0].Key); d["args"] != "" {
		t.Fatalf("read detail should omit redundant path json: %+v", d)
	}
	if got := rows[0].Actions[1]; got.Result != "exit 1" || !got.Failed {
		t.Fatalf("failed bash action = %+v", got)
	}
	if d := tr.detail(rows[0].Actions[1].Key); d["args"] != "$ go test ./..." {
		t.Fatalf("bash detail args = %q", d["args"])
	}
	if got := rows[0].Actions[2]; got.Verb != "docs · fetch" {
		t.Fatalf("mcp verb = %q", got.Verb)
	}
	if got := rows[1].Actions; len(got) != 1 || got[0].Verb != "edit" || got[0].Result != "+2 -1" {
		t.Fatalf("edit row = %+v", got)
	}
	if got := rows[2].Actions; len(got) != 1 || got[0].Verb != "write" || got[0].Result != "+3 ln" {
		t.Fatalf("write row = %+v", got)
	}
	if d := tr.detail(rows[2].Actions[0].Key); d["args"] != "line1\nline2\nline3\n" {
		t.Fatalf("write detail args = %q", d["args"])
	}
}

func TestDropAppSlotFiltersTerminalOnlyGlyphs(t *testing.T) {
	cases := []struct {
		slot Slot
		drop bool
	}{
		{Slot{Key: "status:mitosis", Lines: []string{"○"}}, true},
		{Slot{Key: "status:provider-usage", Lines: []string{"5h 70%"}}, true},
		{Slot{Key: "status:custom", Lines: []string{"\x1b[32m●\x1b[0m"}}, true},
		{Slot{Key: "widget:tasks", Lines: []string{"✓ 1/2  → fix it"}}, false},
		{Slot{Key: "notice", Lines: []string{"a server needs attention"}, Alarm: true}, false},
	}
	for _, tc := range cases {
		if got := dropAppSlot(tc.slot); got != tc.drop {
			t.Errorf("dropAppSlot(%+v) = %v, want %v", tc.slot, got, tc.drop)
		}
	}
}

func TestTabBusyTracksEventsSnapshotAndOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var step atomic.Int32
	a := &App{
		ctx: ctx, self: "me", asked: map[*tab]bool{},
		o: Options{Emit: func(any) {}, Call: func(callCtx context.Context, method string, params, result any) error {
			if method != "remote" {
				return nil
			}
			raw := mitosis.JSON(params)
			var req struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(raw, &req)
			if req.Method != "events.subscribe" {
				<-callCtx.Done()
				return callCtx.Err()
			}
			switch step.Add(1) {
			case 1:
				return json.Unmarshal([]byte(`{"snapshot_id":"s1","cursor":"c1","messages":[],"partial":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`), result)
			case 2:
				return json.Unmarshal([]byte(`{"cursor":"c2","state":"p1","events":[{"data":{"type":"agent_start"}}]}`), result)
			case 3:
				return json.Unmarshal([]byte(`{"cursor":"c3","state":"p1","events":[{"data":{"type":"agent_end"}}]}`), result)
			}
			return nil
		}},
		machines: []*machine{{id: "peer1", host: "lab", state: "connected", instances: []instance{{session: "sess1", title: "Build", busy: true}}}},
	}
	tab := &tab{a: a, Tab: Tab{ID: "t1", Peer: "peer1"}, session: "sess1", running: true, pulse: "p1", wake: make(chan struct{}, 1), done: make(chan struct{})}
	tab.snapshot()
	if !tab.Busy {
		t.Fatal("snapshot with partial did not mark tab busy")
	}
	tab.Busy = false
	tab.events(true)
	if !tab.Busy {
		t.Fatal("agent_start event did not mark tab busy immediately")
	}
	tab.events(true)
	if tab.Busy {
		t.Fatal("agent_end event did not clear tab busy")
	}
	res, err := a.open("i:sess1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := res.(map[string]string)["tab"]
	a.mu.Lock()
	opened := a.tab(id)
	busy := opened != nil && opened.Busy
	if opened != nil {
		close(opened.done)
	}
	a.mu.Unlock()
	if !busy {
		t.Fatal("opening a busy running instance did not start tab with Busy=true")
	}
}

func TestTranscriptPeerAndSubagentExchanges(t *testing.T) {
	var tr transcript
	tr.load([]json.RawMessage{
		json.RawMessage(`{"role":"user","content":"Message from Orb agent orb:ed25519:kmUVCF6YvJHHyEIy90yc0t6PqIN6qr_89q-1zybjRqU, conversation s1 (Refactor, MacBook):\n\nIs staging green?"}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"agents","arguments":{"action":"list"}},{"type":"toolCall","id":"c2","name":"agents","arguments":{"action":"send","agent":"ordalie-lab-3","text":"Check migration 042"}},{"type":"toolCall","id":"c3","name":"subagent","arguments":{"mode":"single","agent":"reviewer","task":"Verify locks"}}]}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"c1","content":[{"type":"text","text":"Agent p1 · ordalie-lab-3"}]}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"c2","content":[{"type":"text","text":"Queued: conversation s2 reads it after its current turn."}],"details":{"who":"ordalie-lab-3 · Deploy","peer":"orb:ed25519:rIMdmCCI2Y0bhXFOSyAs51-PknuBwj3wx9lzUId2SKI","session":"s2","status":"queued"}}`),
		json.RawMessage(`{"role":"toolResult","toolCallId":"c3","content":[{"type":"text","text":"Locks look safe."}],"details":{"steps":"read lock.go\nrun -race"}}`),
	}, 0)
	rows, _ := tr.rows()
	// Expect: incoming peer row, folded run row (agents list), outgoing peer row (agents send), local subagent peer row.
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != "peer" || rows[0].Exchange == nil || rows[0].Exchange.Dir != "in" || rows[0].Exchange.Who != "MacBook · Refactor" || rows[0].Exchange.Text != "Is staging green?" {
		t.Fatalf("row[0] = %+v", rows[0])
	}
	if rows[1].Kind != "run" {
		t.Fatalf("row[1] should keep agents list folded in run, got %+v", rows[1])
	}
	if rows[2].Kind != "peer" || rows[2].Exchange == nil || rows[2].Exchange.Dir != "out" || rows[2].Exchange.Scope != "peer" || rows[2].Exchange.Who != "ordalie-lab-3 · Deploy" || rows[2].Exchange.Status != "queued" {
		t.Fatalf("row[2] = %+v", rows[2])
	}
	if rows[3].Kind != "peer" || rows[3].Exchange == nil || rows[3].Exchange.Dir != "out" || rows[3].Exchange.Scope != "subagent" || rows[3].Exchange.Who != "reviewer" || rows[3].Exchange.Status != "2 steps" || rows[3].Exchange.Reply != "Locks look safe." {
		t.Fatalf("row[3] = %+v", rows[3])
	}
}

func TestRemoteUpdateWaitsForRestartAndRefreshesVersion(t *testing.T) {
	ctx := t.Context()
	var probes atomic.Int32
	c := newClient()
	a := &App{
		ctx:        ctx,
		asked:      map[*tab]bool{},
		refreshNow: make(chan struct{}, 1),
		machines:   []*machine{{id: "peer-1", host: "lab", version: "0.27.0", state: "connected", loadDone: true}},
		o: Options{
			Emit: func(m any) { c.out <- mitosis.JSON(m) },
			Call: func(_ context.Context, method string, params, result any) error {
				if method != "remote" {
					return json.Unmarshal([]byte(`{}`), result)
				}
				var req struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(mitosis.JSON(params), &req)
				switch req.Method {
				case "host.update":
					return json.Unmarshal([]byte(`{"from":"0.27.0","to":"0.28.0","status":"updated · Mitosis restarting"}`), result)
				case "host.sessions":
					if probes.Add(1) == 1 {
						return fmt.Errorf("peer restarting")
					}
					return json.Unmarshal([]byte(`{"host":"lab","version":"0.28.0","items":[]}`), result)
				}
				return json.Unmarshal([]byte(`{}`), result)
			},
		},
	}
	a.Do([]byte(`{"id":"u1","do":"update","machine":"peer-1"}`))
	c.until(t, "update reply", func() bool { return c.replies["u1"] != nil })
	var msg string
	if err := json.Unmarshal(c.replies["u1"], &msg); err != nil || msg != "updated to 0.28.0" {
		t.Fatalf("update reply = %s (err=%v), want %q", c.replies["u1"], err, "updated to 0.28.0")
	}
	a.mu.Lock()
	gotVersion := a.machine("peer-1").version
	a.mu.Unlock()
	if gotVersion != "0.28.0" {
		t.Fatalf("machine version after update = %q, want 0.28.0", gotVersion)
	}
}
