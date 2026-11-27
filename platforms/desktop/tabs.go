package main

import (
	"cmp"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// tabChips are a tile's tabs, at the left of its bar: the conversations open in it, as a
// browser's. Each says what it does (asks, unread, working, opening); a click shows it, a double
// click renames it, × closes it (its Orb runs on), a drag takes it to another tile or a side.
// Home shows as a tab of its own while the tile shows it; + opens it.
func (a *app) tabChips(c *ui.Context, x *tile) {
	o, pal := a.o, p(c)
	chip := func(key string, on bool, body func(hovered bool)) ui.Element {
		e := ui.Row(c.Key(key)).Height(28).Padding(0, 6, 0, 10).Gap(7).Radius(6).Shrink(1).MinWidth(0).AlignItems(ui.Center)
		hovered := e.Hovered()
		if on {
			e.Background(pal.recessed)
		} else {
			hover(e, pal.hover)
		}
		return e.Children(func() { body(hovered) })
	}
	closeX := func(tip string) bool {
		return press(ui.Row(c).Size(18, 18).Radius(soft).Justify(ui.Center).Children(func() { ui.Text(c, "×").FontSize(14).TextColor(pal.meta) }).Tooltip(tip))
	}
	// ORB, first in the window's first tile as on the phone, is Home: a new session, the
	// conversations open, every thread.
	home := x.tab == ""
	// Tabs past the bar's room narrow, as a browser's, down to a few letters each; those that do not
	// fit then wait behind +N, which lists them. The one shown always shows.
	// The room is the bar's left once the readings and tools at its right have theirs, as last drawn.
	fit := len(x.tabs)
	if x.tabsW > 0 {
		fit = max(int((x.tabsW-36-map[bool]float32{true: 44}[a.left])/100), 1) // + and ORB keep their room
	}
	shown, hidden := x.tabs, []string(nil)
	if len(x.tabs) > fit {
		fit = max(fit-1, 1) // room for +N
		shown = slices.Clone(x.tabs[:fit])
		if at := slices.Index(x.tabs, x.tab); at >= fit {
			shown[fit-1] = x.tab
		}
		for _, id := range x.tabs {
			if !slices.Contains(shown, id) {
				hidden = append(hidden, id)
			}
		}
	}
	// ORB sits with the tabs, closer to them than to the traffic lights.
	ui.Row(c).Gap(2).Grow(1).Basis(0).MinWidth(0).Clip().AlignItems(ui.Center).DrawOver(func(_ *ui.Painter, r ui.Rect) { x.tabsW = r.W }).Children(func() {
		if a.left {
			e := chip("orb", home, func(bool) {
				stretch(c, "ORB", 15, map[bool]ui.Color{true: pal.fg, false: pal.meta}[home], 1)
			}).Padding(0, 6).Shrink(0).Tooltip("Home  " + keys("T"))
			if press(e) {
				a.activate(x)
				a.show("")
			}
		}
		for _, id := range shown {
			i := slices.Index(x.tabs, id)
			t := o.tab(id)
			if t == nil {
				continue
			}
			on := x.tab == id
			e := chip(id, on, func(hovered bool) {
				ink := pal.mute
				switch {
				case on:
					ink = pal.fg
				case t.Asleep:
					ink = pal.meta
				}
				// Its square pulses while it works or opens (its tip says which).
				where(c, o.hue(t.Peer), t.Busy || strings.TrimSpace(t.Status) != "", t.Ask != nil, ink)
				if !a.renameField(c, id) {
					w := 400
					if on || t.Unread > 0 {
						w = strong
					}
					ui.Text(c, cmp.Or(t.Title, "new session")).MaxWidth(170).Shrink(1).MinWidth(36).FontSize(small).FontWeight(w).TextColor(ink).SingleLine()
				}
				switch {
				case t.Ask != nil:
					ui.Text(c, "asks").FontSize(12).TextColor(rupture)
				case t.Unread > 0 && !on:
					ui.Text(c, "("+map[bool]string{true: "99+", false: strconv.Itoa(t.Unread)}[t.Unread > 99]+")").TextColor(pal.meta).FontSize(labelSize + 1).FontWeight(strong)
				}
				// Its × has its place always, as a browser's tab: shown on it or under the pointer.
				if on || hovered {
					if closeX("Close  " + keys("W") + " · it runs on") {
						a.close(id)
					}
				} else {
					ui.Box(c).Size(18, 18)
				}
			}).Tooltip(strings.Join(nonEmpty(t.Title, strings.Join(nonEmpty(t.Where, tidy(t.CWD)), " · "), t.Status), "\n"))
			a.carry(e, carried{tab: id})
			if over, ok := ui.DragOver[carried](e); ok && over.tab != "" && over.tab != id {
				from := slices.Index(x.tabs, over.tab)
				after := from >= 0 && from < i
				e.DrawOver(func(pt *ui.Painter, r ui.Rect) {
					lx := r.X
					if after {
						lx = r.X + r.W - 2
					}
					pt.Fill(ui.Rect{X: lx, Y: r.Y + 4, W: 2, H: r.H - 8}, pal.fg, 1)
				})
			}
			if v, ok := ui.Drop[carried](e); ok && v.tab != "" && v.tab != id {
				a.reorderTab(x, v.tab, i)
			}
			e.ContextMenu(func(m *ui.Menu) {
				if m.Item("Rename…").Chosen() {
					a.renameTab(*t)
				}
				if m.Item("Open to the right").Chosen() {
					a.place(x, "right", id, false)
				}
				if i > 0 && m.Item("Move left").Chosen() {
					a.reorderTab(x, id, i-1)
				}
				if i+1 < len(x.tabs) && m.Item("Move right").Chosen() {
					a.reorderTab(x, id, i+1)
				}
				if m.Item("Close").Shortcut(cmd, ui.KeyW).Chosen() {
					a.close(id)
				}
			})
			if press(e) {
				a.show(id)
			}
			if e.DoubleClicked() {
				a.renameTab(*t)
			}
		}
		if len(hidden) > 0 {
			more := ui.Row(c.Key("more")).Height(30).Padding(0, 9).Radius(7).Shrink(0).AlignItems(ui.Center).Children(func() {
				ui.Textf(c, "+%d", len(hidden)).FontSize(small).FontWeight(medium).TextColor(pal.mute)
			}).Tooltip("The other tabs of this tile")
			hover(more, pal.hover)
			more.Menu(func(m *ui.Menu) {
				for _, id := range hidden {
					if t := o.tab(id); t != nil && m.Item(cmp.Or(t.Title, "new session")).Chosen() {
						a.show(id)
					}
				}
			})
		}
		if x.tab == "" && !a.left {
			e := chip("home", true, func(hovered bool) {
				ui.Text(c, "new session").FontSize(small).FontWeight(strong).TextColor(pal.fg).SingleLine()
				if !a.root.leaf() && closeX("Close tile  "+keys("⇧W")) {
					a.unsplit(x)
				}
			})
			if press(e) {
				a.activate(x)
				a.focus++
			}
		}
		if x.tab != "" {
			plus := ui.Row(c.Key("+")).Size(28, 28).Radius(6).Justify(ui.Center).Shrink(0).Children(func() { ui.Text(c, "+").FontSize(18).TextColor(pal.mute) }).Tooltip("New tab  " + keys("T"))
			if press(hover(plus, pal.hover)) {
				a.activate(x)
				a.show("")
			}
		}
		// Dropping a tab on the empty end of the bar moves it to the last slot of this tile.
		tailDrop := ui.Box(c.Key("tab-tail")).Grow(1).Basis(0).Height(28)
		if over, ok := ui.DragOver[carried](tailDrop); ok && over.tab != "" {
			tailDrop.DrawOver(func(pt *ui.Painter, r ui.Rect) {
				pt.Fill(ui.Rect{X: r.X + 2, Y: r.Y + 4, W: 2, H: r.H - 8}, pal.fg, 1)
			})
		}
		if v, ok := ui.Drop[carried](tailDrop); ok && v.tab != "" {
			a.reorderTab(x, v.tab, len(x.tabs))
		}
	})
}

// reorderTab moves tabID to position to inside tile x (either reordering within x, or moving it
// from another tile into x at that exact slot) and persists the order when x is the sole tile.
func (a *app) reorderTab(x *tile, tabID string, to int) {
	if tabID == "" || x == nil {
		return
	}
	from := slices.Index(x.tabs, tabID)
	if from >= 0 {
		x.tabs = slices.Delete(x.tabs, from, from+1)
		if to > len(x.tabs) {
			to = len(x.tabs)
		}
		x.tabs = slices.Insert(x.tabs, max(0, to), tabID)
		x.tab = tabID
		a.activate(x)
		if a.o != nil {
			a.o.send("move", "tab", tabID, "to", max(0, to))
		}
		return
	}
	// Came from another tile: take ownership at the requested index.
	a.own(x, tabID)
	if cur := slices.Index(x.tabs, tabID); cur >= 0 {
		x.tabs = slices.Delete(x.tabs, cur, cur+1)
		if to > len(x.tabs) {
			to = len(x.tabs)
		}
		x.tabs = slices.Insert(x.tabs, max(0, to), tabID)
	}
	x.tab = tabID
	a.activate(x)
}

// menuMark is the menu at the window's top right, as the phone's: the conversation shown, the
// machine it runs on (this computer on Home), the Mitosis, Settings.
func (a *app) menuMark(c *ui.Context) {
	o, pal := a.o, p(c)
	t := o.tab(a.tab)
	peer := o.state.Self
	if t != nil {
		peer = t.Peer
	}
	e := ui.Row(c).Size(28, 28).Radius(6).Justify(ui.Center).Shrink(0).Children(func() {
		ui.Box(c).Size(14, 10).Draw(func(pt *ui.Painter, r ui.Rect) {
			for i := range 3 {
				pt.Fill(ui.Rect{X: r.X, Y: r.Y + float32(i)*4.4, W: r.W, H: 1.3}, pal.mute, 0.6)
			}
		})
	}).Tooltip("Menu")
	hover(e, pal.hover)
	e.Menu(func(m *ui.Menu) {
		if t != nil {
			if m.Item("Rename “" + cmp.Or(t.Title, "new session") + "”").Chosen() {
				a.renameTab(*t)
			}
			if m.Item("Terminal").Shortcut(cmd, ui.KeyJ).Chosen() {
				a.terminalOf(t.ID)
			}
			if m.Item("Close conversation").Shortcut(cmd, ui.KeyW).Chosen() {
				a.close(t.ID)
			}
			m.Separator()
		}
		if m.Item("Providers · " + a.machineName(peer)).Chosen() {
			reread()
			a.stack = []screen{{kind: "providers", peer: peer}}
		}
		if m.Item("Plugins · " + a.machineName(peer)).Chosen() {
			reread()
			a.stack = []screen{{kind: "plugins", peer: peer}}
		}
		if m.Item("A folder on " + a.machineName(peer) + "…").Chosen() {
			a.explore(peer)
		}
		m.Separator()
		if m.Item("Devices · " + o.state.Summary).Chosen() {
			reread()
			a.stack = []screen{{kind: "mitosis", peer: o.state.Self}}
		}
		if m.Item("Settings").Shortcut(cmd, ui.KeyComma).Chosen() {
			a.settings()
		}
	})
}

// renameField edits a row's name in place, the whole of it, when it is the one renamed: Return or
// leaving the field saves it, Escape leaves it as it was.
func (a *app) renameField(c *ui.Context, row string) bool {
	r := a.rename
	if r == nil || r.row != row {
		return false
	}
	in := ui.TextInputBase(c, &r.name).Grow(1).Basis(0).MinWidth(0).TextColor(p(c).fg).AutoFocus()
	if in.Submitted() || r.took && !in.Focused() {
		if name := strings.TrimSpace(r.name); name != "" {
			r.apply(name)
		}
		a.rename = nil
	}
	r.took = r.took || in.Focused()
	c.OnShortcut(0, ui.KeyEscape, func() { a.rename = nil })
	return true
}

// openPalette opens Go to (⌘K): every conversation open, every thread, a new session anywhere.
func (a *app) openPalette() {
	a.palette, a.search, a.pick, a.showArchived = true, "", 0, false
}

func (a *app) loadArchived() {
	a.archivedLoading = true
	then(a.o, func(r struct{ Entries []Entry }, err error) {
		a.archivedLoading = false
		if err != nil {
			a.say(err.Error())
			return
		}
		a.archived = r.Entries
	}, "threads.archived")
}

func (a *app) unarchiveThread(e Entry) {
	a.archived = slices.DeleteFunc(a.archived, func(x Entry) bool { return x.Key == e.Key })
	then(a.o, func(_ struct{}, err error) {
		if err != nil {
			a.say(err.Error())
			a.loadArchived()
		}
	}, "archive", "key", e.Key, "archived", false)
}

// hit is one line of Go to, and what choosing it does: here, or (⌘↩) in a tile at the right.
type hit struct {
	title, meta string
	score       int
	live, asks  bool
	archived    bool
	hue         int
	here, right func()
	thread      *Entry // a closed thread: its menu renames, archives/unarchives, or deletes it
}

func (a *app) paletteSheet(c *ui.Context) {
	o, pal := a.o, p(c)
	open := true
	q := strings.TrimSpace(a.search)
	match := func(fields ...string) (int, bool) {
		if q == "" {
			return 0, true
		}
		return fuzzyTextScore(strings.Join(fields, " "), q)
	}
	var hits []hit
	add := func(h hit, fields ...string) {
		if sc, ok := match(fields...); ok {
			h.score = sc
			hits = append(hits, h)
		}
	}
	for _, t := range o.state.Tabs {
		t := t
		add(hit{title: cmp.Or(t.Title, "new session"), meta: strings.Join(nonEmpty("open", map[bool]string{true: t.Where}[t.Remote]), " · "), live: t.Busy, asks: t.Ask != nil, hue: o.hue(t.Peer),
			here: func() { a.show(t.ID) }, right: func() { a.place(a.active, "right", t.ID, false) }}, t.Title, t.Where, t.CWD, "open")
	}
	// Quick workspace & model actions (aligned with Web CommandPalette).
	if len(o.state.Tabs) > 0 || a.overview {
		add(hit{title: "Overview · live agent grid", meta: keys("G") + " · pinch",
			here: func() { a.overview = !a.overview; a.pinchScale = 1 }, right: func() { a.overview = !a.overview; a.pinchScale = 1 }}, "overview grid all agents tabs pinch")
	}
	if cur := o.tab(a.tab); cur != nil {
		add(hit{title: "Rename session", meta: keys("R"), here: func() { a.renameTab(*cur) }, right: func() { a.renameTab(*cur) }}, "rename session", cur.Title)
	}
	add(hit{title: "Model & reasoning", meta: map[bool]string{true: "⌃M", false: "Ctrl+M"}[runtime.GOOS == "darwin"],
		here: func() { a.chooseModel(o.tab(a.tab), true) }, right: func() { a.chooseModel(o.tab(a.tab), true) }}, "model reasoning thinking switch")
	add(hit{title: "Cycle reasoning level", meta: "⇧Tab",
		here: a.cycleReasoning, right: a.cycleReasoning}, "cycle reasoning thinking effort")
	add(hit{title: "Toggle tool outputs", meta: map[bool]string{true: "⌃O", false: "Ctrl+O"}[runtime.GOOS == "darwin"],
		here: a.toggleToolOutputs, right: a.toggleToolOutputs}, "toggle expand collapse tool outputs")
	add(hit{title: "Split right", meta: keys("D"),
		here: func() { a.split(a.active, "right", &tile{}) }, right: func() { a.split(a.active, "right", &tile{}) }}, "split right tile pane")
	add(hit{title: "Split down", meta: keys("⇧D"),
		here: func() { a.split(a.active, "bottom", &tile{}) }, right: func() { a.split(a.active, "bottom", &tile{}) }}, "split down tile pane")
	if !a.root.leaf() {
		add(hit{title: "Even out tiles", meta: keys("⌃="),
			here: func() { a.root.even(true); a.reshape() }, right: func() { a.root.even(true); a.reshape() }}, "even equalize tiles split")
	}
	add(hit{title: "Settings", meta: keys(","), here: a.settings, right: a.settings}, "settings preferences providers plugins mitosis")

	for _, m := range o.home.Machines {
		m := m
		if m.Launch {
			add(hit{title: "New session on " + m.Name, hue: o.hue(m.ID),
				here: func() { a.start(m.ID, "") }, right: func() { a.start(m.ID, "") }}, "new session", m.Name)
			add(hit{title: "Browse folders on " + m.Name, hue: o.hue(m.ID),
				here: func() { a.exploring = m.ID }, right: func() { a.exploring = m.ID }}, "browse folders directory", m.Name)
		}
	}
	for _, e := range o.home.Entries {
		if len(hits) >= 80 && q == "" {
			break
		}
		if !e.Open {
			e := e
			meta := ago(e.Modified)
			mName := ""
			if m := o.machine(e.Machine); m != nil && !m.Self {
				mName = m.Name
				meta = m.Name + " · " + meta
			}
			add(hit{title: e.Title, meta: meta, live: e.Live, asks: e.Asks, hue: o.hue(e.Machine),
				here: func() { a.open(e.Key) }, right: func() { a.dropped(a.active, "right", carried{key: e.Key}) }, thread: &e}, e.Title, e.CWD, mName)
		}
	}
	if a.showArchived {
		for _, e := range a.archived {
			e := e
			meta := "archived"
			if age := ago(e.Modified); age != "" {
				meta = "archived · " + age
			}
			mName := ""
			if m := o.machine(e.Machine); m != nil && !m.Self {
				mName = m.Name
				meta = m.Name + " · " + meta
			}
			add(hit{title: e.Title, meta: meta, archived: true, hue: o.hue(e.Machine),
				here: func() { a.unarchiveThread(e) }, right: func() { a.unarchiveThread(e) }, thread: &e}, e.Title, e.CWD, mName, "archived")
		}
	}
	if q != "" {
		slices.SortStableFunc(hits, func(x, y hit) int { return y.score - x.score })
		if len(hits) > 60 {
			hits = hits[:60]
		}
	}
	a.pick = min(max(a.pick, 0), max(len(hits)-1, 0))
	choose := func(right bool) {
		if a.pick < len(hits) {
			a.palette = false
			if right {
				hits[a.pick].right()
			} else {
				hits[a.pick].here()
			}
		}
	}
	modal(c, &open, 600, func() {
		in := field(c, &a.search, "Go to a conversation, a thread, a new session…", false).Margin(12, 12, 6).AutoFocus()
		if in.Changed() {
			a.pick = 0
		}
		if in.Submitted() {
			choose(false)
		}
		c.OnShortcut(0, ui.KeyDown, func() { a.pick++ })
		c.OnShortcut(0, ui.KeyUp, func() { a.pick-- })
		c.OnShortcut(cmd, ui.KeyEnter, func() { choose(true) })
		ui.Scroll(c).FillWidth().MaxHeight(440).Shrink(1).Padding(0, 6, 8).Children(func() {
			if len(hits) == 0 {
				ui.Text(c, "Nothing matches “"+strings.TrimSpace(a.search)+"”").Padding(12, 10).FontSize(12).TextColor(pal.meta)
			}
			for i, h := range hits {
				row := ui.Row(c.Key(fmt.Sprint(i))).FillWidth().Padding(8, 10).Gap(10).Radius(8).Children(func() {
					where(c, h.hue, h.live, h.asks, pal.mute)
					title := ui.Text(c, h.title).Grow(1).Basis(0).SingleLine()
					if h.archived {
						title.TextColor(pal.meta)
					}
					ink := pal.meta
					if h.asks {
						ink = rupture
					}
					ui.Text(c, h.meta).FontSize(12).TextColor(ink).SingleLine()
				})
				if i == a.pick {
					row.Background(pal.raised)
				}
				if row.Hovered() {
					a.pick = i
				}
				if h.thread != nil {
					if h.archived {
						e := *h.thread
						row.ContextMenu(func(m *ui.Menu) {
							if m.Item("Unarchive").Chosen() {
								a.unarchiveThread(e)
							}
						})
					} else {
						row.ContextMenu(func(m *ui.Menu) { a.threadMenu(m, *h.thread) })
					}
				}
				if press(row) {
					choose(false)
				}
			}
		})
		ui.Row(c).FillWidth().Padding(6, 16, 10).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "↩ open · "+keys("↩")+" open at the right · esc close").Grow(1).FontSize(labelSize + 1).TextColor(pal.meta).SingleLine()
			label := "Show archived"
			if a.showArchived && a.archivedLoading {
				label = "Archived…"
			}
			chip := hover(ui.Row(c.Key("palette:archived")).Padding(2, 8).Radius(4).Border(1, pal.rule), pal.hover)
			if a.showArchived {
				chip.Background(pal.raised).Border(1, pal.meta)
			}
			chip.Children(func() {
				ink := pal.meta
				if a.showArchived {
					ink = pal.fg
				}
				ui.Text(c, label).FontSize(labelSize + 1).TextColor(ink).SingleLine()
			})
			if press(chip) {
				a.showArchived = !a.showArchived
				if a.showArchived {
					a.loadArchived()
				}
			}
		})
	})
	if !open {
		a.palette = false
	}
}

// magi is this computer's state put to three votes, as NERV's MAGI put each decision to their
// three computers: its Mitosis is up, its paired machines are connected, its Orb is current. Each
// says 承認 (approved) or 否決 (refused) in red; a refusal says why, and Mitosis's open its screen.
func (a *app) magi(c *ui.Context) {
	o, pal := a.o, p(c)
	var self *Machine
	peers, connected := 0, 0
	for i, m := range o.home.Machines {
		switch {
		case m.Self:
			self = &o.home.Machines[i]
		case m.Connected:
			peers, connected = peers+1, connected+1
		default:
			peers++
		}
	}
	version, current := "—", true
	if self != nil && self.Version != "" {
		version = self.Version
		current = o.state.Latest == "" || !newer(o.state.Latest, self.Version)
	}
	mitosisTip := "Mitosis is starting"
	if o.state.Up {
		mitosisTip = "Mitosis is up on this computer"
	}
	votes := []struct {
		name, value, tip string
		yes, mitosis     bool
	}{
		{"mitosis", map[bool]string{true: "up", false: "…"}[o.state.Up], mitosisTip, o.state.Up, true},
		{"peers", fmt.Sprintf("%d/%d", connected, peers), fmt.Sprintf("%d of %d paired machines connected", connected, peers), connected == peers, true},
		{"orb", version, map[bool]string{true: "This computer runs the newest Orb", false: "Orb " + o.state.Latest + " is out: orb update, then orb mitosis stop && orb mitosis start"}[current], current, false},
	}
	ui.Row(c).FillWidth().Gap(4).Children(func() {
		for _, v := range votes {
			ink, verdict := pal.mute, "承認"
			if !v.yes {
				ink, verdict = rupture, "否決"
			}
			cell := ui.Column(c.Key(v.name)).Grow(1).Basis(0).MinWidth(0).Padding(6, 8).Gap(2).Radius(4).Border(1, pal.rule.Alpha(0.8)).Tooltip(v.tip)
			cell.Children(func() {
				label(c, v.name)
				ui.Row(c).FillWidth().Gap(4).Children(func() {
					ui.Text(c, v.value).Grow(1).Basis(0).MinWidth(0).FontSize(12).TextColor(ink).SingleLine()
					ui.Text(c, verdict).FontSize(labelSize).FontWeight(strong).TextColor(ink)
				})
			})
			if v.mitosis && press(cell) {
				reread()
				a.stack = []screen{{kind: "mitosis"}}
			}
		}
	})
}
