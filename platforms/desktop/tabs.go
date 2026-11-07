package main

import (
	"cmp"
	"fmt"
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
		e := ui.Row(c.Key(key)).Height(30).Padding(0, 6, 0, 10).Gap(7).Radius(7).Shrink(1).MinWidth(96).AlignItems(ui.Center)
		hovered := e.Hovered()
		if on {
			e.Background(pal.raised)
		} else {
			hover(e, pal.raised.Alpha(0.6))
		}
		return e.Children(func() { body(hovered) })
	}
	closeX := func(tip string) bool {
		return press(ui.Row(c).Size(18, 18).Radius(4).Justify(ui.Center).Children(func() { ui.Text(c, "×").FontSize(14).TextColor(pal.meta) }).Tooltip(tip))
	}
	// ORB, first in the window's first tile as on the phone, is Home: a new session, the
	// conversations open, every thread.
	if a.left {
		ink := pal.meta
		if x.tab == "" {
			ink = pal.fg
		}
		if press(ui.Row(c).Height(30).Padding(0, 8).Radius(7).Shrink(0).AlignItems(ui.Center).Children(func() { stretch(c, "ORB", 15, ink, 1) }).Tooltip("Home  " + keys("T"))) {
			a.activate(x)
			a.show("")
		}
	}
	// Tabs past the bar's room narrow, as a browser's, their names cut; a drag takes any of them.
	ui.Row(c).Gap(2).Shrink(1).MinWidth(0).Clip().AlignItems(ui.Center).Children(func() {
		for i, id := range x.tabs {
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
					ui.Text(c, cmp.Or(t.Title, "new session")).MaxWidth(170).Shrink(1).MinWidth(0).FontSize(small).FontWeight(w).TextColor(ink).SingleLine()
				}
				switch {
				case t.Ask != nil:
					ui.Text(c, "asks").FontSize(12).TextColor(rupture)
				case t.Unread > 0 && !on:
					ui.Text(c, map[bool]string{true: "99+", false: strconv.Itoa(t.Unread)}[t.Unread > 99]).Padding(0, 5).Radius(7).Background(pal.fg).TextColor(pal.bg).FontSize(labelSize + 1).FontWeight(strong)
				}
				if (on || hovered) && closeX("Close  "+keys("W")+" · it runs on") {
					a.close(id)
				}
			}).Tooltip(strings.Join(nonEmpty(t.Title, strings.Join(nonEmpty(t.Where, tidy(t.CWD)), " · "), t.Status), "\n"))
			a.carry(e, carried{tab: id})
			e.ContextMenu(func(m *ui.Menu) {
				if m.Item("Rename…").Chosen() {
					a.renameTab(*t)
				}
				if m.Item("Open to the right").Chosen() {
					a.place(x, "right", id, false)
				}
				if i > 0 && m.Item("Move left").Chosen() {
					x.tabs[i-1], x.tabs[i] = x.tabs[i], x.tabs[i-1]
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
	})
	if x.tab != "" && !a.left && press(ui.Row(c).Size(26, 26).Radius(6).Justify(ui.Center).Shrink(0).Children(func() { ui.Text(c, "+").FontSize(18).TextColor(pal.mute) }).Tooltip("New tab  "+keys("T"))) {
		a.activate(x)
		a.show("")
	}
}

// menuMark is the menu at the window's top right, as the phone's: the conversation shown, the
// machine it runs on (this computer on Home), the Bridge, Settings.
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
	hover(e, pal.raised)
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
			a.stack = []screen{{kind: "bridge", peer: o.state.Self}}
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
func (a *app) openPalette() { a.palette, a.search, a.pick = true, "", 0 }

// hit is one line of Go to, and what choosing it does: here, or (⌘↩) in a tile at the right.
type hit struct {
	title, meta string
	live, asks  bool
	hue         int
	here, right func()
}

func (a *app) paletteSheet(c *ui.Context) {
	o, pal := a.o, p(c)
	open := true
	q := strings.ToLower(strings.TrimSpace(a.search))
	has := func(s ...string) bool { return q == "" || strings.Contains(strings.ToLower(strings.Join(s, " ")), q) }
	var hits []hit
	for _, t := range o.state.Tabs {
		if has(t.Title, t.Where, t.CWD) {
			hits = append(hits, hit{title: cmp.Or(t.Title, "new session"), meta: strings.Join(nonEmpty("open", map[bool]string{true: t.Where}[t.Remote]), " · "), live: t.Busy, asks: t.Ask != nil, hue: o.hue(t.Peer),
				here: func() { a.show(t.ID) }, right: func() { a.place(a.active, "right", t.ID, false) }})
		}
	}
	for _, m := range o.home.Machines {
		if m.Launch && has("new session", m.Name) {
			hits = append(hits, hit{title: "new session on " + m.Name, hue: o.hue(m.ID),
				here: func() { a.start(m.ID, "") }, right: func() { a.start(m.ID, "") }})
		}
	}
	for _, e := range o.home.Entries {
		if len(hits) >= 60 {
			break
		}
		if !e.Open && has(e.Title, e.CWD) {
			meta := ago(e.Modified)
			if m := o.machine(e.Machine); m != nil && !m.Self {
				meta = m.Name + " · " + meta
			}
			hits = append(hits, hit{title: e.Title, meta: meta, live: e.Live, asks: e.Asks, hue: o.hue(e.Machine),
				here: func() { a.open(e.Key) }, right: func() { a.dropped(a.active, "right", carried{key: e.Key}) }})
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
					ui.Text(c, h.title).Grow(1).Basis(0).SingleLine()
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
				if press(row) {
					choose(false)
				}
			}
		})
		ui.Text(c, "↩ open · "+keys("↩")+" open at the right · esc close").Padding(6, 16, 10).FontSize(labelSize + 1).TextColor(pal.meta)
	})
	if !open {
		a.palette = false
	}
}

// magi is this computer's state put to three votes, as NERV's MAGI put each decision to their
// three computers: its Bridge is up, its paired machines are connected, its Orb is current. Each
// says 承認 (approved) or 否決 (refused) in red; a refusal says why, and Bridge's open its screen.
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
	bridgeTip := "Bridge is starting"
	if o.state.Up {
		bridgeTip = "Bridge is up on this computer"
	}
	votes := []struct {
		name, value, tip string
		yes, bridge      bool
	}{
		{"bridge", map[bool]string{true: "up", false: "…"}[o.state.Up], bridgeTip, o.state.Up, true},
		{"peers", fmt.Sprintf("%d/%d", connected, peers), fmt.Sprintf("%d of %d paired machines connected", connected, peers), connected == peers, true},
		{"orb", version, map[bool]string{true: "This computer runs the newest Orb", false: "Orb " + o.state.Latest + " is out: orb update, then orb bridge stop && orb bridge start"}[current], current, false},
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
			if v.bridge && press(cell) {
				reread()
				a.stack = []screen{{kind: "bridge"}}
			}
		}
	})
}
