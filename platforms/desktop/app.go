package main

import (
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

// renaming is a name to change, with the current one ready to edit, and maybe a way to delete it.
type renaming struct {
	name   string
	delete func()
	apply  func(string)
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
	active     *tile     // the tile the keys and the sidebar act on; a.tab is its conversation
	building   *tile     // the tile being built
	corner     bool      // it reaches the window's top right, where the window's controls are
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
	model      string    // the conversation whose model sheet shows, next for Home's
	fresh      *Tab      // what a new conversation starts on: this computer's models and default, nil until read
	reading    bool      // fresh is being read
	viewing    [2]string // an image shown whole: its reference and conversation
	notice     string    // one line that fades: copied, or why something failed
	lists      map[string]*ui.ListState
	shells     map[string]*shell // each conversation's terminal, while it lives
	answer     map[string]string // the free answer typed to an interrupt

	expanded map[string]bool              // the actions open, by tab and key
	details  map[string]map[string]string // what an open action was given and returned
	pics     map[string]*ui.Bitmap        // pictures by reference and size
	loading  map[string]bool              // pictures asked for

	query      string    // the model sheet's search
	accounts   []Account // the accounts of the machine the model sheet shows
	accountsOf string    // that machine
	found      []Completion
	foundFor   string  // the @ token completions were asked for
	focused    int     // the last focus request the prompt box took
	path       string  // a folder typed on a device's screen
	side       float32 // the sidebar's width
	sideHidden bool
	search     string // the sidebar's search
	seek       int    // search asked for (⌘K), which its field takes once
	sought     int
	fold       map[string]bool      // the machines whose threads the sidebar folds
	listing    time.Time            // until when a tab shown waits for the view to list it
	answered   string               // the pairing claim answered, until the view drops it
	asked      map[string]time.Time // when each question showed: its keys arm a moment later
	keyed      bool                 // what shows now was opened by a key, so it shows at once
	carded     bool                 // the start screen's title has wiped in, once a launch
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
func (a *app) back()         { reread(); a.fresh = nil; a.stack = a.stack[:max(len(a.stack)-1, 0)] }

// next is the model sheet of the conversation Home starts.
const next = "next"

// starts is what a new conversation on this computer starts on, read once Bridge is up and again
// after another screen or conversation showed (Providers signs in; a conversation's model change
// becomes the default).
func (a *app) starts() *Tab {
	o := a.o
	if a.fresh == nil && !a.reading && o.state.Up {
		a.reading = true
		self := o.state.Self
		then(o, func(r struct {
			Models  []string
			Default string
		}, err error) {
			// A machine that cannot say leaves the model to Providers, until the next read.
			a.reading = false
			a.fresh = &Tab{Peer: self, Where: device, Model: r.Default, Models: r.Models, Loaded: err == nil}
		}, "models", "machine", self)
	}
	return a.fresh
}

// show shows a conversation by its tab id, Home for "", closing any screen above them.
// In tiles, it focuses the tile already showing it, else shows it in the focused one.
func (a *app) show(tab string) {
	a.stack, a.fresh = nil, nil
	a.listing = time.Now().Add(5 * time.Second)
	a.focus++ // its prompt box takes the keys, and with them its tile
	if x := a.showing(tab); x != nil && tab != "" {
		a.activate(x)
		return
	}
	x := a.active
	if x.shell {
		if s := a.showing(x.tab); s != nil {
			x = s
		}
		x.shell = false
	}
	x.tab = tab
	a.activate(x)
}

// say shows a line for a moment.
func (a *app) say(s string) {
	a.notice = s
	time.AfterFunc(4*time.Second, func() {
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
	// A tab just opened is shown before the view lists it; one it no longer lists was closed elsewhere.
	if o.state.Up && time.Now().After(a.listing) {
		for _, t := range a.root.tiles(nil) {
			if t.tab != "" && o.tab(t.tab) == nil {
				a.unsplit(t)
			}
		}
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
		for _, t := range a.root.tiles(nil) {
			if t.tab != "" && !slices.Contains(shown, t.tab) {
				shown = append(shown, t.tab)
			}
		}
	}
	o.show(shown)
	a.shortcuts(c)
	main := func() {
		ui.Column(c).Fill().Background(pal.bg).Children(func() {
			top := a.top()
			switch top.kind {
			case "":
				top := a.root
				if a.zoomed != nil && a.contains(a.zoomed) {
					top = a.zoomed
				} else {
					a.zoomed = nil
				}
				a.layout(c, top, true)
				a.building = nil
				a.saveTiles()
				return
			}
			// The window's controls keep their room above every other screen.
			ui.Box(c).FillWidth().Height(c.TitleBar().Height).DragWindow()
			switch top.kind {
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
			case "device":
				a.deviceScreen(c, top.peer)
			case "folder":
				a.folderScreen(c, top.peer, top.arg)
			}
		})
	}
	ui.Column(c).Fill().Background(pal.bg).LineHeight(1.45).Children(func() {
		if a.sideHidden {
			main()
			return
		}
		ui.Split(c, &a.side, func() { a.sidebar(c) }, main).Fill()
	})
	a.overlays(c)
}

// titleBar is the top of a tile: it drags the window, leaves the window's controls their room in
// the corner, and holds the tile's own controls while the pointer is on it.
func (a *app) titleBar(c *ui.Context, body func()) {
	tb := c.TitleBar()
	right := float32(margin)
	if a.corner {
		right = tb.Right + 12
	}
	bar := ui.Row(c).FillWidth().Height(max(tb.Height, 48)).Padding(0, right, 0, margin).Gap(14).BorderWidth(0, 0, 1, 0).BorderColor(p(c).rule).Clip().DragWindow()
	if x := a.building; x != nil {
		bar.OnClick(func() { a.activate(x) })
	}
	hovered := bar.Hovered()
	bar.Children(func() {
		a.tools, a.barHover = false, hovered
		body()
		a.tileTools(c)
	})
	a.tileMenu(bar)
}

// close stops following a conversation; the one after it shows in its place, Home after the last.
// Among tiles, its tiles close.
func (a *app) close(id string) {
	tabs := a.o.state.Tabs
	for _, t := range a.root.tiles(nil) {
		if t.tab != id {
			continue
		}
		if t.shell || !a.root.leaf() {
			a.unsplit(t)
			continue
		}
		t.tab = ""
		if at := slices.IndexFunc(tabs, func(t Tab) bool { return t.ID == id }); at >= 0 && len(tabs) > 1 {
			rest := slices.Delete(slices.Clone(tabs), at, at+1)
			t.tab = rest[min(at, len(rest)-1)].ID
		}
		a.activate(t)
	}
	if s := a.shells[id]; s != nil {
		s.close()
		delete(a.shells, id)
	}
	delete(a.drafts, id)
	a.o.send("close", "tab", id)
}

func (a *app) renameTab(t Tab) {
	a.rename = &renaming{name: t.Title, apply: func(name string) { a.o.send("rename", "tab", t.ID, "name", name) }}
}

// renameThread renames a Home row's thread through the Orb that has it open, and offers deleting it.
func (a *app) renameThread(e Entry) {
	r := &renaming{name: e.Title, apply: func(name string) { a.o.send("rename", "key", e.Key, "name", name) }}
	if e.Deletable {
		r.delete = func() { a.o.send("delete", "key", e.Key) }
	}
	a.rename = r
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
	then(a.o, func(r struct{ Tab string }, err error) { a.opened(r.Tab, err) }, "open", "key", key)
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

// chooseWhere chooses where the next message goes: a new conversation on this computer, an Orb
// running somewhere, or a folder of a machine.
func (a *app) chooseWhere() {
	a.keyed = false
	var running []Running
	var hosts []Machine
	for _, m := range a.o.home.Machines {
		running = append(running, m.Running...)
		if !m.Self && m.Launch {
			hosts = append(hosts, m)
		}
	}
	options := []string{"new on " + device}
	for _, r := range running {
		options = append(options, r.Label)
	}
	for _, m := range hosts {
		options = append(options, "a folder on "+m.Name+"…")
	}
	a.picker = &picker{title: "run on", options: options, at: &a.active.whereAt, pick: func(s string) {
		at := slices.Index(options, s)
		switch {
		case at == 0:
			a.start(a.o.state.Self, "")
		case at <= len(running):
			a.open("i:" + running[at-1].Instance)
		default:
			a.push(screen{kind: "device", peer: hosts[at-1-len(running)].ID})
		}
	}}
}

func (a *app) chooseModel(t *Tab, keyed bool) {
	a.keyed = keyed
	if f := a.starts(); t == nil && f != nil && len(f.Models) > 0 {
		a.model = next
		return
	}
	if t == nil || len(t.Models) == 0 {
		peer := a.o.state.Self
		if t != nil {
			peer = t.Peer
		}
		a.push(screen{kind: "providers", peer: peer})
		return
	}
	a.model = t.ID
}

// shortcuts are the menu's keys, as the Mac app had them.
func (a *app) shortcuts(c *ui.Context) {
	o := a.o
	// Off macOS they are Control's keys, which a shell in the terminal needs (Ctrl+W, Ctrl+L…).
	if runtime.GOOS != "darwin" && a.active.shell && c.FocusWithin(a.active.at) {
		return
	}
	c.OnShortcut(cmd, ui.KeyN, a.newSession)
	c.OnShortcut(cmd, ui.KeyK, func() { a.sideHidden = false; a.seek++ })
	c.OnShortcut(ui.Ctrl, ui.KeyTab, func() { a.cycle(1) })
	c.OnShortcut(ui.Ctrl|ui.Shift, ui.KeyTab, func() { a.cycle(-1) })
	// Among tiles ⌘W closes the tile, its conversation staying open in the sidebar.
	c.OnShortcut(cmd, ui.KeyW, func() {
		if !a.root.leaf() {
			a.unsplit(a.active)
		} else if a.tab != "" {
			a.close(a.tab)
		}
	})
	c.OnShortcut(cmd, ui.KeyD, func() { a.split(a.active, "right", &tile{}) })
	c.OnShortcut(cmd|ui.Shift, ui.KeyD, func() { a.split(a.active, "bottom", &tile{}) })
	c.OnShortcut(cmd, ui.KeyBracketLeft, func() { a.cycle(-1) })
	c.OnShortcut(cmd, ui.KeyBracketRight, func() { a.cycle(1) })
	c.OnShortcut(cmd, ui.KeyEqual, func() { a.root.even(true); a.reshape() })
	c.OnShortcut(cmd|ui.Shift, ui.KeyEnter, func() {
		if !a.root.leaf() {
			a.zoom(a.active)
		}
	})
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
	c.OnShortcut(cmd, ui.KeyComma, func() { a.push(screen{kind: "providers", peer: o.state.Self}) })
	c.OnShortcut(cmd, ui.KeyBackslash, func() { a.sideHidden = !a.sideHidden })
	c.OnShortcut(cmd|ui.Shift, ui.KeyBracketLeft, func() { a.move(-1) })
	c.OnShortcut(cmd|ui.Shift, ui.KeyBracketRight, func() { a.move(1) })
	for i := range 9 {
		c.OnShortcut(cmd, ui.Key1+ui.Key(i), func() {
			if i < len(o.state.Tabs) {
				a.show(o.state.Tabs[i].ID)
			}
		})
	}
}

func (a *app) move(by int) {
	tabs := a.o.state.Tabs
	if len(tabs) == 0 {
		return
	}
	at := slices.IndexFunc(tabs, func(t Tab) bool { return t.ID == a.tab })
	if at < 0 && by < 0 {
		at = 0 // from Home, back to the last
	}
	a.show(tabs[(at+by+len(tabs))%len(tabs)].ID)
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
	t := o.tab(a.model)
	if a.model == next {
		t = a.fresh
	}
	if t != nil {
		a.modelSheet(c, t)
	} else {
		a.model = ""
	}
	if a.rename != nil {
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

// menuRow is a choice in a popover or a dialog: its text, and a dot by the one chosen.
func menuRow(c *ui.Context, text string, chosen bool) ui.Element {
	pal := p(c)
	return hover(ui.Row(c).FillWidth().Padding(8, 12).Radius(8), pal.raised).Children(func() {
		w := 400
		if chosen {
			w = strong
		}
		ui.Text(c, text).Grow(1).Basis(0).FontWeight(w).SingleLine().Tooltip(text)
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
		ui.Scroll(c).MaxHeight(420).Padding(0, 4, 6).Children(func() {
			for _, o := range pk.options {
				if press(menuRow(c.Key(o), o, o == pk.selected)) {
					a.picker = nil
					pk.pick(o)
				}
			}
		})
	}
	if pk.at != nil {
		popover(c, pk.at, &open, 300, a.keyed, list)
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
			if r.delete != nil && btn(c, "delete", false, rupture, pal.bg) {
				r.delete()
				a.rename = nil
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
				Children(func() { ui.Text(c, a.notice).FontSize(small).TextColor(pal.bg).MaxWidth(520).MaxLines(3) })
		})
	})
}
