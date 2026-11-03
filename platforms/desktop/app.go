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
	o       *Orb
	stack   []screen
	tab     string            // the conversation shown, "" for Home
	drafts  map[string]string // each conversation's draft, "" for Home's
	focus   int               // asked to focus the prompt box, each time it grows
	picker  *picker
	rename  *renaming
	model   string    // the conversation whose model sheet shows
	viewing [2]string // an image shown whole: its reference and conversation
	notice  string    // one line that fades: copied, or why something failed
	lists   map[string]*ui.ListState
	shells  map[string]*shell  // each conversation's terminal, while it lives
	term    map[string]bool    // the conversations whose terminal shows
	answer  map[string]string  // the free answer typed to an interrupt
	split   map[string]float32 // the height of each conversation's terminal

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
	search     string               // the sidebar's search
	fold       map[string]bool      // the machines whose threads the sidebar folds
	listing    time.Time            // until when a tab shown waits for the view to list it
	termPane   ui.Handle            // the terminal on screen, which takes Control's keys off macOS
	answered   string               // the pairing claim answered, until the view drops it
	asked      map[string]time.Time // when each question showed: its keys arm a moment later
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

func (a *app) push(s screen) { reread(); a.stack = append(a.stack, s) }
func (a *app) back()         { reread(); a.stack = a.stack[:max(len(a.stack)-1, 0)] }

// show shows a conversation by its tab id, Home for "", closing any screen above them.
func (a *app) show(tab string) {
	a.stack = nil
	a.tab = tab
	a.listing = time.Now().Add(5 * time.Second)
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
	if a.tab != "" && o.tab(a.tab) == nil && o.state.Up && time.Now().After(a.listing) {
		a.tab = ""
	}
	// The terminals of conversations closed elsewhere end with them.
	for id, s := range a.shells {
		if o.state.Up && o.tab(id) == nil && id != a.tab {
			s.close()
			delete(a.shells, id)
		}
	}
	if a.top().kind == "" && a.tab != "" {
		o.show([]string{a.tab})
	} else {
		o.show(nil)
	}
	a.shortcuts(c)
	main := func() {
		ui.Column(c).Fill().Background(pal.bg).Children(func() {
			top := a.top()
			switch top.kind {
			case "":
				if t := o.tab(a.tab); t != nil {
					a.chat(c, t)
				} else {
					a.homeScreen(c)
				}
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
	ui.Column(c).Fill().Background(pal.bg).LetterSpacing(-0.4).Children(func() {
		if a.sideHidden {
			main()
			return
		}
		ui.Split(c, &a.side, func() { a.sidebar(c) }, main).Fill()
	})
	a.overlays(c)
}

// titleBar is the top of the main pane: it drags the window, and leaves the window's controls their room.
func titleBar(c *ui.Context, body func()) {
	tb := c.TitleBar()
	ui.Row(c).FillWidth().Height(max(tb.Height, 48)).Padding(0, tb.Right+12, 0, margin).Gap(14).BorderWidth(0, 0, 1, 0).BorderColor(p(c).rule).
		DragWindow().Children(body)
}

// close stops following a conversation; the one after it shows in its place, Home after the last.
func (a *app) close(id string) {
	tabs := a.o.state.Tabs
	if a.tab == id {
		a.tab = ""
		if at := slices.IndexFunc(tabs, func(t Tab) bool { return t.ID == id }); at >= 0 && len(tabs) > 1 {
			rest := slices.Delete(slices.Clone(tabs), at, at+1)
			a.tab = rest[min(at, len(rest)-1)].ID
		}
	}
	if s := a.shells[id]; s != nil {
		s.close()
		delete(a.shells, id)
	}
	delete(a.term, id)
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
			a.chooseModel(t)
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
	a.picker = &picker{title: "run on", options: options, pick: func(s string) {
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

func (a *app) chooseModel(t *Tab) {
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
	if runtime.GOOS != "darwin" && c.FocusWithin(a.termPane) {
		return
	}
	c.OnShortcut(cmd, ui.KeyN, func() { a.show("") })
	c.OnShortcut(cmd, ui.KeyW, func() {
		if a.tab != "" {
			a.close(a.tab)
		}
	})
	c.OnShortcut(cmd, ui.KeyPeriod, func() {
		if a.tab != "" {
			o.send("abort", "tab", a.tab)
		}
	})
	c.OnShortcut(cmd, ui.KeyJ, func() {
		if a.tab != "" {
			a.term[a.tab] = !a.term[a.tab]
		}
	})
	c.OnShortcut(cmd, ui.KeyL, func() { a.focus++ })
	c.OnShortcut(cmd|ui.Shift, ui.KeyM, func() { a.chooseModel(o.tab(a.tab)) })
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
	if t := o.tab(a.model); t != nil {
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
	if a.notice != "" {
		a.toast(c)
	}
}

// sheet is a card rising from the bottom over a dimmed window; a click outside or Escape closes it.
func sheet(c *ui.Context, open *bool, width float32, body func()) {
	pal := p(c)
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4)).Justify(ui.End).Padding(0, 0, 24, 0)
		panel.Width(width).MaxHeightPercent(80).Radius(pane+4).Background(pal.bg).Border(1, pal.rule).
			Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 40}, Animation: &ui.Smooth}).Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.25))
		body()
	})
}

func (a *app) pickerSheet(c *ui.Context) {
	pk, pal := a.picker, p(c)
	open := true
	sheet(c, &open, 460, func() {
		label(c, pk.title).Padding(18, margin, 6)
		ui.Scroll(c).MaxHeight(440).Children(func() {
			for _, o := range pk.options {
				sel := o == pk.selected
				row := ui.Row(c.Key(o)).FillWidth().Padding(11, margin).Children(func() {
					w := 400
					if sel {
						w = strong
					}
					ui.Text(c, o).Grow(1).FontSize(15).FontWeight(w).SingleLine()
					if sel {
						dot(c, pal.fg, 8, false, false)
					}
				})
				if row.Hovered() {
					row.Background(pal.raised)
				}
				if press(row) {
					a.picker = nil
					pk.pick(o)
				}
			}
		})
		ui.Box(c).Height(10)
	})
	if !open {
		a.picker = nil
	}
}

// renameSheet is a name in a field; done saves it, outside or Escape leaves it.
func (a *app) renameSheet(c *ui.Context) {
	r, pal := a.rename, p(c)
	open := true
	sheet(c, &open, 460, func() {
		label(c, "rename").Padding(18, margin, 0)
		ui.Row(c).Padding(10, 12, 16, margin).Gap(8).Children(func() {
			done := func() {
				if name := strings.TrimSpace(r.name); name != "" {
					r.apply(name)
				}
				a.rename = nil
			}
			if ui.TextInputBase(c, &r.name).Grow(1).FontSize(17).FontWeight(medium).AutoFocus().Submitted() {
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

// toast is one line at the bottom that fades away.
func (a *app) toast(c *ui.Context) {
	pal := p(c)
	ui.Overlay(c, func() {
		e := ui.Row(c).Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(110).Padding(8, 14).Radius(soft).Background(pal.fg)
		e.Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 12}}).Children(func() {
			ui.Text(c, a.notice).FontSize(small).TextColor(pal.bg).MaxWidth(520).MaxLines(3)
		})
	})
}
