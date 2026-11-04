package main

import "github.com/egoist/mygo/ui"

// tile is a part of the window, as a tiling window manager cuts it: two tiles, side by side or one
// above the other, or a tile showing a conversation (Home for none) or the terminal where one runs.
// Both hands work it: the keys (⌘D, ⌘⇧D, ⌘W, ⌘[ ⌘], ⌘=, ⌘J) and the pointer (a thread or a tile
// dropped on a tile's side or middle, dividers dragged, a tile's bar and its menu).
type tile struct {
	a, b  *tile
	below bool    // b under a rather than beside it
	share float32 // a's percentage of the room

	tab     string // a tile's conversation, "" for Home
	shell   bool   // the tile shows tab's terminal
	at      ui.Handle
	modelAt ui.Handle // its prompt box's model and place, which their popovers open out of
	whereAt ui.Handle
	over    string  // the side a drag over it would drop on
	width   float32 // as last drawn
}

// carried is what a drag carries: an open conversation, a thread to open, or a tile to move.
type carried struct {
	tab, key string
	tile     *tile
}

func (p *tile) leaf() bool { return p.a == nil }

// tiles lists the tiles under p, in reading order.
func (p *tile) tiles(out []*tile) []*tile {
	if p.leaf() {
		return append(out, p)
	}
	return p.b.tiles(p.a.tiles(out))
}

// count is how many tiles p lines up in the direction of a split below or beside.
func (p *tile) count(below bool) int {
	if p.leaf() || p.below != below {
		return 1
	}
	return p.a.count(below) + p.b.count(below)
}

// even shares the room so that the tiles in a line are as wide (or as tall) as each other: in
// every line under p with all, else in p's own.
func (p *tile) even(all bool) {
	if p.leaf() {
		return
	}
	n, m := p.a.count(p.below), p.b.count(p.below)
	p.share = 100 * float32(n) / float32(n+m)
	for _, k := range []*tile{p.a, p.b} {
		if all || !k.leaf() && k.below == p.below {
			k.even(all)
		}
	}
}

// parent is the split holding x, nil for the root.
func (a *app) parent(x *tile) *tile {
	var find func(p *tile) *tile
	find = func(p *tile) *tile {
		if p.leaf() {
			return nil
		}
		if p.a == x || p.b == x {
			return p
		}
		if f := find(p.a); f != nil {
			return f
		}
		return find(p.b)
	}
	return find(a.root)
}

// replace puts n where x is in the layout.
func (a *app) replace(x, n *tile) {
	switch p := a.parent(x); {
	case p == nil:
		a.root = n
	case p.a == x:
		p.a = n
	default:
		p.b = n
	}
}

// split puts n on a side of x (left, right, top, bottom), sharing x's room evenly, and focuses it.
func (a *app) split(x *tile, side string, n *tile) {
	s := &tile{a: x, b: n, below: side == "top" || side == "bottom", share: 50}
	if side == "left" || side == "top" {
		s.a, s.b = n, x
	}
	a.replace(x, s)
	// A new tile takes its share of its line, as in a tiling window manager.
	line := s
	for p := a.parent(line); p != nil && p.below == s.below; p = a.parent(p) {
		line = p
	}
	line.even(false)
	a.activate(n)
}

// unsplit takes x out of the layout, its sibling taking its room; the last tile turns Home.
func (a *app) unsplit(x *tile) {
	p := a.parent(x)
	if p == nil {
		x.tab, x.shell = "", false
		a.activate(x)
		return
	}
	other := p.a
	if other == x {
		other = p.b
	}
	a.replace(p, other)
	if a.active == x {
		a.activate(other.tiles(nil)[0])
	}
}

// activate makes x the tile the keys and the sidebar act on.
func (a *app) activate(x *tile) {
	a.active = x
	a.tab = x.tab
}

// showing is the tile showing a conversation (not its terminal), nil when none does.
func (a *app) showing(tab string) *tile {
	for _, t := range a.root.tiles(nil) {
		if t.tab == tab && !t.shell {
			return t
		}
	}
	return nil
}

// place shows tab in x, or (side not "middle") in a new tile on that side of x. A conversation
// shows once: a tile already showing it gives way.
func (a *app) place(x *tile, side, tab string, shell bool) {
	if tab != "" {
		for _, t := range a.root.tiles(nil) {
			if t != x && t.tab == tab && t.shell == shell {
				a.unsplit(t)
			}
		}
	}
	if side == "middle" {
		x.tab, x.shell = tab, shell
		a.activate(x)
		return
	}
	a.split(x, side, &tile{tab: tab, shell: shell})
}

// terminalOf toggles the tile of tab's terminal, under the tile showing it (⌘J, >_).
func (a *app) terminalOf(tab string) {
	for _, t := range a.root.tiles(nil) {
		if t.tab == tab && t.shell {
			a.unsplit(t)
			return
		}
	}
	x := a.showing(tab)
	if x == nil {
		x = a.active
	}
	s := &tile{a: x, b: &tile{tab: tab, shell: true}, below: true, share: 65}
	a.replace(x, s)
	a.activate(s.b)
}

// hasTerminal reports whether tab's terminal shows in a tile.
func (a *app) hasTerminal(tab string) bool {
	for _, t := range a.root.tiles(nil) {
		if t.tab == tab && t.shell {
			return true
		}
	}
	return false
}

// cycle moves the focus to the next tile, or the previous one.
func (a *app) cycle(by int) {
	all := a.root.tiles(nil)
	for i, t := range all {
		if t == a.active {
			a.activate(all[(i+by+len(all))%len(all)])
			return
		}
	}
}

// dropped applies what a drag left on a side of x.
func (a *app) dropped(x *tile, side string, v carried) {
	switch {
	case v.tile != nil:
		if v.tile == x {
			return
		}
		a.unsplit(v.tile)
		a.place(x, side, v.tile.tab, v.tile.shell)
	case v.tab != "":
		a.place(x, side, v.tab, false)
	case v.key != "":
		then(a.o, func(r struct{ Tab string }, err error) {
			if err != nil {
				a.say(err.Error())
			} else if r.Tab != "" && a.contains(x) {
				a.place(x, side, r.Tab, false)
			}
		}, "open", "key", v.key)
	}
}

func (a *app) contains(x *tile) bool {
	for _, t := range a.root.tiles(nil) {
		if t == x {
			return true
		}
	}
	return false
}

// layout draws x, splits as dividers the pointer drags; corner is whether x reaches the window's
// top right, where its controls are.
func (a *app) layout(c *ui.Context, x *tile, corner bool) {
	if !x.leaf() {
		first := func() { a.layout(c, x.a, corner && x.below) }
		second := func() { a.layout(c, x.b, corner && !x.below) }
		if x.below {
			ui.SplitVerticalPercent(c, &x.share, first, second).Fill()
		} else {
			ui.SplitPercent(c, &x.share, first, second).Fill()
		}
		return
	}
	a.building, a.corner = x, corner
	a.narrow, a.tight = x.width > 0 && x.width < 560, x.width > 0 && x.width < 400
	if c.FocusWithin(x.at) && a.active != x {
		a.activate(x)
	}
	o, pal := a.o, p(c)
	many := !a.root.leaf()
	e := ui.Column(c).Fill().Background(pal.bg).Bind(&x.at).Children(func() {
		t := o.tab(x.tab)
		switch {
		case t != nil && x.shell:
			a.terminal(c, t, x)
		case t != nil:
			a.chat(c, t)
		default:
			a.homeScreen(c)
		}
		if a.dragging {
			a.zones(c, x)
		}
	})
	focused, over := many && a.active == x, x.over
	x.over = ""
	e.DrawOver(func(pt *ui.Painter, r ui.Rect) {
		x.width = r.W
		// The focused tile's top edge in the rupture, as the sidebar marks the conversation shown.
		if focused {
			pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 2}, rupture, 0)
		}
		// Where a drop would put what is dragged: this half, or the whole tile.
		h := r
		switch over {
		case "":
			return
		case "left":
			h.W /= 2
		case "right":
			h.X, h.W = h.X+h.W/2, h.W/2
		case "top":
			h.H /= 2
		case "bottom":
			h.Y, h.H = h.Y+h.H/2, h.H/2
		}
		pt.Fill(h, rupture.Alpha(0.08), 0)
		pt.Stroke(h, rupture.Alpha(0.6), 0, 1)
	})
}

// zones are the parts of a tile a drag drops on while it lasts: a side opens a tile there, the
// middle shows what is dragged in this one.
func (a *app) zones(c *ui.Context, x *tile) {
	for _, z := range [...]struct {
		side       string
		l, t, w, h float32
	}{
		{"middle", 25, 25, 50, 50}, {"left", 0, 0, 25, 100}, {"right", 75, 0, 25, 100}, {"top", 25, 0, 50, 25}, {"bottom", 25, 75, 50, 25},
	} {
		e := ui.Box(c.Key(z.side)).Absolute().LeftPercent(z.l).TopPercent(z.t).WidthPercent(z.w).HeightPercent(z.h)
		if _, ok := ui.DragOver[carried](e); ok {
			x.over = z.side
		}
		if v, ok := ui.Drop[carried](e); ok {
			a.dropped(x, z.side, v)
		}
	}
}

// tileMenu is what a tile's bar offers the pointer, on hover and on its menu: split it, show its
// conversation's terminal, close it.
func (a *app) tileMenu(c *ui.Context, bar ui.Element, hovered bool) {
	x, pal := a.building, p(c)
	if x == nil {
		return
	}
	many := !a.root.leaf()
	if hovered {
		icon := func(tip string, draw func(pt *ui.Painter, r ui.Rect)) bool {
			e := ui.Box(c.Key(tip)).Size(24, 24).Radius(soft).Shrink(0).Tooltip(tip)
			hover(e, pal.raised)
			e.Draw(func(pt *ui.Painter, r ui.Rect) {
				draw(pt, ui.Rect{X: r.X + 6, Y: r.Y + 7, W: 12, H: 10})
			})
			return press(e)
		}
		if icon("Split right  "+keys("D"), func(pt *ui.Painter, r ui.Rect) {
			pt.Stroke(r, pal.meta, 1.5, 1.2)
			pt.Fill(ui.Rect{X: r.X + r.W/2 - 0.6, Y: r.Y, W: 1.2, H: r.H}, pal.meta, 0)
		}) {
			a.split(x, "right", &tile{})
		}
		if icon("Split down  "+keys("⇧D"), func(pt *ui.Painter, r ui.Rect) {
			pt.Stroke(r, pal.meta, 1.5, 1.2)
			pt.Fill(ui.Rect{X: r.X, Y: r.Y + r.H/2 - 0.6, W: r.W, H: 1.2}, pal.meta, 0)
		}) {
			a.split(x, "bottom", &tile{})
		}
		if many && press(ui.Text(c, "×").FontSize(16).TextColor(pal.meta).PaddingX(6).Tooltip("Close tile  "+keys("W"))) {
			a.unsplit(x)
		}
	}
	bar.ContextMenu(func(m *ui.Menu) {
		if m.Item("Split right").Chosen() {
			a.split(x, "right", &tile{})
		}
		if m.Item("Split down").Chosen() {
			a.split(x, "bottom", &tile{})
		}
		if x.tab != "" && !x.shell && m.Item("Terminal").Chosen() {
			a.terminalOf(x.tab)
		}
		if many && m.Item("Even out tiles").Chosen() {
			a.root.even(true)
		}
		if many && m.Item("Close tile").Chosen() {
			a.unsplit(x)
		}
	})
}

// carry makes e drag v, and says so to the tiles, which then show where it would drop.
func (a *app) carry(e ui.Element, v carried) {
	if v.tile != nil && a.root.leaf() {
		return // a lone tile has nowhere to go
	}
	if e.Drag(v).Dragging() {
		a.dragNow = true
	}
}
