package themefile

import "testing"

// Expected values come from upstream v1.0.0 parseColor/colorToHex.
func TestNormalizeColorMatchesUpstream(t *testing.T) {
	for input, want := range map[string]string{
		"okhsl(262 14% 16%)":      "#21252c",
		"okhsl(53 51% 24%)":       "#4e2f1b",
		"oklch(70% 0.15 150)":     "#4cb86a",
		"oklch(100% 0.3 150)":     "#ffffff",
		"okhsl(0 0% 50%)":         "#777777",
		"#abc":                    "#aabbcc",
		"okhsl(200 90% 60%)":      "#1ca4aa",
		"okhsl(10 100% 40%)":      "#af0045",
		"#A1B2C3":                 "#A1B2C3",
		"OKHSL(262deg 0.14 0.16)": "#21252c",
	} {
		got, err := normalizeColor(input)
		if err != nil || got != want {
			t.Errorf("normalizeColor(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"okhsl(1 2)", "oklch(150% 0.1 20)", "rgb(1,2,3)"} {
		if got, err := normalizeColor(input); err == nil {
			t.Errorf("normalizeColor(%q) = %q, want an error", input, got)
		}
	}
}
