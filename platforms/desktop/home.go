package main

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// homeScreen starts a session: the prompt box in the middle of the window, where it runs a click
// away, the latest threads under it. Every thread is in the sidebar.
func (a *app) homeScreen(c *ui.Context) {
	o, pal := a.o, p(c)
	titleBar(c, func() {})
	ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).Padding(0, margin, 48).Children(func() {
		// An Evangelion title card, as the series sets one: the episode, the title compressed to fill
		// its frame, its name in Japanese under it.
		reading(c).MaxWidth(680).PaddingX(margin).Children(func() {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, map[bool]string{true: fmt.Sprintf("第%03d話", len(o.home.Entries)+1), false: "第———話"}[o.state.Up]).FontSize(13).FontWeight(strong).TextColor(rupture)
				ui.Box(c).Grow(1).Height(1).Background(pal.rule)
			})
			// The title wipes in the first time the app shows it, as an episode's card cuts in.
			card := stretch(c, "NEW SESSION", 112, pal.fg, 0).Margin(12, 0, 0)
			if !a.carded {
				v := reveal(card, "card", 60*time.Millisecond)
				wipe(card, v, pal.bg)
				a.carded = v == 1
			}
			ui.Row(c).FillWidth().Margin(12, 0, 0).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "新規セッション").FontSize(20).FontWeight(strong)
				ui.Box(c).Grow(1).Height(1).Background(pal.rule)
				ui.Box(c).Size(12, 12).Background(rupture)
			})
			a.launching(c)
		})
		ui.Box(c).Height(14)
		ui.Column(c).FillWidth().MaxWidth(680+2*overhang+2*margin).Margin(0, ui.Auto).Children(func() { a.promptBox(c, nil, "What should we work on?") })
		var recent []Entry
		for _, e := range o.home.Entries {
			if !e.Open && len(recent) < 5 {
				recent = append(recent, e)
			}
		}
		a.boot(c)
		if len(recent) > 0 && o.state.Up {
			reading(c).MaxWidth(680).Margin(18, ui.Auto, 0).PaddingX(margin).Children(func() {
				label(c, "latest").TextColor(pal.meta).Margin(0, 0, 4)
				for _, e := range recent {
					meta := ""
					if m := o.machine(e.Machine); m != nil && !m.Self {
						meta = m.Name
					}
					if press(sessionRow(c.Key(e.Key), e.Title, meta, ago(e.Modified), e.Live, e.Asks, false, o.hue(e.Machine))) {
						a.open(e.Key)
					}
				}
			})
		}
		if len(o.home.Machines) < 2 && o.state.Up {
			reading(c).MaxWidth(680).Margin(10, ui.Auto, 0).PaddingX(margin).Children(func() {
				row := ui.Row(c).FillWidth().PaddingY(12).Children(func() {
					ui.Text(c, "No paired devices").Grow(1).TextColor(pal.mute)
					ui.Text(c, "Pair ›").FontWeight(medium)
				})
				if press(row) {
					a.push(screen{kind: "bridge"})
				}
			})
		}
	})
}

// sessionRow is one line per session: its title, then where it lives and its condition in words
// (live, asking, or how long ago). No rules between rows: the space and the weight of the open one
// are enough.
func sessionRow(c *ui.Context, title, meta, age string, live, asks, current bool, hueN int) ui.Element {
	pal := p(c)
	e := ui.Row(c).FillWidth().Padding(9, 8).Margin(0, -8).Gap(10).Radius(soft)
	if e.Hovered() {
		e.Background(pal.raised)
	}
	return e.Children(func() {
		where(c, hueN, live, asks, pal.mute)
		w := 400
		if current || live {
			w = strong
		}
		ui.Text(c, title).Grow(1).Basis(0).FontSize(15).FontWeight(w).SingleLine()
		state := age
		switch {
		case asks:
			state = "asks"
		case live:
			state = "live"
		}
		ink := pal.meta
		if asks {
			ink = rupture
		}
		ui.Text(c, strings.Join(nonEmpty(meta, state), " · ")).FontSize(small).TextColor(ink).SingleLine()
	})
}

// launching is what starting Orb on a device is doing, on whichever screen started it.
func (a *app) launching(c *ui.Context) {
	text := a.o.state.Launching
	if text == "" {
		return
	}
	starting := strings.HasPrefix(text, "starting")
	ink := rupture
	if starting {
		ink = p(c).mute
	}
	ui.Row(c).FillWidth().PaddingY(8).Gap(10).Children(func() {
		dot(c, ink, 8, starting, false)
		ui.Text(c, text).Grow(1).Basis(0).FontSize(small).TextColor(ink).MaxLines(3)
	})
}

// tidy writes a path the way its owner reads it: /home/name or /Users/name as ~. Rows call it each
// frame, so it allocates only when it shortens a path.
func tidy(p string) string {
	for _, root := range []string{"/home/", "/Users/"} {
		if rest, ok := strings.CutPrefix(p, root); ok && rest != "" && rest[0] != '/' {
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				return "~" + rest[i:]
			}
			return "~"
		}
	}
	return p
}

// deviceScreen is one machine: its folders, most recently worked in first, and any other folder by path.
func (a *app) deviceScreen(c *ui.Context, peer string) {
	m := a.o.machine(peer)
	if m == nil {
		a.back()
		return
	}
	threads := 0
	for _, f := range m.Folders {
		threads += f.Threads
	}
	sub := fmt.Sprintf("%d folders · %d threads", len(m.Folders), threads)
	if m.Version != "" {
		sub += " · orb " + m.Version
	}
	page(c, func() {
		header(c, m.Name, sub, a.back, func() {
			if btn(c, "providers", false, p(c).fg, p(c).bg) {
				a.push(screen{kind: "providers", peer: m.ID})
			}
		})
	}, func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if field(c, &a.path, "another folder · ~/code/project", false).Grow(1).Submitted() || btn(c, "open", true, p(c).fg, p(c).bg) {
				if dir := strings.TrimSpace(a.path); dir != "" {
					a.path = ""
					a.push(screen{kind: "folder", peer: m.ID, arg: dir})
				}
			}
		})
		a.launching(c)
		label(c, "folders").Margin(18, 0, 4)
		for _, f := range m.Folders {
			name := path.Base(f.CWD)
			if press(sessionRow(c.Key(f.CWD), name, fmt.Sprint(f.Threads), ago(f.Modified), f.Live, false, false, a.o.hue(peer)).Tooltip(tidy(f.CWD))) {
				a.push(screen{kind: "folder", peer: m.ID, arg: f.CWD})
			}
		}
	})
}

// folderScreen is one folder on a machine: start a thread here, or open one of its threads.
func (a *app) folderScreen(c *ui.Context, peer, cwd string) {
	o, pal := a.o, p(c)
	m := o.machine(peer)
	if m == nil {
		a.back()
		return
	}
	var threads []Entry
	for _, e := range o.home.Entries {
		if e.Machine == peer && e.CWD == cwd && !e.Unstored {
			threads = append(threads, e)
		}
	}
	page(c, func() {
		header(c, path.Base(cwd), m.Name+" · "+tidy(cwd), a.back, nil)
	}, func() {
		ui.Column(c).FillWidth().Children(func() {
			if btn(c, "new thread here", true, pal.fg, pal.bg) {
				a.start(peer, cwd)
			}
		})
		a.launching(c)
		if len(threads) == 0 {
			label(c, "no threads here yet").TextColor(pal.meta).Margin(18, 0, 4)
		} else {
			label(c, "threads").Margin(18, 0, 4)
		}
		for _, e := range threads {
			meta := ""
			if e.Open {
				meta = "open"
			}
			row := sessionRow(c.Key(e.Key), e.Title, meta, ago(e.Modified), e.Live, e.Asks, false, o.hue(peer))
			row.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("Rename…").Chosen() {
					a.renameThread(e)
				}
			})
			if press(row) {
				a.open(e.Key)
			}
		}
	})
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// boot is what starting is doing, line by line as each part answers, as an EVA's entry plug reads
// out its start: shown while this computer's Bridge is not up yet, and only when that takes long
// enough to read (a Bridge already running answers before it would show).
func (a *app) boot(c *ui.Context) {
	o, pal := a.o, p(c)
	if o.state.Up {
		return
	}
	if wait := 300*time.Millisecond - time.Since(o.began); wait > 0 {
		c.After(wait)
		return
	}
	connected := 0
	for _, m := range o.home.Machines {
		if m.Connected && !m.Self {
			connected++
		}
	}
	type line struct{ step, jp, what string }
	lines := []line{{"orb app", "起動", "starting"}}
	if o.running != "" {
		lines[0].what = tidy(o.running)
		lines = append(lines, line{"bridge", "接続", "starting Bridge on this computer…"})
	}
	if len(o.home.Machines) > 0 {
		lines = append(lines, line{"peers", "同期", fmt.Sprintf("%d of %d connected", connected, len(o.home.Machines)-1)})
	}
	if s := o.state.Summary; strings.HasPrefix(s, "orb app ended") {
		lines = append(lines, line{"error", "否決", s})
	}
	reading(c).MaxWidth(680).Margin(18, ui.Auto, 0).PaddingX(margin).Gap(4).Children(func() {
		for _, l := range lines {
			ink := pal.mute
			if l.jp == "否決" {
				ink = rupture
			}
			ui.Row(c.Key(l.step)).FillWidth().Gap(12).Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 4}, Animation: &opening}).Children(func() {
				label(c, l.step).Width(64)
				ui.Text(c, l.jp).FontSize(12).TextColor(rupture).Width(28)
				ui.Text(c, l.what).Grow(1).Basis(0).MinWidth(0).FontSize(12).TextColor(ink).SingleLine()
			})
		}
	})
}
