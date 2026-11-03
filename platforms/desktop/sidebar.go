package main

import (
	"cmp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// sidebar is the conversations open, then each machine's threads, newest first, in sections that
// fold; at the foot, how things stand and the screens of settings. Selecting a thread opens it.
func (a *app) sidebar(c *ui.Context) {
	o, pal := a.o, p(c)
	tb := c.TitleBar()
	ui.Column(c).Fill().Background(pal.raised).Children(func() {
		ui.Row(c).FillWidth().Height(max(tb.Height, 48)).Padding(0, 10, 0, tb.Left+margin).DragWindow().Children(func() {
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
		field(c, &a.search, "Search threads", false).Margin(0, 10, 6).FontSize(small)
		ui.Scroll(c).Grow(1).Basis(0).FillWidth().Padding(4, 8, 12).Children(func() {
			q := strings.ToLower(strings.TrimSpace(a.search))
			if len(o.state.Tabs) > 0 && q == "" {
				sideLabel(c, "open", "")
				for i, t := range o.state.Tabs {
					a.tabRow(c.Key(t.ID), t, i)
				}
			}
			for _, m := range o.home.Machines {
				var threads []Entry
				for _, e := range o.home.Entries {
					if e.Machine == m.ID && !e.Open && (q == "" || strings.Contains(strings.ToLower(e.Title), q)) {
						threads = append(threads, e)
					}
				}
				if len(threads) == 0 && (m.Self || q != "") {
					continue
				}
				a.machineHeader(c.Key("m:"+m.ID), m)
				if a.fold[m.ID] {
					continue
				}
				for _, e := range threads[:min(len(threads), 60)] {
					a.threadRow(c.Key(e.Key), e)
				}
			}
		})
		a.foot(c)
	})
}

func sideLabel(c *ui.Context, name, state string) {
	ui.Row(c).FillWidth().Padding(14, 8, 4).Children(func() {
		label(c, name).Grow(1).TextColor(p(c).meta)
		ui.Text(c, state).FontSize(labelSize).TextColor(p(c).meta)
	})
}

// sideRow is one line of the sidebar: where it runs, its name, what it says at right.
func sideRow(c *ui.Context, on bool, body func()) ui.Element {
	pal := p(c)
	e := ui.Row(c).FillWidth().Padding(6, 8).Gap(9).Radius(soft)
	switch {
	case on:
		e.Background(pal.bg)
	case e.Hovered():
		e.Background(pal.bg.Alpha(0.6))
	}
	return e.Children(body)
}

func (a *app) tabRow(c *ui.Context, t Tab, at int) {
	o, pal := a.o, p(c)
	on := t.ID == a.tab && a.top().kind == ""
	e := sideRow(c, on, func() {
		where(c, o.hue(t.Peer), t.Busy, t.Ask != nil, pal.fg)
		w := 400
		if on || t.Unread > 0 {
			w = strong
		}
		ui.Text(c, cmp.Or(t.Title, "new session")).Grow(1).Basis(0).FontWeight(w).SingleLine()
		// Answers that came while it was out of sight, until it shows.
		if t.Unread > 0 {
			ui.Text(c, strconv.Itoa(t.Unread)).Padding(0, 6).Radius(8).Background(pal.fg).TextColor(pal.bg).FontSize(labelSize + 1).FontWeight(strong)
		} else if t.Remote {
			ui.Text(c, t.Where).MaxWidth(90).FontSize(12).TextColor(pal.meta).SingleLine()
		}
	}).Drag(t.ID).Tooltip(strings.Join(nonEmpty(t.Where, tidy(t.CWD)), " · "))
	e.ContextMenu(func(m *ui.Menu) {
		if m.Item("Rename…").Chosen() {
			a.renameTab(t)
		}
		if m.Item("Close").Shortcut(cmd, ui.KeyW).Chosen() {
			a.close(t.ID)
		}
	})
	// A tab dropped on another takes its place; the view keeps the order, for every app.
	if id, ok := ui.Drop[string](e); ok && id != t.ID {
		o.send("move", "tab", id, "to", at)
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
	}).Tooltip(tidy(e.CWD))
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("Rename…").Chosen() {
			a.renameThread(e)
		}
	})
	if press(row) {
		a.open(e.Key)
	}
}

// machineHeader names a machine in its hue; a click folds its threads, its menu reaches its folders,
// providers and plugins.
func (a *app) machineHeader(c *ui.Context, m Machine) {
	pal := p(c)
	e := ui.Row(c).FillWidth().Padding(14, 8, 4).Gap(8).Children(func() {
		dot(c, hue(c, a.o.hue(m.ID), pal.fg), 6, false, false)
		label(c, m.Name).Grow(1).Basis(0)
		mark := "⌄"
		if a.fold[m.ID] {
			mark = "›"
		}
		if !m.Connected && !m.Self {
			ui.Text(c, "offline").FontSize(labelSize).TextColor(pal.meta)
		}
		ui.Text(c, mark).FontSize(12).TextColor(pal.meta)
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
	ui.Column(c).FillWidth().Padding(8, 10, 10).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(pal.rule).Children(func() {
		a.update(c)
		ui.Row(c).FillWidth().Gap(8).PaddingX(6).Children(func() {
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
				e := ui.Row(c.Key(s.kind)).Padding(5, 7).Radius(soft).Children(func() {
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
					a.stack = []screen{{kind: s.kind, peer: o.state.Self}}
				}
			}
		})
	})
}
