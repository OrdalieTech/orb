package view

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/agent/session/exporthtml"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
)

// Tab is a conversation an app has open, as it draws its header, strip and prompt box; its
// conversation comes as rows.
type Tab struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Peer      string    `json:"peer"`
	Where     string    `json:"where"` // the machine's name
	Remote    bool      `json:"remote,omitempty"`
	CWD       string    `json:"cwd,omitempty"`
	Busy      bool      `json:"busy,omitempty"`
	Online    bool      `json:"online"`
	Loaded    bool      `json:"loaded,omitempty"` // an empty conversation is ready, not still coming
	Earlier   bool      `json:"earlier,omitempty"`
	Streaming bool      `json:"streaming,omitempty"`
	Status    string    `json:"status,omitempty"`
	Model     string    `json:"model,omitempty"` // provider/id
	Thinking  string    `json:"thinking,omitempty"`
	Levels    []string  `json:"levels,omitempty"`  // reasoning levels the model takes, lowest first
	Context   float64   `json:"context,omitempty"` // share of the context window in use
	Cost      float64   `json:"cost,omitempty"`
	Usage     *Usage    `json:"usage,omitempty"`
	Ask       *Ask      `json:"ask,omitempty"`
	Unread    int       `json:"unread,omitempty"` // answers that finished out of sight since it was last shown
	Commands  []Command `json:"commands,omitempty"`
	Models    []string  `json:"models,omitempty"`
}

// Usage is the plan limits of a conversation's provider account, as its Orb last read them;
// windows that reset since are left out.
type Usage struct {
	Plan    string   `json:"plan,omitempty"`
	At      int64    `json:"at"` // milliseconds
	Windows []Window `json:"windows"`
}

type Window struct {
	Name   string  `json:"name"`
	Left   float64 `json:"left"`   // percent
	Resets int64   `json:"resets"` // milliseconds; 0 when unknown
}

// Ask is a plugin stopping the world until someone answers: an approval, or one question of a
// series; Free takes any text.
type Ask struct {
	ID      string   `json:"id"`
	Owner   string   `json:"owner"`
	Title   string   `json:"title"`
	Message string   `json:"message,omitempty"`
	Choices []string `json:"choices,omitempty"`
	Free    bool     `json:"free,omitempty"`
}

// Command is one entry of the slash palette; Now runs on tap, the rest take an argument.
type Command struct {
	Name string `json:"name"`
	Hint string `json:"hint,omitempty"`
	Now  bool   `json:"now,omitempty"`
}

// descriptor is what `instances.describe` says of an Orb.
type descriptor struct {
	Name, CWD, Model, Provider, Status, Catalog, State string
	Thinking                                           *string
	Waits                                              bool
	Generation                                         string `json:"registration_generation"`
	Target                                             struct {
		Session   string `json:"session_id"`
		Revision  string `json:"session_revision"`
		Execution string `json:"execution_id"`
	}
	Models []struct {
		ID, Provider, Name string
		Thinking           []string
	}
	Commands []struct{ Name, Description string }
	Stats    *struct {
		Cost         float64
		ContextUsage *struct{ Percent float64 } `json:"contextUsage"`
	}
	Usage *struct {
		Plan    string
		Windows []struct {
			Name      string
			Remaining float64
			ResetsAt  time.Time `json:"resets_at"`
		}
		CheckedAt time.Time `json:"checked_at"`
	}
	Input *struct {
		ID, Title    string
		Choices      []string
		Presentation *struct {
			Kind string
			Data struct{ Questions []question }
		}
	}
}

type question struct {
	ID, Question, Header string
	Options              []struct{ Label string }
}

// tab follows a conversation in an Orb on Bridge, this machine's or a peer's, with the calls
// `orb bridge view` makes and long polls. Its fields are guarded by the app's lock.
type tab struct {
	a                      *App
	Tab                    // what the app draws
	instance               string
	session                string // the thread, kept across the Orbs that serve it
	info                   descriptor
	cursor, pulse          string
	from                   int // the first message shown, once known: reloads keep the window, so rows keep their keys
	stale, gone, reopening bool
	ended                  bool // a turn ended since the last flush, as the stream said
	watched, follows       bool
	seen                   time.Time
	asked                  string
	questions              []question
	answers                []any
	queued, queuedFrom     string // a message waiting for the Orb (a new or reopened thread) to hold another session
	nextTry                time.Time
	tr                     transcript
	rows                   []json.RawMessage // what the app last received
	drawn                  []*item           // the items those rows showed
	wake                   chan struct{}
	done                   chan struct{}
	cancelPoll             context.CancelFunc
}

// tail is how many messages a conversation opens with; earlier ones load on demand.
const tail = 80

func (a *App) newTab(peer, instance, where, session string) *tab {
	t := &tab{a: a, Tab: Tab{ID: protocol.NewID(), Peer: peer, Where: where, Online: true}, instance: instance, session: session, from: -1, stale: true, wake: make(chan struct{}, 1), done: make(chan struct{})}
	a.tabs = append(a.tabs, t)
	go t.follow()
	return t
}

func (t *tab) wakeUp() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// follow follows the Orb until the tab closes; no failure ends it, the next round tries again.
func (t *tab) follow() {
	for {
		wait := t.step()
		select {
		case <-t.a.ctx.Done():
			return
		case <-t.done:
			return
		case <-t.wake:
		case <-time.After(wait):
		}
	}
}

func (t *tab) step() time.Duration {
	a := t.a
	a.mu.Lock()
	reopen := (t.instance == "" || t.gone) && t.watched && time.Now().After(t.nextTry)
	idle := t.instance == "" || t.gone
	live, waits := t.watched || t.follows, t.info.Waits
	describe := t.stale || !waits || !live
	a.mu.Unlock()
	// A tab whose Orb is not running (restored, or ended there) starts one once shown.
	if idle {
		if reopen {
			t.reopen("")
		}
		return time.Second
	}
	if describe {
		if !t.describe() {
			if t.gone {
				return 5 * time.Second
			}
			return 2 * time.Second
		}
		a.mu.Lock()
		t.stale = false
		a.mu.Unlock()
	}
	if !live {
		a.mu.Lock()
		t.cursor = "" // the transcript is fetched again when a screen shows it
		a.mu.Unlock()
		return 5 * time.Second
	}
	a.mu.Lock()
	cursor := t.cursor
	a.mu.Unlock()
	if cursor == "" {
		t.snapshot()
	} else {
		t.events(waits)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	// Off screen, a turn's events gather between polls instead of waking the app per token.
	case waits && !t.watched && t.Busy:
		return 1500 * time.Millisecond
	case waits:
		return 0
	case t.Busy:
		return 350 * time.Millisecond
	}
	return 900 * time.Millisecond
}

func (t *tab) remote(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(t.a.ctx, 30*time.Second)
	defer cancel()
	return t.a.remote(ctx, t.Peer, method, params, result)
}

func (t *tab) describe() bool {
	a := t.a
	a.mu.Lock()
	params := map[string]string{"instance_id": t.instance}
	// Its models and commands come only when they changed since the digest this side holds; an Orb
	// that never gave one (before 0.19) takes no such parameter.
	if t.info.Catalog != "" {
		params["catalog"] = t.info.Catalog
	}
	a.mu.Unlock()
	var d descriptor
	err := t.remote("instances.describe", params, &d)
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.flush()
	if err != nil {
		t.Online = false
		// Unreachable is a network matter; not found means the Orb itself ended over there.
		t.gone = bridge.Code(err) == "not_found" && t.info.Target.Session != ""
		t.Status = map[bool]string{true: "ended on that device · send a message to reopen it", false: "offline · read-only · reconnecting"}[t.gone]
		return false
	}
	if d.Target.Session != t.info.Target.Session {
		t.cursor, t.from, t.Loaded = "", -1, false
		t.tr.clear()
	}
	if d.Catalog == t.info.Catalog && d.Models == nil {
		d.Models, d.Commands = t.info.Models, t.info.Commands
	}
	t.info, t.Online, t.gone = d, true, false
	// The next long poll waits for a change since what this describes, not since the last page:
	// a turn begun and ended in between would otherwise leave the tab working.
	t.pulse = cmp.Or(d.State, t.pulse)
	if strings.HasPrefix(t.Status, "offline") || strings.HasPrefix(t.Status, "ended") || t.Status == "reconnecting" {
		t.Status = ""
	}
	t.session, t.CWD = d.Target.Session, d.CWD
	t.Title = d.Name
	if t.Title == "" {
		t.Title = firstUserLine(&t.tr)
	}
	if t.Title == "" {
		t.Title = path.Base(d.CWD)
	}
	// The Orb names its model by display name and provider (an older one, the name alone); its
	// catalog maps that to an id and the levels it takes.
	t.Model, t.Levels, t.Models = d.Model, nil, nil
	known := false
	for _, m := range d.Models {
		t.Models = append(t.Models, m.Provider+"/"+m.ID)
		if !known && m.Name == d.Model && (d.Provider == "" || m.Provider == d.Provider) {
			t.Model, t.Levels, known = m.Provider+"/"+m.ID, m.Thinking, true
		}
	}
	if d.Thinking != nil {
		t.Thinking = *d.Thinking // an Orb before 0.15 keeps it to itself: the last choice stands
	}
	t.Busy = d.Target.Execution != "" || strings.Contains(strings.ToLower(d.Status), "stream")
	t.Cost, t.Context = 0, 0
	if d.Stats != nil {
		t.Cost = d.Stats.Cost
		if d.Stats.ContextUsage != nil {
			t.Context = d.Stats.ContextUsage.Percent / 100
		}
	}
	t.Usage = nil
	if u := d.Usage; u != nil {
		t.Usage = &Usage{Plan: u.Plan, At: u.CheckedAt.UnixMilli(), Windows: []Window{}}
		for _, w := range u.Windows {
			if w.ResetsAt.IsZero() || w.ResetsAt.After(time.Now()) {
				t.Usage.Windows = append(t.Usage.Windows, Window{w.Name, w.Remaining, max(0, w.ResetsAt.UnixMilli())})
			}
		}
	}
	t.Commands = nil
	for _, c := range d.Commands {
		t.Commands = append(t.Commands, Command{Name: c.Name, Hint: c.Description})
	}
	switch in := d.Input; {
	case in == nil:
		t.asked, t.questions, t.Ask = "", nil, nil
	case in.ID != t.asked:
		t.asked, t.questions, t.answers = in.ID, nil, nil
		if p := in.Presentation; p != nil && p.Kind == "questions" && len(p.Data.Questions) > 0 {
			t.questions = p.Data.Questions
			t.Ask = t.question()
		} else {
			owner := "questions"
			if slices.ContainsFunc(in.Choices, func(c string) bool { return strings.Contains(c, "approve") }) {
				owner = "permissions"
			}
			t.Ask = &Ask{ID: in.ID, Owner: owner, Title: in.Title, Choices: in.Choices, Free: len(in.Choices) == 0}
		}
	}
	if t.queued != "" && t.session != "" && t.session != t.queuedFrom {
		text := t.queued
		t.queued = ""
		go t.call("prompt", map[string]string{"text": t.invoking(text)})
	}
	return true
}

func firstUserLine(t *transcript) string {
	for _, it := range t.items {
		if it.kind == "y" {
			return firstLine(it.text, 48)
		}
	}
	return ""
}

// question is the next of a questions plugin's series: the app walks them and the plugin
// validates the one JSON result (plugins/questions).
func (t *tab) question() *Ask {
	q := t.questions[len(t.answers)]
	ask := &Ask{ID: t.asked, Owner: "questions", Title: q.Question, Message: q.Header, Free: true}
	if len(t.questions) > 1 {
		ask.Message += fmt.Sprintf("  %d / %d", len(t.answers)+1, len(t.questions))
	}
	for _, o := range q.Options {
		ask.Choices = append(ask.Choices, o.Label)
	}
	return ask
}

// snapshot reads the conversation from the first message shown — its last [tail] when it opens,
// all of it on an Orb that cannot start there — then swaps it in at once: never half loaded.
func (t *tab) snapshot() {
	a := t.a
	a.mu.Lock()
	instance, asked, waits := t.instance, t.from, t.info.Waits
	a.mu.Unlock()
	offset := ""
	if asked > 0 {
		offset = fmt.Sprint(asked)
	}
	var snap, cursor string
	var messages []json.RawMessage
	var partial json.RawMessage
	from := -1
	defer func() {
		if snap != "" {
			_ = t.remote("events.unsubscribe", map[string]string{"instance_id": instance, "snapshot_id": snap}, nil)
		}
	}()
	for range 64 {
		params := map[string]any{"instance_id": instance, "snapshot_id": snap, "offset": offset}
		if snap == "" && asked < 0 && waits {
			params["tail"] = tail
		}
		var r struct {
			SnapshotID string            `json:"snapshot_id"`
			Messages   []json.RawMessage `json:"messages"`
			Partial    json.RawMessage   `json:"partial"`
			Cursor     string            `json:"cursor"`
			Offset     string            `json:"offset"`
			From       int               `json:"from"`
		}
		if err := t.remote("events.subscribe", params, &r); err != nil {
			a.mu.Lock()
			t.from = -1 // a compacted conversation may be shorter now
			a.mu.Unlock()
			return
		}
		if snap == "" {
			from = r.From
		}
		messages, snap, offset, partial, cursor = append(messages, r.Messages...), r.SnapshotID, r.Offset, r.Partial, r.Cursor
		if offset == "" {
			break
		}
	}
	if offset != "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.flush()
	t.from, t.Earlier = from, from > 0 && (asked < 0 || from <= asked)
	t.tr.clear()
	t.tr.load(messages, from)
	if partial != nil {
		t.tr.apply(json.RawMessage(`{"type":"message_update","message":` + string(partial) + `}`))
	}
	t.cursor, t.Loaded = cursor, true
}

func (t *tab) events(waits bool) {
	a := t.a
	a.mu.Lock()
	params := map[string]any{"instance_id": t.instance, "cursor": t.cursor}
	if waits {
		params["wait"], params["state"] = true, t.pulse
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	t.cancelPoll = cancel
	a.mu.Unlock()
	defer cancel()
	var r struct {
		Events []struct{ Data json.RawMessage } `json:"events"`
		Cursor string                           `json:"cursor"`
		State  string                           `json:"state"`
	}
	err := a.remote(ctx, t.Peer, "events.subscribe", params, &r)
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.flush()
	if err != nil {
		t.cursor = ""
		return
	}
	for _, e := range r.Events {
		var info struct{ Type, Name string }
		if json.Unmarshal(e.Data, &info) == nil && info.Type == "session_info_changed" && info.Name != "" {
			t.Title = info.Name
		}
		t.ended = t.ended || info.Type == "agent_end"
		t.tr.apply(e.Data)
	}
	t.cursor = cmp.Or(r.Cursor, t.cursor)
	if r.State != t.pulse {
		t.pulse, t.stale = r.State, true
	}
}

// call runs a method on the Orb's session; its error, if any, becomes the tab's status.
func (t *tab) call(method string, args any) {
	a := t.a
	a.mu.Lock()
	if t.info.Target.Session == "" {
		a.mu.Unlock()
		return
	}
	call := bridge.Call{InstanceID: t.instance, Service: protocol.Service, Method: method, SessionID: t.info.Target.Session, OperationID: protocol.NewID(),
		Expected: bridge.Expected{Generation: t.info.Generation, Revision: t.info.Target.Revision}, Args: bridge.JSON(args)}
	a.mu.Unlock()
	if err := t.remote("instances.call", call, nil); err != nil {
		a.mu.Lock()
		t.Status = err.Error()
		a.flush()
		a.mu.Unlock()
	}
}

// read is a read-only call on the Orb, answered at once.
func (t *tab) read(method string, args, result any) error {
	t.a.mu.Lock()
	instance := t.instance
	t.a.mu.Unlock()
	return t.remote("instances.call", bridge.Call{InstanceID: instance, Service: protocol.Service, Method: method, Args: bridge.JSON(args)}, result)
}

func (t *tab) execution(args map[string]any) map[string]any {
	args["execution_id"] = t.info.Target.Execution
	return args
}

// invoking puts a skill invoked anywhere in a message before it, as the TUI does.
func (t *tab) invoking(text string) string {
	return exporthtml.SkillSubmission(text, func(name string) bool {
		return slices.ContainsFunc(t.Commands, func(c Command) bool { return c.Name == "skill:"+name })
	})
}

// prompt sends a message; while a turn runs it queues after it. Called with the lock held.
func (t *tab) prompt(text string) {
	t.tr.sent = append(t.tr.sent, text)
	switch {
	case t.gone || t.instance == "":
		go t.reopen(text)
	case t.info.Target.Session == "":
		t.queued, t.queuedFrom = text, ""
	case t.Busy:
		t.tr.waiting(false)
		args := t.execution(map[string]any{"text": t.invoking(text)})
		go t.call("follow_up", args)
	default:
		go t.call("prompt", map[string]string{"text": t.invoking(text)})
	}
}

// reopen starts Orb on the thread again over there, or joins the one that has it open.
func (t *tab) reopen(text string) {
	a := t.a
	a.mu.Lock()
	if text != "" {
		t.queued, t.queuedFrom = text, ""
	}
	if t.reopening {
		a.mu.Unlock()
		return
	}
	t.reopening, t.Status = true, "opening the thread…"
	session := t.session
	a.flush()
	a.mu.Unlock()
	instance, err := a.launch(t.Peer, "", session)
	a.mu.Lock()
	defer a.mu.Unlock()
	t.reopening, t.Status = false, ""
	if err != nil {
		t.Status, t.nextTry = err.Error(), time.Now().Add(30*time.Second)
	} else {
		t.instance, t.gone, t.cursor, t.stale = instance, false, "", true
		t.wakeUp()
	}
	a.flush()
}

// answer replies to the open interrupt; nil dismisses it.
func (t *tab) answer(value *string) {
	ask := t.Ask
	if ask == nil {
		return
	}
	t.Ask = nil
	reply := ""
	switch {
	case t.questions == nil && value != nil:
		reply = *value
	case t.questions != nil && value == nil:
		reply = `{"cancelled":true}`
	case t.questions != nil:
		q := t.questions[len(t.answers)]
		answer := map[string]any{"id": q.ID, "selected": []string{}}
		if slices.ContainsFunc(q.Options, func(o struct{ Label string }) bool { return o.Label == *value }) {
			answer["selected"] = []string{*value}
		} else {
			answer["custom"] = *value
		}
		if t.answers = append(t.answers, answer); len(t.answers) < len(t.questions) {
			t.Ask = t.question()
			return
		}
		reply = string(bridge.JSON(map[string]any{"answers": t.answers}))
	}
	args := t.execution(map[string]any{"id": ask.ID, "value": reply})
	go t.call("input.reply", args)
}

// useModel switches the model; the Orb keeps the choice as its default, so the threads that
// follow there start with it.
func (t *tab) useModel(id string) {
	provider, model, _ := strings.Cut(id, "/")
	args := map[string]any{"provider": provider, "model": model}
	if t.Thinking != "" {
		args["thinking"] = t.Thinking
	}
	go t.call("session.model", args)
}

func (t *tab) useThinking(level string) {
	t.Thinking = level
	if strings.Contains(t.Model, "/") {
		t.useModel(t.Model)
	}
}

// rename names the conversation; an Orb takes calls once it has described its session, so a
// rename right after a launch waits for that.
func (t *tab) rename(name string) {
	t.Title = name
	go func() {
		for range 40 {
			t.a.mu.Lock()
			ready := t.info.Target.Session != ""
			t.a.mu.Unlock()
			if ready {
				t.call("session.name", map[string]string{"name": name})
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

// earlier shows a hundred more of the older messages.
func (t *tab) earlier() {
	t.from, t.cursor = max(0, t.from-100), ""
	if t.cancelPoll != nil {
		t.cancelPoll()
	}
	t.wakeUp()
}

// render sends the app what changed in the conversation: the rows from the first that differs.
// Rows showing only items unchanged since the last render keep their encoding, so a streamed
// token costs the rows of its message, not the conversation's. Called with the lock held, for
// tabs a screen shows or that follow off screen.
func (t *tab) render() {
	items := t.tr.items
	from := 0
	for from < len(items) && from < len(t.drawn) && items[from] == t.drawn[from] && items[from].face() == items[from].drawn {
		from++
	}
	rows, last := t.tr.rows()
	k := 0
	for k < len(rows) && k < len(t.rows) && last[k] < from {
		k++
	}
	tail := make([]json.RawMessage, 0, len(rows)-k)
	for _, r := range rows[k:] {
		tail = append(tail, bridge.JSON(r))
	}
	at := 0
	for at < len(tail) && k+at < len(t.rows) && string(tail[at]) == string(t.rows[k+at]) {
		at++
	}
	unchanged := at == len(tail) && k+at == len(t.rows)
	t.rows = append(t.rows[:k], tail...)
	for _, it := range items[from:] {
		it.drawn = it.face()
	}
	t.drawn = append(t.drawn[:0], items...)
	if !unchanged {
		t.a.o.Emit(map[string]any{"t": "rows", "tab": t.ID, "at": k + at, "rows": tail[at:]})
	}
}
