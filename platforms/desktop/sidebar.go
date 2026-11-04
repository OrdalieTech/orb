package main

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// gutter is where the sidebar's grounds start (rows, fields, the MAGI); their text starts 8 in,
// where the wordmark's does.
const gutter = 12

// sidebar is the conversations open, then each machine's threads, newest first, in sections that
// fold; at the foot, how things stand and the screens of settings. Selecting a thread opens it.
func (a *app) sidebar(c *ui.Context) {
	o, pal := a.o, p(c)
	tb := c.TitleBar()
	ui.Column(c).Fill().Background(pal.raised).Children(func() {
		ui.Row(c).FillWidth().Height(max(tb.Height, 48)).Padding(0, 12, 0, tb.Left+gutter+8).DragWindow().Children(func() {
			ink := pal.meta
			if a.tab == "" && a.top().kind == "" {
				ink = pal.fg
			}
			if press(ui.Row(c).Children(func() { stretch(c, "ORB", 17, ink, 1) }).Tooltip("New session  " + keys("N"))) {
				a.show("")
			}
			ui.Spacer(c)
			if press(ui.Row(c).Padding(4, 8).Radius(soft).Children(func() { ui.Text(c, "+").FontSize(20).TextColor(pal.mute) }).Tooltip("New session  " + keys("N"))) {
				a.show("")
				a.focus++
			}
		})
		field(c, &a.search, "Search threads", false).Margin(0, gutter, 8).PaddingX(8).FontSize(small)
		ui.Scroll(c).Grow(1).Basis(0).FillWidth().Padding(4, gutter, 12).Children(func() {
			q := strings.ToLower(strings.TrimSpace(a.search))
			match := func(title string) bool { return q == "" || strings.Contains(strings.ToLower(title), q) }
			section := 0 // numbered, as a HUD numbers its panels
			for i, t := range o.state.Tabs {
				if !match(t.Title) {
					continue
				}
				if section == 0 {
					section++
					hudLabel(c, section, "open", "").Padding(14, 8, 4)
				}
				a.tabRow(c.Key(t.ID), t, i)
			}
			for _, m := range o.home.Machines {
				// The first 60 threads show; nothing is copied, as this runs each frame.
				threads := make([]*Entry, 0, 60)
				for i := range o.home.Entries {
					e := &o.home.Entries[i]
					if len(threads) < cap(threads) && e.Machine == m.ID && !e.Open && match(e.Title) {
						threads = append(threads, e)
					}
				}
				if len(threads) == 0 && (m.Self || q != "") {
					continue
				}
				section++
				a.machineHeader(c.Key("m:"+m.ID), m, section)
				if a.fold[m.ID] {
					continue
				}
				for _, e := range threads {
					a.threadRow(c.Key(e.Key), *e)
				}
			}
			if section == 0 && q != "" {
				ui.Text(c, "No thread matches “"+strings.TrimSpace(a.search)+"”").Padding(14, 8).FontSize(12).TextColor(pal.meta)
			}
		})
		a.foot(c)
	})
}

// sideRow is one line of the sidebar: where it runs, its name, what it says at right.
func sideRow(c *ui.Context, on bool, body func()) ui.Element {
	pal := p(c)
	e := ui.Row(c).FillWidth().Padding(6, 8).Gap(9).Radius(8)
	// A red tick at its left edge marks the row shown in the active tile: a HUD's target marker. It
	// grows from the middle in 120 ms.
	tick := e.AnimateTo("tick", map[bool]float32{true: 1, false: 0}[on], hovering)
	if tick > 0 {
		e.DrawOver(func(pt *ui.Painter, r ui.Rect) {
			h := (r.H - 12) * tick
			pt.Fill(ui.Rect{X: r.X, Y: r.Y + (r.H-h)/2, W: 2, H: h}, rupture, 1)
		})
	}
	if on {
		return e.Background(pal.bg).Children(body)
	}
	return hover(e, pal.bg.Alpha(0.6)).Children(body)
}

func (a *app) tabRow(c *ui.Context, t Tab, at int) {
	o, pal := a.o, p(c)
	on := t.ID == a.tab && a.top().kind == ""
	e := sideRow(c, on, func() {
		// A conversation in a tile reads in full ink, one out of sight muted, as threads are.
		ink := pal.mute
		if x := a.showing(t.ID); x != nil && (a.zoomed == nil || a.zoomed == x) {
			ink = pal.fg
		}
		where(c, o.hue(t.Peer), t.Busy, t.Ask != nil, ink)
		w := 400
		if on || t.Unread > 0 {
			w = strong
		}
		ui.Text(c, cmp.Or(t.Title, "new session")).Grow(1).Basis(0).FontWeight(w).TextColor(ink).SingleLine()
		// Answers that came while it was out of sight, until it shows.
		if t.Ask != nil {
			ui.Text(c, "asks").FontSize(12).TextColor(rupture)
		} else if t.Unread > 0 {
			ui.Text(c, map[bool]string{true: "99+", false: strconv.Itoa(t.Unread)}[t.Unread > 99]).Padding(0, 6).Radius(8).Background(pal.fg).TextColor(pal.bg).FontSize(labelSize + 1).FontWeight(strong)
		} else if t.Busy {
			ui.Text(c, "working").FontSize(12).TextColor(pal.meta)
		} else if t.Remote {
			ui.Text(c, t.Where).MaxWidth(90).FontSize(12).TextColor(pal.meta).SingleLine()
		}
	}).Tooltip(strings.Join(nonEmpty(t.Title, strings.Join(nonEmpty(t.Where, tidy(t.CWD)), " · ")), "\n"))
	a.carry(e, carried{tab: t.ID})
	e.ContextMenu(func(m *ui.Menu) {
		if m.Item("Open to the right").Chosen() {
			a.place(a.active, "right", t.ID, false)
		}
		if m.Item("Rename…").Chosen() {
			a.renameTab(t)
		}
		if m.Item("Close conversation").Chosen() {
			a.close(t.ID)
		}
	})
	// A tab dropped on another takes its place; the view keeps the order, for every app.
	if v, ok := ui.Drop[carried](e); ok && v.tab != "" && v.tab != t.ID {
		o.send("move", "tab", v.tab, "to", at)
	}
	if press(e) {
		a.show(t.ID)
	}
}

func (a *app) threadRow(c *ui.Context, e Entry) {
	pal := p(c)
	row := sideRow(c, false, func() {
		where(c, a.o.hue(e.Machine), e.Live, e.Asks, pal.mute)
		ink := pal.mute
		if e.Live {
			ink = pal.fg
		}
		ui.Text(c, e.Title).Grow(1).Basis(0).TextColor(ink).SingleLine()
		state, sink := ago(e.Modified), pal.meta
		switch {
		case e.Asks:
			state, sink = "asks", rupture
		case e.Live:
			state = "live"
		case e.Unstored:
			state = "open"
		}
		ui.Text(c, state).FontSize(12).TextColor(sink)
	}).Tooltip(strings.Join(nonEmpty(e.Title, tidy(e.CWD)), "\n"))
	a.carry(row, carried{key: e.Key})
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("Open to the right").Chosen() {
			a.dropped(a.active, "right", carried{key: e.Key})
		}
		if m.Item("Rename…").Chosen() {
			a.renameThread(e)
		}
	})
	if press(row) {
		a.open(e.Key)
	}
}

// machineHeader names a machine; a click folds its threads (its rows carry its hue), its menu reaches its folders,
// providers and plugins.
func (a *app) machineHeader(c *ui.Context, m Machine, index int) {
	pal := p(c)
	e := ui.Row(c).FillWidth().Padding(14, 8, 4).Gap(8).AlignItems(ui.Center).Children(func() {
		state := ""
		if !m.Connected && !m.Self {
			state = "offline"
		}
		hudLabel(c, index, m.Name, state).Grow(1).Basis(0)
		// Drawn, not a glyph: ⌄ and › sit on the baseline, below the label's centre.
		folded := a.fold[m.ID]
		ui.Box(c).Size(8, 8).Draw(func(pt *ui.Painter, r ui.Rect) {
			x, y := r.X+r.W/2, r.Y+r.H/2
			points := [][2]float32{{x - 3.5, y - 1.75}, {x, y + 1.75}, {x + 3.5, y - 1.75}}
			if folded {
				points = [][2]float32{{x - 1.75, y - 3.5}, {x + 1.75, y}, {x - 1.75, y + 3.5}}
			}
			pt.StrokePolyline(points, 1.2, pal.meta)
		})
	})
	e.ContextMenu(func(menu *ui.Menu) {
		if m.Launch && menu.Item("Folders…").Chosen() {
			a.push(screen{kind: "device", peer: m.ID})
		}
		if menu.Item("Providers").Chosen() {
			a.push(screen{kind: "providers", peer: m.ID})
		}
		if menu.Item("Plugins").Chosen() {
			a.push(screen{kind: "plugins", peer: m.ID})
		}
		if !m.Self && menu.Item("Rename…").Chosen() {
			a.rename = &renaming{name: m.Name, apply: func(name string) { a.o.send("rename", "machine", m.ID, "name", name) }}
		}
	})
	if press(e) {
		a.fold[m.ID] = !a.fold[m.ID]
	}
}

// foot is how things stand (a newer Orb, a question waiting, work going on) and the settings.
func (a *app) foot(c *ui.Context) {
	o, pal := a.o, p(c)
	ui.Column(c).FillWidth().Padding(12, gutter).Gap(8).Children(func() {
		a.magi(c)
		ui.Row(c).FillWidth().Gap(8).PaddingX(8).Children(func() {
			ink := pal.meta
			if strings.HasPrefix(o.state.Summary, "needs you") {
				ink = rupture
			}
			dot(c, map[bool]ui.Color{true: pal.fg, false: pal.rule}[o.state.Up], 6, !o.state.Up, false)
			ui.Text(c, cmp.Or(o.state.Summary, "starting Bridge…")).Grow(1).Basis(0).FontSize(12).TextColor(ink).SingleLine()
		})
		ui.Row(c).FillWidth().Gap(2).Children(func() {
			for _, s := range []struct{ kind, name, tip string }{{"providers", "providers", keys(",")}, {"plugins", "plugins", ""}, {"bridge", "bridge", ""}} {
				on := a.top().kind == s.kind
				e := ui.Row(c.Key(s.kind)).Padding(4, 8).Radius(soft).Children(func() {
					ink := pal.mute
					if on {
						ink = pal.fg
					}
					ui.Text(c, s.name).FontSize(12).FontWeight(medium).TextColor(ink)
				})
				if on {
					e.Background(pal.bg)
				} else if e.Hovered() {
					e.Background(pal.bg.Alpha(0.6))
				}
				if s.tip != "" {
					e.Tooltip(s.tip)
				}
				if press(e) {
					reread()
					a.stack = []screen{{kind: s.kind, peer: o.state.Self}}
				}
			}
		})
	})
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
