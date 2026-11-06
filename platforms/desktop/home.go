package main

import (
	"cmp"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// homeScreen starts a session: the prompt box in the middle of the window, where it runs a click
// away, the latest threads under it. Every thread is a ⌘K away.
func (a *app) homeScreen(c *ui.Context) {
	o, pal := a.o, p(c)
	a.titleBar(c, func() { ui.Spacer(c) })
	// A small tile keeps Home to its prompt box.
	small := a.narrow || a.building != nil && a.building.height > 0 && a.building.height < 480
	ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).Padding(0, margin, 48).Children(func() {
		// An Evangelion title card: a rule, the title stretched tall.
		reading(c).MaxWidth(680).PaddingX(margin).Children(func() {
			if small {
				a.launching(c)
				return
			}
			ui.Box(c).FillWidth().Height(1).Background(pal.rule)
			ui.Row(c).FillWidth().Margin(12, 0, 0).Gap(10).AlignItems(ui.End).Children(func() {
				// The title wipes in the first time the app shows it, as a title card cuts in.
				card := stretch(c, "NEW SESSION", 46, pal.fg, 1)
				if !a.carded {
					v := reveal(card, "card", 60*time.Millisecond)
					wipe(card, v, pal.bg)
					a.carded = v == 1
				}
				ui.Box(c).Size(9, 9).Background(rupture).Margin(0, 0, 3)
			})
			a.launching(c)
		})
		ui.Box(c).Height(18)
		ui.Column(c).FillWidth().MaxWidth(680+2*overhang+2*margin).Margin(0, ui.Auto).Children(func() { a.promptBox(c, nil, "What should we work on?") })
		var recent []Entry
		for _, e := range o.home.Entries {
			if !e.Open && len(recent) < 8 {
				recent = append(recent, e)
			}
		}
		a.boot(c)
		if len(recent) > 0 && o.state.Up && !small {
			reading(c).MaxWidth(680).Margin(18, ui.Auto, 0).PaddingX(margin).Children(func() {
				ui.Row(c).FillWidth().Margin(0, 0, 4).Children(func() {
					label(c, "latest").TextColor(pal.meta).Grow(1)
					if press(ui.Row(c).Children(func() { label(c, "all threads  "+keys("K")).TextColor(pal.mute) })) {
						a.openPalette()
					}
				})
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
		if !small {
			reading(c).MaxWidth(680).Margin(14, ui.Auto, 0).PaddingX(margin).Children(func() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ink := pal.meta
					if strings.HasPrefix(o.state.Summary, "needs you") {
						ink = rupture
					}
					dot(c, map[bool]ui.Color{true: pal.fg, false: pal.rule}[o.state.Up], 6, !o.state.Up, false)
					ui.Text(c, cmp.Or(o.state.Summary, "starting Bridge…")).Grow(1).Basis(0).FontSize(12).TextColor(ink).SingleLine()
					if press(ui.Row(c).Children(func() { label(c, "settings  "+keys(",")).TextColor(pal.mute) })) {
						a.settings()
					}
				})
			})
		}
		if len(o.home.Machines) < 2 && o.state.Up && !small {
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

// deviceScreen is one machine as an explorer: where it is (each folder of the path a click
// back), the folders in it (git ones marked; a click goes in, start begins a session there), a
// filter that ↩ follows, or a path typed whole; its recent folders under.
func (a *app) deviceScreen(c *ui.Context, peer string) {
	o, pal := a.o, p(c)
	m := o.machine(peer)
	if m == nil {
		a.back()
		return
	}
	if a.dirOf != peer {
		a.readDirs(peer, "")
	}
	d := a.dir
	q := strings.ToLower(strings.TrimSpace(a.path))
	var shown []string
	for _, x := range d.Dirs {
		if q == "" || strings.Contains(strings.ToLower(x.Name), q) {
			shown = append(shown, x.Name)
		}
	}
	page(c, func() {
		header(c, m.Name, strings.Join(nonEmpty(map[bool]string{true: "orb " + m.Version}[m.Version != ""], "choose where the session starts"), " · "), a.back, func() {
			if d.Path != "" && btn(c, "start here", true, pal.fg, pal.bg) {
				a.start(peer, d.Path)
			}
		})
		// The path, each of its folders a click back.
		ui.Row(c).FillWidth().Padding(0, margin, 6).Gap(2).Children(func() {
			for _, cr := range crumbs(d.Path) {
				if press(ui.Row(c.Key(cr[1])).Padding(3, 6).Radius(5).Children(func() { ui.Text(c, cr[0]).FontSize(small).FontWeight(medium).TextColor(pal.mute) })) {
					a.readDirs(peer, cr[1])
				}
				if cr[0] != "/" {
					ui.Text(c.Key("/"+cr[1]), "/").FontSize(small).TextColor(pal.rule)
				}
			}
		})
		in := field(c, &a.path, "filter · or a path, ↩", false).Margin(0, margin, 4).AutoFocus()
		if in.Submitted() {
			switch t := strings.TrimSpace(a.path); {
			case strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~"):
				a.readDirs(peer, t)
			case len(shown) > 0:
				a.readDirs(peer, path.Join(d.Path, shown[0]))
			}
		}
	}, func() {
		a.launching(c)
		if a.dirErr != "" {
			ui.Text(c, a.dirErr).FontSize(small).TextColor(rupture).PaddingY(6)
		}
		ui.Column(c).FillWidth().Children(func() {
			if d.Parent != "" && q == "" {
				if press(dirRow(c.Key(".."), "..", false, "")) {
					a.readDirs(peer, d.Parent)
				}
			}
			for _, x := range d.Dirs {
				if q != "" && !strings.Contains(strings.ToLower(x.Name), q) {
					continue
				}
				at := path.Join(d.Path, x.Name)
				row := dirRow(c.Key(x.Name), x.Name, x.Git, "start ›")
				if row.DoubleClicked() {
					a.start(peer, at)
				} else if press(row) {
					a.readDirs(peer, at)
				}
			}
		})
		if len(m.Folders) > 0 && q == "" {
			label(c, "recent").Margin(18, 0, 4).TextColor(pal.meta)
			for i, f := range m.Folders {
				if i == 6 {
					break
				}
				if press(sessionRow(c.Key("r:"+f.CWD), path.Base(f.CWD), fmt.Sprint(f.Threads), ago(f.Modified), f.Live, false, false, o.hue(peer)).Tooltip(tidy(f.CWD))) {
					a.push(screen{kind: "folder", peer: m.ID, arg: f.CWD})
				}
			}
		}
	})
}

// readDirs lists a folder of a machine, its home for "", into the explorer.
func (a *app) readDirs(peer, dir string) {
	a.dirOf, a.path, a.dirErr = peer, "", ""
	then(a.o, func(r Dirs, err error) {
		if a.dirOf != peer {
			return
		}
		if err != nil {
			a.dirErr = err.Error()
			return
		}
		a.dir = r
	}, "dirs", "machine", peer, "cwd", dir)
}

// dirRow is a folder in the explorer, a line tight: its name, git when it is a repository, and
// under the pointer what a double click does.
func dirRow(c *ui.Context, name string, git bool, hint string) ui.Element {
	pal := p(c)
	e := ui.Row(c).FillWidth().Height(30).Padding(0, 8).Margin(0, -8).Gap(10).Radius(6).AlignItems(ui.Center)
	hovered := e.Hovered()
	if hovered {
		e.Background(pal.raised)
	}
	return e.Children(func() {
		ui.Text(c, name).Grow(1).Basis(0).FontSize(small).SingleLine()
		if git {
			label(c, "git").TextColor(pal.meta)
		}
		if hovered && hint != "" {
			ui.Text(c, "double click · "+hint).FontSize(12).TextColor(pal.meta)
		}
	})
}

// crumbs are a path's folders, as their names (the home as ~) and their paths.
func crumbs(p string) [][2]string {
	if p == "" {
		return nil
	}
	out := [][2]string{{"/", "/"}}
	rest := strings.Trim(p, "/")
	at := ""
	if t := tidy(p); strings.HasPrefix(t, "~") {
		at = p[:len(p)-len(t)+1]
		out = [][2]string{{"~", at}}
		rest = strings.Trim(t[1:], "/")
	}
	if rest == "" {
		return out
	}
	for _, name := range strings.Split(rest, "/") {
		at += "/" + name
		out = append(out, [2]string{name, at})
	}
	return out
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
			row.ContextMenu(func(menu *ui.Menu) { a.threadMenu(menu, e) })
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
	type line struct{ step, what string }
	lines := []line{{"orb app", "starting"}}
	if o.running != "" {
		lines[0].what = tidy(o.running)
		lines = append(lines, line{"bridge", "starting Bridge on this computer…"})
	}
	if len(o.home.Machines) > 0 {
		lines = append(lines, line{"peers", fmt.Sprintf("%d of %d connected", connected, len(o.home.Machines)-1)})
	}
	if s := o.state.Summary; strings.HasPrefix(s, "orb app ended") {
		lines = append(lines, line{"error", s})
	}
	reading(c).MaxWidth(680).Margin(18, ui.Auto, 0).PaddingX(margin).Gap(4).Children(func() {
		for _, l := range lines {
			ink := pal.mute
			if l.step == "error" {
				ink = rupture
			}
			ui.Row(c.Key(l.step)).FillWidth().Gap(12).Transition(ui.ElementTransition{Enter: &ui.Motion{Y: 4}, Animation: &opening}).Children(func() {
				label(c, l.step).Width(64)
				ui.Text(c, l.what).Grow(1).Basis(0).MinWidth(0).FontSize(12).TextColor(ink).SingleLine()
			})
		}
	})
}
