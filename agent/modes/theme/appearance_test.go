package theme

import (
	"fmt"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/internal/themefile"
	"github.com/OrdalieTech/orb/tui"
)

func TestTerminalPaletteReadableAcrossBackgrounds(t *testing.T) {
	backgrounds := []tui.RgbColor{{R: 12, G: 25, B: 48}, {R: 255, G: 252, B: 239}, {R: 85, G: 110, B: 90}}
	for gray := range 256 {
		backgrounds = append(backgrounds, tui.RgbColor{R: gray, G: gray, B: gray})
	}
	for r := 0; r < 256; r += 51 {
		for g := 0; g < 256; g += 51 {
			for b := 0; b < 256; b += 51 {
				backgrounds = append(backgrounds, tui.RgbColor{R: r, G: g, B: b})
			}
		}
	}
	for _, mode := range []ColorMode{TrueColor, Color256} {
		native := terminalTheme(mode)
		for _, bg := range backgrounds {
			native.SetTerminalBackground(bg)
			colors := native.Colors()
			if mode == Color256 {
				for role := range colors {
					ansi, _ := native.ForegroundANSI(role)
					layer := 38
					if backgroundTokens[role] {
						ansi, _ = native.BackgroundANSI(role)
						layer = 48
					}
					var index int
					if _, err := fmt.Sscanf(ansi, fmt.Sprintf("\x1b[%d;5;%%dm", layer), &index); err == nil {
						colors[role] = themefile.ANSI256ToHex(index)
					}
				}
			}
			for _, surface := range []string{fmt.Sprintf("#%02x%02x%02x", bg.R, bg.G, bg.B), colors["toolPendingBg"], colors["selectedBg"]} {
				for _, role := range []string{"text", "muted", "accent", "success", "error", "warning"} {
					a, b := luminanceHex(colors[role]), luminanceHex(surface)
					if ratio := (max(a, b) + .05) / (min(a, b) + .05); ratio < 4.5 {
						t.Fatalf("mode=%s background=%+v %s=%s on %s: contrast %.3f", mode, bg, role, colors[role], surface, ratio)
					}
				}
			}
		}
	}

}

func TestThemePairsFollowAppearanceWithoutOverridingExplicitChoices(t *testing.T) {
	previous := Current()
	t.Cleanup(func() { SetCurrent(previous) })
	registry := Load(LoadOptions{NoThemes: true})
	changes := 0
	controller := NewController(registry, "light/dark", Dark, func() { changes++ })
	for _, appearance := range []TerminalTheme{Light, Dark, Light} {
		controller.SetTerminalAppearance(appearance)
		if controller.Name() != string(appearance) {
			t.Fatalf("appearance=%s theme=%s", appearance, controller.Name())
		}
		controller.SetTerminalAppearance(appearance)
	}
	if changes != 4 {
		t.Fatalf("unchanged reports triggered rebuilds: %d", changes)
	}
	if err := controller.Set("dark"); err != nil {
		t.Fatal(err)
	}
	controller.SetTerminalAppearance(Light)
	if controller.Name() != "dark" {
		t.Fatal("appearance replaced explicit dark theme")
	}
	if err := controller.Set("light/dark"); err != nil {
		t.Fatal(err)
	}
	if controller.Name() != "light" {
		t.Fatal("pair did not resolve using the latest appearance")
	}
	instance, _ := registry.Get("dark")
	if err := controller.SetInstance(instance); err != nil {
		t.Fatal(err)
	}
	controller.SetTerminalAppearance(Dark)
	controller.SetTerminalAppearance(Light)
	if controller.Current() != instance || controller.Name() != "<in-memory>" {
		t.Fatal("appearance replaced extension theme")
	}
}

func TestThemeControllerConcurrentAppearanceAndSelection(t *testing.T) {
	previous := Current()
	t.Cleanup(func() { SetCurrent(previous) })
	controller := NewController(Load(LoadOptions{NoThemes: true}), "light/dark", Dark, nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				controller.SetTerminalAppearance(Light)
				_ = controller.Set("light/dark")
				controller.SetTerminalAppearance(Dark)
				_ = controller.Current().Colors()
				_ = controller.Name()
			}
		})
	}
	wg.Wait()
}
