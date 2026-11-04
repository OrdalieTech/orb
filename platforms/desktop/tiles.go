package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// tile is a part of the window, as a tiling window manager cuts it: two tiles, side by side or one
// above the other, or a tile showing a conversation (Home for none) or the terminal where one runs.
// Both hands work it: the keys (⌘D, ⌘⇧D, ⌘W, ⌘[ ⌘], ⌘=, ⌘J) and the pointer (a thread or a tile
// dropped on a tile's side or middle, dividers dragged, a tile's bar and its menu).
type tile struct {
	a, b  *tile
	below bool    // b under a rather than beside it
	share float32 // a's percentage of the room
	room  float32 // that room's width (height below), as last drawn

	tab     string // a tile's conversation, "" for Home
	shell   bool   // the tile shows tab's terminal
	at      ui.Handle
	modelAt ui.Handle // its prompt box's model and place, which their popovers open out of
	whereAt ui.Handle
	over    string  // the side a drag over it would drop on
	shown   string  // the side its drop preview shows, as it fades
	width   float32 // as last drawn
	height  float32
	showing [2]any    // what it shows, conversation and terminal or not
	since   time.Time // since when: it fades in for a moment
}

// match is a tile's identity across layouts: a tile moving between splits glides from where it was.
type match struct{ t *tile }

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

// reshape lets the tiles glide to their new places for a moment: between changes, a divider
// dragged moves them at once.
func (a *app) reshape() { a.morph = time.Now().Add(400 * time.Millisecond) }

// zoom shows x alone, filling the tiles' room, or the layout again (⌘⇧↩).
func (a *app) zoom(x *tile) {
	if a.zoomed == x {
		x = nil
	}
	a.zoomed = x
	a.reshape()
}

// replace puts n where x is in the layout.
func (a *app) replace(x, n *tile) {
	a.reshape()
	a.zoomed = nil
	switch p := a.parent(x); {
	case p == nil:
		a.root = n
	case p.a == x:
		p.a = n
	default:
		p.b = n
	}
}

// split puts n on a side of x (left, right, top, bottom), sharing x's room evenly, and focuses it;
// a line with no room for another tile says so instead.
func (a *app) split(x *tile, side string, n *tile) {
	below := side == "top" || side == "bottom"
	if !a.fits(x, below) {
		a.say("No room for another tile " + map[bool]string{true: "here", false: "in this row"}[below] + " · close one, or split the other way")
		return
	}
	s := &tile{a: x, b: n, below: below, share: 50}
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

// fits reports whether x's line (beside it, or below) has room for another tile.
func (a *app) fits(x *tile, below bool) bool {
	line := x
	for p := a.parent(line); p != nil && p.below == below; p = a.parent(p) {
		line = p
	}
	room := line.room
	if line.leaf() {
		room = map[bool]float32{true: x.height, false: x.width}[below]
	}
	return room == 0 || room/float32(line.count(below)+1) >= map[bool]float32{true: 180, false: 300}[below]
}

// newSession shows Home: in the tile already showing it, else among tiles in a new one beside the
// focused tile (or under it), so the conversations watched stay in sight; else in the lone tile.
func (a *app) newSession() {
	a.focus++
	switch x := a.showing(""); {
	case x != nil:
		a.stack, a.fresh = nil, nil
		a.activate(x)
	case !a.root.leaf() && a.fits(a.active, false):
		a.stack, a.fresh = nil, nil
		a.split(a.active, "right", &tile{})
	case !a.root.leaf() && a.fits(a.active, true):
		a.stack, a.fresh = nil, nil
		a.split(a.active, "bottom", &tile{})
	default:
		a.show("")
	}
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
	// Its line shares the room it leaves, as a split takes its share.
	if q := a.parent(other); q != nil && q.below == p.below {
		for r := a.parent(q); r != nil && r.below == p.below; r = a.parent(r) {
			q = r
		}
		q.even(false)
	} else if !other.leaf() && other.below == p.below {
		other.even(false)
	}
	if a.active == x {
		a.activate(other.tiles(nil)[0])
	}
}

// activate makes x the tile the keys and the sidebar act on; a zoom moves to it.
func (a *app) activate(x *tile) {
	a.active = x
	a.tab = x.tab
	if a.zoomed != nil && a.zoomed != x {
		a.zoomed = x
		a.reshape()
	}
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
		a.reshape()
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
		// No tile gets narrower than a column of text, or lower than a few lines, while the room allows.
		least := map[bool]float32{true: 180, false: 300}[x.below]
		if lo, hi := 100*least*float32(x.a.count(x.below))/x.room, 100-100*least*float32(x.b.count(x.below))/x.room; x.room > 0 && lo < hi {
			x.share = min(max(x.share, lo), hi)
		}
		first := func() { a.layout(c, x.a, corner && x.below) }
		second := func() { a.layout(c, x.b, corner && !x.below) }
		split := ui.SplitPercent
		if x.below {
			split = ui.SplitVerticalPercent
		}
		split(c, &x.share, first, second).Fill().DrawOver(func(_ *ui.Painter, r ui.Rect) { x.room = map[bool]float32{true: r.H, false: r.W}[x.below] })
		return
	}
	a.building, a.corner = x, corner
	a.narrow, a.tight = x.width > 0 && x.width < 560, x.width > 0 && x.width < 400
	if c.FocusWithin(x.at) && a.active != x {
		a.activate(x)
	}
	o, pal := a.o, p(c)
	many := !a.root.leaf() && a.zoomed == nil
	// It glides from where it was as the layout changes, and fades in as it opens or shows another
	// conversation.
	glide := ui.ElementTransition{Match: match{x}, Position: true, Size: true, Duration: time.Millisecond}
	if time.Now().Before(a.morph) {
		glide.Animation = &opening
	}
	e := ui.Column(c).Fill().Background(pal.bg).Bind(&x.at).Transition(glide)
	// The fade is the tile's, not its element's: a tile moving to another split is a new element.
	if now := (time.Now()); x.showing != [2]any{x.tab, x.shell} || x.since.IsZero() {
		x.showing, x.since = [2]any{x.tab, x.shell}, now
	}
	if in := float32(time.Since(x.since)) / float32(260*time.Millisecond); in < 1 && !c.Preferences().ReduceMotion {
		e.Opacity(easeOut(in))
		c.After(16 * time.Millisecond)
	}
	e.Children(func() {
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
	if x.over != "" {
		x.shown = x.over
	}
	focus := e.AnimateTo("focus", map[bool]float32{true: 1}[many && a.active == x], hovering)
	drop := e.AnimateTo("drop", map[bool]float32{true: 1}[x.over != ""], hovering)
	x.over = ""
	e.DrawOver(func(pt *ui.Painter, r ui.Rect) {
		x.width, x.height = r.W, r.H
		// The focused tile's top edge in the rupture, as the sidebar marks the conversation shown.
		if focus > 0 {
			pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 2}, rupture.Alpha(focus), 0)
		}
		// Where a drop would put what is dragged: this half, or the whole tile.
		h := r
		switch x.shown {
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
		pt.Fill(h, rupture.Alpha(0.08*drop), 0)
		pt.Stroke(h, rupture.Alpha(0.6*drop), 0, 1)
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

// tileTools is what a tile's bar offers the pointer on hover: split it, zoom it, close it.
func (a *app) tileTools(c *ui.Context) {
	x, pal := a.building, p(c)
	if x == nil || a.tools {
		return
	}
	a.tools = true
	many := !a.root.leaf()
	// A tight tile keeps its bar to close; the rest is on its menu.
	if a.barHover && a.tight && many {
		if press(ui.Text(c.Key("close"), "×").FontSize(16).TextColor(pal.meta).PaddingX(6).Tooltip("Close tile  " + keys("W"))) {
			a.unsplit(x)
		}
	} else if a.barHover {
		appear := ui.ElementTransition{Enter: &ui.Motion{Opacity: 0, X: 4}, Exit: &ui.Motion{Opacity: 0}, Animation: &hovering}
		icon := func(tip string, draw func(pt *ui.Painter, r ui.Rect)) bool {
			e := ui.Box(c.Key(tip)).Size(24, 24).Radius(soft).Shrink(0).Tooltip(tip).Transition(appear)
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
		if many && icon(map[bool]string{true: "Back to the tiles  ", false: "Zoom  "}[a.zoomed == x]+keys("⇧↩"), func(pt *ui.Painter, r ui.Rect) {
			pt.Stroke(r, pal.meta, 1.5, 1.2)
			if a.zoomed != x {
				pt.Fill(ui.Rect{X: r.X + 3, Y: r.Y + 3, W: r.W - 6, H: r.H - 6}, pal.meta, 0.5)
			}
		}) {
			a.zoom(x)
		}
		if many && press(ui.Text(c.Key("close"), "×").FontSize(16).TextColor(pal.meta).PaddingX(6).Tooltip("Close tile · its conversation stays open  "+keys("W")).Transition(appear)) {
			a.unsplit(x)
		}
	}
}

// tileMenu is the rest on its bar's menu: its conversation's terminal, the layout, closing the
// tile or its conversation; each with its keys, as the tooltips have them.
func (a *app) tileMenu(bar ui.Element) {
	x := a.building
	if x == nil {
		return
	}
	many := !a.root.leaf()
	bar.ContextMenu(func(m *ui.Menu) {
		if m.Item("Split right").Shortcut(cmd, ui.KeyD).Chosen() {
			a.split(x, "right", &tile{})
		}
		if m.Item("Split down").Shortcut(cmd|ui.Shift, ui.KeyD).Chosen() {
			a.split(x, "bottom", &tile{})
		}
		if x.tab != "" && !x.shell && m.Item("Terminal").Shortcut(cmd, ui.KeyJ).Chosen() {
			a.terminalOf(x.tab)
		}
		if many && m.Item(map[bool]string{true: "Back to the tiles", false: "Zoom"}[a.zoomed == x]).Shortcut(cmd|ui.Shift, ui.KeyEnter).Chosen() {
			a.zoom(x)
		}
		if many && m.Item("Even out tiles").Shortcut(cmd, ui.KeyEqual).Chosen() {
			a.root.even(true)
			a.reshape()
		}
		if many && m.Item("Close tile").Shortcut(cmd, ui.KeyW).Chosen() {
			a.unsplit(x)
		}
		if x.tab != "" && !x.shell && m.Item("Close conversation").Chosen() {
			a.close(x.tab)
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

// kept is a tile as the next launch brings it back: conversations by their tab ids, which the view
// keeps across launches.
type kept struct {
	A, B   *kept   `json:",omitempty"`
	Below  bool    `json:",omitempty"`
	Share  float32 `json:",omitempty"`
	Tab    string  `json:",omitempty"`
	Shell  bool    `json:",omitempty"`
	Active bool    `json:",omitempty"`
}

func (a *app) keep(x *tile) *kept {
	if x.leaf() {
		return &kept{Tab: x.tab, Shell: x.shell, Active: x == a.active}
	}
	return &kept{A: a.keep(x.a), B: a.keep(x.b), Below: x.below, Share: x.share}
}

func (a *app) bringBack(k *kept) *tile {
	if k.A == nil || k.B == nil {
		x := &tile{tab: k.Tab, shell: k.Shell}
		if k.Active {
			a.active = x
		}
		return x
	}
	return &tile{a: a.bringBack(k.A), b: a.bringBack(k.B), below: k.Below, share: k.Share}
}

// tilesFile is where the layout waits for the next launch.
func tilesFile() string {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tiles.json")
}

// loadTiles brings back the last launch's layout, a lone Home tile without one.
func (a *app) loadTiles() {
	a.root = &tile{}
	var k kept
	if b, err := os.ReadFile(tilesFile()); err == nil && json.Unmarshal(b, &k) == nil {
		a.root = a.bringBack(&k)
	}
	if a.active == nil || !a.contains(a.active) {
		a.active = a.root.tiles(nil)[0]
	}
	a.tab = a.active.tab
}

// saveTiles writes the layout when it changed.
func (a *app) saveTiles() {
	b, _ := json.Marshal(a.keep(a.root))
	if !bytes.Equal(b, a.savedTiles) {
		a.savedTiles = b
		_ = os.WriteFile(tilesFile(), b, 0o600)
	}
}
