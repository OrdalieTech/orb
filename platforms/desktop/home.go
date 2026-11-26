package main

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// homeScreen starts a session: the prompt box in the middle of the window, where it runs a click
// away, the latest threads under it. Every thread is a ⌘K away.
func (a *app) homeScreen(c *ui.Context) {
	a.titleBar(c, func() {})
	// A small tile keeps Home to its prompt box.
	small := a.narrow || a.building != nil && a.building.height > 0 && a.building.height < 480
	// Centred while it fits; with many cards open, it scrolls from the top.
	ui.Scroll(c.Key("in:home")).Grow(1).Basis(0).FillWidth().Transition(a.entering).Children(func() {
		ui.Column(c).FillWidth().MinHeightPercent(100).Justify(ui.Center).Padding(56, margin, 64).Children(func() { a.homeBody(c, small) })
	})
}

// homePlaceholder reflects where Home's next session will start: folder, remote peer, or default.
func (a *app) homePlaceholder() string {
	peer := cmp.Or(a.target[0], a.o.state.Self)
	m := a.o.machine(peer)
	remote := m != nil && !m.Self
	cwd := a.target[1]
	switch {
	case cwd != "" && remote:
		return fmt.Sprintf("Work in %s on %s…", path.Base(cwd), m.Name)
	case cwd != "":
		return fmt.Sprintf("Work in %s…", path.Base(cwd))
	case remote:
		return fmt.Sprintf("New session on %s…", m.Name)
	default:
		return "What should we work on?"
	}
}

// homeBody is Home under its bar: the title card, the prompt box, the cards open, the latest.
func (a *app) homeBody(c *ui.Context, small bool) {
	o, pal := a.o, p(c)
	{
		// An Evangelion title card: a rule, the title stretched tall.
		reading(c).MaxWidth(680).PaddingX(margin).Children(func() {
			if small {
				a.launching(c)
				return
			}
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
				// The title wipes in the first time the app shows it, as a title card cuts in.
				card := stretch(c, "NEW SESSION", 46, pal.fg, 1)
				if !a.carded {
					v := reveal(card, "card", 60*time.Millisecond)
					wipe(card, v, pal.bg)
					a.carded = v == 1
				}
			})
			a.launching(c)
		})
		ui.Box(c).Height(18)
		// Contextual peer (MAGI) + recent folder scope pills above the Home prompt box,
		// matching Android and Web: picking a peer or folder aims the prompt and filters threads.
		if !small {
			a.homeScopeBar(c)
		}
		// The box a little past the column (8 a side, 2.5% of it), so it stands out of the page
		// without leaving it; its own padding sets its text in.
		ui.Column(c).FillWidth().MaxWidth(680+16).Margin(0, ui.Auto).Children(func() { a.promptBox(c, nil, a.homePlaceholder()) })
		var recent []Entry
		for _, e := range o.home.Entries {
			if e.Open {
				continue
			}
			if a.deviceFilter != "" && e.Machine != a.deviceFilter {
				continue
			}
			if a.folderFilter != "" && e.CWD != a.folderFilter {
				continue
			}
			recent = append(recent, e)
			if len(recent) >= 6 {
				break
			}
		}
		a.boot(c)
		if len(o.state.Tabs) > 0 && !small {
			a.openCards(c)
		}
		if len(recent) > 0 && o.state.Up && !small {
			reading(c).MaxWidth(680).Margin(36, ui.Auto, 0).PaddingX(margin).Children(func() {
				ui.Row(c).FillWidth().Margin(0, 0, 6).AlignItems(ui.Center).Children(func() {
					hudLabel(c, 2, "latest", "").Grow(1)
					if link(c, "all threads", keys("K")) {
						a.openPalette()
					}
				})
				for _, e := range recent {
					meta := ""
					if m := o.machine(e.Machine); m != nil && !m.Self {
						meta = m.Name
					}
					row := sessionRow(c.Key(e.Key), e.Title, meta, ago(e.Modified), e.Live, e.Asks, false, o.hue(e.Machine))
					row.ContextMenu(func(m *ui.Menu) { a.threadMenu(m, e) })
					if press(row) {
						a.open(e.Key)
					}
				}
			})
		}
		if !small {
			reading(c).MaxWidth(680).Margin(32, ui.Auto, 0).PaddingX(margin).Children(func() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ink := pal.meta
					if strings.HasPrefix(o.state.Summary, "needs you") {
						ink = rupture
					}
					dot(c, map[bool]ui.Color{true: pal.fg, false: pal.rule}[o.state.Up], 6, !o.state.Up, false)
					ui.Text(c, cmp.Or(o.state.Summary, "starting Mitosis…")).Grow(1).Basis(0).FontSize(12).TextColor(ink).SingleLine()
					if link(c, "settings", keys(",")) {
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
					a.push(screen{kind: "mitosis"})
				}
			})
		}
	}
}

// openCards are the conversations open, as the phone's overview shows them: a card each, what it
// does and where, the last thing it said; a click shows it, × closes it (its Orb runs on).
func (a *app) openCards(c *ui.Context) {
	o, pal := a.o, p(c)
	// What needs a look first: what asks, works, has news; then what is awake. Six show, two
	// rows, unless all are asked for.
	tabs := slices.Clone(o.state.Tabs)
	slices.SortStableFunc(tabs, func(x, y Tab) int {
		rank := func(t Tab) int {
			switch {
			case t.Ask != nil:
				return 0
			case t.Busy:
				return 1
			case t.Unread > 0:
				return 2
			case !t.Asleep:
				return 3
			}
			return 4
		}
		return rank(x) - rank(y)
	})
	const few = 6
	if !a.allOpen && len(tabs) > few {
		tabs = tabs[:few]
	}
	reading(c).MaxWidth(680).Margin(36, ui.Auto, 0).PaddingX(margin).Children(func() {
		ui.Row(c).FillWidth().Margin(0, 0, 6).AlignItems(ui.Center).Children(func() {
			hudLabel(c, 1, fmt.Sprintf("open · %d", len(o.state.Tabs)), "").Grow(1)
			if len(o.state.Tabs) > few {
				more := map[bool]string{true: "fewer", false: fmt.Sprintf("all %d", len(o.state.Tabs))}[a.allOpen]
				if link(c, more, "") {
					a.allOpen = !a.allOpen
				}
			}
		})
		ui.Grid(c).FillWidth().Columns(map[bool]int{true: 2, false: 3}[a.narrow]).Gap(8).Children(func() {
			for _, t := range tabs {
				border := pal.rule.Alpha(0.55)
				if t.Ask != nil {
					border = rupture
				} else if t.Busy {
					border = pal.mute
				}
				card := ui.Column(c.Key(t.ID)).Height(78).Padding(9, 10).Gap(3).Radius(6).Background(pal.raised).Border(1, border).Clip()
				hovered := card.Hovered()
				hover(card, pal.hover)
				card.Children(func() {
					ui.Row(c).FillWidth().Gap(7).AlignItems(ui.Center).Children(func() {
						// Asleep, it recedes as the phone's cards do, its words still legible.
						where(c, o.hue(t.Peer), t.Busy, t.Ask != nil, map[bool]ui.Color{true: pal.rule, false: pal.mute}[t.Asleep])
						ui.Text(c, cmp.Or(t.Title, "new session")).Grow(1).Basis(0).MinWidth(0).FontSize(small).FontWeight(map[bool]int{true: medium, false: strong}[t.Asleep]).TextColor(map[bool]ui.Color{true: pal.mute, false: pal.fg}[t.Asleep]).SingleLine().Tooltip(cmp.Or(t.Title, "new session"))
						if hovered && press(ui.Row(c).Size(18, 18).Radius(4).Justify(ui.Center).Children(func() { ui.Text(c, "×").FontSize(14).TextColor(pal.meta) }).Tooltip("Close · it runs on")) {
							a.close(t.ID)
						}
					})
					state, ink := "", pal.meta
					switch {
					case t.Ask != nil:
						state, ink = "asks", rupture
					case t.Busy:
						state, ink = "working", pal.fg
					case strings.TrimSpace(t.Status) != "":
						state = strings.Split(t.Status, " · ")[0]
					case t.Unread > 0:
						state, ink = fmt.Sprintf("%d new", t.Unread), pal.fg
					case t.Asleep:
						state = "asleep"
					}
					ui.Text(c, strings.Join(nonEmpty(map[bool]string{true: t.Where}[t.Remote], tidy(t.CWD), state), " · ")).FontSize(12).TextColor(ink).SingleLine()
					if last := lastSaid(o.rows[t.ID]); last != "" {
						ui.Text(c, last).FontSize(12).TextColor(pal.mute).MaxLines(1)
					}
				})
				a.carry(card, carried{tab: t.ID})
				if press(card) {
					a.show(t.ID)
				}
			}
		})
	})
}

// lastSaid is the last line a conversation's rows say, for its card.
func lastSaid(rows []Row) string {
	for i := len(rows) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(rows[i].Text); t != "" {
			if j := strings.LastIndexByte(t, '\n'); j >= 0 {
				t = t[j+1:]
			}
			return t
		}
	}
	return ""
}

// sessionRow is one line per session: its title, then where it lives and its condition in words
// (live, asking, or how long ago). No rules between rows: the space and the weight of the open one
// are enough.
func sessionRow(c *ui.Context, title, meta, age string, live, asks, current bool, hueN int) ui.Element {
	pal := p(c)
	e := hover(ui.Row(c).AlignSelf(ui.Stretch).Padding(9, 8).Margin(0, -8).Gap(10).Radius(soft), pal.hover)
	return e.Children(func() {
		where(c, hueN, live, asks, pal.mute)
		w := 400
		if current || live {
			w = strong
		}
		ui.Text(c, title).Grow(1).Basis(0).FontSize(15).FontWeight(w).SingleLine().Tooltip(title)
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

// explorer chooses the folder a session starts in, on a machine: floating out of the prompt box's
// place (above or below it, as the room allows, never past the window), or a dialog over a screen.
// At its top the path, each folder of it a click back; then a filter that ↩ follows (or a path
// typed whole); the folders in it, git ones marked, a click going in, a double click starting
// there; its recent folders when it shows the home; start here at its foot.
func (a *app) explorer(c *ui.Context) {
	o, pal := a.o, p(c)
	peer := a.exploring
	m := o.machine(peer)
	open := m != nil
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
	body := func() {
		ui.Row(c).FillWidth().Padding(12, 12, 2, 8).Gap(2).AlignItems(ui.Center).Children(func() {
			// Back to the machines it was chosen among.
			if press(hover(ui.Row(c).Size(22, 22).Radius(soft).Justify(ui.Center).AlignItems(ui.Center), pal.hover).Children(func() {
				ui.Text(c, "‹").FontSize(16).TextColor(pal.mute)
			}).Tooltip("Back")) {
				a.exploring = ""
				a.chooseWhere()
			}
			where(c, o.hue(peer), false, false, pal.fg)
			label(c, m.Name).TextColor(pal.meta).MarginStart(4).MarginEnd(8).MaxWidth(120).Tooltip(m.Name)
			// Between the folders a / (none after the root, which is one); the last, where it is, in ink.
			cs := crumbs(d.Path)
			if len(cs) > 4 { // a deep path keeps where it starts and where it is
				cs = append(append(cs[:1:1], [2]string{"…", cs[len(cs)-3][1]}), cs[len(cs)-2:]...)
			}
			for i, cr := range cs {
				if i > 0 && cs[i-1][0] != "/" {
					ui.Text(c.Key("/"+cr[1]), "/").FontSize(12).TextColor(pal.rule)
				}
				ink := pal.mute
				if i == len(cs)-1 {
					ink = pal.fg
				}
				if press(ui.Row(c.Key(cr[1])).Padding(2, 4).Radius(5).Children(func() { ui.Text(c, cr[0]).FontSize(12).FontWeight(medium).TextColor(ink) })) {
					a.readDirs(peer, cr[1])
				}
			}
		})
		in := field(c, &a.path, "filter · or a path, ↩", false).Margin(6, 10, 4).FontSize(small).AutoFocus()
		if in.Submitted() {
			switch t := strings.TrimSpace(a.path); {
			case strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~"):
				a.readDirs(peer, t)
			case len(shown) > 0:
				a.readDirs(peer, path.Join(d.Path, shown[0]))
			}
		}
		ui.Scroll(c).FillWidth().MaxHeight(340).Shrink(1).Padding(2, 10, 4).Children(func() {
			if a.dirErr != "" {
				ui.Text(c, a.dirErr).FontSize(12).TextColor(rupture).Padding(6, 4)
			}
			if d.Parent != "" && q == "" {
				if press(dirRow(c.Key(".."), "..", false)) {
					a.readDirs(peer, d.Parent)
				}
			}
			for _, x := range d.Dirs {
				if q != "" && !strings.Contains(strings.ToLower(x.Name), q) {
					continue
				}
				at := path.Join(d.Path, x.Name)
				row := dirRow(c.Key(x.Name), x.Name, x.Git)
				if row.DoubleClicked() {
					a.exploring = ""
					a.start(peer, at)
				} else if press(row) {
					a.readDirs(peer, at)
				}
			}
			switch {
			case d.Path == "" && a.dirErr == "":
				empty(c, "reading…").PaddingX(8)
			case d.Path != "" && len(shown) == 0 && q != "":
				empty(c, "no folder here matches · ↩ opens a path").PaddingX(8)
			case d.Path != "" && len(d.Dirs) == 0:
				empty(c, "no folders in here").PaddingX(8)
			}
			if q == "" && d.Parent != "" && tidy(d.Path) == "~" && len(m.Folders) > 0 {
				label(c, "recent").Padding(12, 0, 2).TextColor(pal.meta)
				for i, f := range m.Folders {
					if i == 5 {
						break
					}
					if press(dirRow(c.Key("r:"+f.CWD), tidy(f.CWD), false)) {
						a.readDirs(peer, f.CWD)
					}
				}
			}
		})
		rule(c, pal.rule)
		ui.Row(c).FillWidth().Padding(8, 10, 10, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "a click opens a folder").Grow(1).Basis(0).MinWidth(0).FontSize(12).TextColor(pal.meta).SingleLine()
			if d.Path != "" && btn(c, "use this folder", true, pal.fg, pal.bg) {
				a.exploring = ""
				a.start(peer, d.Path)
			}
		})
	}
	if !open {
		a.exploring = ""
		return
	}
	if len(a.stack) > 0 || a.active == nil {
		modal(c, &open, 440, body)
	} else {
		// Out of the prompt box, its left edge on the box's, clear of it.
		popover(c, &a.active.boxAt, &open, 440, true, body)
	}
	if !open {
		a.exploring = ""
	}
}

// explore opens the explorer on a machine, at its home.
func (a *app) explore(peer string) { a.exploring, a.dirOf, a.dir = peer, "", Dirs{} }

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

// dirRow is a folder in the explorer, a line tight: its name, git when it is a repository.
func dirRow(c *ui.Context, name string, git bool) ui.Element {
	pal := p(c)
	e := hover(ui.Row(c).FillWidth().Height(28).Padding(0, 8).Gap(10).Radius(soft).AlignItems(ui.Center), pal.hover)
	return e.Children(func() {
		ui.Text(c, name).Grow(1).Basis(0).FontSize(small).SingleLine()
		if git {
			label(c, "git").TextColor(pal.meta)
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
// out its start: shown while this computer's Mitosis is not up yet, and only when that takes long
// enough to read (a Mitosis already running answers before it would show).
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
		lines = append(lines, line{"mitosis", "starting Mitosis on this computer…"})
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

// homeScopeBar shows the MAGI peer strip (when paired devices exist) and inline recent folder
// pills for the aimed peer. Clicking a peer or folder immediately aims Home's prompt box and
// narrows the latest threads list below.
func (a *app) homeScopeBar(c *ui.Context) {
	o, pal := a.o, p(c)
	machines := slices.Clone(o.home.Machines)
	slices.SortStableFunc(machines, func(x, y Machine) int {
		if x.Self != y.Self {
			if x.Self {
				return -1
			}
			return 1
		}
		return strings.Compare(x.Name, y.Name)
	})
	if len(machines) > 1 {
		var visible []Machine
		hiddenCount := 0
		for _, m := range machines {
			if a.hiddenPeers[m.ID] {
				hiddenCount++
				if !a.showHiddenPeers {
					continue
				}
			}
			visible = append(visible, m)
		}
		reading(c).MaxWidth(680).Margin(0, ui.Auto, 10).PaddingX(margin).Gap(6).Children(func() {
			if len(visible) > 0 {
				ui.Grid(c).FillWidth().Columns(map[bool]int{true: 2, false: 3}[a.narrow]).Gap(8).Children(func() {
					for _, m := range visible {
						m := m
						up := m.Connected
						if m.Self {
							up = o.state.Up
						}
						on := a.deviceFilter == m.ID
						hidden := a.hiddenPeers[m.ID]
						name := m.Name
						if m.Self {
							name = device
						}
						statusText := fmt.Sprintf("%d running", len(m.Running))
						if !up {
							statusText = map[bool]string{true: "starting…", false: "offline"}[m.Self]
						}
						border := pal.rule.Alpha(0.55)
						if on {
							border = pal.fg.Alpha(0.55)
						}
						card := ui.Column(c.Key("peer:"+m.ID)).FillWidth().Height(68).Padding(10, 12).Radius(8).
							Background(pal.raised).Border(1, border).Justify(ui.SpaceBetween)
						hover(card, pal.hover)
						card.ContextMenu(func(menu *ui.Menu) {
							if on {
								if menu.Item("Clear filter").Chosen() {
									a.deviceFilter, a.folderFilter = "", ""
									a.aimHome("", "")
								}
							} else {
								if menu.Item("Target for new sessions").Chosen() {
									a.deviceFilter, a.folderFilter = m.ID, ""
									if m.Self {
										a.aimHome("", "")
									} else {
										a.aimHome(m.ID, "")
									}
								}
							}
							if m.Launch {
								if menu.Item("Browse folders…").Chosen() {
									a.exploring = m.ID
								}
							}
							if menu.Item("Providers & accounts…").Chosen() {
								a.push(screen{kind: "providers", peer: m.ID})
							}
							if menu.Item("Plugins…").Chosen() {
								a.push(screen{kind: "plugins", peer: m.ID})
							}
							menu.Separator()
							if !m.Self {
								if menu.Item("Rename…").Chosen() {
									a.rename = &renaming{name: m.Name, apply: func(newName string) {
										o.send("rename", "machine", m.ID, "name", newName)
									}}
								}
							}
							hideLabel := "Hide peer"
							if hidden {
								hideLabel = "Unhide peer"
							}
							if menu.Item(hideLabel).Chosen() {
								if a.hiddenPeers == nil {
									a.hiddenPeers = map[string]bool{}
								}
								if hidden {
									delete(a.hiddenPeers, m.ID)
								} else {
									a.hiddenPeers[m.ID] = true
									if a.deviceFilter == m.ID {
										a.deviceFilter, a.folderFilter = "", ""
										a.aimHome("", "")
									}
								}
							}
							if !m.Self {
								if menu.Item("Forget device…").Chosen() {
									a.picker = &picker{
										title:   "Forget " + m.Name + "?",
										options: []string{"forget · revoke its access"},
										pick: func(string) {
											o.send("forget", "machine", m.ID)
										},
									}
								}
							}
						})
						card.Children(func() {
							ui.Row(c).FillWidth().Gap(7).AlignItems(ui.Center).Children(func() {
								where(c, o.hue(m.ID), len(m.Running) > 0, !up && !m.Self, map[bool]ui.Color{true: pal.fg, false: pal.mute}[on])
								ui.Text(c, name).Grow(1).Basis(0).MinWidth(0).FontSize(13).FontWeight(strong).TextColor(pal.fg).SingleLine()
								verdict, vInk := "承認", pal.meta
								if !up {
									verdict, vInk = "否決", rupture
								}
								ui.Text(c, verdict).FontSize(10.5).TextColor(vInk).SingleLine()
							})
							ui.Row(c).FillWidth().Gap(5).AlignItems(ui.Center).Children(func() {
								ui.Text(c, statusText).FontSize(11).TextColor(map[bool]ui.Color{true: pal.meta, false: rupture}[up]).SingleLine()
								if len(m.Folders) > 0 {
									fLabel := fmt.Sprintf("· %d folders", len(m.Folders))
									if len(m.Folders) == 1 {
										fLabel = "· 1 folder"
									}
									ui.Text(c, fLabel).FontSize(11).TextColor(pal.meta).SingleLine()
								}
								if hidden {
									ui.Text(c, "· hidden").FontSize(10.5).TextColor(pal.meta).SingleLine()
								}
							})
						})
						if press(card.Tooltip("Click to target " + name + " · right-click for options")) {
							if on {
								a.deviceFilter, a.folderFilter = "", ""
								a.aimHome("", "")
							} else {
								a.deviceFilter, a.folderFilter = m.ID, ""
								if m.Self {
									a.aimHome("", "")
								} else {
									a.aimHome(m.ID, "")
								}
							}
						}
					}
					pairCard := ui.Column(c.Key("peer:pair")).FillWidth().Height(68).Padding(10, 12).Radius(8).
						Border(1, pal.rule.Alpha(0.45)).Justify(ui.Center).AlignItems(ui.Center).Gap(2)
					hover(pairCard, pal.hover)
					pairCard.Children(func() {
						ui.Text(c, "+ pair device").FontSize(12.5).FontWeight(medium).TextColor(pal.mute).SingleLine()
						ui.Text(c, "invite · join").FontSize(10.5).TextColor(pal.meta).SingleLine()
					})
					if press(pairCard.Tooltip("Pair another computer with this Orb")) {
						a.picker = &picker{
							title:   "pair device",
							options: []string{"invite a device · QR & command", "join a device · paste code"},
							pick: func(s string) {
								if strings.HasPrefix(s, "invite") {
									a.push(screen{kind: "invite"})
								} else {
									a.push(screen{kind: "join"})
								}
							},
						}
					}
				})
			}
			if hiddenCount > 0 {
				ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
					toggleText := fmt.Sprintf("%d hidden · show", hiddenCount)
					if a.showHiddenPeers {
						toggleText = "hide hidden"
					}
					link := hover(ui.Row(c.Key("peers:hidden-toggle")).Padding(2, 6).Radius(4), pal.hover)
					link.Children(func() {
						ui.Text(c, toggleText).FontSize(11).TextColor(pal.meta).SingleLine()
					})
					if press(link) {
						a.showHiddenPeers = !a.showHiddenPeers
					}
				})
			}
		})
	}

	// Inline recent folder pills for the selected or aimed peer.
	scopePeer := cmp.Or(a.deviceFilter, a.target[0], o.state.Self)
	m := o.machine(scopePeer)
	if m == nil || (len(m.Folders) == 0 && !m.Launch) {
		return
	}
	folders := m.Folders
	if len(folders) > 8 {
		folders = folders[:8]
	}
	if len(folders) == 0 && a.deviceFilter == "" {
		return
	}
	reading(c).MaxWidth(680).Margin(0, ui.Auto, 10).PaddingX(margin).Children(func() {
		ui.ScrollHorizontal(c).FillWidth().Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				if len(folders) > 0 {
					allOn := a.folderFilter == ""
					allBtn := ui.Row(c.Key("dir:all")).Padding(3, 8).Radius(6)
					if allOn {
						allBtn.Background(pal.recessed)
					} else {
						hover(allBtn, pal.hover)
					}
					allBtn.Children(func() {
						ui.Text(c, "all").FontSize(11.5).FontWeight(map[bool]int{true: medium, false: 400}[allOn]).TextColor(map[bool]ui.Color{true: pal.fg, false: pal.meta}[allOn]).SingleLine()
					})
					if press(allBtn) {
						a.folderFilter = ""
						a.aimHome(map[bool]string{true: "", false: m.ID}[m.Self], "")
					}
				}
				for _, f := range folders {
					f := f
					on := a.folderFilter == f.CWD
					labelText := path.Base(f.CWD)
					if labelText == "." || labelText == "" {
						labelText = f.CWD
					}
					pill := ui.Row(c.Key("dir:"+f.CWD)).Padding(3, 8).Gap(5).Radius(6).AlignItems(ui.Center)
					if on {
						pill.Background(pal.recessed)
					} else {
						hover(pill, pal.hover)
					}
					pill.Children(func() {
						ui.Text(c, labelText).FontSize(11.5).FontWeight(map[bool]int{true: medium, false: 400}[on]).TextColor(map[bool]ui.Color{true: pal.fg, false: pal.mute}[on]).SingleLine()
						if f.Threads > 0 {
							ui.Text(c, fmt.Sprint(f.Threads)).FontSize(10.5).TextColor(pal.meta).SingleLine()
						}
					})
					if press(pill.Tooltip(tidy(f.CWD))) {
						if on {
							a.folderFilter = ""
							a.aimHome(map[bool]string{true: "", false: m.ID}[m.Self], "")
						} else {
							a.folderFilter = f.CWD
							a.aimHome(m.ID, f.CWD)
						}
					}
				}
				if m.Launch {
					browse := hover(ui.Row(c.Key("dir:browse")).Padding(3, 8).Radius(6), pal.hover)
					browse.Children(func() {
						ui.Text(c, "browse ›").FontSize(11.5).TextColor(pal.meta).SingleLine()
					})
					if press(browse) {
						a.exploring = m.ID
					}
				}
			})
		})
	})
}
