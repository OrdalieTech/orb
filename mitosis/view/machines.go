package view

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// Machine is a device on Mitosis, this one first, as Home, the pickers and the Mitosis screen show it.
type Machine struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Self      bool      `json:"self,omitempty"`
	Connected bool      `json:"connected,omitempty"`
	Version   string    `json:"version,omitempty"`
	Launch    bool      `json:"launch,omitempty"` // it lets this app start Orb there
	Hue       int       `json:"hue,omitempty"`    // 1 to 6 for a peer, its own while hues last; 0 for this machine
	Running   []Running `json:"running,omitempty"`
	Folders   []Folder  `json:"folders,omitempty"`
}

// Running is a conversation an Orb runs on a machine, labelled for a picker; Instance is its
// session, the address a picker opens.
type Running struct {
	Instance string `json:"instance"`
	Label    string `json:"label"`
	Busy     bool   `json:"busy,omitempty"`
	Open     bool   `json:"open,omitempty"` // a conversation follows it: a picker leaves it to its tab
}

// Folder is where some of a machine's threads live, most recently worked in first.
type Folder struct {
	CWD      string `json:"cwd"`
	Threads  int    `json:"threads"`
	Modified int64  `json:"modified"`
	Live     bool   `json:"live,omitempty"`
}

// Entry is a row of Home: a thread, or an Orb whose thread is not stored yet, on some machine.
// Home lists them newest first, a live one as of now.
type Entry struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Machine   string `json:"machine"`
	CWD       string `json:"cwd,omitempty"`
	Modified  int64  `json:"modified"`
	Live      bool   `json:"live,omitempty"`
	Asks      bool   `json:"asks,omitempty"`
	Open      bool   `json:"open,omitempty"`     // a tab follows it
	Tab       string `json:"tab,omitempty"`      // that tab's id
	Unstored  bool   `json:"unstored,omitempty"` // its Orb has not stored the thread yet
	Deletable bool   `json:"deletable,omitempty"`
}

type machine struct {
	id, host, version, state string
	instances                []instance // the conversations its Orbs run
	threads                  []thread
	launch, fetching, read   bool // read: its running Orbs were read at least once
}

type instance struct {
	title, cwd, session string
	busy, asks          bool
}

type thread struct {
	id, title, cwd string
	modified       int64
}

func (a *App) machine(id string) *machine {
	for _, m := range a.machines {
		if m.id == id {
			return m
		}
	}
	return nil
}

// name is what a machine calls itself (host.sessions): peers have no names on the wire.
func (a *App) name(m *machine) string {
	if m.id == a.self {
		return a.o.Name
	}
	if n := a.names[m.id]; n != "" {
		return n
	}
	if m.host != "" && m.host != "localhost" {
		return m.host
	}
	return clip(m.id[strings.LastIndex(m.id, ":")+1:], 6)
}

// nameMachine names a peer on this machine, over what it calls itself; "" gives it back its own.
// Called with the lock held.
func (a *App) nameMachine(id, name string) {
	if a.names == nil {
		a.names = map[string]string{}
	}
	if name == "" {
		delete(a.names, id)
	} else {
		a.names[id] = name
	}
	if a.o.SaveNames != nil {
		a.o.SaveNames(mitosis.JSON(a.names))
	}
}

func (a *App) wakeMachines() {
	select {
	case a.refreshNow <- struct{}{}:
	default:
	}
}

// keepMachines reads the machines on Mitosis: often while the app shows, rarely when it does not.
func (a *App) keepMachines() {
	for first := true; ; first = false {
		a.refresh(first)
		a.mu.Lock()
		wait := 15 * time.Second
		if a.visible {
			wait = 2500 * time.Millisecond
			if len(a.machines) < 2 {
				wait = 4 * time.Second
			}
		}
		a.mu.Unlock()
		select {
		case <-a.ctx.Done():
			return
		case <-a.refreshNow:
		case <-time.After(wait):
		}
	}
}

// refresh publishes the machines at once; their Orbs and threads follow as each answers, so a
// slow or unreachable one never holds the others up. Only a shown app reads them, and the first
// time: an app starting has Home ready as it shows.
func (a *App) refresh(first bool) {
	var s struct {
		PeerID  string               `json:"peer_id"`
		Peers   []string             `json:"peers"`
		States  map[string]string    `json:"peer_states"`
		Pending []mitosis.Invitation `json:"pending"`
	}
	ctx, cancel := context.WithTimeout(a.ctx, 25*time.Second)
	defer cancel()
	err := a.o.Call(ctx, "status", struct{}{}, &s)
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.flush()
	if a.up = err == nil; err != nil {
		return
	}
	a.self = s.PeerID
	var next []*machine
	for _, id := range append([]string{s.PeerID}, s.Peers...) {
		state := cmp.Or(s.States[id], "disconnected")
		if id == s.PeerID {
			state = "connected"
		}
		// A forgotten peer leaves this app's view.
		if state == "blocked" || slices.ContainsFunc(next, func(m *machine) bool { return m.id == id }) {
			continue
		}
		m := a.machine(id)
		if m == nil {
			m = &machine{id: id}
		}
		if m.state != "connected" || len(m.instances) == 0 {
			m.state = state
		}
		next = append(next, m)
	}
	a.machines = next
	a.claim = nil
	for _, inv := range s.Pending {
		if inv.Claimant != "" && inv.Status == "pending" {
			a.claim = &Claim{Invitation: inv.ID, Claimant: inv.Claimant}
			break
		}
	}
	// An invitation used or expired is no longer pending.
	if a.invitation != nil && !slices.ContainsFunc(s.Pending, func(inv mitosis.Invitation) bool {
		return inv.ID == a.invitation.id && inv.Status == "pending" && inv.Claimant == ""
	}) {
		a.invitation = nil
	}
	if !a.visible && !first {
		return
	}
	for _, m := range a.machines {
		if !m.fetching {
			m.fetching = true
			go a.fetch(m.id)
		}
	}
	if time.Since(a.threadsAt) > 20*time.Second {
		a.threadsAt = time.Now()
		for _, m := range a.machines {
			go a.loadThreads(m.id)
		}
	}
}

// fetch reads the conversations a machine's Orbs run (agent.describe), and its name when a peer
// was not named yet.
func (a *App) fetch(peer string) {
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	a.mu.Lock()
	m := a.machine(peer)
	unnamed := m != nil && peer != a.self && m.host == ""
	a.mu.Unlock()
	var named struct{ Name string }
	if unnamed && a.remote(ctx, peer, "mitosis.ping", struct{}{}, &named) == nil && named.Name != "" {
		a.mu.Lock()
		m.host = named.Name
		a.mu.Unlock()
	}
	var found []instance
	var err error
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if m := a.machine(peer); m != nil {
			m.fetching = false
			if err == nil {
				m.instances, m.read = found, true
				if len(found) > 0 {
					m.state = "connected"
				}
			}
		}
		a.flush()
	}()
	cursor := ""
	for range 32 {
		var page struct {
			Items  []mitosis.Conversation
			Cursor string
		}
		if err = a.remote(ctx, peer, "agent.describe", map[string]string{"cursor": cursor}, &page); err != nil {
			return
		}
		for _, c := range page.Items {
			found = append(found, instance{title: c.Title, cwd: c.CWD, session: c.SessionID, busy: c.State == "running", asks: c.State == "asking"})
		}
		if cursor = page.Cursor; cursor == "" {
			break
		}
	}
}

// loadThreads reads a machine's stored threads, open or not: those of a machine that lets this
// app start Orb there (host.sessions).
func (a *App) loadThreads(peer string) {
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	var found []thread
	var host, version string
	cursor := ""
	var err error
	for range 32 {
		var page struct {
			Host, Version, Cursor string
			Items                 []struct {
				ID       string `json:"session_id"`
				Name     string
				CWD      string
				First    string
				Modified int64
			}
		}
		if err = a.remote(ctx, peer, "host.sessions", map[string]string{"cursor": cursor}, &page); err != nil {
			break
		}
		host, version = page.Host, page.Version
		for _, it := range page.Items {
			title := it.Name
			if title == "" || title == "null" {
				title = cmp.Or(firstLine(invocation(it.First), 200), path.Base(it.CWD))
			}
			found = append(found, thread{id: it.ID, title: title, cwd: it.CWD, modified: it.Modified})
		}
		if cursor = page.Cursor; cursor == "" {
			break
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.machine(peer)
	switch {
	case m == nil:
		return
	case err == nil:
		m.threads, m.launch, m.version = found, true, version
		if peer != a.self && host != "" {
			m.host = host
		}
	case mitosis.Code(err) == "unauthorized":
		m.threads, m.launch = nil, false
	}
	a.flush()
}

// machinesState is the machines and Home as the app draws them.
func (a *App) machinesState() ([]Machine, []Entry) {
	machines, home := []Machine{}, []Entry{}
	// To the next minute: still "now" in an app, and Home is not sent again on every flush.
	now := time.Now().Truncate(time.Minute).Add(time.Minute).UnixMilli()
	hues := hues(a.machines, a.self)
	for _, m := range a.machines {
		name := a.name(m)
		out := Machine{ID: m.id, Name: name, Self: m.id == a.self, Connected: m.state == "connected", Version: m.version, Launch: m.launch, Hue: hues[m.id]}
		for _, i := range m.instances {
			what := cmp.Or(a.tabTitle(m.id, i.session), i.title)
			for _, th := range m.threads {
				if what == "" && th.id == i.session {
					what = th.title
				}
			}
			label := strings.Join(slices.DeleteFunc([]string{name, base(i.cwd), cmp.Or(what, "new thread")}, func(s string) bool { return s == "" }), " · ")
			// Two Orbs may share a folder: the labels stay distinct.
			for slices.ContainsFunc(out.Running, func(r Running) bool { return r.Label == label }) {
				label += " ·"
			}
			out.Running = append(out.Running, Running{Instance: i.session, Label: label, Busy: i.busy, Open: a.threadTab(m.id, i.session) != nil})
		}
		for _, th := range m.threads {
			at := slices.IndexFunc(out.Folders, func(f Folder) bool { return f.CWD == th.cwd })
			if at < 0 {
				out.Folders, at = append(out.Folders, Folder{CWD: th.cwd}), len(out.Folders)
			}
			f := &out.Folders[at]
			f.Threads, f.Modified = f.Threads+1, max(f.Modified, th.modified)
			f.Live = f.Live || slices.ContainsFunc(m.instances, func(i instance) bool { return i.cwd == th.cwd && i.busy })
		}
		slices.SortStableFunc(out.Folders, func(x, y Folder) int { return cmp.Compare(y.Modified, x.Modified) })
		machines = append(machines, out)
		for _, th := range m.threads {
			at := slices.IndexFunc(m.instances, func(i instance) bool { return i.session == th.id })
			t := a.threadTab(m.id, th.id)
			e := Entry{Key: "t:" + m.id + "|" + th.id, Title: th.title, Machine: m.id, CWD: th.cwd, Modified: th.modified, Open: t != nil, Deletable: m.id == a.self && at < 0}
			e.Live = at >= 0 && m.instances[at].busy
			e.Asks = at >= 0 && m.instances[at].asks
			if t != nil {
				e.Live, e.Asks, e.Open, e.Tab = t.Busy, t.Ask != nil, true, t.ID
			}
			if e.Live {
				e.Modified = now
			}
			home = append(home, e)
		}
		for _, i := range m.instances {
			if slices.ContainsFunc(m.threads, func(th thread) bool { return th.id == i.session }) {
				continue
			}
			e := Entry{Key: "i:" + i.session, Title: cmp.Or(a.tabTitle(m.id, i.session), i.title, base(i.cwd), "new thread"), Machine: m.id, CWD: i.cwd, Modified: now, Live: i.busy, Asks: i.asks, Unstored: true}
			if t := a.threadTab(m.id, i.session); t != nil {
				e.Live, e.Asks, e.Open, e.Tab = t.Busy, t.Ask != nil, true, t.ID
			}
			home = append(home, e)
		}
	}
	slices.SortStableFunc(home, func(x, y Entry) int { return cmp.Compare(y.Modified, x.Modified) })
	return machines, home
}

// hues gives each peer a hue of its own while there are enough: its id picks one, the next free
// one when taken, the peers taken in id order so each keeps its hue as others come and go.
func hues(machines []*machine, self string) map[string]int {
	const n = 6
	out, taken := map[string]int{}, map[int]bool{}
	ids := []string{}
	for _, m := range machines {
		if m.id != self {
			ids = append(ids, m.id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		hue := 1 + int(h.Sum32()%n)
		for i := 0; i < n && taken[hue]; i++ {
			hue = hue%n + 1
		}
		out[id], taken[hue] = hue, true
	}
	return out
}

func base(dir string) string {
	if dir == "" {
		return ""
	}
	return path.Base(dir)
}

func (a *App) threadTab(peer, session string) *tab {
	for _, t := range a.tabs {
		if t.Peer == peer && t.session == session {
			return t
		}
	}
	return nil
}

func (a *App) tabTitle(peer, session string) string {
	if t := a.threadTab(peer, session); t != nil {
		return t.Title
	}
	return ""
}

// launch starts Orb on a machine, in a folder (the home folder when none) on a new thread or on a
// stored thread, and returns the thread as soon as its Orb is on Mitosis.
func (a *App) launch(peer, cwd, session string) (string, error) {
	params := map[string]string{"cwd": cmp.Or(cwd, "~")}
	if session != "" {
		params = map[string]string{"session_id": session}
	}
	var r struct {
		ID string `json:"session_id"`
	}
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	err := a.launchError(a.remote(ctx, peer, "host.launch", params, &r))
	return r.ID, err
}

// launchError says why a machine did not start Orb on a thread or in a folder.
func (a *App) launchError(err error) error {
	switch mitosis.Code(err) {
	case "":
		return nil
	case "unauthorized":
		return fmt.Errorf("this device does not let %s start Orb · on it, run  orb mitosis trust %s", a.o.Name, a.self)
	case "not_found":
		return errors.New("no such folder or thread there")
	case "resource_exhausted":
		return errors.New("every Orb that machine can start is in the middle of a turn · try again when one finishes")
	case "busy":
		return errors.New("the Orb that has this thread open there is too old to share it · restart that Orb to open it here")
	}
	return err
}

// open opens a row of Home or a picker ("t:<machine>|<thread>", "i:<thread>" for one an Orb runs)
// in a tab: the tab that follows it, the Orb that has it open, or one started on it.
func (a *App) open(key string) (any, error) {
	kind, rest, _ := strings.Cut(key, ":")
	a.mu.Lock()
	var peer, session string
	running := false
	switch kind {
	case "i":
		session = rest
		for _, m := range a.machines {
			if slices.ContainsFunc(m.instances, func(i instance) bool { return i.session == rest }) {
				peer, running = m.id, true
			}
		}
	case "t":
		peer, session, _ = strings.Cut(rest, "|")
		if m := a.machine(peer); m != nil {
			running = slices.ContainsFunc(m.instances, func(i instance) bool { return i.session == session })
		}
	}
	m := a.machine(peer)
	t := a.threadTab(peer, session)
	if t == nil && m != nil && running {
		t = a.newTab(peer, a.name(m), session, true)
		// Titled from the start, as start does, so it never shows its folder's name meanwhile.
		for _, i := range m.instances {
			if i.session == session {
				t.Title = i.title
			}
		}
		for _, th := range m.threads {
			if th.id == session && t.Title == "" {
				t.Title = th.title
			}
		}
		a.balance()
	}
	a.mu.Unlock()
	if t != nil {
		return map[string]string{"tab": t.ID}, nil
	}
	if m == nil || session == "" {
		return nil, errors.New("nothing to open there")
	}
	return a.start(peer, "", session, "")
}

// start starts Orb on a machine, in a folder or on a thread, opens it in a tab and sends [text]
// once it is ready; Launching says how it goes.
func (a *App) start(peer, cwd, session, text string) (any, error) {
	a.mu.Lock()
	m := a.machine(peer)
	if m == nil {
		a.mu.Unlock()
		return nil, errors.New("no such machine")
	}
	where := a.name(m)
	a.launching = "starting Orb on " + where + "…"
	a.flush()
	a.mu.Unlock()
	session, err := a.launch(peer, cwd, session)
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.flush()
	if err != nil {
		a.launching = err.Error()
		return nil, err
	}
	a.launching = ""
	t := a.newTab(peer, where, session, true)
	// Titled from the start, so it never shows its folder's name while its Orb describes itself.
	t.Title = firstLine(text, 48)
	for _, th := range m.threads {
		if th.id == session {
			t.Title = th.title
		}
	}
	if text != "" {
		t.prompt(text)
	}
	a.balance()
	return map[string]string{"tab": t.ID}, nil
}

// sendHome is a message typed on Home: a command, or a new conversation on this machine.
func (a *App) sendHome(text string) (any, error) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	if strings.HasPrefix(text, "/") {
		if nav := navigation(name); nav != "" {
			return map[string]string{"nav": nav}, nil
		}
		if name == "new" {
			text = strings.TrimSpace(arg)
		}
	}
	a.mu.Lock()
	self := a.self
	a.mu.Unlock()
	return a.start(self, a.o.CWD, "", text)
}

// renameThread names a thread from Home through the Orb that has it open, its machine starting one
// if none does.
func (a *App) renameThread(key, name string) (any, error) {
	peer, session, _ := strings.Cut(strings.TrimPrefix(key, "t:"), "|")
	a.mu.Lock()
	if t := a.threadTab(peer, session); t != nil {
		t.rename(name)
		a.mu.Unlock()
		return nil, nil
	}
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	call := mitosis.AgentCall{SessionID: session, Method: "session.name", OperationID: protocol.NewID(), Args: mitosis.JSON(map[string]string{"name": name})}
	if err := a.remote(ctx, peer, "agent.call", call, nil); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.threadsAt = time.Time{}
	a.mu.Unlock()
	a.wakeMachines()
	return nil, nil
}

// delete deletes one of this machine's stored threads, one no Orb has open.
func (a *App) delete(key string) (any, error) {
	_, session, _ := strings.Cut(key, "|")
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if _, err := a.o.Run(ctx, "storage", "delete", session); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.threadsAt = time.Time{}
	a.mu.Unlock()
	a.wakeMachines()
	return nil, nil
}
