package main

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// size is conversation text, in DIPs; column is how wide it reads: some 80 characters of it.
const size, column = 15, 720

// turns groups rows by speaker: what the person said, or everything Orb said and did until the next one.
// Turns are slices of rows, not copies, as the view groups them each frame: a patch writes the rows
// to a new array (Orb.heard), never under turns a list still holds.
func turns(rows []Row) [][]Row {
	var out [][]Row
	run := 0
	for i, r := range rows {
		if r.Kind == "you" {
			if i > run {
				out = append(out, rows[run:i])
			}
			out, run = append(out, rows[i:i+1]), i+1
		}
	}
	if run < len(rows) {
		out = append(out, rows[run:])
	}
	return out
}

// reading centres what reads in a column of its own width, wherever the window is.
func reading(c *ui.Context) ui.Element {
	return ui.Column(c).FillWidth().MaxWidth(column).Margin(0, ui.Auto)
}

func (a *app) chat(c *ui.Context, t *Tab) {
	a.strip(c, t)
	a.conversation(c, t)
}

func (a *app) conversation(c *ui.Context, t *Tab) {
	o, pal := a.o, p(c)
	ui.Column(c).Grow(1).Basis(0).FillWidth().Children(func() {
		if !t.Remote && o.state.Acting {
			patternBlue(c, func() { o.send("abort", "tab", t.ID) })
		}
		tt := turns(o.rows[t.ID])
		// A List takes one state: a second tile showing it (a layout saved so) scrolls on its own.
		key := t.ID
		if x := a.building; x != nil && a.showing(t.ID) != x {
			key += fmt.Sprintf("@%p", x)
		}
		ls := a.lists[key]
		if ls == nil {
			ls = &ui.ListState{FollowEnd: true, FollowAnimation: &ui.Smooth}
			a.lists[key] = ls
		}
		// Older messages load above; the ones read so far stay where they are.
		head := 0
		if t.Earlier {
			head = 1
		}
		ls.Key = func(i int) any {
			switch {
			case i < head:
				return "earlier"
			case i-head < len(tt):
				return tt[i-head][0].Key
			}
			return "end"
		}
		ui.List(c, ls, head+len(tt)+1, func(i int) {
			switch {
			case i < head:
				e := reading(c).PaddingY(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "earlier messages").FontSize(small).FontWeight(medium).TextColor(pal.meta)
				})
				if press(e) {
					o.send("earlier", "tab", t.ID)
				}
			case i-head < len(tt):
				a.turn(c, tt[i-head], i == head, t)
			default:
				// Room under the last message, where the caret waits while Orb writes.
				reading(c).Height(40).Padding(6, margin, 0).Children(func() {
					if t.Streaming {
						caret(c)
					}
				})
			}
		}).Grow(1).Basis(0).FillWidth().Justify(ui.End)
		switch {
		case len(tt) > 0:
		case !t.Loaded:
			a.pending(c)
		case t.Asleep:
			label(c, "asleep · a message wakes it").TextColor(pal.meta).Attach(ui.AnchorCenter, ui.AnchorCenter)
		default:
			ui.Column(c).AlignItems(ui.Center).Gap(10).Attach(ui.AnchorCenter, ui.AnchorCenter).Children(func() {
				ui.Icon(c, ordalie).Size(26, 26).TextColor(pal.rule)
				label(c, "ready").TextColor(pal.meta)
			})
		}
		reading(c).Children(func() {
			if t.Status != "" {
				ui.Text(c, t.Status).Padding(4, margin+6).FontSize(small).TextColor(pal.meta)
			}
			// What its plugins show beside it: the task list, a status, the last notice.
			for _, s := range t.Slots {
				ink := pal.meta
				if s.Alarm {
					ink = rupture
				}
				a.folded(c.Key(s.Key), strings.Join(s.Lines, "\n"), ink).PaddingX(margin + 6)
			}
		})
		a.promptBox(c, t, "What next?")
	})
}

// strip is the conversation's title bar: its tile's tabs (its own named and marked shown); at
// right how full its context is, its plan's window nearest the limit (all of them a click away),
// what it cost, and the terminal where it runs.
func (a *app) strip(c *ui.Context, t *Tab) {
	pal := p(c)
	a.titleBar(c, func() {
		ui.Spacer(c)
		a.tileTools(c)
		// A tight tile under the window's controls has room for its title and its terminal only.
		if t.Context > 0 && !(a.tight && a.corner) {
			ink := pal.meta
			if a.tight {
				ink = pal.mute
			}
			if t.Context > 0.8 {
				ink = rupture
			}
			ui.Row(c).Gap(6).Shrink(0).Tooltip("Context in use").Children(func() {
				if !a.tight {
					gauge(c, t.Context, 0.8)
				}
				ui.Textf(c, "%d%%", int(math.Round(t.Context*100))).FontSize(12).TextColor(ink).SingleLine()
			})
		}
		if t.Usage != nil && len(t.Usage.Windows) > 0 && !a.tight {
			w := slices.MinFunc(t.Usage.Windows, func(x, y Window) int { return int(x.Left - y.Left) })
			ink := pal.meta
			if w.Left < 15 {
				ink = rupture
			}
			// 活動限界, the activity limit an EVA's internal battery counts down to: what its plan's
			// window nearest its end has left, and when it resets.
			left := fmt.Sprintf("%s %d%%", w.Name, int(math.Round(w.Left)))
			if w.Resets > 0 {
				left += " · " + time.UnixMilli(w.Resets).Format("15:04")
			}
			limit := ui.Row(c).Gap(6).Shrink(0).Children(func() {
				if !a.narrow {
					ui.Text(c, "活動限界").FontSize(labelSize).TextColor(ink)
				}
				ui.Text(c, left).FontSize(12).TextColor(ink).SingleLine()
			})
			if press(limit.Tooltip("Activity limit: the plan's window nearest its end, and when it resets")) {
				a.usage(t)
			}
		}
		if t.Cost > 0 && !a.narrow {
			ui.Textf(c, "$%.2f", t.Cost).FontSize(12).TextColor(pal.meta).SingleLine().Shrink(0)
		}
		term := ui.Row(c).Padding(4, 8).Radius(soft).Shrink(0).Children(func() { ui.Text(c, ">_").FontWeight(strong) }).Tooltip("Terminal  " + keys("J"))
		if a.hasTerminal(t.ID) {
			term.Background(pal.raised)
		}
		if press(term) {
			a.terminalOf(t.ID)
		}
	})
}

// usage is a conversation's plan windows: what is left of each and when it resets, from its Orb's last reading.
func (a *app) usage(t *Tab) {
	u := t.Usage
	provider, _, _ := strings.Cut(t.Model, "/")
	var lines []string
	for _, w := range u.Windows {
		line := fmt.Sprintf("%s · %d%% left", w.Name, int(math.Round(w.Left)))
		if w.Resets > 0 {
			line += " · resets " + time.UnixMilli(w.Resets).Format("Mon 15:04")
		}
		lines = append(lines, line)
	}
	a.picker = &picker{title: strings.Join(slices.DeleteFunc([]string{provider, u.Plan, "read " + ago(u.At)}, func(s string) bool { return s == "" }), " · "), options: lines, pick: func(string) {}}
}

// patternBlue says another Orb is driving this computer. Blue fills space here and nowhere else.
func patternBlue(c *ui.Context, stop func()) {
	hazard(c, 6, blue, texte.Alpha(0.5))
	ui.Column(c).FillWidth().Background(blue).Padding(14, margin).Children(func() {
		stretch(c, "PATTERN BLUE", 30, texte, 1)
		label(c, "パターン青 · ANOTHER ORB IS DRIVING").TextColor(texte.Alpha(0.75)).MarginY(6)
		ui.Row(c).FillWidth().MarginY(10).Children(func() {
			ui.Text(c, "A peer is prompting this computer").Grow(1).FontWeight(medium).TextColor(texte)
			if btn(c, "stop", true, texte, blue) {
				stop()
			}
		})
	})
}

// caret blinks, off then on, each half of its period; painted, it draws a frame at each change only.
func caret(c *ui.Context) {
	const half = 530 * time.Millisecond
	ui.Box(c).Size(size*0.55, size*1.1).Draw(func(p *ui.Painter, r ui.Rect) {
		at := time.Duration(p.Now().UnixNano()) % (2 * half)
		if at >= half {
			p.Fill(r, rupture, 0)
			at -= half
		}
		p.After(half - at)
	})
}

// pending says what the tile shows is on its way: a dot breathing in its middle, once the tile has
// waited 150 ms, so what comes at once never flashes it.
func (a *app) pending(c *ui.Context) {
	if wait := 150*time.Millisecond - time.Since(a.building.since); wait > 0 {
		c.After(wait)
		return
	}
	pal := p(c)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Attach(ui.AnchorCenter, ui.AnchorCenter).Children(func() {
		dot(c, pal.meta, 6, true, false)
		label(c, "loading").TextColor(pal.meta)
	})
}

// turn: what the person said sits at right on a soft ground (a peer's says it came by Bridge); Orb
// just speaks, full width, its actions inline.
func (a *app) turn(c *ui.Context, rows []Row, first bool, t *Tab) {
	pal := p(c)
	if len(rows) == 1 && rows[0].Kind == "you" {
		you := rows[0]
		top := float32(28)
		if first {
			top = 12
		}
		reading(c).Padding(top, margin, 16, 48).Gap(6).AlignItems(ui.End).Selectable().Children(func() {
			if you.Via != "" {
				ui.Text(c, "from a peer").FontSize(labelSize).TextColor(blue)
			}
			a.pictures(c, you.Images, t)
			if strings.TrimSpace(you.Text) != "" {
				tokens(c, you.Text).Padding(8, 12).Radius(pane).Background(pal.fg.Alpha(0.07)).FontSize(size)
			}
		})
		return
	}
	reading(c).Padding(4, margin, 8).Gap(8).Selectable().Children(func() {
		for _, r := range rows {
			switch r.Kind {
			case "md":
				if r.Block != nil {
					markdown(c.Key(r.Key), *r.Block, size, pal.fg)
				}
			case "run":
				if len(r.Actions) == 1 {
					a.action(c.Key(r.Key), r.Actions[0], t)
				} else {
					a.worked(c.Key(r.Key), r, t)
				}
				// What the tools showed the model stays in view, even while their run is folded.
				a.pictures(c, r.Images, t)
			case "note":
				ink := pal.meta
				if r.Alarm {
					ink = rupture
				}
				a.folded(c.Key(r.Key), r.Text, ink)
			}
		}
	})
}

// fold opens and closes what a line holds: its height and its ink together, in 200 ms.
var fold = ui.ElementTransition{Enter: &ui.Motion{Collapse: true}, Exit: &ui.Motion{Collapse: true}, Animation: &opening}

// worked folds a run of actions into one line, as the TUI folds exploration, naming what runs now;
// it opens to the actions.
func (a *app) worked(c *ui.Context, r Row, t *Tab) {
	open := a.expanded[t.ID+r.Key]
	ui.Column(c).FillWidth().Children(func() {
		verb, result, now := "worked", "", ""
		if r.Live {
			verb = "working"
		}
		if r.Failed > 0 {
			result = fmt.Sprintf("%d failed", r.Failed)
		}
		if !open && r.Now != "" {
			now = " · " + r.Now
		}
		if press(actionLine(c, verb, r.What+now, result, r.Live, r.Failed > 0, &open)) {
			a.expanded[t.ID+r.Key] = !open
		}
		if open {
			ui.Column(c).FillWidth().PaddingStart(16).Transition(fold).Children(func() {
				for _, x := range r.Actions {
					a.action(c.Key(x.Key), x, t)
				}
			})
		}
	})
}

// action is a thought or a tool call: one line that opens into its whole text, or what it was given
// and returned.
func (a *app) action(c *ui.Context, x Action, t *Tab) {
	pal := p(c)
	key := t.ID + x.Key
	open := a.expanded[key]
	ui.Column(c).FillWidth().Children(func() {
		if press(actionLine(c, x.Verb, x.Target, x.Result, x.Live, x.Failed, &open)) {
			a.expanded[key] = !open
			if !open {
				// Read again each time it opens: a live action's output grows.
				then(a.o, func(d map[string]string, err error) { a.details[key] = d }, "detail", "tab", t.ID, "key", x.Key)
			}
		}
		if !open {
			return
		}
		d := a.details[key]
		ui.Column(c).FillWidth().Margin(4, 0, 0, 16).Padding(12).Gap(8).Radius(pane).Background(pal.raised).Border(1, pal.rule).Transition(fold).Children(func() {
			if x.Verb == "thought" {
				ui.Text(c, d["text"]).FontSize(size - 2).TextColor(pal.mute)
				return
			}
			if d["args"] != "" {
				label(c, "input").TextColor(pal.meta)
				ui.Text(c, d["args"]).FontSize(12).TextColor(pal.mute)
			}
			label(c, "output").TextColor(pal.meta)
			out, ink := d["output"], pal.fg
			if d == nil {
				out = "…"
			}
			if x.Failed {
				ink = rupture
			}
			ui.Text(c, out).FontSize(12).TextColor(ink)
		})
	})
}

// folded is a long note or error folded to three lines; a click opens it.
func (a *app) folded(c *ui.Context, text string, ink ui.Color) ui.Element {
	e := ui.Text(c, text).FontSize(small + 1).TextColor(ink)
	open := ui.Local(e, "open", func() bool { return false })
	if !*open {
		e.MaxLines(3)
	}
	if press(e) {
		*open = !*open
	}
	return e
}

// tokenPattern finds cited files, skills and references, which stay recognisable after sending.
var tokenPattern = regexp.MustCompile(`(^|\s)(/skill:[\w.-]+|@[^\s]+|#[\w-]+)`)

func tokens(c *ui.Context, text string) ui.Element {
	pal := p(c)
	return ui.RichText(c).Children(func() {
		at := 0
		for _, m := range tokenPattern.FindAllStringSubmatchIndex(text, -1) {
			ui.Text(c, text[at:m[4]])
			tok := ui.Text(c, text[m[4]:m[5]])
			if text[m[4]] == '/' {
				tok.TextColor(pal.bg).TextBackground(pal.fg)
			} else {
				tok.Underline()
			}
			at = m[5]
		}
		ui.Text(c, text[at:])
	})
}

// pictures is a row of thumbnails, fetched small from the Orb; a click shows one whole.
func (a *app) pictures(c *ui.Context, refs []string, t *Tab) {
	if len(refs) == 0 {
		return
	}
	ui.ScrollHorizontal(c).FillWidth().Children(func() {
		ui.Row(c).Gap(6).Children(func() {
			for _, ref := range refs {
				if press(a.picture(c.Key(ref), ref, t.ID, 384).Height(112).Radius(soft).Clip()) {
					a.viewing = [2]string{ref, t.ID}
				}
			}
		})
	})
}

// decoded is an image as an Orb sends it, decoded with its reply, off the main thread: a picture
// shown whole takes a frame or more to decode.
type decoded struct{ b *ui.Bitmap }

func (p *decoded) UnmarshalJSON(b []byte) error {
	var r struct{ Data string }
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(r.Data)
	if err == nil {
		p.b, err = ui.DecodeBitmap(raw)
	}
	return err
}

// picture is an image fetched at px, already fitted by its Orb, the first time it shows.
func (a *app) picture(c *ui.Context, ref, tab string, px int) ui.Element {
	key := fmt.Sprint(ref, ":", px)
	if b := a.pics[key]; b != nil {
		return ui.Image(c, b)
	}
	if !a.loading[key] {
		a.loading[key] = true
		then(a.o, func(r decoded, err error) {
			if err == nil {
				a.pics[key] = r.b
			}
			if a.pics[key] == nil {
				delete(a.loading, key) // asked again when it next shows
			}
		}, "image", "tab", tab, "ref", ref, "px", px)
	}
	return ui.Box(c).Size(96, 96).Background(p(c).raised)
}

// viewer shows an image whole, fetched at the window's size; a click or Escape closes it.
func (a *app) viewer(c *ui.Context) {
	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.92))
		panel.FillWidth().FillHeight().Padding(24)
		if press(a.picture(c, a.viewing[0], a.viewing[1], 2048).Fill().Fit(ui.Contain)) {
			open = false
		}
	})
	if !open {
		a.viewing = [2]string{}
	}
}

// markdown is one block of what Orb wrote, parsed by the view: a paragraph, heading, list item,
// quote, code, drawing, table or rule.
func markdown(c *ui.Context, b Block, size float32, ink ui.Color) {
	pal := p(c)
	indent := float32(b.Depth * 18)
	switch b.Type {
	case "p":
		spans(c, b.Spans).MarginStart(indent).FontSize(size).TextColor(ink)
	case "h":
		s := size + 1
		if b.Level <= 2 {
			s = size + 3
		}
		spans(c, b.Spans).Margin(6, 0, 0, indent).FontSize(s).FontWeight(strong).LetterSpacing(-0.02 * s).LineHeight(1.25).TextColor(ink)
	case "li":
		ui.Row(c).FillWidth().PaddingStart(indent).AlignItems(ui.Start).Children(func() {
			w := size * 1.8
			if b.Mark == "·" {
				w = size + 1
			}
			ui.Text(c, b.Mark).Width(w).FontSize(size).FontWeight(medium).TextColor(pal.meta)
			spans(c, b.Spans).Grow(1).Basis(0).FontSize(size).TextColor(ink)
		})
	case "quote":
		// A quote holds blocks of its own, its bar as tall as they are.
		ui.Column(c).FillWidth().MarginStart(indent).PaddingStart(14).Gap(size*0.6).BorderWidth(0, 0, 0, 2).BorderColor(pal.rule).Children(func() {
			for _, q := range b.Blocks {
				markdown(c, q, size, pal.mute)
			}
		})
	case "code", "art":
		ui.Column(c).FillWidth().MarginStart(indent).Padding(12).Radius(pane).Background(pal.raised).Border(1, pal.rule).Children(func() {
			ui.Row(c).FillWidth().Margin(0, 0, 6).Children(func() {
				if b.Lang != "" {
					label(c, b.Lang).TextColor(pal.meta)
				}
				ui.Spacer(c)
				if press(ui.Text(c, "copy").FontSize(labelSize).TextColor(pal.meta)) {
					mygo.Clipboard.WriteText(b.Text)
				}
			})
			if b.Type == "art" {
				// A drawing scrolls, its lines flush.
				ui.ScrollHorizontal(c).FillWidth().Children(func() {
					ui.Text(c, b.Text).NoWrap().FontSize(size - 3).LineHeight(1.1)
				})
			} else {
				// Tabs draw as nothing in a text: code keeps its indentation as spaces.
				ui.Text(c, strings.ReplaceAll(b.Text, "\t", "    ")).FontSize(size - 3).LineHeight(1.5).TextColor(pal.fg)
			}
		})
	case "table":
		table(c, b.Rows, size, indent)
	case "rule":
		ui.Box(c).Height(4)
	}
}

// spans is styled runs as text: bold, italic, code on a raised ground, struck, links that open.
func spans(c *ui.Context, ss []Span) ui.Element {
	pal := p(c)
	return ui.RichText(c).Children(func() {
		for _, s := range ss {
			var e ui.Element
			if s.Href != "" {
				e = ui.Link(c, s.Text, s.Href)
			} else {
				e = ui.Text(c, s.Text)
			}
			if s.Bold {
				e.FontWeight(strong)
			}
			if s.Italic {
				e.Italic()
			}
			if s.Strike {
				e.Strikethrough()
			}
			if s.Code {
				e.FontSize(size - 1.5).TextColor(pal.mute).TextBackground(pal.raised)
			}
		}
	})
}

// table is as wide as its cells (each column up to 28 characters, wrapping beyond); a wide one
// scrolls sideways, and fades at the side more of it is.
func table(c *ui.Context, rows [][][]Span, size, indent float32) {
	pal := p(c)
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	widths := make([]float32, cols)
	for i := range widths {
		n := 3
		for _, r := range rows {
			if i < len(r) {
				l := 0
				for _, s := range r[i] {
					l += len([]rune(s.Text))
				}
				n = max(n, min(l, 28))
			}
		}
		widths[i] = float32(n)*(size-1)*0.58 + 16
	}
	sc := ui.ScrollHorizontal(c).FillWidth().MarginStart(indent)
	at := ui.Local(sc, "scroll", func() ui.ScrollState { return ui.ScrollState{} })
	sc.TrackScroll(at).DrawOver(func(pt *ui.Painter, r ui.Rect) {
		if at.X < at.MaxX-1 {
			x := r.X + r.W - 32
			pt.FillPathGradient((&ui.Path{}).MoveTo(x, r.Y).LineTo(r.X+r.W, r.Y).LineTo(r.X+r.W, r.Y+r.H).LineTo(x, r.Y+r.H).Close(),
				ui.LinearGradient{From: pal.bg.Alpha(0), To: pal.bg, Angle: 90})
		}
	})
	sc.Children(func() {
		ui.Column(c).Radius(soft).Border(1, pal.rule).Clip().Dividers(1, pal.rule).Children(func() {
			for r, row := range rows {
				e := ui.Row(c).AlignItems(ui.Stretch)
				if r == 0 {
					e.Background(pal.raised)
				}
				e.Children(func() {
					for i := range cols {
						var cell []Span
						if i < len(row) {
							cell = row[i]
						}
						w := 400
						if r == 0 {
							w = strong
						}
						spans(c, cell).Width(widths[i]).Padding(6, 8).FontSize(size - 1).FontWeight(w)
					}
				})
			}
		})
	})
}

// interrupt is a plugin stopping the world: the only place red fills space. The word names who is
// asking; answer gets nil to dismiss it.
func (a *app) interrupt(c *ui.Context, ask Ask, answer func(*string)) {
	word, caption := "QUESTION", "A QUESTION FOR YOU"
	switch ask.Owner {
	case "permissions":
		word, caption = "APPROVAL", "APPROVAL REQUIRED"
	case "bridge":
		word, caption = "PAIR", "PAIRING REQUEST"
	}
	ink := charcoal
	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(rupture).Transition(ui.ElementTransition{Enter: &ui.Motion{}, Animation: &opening})
		panel.Fill().Background(rupture).Padding(c.TitleBar().Height, 0, 0).Transition(ui.ElementTransition{Enter: &ui.Motion{}, Animation: &opening})
		// Framed in warning stripes, as an emergency is in Evangelion.
		hazard(c, 14, rupture, ink)
		reply := func(v string) { answer(&v) }
		// A key answers only once the question has been on screen a moment, so typing meant for the
		// prompt cannot answer it; a pairing takes a click, after reading the fingerprint.
		if _, ok := a.asked[ask.ID]; !ok {
			a.asked[ask.ID] = time.Now()
		}
		armed := ask.Owner != "bridge" && time.Since(a.asked[ask.ID]) > 800*time.Millisecond
		ui.Column(c).FillWidth().MaxWidth(900).Margin(0, ui.Auto).Grow(1).Children(func() {
			ui.Row(c).FillWidth().Padding(16, margin).Children(func() {
				label(c, ask.Owner).Grow(1).TextColor(ink)
				ui.Text(c, "waiting on you").FontSize(labelSize).TextColor(ink)
			})
			rule(c, ink)
			squeeze := float32(1)
			if len(word) > 5 {
				squeeze = 0.6
			}
			ui.Column(c).Padding(22, margin, 10).Gap(10).Children(func() {
				// The word wipes in from the left, then its caption: a rare moment, so it may take its time.
				title := stretch(c, word, 150, ink, squeeze)
				wipe(title, reveal(title, ask.ID, 0), rupture)
				cap := label(c, caption).TextColor(ink)
				cap.Opacity(reveal(cap, ask.ID, 140*time.Millisecond))
			})
			// Orb's prompts put the request on the first line and its evidence below.
			first, rest, _ := strings.Cut(ask.Title, "\n")
			ui.Text(c, first).PaddingX(margin).FontSize(title + 2).TextColor(ink)
			detail := strings.TrimSpace(strings.Join([]string{rest, ask.Message}, "\n"))
			if detail != "" {
				ui.Scroll(c).MaxHeight(220).Children(func() {
					ui.Text(c, detail).Padding(10, margin).FontSize(small + 1).TextColor(ink.Alpha(0.8)).Selectable()
				})
			}
			ui.Spacer(c)
			rule(c, ink)
			for i, choice := range ask.Choices {
				// Orb's own prompts lead with their key ("y approve once"); others get one.
				key, text := "", choice
				if len(choice) > 2 && choice[1] == ' ' && choice[0] >= 'a' && choice[0] <= 'z' {
					key, text = choice[:1], choice[2:]
				} else {
					key = map[string]string{"allow": "y", "deny": "n"}[choice]
					if key == "" && i < 9 {
						key = fmt.Sprint(i + 1)
					}
				}
				fg, bg := ink, ui.Transparent
				if i == 0 {
					fg, bg = rupture, ink
				}
				row := ui.Row(c.Key(choice)).FillWidth().Padding(14, 20).Background(bg).Children(func() {
					ui.Text(c, key).Width(36).FontWeight(strong).TextColor(fg)
					ui.Text(c, text).TextColor(fg)
				})
				if press(row) || armed && hotkey(panel, key) {
					reply(choice)
				}
			}
			if ask.Free {
				ui.Row(c).FillWidth().Padding(16).Gap(8).Children(func() {
					v := a.answer[ask.ID]
					in := ui.TextInputBase(c, &v).Grow(1).Padding(12, 18).Border(1, ink).Radius(soft).TextColor(ink).
						Placeholder("type an answer").PlaceholderColor(ink.Alpha(0.6)).AutoFocus()
					a.answer[ask.ID] = v
					if in.Submitted() || btn(c, "send", true, ink, rupture) {
						delete(a.answer, ask.ID)
						reply(v)
					}
				})
			}
			rule(c, ink)
			if press(ui.Text(c, "esc  ·  dismiss").Padding(16).FontSize(labelSize).TextColor(ink)) {
				open = false
			}
		})
		hazard(c, 14, rupture, ink)
	})
	if !open {
		answer(nil)
	}
}

// hotkey reports the key of a choice pressed while the panel shows: a letter or a digit.
func hotkey(panel ui.Element, key string) bool {
	if len(key) != 1 {
		return false
	}
	switch k := key[0]; {
	case k >= 'a' && k <= 'z':
		return panel.OverlayShortcut(0, ui.KeyA+ui.Key(k-'a'))
	case k >= '1' && k <= '9':
		return panel.OverlayShortcut(0, ui.Key1+ui.Key(k-'1'))
	}
	return false
}

// reasoning says each level in words.
var reasoning = map[string]string{"off": "no thinking", "minimal": "barely", "low": "light", "medium": "balanced", "high": "thorough", "xhigh": "very thorough", "max": "as long as it takes"}

// modelSheet: the model and its reasoning on top, every model below, those in use first, then
// grouped by provider and narrowed by search; the accounts of its provider on the machine it runs
// on. With no conversation (Home, Providers) it chooses the default new conversations start on,
// on its machine.
func (a *app) modelSheet(c *ui.Context, t *Tab) {
	o, pal := a.o, p(c)
	open := true
	home, def := t.ID == "", a.defaults[t.Peer]
	provider, name, _ := strings.Cut(t.Model, "/")
	if a.accountsOf != t.Peer {
		a.accountsOf, a.accounts = t.Peer, nil
		peer := t.Peer
		then(o, func(list []Account, err error) {
			if a.accountsOf == peer {
				a.accounts = list
			}
		}, "accounts", "machine", peer)
	}
	body := func() {
		ui.Row(c).FillWidth().Padding(14, 16, 0).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
				label(c, map[bool]string{true: "default · " + t.Where, false: "model"}[home]).TextColor(pal.meta)
				ui.Text(c, cmp.Or(name, "none")).FontSize(16).FontWeight(strong).SingleLine()
			})
			if !home && def != nil && t.Model != "" && t.Model != def.Model {
				if btn(c, "make default", false, pal.fg, pal.bg) {
					a.makeDefault(t.Peer, t.Model, t.Thinking)
				}
			} else {
				ui.Text(c, strings.Join(slices.DeleteFunc([]string{provider, map[bool]string{false: t.Where}[home]}, func(s string) bool { return s == "" }), " · ")).
					Margin(0, 0, 2, 12).FontSize(12).TextColor(pal.meta).SingleLine()
			}
			if press(control(c, func() { ui.Text(c, "providers ›").FontSize(small).FontWeight(medium).TextColor(pal.mute) }).MarginStart(8).Tooltip("Connect and edit providers  " + keys(","))) {
				a.model, a.query = "", ""
				a.push(screen{kind: "providers", peer: t.Peer})
			}
		})
		field(c, &a.query, "Search models", false).Margin(10, 12, 6).AutoFocus()
		ui.Scroll(c).FillWidth().MaxHeight(300).Shrink(1).Padding(0, 4, 4).Children(func() {
			q := strings.ToLower(strings.TrimSpace(a.query))
			// The default and what the machine's open conversations run on, above the rest.
			var used []string
			use := func(id string) {
				if slices.Contains(t.Models, id) && !slices.Contains(used, id) {
					used = append(used, id)
				}
			}
			if def != nil {
				use(def.Model)
			}
			for _, x := range o.state.Tabs {
				if x.Peer == t.Peer {
					use(x.Model)
				}
			}
			var last string
			for i, id := range append(used, t.Models...) {
				if q != "" && !strings.Contains(strings.ToLower(id), q) {
					continue
				}
				group, short, _ := strings.Cut(id, "/")
				if i < len(used) {
					group, short = "in use", id
				}
				if group != last {
					last = group
					label(c.Key("h:"+group), group).TextColor(pal.meta).Padding(10, 12, 2)
				}
				if press(menuRow(c.Key(group+":"+id), short, map[bool]string{true: "default"}[def != nil && id == def.Model], id == t.Model)) {
					if home {
						a.makeDefault(t.Peer, id, "")
					} else {
						o.send("model", "tab", t.ID, "name", id)
					}
					a.model, a.query = "", ""
				}
			}
			if len(t.Models) == 0 {
				label(c, map[bool]string{true: "no model: connect a provider", false: "reading models…"}[t.Loaded]).TextColor(pal.meta).Padding(10, 12)
			}
		})
		a.accountsOn(c, t, provider)
		// Reasoning is what changes most often: it sits last.
		if len(t.Levels) > 0 {
			rule(c, pal.rule)
			ui.Column(c).FillWidth().Padding(10, 16, 14).Children(func() {
				ui.Row(c).FillWidth().Children(func() {
					label(c, "reasoning").Grow(1).TextColor(pal.meta)
					ui.Text(c, cmp.Or(reasoning[t.Thinking], t.Thinking)).FontSize(small).TextColor(pal.mute)
				})
				ui.Row(c).FillWidth().Margin(8, 0, 0).Radius(soft).Border(1, pal.fg).Clip().Children(func() {
					for _, l := range t.Levels {
						on := l == t.Thinking
						seg := ui.Row(c.Key(l)).Grow(1).Basis(0).PaddingY(9).Justify(ui.Center)
						seg.Background(seg.AnimateColor("on", map[bool]ui.Color{true: pal.fg, false: ui.Transparent}[on], ui.Smooth))
						ink := pal.fg
						if on {
							ink = pal.bg
						}
						seg.Children(func() {
							ui.Text(c, strings.NewReplacer("minimal", "min", "medium", "med").Replace(l)).FontSize(small).FontWeight(medium).TextColor(ink).SingleLine()
						})
						switch {
						case !press(seg):
						case home:
							a.makeDefault(t.Peer, t.Model, l)
						default:
							o.send("thinking", "tab", t.ID, "name", l)
						}
					}
				})
			})
		}
	}
	// Over a screen (Providers) no prompt box shows to open out of: the sheet is a dialog there.
	if len(a.stack) > 0 {
		modal(c, &open, 420, body)
	} else {
		popover(c, &a.active.modelAt, &open, 400, true, body)
	}
	if !open {
		a.model, a.query = "", ""
	}
}

// accountsOn lists the accounts the model's provider has on the machine the session runs on, as
// that machine lists them: the one in use marked, the others a click away with what their plans
// have left.
func (a *app) accountsOn(c *ui.Context, t *Tab, provider string) {
	pal := p(c)
	var mine []Account
	for _, x := range a.accounts {
		if x.Provider == provider {
			mine = append(mine, x)
		}
	}
	if len(mine) == 0 {
		return
	}
	rule(c, pal.rule)
	ui.Column(c).FillWidth().Padding(12, margin, 4).Children(func() {
		label(c, "account · "+t.Where).TextColor(pal.meta)
		for _, x := range mine {
			row := ui.Row(c.Key(x.ID)).FillWidth().PaddingY(8).Gap(10).Children(func() {
				ink, w := pal.rule, 400
				if x.Active {
					ink, w = pal.fg, strong
				}
				dot(c, ink, 8, false, false)
				ui.Text(c, x.Name).Grow(1).FontSize(15).FontWeight(w).SingleLine()
				if len(x.Windows) > 0 {
					win := slices.MinFunc(x.Windows, func(p, q Window) int { return int(p.Left - q.Left) })
					ink := pal.meta
					if win.Left < 15 {
						ink = rupture
					}
					ui.Textf(c, "%s %d%% left", win.Name, int(math.Round(win.Left))).FontSize(12).TextColor(ink).SingleLine()
				}
			})
			if !x.Active && press(row) {
				then(a.o, func(_ struct{}, err error) {
					if err != nil {
						a.say(err.Error())
					}
					a.accountsOf = "" // read them again
				}, "account", "machine", t.Peer, "provider", x.Provider, "account", x.ID)
			}
		}
	})
}
