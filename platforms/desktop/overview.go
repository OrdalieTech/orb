package main

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// overviewAnim is the crisp ease-out curve shared by the Overview grid and workspace handoff.
var overviewAnim = ui.Animation{Duration: 220 * time.Millisecond, Ease: easeOut}

// onPinch handles continuous macOS/Wayland trackpad magnification (ui.InputZoom).
// Pinching inward shrinks the workspace 1:1 into the Agent Overview grid; pinching outward in
// Overview zooms back into the active conversation.
func (a *app) onPinch(ev ui.InputEvent) {
	if ev.Kind != ui.InputZoom || ev.Scale <= 0 {
		return
	}
	// Ignore pinches while a blocking modal, viewer, terminal, or settings screen is open.
	if a.picker != nil || a.palette || a.exploring != "" || a.model != "" || a.rename != nil || a.viewing[0] != "" || len(a.stack) > 0 {
		return
	}
	if a.active != nil && a.active.shell {
		return
	}
	if len(a.o.state.Tabs) == 0 && !a.overview {
		return
	}
	now := time.Now()
	if now.Before(a.pinchCool) {
		return
	}
	if a.pinchScale == 0 || now.Sub(a.pinchAt) > 220*time.Millisecond {
		a.pinchScale = 1
	}
	a.pinchScale = min(max(a.pinchScale*ev.Scale, 0.55), 1.55)
	a.pinchAt = now

	if !a.overview && a.pinchScale <= 0.78 {
		a.overview = true
		a.pinchScale = 1
		a.pinchCool = now.Add(380 * time.Millisecond)
		return
	}
	if a.overview && a.pinchScale >= 1.24 {
		a.overview = false
		a.pinchScale = 1
		a.pinchCool = now.Add(380 * time.Millisecond)
	}
}

// settlePinch springs an uncommitted pinch smoothly back to 1.0 once the user lifts their fingers.
func (a *app) settlePinch(c *ui.Context) float32 {
	if a.pinchScale == 0 {
		a.pinchScale = 1
	}
	if a.pinchScale != 1 {
		const idle = 150 * time.Millisecond
		if elapsed := time.Since(a.pinchAt); elapsed >= idle {
			a.pinchScale = 1
		} else {
			c.After(idle - elapsed + 10*time.Millisecond)
		}
	}
	return a.pinchScale
}

// overviewScreen draws every open agent conversation as a live streaming card grid,
// matching the Android & Web Overview grids with keyboard navigation and pinch-back.
func (a *app) overviewScreen(c *ui.Context) {
	o, pal := a.o, p(c)
	c.OnShortcut(0, ui.KeyEscape, func() { a.overview = false })

	// Sort live cards: asking first, then busy, unread, awake, asleep.
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

	tb := c.TitleBar()
	left, right := float32(16), float32(16)
	if tb.Left > 0 {
		left = tb.Left + 16
	}
	if tb.Right > 0 {
		right = tb.Right + 12
	}

	ui.Column(c).Fill().Background(pal.bg).Transition(ui.ElementTransition{
		Enter: &ui.Motion{Scale: 1.05, Opacity: 0},
		Exit:  &ui.Motion{Scale: 1.04, Opacity: 0},
		Animation: &overviewAnim,
	}).Children(func() {
		// Minimal top bar with Overview title, live summary, and back/grid toggle.
		ui.Row(c).FillWidth().Height(max(tb.Height, 48)).Padding(0, right, 0, left).Gap(12).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(pal.rule.Alpha(0.65)).DragWindow().Children(func() {
			if press(hover(ui.Row(c).Height(28).Padding(0, 8).Radius(6).AlignItems(ui.Center), pal.hover).Children(func() {
				stretch(c, "ORB", 15, pal.fg, 1)
			}).Tooltip("Home")) {
				a.overview = false
				a.show("")
			}
			ui.Text(c, fmt.Sprintf("overview · %d open", len(tabs))).FontSize(small).FontWeight(medium).TextColor(pal.mute).SingleLine()
			if s := strings.TrimSpace(o.state.Summary); s != "" {
				ink := pal.meta
				if strings.HasPrefix(s, "needs") {
					ink = rupture
				}
				ui.Text(c, s).FontSize(12).TextColor(ink).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			ui.Text(c, "pinch or esc to return").FontSize(11).TextColor(pal.meta).SingleLine()
			a.overviewButton(c)
			a.menuMark(c)
		})

		w, _ := c.Size()
		cols := 3
		switch {
		case w < 640:
			cols = 1
		case w < 980:
			cols = 2
		case w >= 1400:
			cols = 4
		}

		ui.Scroll(c.Key("in:overview")).Grow(1).Basis(0).FillWidth().Children(func() {
			ui.Column(c).FillWidth().MaxWidth(1360).Margin(0, ui.Auto).Padding(20, 24, 36).Gap(16).Children(func() {
				ui.Grid(c).FillWidth().Columns(cols).Gap(12).Children(func() {
					for _, t := range tabs {
						a.overviewCard(c, t)
					}
					// "+ new session" card at the end of the grid.
					addCard := hover(ui.Column(c.Key("ov:new")).Height(210).Padding(16).Radius(10).
						Border(1, pal.rule.Alpha(0.75)).Justify(ui.Center).AlignItems(ui.Center).Gap(6), pal.hover)
					addCard.Children(func() {
						ui.Text(c, "+").FontSize(22).TextColor(pal.meta)
						ui.Text(c, "new session").FontSize(small).FontWeight(medium).TextColor(pal.mute)
					})
					if press(addCard) {
						a.overview = false
						a.newSession()
					}
				})
			})
		})
	})
}

func (a *app) overviewCard(c *ui.Context, t Tab) {
	o, pal := a.o, p(c)
	active := t.ID == a.tab
	border := pal.rule.Alpha(0.6)
	switch {
	case t.Ask != nil:
		border = rupture
	case active:
		border = pal.fg.Alpha(0.55)
	case t.Busy:
		border = pal.mute
	}
	card := ui.Column(c.Key("ov:"+t.ID)).Height(210).Padding(12, 14).Gap(6).Radius(10).
		Background(pal.raised).Border(1, border).Clip()
	if t.Asleep {
		card.Opacity(0.72)
	}
	hovered := card.Hovered()
	hover(card, pal.hover)
	card.ContextMenu(func(m *ui.Menu) {
		if m.Item("Open").Chosen() {
			a.overview = false
			a.show(t.ID)
		}
		if m.Item("Open in split right").Chosen() {
			a.overview = false
			a.place(a.active, "right", t.ID, false)
		}
		if m.Item("Rename…").Chosen() {
			a.overview = false
			a.renameTab(t)
		}
		if m.Item("Close").Shortcut(cmd, ui.KeyW).Chosen() {
			a.close(t.ID)
		}
	})
	card.Children(func() {
		// Header: peer dot, title, unread badge, close button on hover.
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			where(c, o.hue(t.Peer), t.Busy, t.Ask != nil, map[bool]ui.Color{true: pal.fg, false: pal.mute}[active || t.Busy])
			ui.Text(c, cmp.Or(t.Title, "new session")).Grow(1).Basis(0).MinWidth(0).FontSize(13.5).FontWeight(strong).TextColor(pal.fg).SingleLine()
			if t.Unread > 0 {
				ui.Text(c, fmt.Sprintf("%d", t.Unread)).FontSize(11).FontWeight(strong).TextColor(pal.fg)
			}
			if hovered {
				if press(ui.Row(c).Size(20, 20).Radius(4).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "×").FontSize(14).TextColor(pal.meta)
				}).Tooltip("Close · it runs on")) {
					a.close(t.ID)
				}
			}
		})

		// Meta line: machine · folder · state.
		state, stateInk := "", pal.meta
		switch {
		case t.Ask != nil:
			state, stateInk = "asks · "+cmp.Or(t.Ask.Title, "approval"), rupture
		case t.Busy:
			state, stateInk = "working", pal.fg
		case strings.TrimSpace(t.Status) != "":
			state = strings.Split(t.Status, " · ")[0]
		case t.Asleep:
			state = "asleep"
		default:
			_, short, _ := strings.Cut(t.Model, "/")
			state = cmp.Or(short, t.Model)
		}
		meta := strings.Join(nonEmpty(map[bool]string{true: t.Where}[t.Remote], tidy(t.CWD), state), " · ")
		ui.Text(c, meta).FontSize(11).TextColor(stateInk).SingleLine()

		// Live transcript tail preview with soft top fade into card background.
		preview := overviewTail(o.rows[t.ID])
		if preview == "" {
			preview = map[bool]string{true: "working…", false: "ready"}[t.Busy]
		}
		body := ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.End).Clip().Padding(4, 0, 2)
		body.DrawOver(func(pt *ui.Painter, r ui.Rect) {
			fadeH := min(r.H*0.35, 28)
			if fadeH > 2 {
				pt.FillPathGradient(
					(&ui.Path{}).MoveTo(r.X, r.Y).LineTo(r.X+r.W, r.Y).LineTo(r.X+r.W, r.Y+fadeH).LineTo(r.X, r.Y+fadeH).Close(),
					ui.LinearGradient{From: pal.raised, To: pal.raised.Alpha(0), Angle: 180},
				)
			}
		})
		body.Children(func() {
			ui.Text(c, preview).FontSize(11.5).LineHeight(1.4).TextColor(pal.mute).MaxLines(6)
		})

		// Footer: context gauge + session cost when present.
		if t.Context > 0 || t.Cost > 0 {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				if t.Context > 0 {
					fillInk := pal.fg
					if t.Context > 0.8 {
						fillInk = rupture
					}
					pct := min(max(t.Context, 0), 1)
					ui.Box(c).Width(48).Height(3).Radius(1.5).Background(pal.rule).Clip().Children(func() {
						ui.Box(c).Width(float32(48 * pct)).Height(3).Background(fillInk)
					})
					ui.Textf(c, "%d%%", int(math.Round(t.Context*100))).FontSize(10.5).TextColor(map[bool]ui.Color{true: rupture, false: pal.meta}[t.Context > 0.8])
				}
				ui.Spacer(c)
				if t.Cost > 0 {
					ui.Textf(c, "$%.2f", t.Cost).FontSize(10.5).TextColor(pal.meta)
				}
			})
		}
	})
	if press(card) {
		a.overview = false
		a.show(t.ID)
	}
}

// overviewTail builds a compact multi-line excerpt of the latest activity in a conversation.
func overviewTail(rows []Row) string {
	start := max(0, len(rows)-6)
	var out []string
	for _, r := range rows[start:] {
		switch r.Kind {
		case "you":
			if s := strings.TrimSpace(r.Text); s != "" {
				out = append(out, "› "+firstLinePreview(s))
			}
		case "md":
			if r.Block != nil {
				if s := strings.TrimSpace(blockToMarkdown(*r.Block)); s != "" {
					out = append(out, firstLinePreview(s))
				}
			}
		case "run":
			if r.What != "" {
				verb := "worked"
				if r.Live {
					verb = "working"
				}
				out = append(out, verb+" · "+r.What)
			} else if len(r.Actions) == 1 {
				a := r.Actions[0]
				out = append(out, strings.TrimSpace(a.Verb+" "+a.Target))
			}
		case "note":
			if s := strings.TrimSpace(r.Text); s != "" {
				out = append(out, firstLinePreview(s))
			}
		}
	}
	return strings.Join(out, "\n")
}

func firstLinePreview(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if r := []rune(line); len(r) > 90 {
		return string(r[:90]) + "…"
	}
	return line
}

// overviewButton draws the quiet 4-square grid icon in the title bar.
func (a *app) overviewButton(c *ui.Context) {
	pal := p(c)
	ink := pal.meta
	if a.overview {
		ink = pal.fg
	}
	btn := hover(ui.Row(c).Size(28, 28).Radius(6).Justify(ui.Center).AlignItems(ui.Center), pal.hover)
	if a.overview {
		btn.Background(pal.recessed)
	}
	btn.Children(func() {
		ui.Box(c).Size(12, 12).Draw(func(pt *ui.Painter, r ui.Rect) {
			s := float32(4.5)
			gap := r.W - 2*s
			for _, off := range [][2]float32{{0, 0}, {s + gap, 0}, {0, s + gap}, {s + gap, s + gap}} {
				cell := ui.Rect{X: r.X + off[0], Y: r.Y + off[1], W: s, H: s}
				if a.overview {
					pt.Fill(cell, ink, 1)
				} else {
					pt.Stroke(cell, ink, 1.2, 1)
				}
			}
		})
	})
	if press(btn.Tooltip("Overview · live agent grid  " + keys("G") + " · pinch")) {
		a.overview = !a.overview
		a.pinchScale = 1
	}
}
