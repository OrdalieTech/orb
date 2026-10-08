// Package view is the client every Orb app draws. It follows the conversations open on Bridge,
// this machine's and its peers', folds their events into rows ready to draw, and does what the
// app asks; an app only renders and sends intents, so every app behaves the same.
//
// The wire is JSON. Intents come in as {"do": …} (with an "id" when they expect a reply). The
// app gets {"t":"state"} when what it draws outside conversations changes, {"t":"rows"} with the
// rows of a conversation from the first that changed, {"t":"home"} with the machines and their
// threads (larger, changing less often), {"t":"reply"} to an intent with an id, and {"t":"alert"}
// for what happened out of its sight.
package view

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/bridge"
)

// Options are what the host gives the view; the view itself touches no file, process or network.
type Options struct {
	// Call reaches this machine's Bridge owner API, the calls `orb bridge pipe` takes.
	Call func(ctx context.Context, method string, params, result any) error
	// Run runs this machine's orb CLI and returns what it printed.
	Run func(ctx context.Context, args ...string) (string, error)
	// Tabs is what SaveTabs last kept, so the tabs come back when the app starts again.
	Tabs     []byte
	SaveTabs func([]byte)
	// Latest is the newest Orb release, "" when unknown.
	Latest func(ctx context.Context) string
	// Name is what this machine is called in the app ("this phone"); CWD is where a new
	// conversation here starts.
	Name, CWD string
	// Emit sends one message to the app; it is called with the view's lock held, never concurrently.
	Emit func(any)
}

// State is everything an app draws outside conversations.
type State struct {
	Self       string      `json:"self"`
	Up         bool        `json:"up"`
	Tabs       []Tab       `json:"tabs"`
	Claim      *Claim      `json:"claim,omitempty"`
	Invitation *Invitation `json:"invitation,omitempty"`
	Joining    string      `json:"joining,omitempty"`   // claiming · waiting (for the inviter's yes) · paired
	Launching  string      `json:"launching,omitempty"` // starting Orb on a machine, or why that failed
	Login      *Login      `json:"login,omitempty"`
	Acting     bool        `json:"acting,omitempty"` // a peer's prompt is running on this machine
	Summary    string      `json:"summary"`          // one line for a status bar or notification
	Latest     string      `json:"latest,omitempty"`
	Commands   []Command   `json:"commands"` // the app's own slash commands, before each Orb's
}

// builtins are the app's commands, named like the TUI's.
var builtins = []Command{
	{Name: "new", Hint: "fresh session · optional first message"}, {Name: "compact", Hint: "summarize to free context · optional focus"},
	{Name: "name", Hint: "name this session"}, {Name: "model", Hint: "model and reasoning", Now: true}, {Name: "copy", Hint: "copy the last answer", Now: true},
	{Name: "sessions", Hint: "all sessions", Now: true}, {Name: "pair", Hint: "Bridge: scan or share a code", Now: true},
	{Name: "login", Hint: "providers and accounts", Now: true}, {Name: "plugins", Hint: "turn plugins on and off", Now: true},
}

type App struct {
	o          Options
	ctx        context.Context
	mu         sync.Mutex
	self       string
	up         bool
	machines   []*machine
	tabs       []*tab
	visible    bool
	budget     int
	claim      *Claim
	invitation *Invitation
	joining    string
	launching  string
	login      *signIn
	latest     string
	sent, home []byte // the last state and home the app received
	saved      []byte // the tabs last kept
	marks      map[*tab][2]bool
	threadsAt  time.Time // when every machine's threads were read
	refreshNow chan struct{}
}

// New starts the view: it brings back the saved tabs and keeps machines and threads current.
func New(ctx context.Context, o Options) *App {
	a := &App{o: o, ctx: ctx, budget: 4, marks: map[*tab][2]bool{}, refreshNow: make(chan struct{}, 1)}
	var saved []struct{ ID, Peer, Session, Title, Where string }
	_ = json.Unmarshal(o.Tabs, &saved)
	a.mu.Lock()
	for _, s := range saved {
		t := a.newTab(s.Peer, "", s.Where, s.Session)
		t.ID, t.Title = cmp.Or(s.ID, t.ID), s.Title
	}
	a.mu.Unlock()
	go a.keepMachines()
	go func() {
		for o.Latest != nil {
			if latest := o.Latest(ctx); latest != "" {
				a.mu.Lock()
				a.latest = latest
				a.flush()
				a.mu.Unlock()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(6 * time.Hour):
			}
		}
	}()
	return a
}

func (a *App) remote(ctx context.Context, peer, method string, params, result any) error {
	return a.o.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": method, "params": params}, result)
}

// intent is one thing an app asks; each "do" reads only the fields it needs.
type intent struct {
	ID       string   `json:"id"`
	Do       string   `json:"do"`
	Tab      string   `json:"tab"`
	Key      string   `json:"key"`
	Machine  string   `json:"machine"`
	CWD      string   `json:"cwd"`
	Text     string   `json:"text"`
	Value    *string  `json:"value"`
	Name     string   `json:"name"`
	Ref      string   `json:"ref"`
	Px       int      `json:"px"`
	On       bool     `json:"on"`
	Tabs     []string `json:"tabs"`
	Budget   int      `json:"budget"`
	Provider string   `json:"provider"`
	Account  string   `json:"account"`
	Auth     string   `json:"auth"`
}

// Do runs one intent. Those that wait on a machine run on their own; the rest apply at once and
// in order, so two messages sent one after the other arrive in that order.
func (a *App) Do(line []byte) {
	var in intent
	if json.Unmarshal(line, &in) != nil {
		return
	}
	reply := func(result any, err error) {
		if in.ID == "" {
			return
		}
		m := map[string]any{"t": "reply", "id": in.ID}
		if err != nil {
			m["error"] = err.Error()
		} else if result != nil {
			m["result"] = result
		}
		a.mu.Lock()
		a.o.Emit(m)
		a.mu.Unlock()
	}
	if slow := a.slow(in); slow != nil {
		go func() { reply(slow()) }()
		return
	}
	a.mu.Lock()
	result, err := a.now(in)
	a.flush()
	a.mu.Unlock()
	reply(result, err)
}

var errNoTab = errors.New("no such tab")

func (a *App) tab(id string) *tab {
	for _, t := range a.tabs {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// now runs an intent that only changes what the view holds, with the lock held.
func (a *App) now(in intent) (any, error) {
	t := a.tab(in.Tab)
	switch in.Do {
	case "hello":
		// A fresh app has nothing drawn: everything is sent again.
		a.budget, a.sent, a.home = cmp.Or(in.Budget, a.budget), nil, nil
		for _, t := range a.tabs {
			t.rows, t.drawn = nil, nil
		}
		return nil, nil
	case "visible":
		a.visible = in.On
		a.balance()
		a.wakeMachines()
		return nil, nil
	case "show":
		// The tabs on screen follow at full pace; the ones seen last keep up off screen.
		for _, t := range a.tabs {
			if shown := slices.Contains(in.Tabs, t.ID); shown != t.watched {
				t.watched = shown
				if shown {
					t.seen = time.Now()
					t.wakeUp()
				}
			}
		}
		a.balance()
		return nil, nil
	case "close":
		if t != nil {
			a.tabs = slices.DeleteFunc(a.tabs, func(x *tab) bool { return x == t })
			delete(a.marks, t)
			close(t.done)
		}
		return nil, nil
	case "login.answer":
		a.login.answer(in.Text)
		return nil, nil
	case "login.cancel":
		a.login.cancel()
		a.login = nil
		return nil, nil
	case "join.cancel":
		a.joining = ""
		return nil, nil
	}
	if t == nil {
		return nil, errNoTab
	}
	switch in.Do {
	case "send":
		return a.send(t, in.Text)
	case "steer":
		t.tr.sent = append(t.tr.sent, in.Text)
		t.tr.waiting(true)
		args := t.execution(map[string]any{"text": t.invoking(in.Text)})
		go t.call("steer", args)
	case "abort":
		args := t.execution(map[string]any{})
		go t.call("cancel", args)
	case "answer":
		t.answer(in.Value)
	case "model":
		t.useModel(in.Name)
	case "thinking":
		t.useThinking(in.Name)
	case "rename":
		t.rename(in.Name)
	case "earlier":
		t.earlier()
	case "detail":
		return t.tr.detail(in.Key), nil
	default:
		return nil, fmt.Errorf("unknown intent %q", in.Do)
	}
	return nil, nil
}

// send runs a slash command or `!command` the app knows, as the TUI does, or sends the text to
// the Orb. What only the app can do (open a sheet, copy) comes back as {nav} or {copy}.
func (a *App) send(t *tab, text string) (any, error) {
	if command, ok := strings.CutPrefix(text, "!"); ok && strings.TrimSpace(command) != "" {
		t.tr.shell(strings.TrimSpace(command))
		go t.call("shell", map[string]string{"command": strings.TrimSpace(command)})
		return nil, nil
	}
	name, arg, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(text, "/") {
		switch name {
		case "new":
			if arg != "" {
				t.queued, t.queuedFrom = arg, t.session
			}
			go t.call("session.new", map[string]any{})
			return nil, nil
		case "compact":
			args := map[string]string{}
			if arg != "" {
				args["instructions"] = arg
			}
			go t.call("session.compact", args)
			return nil, nil
		case "name":
			if arg != "" {
				t.rename(arg)
				return nil, nil
			}
		case "copy":
			return map[string]string{"copy": t.tr.lastText()}, nil
		}
		if nav := navigation(name); nav != "" {
			return map[string]string{"nav": nav}, nil
		}
	}
	t.prompt(text)
	return nil, nil
}

// navigation is the screen a command opens, "" for one the Orb runs.
func navigation(command string) string {
	switch command {
	case "model":
		return "model"
	case "sessions", "resume":
		return "home"
	case "bridge", "pair":
		return "pair"
	case "login", "providers", "accounts":
		return "providers"
	case "plugins":
		return "plugins"
	}
	return ""
}

// balance keeps the conversations seen last streaming off screen while the app shows, as many as
// the app said it can hold, so a swipe finds them current.
func (a *App) balance() {
	order := slices.Clone(a.tabs)
	slices.SortStableFunc(order, func(x, y *tab) int { return y.seen.Compare(x.seen) })
	for i, t := range order {
		if follows := a.visible && i < a.budget; follows != t.follows {
			t.follows = follows
			t.wakeUp()
		}
	}
}

// flush sends the app what changed: its state, the rows of the conversations it follows, and
// alerts for what happened out of its sight. Called with the lock held.
func (a *App) flush() {
	machines, entries := a.machinesState()
	if b := bridge.JSON(map[string]any{"t": "home", "machines": machines, "entries": entries}); !bytes.Equal(b, a.home) {
		a.home = b
		a.o.Emit(json.RawMessage(b))
	}
	if b := bridge.JSON(struct {
		T string `json:"t"`
		State
	}{"state", a.state()}); !bytes.Equal(b, a.sent) {
		a.sent = b
		a.o.Emit(json.RawMessage(b))
	}
	for _, t := range a.tabs {
		if t.watched || t.follows {
			t.render()
		}
	}
	a.alert()
	a.save()
}

func (a *App) state() State {
	s := State{Self: a.self, Up: a.up, Claim: a.claim, Invitation: a.invitation, Joining: a.joining, Launching: a.launching, Latest: a.latest, Commands: builtins}
	if a.login != nil {
		l := a.login.state
		s.Login = &l
	}
	var asking, working *tab
	for _, t := range a.tabs {
		n := len(t.tr.items)
		t.Streaming = n > 0 && t.tr.items[n-1].kind == "s" && t.tr.items[n-1].live
		t.Remote = t.Peer != a.self
		s.Tabs = append(s.Tabs, t.Tab)
		if t.Ask != nil && asking == nil {
			asking = t
		}
		if t.Busy && working == nil {
			working = t
		}
		if you := lastYou(&t.tr); !t.Remote && t.Busy && you != nil && you.via != "" {
			s.Acting = true
		}
	}
	peers := 0
	for _, m := range a.machines {
		if m.id != a.self && m.state == "connected" {
			peers++
		}
	}
	switch {
	case asking != nil:
		s.Summary = "needs you · " + clip(asking.Ask.Title, 48)
	case s.Acting:
		s.Summary = "a peer is acting here"
	case working != nil:
		s.Summary = "working · " + working.Title
	default:
		s.Summary = fmt.Sprintf("ready · %d peer%s", peers, map[bool]string{true: "", false: "s"}[peers == 1])
	}
	return s
}

func lastYou(t *transcript) *item {
	for i := len(t.items) - 1; i >= 0; i-- {
		if t.items[i].kind == "y" {
			return t.items[i]
		}
	}
	return nil
}

// alert tells the app once when a conversation it does not show finishes a turn or asks something.
func (a *App) alert() {
	for _, t := range a.tabs {
		now := [2]bool{t.Busy, t.Ask != nil}
		was, known := a.marks[t]
		a.marks[t] = now
		if !known || was == now {
			continue
		}
		if was[0] && !now[0] {
			a.threadsAt = time.Time{} // the turn changed a thread: Home reads them again
		}
		if a.visible && t.watched {
			continue
		}
		text := ""
		switch {
		case !was[1] && now[1]:
			text = "Needs you · " + firstLine(t.Ask.Title, 160)
		case was[0] && !now[0]:
			text = cmp.Or(firstLine(strings.TrimSpace(t.tr.lastText()), 160), "Finished")
		default:
			continue
		}
		a.o.Emit(map[string]string{"t": "alert", "tab": t.ID, "title": cmp.Or(t.Title, "Orb") + " · " + t.Where, "text": text})
	}
}

// save keeps the tabs to bring back; a conversation that loaded empty is not stored yet, so there
// would be nothing to reopen.
func (a *App) save() {
	if a.o.SaveTabs == nil {
		return
	}
	type saved struct{ ID, Peer, Session, Title, Where string }
	kept := []saved{}
	for _, t := range a.tabs {
		if t.session != "" && (!t.Loaded || len(t.tr.items) > 0) {
			kept = append(kept, saved{t.ID, t.Peer, t.session, t.Title, t.Where})
		}
	}
	if b := bridge.JSON(kept); !bytes.Equal(b, a.saved) {
		a.saved = b
		a.o.SaveTabs(b)
	}
}
