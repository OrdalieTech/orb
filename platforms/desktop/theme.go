package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Ordalie tokens, as on Android (ui/Theme.kt): soft ink on sage paper, one rupture, a blue kept for
// remote Orbs acting here.
var (
	texte    = ui.Hex("#FAF9F6")
	rupture  = ui.Hex("#C94A3D")
	blue     = ui.Hex("#173E78")
	charcoal = ui.Hex("#2A2D2B")
)

// palette is paper, not white; charcoal, not black: the contrast of printed matter. Rules are drawn, not hinted.
type palette struct{ bg, fg, mute, meta, rule, raised ui.Color }

var (
	light = palette{ui.Hex("#E3E7E0"), charcoal, ui.Hex("#4F5550"), ui.Hex("#7E847F"), ui.Hex("#9EA49F"), ui.Hex("#ECEFE9")}
	dark  = palette{ui.Hex("#1C201E"), ui.Hex("#DADFD8"), ui.Hex("#A8AEA9"), ui.Hex("#7A817C"), ui.Hex("#4E5550"), ui.Hex("#242927")}
	// A peer's hue (1 to 6, as the view assigns them; 0, this computer, takes the ink): six tones
	// apart from each other and from the rupture red, light then dark.
	hues = [][2]string{{"#2F5FA8", "#6E9BE0"}, {"#1F7F73", "#4FBFAE"}, {"#9A6A12", "#D9A441"}, {"#6A47A8", "#A88BE0"}, {"#4A7A22", "#8CC255"}, {"#9C3F86", "#D77CC4"}}
)

// One scale, denser than the phone's: title 18 · body 14 · small 13 · label 10; three weights:
// regular reads, medium marks, semibold names.
const (
	title, body, small, labelSize = 18, 14, 13, 10
	medium, strong                = 500, 600
	soft, pane                    = 6, 10 // radii: what you press or type in, what holds something
	margin                        = 16
	mono                          = "Ubuntu Sans Mono"
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
	}
}

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

// label is small caps in medium weight; everything else is sentence case.
func label(c *ui.Context, s string) ui.Element {
	return ui.Text(c, strings.ToUpper(s)).FontSize(labelSize).FontWeight(medium).LetterSpacing(0.2).TextColor(p(c).mute).SingleLine()
}

// press makes e give way under the pointer, and reports a click on it.
func press(e ui.Element) bool {
	e.Cursor(ui.CursorPointer)
	if e.Pressed() {
		e.Opacity(0.75)
	}
	return e.Clicked()
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
	in := ui.TextInputBase(c, value).Placeholder(hint).PlaceholderColor(pal.meta).Padding(8, 12).Radius(soft).Border(1, edge).FontSize(body)
	if secret {
		in.Password()
	}
	return in
}

// dot marks what lives: round, or square for where a session runs; it pulses while it works.
func dot(c *ui.Context, color ui.Color, size float32, pulse, square bool) ui.Element {
	e := ui.Box(c).Size(size, size).Background(color)
	if !square {
		e.Radius(size / 2)
	}
	if pulse {
		e.Opacity(0.25 + 0.75*e.Loop("pulse", 1400*time.Millisecond, ui.EaseInOut))
	}
	return e
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
func stretch(c *ui.Context, text string, height float32, color ui.Color, squeeze float32) ui.Element {
	size := height / 1.9 / 0.7 // the cap height of Ubuntu Sans Mono is about 0.7em
	return ui.Row(c).Height(height).Children(func() {
		ui.Text(c, text).Font(mono).FontSize(size).Bold().FixedLineHeight(size).LetterSpacing(-0.02*size).TextColor(color).NoWrap().
			Scale(squeeze, 1.9).TransformOrigin(0, 0.5)
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
		ui.Text(c, target).Grow(1).Basis(0).FontSize(small).TextColor(ink).SingleLine()
		r := pal.meta
		if failed {
			r = rupture
		}
		ui.Text(c, result).MaxWidthPercent(30).MarginStart(8).FontSize(12).TextColor(r).SingleLine()
	})
}

// header is the bar of every screen below Home and its conversations: back, the name, what it
// shows, actions at right.
func header(c *ui.Context, name, sub string, back func(), right func()) {
	pal := p(c)
	ui.Row(c).FillWidth().Padding(6, 12, 10, 4).Gap(4).Children(func() {
		if press(ui.Row(c).PaddingX(10).Children(func() { ui.Text(c, "‹").FontSize(26).TextColor(pal.fg) })) {
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

// ago is how long ago, in a word.
func ago(ms int64) string {
	m := time.Since(time.UnixMilli(ms)).Minutes()
	switch {
	case m < 1:
		return "now"
	case m < 60:
		return strconv.Itoa(int(m)) + "m"
	case m < 1440:
		return strconv.Itoa(int(m/60)) + "h"
	}
	return strconv.Itoa(int(m/1440)) + "d"
}
