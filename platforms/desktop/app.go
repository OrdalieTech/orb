package main

import (
	"cmp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// screen is a screen above Home and its conversations; the zero screen is them.
type screen struct {
	kind      string // bridge · invite · join · providers · vendor · plugins · device · folder
	peer, arg string // the machine it shows; a provider, a folder or a code
}

// picker is a choice list rising from the bottom: places, menus, confirmations all use it.
type picker struct {
	title    string
	options  []string
	selected string
	pick     func(string)
	at       *ui.Handle // the trigger it opens out of; none, a dialog
}

// renaming is a name to change, with the current one ready to edit: in its tab (row, the tab's
// id), else in a sheet.
type renaming struct {
	name, row string
	apply     func(string)
	took      bool // its field took the keys: losing them saves it
}

// app is everything the window shows that the view does not say: where it is, each conversation's
// draft, the sheets open.
type app struct {
	o          *Orb
	stack      []screen
	tab        string            // the conversation shown, "" for Home
	drafts     map[string]string // each conversation's draft, "" for Home's
	focus      int               // asked to focus the prompt box, each time it grows
	picker     *picker
	rename     *renaming
	root       *tile     // the window's tiles
	active     *tile     // the tile the keys act on; a.tab is its conversation
	building   *tile     // the tile being built
	corner     bool      // it reaches the window's top right, where the window's controls are
	left       bool      // or its top left, where the traffic lights are
	narrow     bool      // it is too narrow for its bar's details
	tight      bool      // and for its gauges: a column among four or five
	barHover   bool      // the pointer is on its bar, which shows its controls
	tools      bool      // they are drawn: left of its readings, which then stay put
	dragging   bool      // something is dragged: the tiles show where it would drop
	dragNow    bool      // a source said so in this frame
	linger     int       // frames the tiles keep taking a drop after the drag, which arrives after release
	morph      time.Time // until when the tiles glide to new places
	zoomed     *tile     // the tile shown alone, nil for the layout
	savedTiles []byte    // the layout as last saved
	model      string    // the conversation whose model sheet shows, next for a machine's default
	defaultOf  string    // that machine
	fresh      *Tab      // what a new conversation starts on: this computer's models and default, nil until read
	reading    bool      // fresh is being read
	viewing    [2]string // an image shown whole: its reference and conversation
	notice     string    // one line that fades: copied, or why something failed
	undo       func()    // what takes back what it says, while it shows
	lists      map[string]*ui.ListState
	shells     map[string]*shell // each conversation's terminal, while it lives
	answer     map[string]string // the free answer typed to an interrupt
	defaults   map[string]*Tab   // what a new conversation on each machine starts on, as it last said

	expanded map[string]bool              // the actions open, by tab and key
	details  map[string]map[string]string // what an open action was given and returned
	pics     map[string]*ui.Bitmap        // pictures by reference and size
	loading  map[string]bool              // pictures asked for

	query      string                  // the model sheet's search
	accounts   []Account               // the accounts of the machine the model sheet shows
	accountsOf string                  // that machine
	found      map[string][]Completion // each conversation's @ completions
	foundFor   map[string]string       // the @ token they were asked for
	focused    int                     // the last focus request the prompt box took
	path       string                  // what filters the explorer, or a path typed into it
	dir        Dirs                    // the folder it shows
	dirOf      string                  // on that machine
	dirErr     string                  // why it could not list it
	exploring  string                  // the machine the explorer shows, "" closed
	gone       map[string]bool         // tabs closed here, until the view says so too
	palette    bool                    // Go to (⌘K) shows
	search     string                  // what it looks for
	pick       int                     // the line ↩ chooses
	listing    time.Time               // until when a tab shown waits for the view to list it
	answered   string                  // the pairing claim answered, until the view drops it
	asked      map[string]time.Time    // when each question showed: its keys arm a moment later
	keyed      bool                    // what shows now was opened by a key, so it shows at once
	carded     bool                    // the start screen's title has wiped in, once a launch
	opening    string                  // the thread being opened, until its Orb answers
}

// cmd is the key that goes with the app's shortcuts: Command on macOS, Control elsewhere.
var cmd = func() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl
}()

func (a *app) top() screen {
	if len(a.stack) == 0 {
		return screen{}
	}
	return a.stack[len(a.stack)-1]
}

func (a *app) push(s screen) { reread(); a.fresh = nil; a.stack = append(a.stack, s) }
func (a *app) back() {
	reread()
	a.fresh = nil
	a.stack = a.stack[:max(len(a.stack)-1, 0)]
	if len(a.stack) == 0 {
		a.focus++ // back in the tiles, the focused one's prompt box has the keys again
	}
}

// next is the model sheet of a machine's default: what a new conversation there starts on.
const next = "next"

// starts is what a new conversation on this computer starts on, read once Bridge is up and again
// after another screen or conversation showed (Providers signs in; a conversation's model change
// becomes the default).
func (a *app) starts() *Tab {
	if a.fresh == nil && !a.reading && a.o.state.Up {
		a.reading = true
		a.readDefault(a.o.state.Self, func(t *Tab) { a.reading, a.fresh = false, t })
	}
	return a.fresh
}

// readDefault reads what a new conversation on a machine starts on: its models, the default and
// the default's reasoning. A machine that cannot say leaves the model to Providers, until the next read.
func (a *app) readDefault(peer string, done func(*Tab)) {
	then(a.o, func(r struct {
		Models, Levels    []string
		Default, Thinking string
	}, err error) {
		t := &Tab{Peer: peer, Where: device, Model: r.Default, Models: r.Models, Thinking: r.Thinking, Levels: r.Levels, Loaded: err == nil}
		if peer != a.o.state.Self {
			t.Where = a.machineName(peer)
		}
		a.defaults[peer] = t
		if done != nil {
			done(t)
		}
	}, "models", "machine", peer)
}

// makeDefault makes a model, at a reasoning level ("" keeps the level), the default for new
// conversations on a machine.
func (a *app) makeDefault(peer, model, level string) {
	for _, t := range []*Tab{a.defaults[peer], a.fresh} {
		if t != nil && t.Peer == peer {
			t.Model, t.Thinking = model, cmp.Or(level, t.Thinking)
		}
	}
	then(a.o, func(_ struct{}, err error) {
		if err != nil {
			a.say(err.Error())
		}
		a.readDefault(peer, func(t *Tab) {
			if peer == a.o.state.Self {
				a.fresh = t
			}
		})
	}, "default", "machine", peer, "name", model, "level", level)
}

// show shows a conversation by its tab id, Home for "", closing any screen above them: in the
// tile that has it as a tab, else as a new tab of the focused tile.
func (a *app) show(tab string) {
	a.stack, a.fresh = nil, nil
	a.listing = time.Now().Add(5 * time.Second)
	a.focus++ // its prompt box takes the keys, and with them its tile
	for _, x := range a.root.tiles(nil) {
		if tab != "" && !x.shell && slices.Contains(x.tabs, tab) {
			x.tab = tab
			a.activate(x)
			return
		}
	}
	x := a.tabsTile()
	if tab == "" {
		x.tab = ""
	} else {
		a.own(x, tab)
	}
	a.activate(x)
}

// say shows a line for a moment.
func (a *app) say(s string) { a.offer(s, nil) }

// offer shows a line with what takes it back, for longer: the hand has to reach it.
func (a *app) offer(s string, undo func()) {
	a.notice, a.undo = s, undo
	time.AfterFunc(map[bool]time.Duration{true: 4, false: 8}[undo == nil]*time.Second, func() {
		a.o.win.Update(func() {
			if a.notice == s {
				a.notice = ""
			}
		})
	})
}

func (a *app) view(c *ui.Context) {
	theme(c)
	o, pal := a.o, p(c)
	if o.state.Up {
		a.syncTabs()
	}
	if a.dragNow {
		a.linger = 3
	} else if a.linger > 0 {
		a.linger--
	}
	a.dragging, a.dragNow = a.linger > 0, false
	// The terminals of conversations closed elsewhere end with them.
	for id, s := range a.shells {
		if o.state.Up && o.tab(id) == nil && id != a.tab {
			s.close()
			delete(a.shells, id)
		}
	}
	// Every conversation on screen follows at full pace.
	var shown []string
	if a.top().kind == "" {
		for _, t := range a.onScreen().tiles(nil) {
			if t.tab != "" && !slices.Contains(shown, t.tab) {
				shown = append(shown, t.tab)
			}
		}
	}
	o.show(shown)
	a.shortcuts(c)
	if len(a.stack) > 0 && a.picker == nil && a.rename == nil && a.model == "" {
		c.OnShortcut(0, ui.KeyEscape, a.back)
	}
	main := func() {
		ui.Column(c).Fill().Background(pal.bg).Children(func() {
			top := a.top()
			switch top.kind {
			case "":
				top := a.onScreen()
				if top != a.zoomed {
					a.zoomed = nil
				}
				a.layout(c, top, true, true)
				a.building = nil
				a.saveTiles()
				return
			}
			// The window's controls keep their room above every other screen, in a bar that drags
			// it; Settings has its tabs there, level with the traffic lights.
			ui.Row(c).FillWidth().Height(max(c.TitleBar().Height, 48)).Justify(ui.Center).AlignItems(ui.Center).DragWindow().Children(func() {
				if slices.ContainsFunc(settingsKinds, func(s struct{ kind, name string }) bool { return s.kind == top.kind }) {
					a.settingsTabs(c)
				}
			})
			switch top.kind {
			case "general":
				a.generalScreen(c)
			case "bridge":
				a.bridgeScreen(c)
			case "invite":
				a.inviteScreen(c)
			case "join":
				a.joinScreen(c, top.arg)
			case "providers":
				a.providersScreen(c, top.peer)
			case "vendor":
				a.vendorScreen(c, top.arg, top.peer)
			case "plugins":
				a.pluginsScreen(c, top.peer)
			case "folder":
				a.folderScreen(c, top.peer, top.arg)
			}
		})
	}
	ui.Column(c).Fill().Background(pal.bg).LineHeight(1.45).Children(main)
	a.overlays(c)
}

// titleBar is the top of a tile: it drags the window, leaves the window's controls their room in
// the corner, and holds the tile's own controls while the pointer is on it.
func (a *app) titleBar(c *ui.Context, body func()) {
	tb := c.TitleBar()
	left, right := float32(8), float32(margin)
	if a.left {
		left = tb.Left + 12
	}
	if a.corner {
		right = tb.Right + 12
	}
	// Not clickable: a click target does not drag the window. Its title focuses the tile.
	bar := ui.Row(c).FillWidth().Height(max(tb.Height, 48)).Padding(0, right, 0, left).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(p(c).rule).Clip().DragWindow()
	hovered := bar.Hovered()
	bar.Children(func() {
		a.tools, a.barHover = false, hovered
		if x := a.building; x != nil && !x.shell {
			a.tabChips(c, x)
		}
		body()
		a.tileTools(c)
		if a.corner {
			a.menuMark(c)
		}
	})
	a.tileMenu(bar)
}

// close stops following a conversation, its Orb running on: its tab goes, its neighbour shows in
// its place, Home after the last; its terminal closes.
func (a *app) close(id string) {
	if a.gone == nil {
		a.gone = map[string]bool{}
	}
	a.gone[id] = true
	// A closed conversation can come back for a moment: its thread reopens where New session would show.
	for _, e := range a.o.home.Entries {
		if e.Tab == id {
			a.offer("closed “"+cmp.Or(e.Title, "new session")+"”", func() { a.newSession(); a.open(e.Key) })
		}
	}
	for _, t := range a.root.tiles(nil) {
		if t.shell && t.tab == id {
			a.unsplit(t)
		} else if !t.shell {
			a.drop(t, id)
		}
	}
	a.activate(a.active)
	if s := a.shells[id]; s != nil {
		s.close()
		delete(a.shells, id)
	}
	delete(a.drafts, id)
	delete(a.found, id)
	delete(a.foundFor, id)
	a.o.send("close", "tab", id)
}

func (a *app) renameTab(t Tab) {
	a.rename = &renaming{name: t.Title, row: t.ID, apply: func(name string) { a.o.send("rename", "tab", t.ID, "name", name) }}
}

func (a *app) renameThread(e Entry) {
	a.rename = &renaming{name: e.Title, apply: func(name string) { a.o.send("rename", "key", e.Key, "name", name) }}
}

// threadMenu is a thread's menu, as the phone's: rename it through the Orb that has it open, or
// delete one no Orb has open, which unlike closing cannot come back, so it asks first.
func (a *app) threadMenu(m *ui.Menu, e Entry) {
	if m.Item("Rename…").Chosen() {
		a.renameThread(e)
	}
	if e.Deletable && m.Item("Delete forever…").Chosen() {
		a.picker = &picker{title: "delete “" + e.Title + "”? it cannot come back", options: []string{"delete forever", "keep it"}, pick: func(s string) {
			if s == "delete forever" {
				a.o.send("delete", "key", e.Key)
			}
		}}
	}
}

// send sends what the prompt box holds: the view runs commands and messages, and names what only
// the app does (a screen to open, text to copy).
func (a *app) send(t *Tab, text string) {
	id := ""
	if t != nil {
		id = t.ID
	}
	then(a.o, func(r struct{ Tab, Nav, Copy string }, err error) {
		if err != nil {
			a.say(err.Error())
			// Refused (no Orb could start, say): the text goes back where it was typed.
			if a.drafts[id] == "" {
				a.drafts[id] = text
			}
			return
		}
		if r.Tab != "" {
			a.show(r.Tab)
		}
		if r.Copy != "" {
			mygo.Clipboard.WriteText(r.Copy)
			a.say("copied the last answer")
		}
		peer := a.o.state.Self
		if t != nil {
			peer = t.Peer
		}
		switch r.Nav {
		case "model":
			a.chooseModel(t, true)
		case "home":
			a.show("")
		case "pair":
			a.push(screen{kind: "bridge"})
		case "providers", "plugins":
			a.push(screen{kind: r.Nav, peer: peer})
		}
	}, "send", "tab", id, "text", text)
}

// open opens a Home row or a running Orb in its tab.
func (a *app) open(key string) {
	a.opening = key
	then(a.o, func(r struct{ Tab string }, err error) {
		if a.opening == key {
			a.opening = ""
		}
		a.opened(r.Tab, err)
	}, "open", "key", key)
}

// start starts Orb on a machine, in a folder or (cwd empty) where it starts, and opens it.
func (a *app) start(machine, cwd string) {
	then(a.o, func(r struct{ Tab string }, err error) { a.opened(r.Tab, err) }, "start", "machine", machine, "cwd", cwd)
}

func (a *app) opened(tab string, err error) {
	if err != nil {
		a.say(err.Error())
	} else if tab != "" {
		a.show(tab)
	}
}

// chooseWhere chooses where the next message goes: a new conversation on this computer, or in a
// folder of a machine. Orbs already running are a thread away (⌘K), live.
func (a *app) chooseWhere() {
	a.keyed = false
	options, hosts := []string{"new on " + device}, []string{}
	for _, m := range a.o.home.Machines {
		if m.Launch {
			options, hosts = append(options, "a folder on "+m.Name+"…"), append(hosts, m.ID)
		}
	}
	a.picker = &picker{title: "run on", options: options, at: &a.active.whereAt, pick: func(s string) {
		if at := slices.Index(options, s); at == 0 {
			a.start(a.o.state.Self, "")
		} else {
			a.explore(hosts[at-1])
		}
	}}
}

// chooseModel opens the model sheet of a conversation, or with no id of a machine's default (t
// nil: this computer's), at once: models not read yet fill it as they come.
func (a *app) chooseModel(t *Tab, keyed bool) {
	a.keyed = keyed
	if t == nil {
		t = cmp.Or(a.starts(), a.defaults[a.o.state.Self], &Tab{Peer: a.o.state.Self, Where: device})
	}
	a.model, a.defaultOf = cmp.Or(t.ID, next), t.Peer
	if t.ID == "" {
		a.defaults[t.Peer] = t
	}
	// Asked as it opens: a conversation elsewhere may have changed it.
	a.readDefault(t.Peer, nil)
}

// closeTab closes a screen, else the tab shown (its Orb runs on), else the tile it leaves empty.
func (a *app) closeTab() {
	switch x := a.active; {
	case len(a.stack) > 0:
		a.back()
	case x.tab != "" && !x.shell:
		a.close(x.tab)
	case !a.root.leaf() || x.shell:
		a.unsplit(x)
	}
}

// menu is the macOS menu bar, as Ghostty's: the tile keys are its items, which macOS gives the
// keys to before the view. It has no Close Window, which would take ⌘W.
func (a *app) menu() *mygo.Menu {
	do := func(f func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) { a.o.win.Update(f) }
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "New Tab", Accelerator: "Cmd+T", Click: do(a.newSession)},
			{Label: "New Session", Accelerator: "Cmd+N", Click: do(a.newSession)},
			{Label: "Close Tab", Accelerator: "Cmd+W", Click: do(a.closeTab)},
			{Label: "Close Tile", Accelerator: "Shift+Cmd+W", Click: do(func() { a.unsplit(a.active) })},
			mygo.Separator(),
			{Label: "Settings…", Accelerator: "Cmd+,", Click: do(a.settings)},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{Label: "Split Right", Accelerator: "Cmd+D", Click: do(func() { a.split(a.active, "right", &tile{}) })},
			{Label: "Split Down", Accelerator: "Shift+Cmd+D", Click: do(func() { a.split(a.active, "bottom", &tile{}) })},
			{Label: "Terminal", Accelerator: "Cmd+J", Click: do(func() {
				if a.tab != "" {
					a.terminalOf(a.tab)
				}
			})},
			mygo.Separator(),
			{Label: "Models", Accelerator: "Ctrl+M", Click: do(func() { a.chooseModel(a.o.tab(a.tab), true) })},
			{Label: "Go to…", Accelerator: "Cmd+K", Click: do(a.openPalette)},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}

// shortcuts are the menu's keys, as the Mac app had them.
func (a *app) shortcuts(c *ui.Context) {
	o := a.o
	// Off macOS they are Control's keys, which a shell in the terminal needs (Ctrl+W, Ctrl+L…):
	// there only those no shell takes stay, enough to leave it.
	c.OnShortcut(ui.Ctrl, ui.KeyTab, func() { a.cycle(1) })
	c.OnShortcut(ui.Ctrl|ui.Shift, ui.KeyTab, func() { a.cycle(-1) })
	c.OnShortcut(cmd|ui.Shift, ui.KeyEnter, func() {
		if !a.root.leaf() {
			a.zoom(a.active)
		}
	})
	if a.active.shell && c.FocusWithin(a.active.at) {
		if runtime.GOOS != "darwin" {
			return
		}
	} else {
		// ⌃M: Return to a shell, so not in one.
		c.OnShortcut(ui.Ctrl, ui.KeyM, func() { a.chooseModel(o.tab(a.tab), true) })
	}
	c.OnShortcut(cmd, ui.KeyN, a.newSession)
	c.OnShortcut(cmd, ui.KeyK, a.openPalette)
	c.OnShortcut(cmd, ui.KeyT, a.newSession)
	c.OnShortcut(cmd, ui.KeyW, a.closeTab)
	c.OnShortcut(cmd|ui.Shift, ui.KeyW, func() { a.unsplit(a.active) })
	c.OnShortcut(cmd, ui.KeyD, func() { a.split(a.active, "right", &tile{}) })
	c.OnShortcut(cmd|ui.Shift, ui.KeyD, func() { a.split(a.active, "bottom", &tile{}) })
	c.OnShortcut(cmd, ui.KeyBracketLeft, func() { a.cycle(-1) })
	c.OnShortcut(cmd, ui.KeyBracketRight, func() { a.cycle(1) })
	c.OnShortcut(cmd, ui.KeyEqual, func() { a.root.even(true); a.reshape() })
	c.OnShortcut(cmd, ui.KeyPeriod, func() {
		if a.tab != "" {
			o.send("abort", "tab", a.tab)
		}
	})
	c.OnShortcut(cmd, ui.KeyJ, func() {
		if a.tab != "" {
			a.terminalOf(a.tab)
		}
	})
	c.OnShortcut(cmd, ui.KeyL, func() { a.focus++ })
	c.OnShortcut(cmd|ui.Shift, ui.KeyM, func() { a.chooseModel(o.tab(a.tab), true) })
	c.OnShortcut(cmd|ui.Shift, ui.KeyC, func() {
		if t := o.tab(a.tab); t != nil {
			a.send(t, "/copy")
		}
	})
	c.OnShortcut(cmd, ui.KeyComma, a.settings)
	c.OnShortcut(cmd|ui.Shift, ui.KeyBracketLeft, func() { a.move(-1) })
	c.OnShortcut(cmd|ui.Shift, ui.KeyBracketRight, func() { a.move(1) })
	for i := range 9 {
		c.OnShortcut(cmd, ui.Key1+ui.Key(i), func() {
			if x := a.tabsTile(); i < len(x.tabs) {
				a.show(x.tabs[i])
			}
		})
	}
}

// move shows the focused tile's next tab, or the previous one (⇧⌘] ⇧⌘[).
func (a *app) move(by int) {
	tabs := a.tabsTile().tabs
	if len(tabs) == 0 {
		return
	}
	at := slices.Index(tabs, a.tab)
	if at < 0 && by < 0 {
		at = 0 // from Home, back to the last
	}
	a.show(tabs[(at+by+len(tabs))%len(tabs)])
}

// keys names a shortcut as the platform writes it.
func keys(k string) string {
	if runtime.GOOS == "darwin" {
		return "⌘" + k
	}
	return "Ctrl+" + k
}

// overlays: interrupts belong to their owner and stop the world wherever you are; then the sheets.
func (a *app) overlays(c *ui.Context) {
	o := a.o
	var asking *Tab
	if t := o.tab(a.tab); t != nil && t.Ask != nil && a.top().kind == "" {
		asking = t
	}
	for i := range o.state.Tabs {
		if asking == nil && o.state.Tabs[i].Ask != nil {
			asking = &o.state.Tabs[i]
		}
	}
	if asking != nil {
		id := asking.ID
		a.interrupt(c, *asking.Ask, func(v *string) { o.send("answer", "tab", id, "value", v) })
	} else if cl := o.state.Claim; cl != nil && cl.Claimant != a.answered {
		a.pairRequest(c, *cl)
	}
	if a.picker != nil {
		a.pickerSheet(c)
	}
	if a.palette {
		a.paletteSheet(c)
	}
	if a.exploring != "" {
		a.explorer(c)
	}
	t := o.tab(a.model)
	if a.model == next {
		t = a.defaults[a.defaultOf]
	}
	if t != nil {
		a.modelSheet(c, t)
	} else {
		a.model = ""
	}
	if a.rename != nil && a.rename.row == "" {
		a.renameSheet(c)
	}
	if a.viewing[0] != "" {
		a.viewer(c)
	}
	a.toast(c)
}

// popover opens out of its trigger, which at is bound to: from 95% and transparent to whole in
// 200 ms, easing out, growing from the trigger's side; a press outside or Escape closes it. One a
// key opened shows at once, as anything a key does.
func popover(c *ui.Context, at *ui.Handle, open *bool, width float32, keyed bool, body func()) {
	pal := p(c)
	anchor := c.Resolve(*at)
	if !anchor.Valid() {
		*open = false
		return
	}
	_, h := c.Size()
	b := anchor.Bounds()
	above := b.Y > h/2 // where PopoverBase finds room for it
	room := h - b.Y - b.H
	if above {
		room = b.Y
	}
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		panel.Width(width).MaxHeight(room-16).Margin(8, 0, 0, 0).Radius(12).Background(pal.bg).Border(1, pal.rule).Clip().
			Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, map[bool]float32{true: 0.4, false: 0.14}[c.Theme().Dark]))
		if above {
			panel.TransformOrigin(0, 1)
		} else {
			panel.TransformOrigin(0, 0)
		}
		if !keyed {
			panel.Transition(ui.ElementTransition{Enter: &ui.Motion{Scale: 0.95}, Exit: &ui.Motion{Scale: 0.95}, Animation: &opening})
		}
		body()
	})
}

// modal is a dialog in the middle of the dimmed window, which it was not opened from, so it grows
// from its centre: from 96% and transparent in 250 ms, easing out.
func modal(c *ui.Context, open *bool, width float32, body func()) {
	pal := p(c)
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.32)).Transition(ui.ElementTransition{Enter: &ui.Motion{}, Exit: &ui.Motion{}, Animation: &showing})
		panel.Width(width).MaxHeightPercent(80).Radius(14).Background(pal.bg).Border(1, pal.rule).Clip().
			Shadow(0, 16, 48, 0, ui.RGBA(0, 0, 0, 0.25)).
			Transition(ui.ElementTransition{Enter: &ui.Motion{Scale: 0.96}, Exit: &ui.Motion{Scale: 0.96}, Animation: &showing})
		body()
	})
}

// menuRow is a choice in a popover or a dialog: its text, a note by it (the default), and a dot by
// the one chosen.
func menuRow(c *ui.Context, text, note string, chosen bool) ui.Element {
	pal := p(c)
	return hover(ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Gap(10), pal.raised).Children(func() {
		w := 400
		if chosen {
			w = strong
		}
		ui.Text(c, text).Grow(1).Basis(0).FontWeight(w).SingleLine().Tooltip(text)
		if note != "" {
			label(c, note).TextColor(pal.meta)
		}
		if chosen {
			dot(c, pal.fg, 6, false, false)
		}
	})
}

func (a *app) pickerSheet(c *ui.Context) {
	pk := a.picker
	open := true
	list := func() {
		label(c, pk.title).Padding(12, 16, 6)
		ui.Scroll(c).MaxHeight(420).Shrink(1).Padding(0, 4, 6).Children(func() {
			for _, o := range pk.options {
				if press(menuRow(c.Key(o), o, "", o == pk.selected)) {
					a.picker = nil
					pk.pick(o)
				}
			}
		})
	}
	if pk.at != nil {
		popover(c, pk.at, &open, 340, a.keyed, list)
	} else {
		modal(c, &open, 420, list)
	}
	if !open {
		a.picker = nil
	}
}

// renameSheet is a name in a field; done saves it, outside or Escape leaves it.
func (a *app) renameSheet(c *ui.Context) {
	r, pal := a.rename, p(c)
	open := true
	modal(c, &open, 460, func() {
		label(c, "rename").Padding(18, margin, 0)
		ui.Row(c).Padding(10, 12, 16, margin).Gap(8).Children(func() {
			done := func() {
				if name := strings.TrimSpace(r.name); name != "" {
					r.apply(name)
				}
				a.rename = nil
			}
			if ui.TextInputBase(c, &r.name).Grow(1).FontSize(17).FontWeight(medium).LetterSpacing(-0.3).AutoFocus().Submitted() {
				done()
			}
			if btn(c, "save", true, pal.fg, pal.bg) {
				done()
			}
		})
	})
	if !open {
		a.rename = nil
	}
}

// toast is one line at the bottom: it rises in and sinks out the same way, slower and softer than
// the rest, as a passing note.
func (a *app) toast(c *ui.Context) {
	pal := p(c)
	ui.Overlay(c, func() {
		ui.Column(c).Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(96).Children(func() {
			if a.notice == "" {
				return
			}
			slide := ui.Animation{Duration: 400 * time.Millisecond, Ease: ui.EaseInOut}
			ui.Row(c.Key(a.notice)).Padding(8, 14).Radius(10).Background(pal.fg).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.18)).
				Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 16}, Exit: &ui.Motion{Y: 16}, Animation: &slide}).
				Gap(14).Children(func() {
				ui.Text(c, a.notice).FontSize(small).TextColor(pal.bg).MaxWidth(520).MaxLines(3)
				if a.undo != nil && press(ui.Text(c, "undo").FontSize(small).FontWeight(strong).TextColor(pal.bg)) {
					a.undo()
					a.notice, a.undo = "", nil
				}
			})
		})
	})
}
