package modes

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

func TestAppearanceWatcherRecoversTransitionsAndKeepsLastGoodColors(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.SetCurrent(previous) })
	synctest.Test(t, func(t *testing.T) {
		terminal := &f12LifecycleInputTerminal{fakeTerminalImpl: newFakeTerminal(80, 24)}
		mode := &InteractiveMode{ui: tui.NewTUI(terminal)}
		mode.themeRegistry = theme.Load(theme.LoadOptions{NoThemes: true, Mode: theme.TrueColor})
		mode.themeController = theme.Initialize(mode.themeRegistry, "terminal", theme.Dark, mode.ui.Invalidate)
		if err := mode.ui.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = mode.ui.Stop() }()
		stop := mode.watchTerminalBackground(context.Background())
		synctest.Wait()
		terminal.send("\x1b[?997;2n")
		if theme.Current().Appearance() != "light" {
			t.Fatal("scheme-only terminal did not adopt light mode")
		}
		synctest.Wait()
		terminal.send("\x1b]11;#fffcef\x07")
		palette := theme.Current().Palette()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		mode.setTerminalBackground(nil)
		if theme.Current().Palette() != palette {
			t.Fatal("timeout discarded known colors")
		}
		terminal.send("\x1b[?997;1n")
		if theme.Current().Appearance() != "dark" {
			t.Fatal("dark transition kept a stale light panel while waiting for RGB")
		}
		synctest.Wait()
		terminal.send("\x1b]11;#181b20\x07")
		if got := theme.Current().ExportColors()["pageBg"]; got != "#181b20" {
			t.Fatalf("late/missing earlier replies poisoned detection: %s", got)
		}
		palette = theme.Current().Palette()
		mode.setTerminalBackground(&tui.RgbColor{R: 24, G: 27, B: 32})
		terminal.send("\x1b[?997;1n")
		if theme.Current().Palette() != palette {
			t.Fatal("unchanged reports rebuilt the palette")
		}
		// No scheme notification: the periodic query still detects the change.
		time.Sleep(5 * time.Second)
		synctest.Wait()
		terminal.send("\x1b]11;#ffffff\x07")
		if theme.Current().Appearance() != "light" {
			t.Fatal("missed notification left stale dark colors")
		}
		// A terminal may use a light profile despite a dark system preference.
		terminal.send("\x1b[?997;1n")
		if theme.Current().Appearance() != "light" {
			t.Fatal("unchanged preference overrode the measured terminal background")
		}
		stop()
		palette = theme.Current().Palette()
		terminal.send("\x1b[?997;1n")
		terminal.send("\x1b]11;#000000\x07")
		if theme.Current().Palette() != palette {
			t.Fatal("disposed watcher changed colors")
		}
	})
}

func TestOpenModalInvalidationRecolorsCachedMarkdown(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.SetCurrent(previous) })
	registry := theme.Load(theme.LoadOptions{NoThemes: true, Mode: theme.TrueColor})
	dark, _ := registry.Get("dark")
	theme.SetCurrent(dark)
	makeFrame := func() *tui.Frame {
		return menuFrame("Details", tui.NewMarkdown("# Heading\n\nText with `code`\n\n```go\nfmt.Println(42)\n```", 0, 0, theme.MarkdownTheme(), nil, nil))
	}
	frame := makeFrame()
	frame.Render(60)
	for _, name := range []string{"light", "dark", "light"} {
		current, _ := registry.Get(name)
		theme.SetCurrent(current)
		frame.Invalidate()
		if got, want := strings.Join(frame.Render(60), "\n"), strings.Join(makeFrame().Render(60), "\n"); got != want {
			t.Fatalf("%s modal retained cached markdown colors\n%q\nwant\n%q", name, got, want)
		}
	}
}

func TestOpenCommandPaletteRecolorsWithoutLosingSearchOrSelection(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.SetCurrent(previous) })
	registry := theme.Load(theme.LoadOptions{NoThemes: true, Mode: theme.TrueColor})
	native, _ := registry.Get("terminal")
	theme.SetCurrent(native)
	makeFrame := func() (*commandPalette, *tui.Frame) {
		rows := []tui.GridRow{
			{Value: "model", Cells: []string{theme.FG("text", "Choose model"), "ctrl+m"}, Search: "model", Detail: []string{"Choose a model"}},
			{Value: "settings", Cells: []string{"Settings"}, Search: "settings"},
		}
		palette := newCommandPalette(rows, NewAppKeybindings(nil), func() int { return 30 }, nil, nil)
		palette.SetFocused(true)
		palette.HandleInput(tui.KeyEvent{Raw: "m"})
		return palette, menuFrame("Commands", palette)
	}
	palette, frame := makeFrame()
	for _, name := range []string{"dark", "light", "terminal-light", "terminal-dark", "light", "dark"} {
		if strings.HasPrefix(name, "terminal-") {
			theme.SetCurrent(native)
			background := tui.RgbColor{R: 24, G: 27, B: 32}
			if name == "terminal-light" {
				background = tui.RgbColor{R: 255, G: 252, B: 239}
			}
			native.SetTerminalBackground(background)
		} else {
			current, _ := registry.Get(name)
			theme.SetCurrent(current)
		}
		for _, width := range []int{20, 40, 80, 120} {
			_, fresh := makeFrame()
			got, want := strings.Join(frame.Render(width), "\n"), strings.Join(fresh.Render(width), "\n")
			if got != want {
				t.Fatalf("%s at %d: open modal retained old styles\n%q\nwant\n%q", name, width, got, want)
			}
			if !strings.Contains(got, theme.BGANSI("selectedBg")) || !strings.HasPrefix(got, theme.FGANSI("text")) {
				t.Fatalf("%s: missing semantic selection or modal foreground", name)
			}
		}
		if palette.input.GetValue() != "m" || palette.list.SelectedValue() != "model" {
			t.Fatal("theme change reset the open menu")
		}
	}
}
