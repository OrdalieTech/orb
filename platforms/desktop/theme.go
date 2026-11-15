package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
)

// ordalie is the maker's mark, kept small and faint where the app waits on you, as the TUI's
// empty chat shows it.
//
//go:embed ordalie.svg
var ordalieSVG []byte
var ordalie = ui.MustParseSVG(ordalieSVG)

// Ordalie tokens, as on Android (ui/Theme.kt): soft ink on sage paper, one rupture, a blue kept for
// remote Orbs acting here.
var (
	texte    = ui.Hex("#FAF9F6")
	rupture  = ui.Hex("#C94A3D")
	blue     = ui.Hex("#173E78")
	charcoal = ui.Hex("#2A2D2B")
)

// palette is paper, not white; charcoal, not black: the contrast of printed matter. Rules are drawn,
// not hinted; meta text keeps 4.4:1 on paper, and hover is a ground the eye sees (raised is not).
type palette struct{ bg, fg, mute, meta, rule, raised, hover ui.Color }

var (
	light = palette{ui.Hex("#E3E7E0"), charcoal, ui.Hex("#444A45"), ui.Hex("#646A65"), ui.Hex("#9EA49F"), ui.Hex("#ECEFE9"), ui.Hex("#D6DAD3")}
	dark  = palette{ui.Hex("#1C201E"), ui.Hex("#DADFD8"), ui.Hex("#A8AEA9"), ui.Hex("#8A918C"), ui.Hex("#4E5550"), ui.Hex("#242927"), ui.Hex("#2F3331")}
	// A peer's hue (1 to 6, as the view assigns them; 0, this computer, takes the ink): six tones
	// apart from each other and from the rupture red, light then dark.
	hues = [][2]string{{"#2F5FA8", "#6E9BE0"}, {"#1F7F73", "#4FBFAE"}, {"#9A6A12", "#D9A441"}, {"#6A47A8", "#A88BE0"}, {"#4A7A22", "#8CC255"}, {"#9C3F86", "#D77CC4"}}
)

// One scale, denser than the phone's: title 18 · body 14 · small 13 · label 10; three weights:
// regular reads, medium marks, semibold names. One face, the Android app's: Ubuntu Sans Mono, set
// without tracking at reading sizes (a monospace's cells are already tight), opened up for small
// caps, closed a little for titles.
const (
	title, body, small, labelSize = 18, 14, 13, 10
	medium, strong                = 500, 600
	soft, pane                    = 6, 10 // radii: what you press or type in, what holds something
	margin                        = 16
	mono                          = "Ubuntu Sans Mono"
)

// Motion, in Emil Kowalski's terms: strong curves rather than the built-in ones, and UI under 300 ms.
// Entering and pressing ease out; what moves on screen eases in and out. Nothing a key does animates.
var (
	easeOut = ui.CubicBezier(0.23, 1, 0.32, 1)

	pressing = ui.Animation{Duration: 120 * time.Millisecond, Ease: easeOut}                            // a press gives way
	hovering = ui.Animation{Duration: 120 * time.Millisecond, Ease: ui.CubicBezier(0.25, 0.1, 0.25, 1)} // a colour, eased both ways
	opening  = ui.Animation{Duration: 200 * time.Millisecond, Ease: easeOut}                            // popovers, folds
	// A tab's content coming in under its bar, by pointer only: quick and short.
	switching = ui.Animation{Duration: 180 * time.Millisecond, Ease: easeOut}
	// A passing note rises and sinks, softer than the rest.
	passing = ui.Animation{Duration: 400 * time.Millisecond, Ease: ui.CubicBezier(0.25, 0.1, 0.25, 1)}
	showing = ui.Animation{Duration: 250 * time.Millisecond, Ease: easeOut} // dialogs
	// Tiles taking new places, and a transcript following what streams in: a spring keeps its pace
	// when more comes on the way.
	moving = ui.Animation{Spring: ui.SpringOptions{Duration: 300 * time.Millisecond}}
)

// p is the palette of the appearance the system shows.
func p(c *ui.Context) *palette {
	if c.Theme().Dark {
		return &dark
	}
	return &light
}

func hue(c *ui.Context, n int, here ui.Color) ui.Color {
	if n <= 0 {
		return here
	}
	h := hues[(n-1)%len(hues)]
	if c.Theme().Dark {
		return ui.Hex(h[1])
	}
	return ui.Hex(h[0])
}

// loadFont registers Ubuntu Sans Mono, the face the apps share, from the app's resources; the
// system's monospaced font stands in without it.
func loadFont() {
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return
	}
	if b, err := os.ReadFile(filepath.Join(dir, "ubuntu_sans_mono.ttf")); err == nil {
		_ = ui.RegisterFont(b, mono)
		if face, err := font.ParseTTF(bytes.NewReader(b)); err == nil {
			face.SetVariations([]font.Variation{{Tag: ot.MustNewTag("wght"), Value: 700}})
			bold = face
		}
	}
}

// bold is Ubuntu Sans Mono Bold, whose outlines the title cards draw; nil without the font.
var bold *font.Face

// theme makes the system's light or dark theme Orb's: its face, its paper and ink.
func theme(c *ui.Context) {
	t := *c.Theme()
	pal := p(c)
	t.Font, t.FontSize, t.Radius = mono+", monospace", body, soft
	t.Background, t.Text, t.TextMuted, t.Border = pal.bg, pal.fg, pal.meta, pal.rule
	t.Surface, t.SurfaceHover, t.SurfacePressed = pal.raised, pal.raised.Mix(pal.fg, 0.04), pal.raised.Mix(pal.fg, 0.08)
	t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = pal.fg, pal.fg, pal.mute, pal.bg
	t.Danger, t.Focus, t.Selection = rupture, pal.mute, pal.fg.Alpha(0.18)
	t.Inverse, t.InverseText = pal.fg, pal.bg
	c.SetTheme(&t)
}

// label is small caps in medium weight, tracked out as small text wants; everything else is
// sentence case.
func label(c *ui.Context, s string) ui.Element {
	return ui.Text(c, strings.ToUpper(s)).FontSize(labelSize + 1).FontWeight(medium).LetterSpacing(0.9).TextColor(p(c).mute).SingleLine()
}

// press makes e give way under the pointer the moment it goes down (scaled to 0.97), and reports a
// click on it.
func press(e ui.Element) bool {
	e.Cursor(ui.CursorPointer)
	// A pose repaints without building the view again; 0.98, as wide rows give way too much at 0.97.
	e.AnimatePose(ui.Pose{Scale: map[bool]float32{true: 0.98, false: 1}[e.Pressed()]}, pressing)
	return e.Clicked()
}

// hover is the ground an element takes under the pointer, eased in and out.
func hover(e ui.Element, on ui.Color) ui.Element {
	return e.Background(e.AnimateColor("hover", map[bool]ui.Color{true: on, false: on.Alpha(0)}[e.Hovered()], hovering))
}

// link is a small action in words, unlike the labels it sits by: sentence case, its key after
// it, a ground under the pointer; its words stay on the column's edge.
func link(c *ui.Context, text, key string) bool {
	pal := p(c)
	return press(hover(ui.Row(c).Height(24).Padding(0, 8).Margin(0, -8).Gap(8).Radius(soft).AlignItems(ui.Center), pal.hover).Children(func() {
		ui.Text(c, text).FontSize(12).FontWeight(medium).TextColor(pal.mute)
		if key != "" {
			ui.Text(c, key).FontSize(12).TextColor(pal.meta)
		}
	}))
}

// empty says a list has nothing to show, and why, where its rows would be.
func empty(c *ui.Context, text string) ui.Element {
	return ui.Text(c, text).FontSize(12).TextColor(p(c).meta).PaddingY(10)
}

// rule is a hairline separating regions (turns, rows of a table, the prompt), never words.
func rule(c *ui.Context, color ui.Color) ui.Element {
	return ui.Box(c).Height(1).FillWidth().Background(color)
}

// btn is the one button: an outline that fills when it matters; it reports a click.
func btn(c *ui.Context, text string, inverted bool, color, on ui.Color) bool {
	e := ui.Row(c).Padding(7, 14).Radius(soft).Border(1, color).Justify(ui.Center)
	fg := color
	if inverted {
		e.Background(color)
		fg = on
	}
	e.Children(func() { ui.Text(c, text).FontSize(small).FontWeight(strong).TextColor(fg).SingleLine() })
	return press(e)
}

// chip is a small mark: inverted, outlined or quiet.
func chip(c *ui.Context, text string, kind int) ui.Element {
	pal := p(c)
	bg, fg, edge := ui.Transparent, pal.fg, pal.fg
	switch kind {
	case inverted:
		bg, fg = pal.fg, pal.bg
	case quiet:
		fg, edge = pal.mute, pal.rule
	}
	return ui.Row(c).Padding(2, 8).Radius(soft).Background(bg).Border(1, edge).Children(func() {
		ui.Text(c, text).FontSize(12).FontWeight(medium).TextColor(fg).SingleLine()
	})
}

const (
	outline = iota
	inverted
	quiet
)

// field is the one text field: a round outline that firms up once it holds something.
func field(c *ui.Context, value *string, hint string, secret bool) ui.Element {
	pal := p(c)
	edge := pal.rule
	if *value != "" {
		edge = pal.fg
	}
	in := ui.TextInputBase(c, value).Placeholder(hint).PlaceholderColor(pal.meta).Padding(6, 10).Radius(soft).Border(1, edge).FontSize(body)
	if secret {
		in.Password()
	}
	return in
}

// beat is the period of a dot's pulse, and beatRate the frames a second its fade looks as smooth
// at as at the display's: something works for minutes, and the window draws only the dot meanwhile.
const beat, beatRate = 1400 * time.Millisecond, 20

// dot marks what lives: round, or square for where a session runs; it pulses while it works.
func dot(c *ui.Context, color ui.Color, size float32, pulse, square bool) ui.Element {
	e := ui.Box(c).Size(size, size)
	radius := size / 2
	if square {
		radius = 0
	}
	// Reduce Motion keeps it still: a slow loop is what it asks to be spared.
	if !pulse || c.Preferences().ReduceMotion {
		return e.Background(color).Radius(radius)
	}
	// Painted, not built: a pulse built into the view (Element.Loop) builds the whole view every frame.
	return e.Draw(func(p *ui.Painter, r ui.Rect) {
		// It breathes: down and back up again in each beat.
		at := float32(p.Now().UnixNano()%int64(beat)) / float32(beat)
		p.Fill(r, color.Alpha(0.25+0.75*ui.EaseInOut(abs(1-2*at))), radius)
		p.AnimationFrameRate(beatRate)
	})
}

// where is where a session runs, as a square: the ink for this computer, a paired device's own hue,
// red while it asks you something; it pulses while it works. Tabs, rows and the prompt box share it.
func where(c *ui.Context, n int, live, asks bool, here ui.Color) ui.Element {
	color := hue(c, n, here)
	if asks {
		color = rupture
	}
	return dot(c, color, 7, live, true)
}

// stretch is the EVA title card: Ubuntu Sans Mono Bold stretched ×1.9 tall, squeezed as needed;
// reserved for events and identities.
//
// The capitals are the font's own outlines, stretched as vectors and filled at the screen's
// resolution: text laid out and then scaled would stretch its pixels.
func stretch(c *ui.Context, text string, height float32, color ui.Color, squeeze float32) ui.Element {
	if bold == nil {
		size := height / 1.9 / 0.7 // the cap height of Ubuntu Sans Mono is about 0.7em
		return ui.Row(c).Height(height).Children(func() {
			ui.Text(c, text).FontSize(size).Bold().FixedLineHeight(size).TextColor(color).NoWrap().Scale(squeeze, 1.9).TransformOrigin(0, 0.5)
		})
	}
	h, _ := bold.NominalGlyph('H')
	caps, _ := bold.GlyphExtents(h)
	sy := height / caps.YBearing // font units to DIPs, capitals filling the height
	sx := sy / 1.9 * squeeze
	// Letters are set by their ink, a fixed gap apart, as Matisse is: the mono's equal advances
	// would leave an I or an S floating in its cell.
	em := float32(bold.Upem())
	set := func(at func(g ot.GID, x float32)) (x float32) {
		for i, r := range text {
			if r == ' ' {
				x += 0.3 * em
				continue
			}
			g, _ := bold.NominalGlyph(r)
			ink, _ := bold.GlyphExtents(g)
			if i > 0 && text[i-1] != ' ' {
				x += 0.035 * em
			}
			at(g, x-ink.XBearing)
			x += ink.Width
		}
		return x
	}
	return ui.Box(c).Size(set(func(ot.GID, float32) {})*sx, height).Label(text).Draw(func(p *ui.Painter, b ui.Rect) {
		var path ui.Path
		set(func(g ot.GID, gx float32) {
			x := b.X + gx*sx
			if o, ok := bold.GlyphDataOutline(g); ok {
				for _, s := range o.Segments {
					a := s.Args
					px := func(i int) (float32, float32) { return x + a[i].X*sx, b.Y + height - a[i].Y*sy }
					switch s.Op {
					case ot.SegmentOpMoveTo:
						path.MoveTo(px(0))
					case ot.SegmentOpLineTo:
						path.LineTo(px(0))
					case ot.SegmentOpQuadTo:
						x1, y1 := px(0)
						x2, y2 := px(1)
						path.QuadTo(x1, y1, x2, y2)
					case ot.SegmentOpCubeTo:
						x1, y1 := px(0)
						x2, y2 := px(1)
						x3, y3 := px(2)
						path.CubeTo(x1, y1, x2, y2, x3, y3)
					}
				}
			}
		})
		p.FillPath(&path, color)
	})
}

// actionLine is one line per action (a tool call, a thought, a group of them): a mark, the verb,
// what it acted on, what came of it. Live actions get the dot; ones that open show › or ⌄.
func actionLine(c *ui.Context, verb, target, result string, live, failed bool, open *bool) ui.Element {
	pal := p(c)
	ink := pal.mute
	if live {
		ink = pal.fg
	}
	return ui.Row(c).FillWidth().PaddingY(3).Children(func() {
		ui.Row(c).Width(16).Children(func() {
			switch {
			case live:
				dot(c, rupture, 6, true, false)
			case open != nil:
				mark := "›"
				if *open {
					mark = "⌄"
				}
				ui.Text(c, mark).TextColor(pal.meta)
			}
		})
		ui.Text(c, verb).Width(80).FontSize(small).FontWeight(medium).TextColor(ink).SingleLine()
		ui.Text(c, target).Grow(1).Basis(0).FontSize(12).TextColor(ink).SingleLine().Tooltip(target)
		r := pal.meta
		if failed {
			r = rupture
		}
		ui.Text(c, result).MaxWidthPercent(30).MarginStart(8).FontSize(11).TextColor(r).SingleLine()
	})
}

// header is the bar of every screen below Home and its conversations: back, the name, what it
// shows, actions at right.
func header(c *ui.Context, name, sub string, back func(), right func()) {
	pal := p(c)
	// Without a way back, the name starts on the body's edge.
	ui.Row(c).FillWidth().Padding(6, 12, 10, map[bool]float32{true: 4, false: margin}[back != nil]).Gap(4).Children(func() {
		if back != nil && press(ui.Row(c).PaddingX(10).Children(func() { ui.Text(c, "‹").FontSize(26).TextColor(pal.fg) })) {
			back()
		}
		ui.Column(c).Grow(1).Basis(0).Children(func() {
			ui.Text(c, name).FontSize(title).FontWeight(strong).SingleLine()
			if sub != "" {
				ui.Text(c, sub).FontSize(small).TextColor(pal.meta).SingleLine()
			}
		})
		if right != nil {
			ui.Row(c).Gap(8).Children(right)
		}
	})
}

// page is a screen's header and its scrolling body, in a column of reading width.
func page(c *ui.Context, top func(), body func()) {
	ui.Column(c).FillWidth().MaxWidth(760).Margin(0, ui.Auto).Children(top)
	ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
		ui.Column(c).FillWidth().MaxWidth(760).Margin(0, ui.Auto).Padding(0, margin, 24).Gap(10).Children(body)
	})
}

// section is a labelled region of a screen: a rule, its name, its state at right.
func section(c *ui.Context, name, state string, body func()) {
	pal := p(c)
	ui.Column(c).FillWidth().Children(func() {
		rule(c, pal.rule)
		ui.Row(c).FillWidth().Padding(12, 0, 8, 0).Children(func() {
			label(c, name).Grow(1)
			ui.Text(c, state).FontSize(labelSize).TextColor(pal.meta)
		})
		body()
	})
}

// ago is how long ago, in a word, and a date past a week, which "45d" says worse.
func ago(ms int64) string {
	at := time.UnixMilli(ms)
	m := time.Since(at).Minutes()
	switch {
	case m < 1:
		return "now"
	case m < 60:
		return strconv.Itoa(int(m)) + "m"
	case m < 1440:
		return strconv.Itoa(int(m/60)) + "h"
	case m < 7*1440:
		return strconv.Itoa(int(m/1440)) + "d"
	case at.Year() == time.Now().Year():
		return at.Format("Jan 2")
	}
	return at.Format("Jan 2006")
}

func abs(x float32) float32 { return max(x, -x) }

// The NERV look, after Evangelion's interfaces: spent where it carries meaning (focus, a takeover,
// a gauge, a title card), never on every row.

// reveal is 0 to 1 over 260 ms, easing out, the first time e is built with key: what wipes or
// draws in plays it once. Reduce Motion skips to the end.
func reveal(e ui.Element, key any, delay time.Duration) float32 {
	return e.Keyframes(key, []ui.Keyframe{{At: 0, Value: 0}, {At: 260 * time.Millisecond, Value: 1, Ease: easeOut}}, ui.KeyframeOptions{Delay: delay})
}

// wipe hides what of e is right of v (0 to 1) under its ground, so e is uncovered from the left.
func wipe(e ui.Element, v float32, ground ui.Color) {
	if v < 1 {
		e.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + r.W*v, Y: r.Y - 4, W: r.W*(1-v) + 1, H: r.H + 8}, ground, 0)
		})
	}
}

// hazard is a band of warning stripes: ink slanting over ground.
func hazard(c *ui.Context, height float32, ground, ink ui.Color) ui.Element {
	return ui.Box(c).FillWidth().Height(height).Background(ground).Stripes(ink, 6, 6, 45)
}

// gauge is a reading in segments, as Evangelion shows a synchronisation rate: lit up to value
// (0 to 1), red past alarm.
func gauge(c *ui.Context, value float64, alarm float64) ui.Element {
	pal := p(c)
	const segments = 10
	lit := int(value*segments + 0.5)
	return ui.Row(c).Gap(2).Shrink(0).Children(func() {
		for i := range segments {
			ink := pal.rule.Alpha(0.6)
			switch {
			case i < lit && value > alarm:
				ink = rupture
			case i < lit:
				ink = pal.mute
			}
			// A change runs along the segments, 25 ms apart, as a reading climbs on a NERV screen.
			seg := ui.Box(c.Key(i)).Size(3, 10)
			seg.Background(seg.AnimateColor("lit", ink, ui.Animation{Duration: 120 * time.Millisecond, Ease: easeOut, Delay: time.Duration(i) * 15 * time.Millisecond}))
		}
	})
}

// hudLabel is a section's name as a HUD numbers it: its index, its name, its state at the edge.
func hudLabel(c *ui.Context, index int, name, state string) ui.Element {
	pal := p(c)
	return ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, fmt.Sprintf("%02d", index)).FontSize(labelSize).FontWeight(strong).TextColor(rupture)
		label(c, name).Shrink(1).MinWidth(0).TextColor(pal.meta)
		ui.Spacer(c)
		if state != "" {
			ui.Text(c, state).FontSize(labelSize).TextColor(pal.meta).Shrink(0)
		}
	})
}
